package operator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestHandlers(t *testing.T) *LifecycleHandlers {
	t.Helper()
	return &LifecycleHandlers{}
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
	h := newTestHandlers(t)

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
}

func TestEnabledFalse(t *testing.T) {
	h := newTestHandlers(t)
	_ = putEnabled(t, h, "enabled=1", "")
	w := putEnabled(t, h, "enabled=0", "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
}

func TestEnabledFromJSONBody(t *testing.T) {
	h := newTestHandlers(t)
	// Some AppAPI versions POST the flag as JSON body rather than query param.
	w := putEnabled(t, h, "", `{"enabled": true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestEnabledMissingFlag(t *testing.T) {
	h := newTestHandlers(t)
	w := putEnabled(t, h, "", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", w.Code)
	}
}

func TestEnabledIdempotent(t *testing.T) {
	h := newTestHandlers(t)
	for i := 0; i < 3; i++ {
		w := putEnabled(t, h, "enabled=1", "")
		if w.Code != http.StatusOK {
			t.Fatalf("attempt %d: got %d, want 200", i, w.Code)
		}
	}
}

func TestInitOK(t *testing.T) {
	h := newTestHandlers(t)
	w := postInit(t, h)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	if strings.TrimSpace(w.Body.String()) != "{}" {
		t.Fatalf("got body %q, want {}", w.Body.String())
	}
}

func TestInitIdempotent(t *testing.T) {
	h := newTestHandlers(t)
	for i := 0; i < 3; i++ {
		w := postInit(t, h)
		if w.Code != http.StatusOK {
			t.Fatalf("attempt %d: got %d, want 200", i, w.Code)
		}
	}
}

func TestEnabledTrueTriggersUIRegistrar(t *testing.T) {
	h := newTestHandlers(t)
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
	h := newTestHandlers(t)
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
	h := newTestHandlers(t)
	called := make(chan bool, 2)
	h.EnabledCallback = func(enabled bool) { called <- enabled }

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
	h := newTestHandlers(t)
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
	h := newTestHandlers(t)
	mux := http.NewServeMux()
	h.Register(mux)

	r := httptest.NewRequest(http.MethodGet, "/init", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got %d, want 405", w.Code)
	}
}
