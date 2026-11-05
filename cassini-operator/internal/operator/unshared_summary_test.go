package operator

import (
	"context"
	"testing"
)

// Since is when the oldest unshared meeting happened, not when its shares
// were last reconciled: a re-share today on a 1 October meeting keeps since
// at 1 October. An import stands at its creation; a meeting whose start is
// not known at all, at its reconciliation.
func TestUnsharedSinceIsTheOldestAffectedMeetingsStart(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	ctx := context.Background()
	record := func(id, updatedAt string) {
		t.Helper()
		if err := rt.store.RecordJobShareOutcome(ctx, id, shareOutcomePartial, []skippedShare{{Type: "user", ID: "bob"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := rt.store.db.Exec(`UPDATE job_share_outcomes SET updated_at=? WHERE job_id=?`, updatedAt, id); err != nil {
			t.Fatal(err)
		}
	}
	// Recorded on 1 October, re-shared on 4 November.
	seedJobRow(t, rt.store.db, seededJobRow{ID: "J1", Stage: "done", State: "succeeded", CreatedAt: "2026-10-01T09:00:00Z"})
	setJobTimes(t, rt, "J1", "2026-10-01T09:14:00.000000000Z", "2026-10-01T10:00:00.000000000Z")
	record("J1", "2026-11-04T12:00:00.000000000Z")
	// Imported on 20 October, no job: its lifecycle's creation.
	if _, err := rt.store.db.Exec(`INSERT INTO meeting_lifecycle(name,file_id,document_path,state,age_anchor,anchor_source,created_at_utc,recorded_at_local,reason)
VALUES('IMP1.opus',43,'CassiniRecordings/meetings/IMP1.opus','active','2026-10-20T08:00:00Z','createdAtUtc','2026-10-20T08:00:00Z','','')`); err != nil {
		t.Fatal(err)
	}
	record("IMP1", "2026-11-04T12:00:00.000000000Z")
	summary, err := rt.store.unsharedSummary(ctx)
	if err != nil || summary == nil || summary.Jobs != 2 || summary.Since != "2026-10-01T09:14:00Z" {
		t.Fatalf("summary = %+v, %v; want since 1 October", summary, err)
	}
	// Nothing known about when J9 happened: its reconciliation stands in.
	record("J9", "2026-09-15T00:00:00.000000000Z")
	if summary, _ := rt.store.unsharedSummary(ctx); summary == nil || summary.Since != "2026-09-15T00:00:00Z" {
		t.Fatalf("summary = %+v; want since the unknown meeting's reconciliation", summary)
	}
}
