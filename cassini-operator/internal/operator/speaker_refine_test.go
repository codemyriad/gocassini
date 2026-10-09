package operator

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeSpeakersCLI stands in for `cassini speakers diarize|apply`. Every call is
// appended to "$0.calls". diarize writes a turn set numbered by how many
// diarizations ran so far, so a test can tell which run's turns an apply got;
// apply copies the edits and the turns it was handed into the bundle and
// prints a report. "$0.diarize-unavailable" and "$0.apply-fails" make the
// matching command fail the way the real CLI does. Each diarize appends the
// CASSINI_DIARIZATION_THREADS it was given to "$0.threads".
func fakeSpeakersCLI(t *testing.T) string {
	t.Helper()
	return writeFakeCassini(t, `echo "$*" >> "$0.calls"
[ "$1" = speakers ] || { echo "unexpected command: $*" >&2; exit 1; }
sub="$2"; shift 2
target="$1"; shift
while [ $# -gt 0 ]; do
  case "$1" in
    --speaker) spk="$2"; shift 2 ;;
    --out) out="$2"; shift 2 ;;
    --edits) edits="$2"; shift 2 ;;
    --turns-dir) turns="$2"; shift 2 ;;
    *) shift ;;
  esac
done
case "$sub" in
  diarize)
    echo "${CASSINI_DIARIZATION_THREADS:-unset}" >> "$0.threads"
    if [ -f "$0.diarize-unavailable" ]; then
      echo "diarization-unavailable: no Nemotron model at /models" >&2; exit 3
    fi
    n=$(grep -c '^speakers diarize' "$0.calls")
    printf '{"format":"cassini.speaker-turns.v1","speakerId":"%s","source":{"file":"recording.mkv","sha256":"src%s"},"model":{"name":"m","sha256":"model"},"turns":[{"diarization":%s}]}' "$spk" "$n" "$n" > "$out"
    ;;
  apply)
    if [ -f "$0.apply-fails" ]; then echo "apply exploded" >&2; exit 1; fi
    cp "$edits" "$target/speaker-edits.json"
    cat "$turns"/*.json > "$target/turns-used.json" 2>/dev/null || : > "$target/turns-used.json"
    rev=$(sed -n 's/.*"revision":\([0-9]*\).*/\1/p' "$edits")
    printf '{"revision":%s,"splits":[],"missing":[],"inconclusive":[],"merged":0,"speakerCount":3}\n' "$rev"
    ;;
  *) echo "unexpected speakers command: $sub" >&2; exit 2 ;;
esac
`)
}

func speakersCalls(t *testing.T, bin, prefix string) []string {
	t.Helper()
	raw, err := os.ReadFile(bin + ".calls")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.HasPrefix(line, prefix) {
			calls = append(calls, line)
		}
	}
	return calls
}

// newSpeakerRuntime is newTestRuntime with the speaker-edits seam wired the
// way run.go wires it, a stand-in CLI, and one published job. A refine must
// never run the ASR build, so the wrapped build fails the test if it is called.
func newSpeakerRuntime(t *testing.T, jobID string) (*Runtime, string) {
	t.Helper()
	rt, cleanup := newTestRuntime(t)
	t.Cleanup(cleanup)
	bin := fakeSpeakersCLI(t)
	rt.cfg.CassiniBin = bin
	rt.buildJobFn = rt.withSpeakerEdits(func(ctx context.Context, task buildTask) (string, error) {
		t.Errorf("attempt %d ran an ASR build", task.AttemptNumber)
		return "", errors.New("unexpected build")
	})
	seedSpeakerJob(t, rt.store, rt.cfg.WorkRoot, jobID)
	return rt, bin
}

// queueSpeakerEdits saves doc the way a person does once the page shows the
// last edit applied or failed. A worker that recorded that state may still
// hold the job's artifacts (a publish promotes and prunes after it marks
// the attempt succeeded) and a save then answers busy, so this first waits
// for the worker to let go: taking the lock blocks until it does.
func queueSpeakerEdits(t *testing.T, rt *Runtime, jobID string, expect int, doc speakerEditsDoc) {
	t.Helper()
	rt.store.lockArtifacts(jobID)()
	if _, err := rt.store.QueueSpeakerEdits(context.Background(), jobID, expect, doc, "alice", nowUTCString()); err != nil {
		t.Fatalf("QueueSpeakerEdits() error = %v", err)
	}
	rt.dispatchQueuedRefine(jobID)
}

func TestRefineDiarizesOnceAppliesAndPublishes(t *testing.T) {
	rt, bin := newSpeakerRuntime(t, "JOB1")
	ctx := context.Background()
	webmBefore := annTestRead(t, filepath.Join(canonicalMeetingPath(rt.cfg.WorkRoot, "JOB1"), "meeting.webm"))

	doc := splitDoc(speakerTestRoom)
	doc.Labels = []speakerEditsLabel{{SpeakerID: speakerTestRoom + "~1", Label: "Ann"}}
	queueSpeakerEdits(t, rt, "JOB1", 0, doc)
	rec := waitForSpeakerEdits(t, rt.store, "JOB1", func(r speakerEditsRecord) bool { return r.AppliedRevision == 1 })
	job := waitForJobState(t, rt.store, "JOB1", "succeeded")
	if job.CurrentAttemptNumber != 2 {
		t.Fatalf("current attempt = %d, want the refine (2)", job.CurrentAttemptNumber)
	}
	waitForPublishedAttempt(t, rt, "JOB1", 2)
	if rec.LastError != "" || !strings.Contains(string(rec.LastReport), `"revision":1`) {
		t.Fatalf("after publish: lastError=%q report=%s", rec.LastError, rec.LastReport)
	}

	diarize := speakersCalls(t, bin, "speakers diarize")
	if len(diarize) != 1 || !strings.Contains(diarize[0], canonicalRunPath(rt.cfg.WorkRoot, "JOB1")) || !strings.Contains(diarize[0], "--speaker "+speakerTestRoom) {
		t.Fatalf("diarize calls = %v, want one over the job's capture for %s", diarize, speakerTestRoom)
	}
	turns, ok, err := rt.store.SpeakerSplitTurns(ctx, "JOB1", speakerTestRoom)
	if err != nil || !ok || !strings.Contains(turns, `"diarization":1`) {
		t.Fatalf("stored turns = %q %v %v", turns, ok, err)
	}
	var modelSHA, sourceSHA string
	if err := rt.store.db.QueryRow(`SELECT model_sha256, source_sha256 FROM speaker_split_turns WHERE job_id = 'JOB1'`).Scan(&modelSHA, &sourceSHA); err != nil || modelSHA != "model" || sourceSHA != "src1" {
		t.Fatalf("turn provenance = %q %q %v", modelSHA, sourceSHA, err)
	}
	apply := speakersCalls(t, bin, "speakers apply")
	if len(apply) != 1 || !strings.HasPrefix(apply[0], "speakers apply "+attemptMeetingPath(rt.cfg.WorkRoot, "JOB1", 2)+" ") || !strings.HasSuffix(apply[0], "--json") {
		t.Fatalf("apply calls = %v, want one on the refine's own bundle", apply)
	}
	// The CLI refuses stored turns measured on any other recording.
	if !strings.Contains(apply[0], " --recording "+canonicalRunPath(rt.cfg.WorkRoot, "JOB1")+" ") {
		t.Fatalf("apply call %q does not name the capture the turns must come from", apply[0])
	}

	// The refined bundle is what was promoted, and it is the same audio.
	current := canonicalMeetingPath(rt.cfg.WorkRoot, "JOB1")
	var applied speakerEditsDoc
	if err := json.Unmarshal([]byte(annTestRead(t, filepath.Join(current, "speaker-edits.json"))), &applied); err != nil {
		t.Fatal(err)
	}
	if applied.Format != speakerEditsFormat || applied.Revision != 1 || len(applied.Splits) != 1 || applied.Labels[0].Label != "Ann" {
		t.Fatalf("apply was handed %+v, want the revision 1 snapshot", applied)
	}
	if got := annTestRead(t, filepath.Join(current, "meeting.webm")); got != webmBefore {
		t.Fatal("the refine changed the meeting audio")
	}

	state, err := rt.speakerEditsState(ctx, "JOB1")
	if err != nil || state.State != speakerStateIdle || state.AppliedRevision != 1 || state.Revision != 1 {
		t.Fatalf("state after publish = %+v, %v", state, err)
	}

	// A second edit names a voice. The participant was diarized already, so
	// its turns are reused — voice ids must not move — and nothing new runs.
	doc.Labels = append(doc.Labels, speakerEditsLabel{SpeakerID: speakerTestRoom + "~2", Label: "Bea"})
	queueSpeakerEdits(t, rt, "JOB1", 1, doc)
	waitForSpeakerEdits(t, rt.store, "JOB1", func(r speakerEditsRecord) bool { return r.AppliedRevision == 2 })
	waitForPublishedAttempt(t, rt, "JOB1", 3)
	if got := len(speakersCalls(t, bin, "speakers diarize")); got != 1 {
		t.Fatalf("diarize ran %d times, want the stored turns reused", got)
	}
	if got := annTestRead(t, filepath.Join(canonicalMeetingPath(rt.cfg.WorkRoot, "JOB1"), "turns-used.json")); got != turns {
		t.Fatalf("second apply got turns %q, want the stored %q", got, turns)
	}
	if got := len(speakersCalls(t, bin, "speakers apply")); got != 2 {
		t.Fatalf("apply ran %d times, want 2", got)
	}
}

func TestRefineFailureIsReportedAndLeavesThePublishedMeeting(t *testing.T) {
	rt, bin := newSpeakerRuntime(t, "JOB1")
	if err := os.WriteFile(bin+".apply-fails", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	queueSpeakerEdits(t, rt, "JOB1", 0, splitDoc(speakerTestRoom))
	job := waitForJobState(t, rt.store, "JOB1", "failed")
	if job.Error == nil || !strings.Contains(*job.Error, "apply exploded") {
		t.Fatalf("job error = %v", jobErrorText(job))
	}
	rec, err := rt.store.GetSpeakerEdits(context.Background(), "JOB1")
	if err != nil || rec.AppliedRevision != 0 || !strings.Contains(rec.LastError, "apply exploded") {
		t.Fatalf("edits after failure = %+v, %v", rec, err)
	}
	state, err := rt.speakerEditsState(context.Background(), "JOB1")
	// Every reader of the meeting sees the state: the CLI's own words, with
	// the operator's paths in them, stay in the record and the log.
	if err != nil || state.State != speakerStateFailed || state.LastError != "The recording could not be updated." {
		t.Fatalf("state after failure = %+v, %v", state, err)
	}
	if _, err := os.Stat(filepath.Join(canonicalMeetingPath(rt.cfg.WorkRoot, "JOB1"), "speaker-edits.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed refine reached the current meeting: %v", err)
	}

	// A failed job is idle: the same edits can be sent again, and succeed.
	if err := os.Remove(bin + ".apply-fails"); err != nil {
		t.Fatal(err)
	}
	queueSpeakerEdits(t, rt, "JOB1", 1, splitDoc(speakerTestRoom))
	rec = waitForSpeakerEdits(t, rt.store, "JOB1", func(r speakerEditsRecord) bool { return r.AppliedRevision == 2 })
	if rec.LastError != "" {
		t.Fatalf("lastError = %q after a successful retry", rec.LastError)
	}
}

func TestRefineReportsDiarizationUnavailableWithTheCLIReason(t *testing.T) {
	rt, bin := newSpeakerRuntime(t, "JOB1")
	if err := os.WriteFile(bin+".diarize-unavailable", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	queueSpeakerEdits(t, rt, "JOB1", 0, splitDoc(speakerTestRoom))
	waitForJobState(t, rt.store, "JOB1", "failed")
	rec, _ := rt.store.GetSpeakerEdits(context.Background(), "JOB1")
	if !strings.Contains(rec.LastError, "diarization-unavailable: no Nemotron model") {
		t.Fatalf("lastError = %q, want the CLI's reason", rec.LastError)
	}
	if _, ok, _ := rt.store.SpeakerSplitTurns(context.Background(), "JOB1", speakerTestRoom); ok {
		t.Fatal("a failed diarization stored turns")
	}
	if len(speakersCalls(t, bin, "speakers apply")) != 0 {
		t.Fatal("apply ran without the split's turns")
	}
}

// Diarizing decodes a participant's whole track and runs a model on a host
// shared with Nextcloud and Talk. With too little memory free it must not
// start: the attempt goes back to the queue, as a build that waits does, and
// nothing is reported as failed.
func TestRefineDiarizationWaitsForMemoryLikeABuild(t *testing.T) {
	rt, bin := newSpeakerRuntime(t, "JOB1")
	setSpeakerMeetingAudioMs(t, rt.cfg.WorkRoot, "JOB1", 2*60*60*1000)
	t.Setenv("CASSINI_BUILD_MEM_WAIT_SECS", "0")
	t.Setenv("CASSINI_BUILD_CPU_MEM_HEADROOM_MB", "1024")
	orig := probeAvailableMem
	t.Cleanup(func() { probeAvailableMem = orig })
	// Enough for an audio-only build (1024 MiB), not for diarizing two hours.
	probeAvailableMem = func() int { return 1600 }

	queueSpeakerEdits(t, rt, "JOB1", 0, splitDoc(speakerTestRoom))
	deadline := time.Now().Add(testWaitTimeout)
	var job Job
	for time.Now().Before(deadline) {
		var err error
		if job, err = rt.store.GetJob(context.Background(), "JOB1"); err == nil && (job.BuildDeferralCount > 0 || job.Stage == "done") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if job.BuildDeferralCount == 0 || job.Stage != "build" || job.State != "queued" || !strings.Contains(jobErrorText(job), "host memory") {
		t.Fatalf("job = %s/%s deferrals %d error %q; want the refine deferred for memory", job.Stage, job.State, job.BuildDeferralCount, jobErrorText(job))
	}
	if calls := speakersCalls(t, bin, "speakers"); len(calls) != 0 {
		t.Fatalf("speakers commands ran without the memory to diarize: %v", calls)
	}
	rec, err := rt.store.GetSpeakerEdits(context.Background(), "JOB1")
	if err != nil || rec.LastError != "" {
		t.Fatalf("edits after a memory wait = %+v, %v; a wait is not a failure", rec, err)
	}
	if got, want := speakerDiarizeMemMB(2*60*60*1000), speakerDiarizeModelMB+440; got != want {
		t.Fatalf("speakerDiarizeMemMB(2h) = %d, want %d", got, want)
	}
}

// The diarizer gets the thread budget a CPU transcription gets: the cores
// minus the reserve left for Nextcloud and Talk. A value the operator's own
// environment carries does not reach it.
func TestRefineDiarizesWithTheBuildThreadBudget(t *testing.T) {
	rt, bin := newSpeakerRuntime(t, "JOB1")
	t.Setenv("CASSINI_BUILD_CPU_RESERVE", "2")
	t.Setenv(envDiarizationThreads, "12")
	orig := probeOnlineCPUs
	t.Cleanup(func() { probeOnlineCPUs = orig })
	probeOnlineCPUs = func() int { return 8 }

	queueSpeakerEdits(t, rt, "JOB1", 0, splitDoc(speakerTestRoom))
	waitForSpeakerEdits(t, rt.store, "JOB1", func(r speakerEditsRecord) bool { return r.AppliedRevision == 1 })
	if got := strings.TrimSpace(annTestRead(t, bin+".threads")); got != "6" {
		t.Fatalf("diarize ran with %s=%q, want the build budget 8-2 = 6", envDiarizationThreads, got)
	}
}

func TestSpeakerDiarizeThreads(t *testing.T) {
	orig := probeOnlineCPUs
	t.Cleanup(func() { probeOnlineCPUs = orig })
	for _, c := range []struct {
		name          string
		cpus, reserve int
		want          int
	}{
		{"budget", 8, 2, 6},
		{"one core left", 2, 1, 1},
		{"reserve takes every core", 2, 4, 1},
		{"capped at 16", 64, 0, 16},
		{"cores unknown keep the default", 0, 0, speakerDiarizeThreadsDefault},
	} {
		probeOnlineCPUs = func() int { return c.cpus }
		limits := resourceLimits{cpuReserve: c.reserve}
		if got := limits.speakerDiarizeThreads(); got != c.want {
			t.Errorf("%s: %d cpus, reserve %d: threads = %d, want %d", c.name, c.cpus, c.reserve, got, c.want)
		}
	}
}

// A participant who reconnected has a stream per connection, and the CLI
// sums them for the diarizer. It decodes each later stream into the running
// mix, so the peak is one meeting-length track however often they rejoined —
// the budget admission waits for. An eight-hour meeting needs 1,758 MiB per
// track: one track must fit in 3,300 MiB free with the 1,024 MiB headroom,
// where two (3,516 MiB of audio) would not.
func TestSpeakerDiarizeBudgetIsOneTrackForAReconnectingParticipant(t *testing.T) {
	const eightHours = int64(8 * 60 * 60 * 1000)
	const trackMB = (eightHours*16*4 + 1<<20 - 1) >> 20 // 16 samples/ms of float32
	if got, want := speakerDiarizeMemMB(eightHours), speakerDiarizeModelMB+int(trackMB); got != want {
		t.Fatalf("speakerDiarizeMemMB(8h) = %d, want the model and one %d MiB track = %d", got, trackMB, want)
	}
	t.Setenv("CASSINI_BUILD_MEM_WAIT_SECS", "0")
	t.Setenv("CASSINI_BUILD_CPU_MEM_HEADROOM_MB", "1024")
	orig := probeAvailableMem
	t.Cleanup(func() { probeAvailableMem = orig })
	probeAvailableMem = func() int { return 3300 }
	limits := resourceLimitsFromEnv()
	if err := limits.waitForMemory(context.Background(), speakerDiarizeMemMB(eightHours)+limits.cpuMemHeadroomMB, func(string, ...any) {}); err != nil {
		t.Fatalf("an eight-hour diarization was refused with 3,300 MiB free: %v", err)
	}
}

// The refine itself refuses to copy an unpublished rebuild, whatever queued
// it: with marks the carry would refuse the changed audio, and without marks
// the rebuild would be published under the name of a speaker edit.
func TestRefineRefusesARebuildThatWasNeverPublished(t *testing.T) {
	rt, bin := newSpeakerRuntime(t, "JOB1")
	markSpeakerRebuildUnpublished(t, rt.store, rt.cfg.WorkRoot, "JOB1")
	queueSpeakerEdits(t, rt, "JOB1", 0, splitDoc(speakerTestRoom))
	waitForJobState(t, rt.store, "JOB1", "failed")
	if calls := speakersCalls(t, bin, "speakers"); len(calls) != 0 {
		t.Fatalf("speakers commands ran on an unpublished rebuild: %v", calls)
	}
	rec, err := rt.store.GetSpeakerEdits(context.Background(), "JOB1")
	if err != nil || rec.AppliedRevision != 0 || !strings.Contains(rec.LastError, speakerReasonUnpublishedRebuild) {
		t.Fatalf("edits = %+v, %v", rec, err)
	}
}

// A refine queued before an operator restart exists only as a build/queued
// row. The startup sweep must leave it queued and the requeue dispatcher must
// run it as a refine, from what the attempt row says.
func TestQueuedRefineSurvivesRestart(t *testing.T) {
	rt, bin := newSpeakerRuntime(t, "JOB1")
	if _, err := rt.store.QueueSpeakerEdits(context.Background(), "JOB1", 0, splitDoc(speakerTestRoom), "alice", nowUTCString()); err != nil {
		t.Fatal(err)
	}
	interrupted, err := rt.store.MarkIncompleteJobsInterrupted(context.Background(), nowUTCString())
	if err != nil || interrupted != 0 {
		t.Fatalf("MarkIncompleteJobsInterrupted() = %d, %v; the queued refine must stay queued", interrupted, err)
	}
	rt.kickRequeueScan()
	waitForSpeakerEdits(t, rt.store, "JOB1", func(r speakerEditsRecord) bool { return r.AppliedRevision == 1 })
	if len(speakersCalls(t, bin, "speakers apply")) != 1 {
		t.Fatal("the resumed attempt did not run as a refine")
	}
}

// A rerun rebuilds from the capture. It must replay the edits onto the fresh
// bundle, with the stored turns, before promote — or the next rerun silently
// undoes every split and name.
func TestRerunReplaysSpeakerEdits(t *testing.T) {
	rt, bin := newSpeakerRuntime(t, "JOB1")
	ctx := context.Background()
	builds := 0
	rt.buildJobFn = rt.withSpeakerEdits(func(ctx context.Context, task buildTask) (string, error) {
		builds++
		meetingPath := attemptMeetingPath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
		writeSpeakerMeetingFixture(t, meetingPath, task.ArtifactRunPath)
		return meetingPath, nil
	})
	doc := splitDoc(speakerTestRoom)
	doc.Revision = 4
	raw, _ := json.Marshal(doc)
	if _, err := rt.store.db.Exec(`INSERT INTO speaker_edits (job_id, revision, doc_json, applied_revision, applied_doc_json, updated_at) VALUES ('JOB1', 4, ?, 4, ?, ?)`, string(raw), string(raw), nowUTCString()); err != nil {
		t.Fatal(err)
	}
	stored, err := rt.store.PutSpeakerSplitTurns(ctx, "JOB1", speakerTestRoom, `{"speakerId":"spk_room","turns":"stored"}`, "model", "src", nowUTCString())
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	rt.jobDetailHandler(rec, httptest.NewRequest(http.MethodPost, "/jobs/JOB1/rerun", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("rerun status = %d %s", rec.Code, rec.Body.String())
	}
	job := waitForJobStageState(t, rt.store, "JOB1", "done", "succeeded", "failed")
	if job.State != "succeeded" || builds != 1 {
		t.Fatalf("rerun = %s (error %s), builds %d", job.State, jobErrorText(job), builds)
	}
	waitForPublishedAttempt(t, rt, "JOB1", 2)
	apply := speakersCalls(t, bin, "speakers apply")
	if len(apply) != 1 || !strings.HasPrefix(apply[0], "speakers apply "+attemptMeetingPath(rt.cfg.WorkRoot, "JOB1", 2)+" ") {
		t.Fatalf("apply calls = %v, want one on the rerun's fresh bundle", apply)
	}
	if len(speakersCalls(t, bin, "speakers diarize")) != 0 {
		t.Fatal("the rerun diarized again instead of reusing the stored turns")
	}
	current := canonicalMeetingPath(rt.cfg.WorkRoot, "JOB1")
	if got := annTestRead(t, filepath.Join(current, "turns-used.json")); got != stored {
		t.Fatalf("replay got turns %q, want the stored %q", got, stored)
	}
	if !strings.Contains(annTestRead(t, filepath.Join(current, "speaker-edits.json")), `"revision":4`) {
		t.Fatal("the promoted rerun does not carry the replayed edits")
	}
	if state, _ := rt.speakerEditsState(ctx, "JOB1"); state.State != speakerStateIdle || state.AppliedRevision != 4 {
		t.Fatalf("state after rerun = %+v", state)
	}
}

// A split that never applied — the runtime could not diarize it — must not
// take the meeting's reruns down with it: a rerun replays what the recording
// carries, and the failed revision stays reported as failed.
func TestRerunAfterASplitThatNeverAppliedStillSucceeds(t *testing.T) {
	rt, bin := newSpeakerRuntime(t, "JOB1")
	ctx := context.Background()
	named := emptySpeakerEditsDoc()
	named.Labels = []speakerEditsLabel{{SpeakerID: speakerTestRemote, Label: "Remote Ann"}}
	queueSpeakerEdits(t, rt, "JOB1", 0, named)
	waitForSpeakerEdits(t, rt.store, "JOB1", func(r speakerEditsRecord) bool { return r.AppliedRevision == 1 })

	if err := os.WriteFile(bin+".diarize-unavailable", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	split := named
	split.Splits = []speakerEditsSplit{{SpeakerID: speakerTestRoom}}
	queueSpeakerEdits(t, rt, "JOB1", 1, split)
	waitForJobState(t, rt.store, "JOB1", "failed")

	rt.buildJobFn = rt.withSpeakerEdits(func(ctx context.Context, task buildTask) (string, error) {
		meetingPath := attemptMeetingPath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
		writeSpeakerMeetingFixture(t, meetingPath, task.ArtifactRunPath)
		return meetingPath, nil
	})
	rec := httptest.NewRecorder()
	rt.jobDetailHandler(rec, httptest.NewRequest(http.MethodPost, "/jobs/JOB1/rerun", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("rerun status = %d %s", rec.Code, rec.Body.String())
	}
	job := waitForJobStageState(t, rt.store, "JOB1", "done", "succeeded", "failed")
	if job.State != "succeeded" || job.CurrentAttemptNumber != 4 {
		t.Fatalf("rerun = %s attempt %d (error %s), want the rerun to succeed", job.State, job.CurrentAttemptNumber, jobErrorText(job))
	}
	waitForPublishedAttempt(t, rt, "JOB1", 4)
	if got := len(speakersCalls(t, bin, "speakers diarize")); got != 1 {
		t.Fatalf("diarize ran %d times, want only the failed split's attempt", got)
	}
	replayed := annTestRead(t, filepath.Join(canonicalMeetingPath(rt.cfg.WorkRoot, "JOB1"), "speaker-edits.json"))
	if !strings.Contains(replayed, `"revision":1`) || !strings.Contains(replayed, "Remote Ann") {
		t.Fatalf("the rerun replayed %s, want the applied revision 1", replayed)
	}
	state, err := rt.speakerEditsState(ctx, "JOB1")
	if err != nil || state.State != speakerStateFailed || state.Revision != 2 || state.AppliedRevision != 1 || state.LastError != "Voice separation is not available on this server." {
		t.Fatalf("state after the rerun = %+v, %v; want revision 2 still reported failed", state, err)
	}
}

// A refine whose publish fails is reported failed: nothing in the
// speaker-edits path saw that failure, so it comes from the attempt, and the
// publish's own words (remote paths, Nextcloud's answer) are not repeated.
func TestRefinePublishFailureIsReported(t *testing.T) {
	rt, _ := newSpeakerRuntime(t, "JOB1")
	rt.publishJobFn = func(ctx context.Context, task publishTask) (string, error) {
		return "", errors.New("publish exploded")
	}
	queueSpeakerEdits(t, rt, "JOB1", 0, splitDoc(speakerTestRoom))
	waitForJobState(t, rt.store, "JOB1", "failed")
	state, err := rt.speakerEditsState(context.Background(), "JOB1")
	if err != nil || state.State != speakerStateFailed || state.AppliedRevision != 0 || state.LastError != "The recording could not be updated." {
		t.Fatalf("state after a failed publish = %+v, %v", state, err)
	}
}

// newProductionBuildRuntime is newTestRuntime keeping the build stage
// NewRuntime wires, so a test sees what production runs for an attempt.
func newProductionBuildRuntime(t *testing.T, bin string) *Runtime {
	t.Helper()
	t.Setenv(envTalkSignalingInternalSecret, "test-internal-secret")
	repoRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	t.Setenv("CASSINI_REPO_ROOT", repoRoot)
	t.Setenv(envSTTCUDACapable, "1")
	tmp := t.TempDir()
	store, err := OpenStore(filepath.Join(tmp, "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	rt := NewRuntime(context.Background(), store, Config{
		RepoRoot:         repoRoot,
		BindAddr:         "127.0.0.1:0",
		DBPath:           filepath.Join(tmp, "jobs.sqlite3"),
		WorkRoot:         filepath.Join(tmp, "jobs"),
		SiteRoot:         filepath.Join(tmp, "site"),
		CassiniBin:       bin,
		MaxRecordWorkers: 1,
		MaxBuildWorkers:  1,
		TalkSharedSecret: "test-recording-secret",
	}, log.New(ioDiscard{}, "", 0), ioDiscard{}, ioDiscard{})
	rt.sealJobFn = writeSealedOpusFixture(rt)
	rt.publishJobFn = func(ctx context.Context, task publishTask) (string, error) {
		sitePath := attemptSitePath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
		return sitePath, writeReadySiteBundleFixture(sitePath, currentRoot(rt.cfg.WorkRoot))
	}
	rt.referenceFrontendProbe = func() (bool, bool) { return true, true }
	t.Cleanup(func() { cleanupTestRuntime(t, rt, store) })
	return rt
}

// The refine tests above wrap the build by hand. This one keeps NewRuntime's:
// a refine there must run `speakers apply` and never `cassini build`, which
// would re-transcribe and silently drop the edits.
func TestNewRuntimeRunsARefineWithoutBuilding(t *testing.T) {
	bin := fakeSpeakersCLI(t)
	rt := newProductionBuildRuntime(t, bin)
	seedSpeakerJob(t, rt.store, rt.cfg.WorkRoot, "JOB1")
	queueSpeakerEdits(t, rt, "JOB1", 0, splitDoc(speakerTestRoom))
	waitForSpeakerEdits(t, rt.store, "JOB1", func(r speakerEditsRecord) bool { return r.AppliedRevision == 1 })
	if got := len(speakersCalls(t, bin, "speakers apply")); got != 1 {
		t.Fatalf("speakers apply ran %d times, want 1", got)
	}
	if builds := speakersCalls(t, bin, "build"); len(builds) != 0 {
		t.Fatalf("a refine ran %v", builds)
	}
}

// fakeModelsCLI answers `cassini models list` from <bin>.inventory.json, the
// way the operator's Settings page and speaker availability both read it.
func fakeModelsCLI(t *testing.T) string {
	t.Helper()
	return writeFakeCassini(t, `echo "$*" >> "$0.calls"
[ "$1 $2" = "models list" ] || { echo "unexpected command: $*" >&2; exit 1; }
[ -f "$0.list-fails" ] && { echo "catalogue broken" >&2; exit 1; }
cat "$0.inventory.json"
`)
}

// writeDiarizerInventory makes the fake CLI list a speech model and the
// default diarizer in the given state.
func writeDiarizerInventory(t *testing.T, bin string, installed, ready, runtimeSupported bool) {
	t.Helper()
	supported := runtimeSupported
	models := []modelInfo{
		{ID: modelParakeet110M, Kind: "speech", Revision: strings.Repeat("a", 64), Installed: true, Ready: true, Device: "cpu"},
		{ID: defaultDiarizationModel, Kind: modelKindDiarization, Revision: strings.Repeat("c", 64), Installed: installed, Ready: ready, Device: "cpu", RuntimeSupported: &supported},
	}
	b, _ := json.Marshal(models)
	if err := os.WriteFile(bin+".inventory.json", b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiarizationAvailabilityAsksTheModelInventory(t *testing.T) {
	t.Setenv(envDiarizationModel, "")
	bin := fakeModelsCLI(t)
	rt := &Runtime{logger: log.New(ioDiscard{}, "", 0)}
	rt.cfg.CassiniBin = bin
	rt.cfg.ModelCacheRoot = t.TempDir()
	ctx := context.Background()
	check := func(wantOK bool, wantDetail string) {
		t.Helper()
		rt.invalidateModelInventory()
		ok, detail := rt.diarizationAvailability(ctx)
		if ok != wantOK || !strings.Contains(detail, wantDetail) || (ok && detail != "") {
			t.Fatalf("availability = %v %q, want %v with %q", ok, detail, wantOK, wantDetail)
		}
	}

	// The proof of concept's loose file is not an installed model any more.
	loose := filepath.Join(rt.cfg.ModelCacheRoot, "models", defaultDiarizationModel, "model.int8.onnx")
	if err := os.MkdirAll(filepath.Dir(loose), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(loose, []byte("onnx"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeDiarizerInventory(t, bin, false, false, true)
	check(false, "cassini models install "+defaultDiarizationModel)
	check(false, "Settings")

	writeDiarizerInventory(t, bin, true, false, false)
	check(false, "cannot run speaker separation")

	writeDiarizerInventory(t, bin, true, true, true)
	check(true, "")
	if calls := speakersCalls(t, bin, "models list"); len(calls) == 0 || !strings.Contains(calls[0], "--cache-root "+rt.cfg.ModelCacheRoot) || !strings.Contains(calls[0], "--device cpu") {
		t.Fatalf("inventory calls = %v, want the operator's store on the CPU", calls)
	}

	// A binary that lists no diarizer, or cannot list at all.
	if err := os.WriteFile(bin+".inventory.json", []byte(`[]`), 0o644); err != nil {
		t.Fatal(err)
	}
	check(false, "no speaker separation model")
	if err := os.WriteFile(bin+".list-fails", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	check(false, "catalogue broken")

	// The development override wins, as it does for the CLI.
	explicit := filepath.Join(t.TempDir(), "nemotron.onnx")
	t.Setenv(envDiarizationModel, explicit)
	check(false, envDiarizationModel)
	if err := os.WriteFile(explicit, []byte("onnx"), 0o644); err != nil {
		t.Fatal(err)
	}
	check(true, "")
	t.Setenv(envDiarizationModel, filepath.Dir(explicit))
	check(false, "not a model file")
}

func TestRerunWithoutSpeakerEditsRunsNoSpeakersCommand(t *testing.T) {
	rt, bin := newSpeakerRuntime(t, "JOB1")
	rt.buildJobFn = rt.withSpeakerEdits(func(ctx context.Context, task buildTask) (string, error) {
		meetingPath := attemptMeetingPath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
		writeSpeakerMeetingFixture(t, meetingPath, task.ArtifactRunPath)
		return meetingPath, nil
	})
	rec := httptest.NewRecorder()
	rt.jobDetailHandler(rec, httptest.NewRequest(http.MethodPost, "/jobs/JOB1/rerun", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("rerun status = %d %s", rec.Code, rec.Body.String())
	}
	job := waitForJobStageState(t, rt.store, "JOB1", "done", "succeeded", "failed")
	if job.State != "succeeded" {
		t.Fatalf("rerun failed: %s", jobErrorText(job))
	}
	if calls := speakersCalls(t, bin, ""); len(calls) != 0 {
		t.Fatalf("a rerun nobody edited ran %v", calls)
	}
}

// unresolvedCarryCLI is a stand-in `cassini annotate` whose carry finds two
// marks that do not resolve against the sealed file's audio.
func unresolvedCarryCLI(t *testing.T) string {
	return writeFakeCassini(t, `out=""; prev=""
for a in "$@"; do
  if [ "$prev" = "--out" ]; then out="$a"; fi
  prev="$a"
done
case "$2" in
  carry) printf 'OPUS-carried' > "$out"; printf '%s' '{"format":"cassini.annotate.result.v1","carried":2,"resolved":false}' ;;
  show) printf '%s' '{"format":"cassini.annotate.result.v1","resolved":true}' ;;
  *) exit 1 ;;
esac
`)
}

// The publish path keeps marks only when the carried copy resolves against the
// new audio. For a speaker edit the audio is the same by construction, so an
// unresolved carry is a fault: the publish must fail rather than discard them.
func TestSpeakerEditPublishFailsInsteadOfDiscardingMarks(t *testing.T) {
	nc := newAnnotationsNextcloud(t, "MEETING1.opus")
	sink := &nextcloudFilesPublishSink{cfg: testExAppConfig(nc.url), client: http.DefaultClient, cassiniBin: unresolvedCarryCLI(t)}
	local := filepath.Join(t.TempDir(), "sealed.opus")
	if err := os.WriteFile(local, []byte("OPUS-sealed"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	item := upload{local: local, remote: annTestRecording, size: 11, keepMarks: true}
	if _, _, err := sink.stageDeliveredMarks(ctx, item, t.TempDir()); err == nil || !strings.Contains(err.Error(), "discard 2 mark(s)") {
		t.Fatalf("speaker-edit carry error = %v, want the publish refused", err)
	}
	// An ordinary rerun is a new recording, and its old marks may go.
	item.keepMarks = false
	staged, _, err := sink.stageDeliveredMarks(ctx, item, t.TempDir())
	if err != nil || staged.local != local {
		t.Fatalf("rerun carry = %+v, %v; want the sealed file uploaded as is", staged, err)
	}
}

// Deliver is where the attempt is known, so it is what must arm the guard.
func TestDeliverOfASpeakerEditRefusesToDropMarks(t *testing.T) {
	ctx := context.Background()
	rt, cleanup := newBarePublishRuntime(t, log.New(ioDiscard{}, "", 0))
	defer cleanup()
	const jobID = "DIRECT1"
	seedSpeakerJob(t, rt.store, rt.cfg.WorkRoot, jobID)
	if _, err := rt.store.QueueSpeakerEdits(ctx, jobID, 0, splitDoc(speakerTestRoom), "alice", nowUTCString()); err != nil {
		t.Fatal(err)
	}
	site := t.TempDir()
	if err := os.MkdirAll(filepath.Join(site, "meetings"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(site, "meetings", jobID+".opus"), []byte("sealed bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(site, "catalog.json"), []byte(`{"version":"cassini.viewer.catalog.v1","meetings":[{"id":"DIRECT1","title":"Planning","audioPath":"./meetings/DIRECT1.opus"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	files := newFakeNCFiles()
	remote := ncRecordingsRoot + "/meetings/" + jobID + ".opus"
	files.files[remote] = []byte("delivered with marks")
	server := files.server(t)
	defer server.Close()
	cfg := ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppVersion: "1", AppSecret: "secret", PublishSink: publishSinkNextcloudFiles}
	sink := &directSharesPublishSink{&nextcloudFilesPublishSink{cfg: cfg, client: server.Client(), logger: log.New(ioDiscard{}, "", 0), cassiniBin: unresolvedCarryCLI(t), rt: rt}}
	ncAccessSubstrate.reset()
	ncAccessSubstrate.markApplicable()
	ncAccessSubstrate.beginRun()
	ncAccessSubstrate.succeed()
	t.Cleanup(ncAccessSubstrate.reset)

	_, err := sink.Deliver(ctx, publishDelivery{AttemptSitePath: site, JobID: jobID, AttemptNumber: 2})
	if err == nil || !strings.Contains(err.Error(), "discard 2 mark(s)") {
		t.Fatalf("Deliver() error = %v, want the refine's publish refused", err)
	}
	files.mu.Lock()
	if got := string(files.files[remote]); got != "delivered with marks" {
		files.mu.Unlock()
		t.Fatalf("remote bytes = %q, want the published recording untouched", got)
	}
	files.mu.Unlock()

	// A rerun that replays the same edits re-encodes the audio like any
	// rerun, so it keeps the rerun rule: the publish goes ahead.
	if _, err := rt.store.db.ExecContext(ctx, `UPDATE job_attempts SET trigger_kind = 'rerun' WHERE job_id = ? AND attempt_number = 2`, jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Deliver(ctx, publishDelivery{AttemptSitePath: site, JobID: jobID, AttemptNumber: 2}); err != nil && strings.Contains(err.Error(), "discard 2 mark(s)") {
		t.Fatalf("Deliver() of a replaying rerun refused: %v", err)
	}
}

func jobErrorText(job Job) string {
	if job.Error == nil {
		return "<nil>"
	}
	return *job.Error
}
