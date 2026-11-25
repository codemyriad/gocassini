package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// How many imports run at once is an administrator's setting, changed from
// the Settings panel and applied at once, without a redeploy.
//
// The value an administrator saves is the one used, and a redeploy never
// resets it: it lives in import-settings.json on the persistent volume. Until
// one is saved there is no file, and the deploy environment's
// CASSINI_MAX_CONCURRENT_IMPORTS applies, or the default without it. So the
// variable keeps working on a deployment whose administrator never opens the
// panel, and stops mattering the moment one does.
const (
	importSettingsFileName = "import-settings.json"
	// maxConcurrentImportsLimit bounds the setting. Each running import holds
	// a `cassini publish` process and up to twice its file on the work disk;
	// more than this many at once is never what a server wants.
	maxConcurrentImportsLimit = 10

	importLimitSourceAdmin   = "admin"
	importLimitSourceDeploy  = "deploy"
	importLimitSourceDefault = "default"
)

type importSettings struct {
	MaxConcurrentImports int `json:"max_concurrent_imports"`
}

type importSettingsResponse struct {
	MaxConcurrentImports int `json:"max_concurrent_imports"`
	// Source is where the value comes from: "admin" (saved in the panel),
	// "deploy" (CASSINI_MAX_CONCURRENT_IMPORTS) or "default".
	Source  string `json:"source"`
	Default int    `json:"default"`
	Max     int    `json:"max"`
}

func importSettingsPath(cfg Config) string {
	return filepath.Join(filepath.Dir(cfg.DBPath), importSettingsFileName)
}

// loadImportLimit reads the saved setting, or, without one, the deploy
// environment. A file that cannot be read or holds a value out of bounds is
// logged and passed over for the environment, rather than stopping imports.
func loadImportLimit(path string, getenv func(string) string, logger *log.Logger) (int, string) {
	fallback := func() (int, string) {
		n, fromEnv := maxConcurrentImports(getenv, logger)
		if n > maxConcurrentImportsLimit {
			if logger != nil {
				logger.Printf("imports: %s=%d is above the limit of %d; using %d", envMaxConcurrentImports, n, maxConcurrentImportsLimit, maxConcurrentImportsLimit)
			}
			n = maxConcurrentImportsLimit
		}
		if fromEnv {
			return n, importLimitSourceDeploy
		}
		return n, importLimitSourceDefault
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return fallback()
	}
	var s importSettings
	if err == nil {
		err = json.Unmarshal(raw, &s)
	}
	if err == nil {
		err = validMaxConcurrentImports(s.MaxConcurrentImports)
	}
	if err != nil {
		if logger != nil {
			logger.Printf("imports: %s cannot be used (%v); using the deploy environment until an administrator saves the setting again", filepath.Base(path), err)
		}
		return fallback()
	}
	return s.MaxConcurrentImports, importLimitSourceAdmin
}

func validMaxConcurrentImports(n int) error {
	if n < 1 || n > maxConcurrentImportsLimit {
		return fmt.Errorf("max_concurrent_imports must be between 1 and %d (got %d)", maxConcurrentImportsLimit, n)
	}
	return nil
}

// importSettingsHandler serves GET and PUT operator/settings/imports (ADMIN at
// the proxy, under ^operator\/settings). A PUT is saved first and applied
// after: a change that cannot be kept is never applied.
func (rt *Runtime) importSettingsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		writeMethodNotAllowed(w, http.MethodGet+", "+http.MethodPut)
		return
	}
	if rt.importSlots == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "imports are not available on this server")
		return
	}
	rt.importSettingsMu.Lock()
	defer rt.importSettingsMu.Unlock()
	if r.Method == http.MethodPut {
		var in struct {
			MaxConcurrentImports *int `json:"max_concurrent_imports"`
		}
		dec := json.NewDecoder(io.LimitReader(r.Body, 1<<16))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil || in.MaxConcurrentImports == nil {
			writeJSONError(w, http.StatusBadRequest, "send {\"max_concurrent_imports\": <number>}")
			return
		}
		n := *in.MaxConcurrentImports
		if err := validMaxConcurrentImports(n); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		data, err := json.MarshalIndent(importSettings{MaxConcurrentImports: n}, "", "  ")
		if err == nil {
			err = writeFileAtomic(rt.importSettingsPath, append(data, '\n'), 0o600)
		}
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("save import settings: %v", err))
			return
		}
		rt.importLimitSource = importLimitSourceAdmin
		rt.importSlots.setLimit(n)
		if rt.logger != nil {
			rt.logger.Printf("imports: an administrator set up to %d to run at once", n)
		}
	}
	writeJSON(w, http.StatusOK, importSettingsResponse{
		MaxConcurrentImports: rt.importSlots.limitNow(),
		Source:               rt.importLimitSource,
		Default:              defaultMaxConcurrentImports,
		Max:                  maxConcurrentImportsLimit,
	})
}

// importSlotPool bounds how many imports run at once, with a limit that can
// change while they run. Waiters are served in arrival order. Raising the
// limit starts waiters at once; lowering it lets running imports finish, and
// no new one starts until fewer than the new limit run.
type importSlotPool struct {
	mu      sync.Mutex
	limit   int
	used    int
	waiters []chan struct{}
}

func newImportSlotPool(limit int) *importSlotPool {
	return &importSlotPool{limit: limit}
}

// acquire waits for a slot, or for ctx to end. The returned release gives the
// slot back; it must be called exactly once.
func (p *importSlotPool) acquire(ctx context.Context) (func(), error) {
	p.mu.Lock()
	if p.used < p.limit && len(p.waiters) == 0 {
		p.used++
		p.mu.Unlock()
		return p.release, nil
	}
	ready := make(chan struct{})
	p.waiters = append(p.waiters, ready)
	p.mu.Unlock()
	select {
	case <-ready:
		return p.release, nil
	case <-ctx.Done():
		p.mu.Lock()
		for i, w := range p.waiters {
			if w == ready {
				p.waiters = append(p.waiters[:i], p.waiters[i+1:]...)
				p.mu.Unlock()
				return nil, ctx.Err()
			}
		}
		p.mu.Unlock()
		// Granted just as ctx ended: the slot is ours, so give it back.
		p.release()
		return nil, ctx.Err()
	}
}

func (p *importSlotPool) release() {
	p.mu.Lock()
	p.used--
	p.grantLocked()
	p.mu.Unlock()
}

func (p *importSlotPool) setLimit(n int) {
	p.mu.Lock()
	p.limit = n
	p.grantLocked()
	p.mu.Unlock()
}

// grantLocked hands free slots to waiters, oldest first.
func (p *importSlotPool) grantLocked() {
	for p.used < p.limit && len(p.waiters) > 0 {
		ready := p.waiters[0]
		p.waiters = p.waiters[1:]
		p.used++
		close(ready)
	}
}

func (p *importSlotPool) limitNow() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.limit
}

// inUse is how many slots are held: running imports.
func (p *importSlotPool) inUse() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.used
}
