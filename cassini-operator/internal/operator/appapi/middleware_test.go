package appapi

import (
	"bytes"
	"encoding/base64"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	testAppID      = "gocassini"
	testAppSecret  = "s3cret-shared-with-appapi"
	testAppVersion = "0.1.0"
)

func newConfig(buf *bytes.Buffer) Config {
	return Config{
		AppID:      testAppID,
		AppVersion: testAppVersion,
		AppSecret:  testAppSecret,
		Logger:     log.New(buf, "test: ", 0),
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if uid := UserID(r.Context()); uid != "" {
			w.Header().Set("X-Test-User", uid)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

func buildRequest(t *testing.T, headerAuthValue string, withVersion, withAppID bool) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/operator/jobs", nil)
	if headerAuthValue != "" {
		r.Header.Set(headerAuth, headerAuthValue)
	}
	if withAppID {
		r.Header.Set(headerExAppID, testAppID)
	}
	if withVersion {
		r.Header.Set(headerExAppVer, testAppVersion)
	}
	r.Header.Set(headerRequestID, "trace-abc")
	r.Header.Set(headerAAVersion, "5.0.0")
	return r
}

func encodeAuth(userID, secret string) string {
	return base64.StdEncoding.EncodeToString([]byte(userID + ":" + secret))
}

func TestMiddlewareValidHeader(t *testing.T) {
	var buf bytes.Buffer
	cfg := newConfig(&buf)
	h := cfg.Middleware(okHandler())

	r := buildRequest(t, encodeAuth("alice", testAppSecret), true, true)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; logs=%q", w.Code, buf.String())
	}
	if got := w.Header().Get("X-Test-User"); got != "alice" {
		t.Fatalf("user id not propagated: got %q want %q", got, "alice")
	}
}

func TestMiddlewareValidHeaderEmptyUserID(t *testing.T) {
	var buf bytes.Buffer
	cfg := newConfig(&buf)
	h := cfg.Middleware(okHandler())

	// Lifecycle callbacks may carry an empty user id.
	r := buildRequest(t, encodeAuth("", testAppSecret), true, true)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; logs=%q", w.Code, buf.String())
	}
	if got := w.Header().Get("X-Test-User"); got != "" {
		t.Fatalf("expected empty user id, got %q", got)
	}
}

func TestMiddlewareMissingHeader(t *testing.T) {
	var buf bytes.Buffer
	cfg := newConfig(&buf)
	h := cfg.Middleware(okHandler())

	r := buildRequest(t, "", true, true)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", w.Code)
	}
}

func TestMiddlewareMalformedBase64(t *testing.T) {
	var buf bytes.Buffer
	cfg := newConfig(&buf)
	h := cfg.Middleware(okHandler())

	r := buildRequest(t, "not-base64!@#$", true, true)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", w.Code)
	}
}

func TestMiddlewareWrongSecret(t *testing.T) {
	var buf bytes.Buffer
	cfg := newConfig(&buf)
	h := cfg.Middleware(okHandler())

	r := buildRequest(t, encodeAuth("alice", "wrong-secret"), true, true)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", w.Code)
	}
}

func TestMiddlewareWrongAppID(t *testing.T) {
	var buf bytes.Buffer
	cfg := newConfig(&buf)
	h := cfg.Middleware(okHandler())

	r := httptest.NewRequest(http.MethodGet, "/operator/jobs", nil)
	r.Header.Set(headerAuth, encodeAuth("alice", testAppSecret))
	r.Header.Set(headerExAppID, "some-other-app")
	r.Header.Set(headerExAppVer, testAppVersion)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", w.Code)
	}
}

func TestMiddlewareWrongAppVersion(t *testing.T) {
	var buf bytes.Buffer
	cfg := newConfig(&buf)
	h := cfg.Middleware(okHandler())

	r := httptest.NewRequest(http.MethodGet, "/operator/jobs", nil)
	r.Header.Set(headerAuth, encodeAuth("alice", testAppSecret))
	r.Header.Set(headerExAppID, testAppID)
	r.Header.Set(headerExAppVer, "9.9.9")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", w.Code)
	}
}

func TestMiddlewareAppVersionUnchecked(t *testing.T) {
	var buf bytes.Buffer
	cfg := newConfig(&buf)
	cfg.AppVersion = "" // explicit opt-out of version check
	h := cfg.Middleware(okHandler())

	r := httptest.NewRequest(http.MethodGet, "/operator/jobs", nil)
	r.Header.Set(headerAuth, encodeAuth("alice", testAppSecret))
	r.Header.Set(headerExAppID, testAppID)
	r.Header.Set(headerExAppVer, "anything-goes")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
}

func TestMiddlewareDisabledWhenNoSecret(t *testing.T) {
	var buf bytes.Buffer
	cfg := Config{AppID: testAppID, Logger: log.New(&buf, "test: ", 0)}
	h := cfg.Middleware(okHandler())

	r := httptest.NewRequest(http.MethodGet, "/operator/jobs", nil)
	// No AppAPI headers — should pass through.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 when middleware is disabled", w.Code)
	}
}

func TestMiddlewareDoesNotLogSecret(t *testing.T) {
	var buf bytes.Buffer
	cfg := newConfig(&buf)
	h := cfg.Middleware(okHandler())

	// Send a malformed auth attempt with a recognizable secret-looking value.
	r := buildRequest(t, encodeAuth("alice", "ALIENATED-SECRET-VALUE-FOR-LOG-SCAN"), true, true)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	logs := buf.String()
	if strings.Contains(logs, "ALIENATED-SECRET-VALUE-FOR-LOG-SCAN") {
		t.Fatalf("middleware leaked attempted secret in logs: %q", logs)
	}
	if strings.Contains(logs, encodeAuth("alice", "ALIENATED-SECRET-VALUE-FOR-LOG-SCAN")) {
		t.Fatalf("middleware leaked AUTHORIZATION-APP-API value in logs: %q", logs)
	}
	if !strings.Contains(logs, "aa_request_id=trace-abc") {
		t.Fatalf("middleware should log AA-REQUEST-ID; got %q", logs)
	}
}

func TestUserIDFromContextEmpty(t *testing.T) {
	if got := UserID(httptest.NewRequest(http.MethodGet, "/", nil).Context()); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}
