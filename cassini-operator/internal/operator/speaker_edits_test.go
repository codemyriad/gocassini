package operator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Speaker separation (docs/speaker-separation.md): the edits document, its
// store, and the fixtures the refine and handler tests share.

const (
	speakerTestRoom   = "spk_room"
	speakerTestRemote = "spk_remote"
	// speakerTestTranscript is a two-device transcript: a shared meeting-room
	// laptop and one remote participant.
	speakerTestTranscript = `{"version":"transcript.words.v1","speakers":[` +
		`{"id":"spk_room","label":"Meeting room laptop"},{"id":"spk_remote","label":"Remote"}],"segments":[]}`
)

// seedSpeakerJob gives jobID what a published ExApp-era meeting has on the
// operator volume: a done/succeeded job, its ready current/<job>.run capture
// and its current/<job>.meeting with a two-participant transcript.
func seedSpeakerJob(t *testing.T, store *Store, workRoot, jobID string) {
	t.Helper()
	seedJobRow(t, store.db, seededJobRow{ID: jobID, Stage: "done", State: "succeeded", CreatedAt: "2026-10-01T10:00:00Z", CompletedAt: strPtr("2026-10-01T11:00:00Z")})
	bundle, err := PrepareRunBundle(canonicalRunPath(workRoot, jobID), false)
	if err != nil {
		t.Fatalf("PrepareRunBundle() error = %v", err)
	}
	if err := os.WriteFile(bundle.RecordingPath, []byte("fake-mkv"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := FinalizeRunBundle(bundle, RunManifest{SourceMode: "talk", RecorderName: "Test"}); err != nil {
		t.Fatalf("FinalizeRunBundle() error = %v", err)
	}
	setJobArtifactRunPath(t, store.db, jobID, bundle.RootDir)
	writeSpeakerMeetingFixture(t, canonicalMeetingPath(workRoot, jobID), bundle.RootDir)
}

func writeSpeakerMeetingFixture(t *testing.T, meetingPath, runPath string) {
	t.Helper()
	if err := writeReadyMeetingBundleFixture(meetingPath, runPath); err != nil {
		t.Fatalf("write meeting fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(meetingPath, "transcript.words.v1.json"), []byte(speakerTestTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
}

func openSpeakerTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	t.Setenv("CASSINI_REPO_ROOT", filepath.Clean(filepath.Join("..", "..", "..")))
	tmp := t.TempDir()
	store, err := OpenStore(filepath.Join(tmp, "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, filepath.Join(tmp, "jobs")
}

func splitDoc(ids ...string) speakerEditsDoc {
	doc := emptySpeakerEditsDoc()
	for _, id := range ids {
		doc.Splits = append(doc.Splits, speakerEditsSplit{SpeakerID: id})
	}
	return doc
}

// waitForSpeakerEdits polls the store, as waitForJobState does, until cond
// holds for the job's edits record.
func waitForSpeakerEdits(t *testing.T, store *Store, jobID string, cond func(speakerEditsRecord) bool) speakerEditsRecord {
	t.Helper()
	deadline := time.Now().Add(testWaitTimeout)
	for time.Now().Before(deadline) {
		rec, err := store.GetSpeakerEdits(context.Background(), jobID)
		if err == nil && cond(rec) {
			return rec
		}
		time.Sleep(20 * time.Millisecond)
	}
	rec, err := store.GetSpeakerEdits(context.Background(), jobID)
	t.Fatalf("speaker edits of %s never reached the wanted state: %+v (%v)", jobID, rec, err)
	return rec
}

func TestSpeakerEditsValidationMirrorsTheRecorderRules(t *testing.T) {
	// "Spk-Room" is a participant the CLI could not name a turns file after.
	participants := map[string]bool{speakerTestRoom: true, speakerTestRemote: true, "Spk-Room": true}
	cases := []struct {
		name string
		doc  speakerEditsDoc
		want string
	}{
		{"split a voice", speakerEditsDoc{Splits: []speakerEditsSplit{{"spk_room~1"}}}, "cannot split"},
		{"split twice", speakerEditsDoc{Splits: []speakerEditsSplit{{speakerTestRoom}, {speakerTestRoom}}}, "split twice"},
		{"split a stranger", speakerEditsDoc{Splits: []speakerEditsSplit{{"spk_nobody"}}}, "not a participant"},
		{"split an id the CLI refuses", speakerEditsDoc{Splits: []speakerEditsSplit{{"Spk-Room"}}}, "not a participant"},
		{"merge across devices", speakerEditsDoc{Merges: []speakerEditsMerge{{"spk_room~1", "spk_remote~1"}}}, "invalid merge"},
		{"merge a device", speakerEditsDoc{Merges: []speakerEditsMerge{{speakerTestRoom, "spk_room~1"}}}, "invalid merge"},
		{"merge into itself", speakerEditsDoc{Merges: []speakerEditsMerge{{"spk_room~1", "spk_room~1"}}}, "invalid merge"},
		{"merge twice", speakerEditsDoc{Merges: []speakerEditsMerge{{"spk_room~1", "spk_room~2"}, {"spk_room~1", "spk_room~3"}}}, "merged twice"},
		{"merge chain", speakerEditsDoc{Merges: []speakerEditsMerge{{"spk_room~1", "spk_room~2"}, {"spk_room~2", "spk_room~3"}}}, "itself merged"},
		{"label without speaker", speakerEditsDoc{Labels: []speakerEditsLabel{{"", "Ann"}}}, "without speaker"},
		{"label twice", speakerEditsDoc{Labels: []speakerEditsLabel{{"spk_room~1", "Ann"}, {"spk_room~1", "Bea"}}}, "labelled twice"},
		{"empty label", speakerEditsDoc{Labels: []speakerEditsLabel{{"spk_room~1", "   "}}}, "1-64"},
		{"long label", speakerEditsDoc{Labels: []speakerEditsLabel{{"spk_room~1", strings.Repeat("é", 65)}}}, "1-64"},
		{"control character", speakerEditsDoc{Labels: []speakerEditsLabel{{"spk_room~1", "Ann\u0007"}}}, "control"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.doc.validate(participants)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validate() = %v, want an error mentioning %q", err, tc.want)
			}
		})
	}

	ok := speakerEditsDoc{
		Splits: []speakerEditsSplit{{speakerTestRoom}},
		Merges: []speakerEditsMerge{{"spk_room~3", "spk_room~1"}},
		Labels: []speakerEditsLabel{{"spk_room~1", "  Ann Smith "}, {speakerTestRemote, strings.Repeat("é", 64)}},
	}
	if err := ok.validate(participants); err != nil {
		t.Fatalf("a valid document was refused: %v", err)
	}
	if ok.Labels[0].Label != "Ann Smith" {
		t.Fatalf("label = %q, want it trimmed", ok.Labels[0].Label)
	}
}

func TestQueueSpeakerEditsStoresTheRevisionAndQueuesTheRefineTogether(t *testing.T) {
	store, workRoot := openSpeakerTestStore(t)
	ctx := context.Background()
	seedSpeakerJob(t, store, workRoot, "JOB1")

	doc := splitDoc(speakerTestRoom)
	revision, err := store.QueueSpeakerEdits(ctx, "JOB1", 0, doc, "alice", nowUTCString())
	if err != nil || revision != 1 {
		t.Fatalf("QueueSpeakerEdits() = %d, %v; want 1, nil", revision, err)
	}
	rec, err := store.GetSpeakerEdits(ctx, "JOB1")
	if err != nil || rec.Revision != 1 || rec.Doc.Revision != 1 || rec.AppliedRevision != 0 || len(rec.Doc.Splits) != 1 {
		t.Fatalf("stored edits = %+v, %v", rec, err)
	}
	var updatedBy string
	if err := store.db.QueryRow(`SELECT updated_by FROM speaker_edits WHERE job_id = 'JOB1'`).Scan(&updatedBy); err != nil || updatedBy != "alice" {
		t.Fatalf("updated_by = %q, %v", updatedBy, err)
	}
	job := mustGetJob(t, store, "JOB1")
	if job.Stage != "build" || job.State != "queued" || job.CurrentAttemptNumber != 2 {
		t.Fatalf("job = %s/%s attempt %d, want build/queued attempt 2", job.Stage, job.State, job.CurrentAttemptNumber)
	}
	kind, snapshot, err := store.AttemptSpeakerEdits(ctx, "JOB1", 2)
	if err != nil || kind != triggerKindRefine || snapshot == nil || snapshot.Revision != 1 || snapshot.Splits[0].SpeakerID != speakerTestRoom {
		t.Fatalf("attempt 2 = %q %+v %v, want a refine carrying revision 1", kind, snapshot, err)
	}
	tasks, err := store.ListQueuedBuildTasks(ctx)
	if err != nil || len(tasks) != 1 || tasks[0].JobID != "JOB1" || tasks[0].AttemptNumber != 2 {
		t.Fatalf("queued build tasks = %+v, %v — the dispatcher must see the refine", tasks, err)
	}

	// The job is busy now. The refusal must leave the edits where they were:
	// a stored revision with no attempt to apply it would never reach the
	// recording.
	if _, err := store.QueueSpeakerEdits(ctx, "JOB1", 1, emptySpeakerEditsDoc(), "bob", nowUTCString()); !errors.Is(err, errSpeakerEditsBusy) {
		t.Fatalf("QueueSpeakerEdits(busy) error = %v, want errSpeakerEditsBusy", err)
	}
	if rec, _ := store.GetSpeakerEdits(ctx, "JOB1"); rec.Revision != 1 || len(rec.Doc.Splits) != 1 {
		t.Fatalf("a refused edit changed the stored document: %+v", rec)
	}
	current, err := store.QueueSpeakerEdits(ctx, "JOB1", 0, emptySpeakerEditsDoc(), "bob", nowUTCString())
	if !errors.Is(err, errSpeakerEditsRevisionConflict) || current != 1 {
		t.Fatalf("QueueSpeakerEdits(stale) = %d, %v; want 1, errSpeakerEditsRevisionConflict", current, err)
	}
}

func TestQueueSpeakerEditsAcceptsARetryAfterAFailedRefine(t *testing.T) {
	store, workRoot := openSpeakerTestStore(t)
	ctx := context.Background()
	seedSpeakerJob(t, store, workRoot, "JOB1")
	if _, err := store.db.Exec(`UPDATE jobs SET stage = 'done', state = 'failed' WHERE id = 'JOB1'`); err != nil {
		t.Fatal(err)
	}
	if revision, err := store.QueueSpeakerEdits(ctx, "JOB1", 0, splitDoc(speakerTestRoom), "alice", nowUTCString()); err != nil || revision != 1 {
		t.Fatalf("QueueSpeakerEdits(failed job) = %d, %v; want 1, nil", revision, err)
	}
}

// A job a restart interrupted is idle too.
func TestQueueSpeakerEditsAcceptsAnInterruptedJob(t *testing.T) {
	store, workRoot := openSpeakerTestStore(t)
	ctx := context.Background()
	seedSpeakerJob(t, store, workRoot, "JOB1")
	if _, err := store.db.Exec(`UPDATE jobs SET stage = 'publish', state = 'interrupted' WHERE id = 'JOB1'`); err != nil {
		t.Fatal(err)
	}
	if revision, err := store.QueueSpeakerEdits(ctx, "JOB1", 0, splitDoc(speakerTestRoom), "alice", nowUTCString()); err != nil || revision != 1 {
		t.Fatalf("QueueSpeakerEdits(interrupted job) = %d, %v; want 1, nil", revision, err)
	}
}

func TestSpeakerSplitTurnsAreWriteOnce(t *testing.T) {
	store, _ := openSpeakerTestStore(t)
	ctx := context.Background()
	first, err := store.PutSpeakerSplitTurns(ctx, "JOB1", speakerTestRoom, `{"first":true}`, "m", "s", nowUTCString())
	if err != nil || first != `{"first":true}` {
		t.Fatalf("first put = %q, %v", first, err)
	}
	second, err := store.PutSpeakerSplitTurns(ctx, "JOB1", speakerTestRoom, `{"second":true}`, "m2", "s2", nowUTCString())
	if err != nil || second != `{"first":true}` {
		t.Fatalf("second put = %q, %v; want the first turns kept", second, err)
	}
	missing, err := store.MissingSpeakerSplitTurns(ctx, "JOB1", splitDoc(speakerTestRoom, speakerTestRemote))
	if err != nil || len(missing) != 1 || missing[0] != speakerTestRemote {
		t.Fatalf("missing = %v, %v", missing, err)
	}
}

// The applied revision moves in the transaction that marks the publish
// succeeded: the page polls until it sees the attempt done, and an attempt
// done with its revision not yet applied reads as "idle, never applied".
func TestPublishSuccessAppliesTheSpeakerEditsInTheSameTransaction(t *testing.T) {
	store, workRoot := openSpeakerTestStore(t)
	ctx := context.Background()
	seedSpeakerJob(t, store, workRoot, "JOB1")
	if _, err := store.QueueSpeakerEdits(ctx, "JOB1", 0, splitDoc(speakerTestRoom), "alice", nowUTCString()); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAttemptSpeakerEditsReport(ctx, "JOB1", 2, `{"revision":1}`); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSpeakerEditsError(ctx, "JOB1", "an earlier failure"); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPublishSucceeded(ctx, "JOB1", "site", "attempt-site", nowUTCString()); err != nil {
		t.Fatal(err)
	}
	rec, _ := store.GetSpeakerEdits(ctx, "JOB1")
	if rec.AppliedRevision != 1 || rec.LastError != "" || string(rec.LastReport) != `{"revision":1}` {
		t.Fatalf("after publish = %+v", rec)
	}
	var appliedDoc string
	if err := store.db.QueryRow(`SELECT applied_doc_json FROM speaker_edits WHERE job_id = 'JOB1'`).Scan(&appliedDoc); err != nil || !strings.Contains(appliedDoc, `"revision":1`) {
		t.Fatalf("applied_doc_json = %q, %v; want the published snapshot", appliedDoc, err)
	}
}

func TestSpeakerEditsAppliedRevisionNeverMovesBackwards(t *testing.T) {
	store, workRoot := openSpeakerTestStore(t)
	ctx := context.Background()
	seedSpeakerJob(t, store, workRoot, "JOB1")
	if _, err := store.QueueSpeakerEdits(ctx, "JOB1", 0, splitDoc(speakerTestRoom), "alice", nowUTCString()); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAttemptSpeakerEditsReport(ctx, "JOB1", 2, `{"revision":1}`); err != nil {
		t.Fatal(err)
	}
	// A later revision already published (an out-of-order publish).
	if _, err := store.db.Exec(`UPDATE speaker_edits SET applied_revision = 5, applied_doc_json = '{"revision":5}', last_report_json = '{"revision":5}' WHERE job_id = 'JOB1'`); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPublishSucceeded(ctx, "JOB1", "site", "attempt-site", nowUTCString()); err != nil {
		t.Fatal(err)
	}
	rec, _ := store.GetSpeakerEdits(ctx, "JOB1")
	var appliedDoc string
	_ = store.db.QueryRow(`SELECT applied_doc_json FROM speaker_edits WHERE job_id = 'JOB1'`).Scan(&appliedDoc)
	if rec.AppliedRevision != 5 || string(rec.LastReport) != `{"revision":5}` || appliedDoc != `{"revision":5}` {
		t.Fatalf("after a late publish of revision 1 = %+v, applied doc %s", rec, appliedDoc)
	}
	// An attempt with no snapshot changes nothing.
	seedSpeakerJob(t, store, workRoot, "PLAIN")
	if err := store.MarkPublishSucceeded(ctx, "PLAIN", "site", "attempt-site", nowUTCString()); err != nil {
		t.Fatal(err)
	}
	if rec, _ := store.GetSpeakerEdits(ctx, "PLAIN"); rec.Revision != 0 || rec.AppliedRevision != 0 {
		t.Fatalf("a publish with no snapshot touched speaker edits: %+v", rec)
	}
}

func TestRerunSnapshotsTheSpeakerEditsOnlyOnceSomebodyEdited(t *testing.T) {
	store, workRoot := openSpeakerTestStore(t)
	ctx := context.Background()
	seedSpeakerJob(t, store, workRoot, "PLAIN")
	seedSpeakerJob(t, store, workRoot, "EDITED")

	plain, err := store.QueueRerunAttempt(ctx, mustGetJob(t, store, "PLAIN"), nowUTCString())
	if err != nil {
		t.Fatal(err)
	}
	if kind, snapshot, err := store.AttemptSpeakerEdits(ctx, "PLAIN", plain.CurrentAttemptNumber); err != nil || kind != triggerKindRerun || snapshot != nil {
		t.Fatalf("plain rerun = %q %+v %v, want a rerun with no snapshot", kind, snapshot, err)
	}

	// Revision 4 adds a split that never applied; the recording carries 3.
	applied := splitDoc(speakerTestRoom)
	applied.Revision = 3
	appliedRaw, _ := json.Marshal(applied)
	latest := splitDoc(speakerTestRoom, speakerTestRemote)
	latest.Revision = 4
	latestRaw, _ := json.Marshal(latest)
	if _, err := store.db.Exec(`INSERT INTO speaker_edits (job_id, revision, doc_json, applied_revision, applied_doc_json, updated_at) VALUES ('EDITED', 4, ?, 3, ?, ?)`, string(latestRaw), string(appliedRaw), nowUTCString()); err != nil {
		t.Fatal(err)
	}
	edited, err := store.QueueRerunAttempt(ctx, mustGetJob(t, store, "EDITED"), nowUTCString())
	if err != nil {
		t.Fatal(err)
	}
	kind, snapshot, err := store.AttemptSpeakerEdits(ctx, "EDITED", edited.CurrentAttemptNumber)
	if err != nil || kind != triggerKindRerun || snapshot == nil || snapshot.Revision != 3 || len(snapshot.Splits) != 1 {
		t.Fatalf("edited rerun = %q %+v %v, want a rerun replaying the applied revision 3", kind, snapshot, err)
	}

	// Edits that never applied at all are not replayed.
	seedSpeakerJob(t, store, workRoot, "NEVER")
	if _, err := store.db.Exec(`INSERT INTO speaker_edits (job_id, revision, doc_json, applied_revision, updated_at) VALUES ('NEVER', 1, ?, 0, ?)`, string(latestRaw), nowUTCString()); err != nil {
		t.Fatal(err)
	}
	never, err := store.QueueRerunAttempt(ctx, mustGetJob(t, store, "NEVER"), nowUTCString())
	if err != nil {
		t.Fatal(err)
	}
	if _, snapshot, err := store.AttemptSpeakerEdits(ctx, "NEVER", never.CurrentAttemptNumber); err != nil || snapshot != nil {
		t.Fatalf("rerun of never-applied edits = %+v %v, want no snapshot", snapshot, err)
	}
}
