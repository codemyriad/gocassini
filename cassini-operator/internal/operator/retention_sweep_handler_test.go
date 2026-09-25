package operator

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRetentionManualSweep(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	id := "manual"
	seedRetentionJob(t, rt, id)
	rt.cfg.BasePath = "/operator"
	rt.cfg.APIToken = "test-token"
	h := newHTTPHandler(rt.logger, rt, ExAppConfig{})
	call := func(method, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/operator/storage/retention/sweep", nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := call("POST", ""); w.Code != 401 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("GET", "test-token"); w.Code != 405 || w.Header().Get("Allow") != "POST" {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("POST", "test-token"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	assertExists(t, attemptLogsDir(rt.cfg.WorkRoot, id, 1), "forever preserved")
	rt.retention.settings.Logs = retentionPolicy{Count: 1, Unit: "days"}
	// Active jobs are reserved just as they are in a scheduled pass.
	if _, err := rt.store.db.Exec(`UPDATE jobs SET stage='build',state='queued' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if w := call("POST", "test-token"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	assertExists(t, attemptLogsDir(rt.cfg.WorkRoot, id, 1), "active job preserved")
	if _, err := rt.store.db.Exec(`UPDATE jobs SET stage='done',state='failed' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	rt.retentionSweepMu.Lock()
	w := call("POST", "test-token")
	err := rt.runRetentionSweep(context.Background(), time.Now())
	rt.retentionSweepMu.Unlock()
	if w.Code != 409 || !errors.Is(err, errRetentionSweepBusy) {
		t.Fatal("shared admission guard", w.Code, err)
	}
	if w := call("POST", "test-token"); w.Code != 200 || !strings.Contains(w.Body.String(), "completed") {
		t.Fatal(w.Code, w.Body.String())
	}
	assertGone(t, attemptLogsDir(rt.cfg.WorkRoot, id, 1), "manual expiry completed before response")
	assertExists(t, attemptRunPath(rt.cfg.WorkRoot, id, 1), "other categories still forever")
	if w := call("POST", "test-token"); w.Code != 200 {
		t.Fatal("repeat sweep", w.Code)
	}
	select {
	case <-rt.retention.changed:
		t.Fatal("manual sweep changed daily timer")
	default:
	}
	rt.retention.loadErr = errors.New("invalid config")
	if w := call("POST", "test-token"); w.Code != 503 {
		t.Fatal(w.Code)
	}
}

func TestRetentionManualSweepReportsFailure(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	seedRetentionJob(t, rt, "unsafe")
	rt.retention.settings.Logs = retentionPolicy{Count: 1, Unit: "days"}
	if err := os.Symlink(t.TempDir(), filepath.Join(attemptLogsDir(rt.cfg.WorkRoot, "unsafe", 1), "link")); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	rt.retentionSweepHandler(w, httptest.NewRequest("POST", "/storage/retention/sweep", nil))
	if w.Code != 500 {
		t.Fatal(w.Code, w.Body.String())
	}
	assertExists(t, attemptLogsDir(rt.cfg.WorkRoot, "unsafe", 1), "unsafe tree preserved")
}

func TestRetentionManualSweepManifestAdmin(t *testing.T) {
	b, err := os.ReadFile("../../../appinfo/info.xml")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Routes []struct {
			URL    string `xml:"url"`
			Verb   string `xml:"verb"`
			Access string `xml:"access_level"`
		} `xml:"external-app>routes>route"`
	}
	if err := xml.Unmarshal(b, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, r := range manifest.Routes {
		if strings.Contains(r.URL, "retention") && strings.Contains(r.URL, "sweep") {
			if r.Verb != "POST" || r.Access != "ADMIN" {
				t.Fatal(r)
			}
			return
		}
	}
	t.Fatal("manual sweep ADMIN route missing")
}
