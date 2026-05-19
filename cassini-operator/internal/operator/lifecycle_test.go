package operator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func newTestHandlers(t *testing.T) (*LifecycleHandlers, string) {
	t.Helper()
	statePath := filepath.Join(t.TempDir(), "app-state.json")
	return &LifecycleHandlers{
		Store: NewFileLifecycleStore(statePath),
		NowUTC: func() string {
			return "2026-01-01T00:00:00Z"
		},
	}, statePath
}

func putEnabled(t *testing.T, h *LifecycleHandlers, query string, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.Register(mux)

	url := "/enabled"
	if query != "" {
		url += "?" + query
	}
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	var r *http.Request
	if reader != nil {
		r = httptest.NewRequest(http.MethodPut, url, reader)
	} else {
		r = httptest.NewRequest(http.MethodPut, url, nil)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func postInit(t *testing.T, h *LifecycleHandlers) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.Register(mux)

	r := httptest.NewRequest(http.MethodPost, "/init", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestEnabledTrue(t *testing.T) {
	h, statePath := newTestHandlers(t)

	w := putEnabled(t, h, "enabled=1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["error"] != "" {
		t.Fatalf("got error %q, want empty", resp["error"])
	}

	// State persisted to disk.
	store := NewFileLifecycleStore(statePath)
	st, err := store.Load()
	if err != nil {
		t.Fatalf("reload state: %v", err)
	}
	if !st.Enabled {
		t.Fatalf("state.Enabled = false, want true")
	}
}

func TestEnabledFalse(t *testing.T) {
	h, statePath := newTestHandlers(t)
	// First enable, then disable, to prove state transitions.
	_ = putEnabled(t, h, "enabled=1", "")
	w := putEnabled(t, h, "enabled=0", "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}

	store := NewFileLifecycleStore(statePath)
	st, err := store.Load()
	if err != nil {
		t.Fatalf("reload state: %v", err)
	}
	if st.Enabled {
		t.Fatalf("state.Enabled = true, want false")
	}
}

func TestEnabledFromJSONBody(t *testing.T) {
	h, _ := newTestHandlers(t)
	// Some AppAPI versions POST the flag as JSON body rather than query param.
	w := putEnabled(t, h, "", `{"enabled": true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestEnabledMissingFlag(t *testing.T) {
	h, _ := newTestHandlers(t)
	w := putEnabled(t, h, "", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", w.Code)
	}
}

func TestEnabledIdempotent(t *testing.T) {
	h, statePath := newTestHandlers(t)
	for i := 0; i < 3; i++ {
		w := putEnabled(t, h, "enabled=1", "")
		if w.Code != http.StatusOK {
			t.Fatalf("attempt %d: got %d, want 200", i, w.Code)
		}
	}
	store := NewFileLifecycleStore(statePath)
	st, err := store.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !st.Enabled {
		t.Fatalf("state not persisted after idempotent calls")
	}
}

func TestEnabledStateSurvivesRestart(t *testing.T) {
	// REGRESSION: lifecycle state on disk must be re-readable by a fresh
	// handler instance — proves the JSON file is the source of truth.
	statePath := filepath.Join(t.TempDir(), "app-state.json")
	h1 := &LifecycleHandlers{
		Store:  NewFileLifecycleStore(statePath),
		NowUTC: func() string { return "2026-01-01T00:00:00Z" },
	}
	w := putEnabled(t, h1, "enabled=1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}

	// Simulate container restart by constructing a new handler over the
	// same on-disk state.
	h2 := &LifecycleHandlers{
		Store:  NewFileLifecycleStore(statePath),
		NowUTC: func() string { return "2026-01-01T00:00:01Z" },
	}
	st, err := h2.Store.Load()
	if err != nil {
		t.Fatalf("h2 load: %v", err)
	}
	if !st.Enabled {
		t.Fatalf("state lost across restart: got %+v", st)
	}
}

func TestInitOK(t *testing.T) {
	h, _ := newTestHandlers(t)
	w := postInit(t, h)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	if strings.TrimSpace(w.Body.String()) != "{}" {
		t.Fatalf("got body %q, want {}", w.Body.String())
	}
}

func TestInitIdempotent(t *testing.T) {
	h, statePath := newTestHandlers(t)
	for i := 0; i < 3; i++ {
		w := postInit(t, h)
		if w.Code != http.StatusOK {
			t.Fatalf("attempt %d: got %d, want 200", i, w.Code)
		}
	}
	// LastInitAt updated, Enabled unchanged.
	store := NewFileLifecycleStore(statePath)
	st, err := store.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if st.LastInitAt == "" {
		t.Fatalf("LastInitAt not persisted")
	}
}

func TestEnabledRejectsGET(t *testing.T) {
	h, _ := newTestHandlers(t)
	mux := http.NewServeMux()
	h.Register(mux)

	r := httptest.NewRequest(http.MethodGet, "/enabled", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got %d, want 405", w.Code)
	}
}

func TestInitRejectsGET(t *testing.T) {
	h, _ := newTestHandlers(t)
	mux := http.NewServeMux()
	h.Register(mux)

	r := httptest.NewRequest(http.MethodGet, "/init", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got %d, want 405", w.Code)
	}
}
