package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestRemotePreviewDoesNotPersistIntent(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	rt.retention = newRetentionConfig(filepath.Join(t.TempDir(), "settings.json"))
	ctx := context.Background()
	m := meetingLifecycle{Name: "m.opus", FileID: 42, Path: ncRecordingsRoot + "/meetings/m.opus", State: "active", Anchor: "2020-01-01T00:00:00Z", CreatedAtUTC: "2020-01-01T00:00:00Z", AnchorSource: "createdAtUtc"}
	if err := rt.store.adoptMeetingLifecycle(ctx, m); err != nil {
		t.Fatal(err)
	}
	proposed := rt.retention.settings
	proposed.Nextcloud.Meetings = retentionPolicy{Count: 30, Unit: "days"}
	raw, _ := json.Marshal(proposed)
	w := httptest.NewRecorder()
	rt.remoteRetentionPreviewHandler(w, httptest.NewRequest("POST", "/", bytes.NewReader(raw)))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var preview remoteRetentionPreview
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil || preview.Retire != 1 {
		t.Fatal("bad preview", err)
	}
	if !rt.retention.settings.Nextcloud.Meetings.Forever {
		t.Fatal("preview saved settings")
	}
	ops, err := rt.store.pendingRemoteOperations(ctx)
	if err != nil || len(ops) != 0 {
		t.Fatal("preview created operation", err)
	}
	after, _, err := rt.store.meetingLifecycle(ctx, m.Name)
	if err != nil || after != m {
		t.Fatal("preview changed lifecycle", err)
	}
}

func TestWholeMeetingPolicyDeadlines(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	m := meetingLifecycle{Name: "m.opus", State: "active", Anchor: "2026-08-02T23:59:59Z", CreatedAtUTC: "2026-08-02T23:59:59Z", AnchorSource: "createdAtUtc"}
	for _, tc := range []struct {
		policy           retentionPolicy
		valid            bool
		action, deadline string
	}{
		{retentionPolicy{Forever: true}, true, "keep", ""},
		{retentionPolicy{Count: 30, Unit: "days"}, true, "retire", "2026-09-01"},
		{retentionPolicy{Count: 31, Unit: "days"}, true, "keep", "2026-09-02"},
		{retentionPolicy{}, false, "", ""},
		{retentionPolicy{Count: 1, Unit: "months"}, false, "", ""},
		{retentionPolicy{Forever: true, Count: 1}, false, "", ""},
	} {
		p := nextcloudRetentionSettings{Meetings: tc.policy}
		if (p.validate() == nil) != tc.valid {
			t.Fatalf("validation: %+v", p)
		}
		if !tc.valid {
			continue
		}
		got := evaluateRemoteRetention(m, p, now)
		if got.Action != tc.action || got.Deadline != tc.deadline {
			t.Fatalf("%+v: %+v", p, got)
		}
	}
	for _, state := range []string{"retiring", "retired"} {
		m.State = state
		if got := evaluateRemoteRetention(m, defaultNextcloudRetention(), now); got.Action != state {
			t.Fatal(got)
		}
	}
	m.State = "active"
	m.Anchor = "invalid"
	if got := evaluateRemoteRetention(m, defaultNextcloudRetention(), now); got.Action != "skip" {
		t.Fatal(got)
	}
}

func TestPreviewPublishedAgeAndDecision(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		recorded string
		age      int
		decision string
	}{
		{"2026-08-02T23:59:59", 30, "evict"},
		{"", 1, "keep"},
		{"2026-09-02T00:00:00", -1, "keep"},
	} {
		m := meetingLifecycle{Name: "m.opus", State: "active", CreatedAtUTC: "2026-08-31T12:00:00Z", RecordedAtLocal: tc.recorded}
		if err := setMeetingRetentionAge(&m); err != nil {
			t.Fatal(err)
		}
		effect := evaluateRemoteRetention(m, nextcloudRetentionSettings{Meetings: retentionPolicy{Count: 30, Unit: "days"}}, now)
		raw, err := json.Marshal(effect)
		if err != nil {
			t.Fatal(err)
		}
		var row map[string]any
		if err := json.Unmarshal(raw, &row); err != nil {
			t.Fatal(err)
		}
		if row["name"] != "m.opus" || row["createdAtUtc"] != m.CreatedAtUTC || row["recordedAtLocal"] != tc.recorded || row["age"] != float64(tc.age) || row["decision"] != tc.decision {
			t.Fatalf("wrong preview: %s", raw)
		}
	}
}
