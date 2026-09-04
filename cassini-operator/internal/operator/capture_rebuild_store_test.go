package operator

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newRebuildTestStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("CASSINI_REPO_ROOT", filepath.Clean(filepath.Join("..", "..", "..")))
	store, err := OpenStore(filepath.Join(t.TempDir(), "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// ms derives an epoch from a readable instant. An epoch constant typed by hand
// is a silent way to test the wrong moment.
func ms(t *testing.T, rfc3339 string) int64 {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		t.Fatalf("parse %s: %v", rfc3339, err)
	}
	return parsed.UnixMilli()
}

// stamp renders an instant the way the store persists one.
func stamp(t *testing.T, rfc3339 string) string {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		t.Fatalf("parse %s: %v", rfc3339, err)
	}
	return formatUTCString(parsed)
}

// seedRecording puts a job in the store with a Talk binding and a recording
// window, which is what an upload has to be matched against.
func seedRecording(t *testing.T, store *Store, id, room, startedAt, finishedAt string) {
	t.Helper()
	ctx := context.Background()
	now := nowUTCString()
	if err := store.InsertQueuedJob(ctx, Job{
		ID: id, Provider: "talk", RequestJSON: "{}",
		Stage: "record", State: "queued", CurrentAttemptNumber: 1,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("InsertQueuedJob: %v", err)
	}
	binding := `{"backend_url":"https://nc.example","room_token":"` + room + `","owner":"alice"}`
	finished := any(nil)
	if finishedAt != "" {
		finished = finishedAt
	}
	if _, err := store.db.Exec(
		`UPDATE jobs SET talk_binding = ?, record_started_at = ?, record_finished_at = ? WHERE id = ?`,
		binding, startedAt, finished, id); err != nil {
		t.Fatalf("seed recording: %v", err)
	}
}

func setJobState(t *testing.T, store *Store, id, stage, state string) {
	t.Helper()
	if _, err := store.db.Exec(`UPDATE jobs SET stage = ?, state = ? WHERE id = ?`, stage, state, id); err != nil {
		t.Fatalf("set job state: %v", err)
	}
}

func owedJobIDs(t *testing.T, store *Store) []string {
	t.Helper()
	candidates, err := store.ListJobsAwaitingSourceAudioRebuild(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListJobsAwaitingSourceAudioRebuild: %v", err)
	}
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.JobID)
	}
	return ids
}

// R2. An upload is matched to a recording by room AND by an overlapping call
// window. The room alone is not enough: a busy room holds many recordings, and
// matching on it would put one meeting's speech in another's transcript.
func TestResolveJobForCaptureMatchesRoomAndWindow(t *testing.T) {
	store := newRebuildTestStore(t)
	ctx := context.Background()

	seedRecording(t, store, "morning", "room-a", stamp(t, "2026-09-02T10:00:00Z"), stamp(t, "2026-09-02T11:00:00Z"))
	seedRecording(t, store, "afternoon", "room-a", stamp(t, "2026-09-02T14:00:00Z"), stamp(t, "2026-09-02T15:00:00Z"))
	seedRecording(t, store, "elsewhere", "room-b", stamp(t, "2026-09-02T10:30:00Z"), stamp(t, "2026-09-02T10:45:00Z"))

	got, err := store.ResolveJobForCapture(ctx, "room-a", ms(t, "2026-09-02T10:15:00Z"), ms(t, "2026-09-02T10:40:00Z"))
	if err != nil {
		t.Fatalf("ResolveJobForCapture: %v", err)
	}
	if got.JobID != "morning" {
		t.Fatalf("a capture from 10:15 resolved to %q, want the 10:00 recording", got.JobID)
	}

	got, err = store.ResolveJobForCapture(ctx, "room-a", ms(t, "2026-09-02T14:15:00Z"), ms(t, "2026-09-02T14:30:00Z"))
	if err != nil {
		t.Fatalf("ResolveJobForCapture: %v", err)
	}
	if got.JobID != "afternoon" {
		t.Fatalf("a capture from 14:15 resolved to %q; the window is what separates two recordings in one room", got.JobID)
	}

	// A capture from the next day matches nothing rather than the nearest thing.
	nextDay := ms(t, "2026-09-03T10:00:00Z")
	if _, err := store.ResolveJobForCapture(ctx, "room-a", nextDay, nextDay+60_000); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("a capture from another day resolved anyway (err=%v); that is one meeting's speech in another's transcript", err)
	}
	// A room this operator never recorded is an ordinary miss, not an error.
	if _, err := store.ResolveJobForCapture(ctx, "room-unknown", ms(t, "2026-09-02T10:15:00Z"), ms(t, "2026-09-02T10:40:00Z")); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("an unrecorded room gave err=%v, want sql.ErrNoRows", err)
	}
	// A job with no Talk binding at all is absent from the room index rather
	// than an error.
	if err := store.InsertQueuedJob(ctx, Job{ID: "bare", Provider: "cli", RequestJSON: "{}", Stage: "record", State: "queued", CurrentAttemptNumber: 1, CreatedAt: nowUTCString(), UpdatedAt: nowUTCString()}); err != nil {
		t.Fatalf("InsertQueuedJob: %v", err)
	}
	if _, err := store.ResolveJobForCapture(ctx, "", 1, 2); err == nil {
		t.Fatal("an empty room token resolved to something")
	}
}

// R2. Two recordings of one room whose windows overlap is a state nobody
// modelled, and choosing between them would put somebody's words in the wrong
// meeting. The answer is a refusal an administrator can read, not a tie-break.
func TestResolveJobForCaptureRefusesAnAmbiguousMatch(t *testing.T) {
	store := newRebuildTestStore(t)
	seedRecording(t, store, "first", "room-a", stamp(t, "2026-09-02T10:00:00Z"), stamp(t, "2026-09-02T11:00:00Z"))
	seedRecording(t, store, "second", "room-a", stamp(t, "2026-09-02T10:30:00Z"), stamp(t, "2026-09-02T11:30:00Z"))

	_, err := store.ResolveJobForCapture(context.Background(), "room-a",
		ms(t, "2026-09-02T10:40:00Z"), ms(t, "2026-09-02T10:50:00Z"))
	if !errors.Is(err, ErrCaptureJobAmbiguous) {
		t.Fatalf("two overlapping recordings resolved to one anyway: err=%v", err)
	}
	if got := err.Error(); !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Fatalf("the ambiguity error does not name both candidates: %q", got)
	}
}

// R2. A recorder that died without ever writing an end must not compete for the
// captures of every call that room holds afterwards.
//
// The row's own timestamps cannot bound it: the startup sweep rewrites
// updated_at on every restart, so any window derived from it grows to the
// present moment each time the operator comes up — and then a recording that
// crashed last month matches today's standup, which is a wrong attribution on
// its own and, once two of them do it, an ambiguity that refuses the rebuild
// for the real recording as well.
func TestARecordingThatDiedDoesNotCompeteForLaterCaptures(t *testing.T) {
	store := newRebuildTestStore(t)
	ctx := context.Background()

	seedRecording(t, store, "died", "room-a", stamp(t, "2026-08-02T10:00:00Z"), "")
	setJobState(t, store, "died", "record", "interrupted")
	// What a restart does to that row: interrupted_at and updated_at move to
	// the startup epoch, long after the recording it describes.
	if _, err := store.MarkIncompleteJobsInterrupted(ctx, nowUTCString()); err != nil {
		t.Fatalf("MarkIncompleteJobsInterrupted: %v", err)
	}
	seedRecording(t, store, "today", "room-a", stamp(t, "2026-09-02T10:00:00Z"), stamp(t, "2026-09-02T11:00:00Z"))

	got, err := store.ResolveJobForCapture(ctx, "room-a", ms(t, "2026-09-02T10:05:00Z"), ms(t, "2026-09-02T10:55:00Z"))
	if err != nil {
		t.Fatalf("a capture from today could not be resolved because a dead recording still matched it: %v", err)
	}
	if got.JobID != "today" {
		t.Fatalf("today's capture resolved to %q", got.JobID)
	}

	// Not even for a capture from its own moment: it produced no run bundle, so
	// there is nothing to rebuild and nothing to attribute the upload to.
	if _, err := store.ResolveJobForCapture(ctx, "room-a", ms(t, "2026-08-02T10:05:00Z"), ms(t, "2026-08-02T10:15:00Z")); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("a recording that never wrote an end was matched anyway: %v", err)
	}
}

// R2. A recording that is still being made has a genuinely open end, and an
// upload for it must resolve however late in the call the participant joined.
// A participant who joins forty minutes in, leaves and uploads is the ordinary
// case, and any bound taken from the row would refuse them.
func TestALiveRecordingMatchesAParticipantWhoJoinedLate(t *testing.T) {
	store := newRebuildTestStore(t)
	ctx := context.Background()

	seedRecording(t, store, "live", "room-a", stamp(t, "2026-09-02T10:00:00Z"), "")
	setJobState(t, store, "live", "record", "running")

	got, err := store.ResolveJobForCapture(ctx, "room-a", ms(t, "2026-09-02T10:40:00Z"), ms(t, "2026-09-02T10:55:00Z"))
	if err != nil {
		t.Fatalf("a participant who joined forty minutes into a live recording could not be resolved: %v", err)
	}
	if got.JobID != "live" {
		t.Fatalf("the late joiner's capture resolved to %q", got.JobID)
	}

	// And not to a call that had already ended before it started. A live
	// recording's window is everything recorded SO FAR: open at the end, and
	// firmly closed at the beginning.
	before := ms(t, "2026-09-02T08:00:00Z")
	if _, err := store.ResolveJobForCapture(ctx, "room-a", before, before+600_000); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("a live recording claimed a capture from a call that ended before it started: %v", err)
	}
}

// R6. A job an administrator has to look at never gets an automatic rebuild, so
// its job JSON must not promise one — and /operator/status must count the same
// way, or the two disagree about the same row.
func TestAFailedJobDoesNotPromiseARebuild(t *testing.T) {
	store := newRebuildTestStore(t)
	ctx := context.Background()
	seedRecording(t, store, "job-1", "room-a", stamp(t, "2026-09-02T10:00:00Z"), stamp(t, "2026-09-02T11:00:00Z"))
	if err := store.NoteSourceAudioUpload(ctx, "job-1", nowUTCString()); err != nil {
		t.Fatalf("NoteSourceAudioUpload: %v", err)
	}
	setJobState(t, store, "job-1", "done", "failed")

	job, err := store.GetJob(ctx, "job-1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.SourceAudioRebuild.Pending {
		t.Fatal("a failed job promises a rebuild that is never coming")
	}
	if job.SourceAudioRebuild.UploadSeq != 1 {
		t.Fatalf("the arrival was not recorded on the failed job: %+v", job.SourceAudioRebuild)
	}
	pending, _, err := store.CountSourceAudioRebuilds(ctx)
	if err != nil {
		t.Fatalf("CountSourceAudioRebuilds: %v", err)
	}
	if pending != 0 {
		t.Fatalf("/operator/status counts %d pending rebuilds where the job JSON says none", pending)
	}
}

// R1. The counters are the whole mechanism: a gap means audio arrived that no
// build has seen, and a wave of uploads is one gap rather than one each.
func TestSourceAudioCountersOweAndClearOneRebuild(t *testing.T) {
	store := newRebuildTestStore(t)
	ctx := context.Background()
	seedRecording(t, store, "job-1", "room-a", stamp(t, "2026-09-02T10:00:00Z"), stamp(t, "2026-09-02T11:00:00Z"))
	setJobState(t, store, "job-1", "done", "succeeded")

	if owed := owedJobIDs(t, store); len(owed) != 0 {
		t.Fatalf("a job with no uploads is owed a rebuild: %v", owed)
	}

	if err := store.NoteSourceAudioUpload(ctx, "job-1", nowUTCString()); err != nil {
		t.Fatalf("NoteSourceAudioUpload: %v", err)
	}
	if owed := owedJobIDs(t, store); len(owed) != 1 || owed[0] != "job-1" {
		t.Fatalf("a late upload did not make the job owe a rebuild: %v", owed)
	}

	seq, err := store.SourceAudioUploadSeq(ctx, "job-1")
	if err != nil {
		t.Fatalf("SourceAudioUploadSeq: %v", err)
	}
	if err := store.MarkSourceAudioBuilt(ctx, "job-1", seq, "digest-1"); err != nil {
		t.Fatalf("MarkSourceAudioBuilt: %v", err)
	}
	if owed := owedJobIDs(t, store); len(owed) != 0 {
		t.Fatalf("the rebuild did not clear the debt, so it would run for ever: %v", owed)
	}

	// Three more uploads: one debt, not three.
	for i := 0; i < 3; i++ {
		if err := store.NoteSourceAudioUpload(ctx, "job-1", nowUTCString()); err != nil {
			t.Fatalf("NoteSourceAudioUpload: %v", err)
		}
	}
	owed := owedJobIDs(t, store)
	if len(owed) != 1 {
		t.Fatalf("three uploads produced %d rebuild candidates, want one", len(owed))
	}

	// And the digest the build stamped came back with them, so the dispatcher
	// can tell a genuinely new capture set from the one it already used.
	candidates, err := store.ListJobsAwaitingSourceAudioRebuild(ctx, 0)
	if err != nil {
		t.Fatalf("ListJobsAwaitingSourceAudioRebuild: %v", err)
	}
	if candidates[0].BuiltDigest != "digest-1" {
		t.Fatalf("built digest = %q, want the one the last build stamped", candidates[0].BuiltDigest)
	}
	if candidates[0].RoomToken != "room-a" {
		t.Fatalf("room token = %q, want room-a", candidates[0].RoomToken)
	}
	if candidates[0].UploadedAt.IsZero() {
		t.Fatal("the arrival time is missing, so the quiet period cannot be measured")
	}
}

// R1. An upload landing WHILE a build runs must not be swallowed by that
// build's own stamp: it would be counted as consumed by a build that never
// opened it, and no rebuild would ever be owed for it.
func TestUploadDuringABuildIsStillOwedARebuild(t *testing.T) {
	store := newRebuildTestStore(t)
	ctx := context.Background()
	seedRecording(t, store, "job-1", "room-a", stamp(t, "2026-09-02T10:00:00Z"), stamp(t, "2026-09-02T11:00:00Z"))

	if err := store.NoteSourceAudioUpload(ctx, "job-1", nowUTCString()); err != nil {
		t.Fatalf("NoteSourceAudioUpload: %v", err)
	}
	// The build claims and reads the counter.
	claimed, err := store.SourceAudioUploadSeq(ctx, "job-1")
	if err != nil {
		t.Fatalf("SourceAudioUploadSeq: %v", err)
	}
	// A second upload lands while it is working.
	if err := store.NoteSourceAudioUpload(ctx, "job-1", nowUTCString()); err != nil {
		t.Fatalf("NoteSourceAudioUpload: %v", err)
	}
	// The build succeeds and stamps only what it saw.
	if err := store.MarkSourceAudioBuilt(ctx, "job-1", claimed, "digest-1"); err != nil {
		t.Fatalf("MarkSourceAudioBuilt: %v", err)
	}
	setJobState(t, store, "job-1", "done", "succeeded")

	if owed := owedJobIDs(t, store); len(owed) != 1 {
		t.Fatalf("an upload that landed mid-build was swallowed by that build's stamp; that audio would never reach a transcript (owed=%v)", owed)
	}
}

// R3. The intent is a column, so it is still there after a restart — and the
// dispatcher's first pass after startup is what re-detects it.
func TestSourceAudioDebtSurvivesARestart(t *testing.T) {
	t.Setenv("CASSINI_REPO_ROOT", filepath.Clean(filepath.Join("..", "..", "..")))
	path := filepath.Join(t.TempDir(), "jobs.sqlite3")
	ctx := context.Background()

	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	seedRecording(t, store, "job-1", "room-a", stamp(t, "2026-09-02T10:00:00Z"), stamp(t, "2026-09-02T11:00:00Z"))
	setJobState(t, store, "job-1", "done", "succeeded")
	if err := store.NoteSourceAudioUpload(ctx, "job-1", nowUTCString()); err != nil {
		t.Fatalf("NoteSourceAudioUpload: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()
	if owed := owedJobIDs(t, reopened); len(owed) != 1 || owed[0] != "job-1" {
		t.Fatalf("the rebuild was lost across a restart: %v", owed)
	}
}

// R3. A rebuild the operator died in the middle of comes back on its own. The
// startup sweep puts the job in 'interrupted', the build stamped nothing
// because it never succeeded, and the debt is still there — so the same scan
// that finds any other owed job finds this one, with no recovery path of its
// own to get wrong.
func TestAnInterruptedRebuildIsStillOwed(t *testing.T) {
	store := newRebuildTestStore(t)
	ctx := context.Background()
	seedRecording(t, store, "job-1", "room-a", stamp(t, "2026-09-02T10:00:00Z"), stamp(t, "2026-09-02T11:00:00Z"))
	setJobState(t, store, "job-1", "build", "running")
	if err := store.NoteSourceAudioUpload(ctx, "job-1", nowUTCString()); err != nil {
		t.Fatalf("NoteSourceAudioUpload: %v", err)
	}
	// A running build is not a candidate: it will read the counter itself.
	if owed := owedJobIDs(t, store); len(owed) != 0 {
		t.Fatalf("a running build was queued for a rebuild as well: %v", owed)
	}

	if _, err := store.MarkIncompleteJobsInterrupted(ctx, nowUTCString()); err != nil {
		t.Fatalf("MarkIncompleteJobsInterrupted: %v", err)
	}
	if owed := owedJobIDs(t, store); len(owed) != 1 || owed[0] != "job-1" {
		t.Fatalf("an interrupted rebuild was never re-detected: %v", owed)
	}
}

// A job that failed or was blocked has a reason of its own and an administrator
// to decide about it; re-running it quietly on the back of an upload would hide
// that. Its debt is not lost — an administrator's own rerun consumes it.
func TestFailedAndBlockedJobsAreNotRebuiltOnTheirOwn(t *testing.T) {
	store := newRebuildTestStore(t)
	ctx := context.Background()
	for _, state := range []string{"failed", "blocked"} {
		id := "job-" + state
		seedRecording(t, store, id, "room-"+state, stamp(t, "2026-09-02T10:00:00Z"), stamp(t, "2026-09-02T11:00:00Z"))
		if err := store.NoteSourceAudioUpload(ctx, id, nowUTCString()); err != nil {
			t.Fatalf("NoteSourceAudioUpload: %v", err)
		}
		setJobState(t, store, id, "done", state)
	}
	if owed := owedJobIDs(t, store); len(owed) != 0 {
		t.Fatalf("a job an administrator has to look at was rebuilt automatically: %v", owed)
	}
}

// R6. The counters ride on the job JSON, so a pending rebuild is visible
// without reading the container log.
func TestJobJSONReportsAPendingSourceAudioRebuild(t *testing.T) {
	store := newRebuildTestStore(t)
	ctx := context.Background()
	seedRecording(t, store, "job-1", "room-a", stamp(t, "2026-09-02T10:00:00Z"), stamp(t, "2026-09-02T11:00:00Z"))
	setJobState(t, store, "job-1", "done", "succeeded")

	job, err := store.GetJob(ctx, "job-1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.SourceAudioRebuild.Pending {
		t.Fatal("a job with no late upload reports a pending rebuild")
	}

	if err := store.NoteSourceAudioUpload(ctx, "job-1", nowUTCString()); err != nil {
		t.Fatalf("NoteSourceAudioUpload: %v", err)
	}
	job, err = store.GetJob(ctx, "job-1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if !job.SourceAudioRebuild.Pending || job.SourceAudioRebuild.UploadSeq != 1 || job.SourceAudioRebuild.BuiltSeq != 0 {
		t.Fatalf("job JSON does not report the owed rebuild: %+v", job.SourceAudioRebuild)
	}
	if job.SourceAudioRebuild.LastUploadAt == "" {
		t.Fatal("job JSON does not say when the late upload arrived")
	}

	// A job that has spent its ceiling reports honestly: audio is owed, and it
	// is not going to be rebuilt for.
	if _, err := store.db.Exec(`UPDATE jobs SET source_audio_rebuild_count = ? WHERE id = ?`, maxSourceAudioRebuilds, "job-1"); err != nil {
		t.Fatalf("set rebuild count: %v", err)
	}
	job, err = store.GetJob(ctx, "job-1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.SourceAudioRebuild.Pending {
		t.Fatal("a job past its rebuild ceiling still claims a rebuild is pending")
	}

	// And the same figures reach /operator/status.
	pending, run, err := store.CountSourceAudioRebuilds(ctx)
	if err != nil {
		t.Fatalf("CountSourceAudioRebuilds: %v", err)
	}
	if pending != 0 || run != maxSourceAudioRebuilds {
		t.Fatalf("status counters = pending %d run %d, want 0 and %d", pending, run, maxSourceAudioRebuilds)
	}
}

// A note for a job that is not there is not retried into the ground.
func TestNoteSourceAudioUploadReportsAMissingJob(t *testing.T) {
	store := newRebuildTestStore(t)
	if err := store.NoteSourceAudioUpload(context.Background(), "nope", nowUTCString()); !errors.Is(err, ErrNoSuchJob) {
		t.Fatalf("noting an upload for a job that does not exist = %v, want ErrNoSuchJob", err)
	}
}
