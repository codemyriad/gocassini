package operator

import (
	"context"
	"testing"
	"time"
)

func TestRemoteRetentionUsesJobRecordingHistory(t *testing.T) {
	for _, tc := range []struct {
		name, jobDate, attemptDate, want string
		noJob, unpublished               bool
	}{
		{name: "job completion", jobDate: "2026-03-05T13:38:29+01:00", want: "2026-03-05T12:38:29Z"},
		{name: "earliest attempt survives republish", jobDate: "2026-09-29T12:00:00Z", attemptDate: "2026-03-05T12:38:29Z", want: "2026-03-05T12:38:29Z"},
		{name: "attempt supplies missing job date", attemptDate: "2026-03-05T12:38:29Z", want: "2026-03-05T12:38:29Z"},
		{name: "no job history", noJob: true},
		{name: "no recording completion"},
		{name: "invalid recording completion", jobDate: "invalid", attemptDate: "invalid"},
		{name: "no successful publication", jobDate: "2026-03-05T12:38:29Z", unpublished: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, close := newBareSealRuntime(t)
			defer close()
			ctx := context.Background()
			if !tc.noJob {
				insertJob(t, rt.store.db, "m", "2026-09-29T12:00:00Z")
				if _, err := rt.store.db.Exec(`UPDATE jobs SET record_finished_at=NULLIF(?, '') WHERE id='m'`, tc.jobDate); err != nil {
					t.Fatal(err)
				}
				state := "succeeded"
				if tc.unpublished {
					state = "failed"
				}
				if _, err := rt.store.db.Exec(`UPDATE job_attempts SET stage='done', state=?, record_finished_at=NULLIF(?, '') WHERE job_id='m'`, state, tc.attemptDate); err != nil {
					t.Fatal(err)
				}
			}
			service := &annotationService{rt: rt, exapp: ExAppConfig{sharePaths: &recordingSharePathCache{
				ownerNames: map[int64]string{42: "m.opus"}, ownerExpires: time.Now().Add(time.Hour),
			}}}
			meetings, skipped, err := service.retentionInventory(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if len(meetings) != 0 || len(skipped) != 1 || skipped[0].Name != "m.opus" || skipped[0].Action != "skip" {
					t.Fatalf("unsupported history must skip file: meetings=%+v skipped=%+v", meetings, skipped)
				}
				return
			}
			if len(meetings) != 1 || len(skipped) != 0 || meetings[0].Anchor != tc.want || meetings[0].AnchorSource != "recording-completed" {
				t.Fatalf("wrong job-derived age: meetings=%+v skipped=%+v", meetings, skipped)
			}
			if err := rt.store.adoptMeetingLifecycle(ctx, meetings[0]); err != nil {
				t.Fatal(err)
			}
			// Later processing must not make an adopted recording younger.
			for _, query := range []string{
				`UPDATE jobs SET record_finished_at='2026-09-29T12:00:00Z' WHERE id='m'`,
				`UPDATE job_attempts SET record_finished_at='2026-09-29T12:00:00Z' WHERE job_id='m'`,
			} {
				if _, err := rt.store.db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			meetings, skipped, err = service.retentionInventory(ctx)
			if err != nil || len(meetings) != 1 || len(skipped) != 0 || meetings[0].Anchor != tc.want {
				t.Fatalf("saved age changed after republish: meetings=%+v skipped=%+v err=%v", meetings, skipped, err)
			}
		})
	}
}
