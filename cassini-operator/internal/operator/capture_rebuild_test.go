package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedRebuildCapture writes one promoted capture the way the upload handler
// leaves it, so the dispatcher's scan has something real to read.
func seedRebuildCapture(t *testing.T, root, room, owner string, callStartMS, callEndMS int64, segmentBytes int) string {
	t.Helper()
	dir := captureUploadDir(root, room, owner, callStartMS)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	sidecar := captureSidecar{
		Format:          captureSourceFormat,
		RoomToken:       room,
		ParticipantID:   owner,
		OwnerUserID:     owner,
		CallStartWallMS: callStartMS,
		CallEndWallMS:   callEndMS,
		ReceivedAt:      time.Now().UTC().Format(time.RFC3339),
		Segments: []captureSegment{{
			Index:       0,
			AudioName:   "segment-0.webm",
			MimeType:    "audio/webm;codecs=opus",
			StartWallMS: callStartMS,
			StopWallMS:  callEndMS,
		}},
	}
	body, err := json.Marshal(sidecar)
	if err != nil {
		t.Fatalf("marshal sidecar: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, captureSidecarName), body, 0o640); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "segment-0.webm"), make([]byte, segmentBytes), 0o640); err != nil {
		t.Fatalf("write segment: %v", err)
	}
	return dir
}

// R5. The digest is what tells "the same uploads the last build already read"
// from "audio this meeting has never been transcribed with", and it has to see
// a segment that grew as different even when every declared field matches.
func TestScanSourceCapturesSeesOnlyThisRecordingsAudio(t *testing.T) {
	root := t.TempDir()
	callStart := ms(t, "2026-09-02T10:05:00Z")
	callEnd := ms(t, "2026-09-02T10:55:00Z")
	window := captureRecordingWindow{StartMS: ms(t, "2026-09-02T10:00:00Z"), EndMS: ms(t, "2026-09-02T11:00:00Z")}

	seedRebuildCapture(t, root, "room-a", "alice", callStart, callEnd, 1024)
	set, err := scanSourceCapturesForRecording(root, "room-a", window)
	if err != nil {
		t.Fatalf("scanSourceCapturesForRecording: %v", err)
	}
	if set.Count != 1 || len(set.Owners) != 1 || set.Owners[0] != "alice" {
		t.Fatalf("scan = %+v, want alice's one capture", set)
	}
	first := set.Digest

	// A capture from another room, and one from another call in this room, are
	// not this recording's audio.
	seedRebuildCapture(t, root, "room-b", "alice", callStart, callEnd, 4096)
	seedRebuildCapture(t, root, "room-a", "alice", ms(t, "2026-09-03T10:05:00Z"), ms(t, "2026-09-03T10:55:00Z"), 4096)
	set, err = scanSourceCapturesForRecording(root, "room-a", window)
	if err != nil {
		t.Fatalf("scanSourceCapturesForRecording: %v", err)
	}
	if set.Count != 1 || set.Digest != first {
		t.Fatalf("a capture from another room or another call changed this recording's set: %+v", set)
	}

	// The set-aside copy of a capture being replaced sits at the same depth and
	// must be ignored, exactly as the build ignores it. Counting it would make
	// every promotion look like new audio.
	aside := captureUploadDir(root, "room-a", "alice", callStart) + captureSupersededSuffix
	if err := os.MkdirAll(aside, 0o750); err != nil {
		t.Fatalf("mkdir set-aside: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(captureUploadDir(root, "room-a", "alice", callStart), captureSidecarName))
	if err := os.WriteFile(filepath.Join(aside, captureSidecarName), body, 0o640); err != nil {
		t.Fatalf("write set-aside sidecar: %v", err)
	}
	set, err = scanSourceCapturesForRecording(root, "room-a", window)
	if err != nil {
		t.Fatalf("scanSourceCapturesForRecording: %v", err)
	}
	if set.Count != 1 || set.Digest != first {
		t.Fatalf("the set-aside copy was counted as a capture: %+v", set)
	}

	// A second participant is new audio.
	seedRebuildCapture(t, root, "room-a", "bob", callStart, callEnd, 2048)
	set, err = scanSourceCapturesForRecording(root, "room-a", window)
	if err != nil {
		t.Fatalf("scanSourceCapturesForRecording: %v", err)
	}
	if set.Count != 2 || set.Digest == first {
		t.Fatalf("a second participant's capture did not change the set: %+v", set)
	}
	withBob := set.Digest

	// And so is more of the SAME segment. A checkpointed sidecar describes a
	// segment that was still growing, so metadata alone compares equal while
	// the bytes differ; the size is the only thing that separates them.
	if err := os.WriteFile(filepath.Join(captureUploadDir(root, "room-a", "alice", callStart), "segment-0.webm"), make([]byte, 8192), 0o640); err != nil {
		t.Fatalf("grow segment: %v", err)
	}
	set, err = scanSourceCapturesForRecording(root, "room-a", window)
	if err != nil {
		t.Fatalf("scanSourceCapturesForRecording: %v", err)
	}
	if set.Digest == withBob {
		t.Fatal("a segment that grew compares equal to its own earlier prefix; a fuller upload would be treated as already built")
	}
}

// R3. A transient database error must not be the difference between a
// participant's audio reaching the transcript and being stored forever unread.
func TestSourceAudioWriteIsRetriedNotDropped(t *testing.T) {
	sourceAudioNoteBackoff = time.Millisecond
	t.Cleanup(func() { sourceAudioNoteBackoff = 100 * time.Millisecond })

	calls := 0
	err := retrySourceAudioWrite(context.Background(), func() error {
		calls++
		if calls < 3 {
			return errors.New("database is locked")
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("a transient error was not retried: err=%v after %d attempts", err, calls)
	}

	// Bounded, and the last error is reported rather than swallowed.
	calls = 0
	boom := errors.New("disk I/O error")
	err = retrySourceAudioWrite(context.Background(), func() error {
		calls++
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("a persistent error was reported as %v, want the underlying failure", err)
	}
	if calls != sourceAudioNoteAttempts {
		t.Fatalf("gave up after %d attempts, want %d", calls, sourceAudioNoteAttempts)
	}

	// A row that is not there is not retried into the ground: nothing about
	// waiting makes it appear.
	calls = 0
	if err := retrySourceAudioWrite(context.Background(), func() error {
		calls++
		return ErrNoSuchJob
	}); !errors.Is(err, ErrNoSuchJob) || calls != 1 {
		t.Fatalf("a missing job was retried %d times (err=%v)", calls, err)
	}
}

// rebuildRuntime is a runtime with a capture root and an instant quiet period,
// plus a recording that has already been built and published once.
func rebuildRuntime(t *testing.T) (*Runtime, func()) {
	t.Helper()
	rt, cleanup := newTestRuntime(t)
	rt.cfg.CaptureRoot = filepath.Join(t.TempDir(), "capture")
	rt.sourceAudioRebuildQuiet.Store(int64(time.Nanosecond))
	return rt, cleanup
}

// seedFinishedRecording puts a job in the store that has recorded, built and
// finished, with a ready run bundle a rerun can be built from.
func seedFinishedRecording(t *testing.T, rt *Runtime, id, room string, window captureRecordingWindow) {
	t.Helper()
	seedRecording(t, rt.store, id, room,
		formatUTCString(time.UnixMilli(window.StartMS).UTC()),
		formatUTCString(time.UnixMilli(window.EndMS).UTC()))

	runPath := attemptRunPath(rt.cfg.WorkRoot, id, 1)
	bundle, err := PrepareRunBundle(runPath, false)
	if err != nil {
		t.Fatalf("PrepareRunBundle: %v", err)
	}
	if err := os.WriteFile(bundle.RecordingPath, []byte("fake-mkv"), 0o644); err != nil {
		t.Fatalf("write recording: %v", err)
	}
	if err := FinalizeRunBundle(bundle, RunManifest{SourceMode: "talk"}); err != nil {
		t.Fatalf("FinalizeRunBundle: %v", err)
	}
	if _, err := rt.store.db.Exec(
		`UPDATE jobs SET stage = 'done', state = 'succeeded', artifact_run_path = ?, completed_at = ? WHERE id = ?`,
		bundle.RootDir, nowUTCString(), id); err != nil {
		t.Fatalf("finish job: %v", err)
	}
}

func jobAttemptCount(t *testing.T, rt *Runtime, id string) int {
	t.Helper()
	attempts, err := rt.store.ListJobAttempts(context.Background(), id)
	if err != nil {
		t.Fatalf("ListJobAttempts: %v", err)
	}
	return len(attempts)
}

// R1 and R4. The whole point: an upload lands after the meeting was built, and
// the operator transcribes it again — through the ordinary rerun path, as a new
// attempt that builds, seals and publishes its own artifacts. Nothing
// overwrites the published meeting in place, so a rebuild that dies half way
// leaves the previous one exactly as it was.
func TestALateUploadRebuildsTheMeetingAsANewAttempt(t *testing.T) {
	rt, cleanup := rebuildRuntime(t)
	defer cleanup()
	ctx := context.Background()

	window := captureRecordingWindow{StartMS: ms(t, "2026-09-02T10:00:00Z"), EndMS: ms(t, "2026-09-02T11:00:00Z")}
	seedFinishedRecording(t, rt, "job-1", "room-a", window)
	seedRebuildCapture(t, rt.cfg.CaptureRoot, "room-a", "alice",
		ms(t, "2026-09-02T10:05:00Z"), ms(t, "2026-09-02T10:55:00Z"), 1024)
	if err := rt.store.NoteSourceAudioUpload(ctx, "job-1", nowUTCString()); err != nil {
		t.Fatalf("NoteSourceAudioUpload: %v", err)
	}
	if before := jobAttemptCount(t, rt, "job-1"); before != 1 {
		t.Fatalf("the seeded job already has %d attempts", before)
	}

	rt.dispatchSourceAudioRebuilds()

	job := waitForJobState(t, rt.store, "job-1", "succeeded")
	if job.CurrentAttemptNumber != 2 {
		t.Fatalf("current_attempt_number = %d, want the rebuild to be attempt 2", job.CurrentAttemptNumber)
	}
	if jobAttemptCount(t, rt, "job-1") != 2 {
		t.Fatalf("the rebuild did not run as its own attempt: %d attempts", jobAttemptCount(t, rt, "job-1"))
	}
	attempts, err := rt.store.ListJobAttempts(ctx, "job-1")
	if err != nil {
		t.Fatalf("ListJobAttempts: %v", err)
	}
	if attempts[0].TriggerKind != "rerun" {
		t.Fatalf("the rebuild attempt's trigger = %q, want rerun", attempts[0].TriggerKind)
	}
	// Its own artifacts. The rebuild seals an immutable `.opus` of its own and
	// publishes that (D-583); nothing rewrites the bytes the first attempt
	// published, so a rebuild that dies half way cannot leave a truncated
	// meeting where a good one was.
	wantOpus := attemptOpusPath(rt.cfg.WorkRoot, "job-1", 2)
	if attempts[0].ArtifactOpusPath == nil || *attempts[0].ArtifactOpusPath != wantOpus {
		t.Fatalf("the rebuild did not seal its own portable meeting: %v, want %s",
			attempts[0].ArtifactOpusPath, wantOpus)
	}
	if attempts[0].ArtifactMeetingPath == nil ||
		*attempts[0].ArtifactMeetingPath != attemptMeetingPath(rt.cfg.WorkRoot, "job-1", 2) {
		t.Fatalf("the rebuild built over an earlier attempt's meeting bundle: %v", attempts[0].ArtifactMeetingPath)
	}
	// The debt is settled by the build that consumed it, so the next pass does
	// nothing.
	job, err = rt.store.GetJob(ctx, "job-1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.SourceAudioRebuild.Pending {
		t.Fatalf("the rebuild ran but the meeting is still owed one: %+v", job.SourceAudioRebuild)
	}
	if job.SourceAudioRebuild.RebuildCount != 1 {
		t.Fatalf("rebuild_count = %d, want 1", job.SourceAudioRebuild.RebuildCount)
	}
}

// R1. While the first build is still running, the rebuild is owed but not
// dispatched: it waits for the job to be terminal and is picked up by a later
// scan. Nothing runs two builds of one meeting at once.
func TestARebuildWaitsForTheRunningBuildToFinish(t *testing.T) {
	rt, cleanup := rebuildRuntime(t)
	defer cleanup()
	ctx := context.Background()

	window := captureRecordingWindow{StartMS: ms(t, "2026-09-02T10:00:00Z"), EndMS: ms(t, "2026-09-02T11:00:00Z")}
	seedFinishedRecording(t, rt, "job-1", "room-a", window)
	seedRebuildCapture(t, rt.cfg.CaptureRoot, "room-a", "alice",
		ms(t, "2026-09-02T10:05:00Z"), ms(t, "2026-09-02T10:55:00Z"), 1024)
	// Put the job back mid-build, which is where a late upload usually finds it.
	setJobState(t, rt.store, "job-1", "build", "running")
	if err := rt.store.NoteSourceAudioUpload(ctx, "job-1", nowUTCString()); err != nil {
		t.Fatalf("NoteSourceAudioUpload: %v", err)
	}

	rt.dispatchSourceAudioRebuilds()
	if got := jobAttemptCount(t, rt, "job-1"); got != 1 {
		t.Fatalf("a rebuild was queued while the build was still running: %d attempts", got)
	}
	job, err := rt.store.GetJob(ctx, "job-1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if !job.SourceAudioRebuild.Pending {
		t.Fatal("the rebuild was dropped rather than deferred; a slow uploader would lose their audio to a build that was merely busy")
	}

	// The build finishes. The next scan picks the rebuild up.
	setJobState(t, rt.store, "job-1", "done", "succeeded")
	rt.dispatchSourceAudioRebuilds()
	waitForJobState(t, rt.store, "job-1", "succeeded")
	if got := jobAttemptCount(t, rt, "job-1"); got != 2 {
		t.Fatalf("the deferred rebuild never ran: %d attempts", got)
	}
}

// R1. Four participants finish uploading within a minute of each other. That is
// one rebuild, not four: acting on the first arrival would transcribe the
// meeting again for each of the others in turn.
func TestAWaveOfUploadsCoalescesIntoOneRebuild(t *testing.T) {
	rt, cleanup := rebuildRuntime(t)
	defer cleanup()
	ctx := context.Background()
	rt.sourceAudioRebuildQuiet.Store(int64(time.Hour))

	window := captureRecordingWindow{StartMS: ms(t, "2026-09-02T10:00:00Z"), EndMS: ms(t, "2026-09-02T11:00:00Z")}
	seedFinishedRecording(t, rt, "job-1", "room-a", window)
	for i, owner := range []string{"alice", "bob", "carol", "dave"} {
		seedRebuildCapture(t, rt.cfg.CaptureRoot, "room-a", owner,
			ms(t, "2026-09-02T10:05:00Z")+int64(i), ms(t, "2026-09-02T10:55:00Z"), 1024)
		if err := rt.store.NoteSourceAudioUpload(ctx, "job-1", nowUTCString()); err != nil {
			t.Fatalf("NoteSourceAudioUpload: %v", err)
		}
		// Each arrival is judged; none of them may act while the wave is still
		// arriving.
		rt.dispatchSourceAudioRebuilds()
		if got := jobAttemptCount(t, rt, "job-1"); got != 1 {
			t.Fatalf("upload %d rebuilt the meeting while the wave was still arriving: %d attempts", i, got)
		}
	}

	// The room goes quiet. One rebuild covers all four.
	rt.sourceAudioRebuildQuiet.Store(int64(time.Nanosecond))
	rt.dispatchSourceAudioRebuilds()
	waitForJobState(t, rt.store, "job-1", "succeeded")
	if got := jobAttemptCount(t, rt, "job-1"); got != 2 {
		t.Fatalf("four uploads produced %d attempts, want one rebuild", got-1)
	}
	job, err := rt.store.GetJob(ctx, "job-1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.SourceAudioRebuild.RebuildCount != 1 {
		t.Fatalf("rebuild_count = %d, want one rebuild for the whole wave", job.SourceAudioRebuild.RebuildCount)
	}
}

// R5. A rebuild that would read exactly the bytes the last one read is a logged
// no-op. Republishing a byte-identical meeting costs a full transcription and
// changes nothing an audience can see.
func TestARebuildFindingTheSameCapturesIsANoOp(t *testing.T) {
	rt, cleanup := rebuildRuntime(t)
	defer cleanup()
	ctx := context.Background()

	window := captureRecordingWindow{StartMS: ms(t, "2026-09-02T10:00:00Z"), EndMS: ms(t, "2026-09-02T11:00:00Z")}
	seedFinishedRecording(t, rt, "job-1", "room-a", window)
	seedRebuildCapture(t, rt.cfg.CaptureRoot, "room-a", "alice",
		ms(t, "2026-09-02T10:05:00Z"), ms(t, "2026-09-02T10:55:00Z"), 1024)

	// Pretend the last successful build already consumed exactly this set.
	set, err := scanSourceCapturesForRecording(rt.cfg.CaptureRoot, "room-a", window)
	if err != nil {
		t.Fatalf("scanSourceCapturesForRecording: %v", err)
	}
	if err := rt.store.NoteSourceAudioUpload(ctx, "job-1", nowUTCString()); err != nil {
		t.Fatalf("NoteSourceAudioUpload: %v", err)
	}
	if err := rt.store.MarkSourceAudioBuilt(ctx, "job-1", 0, set.Digest); err != nil {
		t.Fatalf("MarkSourceAudioBuilt: %v", err)
	}

	rt.dispatchSourceAudioRebuilds()
	if got := jobAttemptCount(t, rt, "job-1"); got != 1 {
		t.Fatalf("a rebuild republished a byte-identical meeting: %d attempts", got)
	}
	job, err := rt.store.GetJob(ctx, "job-1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.SourceAudioRebuild.Pending {
		t.Fatal("the no-op left the debt owed, so it would be re-decided every fifteen seconds for ever")
	}
}

// R5. If the retention sweep took the capture between the upload and the
// rebuild, rebuilding now would replace a meeting that HAS the audio with one
// that does not.
func TestARebuildWithNoCaptureLeftOnDiskIsRefused(t *testing.T) {
	rt, cleanup := rebuildRuntime(t)
	defer cleanup()
	ctx := context.Background()

	window := captureRecordingWindow{StartMS: ms(t, "2026-09-02T10:00:00Z"), EndMS: ms(t, "2026-09-02T11:00:00Z")}
	seedFinishedRecording(t, rt, "job-1", "room-a", window)
	if err := rt.store.NoteSourceAudioUpload(ctx, "job-1", nowUTCString()); err != nil {
		t.Fatalf("NoteSourceAudioUpload: %v", err)
	}

	rt.dispatchSourceAudioRebuilds()
	if got := jobAttemptCount(t, rt, "job-1"); got != 1 {
		t.Fatalf("a meeting was rebuilt from a capture that is no longer there: %d attempts", got)
	}
	job, err := rt.store.GetJob(ctx, "job-1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.SourceAudioRebuild.Pending {
		t.Fatal("the refusal left the debt owed, so it would be re-decided for ever")
	}
}

// R5. A meeting older than the window the capture sweep enforces is not
// rebuilt: its audio is either already gone or about to be deleted underneath
// the build.
func TestARebuildPastTheRetentionWindowIsRefused(t *testing.T) {
	rt, cleanup := rebuildRuntime(t)
	defer cleanup()
	ctx := context.Background()
	t.Setenv(envCaptureMaxAgeHours, "24")

	old := time.Now().UTC().Add(-72 * time.Hour)
	window := captureRecordingWindow{StartMS: old.UnixMilli(), EndMS: old.Add(time.Hour).UnixMilli()}
	seedFinishedRecording(t, rt, "job-1", "room-a", window)
	seedRebuildCapture(t, rt.cfg.CaptureRoot, "room-a", "alice",
		window.StartMS+60_000, window.EndMS-60_000, 1024)
	if err := rt.store.NoteSourceAudioUpload(ctx, "job-1", nowUTCString()); err != nil {
		t.Fatalf("NoteSourceAudioUpload: %v", err)
	}

	rt.dispatchSourceAudioRebuilds()
	if got := jobAttemptCount(t, rt, "job-1"); got != 1 {
		t.Fatalf("a meeting past the capture retention window was rebuilt: %d attempts", got)
	}
	job, err := rt.store.GetJob(ctx, "job-1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.SourceAudioRebuild.Pending {
		t.Fatal("the refusal left the debt owed, so it would be re-decided every fifteen seconds for ever")
	}
}

// A recording that never produced a run bundle — an interrupted RECORDING, as
// opposed to an interrupted rebuild — can never be rebuilt from. Left owed it
// would be re-decided on every scan for the life of the installation, and would
// hold one of the scan's slots against every meeting that could be rebuilt.
func TestARecordingWithNoRunBundleSettlesRatherThanLooping(t *testing.T) {
	logs := &syncBuffer{}
	rt, cleanup := newTestRuntimeWithLogger(t, log.New(logs, "", 0))
	defer cleanup()
	rt.cfg.CaptureRoot = filepath.Join(t.TempDir(), "capture")
	rt.sourceAudioRebuildQuiet.Store(int64(time.Nanosecond))
	ctx := context.Background()

	seedRecording(t, rt.store, "job-1", "room-a", stamp(t, "2026-09-02T10:00:00Z"), stamp(t, "2026-09-02T11:00:00Z"))
	seedRebuildCapture(t, rt.cfg.CaptureRoot, "room-a", "alice",
		ms(t, "2026-09-02T10:05:00Z"), ms(t, "2026-09-02T10:55:00Z"), 1024)
	if err := rt.store.NoteSourceAudioUpload(ctx, "job-1", nowUTCString()); err != nil {
		t.Fatalf("NoteSourceAudioUpload: %v", err)
	}
	// The recorder died with the operator: interrupted, and with no bundle.
	setJobState(t, rt.store, "job-1", "record", "interrupted")

	rt.dispatchSourceAudioRebuilds()
	job, err := rt.store.GetJob(ctx, "job-1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.SourceAudioRebuild.Pending {
		t.Fatal("a recording that can never be rebuilt stayed owed for ever")
	}
	if got := logs.String(); !strings.Contains(got, "no run bundle to rebuild from") {
		t.Fatalf("the refusal is not in the log, so nobody can act on it: %q", got)
	}
}

// R5 from the other side: a job that has spent its ceiling must not occupy the
// scan's candidate list. Its debt is deliberately never settled, so a handful
// of them would otherwise fill the scan and starve every other meeting in the
// installation of its rebuild.
func TestCappedJobsDoNotStarveTheRebuildScan(t *testing.T) {
	rt, cleanup := rebuildRuntime(t)
	defer cleanup()
	ctx := context.Background()

	// More capped jobs than the scan's own limit, all older than the one that
	// still has an allowance, so they sort ahead of it.
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("spent-%02d", i)
		seedRecording(t, rt.store, id, fmt.Sprintf("room-spent-%02d", i),
			stamp(t, "2026-09-01T10:00:00Z"), stamp(t, "2026-09-01T11:00:00Z"))
		setJobState(t, rt.store, id, "done", "succeeded")
		if err := rt.store.NoteSourceAudioUpload(ctx, id, stamp(t, "2026-09-01T12:00:00Z")); err != nil {
			t.Fatalf("NoteSourceAudioUpload: %v", err)
		}
		if _, err := rt.store.db.Exec(
			`UPDATE jobs SET source_audio_rebuild_count = ?, updated_at = ? WHERE id = ?`,
			maxSourceAudioRebuilds, stamp(t, "2026-09-01T12:00:00Z"), id); err != nil {
			t.Fatalf("spend the ceiling: %v", err)
		}
	}

	window := captureRecordingWindow{StartMS: ms(t, "2026-09-02T10:00:00Z"), EndMS: ms(t, "2026-09-02T11:00:00Z")}
	seedFinishedRecording(t, rt, "job-live", "room-live", window)
	seedRebuildCapture(t, rt.cfg.CaptureRoot, "room-live", "alice",
		ms(t, "2026-09-02T10:05:00Z"), ms(t, "2026-09-02T10:55:00Z"), 1024)
	if err := rt.store.NoteSourceAudioUpload(ctx, "job-live", nowUTCString()); err != nil {
		t.Fatalf("NoteSourceAudioUpload: %v", err)
	}

	rt.dispatchSourceAudioRebuilds()
	waitForJobState(t, rt.store, "job-live", "succeeded")
	if got := jobAttemptCount(t, rt, "job-live"); got != 2 {
		t.Fatalf("twenty meetings that had spent their ceiling starved the one that had not: %d attempts", got)
	}
}

// R1/R5. With ingestion off the build is not given the capture root at all, so
// it must not stamp the counter: doing so would record that a build had read
// audio it was never shown, and turning ingestion on afterwards would find no
// debt and never use it.
func TestABuildWithIngestionOffDoesNotConsumeTheDebt(t *testing.T) {
	rt, cleanup := rebuildRuntime(t)
	defer cleanup()
	ctx := context.Background()
	t.Setenv(envSourceAudioIngestEnabled, "0")

	window := captureRecordingWindow{StartMS: ms(t, "2026-09-02T10:00:00Z"), EndMS: ms(t, "2026-09-02T11:00:00Z")}
	seedFinishedRecording(t, rt, "job-1", "room-a", window)
	seedRebuildCapture(t, rt.cfg.CaptureRoot, "room-a", "alice",
		ms(t, "2026-09-02T10:05:00Z"), ms(t, "2026-09-02T10:55:00Z"), 1024)
	if err := rt.store.NoteSourceAudioUpload(ctx, "job-1", nowUTCString()); err != nil {
		t.Fatalf("NoteSourceAudioUpload: %v", err)
	}

	seq, digest := rt.sourceAudioConsumption("job-1")
	if seq != 0 || digest != "" {
		t.Fatalf("a build with ingestion off claimed to consume %d uploads (digest %q)", seq, digest)
	}
	job, err := rt.store.GetJob(ctx, "job-1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if !job.SourceAudioRebuild.Pending {
		t.Fatal("the debt was consumed with ingestion off; turning it on later would never use the audio")
	}
}

// R5. At most maxSourceAudioRebuilds per meeting. Beyond that something is
// wrong that another transcription will not fix, and each one is a full GPU
// pass on a box that also has to record the next meeting.
func TestRebuildsAreCappedPerMeeting(t *testing.T) {
	rt, cleanup := rebuildRuntime(t)
	defer cleanup()
	ctx := context.Background()

	window := captureRecordingWindow{StartMS: ms(t, "2026-09-02T10:00:00Z"), EndMS: ms(t, "2026-09-02T11:00:00Z")}
	seedFinishedRecording(t, rt, "job-1", "room-a", window)

	for round := 1; round <= maxSourceAudioRebuilds+1; round++ {
		// Each round brings genuinely different audio, so nothing but the cap
		// can stop the rebuild.
		seedRebuildCapture(t, rt.cfg.CaptureRoot, "room-a", fmt.Sprintf("owner-%d", round),
			ms(t, "2026-09-02T10:05:00Z")+int64(round), ms(t, "2026-09-02T10:55:00Z"), 1024*round)
		if err := rt.store.NoteSourceAudioUpload(ctx, "job-1", nowUTCString()); err != nil {
			t.Fatalf("NoteSourceAudioUpload: %v", err)
		}
		rt.dispatchSourceAudioRebuilds()
		waitForJobState(t, rt.store, "job-1", "succeeded")

		want := round + 1
		if round > maxSourceAudioRebuilds {
			want = maxSourceAudioRebuilds + 1
		}
		if got := jobAttemptCount(t, rt, "job-1"); got != want {
			t.Fatalf("round %d: %d attempts, want %d (the ceiling is %d rebuilds)", round, got, want, maxSourceAudioRebuilds)
		}
	}
	job, err := rt.store.GetJob(ctx, "job-1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.SourceAudioRebuild.RebuildCount != maxSourceAudioRebuilds {
		t.Fatalf("rebuild_count = %d, want the ceiling %d", job.SourceAudioRebuild.RebuildCount, maxSourceAudioRebuilds)
	}
	if job.SourceAudioRebuild.Pending {
		t.Fatal("a job past its ceiling still claims a rebuild is pending")
	}
}

// With ingestion off a rebuild would read the capture and then not use it,
// republishing a byte-identical meeting. The debt stays owed, so turning
// ingestion on later still picks it up.
func TestNoRebuildWhileIngestionIsOff(t *testing.T) {
	rt, cleanup := rebuildRuntime(t)
	defer cleanup()
	ctx := context.Background()
	t.Setenv(envSourceAudioIngestEnabled, "0")

	window := captureRecordingWindow{StartMS: ms(t, "2026-09-02T10:00:00Z"), EndMS: ms(t, "2026-09-02T11:00:00Z")}
	seedFinishedRecording(t, rt, "job-1", "room-a", window)
	seedRebuildCapture(t, rt.cfg.CaptureRoot, "room-a", "alice",
		ms(t, "2026-09-02T10:05:00Z"), ms(t, "2026-09-02T10:55:00Z"), 1024)
	if err := rt.store.NoteSourceAudioUpload(ctx, "job-1", nowUTCString()); err != nil {
		t.Fatalf("NoteSourceAudioUpload: %v", err)
	}

	rt.dispatchSourceAudioRebuilds()
	if got := jobAttemptCount(t, rt, "job-1"); got != 1 {
		t.Fatalf("a meeting was rebuilt with ingestion off: %d attempts", got)
	}
	job, err := rt.store.GetJob(ctx, "job-1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if !job.SourceAudioRebuild.Pending {
		t.Fatal("the debt was settled while ingestion was off; turning it on later would never use the audio")
	}
}

// R2. An upload arriving for a room with two overlapping recordings schedules
// nothing and says why. Choosing one would put a meeting's speech in another
// meeting's transcript, and no later correction undoes that.
func TestAnAmbiguousUploadSchedulesNothingAndSaysSo(t *testing.T) {
	logs := &syncBuffer{}
	rt, cleanup := newTestRuntimeWithLogger(t, log.New(logs, "", 0))
	defer cleanup()
	rt.cfg.CaptureRoot = filepath.Join(t.TempDir(), "capture")
	ctx := context.Background()

	seedRecording(t, rt.store, "first", "room-a", stamp(t, "2026-09-02T10:00:00Z"), stamp(t, "2026-09-02T11:00:00Z"))
	seedRecording(t, rt.store, "second", "room-a", stamp(t, "2026-09-02T10:30:00Z"), stamp(t, "2026-09-02T11:30:00Z"))
	setJobState(t, rt.store, "first", "done", "succeeded")
	setJobState(t, rt.store, "second", "done", "succeeded")

	sidecar := captureSidecar{
		Format: captureSourceFormat, RoomToken: "room-a", OwnerUserID: "alice",
		CallStartWallMS: ms(t, "2026-09-02T10:40:00Z"), CallEndWallMS: ms(t, "2026-09-02T10:50:00Z"),
	}
	rt.noteCaptureArrival(&sidecar, "alice", rt.logger)

	for _, id := range []string{"first", "second"} {
		job, err := rt.store.GetJob(ctx, id)
		if err != nil {
			t.Fatalf("GetJob(%s): %v", id, err)
		}
		if job.SourceAudioRebuild.UploadSeq != 0 {
			t.Fatalf("an ambiguous upload was attributed to job %s anyway", id)
		}
	}
	if got := logs.String(); !strings.Contains(got, "matches more than one recording") {
		t.Fatalf("the refusal is not in the log, so nobody can act on it: %q", got)
	}
}

// An upload for a call this operator never recorded is stored and says so. It
// is the ordinary case for a room whose recording predates the Talk binding,
// not a failure.
func TestAnUnmatchedUploadIsStoredWithoutARebuild(t *testing.T) {
	logs := &syncBuffer{}
	rt, cleanup := newTestRuntimeWithLogger(t, log.New(logs, "", 0))
	defer cleanup()
	rt.cfg.CaptureRoot = filepath.Join(t.TempDir(), "capture")

	sidecar := captureSidecar{
		Format: captureSourceFormat, RoomToken: "room-nobody-recorded", OwnerUserID: "alice",
		CallStartWallMS: ms(t, "2026-09-02T10:40:00Z"), CallEndWallMS: ms(t, "2026-09-02T10:50:00Z"),
	}
	rt.noteCaptureArrival(&sidecar, "alice", rt.logger)
	if got := logs.String(); !strings.Contains(got, "no recording matches") {
		t.Fatalf("an unmatched upload left no trace: %q", got)
	}
}

// The happy path of attribution: an accepted upload finds its recording and is
// owed a rebuild, with the room and the call window both doing work.
func TestAnAcceptedUploadIsAttributedToItsRecording(t *testing.T) {
	rt, cleanup := rebuildRuntime(t)
	defer cleanup()
	ctx := context.Background()

	seedRecording(t, rt.store, "job-1", "room-a", stamp(t, "2026-09-02T10:00:00Z"), stamp(t, "2026-09-02T11:00:00Z"))
	setJobState(t, rt.store, "job-1", "done", "succeeded")

	sidecar := captureSidecar{
		Format: captureSourceFormat, RoomToken: "room-a", OwnerUserID: "alice",
		CallStartWallMS: ms(t, "2026-09-02T10:05:00Z"), CallEndWallMS: ms(t, "2026-09-02T10:55:00Z"),
	}
	rt.noteCaptureArrival(&sidecar, "alice", rt.logger)

	job, err := rt.store.GetJob(ctx, "job-1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if !job.SourceAudioRebuild.Pending || job.SourceAudioRebuild.UploadSeq != 1 {
		t.Fatalf("an accepted upload was not attributed to its recording: %+v", job.SourceAudioRebuild)
	}
}
