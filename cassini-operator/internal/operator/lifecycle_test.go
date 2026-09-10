package operator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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

func TestEnabledTrueTriggersUIRegistrar(t *testing.T) {
	h, _ := newTestHandlers(t)
	called := make(chan struct{}, 1)
	h.UIRegistrar = func() { called <- struct{}{} }

	w := putEnabled(t, h, "enabled=1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("UIRegistrar not called after enabled=1")
	}
}

func TestEnabledFalseDoesNotTriggerUIRegistrar(t *testing.T) {
	h, _ := newTestHandlers(t)
	called := make(chan struct{}, 1)
	h.UIRegistrar = func() { called <- struct{}{} }

	w := putEnabled(t, h, "enabled=0", "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	select {
	case <-called:
		t.Fatal("UIRegistrar called on enabled=0; UI registration belongs to enable only")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestEnabledCallbackReceivesPersistedState(t *testing.T) {
	h, _ := newTestHandlers(t)
	called := make(chan bool, 2)
	h.EnabledCallback = func(enabled bool, _ uint64) { called <- enabled }

	for _, want := range []bool{true, false} {
		query := "enabled=0"
		if want {
			query = "enabled=1"
		}
		w := putEnabled(t, h, query, "")
		if w.Code != http.StatusOK {
			t.Fatalf("got %d, want 200; body=%s", w.Code, w.Body.String())
		}
		select {
		case got := <-called:
			if got != want {
				t.Fatalf("EnabledCallback(%v), want %v", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("EnabledCallback not called for enabled=%v", want)
		}
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

// Neither edge may call back into Nextcloud before the response is written.
//
// The callback POSTs into Nextcloud, and doing that while Nextcloud is still
// blocked serving this request deadlocks a single-worker PHP setup — the same
// reason UIRegistrar is deferred (see its doc comment). An earlier attempt to
// fix the disable-edge race by running the callback first reintroduced exactly
// that deadlock, so this asserts the ordering in both directions.
func TestLifecycleCallbacksNeverRunBeforeTheResponse(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		name := "disable edge"
		query := "enabled=0"
		if enabled {
			name, query = "enable edge", "enabled=1"
		}
		t.Run(name, func(t *testing.T) {
			h, _ := newTestHandlers(t)
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			h.EnabledCallback = func(bool, uint64) {
				entered <- struct{}{}
				<-release
			}
			defer close(release)

			done := make(chan int, 1)
			go func() { done <- putEnabled(t, h, query, "").Code }()

			select {
			case code := <-done:
				if code != http.StatusOK {
					t.Fatalf("got %d, want 200", code)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("the response waited on the callback; that deadlocks single-worker PHP")
			}

			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("EnabledCallback was never called")
			}
		})
	}
}

// Shutdown has to be able to wait for a callback still in flight. On the
// disable edge that callback is the write telling Nextcloud the ExApp's
// settings are no longer live, and AppAPI stops the container as soon as the
// response returns — so without this the write is simply lost.
func TestBackgroundTracksAnInFlightCallback(t *testing.T) {
	h, _ := newTestHandlers(t)
	release := make(chan struct{})
	finished := make(chan struct{})
	h.EnabledCallback = func(bool, uint64) {
		<-release
		close(finished)
	}

	if code := putEnabled(t, h, "enabled=0", "").Code; code != http.StatusOK {
		t.Fatalf("got %d, want 200", code)
	}

	waited := make(chan struct{})
	go func() { h.Background.Wait(); close(waited) }()

	select {
	case <-waited:
		t.Fatal("Background.Wait returned while the callback was still running")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	<-finished
	select {
	case <-waited:
	case <-time.After(2 * time.Second):
		t.Fatal("Background.Wait did not return after the callback finished")
	}
}

// Edge numbers must reflect the order Nextcloud sent the edges in, not the
// order the callback goroutines happened to run. Claiming the number inside
// the goroutine would let a delayed enable out-rank a later disable and put a
// stale "enabled" back into Nextcloud.
func TestLifecycleEdgesAreNumberedInArrivalOrder(t *testing.T) {
	h, _ := newTestHandlers(t)

	var mu sync.Mutex
	seen := map[bool]uint64{}
	done := make(chan struct{}, 2)
	// Delay the FIRST callback so its goroutine finishes last.
	first := true
	h.EnabledCallback = func(enabled bool, edge uint64) {
		mu.Lock()
		delay := first
		first = false
		seen[enabled] = edge
		mu.Unlock()
		if delay {
			time.Sleep(30 * time.Millisecond)
		}
		done <- struct{}{}
	}

	if code := putEnabled(t, h, "enabled=1", "").Code; code != http.StatusOK {
		t.Fatalf("enable: got %d, want 200", code)
	}
	if code := putEnabled(t, h, "enabled=0", "").Code; code != http.StatusOK {
		t.Fatalf("disable: got %d, want 200", code)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("callbacks did not complete")
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if seen[true] >= seen[false] {
		t.Fatalf("enable edge %d did not precede disable edge %d; a delayed enable can now overwrite a later disable",
			seen[true], seen[false])
	}
}
