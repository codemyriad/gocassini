package operator

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRetentionScheduleNextSweep(t *testing.T) {
	for _, tc := range []struct{ name, now, clock, zone, want string }{
		{"default", "2026-09-25T01:00:00Z", "02:00", "UTC", "2026-09-25T02:00:00Z"},
		{"at deadline", "2026-09-25T02:00:00Z", "02:00", "UTC", "2026-09-26T02:00:00Z"},
		{"local day", "2026-09-25T22:00:00Z", "02:00", "Europe/Zagreb", "2026-09-26T00:00:00Z"},
		{"fractional offset", "2026-09-25T00:00:00Z", "06:15", "Asia/Kathmandu", "2026-09-25T00:30:00Z"},
		{"spring gap", "2026-03-28T23:00:00Z", "02:30", "Europe/Zagreb", "2026-03-29T01:00:00Z"},
		{"fall first occurrence", "2026-10-24T22:00:00Z", "02:30", "Europe/Zagreb", "2026-10-25T00:30:00Z"},
		{"fall no second occurrence", "2026-10-25T00:45:00Z", "02:30", "Europe/Zagreb", "2026-10-26T01:30:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now, _ := time.Parse(time.RFC3339, tc.now)
			got := nextRetentionSweep(now, retentionSchedule{Time: tc.clock, Timezone: tc.zone})
			if got.UTC().Format(time.RFC3339) != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestRetentionScheduleSaveAndDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retention.json")
	// Older settings files acquire the original 02:00 UTC schedule.
	data, _ := json.Marshal(defaultRetentionSettings())
	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(data, &legacy); err != nil {
		t.Fatal(err)
	}
	delete(legacy, "schedule")
	data, _ = json.Marshal(legacy)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	c := newRetentionConfig(path)
	if c.loadErr != nil || c.settings.Schedule != defaultRetentionSchedule() {
		t.Fatal("missing default", c.loadErr)
	}
	rt := &Runtime{retention: c}
	put := func(schedule retentionSchedule) int {
		s := c.settings
		s.Schedule = schedule
		data, _ := json.Marshal(s)
		r := httptest.NewRequest(http.MethodPut, "/storage/retention", bytes.NewReader(data))
		r.Header.Set("If-Match", `"0"`)
		w := httptest.NewRecorder()
		rt.retentionHandler(w, r)
		return w.Code
	}
	for _, invalid := range []retentionSchedule{
		{Time: "24:00", Timezone: "UTC"}, {Time: "2:00", Timezone: "UTC"},
		{Time: "02:00:30", Timezone: "UTC"}, {Time: "02:00", Timezone: "Local"},
		{Time: "02:00", Timezone: ""}, {Time: "02:00", Timezone: "Invalid/Zone"},
	} {
		if got := put(invalid); got != 400 {
			t.Fatalf("accepted %+v: %d", invalid, got)
		}
	}
	select {
	case <-c.changed:
		t.Fatal("invalid save woke scheduler")
	default:
	}
	schedule := retentionSchedule{Time: "15:45", Timezone: "Europe/Zagreb"}
	if got := put(schedule); got != 200 {
		t.Fatalf("save: %d", got)
	}
	select {
	case <-c.changed:
	default:
		t.Fatal("save did not wake scheduler")
	}
	loaded := newRetentionConfig(path)
	if loaded.loadErr != nil || loaded.settings.Schedule != schedule {
		t.Fatal("schedule not persisted", loaded.loadErr)
	}
}
