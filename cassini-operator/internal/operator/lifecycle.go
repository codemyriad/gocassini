package operator

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// AppAPI lifecycle callbacks. These are invoked by AppAPI directly (not by
// browser traffic through the proxy) but they still pass through the AppAPI
// auth middleware because AppAPI signs them with AUTHORIZATION-APP-API.
//
//   PUT  /enabled?enabled=1   admin enabled the app, return {"error": ""}
//   PUT  /enabled?enabled=0   admin disabled the app, return {"error": ""}
//   POST /init                run any first-time setup, return {}
//
// We persist the enabled flag in a small JSON file alongside the operator DB
// so it survives container restarts. Re-runs of either endpoint are idempotent.

type LifecycleState struct {
	Enabled    bool   `json:"enabled"`
	LastInitAt string `json:"last_init_at,omitempty"`
}

// LifecycleStore is the minimal persistence backend the lifecycle handlers need.
// Implementations must be safe for concurrent calls (PUT /enabled and POST /init
// can race when an admin clicks fast).
type LifecycleStore interface {
	Load() (LifecycleState, error)
	Save(LifecycleState) error
	// Update applies fn to the stored state as one atomic read-modify-write.
	// The handlers must use it instead of separate Load/Save calls, whose
	// distinct lock scopes let concurrent handlers lose updates (D-364).
	Update(fn func(*LifecycleState)) error
}

type fileLifecycleStore struct {
	path string
	mu   sync.Mutex
}

// NewFileLifecycleStore returns a LifecycleStore that persists to a JSON file.
// The file's parent directory must already exist.
func NewFileLifecycleStore(path string) LifecycleStore {
	return &fileLifecycleStore{path: path}
}

func (s *fileLifecycleStore) Load() (LifecycleState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *fileLifecycleStore) Save(state LifecycleState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(state)
}

func (s *fileLifecycleStore) Update(fn func(*LifecycleState)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadLocked()
	if err != nil {
		return err
	}
	fn(&state)
	return s.saveLocked(state)
}

func (s *fileLifecycleStore) loadLocked() (LifecycleState, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return LifecycleState{}, nil
		}
		return LifecycleState{}, fmt.Errorf("read lifecycle state %s: %w", s.path, err)
	}
	var st LifecycleState
	if len(raw) == 0 {
		return st, nil
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return LifecycleState{}, fmt.Errorf("decode lifecycle state %s: %w", s.path, err)
	}
	return st, nil
}

func (s *fileLifecycleStore) saveLocked(state LifecycleState) error {
	payload, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode lifecycle state: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return fmt.Errorf("write lifecycle state %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("rename lifecycle state to %s: %w", s.path, err)
	}
	return nil
}

// LifecycleHandlers wires PUT /enabled and POST /init onto a mux.
type LifecycleHandlers struct {
	Store  LifecycleStore
	Logger *log.Logger
	NowUTC func() string
	// InitProgressReporter is called once /init has run, in a goroutine so
	// the HTTP response can return immediately. It signals progress=100 to
	// AppAPI via the OCS endpoint. Nil disables the callback (dev / tests).
	InitProgressReporter func()
	// UIRegistrar is called after PUT /enabled?enabled=1 has been persisted,
	// in a goroutine for the same reason as InitProgressReporter: it calls
	// back into Nextcloud (AppAPI's ExApp UI OCS API, see uiRegistrar in
	// exapp.go), which can deadlock a single-worker PHP setup if done before
	// the /enabled response is written. Nil disables it (dev / tests).
	UIRegistrar func()
	// EnabledCallback runs asynchronously after an enabled/disabled state was
	// persisted and answered. ExApp integrations use the enabled=true edge for
	// callbacks that AppAPI would reject while registration is still disabled.
	EnabledCallback func(bool)
}

func (h *LifecycleHandlers) logger() *log.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return log.Default()
}

func (h *LifecycleHandlers) now() string {
	if h.NowUTC != nil {
		return h.NowUTC()
	}
	return nowUTCString()
}

// Register attaches the lifecycle endpoints to the given mux at the root.
// AppAPI calls these on the container itself, NOT through the operator's
// CASSINI_OPERATOR_BASE_PATH prefix.
func (h *LifecycleHandlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("/enabled", h.handleEnabled)
	mux.HandleFunc("/init", h.handleInit)
}

func (h *LifecycleHandlers) handleEnabled(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", http.MethodPut)
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	enabledRaw := strings.TrimSpace(r.URL.Query().Get("enabled"))
	if enabledRaw == "" {
		// AppAPI normally sends `?enabled=1` or `?enabled=0`, but some
		// versions post the flag as a JSON body. Accept either.
		var body struct {
			Enabled any `json:"enabled"`
		}
		if r.Body != nil {
			defer r.Body.Close()
			raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
			if err == nil && len(raw) > 0 {
				_ = json.Unmarshal(raw, &body)
				enabledRaw = fmt.Sprintf("%v", body.Enabled)
			}
		}
	}

	enabled, err := parseEnabledFlag(enabledRaw)
	if err != nil {
		h.logger().Printf("lifecycle enabled: bad flag %q: %v", enabledRaw, err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	if err := h.Store.Update(func(st *LifecycleState) { st.Enabled = enabled }); err != nil {
		h.logger().Printf("lifecycle enabled: update state: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save state failed"})
		return
	}
	h.logger().Printf("lifecycle enabled -> %v", enabled)
	// AppAPI expects `{"error": ""}` on success, `{"error": "..."}` to refuse.
	writeJSON(w, http.StatusOK, map[string]string{"error": ""})
	// AppAPI's lifecycle docs have ExApps register their UI (top-menu
	// entries) "during the enabled method call". Registration is an upsert,
	// so re-enabling just refreshes it; on disable AppAPI hides the entries
	// itself, so there is nothing to undo.
	if enabled && h.UIRegistrar != nil {
		go h.UIRegistrar()
	}
	if h.EnabledCallback != nil {
		go h.EnabledCallback(enabled)
	}
}

func (h *LifecycleHandlers) handleInit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if err := h.Store.Update(func(st *LifecycleState) { st.LastInitAt = h.now() }); err != nil {
		h.logger().Printf("lifecycle init: update state: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save state failed"})
		return
	}
	h.logger().Printf("lifecycle init: ok")
	// AppAPI doesn't require a specific body shape on init success — empty is fine.
	writeJSON(w, http.StatusOK, struct{}{})
	// AppAPI's --wait-finish polls until init_progress=100; the container has
	// to report that back via OCS. We have no async setup work, so signal 100
	// immediately, in a goroutine so the response above isn't blocked.
	if h.InitProgressReporter != nil {
		go h.InitProgressReporter()
	}
}

func parseEnabledFlag(raw string) (bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false, errors.New("missing enabled flag")
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	}
	// fall back to strconv for any other boolean-like input
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("invalid enabled flag %q", raw)
	}
	return b, nil
}

// DefaultLifecycleStatePath returns the canonical on-disk path for the
// lifecycle state file, derived from the operator's DB path.
func DefaultLifecycleStatePath(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), "app-state.json")
}
