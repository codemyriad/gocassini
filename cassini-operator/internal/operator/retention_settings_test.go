package operator

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRetentionCalendar(t *testing.T) {
	for _, tc := range []struct {
		anchor string
		p      retentionPolicy
		want   string
	}{
		{"2024-01-31", retentionPolicy{Count: 1, Unit: "months"}, "2024-02-29"},
		{"2023-01-31", retentionPolicy{Count: 1, Unit: "months"}, "2023-02-28"},
		{"2024-02-29", retentionPolicy{Count: 12, Unit: "months"}, "2025-02-28"},
		{"2026-09-24", retentionPolicy{Count: 1, Unit: "weeks"}, "2026-10-01"},
	} {
		a, _ := time.Parse("2006-01-02", tc.anchor)
		d := tc.p.deadline(a)
		if d.Format("2006-01-02") != tc.want {
			t.Fatal(d, tc.want)
		}
		if !tc.p.due(a, d) || tc.p.due(a, d.AddDate(0, 0, -1)) {
			t.Fatal("date boundary")
		}
	}
}
func TestRetentionValidation(t *testing.T) {
	s := defaultRetentionSettings()
	if err := s.validate(); err != nil {
		t.Fatal(err)
	}
	s.Recordings = retentionPolicy{Count: 30, Unit: "days"}
	if err := s.validate(); err != nil {
		t.Fatal(err)
	}
	s.Recordings = retentionPolicy{Count: 0, Unit: "days"}
	if s.validate() == nil {
		t.Fatal("invalid recordings policy accepted")
	}
	s.Recordings = retentionPolicy{Forever: true}
	s.Logs = retentionPolicy{Count: 10000, Unit: "days"}
	if s.validate() == nil {
		t.Fatal("bound")
	}
}
func TestRetentionSettingsAPI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retention_settings.json")
	rt := &Runtime{retention: newRetentionConfig(path)}
	s := defaultRetentionSettings()
	s.Logs = retentionPolicy{Count: 2, Unit: "weeks"}
	s.Recordings = retentionPolicy{Count: 1, Unit: "months"}
	data, _ := json.Marshal(s)
	put := func(tag string) int {
		r := httptest.NewRequest("PUT", "/storage/retention", bytes.NewReader(data))
		r.Header.Set("If-Match", tag)
		w := httptest.NewRecorder()
		rt.retentionHandler(w, r)
		return w.Code
	}
	if got := put(`"0"`); got != 200 {
		t.Fatal(got)
	}
	if got := put(`"0"`); got != 412 {
		t.Fatal(got)
	}
	loaded := newRetentionConfig(path)
	if loaded.loadErr != nil || loaded.settings.Recordings != s.Recordings || loaded.settings.Logs != s.Logs || loaded.settings.Revision != 1 {
		t.Fatal(loaded)
	}
	if err := os.WriteFile(path, []byte(`{broken`), 0600); err != nil {
		t.Fatal(err)
	}
	if newRetentionConfig(path).loadErr == nil {
		t.Fatal("invalid config must disable expiry")
	}
}

func TestRetentionRouteIsAdminAndRequiresStandaloneToken(t *testing.T) {
	b, err := os.ReadFile("../../../appinfo/info.xml")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		External struct {
			Routes []struct {
				URL    string `xml:"url"`
				Verb   string `xml:"verb"`
				Access string `xml:"access_level"`
			} `xml:"routes>route"`
		} `xml:"external-app"`
	}
	if err = xml.Unmarshal(b, &manifest); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, route := range manifest.External.Routes {
		if strings.Contains(route.URL, `storage\/retention`) {
			found = true
			if route.Access != "ADMIN" || route.Verb != "GET,PUT" {
				t.Fatal(route)
			}
		}
	}
	if !found {
		t.Fatal("ADMIN manifest route missing")
	}
	rt, close := newBareSealRuntime(t)
	defer close()
	rt.retention = newRetentionConfig(filepath.Join(t.TempDir(), "retention.json"))
	rt.cfg.APIToken = "test-token"
	rt.cfg.BasePath = "/operator"
	h := newHTTPHandler(rt.logger, rt, ExAppConfig{})
	for _, token := range []string{"", "Bearer test-token"} {
		req := httptest.NewRequest(http.MethodGet, "/operator/storage/retention", nil)
		req.Header.Set("Authorization", token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		want := http.StatusUnauthorized
		if token != "" {
			want = 200
		}
		if w.Code != want {
			t.Fatalf("status %d want %d: %s", w.Code, want, w.Body.String())
		}
	}
}

func TestRetentionSaveFailureKeepsActivePolicy(t *testing.T) {
	dir := t.TempDir()
	c := newRetentionConfig(filepath.Join(dir, "settings"))
	rt := &Runtime{retention: c}
	// A regular file cannot serve as a settings directory.
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	c.path = filepath.Join(dir, "file", "settings")
	s := defaultRetentionSettings()
	s.Logs = retentionPolicy{Count: 1, Unit: "days"}
	b, _ := json.Marshal(s)
	r := httptest.NewRequest("PUT", "/storage/retention", bytes.NewReader(b))
	r.Header.Set("If-Match", `"0"`)
	w := httptest.NewRecorder()
	rt.retentionHandler(w, r)
	if w.Code != 500 || !c.settings.Logs.Forever || c.settings.Revision != 0 {
		t.Fatal(w.Code, c.settings)
	}
}

func TestRetentionMigrateRecordingPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, recordings string
		want             retentionPolicy
		invalid          bool
	}{
		{"group", `{"mode":"group","policy":{"forever":false,"count":2,"unit":"months"},"fine":{"audio":{"forever":true},"video":{"forever":true}}}`, retentionPolicy{Count: 2, Unit: "months"}, false},
		{"audio forever", `{"mode":"fine","policy":{"forever":false,"count":1,"unit":"days"},"fine":{"audio":{"forever":true},"video":{"forever":false,"count":1,"unit":"days"}}}`, retentionPolicy{Forever: true}, false},
		{"audio finite", `{"mode":"fine","policy":{"forever":true},"fine":{"audio":{"forever":false,"count":3,"unit":"weeks"},"video":{"forever":false,"count":1,"unit":"days"}}}`, retentionPolicy{Count: 3, Unit: "weeks"}, false},
		{"missing audio", `{"mode":"fine","policy":{"forever":true},"fine":{"video":{"forever":true}}}`, retentionPolicy{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(defaultRetentionSettings())
			if err != nil {
				t.Fatal(err)
			}
			var stored map[string]json.RawMessage
			if err = json.Unmarshal(data, &stored); err != nil {
				t.Fatal(err)
			}
			stored["version"] = json.RawMessage(`1`)
			stored["revision"] = json.RawMessage(`7`)
			stored["recordings"] = json.RawMessage(tc.recordings)
			data, err = json.Marshal(stored)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "retention_settings.json")
			if err = os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			c := newRetentionConfig(path)
			if tc.invalid {
				if c.loadErr == nil {
					t.Fatal("invalid legacy settings enabled expiry")
				}
				return
			}
			if c.loadErr != nil || c.settings.Version != 2 || c.settings.Revision != 7 || c.settings.Recordings != tc.want {
				t.Fatalf("migration: %+v, error %v", c.settings, c.loadErr)
			}
			if err = c.save(c.settings); err != nil {
				t.Fatal(err)
			}
			reloaded := newRetentionConfig(path)
			if reloaded.loadErr != nil || reloaded.settings.Recordings != tc.want {
				t.Fatalf("reload: %+v, error %v", reloaded.settings, reloaded.loadErr)
			}
		})
	}
}
