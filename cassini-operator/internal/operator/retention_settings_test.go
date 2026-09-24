package operator

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	s.Recordings.Mode = "fine"
	s.Recordings.Fine["audio"] = retentionPolicy{Count: 30, Unit: "days"}
	s.Recordings.Fine["video"] = retentionPolicy{Count: 1, Unit: "months"}
	if s.validate() == nil {
		t.Fatal("31-day month must not outlive 30 days")
	}
	s.Recordings.Fine["video"] = retentionPolicy{Count: 4, Unit: "weeks"}
	if err := s.validate(); err != nil {
		t.Fatal(err)
	}
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
	if loaded.loadErr != nil || loaded.settings.Logs != s.Logs || loaded.settings.Revision != 1 {
		t.Fatal(loaded)
	}
	if err := os.WriteFile(path, []byte(`{broken`), 0600); err != nil {
		t.Fatal(err)
	}
	if newRetentionConfig(path).loadErr == nil {
		t.Fatal("invalid config must disable expiry")
	}
}
