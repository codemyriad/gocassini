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

func TestNextcloudPolicyOrderingAndDeadlines(t *testing.T) {
	forever := retentionPolicy{Forever: true}
	days := func(n int) retentionPolicy { return retentionPolicy{Count: n, Unit: "days"} }
	for _, tc := range []struct {
		audio, text retentionPolicy
		valid       bool
		action      string
	}{
		{forever, forever, true, "keep"}, {days(30), forever, true, "convert"}, {days(30), days(90), true, "convert"}, {days(30), days(30), true, "retire"}, {days(90), days(30), false, ""}, {forever, days(90), false, ""},
	} {
		p := nextcloudRetentionSettings{tc.audio, tc.text}
		err := p.validate()
		if (err == nil) != tc.valid {
			t.Fatalf("%+v: %v", p, err)
		}
		if !tc.valid {
			continue
		}
		m := meetingLifecycle{Name: "m.opus", Anchor: "2026-08-01T23:59:59Z", AnchorSource: "recording-completed", State: "active", Representation: "opus"}
		result := evaluateRemoteRetention(m, p, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
		if result.Action != tc.action {
			t.Fatalf("%+v: %+v", p, result)
		}
	}
}

func TestRemotePreviewDoesNotPersistIntent(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	rt.retention = newRetentionConfig(filepath.Join(t.TempDir(), "settings.json"))
	ctx := context.Background()
	m := meetingLifecycle{Name: "m.opus", FileID: 42, Path: ncRecordingsRoot + "/meetings/m.opus", Representation: "opus", State: "active", Anchor: "2020-01-01T00:00:00Z", AnchorSource: "recording-completed"}
	if err := rt.store.adoptMeetingLifecycle(ctx, m); err != nil {
		t.Fatal(err)
	}
	proposed := rt.retention.settings
	proposed.Nextcloud.Recordings = retentionPolicy{Count: 30, Unit: "days"}
	raw, _ := json.Marshal(proposed)
	w := httptest.NewRecorder()
	rt.remoteRetentionPreviewHandler(w, httptest.NewRequest("POST", "/", bytes.NewReader(raw)))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var preview remoteRetentionPreview
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil || preview.Convert != 1 || preview.Retire != 0 {
		t.Fatal("bad preview", err)
	}
	if !rt.retention.settings.Nextcloud.Recordings.Forever {
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
