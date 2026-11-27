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

// How many imports run at once, and the largest file one may be, are an
// administrator's settings (#508), changed from Settings › Storage › Imports
// and applied at once. They live in import-settings.json on the persistent
// volume, so a redeploy keeps them; without the file the defaults apply.
const (
	importSettingsFileName = "import-settings.json"
	// maxConcurrentImportsLimit bounds the setting. Each running import holds
	// a `cassini publish` process and up to twice its file on the work disk;
	// more than this many at once is never what a server wants.
	maxConcurrentImportsLimit = 10
	// maxImportFileMiBLimit is the largest file size an administrator can
	// allow, and the default: maxImportUploadBytes, in MiB. The setting can
	// only lower it.
	maxImportFileMiBLimit = int(maxImportUploadBytes >> 20)
)

type importSettings struct {
	MaxConcurrentImports int `json:"max_concurrent_imports"`
	MaxFileMiB           int `json:"max_file_mib"`
}

// savedImportSettings is the file as written; MaxFileMiB is absent from files
// saved before #508, and then the default.
type savedImportSettings struct {
	MaxConcurrentImports int  `json:"max_concurrent_imports"`
	MaxFileMiB           *int `json:"max_file_mib"`
}

func defaultImportSettings() importSettings {
	return importSettings{MaxConcurrentImports: defaultMaxConcurrentImports, MaxFileMiB: maxImportFileMiBLimit}
}

type importSettingsResponse struct {
	MaxConcurrentImports int `json:"max_concurrent_imports"`
	Default              int `json:"default"`
	Max                  int `json:"max"`
	MaxFileMiB           int `json:"max_file_mib"`
	MaxFileMiBLimit      int `json:"max_file_mib_limit"`
}

func importSettingsPath(cfg Config) string {
	return filepath.Join(filepath.Dir(cfg.DBPath), importSettingsFileName)
}

// loadImportSettings reads the saved settings, or gives the defaults without
// them. A file that cannot be read or holds a value out of bounds is kept as
// import-settings.json.rejected-N, logged and passed over for the defaults,
// rather than stopping imports.
func loadImportSettings(path string, logger *log.Logger) importSettings {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return defaultImportSettings()
	}
	var saved savedImportSettings
	if err == nil {
		err = json.Unmarshal(raw, &saved)
	}
	s := importSettings{MaxConcurrentImports: saved.MaxConcurrentImports, MaxFileMiB: maxImportFileMiBLimit}
	if saved.MaxFileMiB != nil {
		s.MaxFileMiB = *saved.MaxFileMiB
	}
	if err == nil {
		err = s.valid()
	}
	if err != nil {
		if logger != nil {
			logger.Print(settingsLoadFailedMessage("import_settings", path, fmt.Sprintf("running up to %d imports at once, of up to %d MiB each, the defaults", defaultMaxConcurrentImports, maxImportFileMiBLimit), "Settings › Storage › Imports", err))
		}
		return defaultImportSettings()
	}
	return s
}

func (s importSettings) valid() error {
	if s.MaxConcurrentImports < 1 || s.MaxConcurrentImports > maxConcurrentImportsLimit {
		return fmt.Errorf("max_concurrent_imports must be between 1 and %d (got %d)", maxConcurrentImportsLimit, s.MaxConcurrentImports)
	}
	if s.MaxFileMiB < 1 || s.MaxFileMiB > maxImportFileMiBLimit {
		return fmt.Errorf("max_file_mib must be between 1 and %d (got %d)", maxImportFileMiBLimit, s.MaxFileMiB)
	}
	return nil
}

// importMaxFileBytes is the largest file a new import may be: the
// administrator's setting, applied when an import is asked for. Imports
// already accepted keep going if it is lowered.
func (rt *Runtime) importMaxFileBytes() int64 {
	if n := rt.importMaxFileMiB.Load(); n > 0 {
		return n << 20
	}
	return maxImportUploadBytes
}

// importSettingsHandler serves GET and PUT operator/settings/imports (ADMIN at
// the proxy, under ^operator\/settings). A PUT is saved first and applied
// after: a change that cannot be kept is never applied. On a server that does
// not serve imports it answers 404, and the panel hides the setting.
func (rt *Runtime) importSettingsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		writeMethodNotAllowed(w, http.MethodGet+", "+http.MethodPut)
		return
	}
	if !rt.importsServed || rt.importSlots == nil {
		writeJSONError(w, http.StatusNotFound, "imports are not available on this server")
		return
	}
	rt.importSettingsMu.Lock()
	defer rt.importSettingsMu.Unlock()
	if r.Method == http.MethodPut {
		// Either value may be sent; the other keeps its current value.
		var in struct {
			MaxConcurrentImports *int `json:"max_concurrent_imports"`
			MaxFileMiB           *int `json:"max_file_mib"`
		}
		dec := json.NewDecoder(io.LimitReader(r.Body, 1<<16))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil || (in.MaxConcurrentImports == nil && in.MaxFileMiB == nil) {
			writeJSONError(w, http.StatusBadRequest, "send {\"max_concurrent_imports\": <number>, \"max_file_mib\": <number>}")
			return
		}
		next := rt.currentImportSettings()
		if in.MaxConcurrentImports != nil {
			next.MaxConcurrentImports = *in.MaxConcurrentImports
		}
		if in.MaxFileMiB != nil {
			next.MaxFileMiB = *in.MaxFileMiB
		}
		if err := next.valid(); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		data, err := json.MarshalIndent(next, "", "  ")
		if err == nil {
			err = writeFileAtomic(rt.importSettingsPath, append(data, '\n'), 0o600)
		}
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("save import settings: %v", err))
			return
		}
		rt.applyImportSettings(next)
		if rt.logger != nil {
			rt.logger.Printf("imports: an administrator set up to %d to run at once, of up to %d MiB each", next.MaxConcurrentImports, next.MaxFileMiB)
		}
	}
	current := rt.currentImportSettings()
	writeJSON(w, http.StatusOK, importSettingsResponse{
		MaxConcurrentImports: current.MaxConcurrentImports,
		Default:              defaultMaxConcurrentImports,
		Max:                  maxConcurrentImportsLimit,
		MaxFileMiB:           current.MaxFileMiB,
		MaxFileMiBLimit:      maxImportFileMiBLimit,
	})
}

func (rt *Runtime) currentImportSettings() importSettings {
	return importSettings{MaxConcurrentImports: rt.importSlots.limitNow(), MaxFileMiB: int(rt.importMaxFileBytes() >> 20)}
}

func (rt *Runtime) applyImportSettings(s importSettings) {
	rt.importSlots.setLimit(s.MaxConcurrentImports)
	rt.importMaxFileMiB.Store(int64(s.MaxFileMiB))
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
