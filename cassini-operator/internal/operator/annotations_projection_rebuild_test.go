package operator

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
)

func TestAnnotationBackfillRestoresPendingQueryRows(t *testing.T) {
	for _, mode := range []string{"old archive", "unchanged checksum", "archive offline", "unreadable archive", "republish unresolved"} {
		t.Run(mode, func(t *testing.T) {
			_, s, h, store := asyncFixture(t)
			postAsync(t, h, markRequest("pending", "req1"))
			ctx := context.Background()
			before, err := store.document(ctx, "MEETING1.opus")
			if err != nil {
				t.Fatal(err)
			}
			baseline, err := s.snapshot(ctx, before.Sync.Confirmed)
			if err != nil {
				t.Fatal(err)
			}
			archive := newFakeAnnotationArchive()
			archive.put("MEETING1.opus", "c0", baseline)
			if mode == "republish unresolved" {
				if err := store.prepareAnnotationRepublish(ctx, "MEETING1.opus", baseline); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.db.Exec(`DELETE FROM annotation_item; DELETE FROM annotation_tag`); err != nil {
				t.Fatal(err)
			}
			if mode == "unchanged checksum" {
				if _, err := store.db.Exec(`UPDATE meeting_annotations SET container_sha256='c0'`); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := store.db.Exec(`DELETE FROM meeting_annotations`); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "archive offline" {
				archive.stateErr["MEETING1.opus"] = errors.New("temporary outage")
			}
			if mode == "unreadable archive" {
				archive.readErr["MEETING1.opus"] = &annotationsUnreadableError{err: errors.New("invalid container")}
			}
			report := archive.rebuild(t, store, "MEETING1.opus")
			if mode == "unchanged checksum" && (report.Unchanged != 1 || archive.reads["MEETING1.opus"] != 0) {
				t.Fatalf("checksum fast path: %+v reads=%d", report, archive.reads["MEETING1.opus"])
			}
			if mode == "archive offline" && report.Failed != 1 {
				t.Fatalf("outage: %+v", report)
			}
			after, err := store.document(ctx, "MEETING1.opus")
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("rebuild changed durable desired/confirmed state: before=%+v after=%+v err=%v", before, after, err)
			}
			vocab, err := store.Vocabulary(ctx, []string{"MEETING1.opus"})
			if err != nil || len(vocab) != 1 || vocab[0].Label != "pending" || vocab[0].Marks != 1 {
				t.Fatalf("desired vocabulary not restored: %+v %v", vocab, err)
			}
			tagged, err := store.taggedMeetings(ctx, "pending", []string{"MEETING1.opus"})
			if err != nil || !tagged["MEETING1.opus"] {
				t.Fatalf("tag search not restored: %v %v", tagged, err)
			}
		})
	}
}

func TestAnnotationPendingImportRebuildsQueryRowsAtomically(t *testing.T) {
	_, s, h, store := asyncFixture(t)
	postAsync(t, h, markRequest("pending", "req1"))
	ctx := context.Background()
	before, err := store.document(ctx, "MEETING1.opus")
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := s.snapshot(ctx, before.Sync.Confirmed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM annotation_item`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.record(ctx, "MEETING1.opus", baseline, false); err != nil {
		t.Fatal(err)
	}
	want, err := store.Vocabulary(ctx, []string{"MEETING1.opus"})
	if err != nil || len(want) != 1 || want[0].Marks != 1 {
		t.Fatalf("pending import did not repair rows: %+v %v", want, err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_rebuild BEFORE INSERT ON annotation_item BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	// Exercise rollback after deleting/reinserting meeting and tag rows.
	if err := store.inTx(ctx, func(tx *sql.Tx) error { return rebuildDesiredAnnotationProjection(ctx, tx, "MEETING1.opus") }); err == nil {
		t.Fatal("ignored projection write failure")
	}
	got, err := store.Vocabulary(ctx, []string{"MEETING1.opus"})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("partial projection escaped rollback: %+v %v", got, err)
	}
	after, err := store.document(ctx, "MEETING1.opus")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("durable state changed: %+v %v", after, err)
	}
}
