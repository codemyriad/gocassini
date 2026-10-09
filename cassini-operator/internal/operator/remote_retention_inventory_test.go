package operator

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestRemoteRetentionUsesPublishedDates(t *testing.T) {
	for _, tc := range []struct {
		name, created, recorded, want, source string
		unpublished, legacy                   bool
	}{
		{name: "creation fallback", created: "2026-03-05T13:38:29Z", want: "2026-03-05T13:38:29Z", source: "createdAtUtc"},
		{name: "recording preferred", created: "2026-09-29T12:00:00Z", recorded: "2026-03-05T23:38:29", want: "2026-03-05T23:38:29Z", source: "recordedAtLocal"},
		{name: "upgrade existing job age", created: "2026-09-29T12:00:00Z", recorded: "2026-03-05T23:38:29", want: "2026-03-05T23:38:29Z", source: "recordedAtLocal", legacy: true},
		{name: "invalid recording date", created: "2026-09-29T12:00:00Z", recorded: "invalid"},
		{name: "invalid required creation", created: "invalid", recorded: "2026-03-05T12:00:00"},
		{name: "unpublished", created: "2026-03-05T12:00:00Z", unpublished: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, close := newBareSealRuntime(t)
			defer close()
			ctx := context.Background()
			insertJob(t, rt.store.db, "m", "2026-09-29T12:00:00Z")
			state := "succeeded"
			if tc.unpublished {
				state = "failed"
			}
			if _, err := rt.store.db.Exec(`UPDATE job_attempts SET stage='done',state=?,record_finished_at='2026-09-29T12:00:00Z' WHERE job_id='m'`, state); err != nil {
				t.Fatal(err)
			}
			metadata, err := openMeetingMetadataStore(filepath.Join(t.TempDir(), "meetings.sqlite3"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer metadata.Close()
			raw, _ := json.Marshal(map[string]string{"audioPath": "./meetings/m.opus", "createdAtUtc": tc.created, "recordedAtLocal": tc.recorded})
			if err := metadata.Put(ctx, 42, "m.opus", raw); err != nil {
				t.Fatal(err)
			}
			service := &annotationService{rt: rt, exapp: ExAppConfig{meetingMetadata: metadata, sharePaths: &recordingSharePathCache{ownerNames: map[int64]string{42: "m.opus"}, ownerExpires: time.Now().Add(time.Hour)}}}
			if tc.legacy {
				if err := rt.store.adoptMeetingLifecycle(ctx, meetingLifecycle{Name: "m.opus", FileID: 42, Path: ncRecordingsRoot + "/meetings/m.opus", State: "active", Anchor: "2026-09-29T12:00:00Z", AnchorSource: "recording-completed"}); err != nil {
					t.Fatal(err)
				}
			}
			meetings, skipped, err := service.retentionInventory(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if len(meetings) != 0 || len(skipped) != 1 || skipped[0].Decision != "keep" {
					t.Fatalf("unsafe inventory: %+v %+v", meetings, skipped)
				}
				return
			}
			if len(meetings) != 1 || len(skipped) != 0 || meetings[0].Anchor != tc.want || meetings[0].AnchorSource != tc.source {
				t.Fatalf("wrong published age: %+v %+v", meetings, skipped)
			}
			// Preview inventory is read-only, including legacy migration.
			before, exists, err := rt.store.meetingLifecycle(ctx, "m.opus")
			if err != nil || exists != tc.legacy {
				t.Fatal(before, exists, err)
			}
			if tc.legacy && before.AnchorSource != "recording-completed" {
				t.Fatal("preview migrated lifecycle")
			}
			if _, err := service.reconcileRetentionInventory(ctx); err != nil {
				t.Fatal(err)
			}
			saved, _, err := rt.store.meetingLifecycle(ctx, "m.opus")
			if err != nil || saved.Anchor != tc.want || saved.CreatedAtUTC != tc.created || saved.RecordedAtLocal != tc.recorded {
				t.Fatal(saved, err)
			}
			if err := metadata.Put(ctx, 42, "m.opus", json.RawMessage(`{"audioPath":"./meetings/m.opus","createdAtUtc":"2026-10-01T00:00:00Z"}`)); err != nil {
				t.Fatal(err)
			}
			meetings, _, err = service.retentionInventory(ctx)
			if err != nil || len(meetings) != 1 || meetings[0].Anchor != tc.want {
				t.Fatal("republish reset age", meetings, err)
			}
		})
	}
}
