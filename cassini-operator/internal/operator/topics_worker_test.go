package operator

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeTopicsCLI is a `cassini` stand-in for the topics worker. It keeps its
// state in files under dir: the model revision, the passages an embed
// writes, the calls it received, and optional failure switches.
const fakeTopicsCLIScript = `#!/bin/sh
D="$FAKE_TOPICS_DIR"
case "$1 $2" in
"models list") cat "$D/models.json"; exit 0;;
"topics versions") printf '{"modelRevision":"%s","chunkerVersion":"natural-1","recipeVersion":"r1","detectorVersion":"d1"}\n' "$(cat "$D/revision")"; exit 0;;
esac
cmd="$2"; shift 2
input=""; out=""; known=""; in=""
while [ $# -gt 0 ]; do
  case "$1" in
  --input) input="$2"; shift 2;;
  --out) out="$2"; shift 2;;
  --known-key) known="$2"; shift 2;;
  --in) in="$2"; shift 2;;
  *) shift;;
  esac
done
echo "$cmd" >> "$D/calls"
rev=$(cat "$D/revision")
if [ "$cmd" = embed ]; then
  payload=$(sha256sum "$input" | cut -d' ' -f1)
  key=$(printf '%s\n%s\nnatural-1\nr1' "$payload" "$rev" | sha256sum | cut -d' ' -f1)
  if [ "$known" = "$key" ]; then
    speakers='[]'; [ -f "$D/unchanged-speakers" ] && speakers=$(cat "$D/unchanged-speakers")
    printf '{"unchanged":true,"embedKey":"%s","speakerTermHashes":%s}\n' "$key" "$speakers"; exit 0
  fi
  if [ -f "$D/hang-before-lock" ]; then touch "$D/waiting"; sleep 30; fi
  if [ -f "$D/exit" ]; then cat "$D/stderr" >&2; exit "$(cat "$D/exit")"; fi
  echo '{"locked":true}'
  if [ -f "$D/hang" ]; then touch "$D/locked"; sleep 30; fi
  echo embedded >> "$D/embeds"
  n=$(grep -c . "$D/passages")
  printf '{"format":"cassini.topic-passages.v1","embedKey":"%s","payloadSha256":"%s","modelRevision":"%s","chunkerVersion":"natural-1","recipeVersion":"r1","dimensions":4,"count":%d,"language":"en","languageConfidence":0.9,"detectorVersion":"d1"}\n' "$key" "$payload" "$rev" "$n" > "$out"
  cat "$D/passages" >> "$out"
  echo "{\"count\":$n}"
  exit 0
fi
if [ "$cmd" = embed-texts ]; then
  echo '{"locked":true}'
  n=$(grep -c . "$in")
  printf '{"format":"cassini.topic-text-vectors.v1","modelRevision":"%s","recipeVersion":"r1","chunkerVersion":"natural-1","dimensions":4,"count":%d}\n' "$rev" "$n" > "$out"
  sed 's/.*"id":"\([^"]*\)".*/{"id":"\1","tokenCount":5,"vector":[0,0,0,1]}/' "$in" >> "$out"
  echo "{\"count\":$n}"
  exit 0
fi
exit 2
`

type fakeTopicsCLI struct {
	t   *testing.T
	dir string
}

func (f fakeTopicsCLI) write(name, content string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f fakeTopicsCLI) remove(name string) { _ = os.Remove(filepath.Join(f.dir, name)) }

// count is how many lines a state file holds: calls, embeds.
func (f fakeTopicsCLI) count(name string) int {
	raw, err := os.ReadFile(filepath.Join(f.dir, name))
	if err != nil {
		return 0
	}
	return strings.Count(string(raw), "\n")
}

// calls are the topics commands the CLI ran, in order. The model worker
// may also run `models install` for its startup check; that is not counted.
func (f fakeTopicsCLI) calls() []string {
	raw, _ := os.ReadFile(filepath.Join(f.dir, "calls"))
	var out []string
	for _, call := range strings.Fields(string(raw)) {
		if strings.HasPrefix(call, "embed") {
			out = append(out, call)
		}
	}
	return out
}

// waitFor blocks until the script creates name: the event the test orders
// against.
func (f fakeTopicsCLI) waitFor(name string) {
	f.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(f.dir, name)); err == nil {
			return
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("the fake CLI never created %s", name)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (f fakeTopicsCLI) setReady(ready bool) {
	f.write("models.json", fmt.Sprintf(`[{"id":%q,"kind":"embedding","installed":true,"ready":%t,"runtime_supported":true,"device":"cpu"}]`, defaultEmbeddingModel, ready))
}

// newTopicsTestService is a runtime with topic suggestions on, the fake CLI
// and a model that is ready.
func newTopicsTestService(t *testing.T) (*topicsService, fakeTopicsCLI) {
	t.Helper()
	rt, cleanup := newTestRuntime(t)
	t.Cleanup(cleanup)
	cli := fakeTopicsCLI{t: t, dir: t.TempDir()}
	t.Setenv("FAKE_TOPICS_DIR", cli.dir)
	script := filepath.Join(t.TempDir(), "cassini")
	if err := os.WriteFile(script, []byte(fakeTopicsCLIScript), 0o755); err != nil {
		t.Fatal(err)
	}
	rt.cfg.CassiniBin = script
	rt.cfg.ModelCacheRoot = t.TempDir()
	cli.write("revision", "embeddinggemma-2-text-int8@rev1")
	cli.write("passages", topicTestPassageLines(4))
	cli.setReady(true)
	settings := rt.currentSettings()
	settings.TopicSuggestions.Enabled = true
	rt.setSettings(settings)
	s := newTopicsService(rt, ExAppConfig{}, log.New(ioDiscard{}, "", 0))
	if s == nil {
		t.Fatal("topics service did not open")
	}
	s.memory = func(context.Context) error { return nil }
	rt.topics = s
	if err := os.MkdirAll(rt.cfg.WorkRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	return s, cli
}

// topicTestPassageLines are n passages of 30 words, one vector each.
func topicTestPassageLines(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, `{"ordinal":%d,"startMs":%d,"endMs":%d,"wordCount":30,"tokenCount":40,"boundary":"sentence","vector":[1,0,0,0]}`+"\n", i, i*10000, i*10000+9000)
	}
	return b.String()
}

// publishTopicTestMeeting writes a promoted recording for job and queues it
// the way the publish hook does.
func publishTopicTestMeeting(t *testing.T, s *topicsService, job, content string, attempt int) string {
	t.Helper()
	local := canonicalOpusPath(s.rt.cfg.WorkRoot, job)
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	name := job + ".opus"
	if err := s.store.enqueuePublished(context.Background(), name, job, hex.EncodeToString(sum[:]), attempt); err != nil {
		t.Fatal(err)
	}
	return name
}

type topicTestRow struct {
	State, Reason, LastError string
	Priority, Attempts       int
	RetryAt                  int64
	EmbedKey                 sql.NullString
}

func topicRow(t *testing.T, s *topicsService, name string) topicTestRow {
	t.Helper()
	var r topicTestRow
	err := s.store.db.QueryRow(`SELECT state,reason,last_error,priority,attempts,retry_at,embed_key FROM topic_meeting WHERE opus_name=?`, name).
		Scan(&r.State, &r.Reason, &r.LastError, &r.Priority, &r.Attempts, &r.RetryAt, &r.EmbedKey)
	if err != nil {
		t.Fatalf("row %s: %v", name, err)
	}
	return r
}

func TestTopicsWorkerEmbedsOnceAndRescoresAnUnchangedTranscript(t *testing.T) {
	s, cli := newTopicsTestService(t)
	ctx := context.Background()
	name := publishTopicTestMeeting(t, s, "job-a", "transcript A", 1)
	if !s.runOnce(ctx) {
		t.Fatal("nothing ran")
	}
	row := topicRow(t, s, name)
	if row.State != topicStateDone || !row.EmbedKey.Valid {
		t.Fatalf("after the first run: %+v", row)
	}
	set, ok, err := s.vectors.meeting(ctx, name)
	if err != nil || !ok || len(set.Passages) != 4 {
		t.Fatalf("vectors: %+v %v %v", set, ok, err)
	}
	var suggestions int
	s.store.db.QueryRow(`SELECT COUNT(*) FROM topic_suggestion`).Scan(&suggestions)
	if suggestions != 0 {
		t.Fatalf("an empty library suggests nothing, got %d", suggestions)
	}
	// A rerun of the same transcript: the CLI is asked, says unchanged, and
	// nothing is embedded again.
	publishTopicTestMeeting(t, s, "job-a", "transcript A", 2)
	if !s.runOnce(ctx) {
		t.Fatal("the rerun did not run")
	}
	if got := topicRow(t, s, name); got.State != topicStateDone {
		t.Fatalf("after the rerun: %+v", got)
	}
	if n := cli.count("embeds"); n != 1 {
		t.Fatalf("the unchanged transcript was embedded %d times", n)
	}
	if calls := cli.calls(); strings.Join(calls, " ") != "embed-texts embed embed" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestTopicsModelChangeRequeuesAndOldVectorsAreNotScored(t *testing.T) {
	s, cli := newTopicsTestService(t)
	ctx := context.Background()
	name := publishTopicTestMeeting(t, s, "job-a", "transcript A", 1)
	s.runOnce(ctx)
	cli.write("revision", "embeddinggemma-2-text-int8@rev2")
	s.invalidateVersions()
	if err := s.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if row := topicRow(t, s, name); row.State != topicStateQueued || row.Priority != topicPriorityBackfill {
		t.Fatalf("a model change must queue the meeting at backfill priority: %+v", row)
	}
	// The controls are embedded again for the new model before any scoring.
	inputs, ready, err := s.store.scoringInputs(ctx, topicVersions{ModelRevision: "embeddinggemma-2-text-int8@rev2", RecipeVersion: "r1"})
	if err != nil || ready {
		t.Fatalf("no rev2 controls yet: %+v %v %v", inputs, ready, err)
	}
	s.runOnce(ctx)
	if n := cli.count("embeds"); n != 2 {
		t.Fatalf("the meeting must be embedded again under the new model, embeds=%d", n)
	}
	set, _, _ := s.vectors.meeting(ctx, name)
	if set.ModelRevision != "embeddinggemma-2-text-int8@rev2" {
		t.Fatalf("stored vectors: %s", set.ModelRevision)
	}
}

func TestTopicsRecordingStartCancelsTheEmbedWithoutAnAttempt(t *testing.T) {
	s, cli := newTopicsTestService(t)
	s.rt.cfg.RecordingPriority = true
	name := publishTopicTestMeeting(t, s, "job-a", "transcript A", 1)
	cli.write("hang", "")
	done := make(chan bool)
	go func() { done <- s.runOnce(context.Background()) }()
	cli.waitFor("locked")
	if !s.rt.reserveRecordSlot() {
		t.Fatal("no record slot")
	}
	<-done
	s.rt.releaseRecordSlot()
	row := topicRow(t, s, name)
	if row.State != topicStateQueued || row.Attempts != 0 {
		t.Fatalf("a preempted embed must go back unharmed: %+v", row)
	}
	if s.stats.Preemptions != 1 {
		t.Fatalf("preemptions = %d", s.stats.Preemptions)
	}
}

func TestTopicsWaitingForTheInferenceLockIsNotAFailure(t *testing.T) {
	s, cli := newTopicsTestService(t)
	s.rt.cfg.RecordingPriority = true
	ctx := context.Background()
	embedTopicTestControls(t, s)
	name := publishTopicTestMeeting(t, s, "job-a", "transcript A", 1)
	cli.write("hang-before-lock", "")
	done := make(chan bool)
	go func() { done <- s.runOnce(ctx) }()
	cli.waitFor("waiting")
	if !s.rt.reserveRecordSlot() {
		t.Fatal("no record slot")
	}
	<-done
	s.rt.releaseRecordSlot()
	if row := topicRow(t, s, name); row.State != topicStateQueued || row.Attempts != 0 {
		t.Fatalf("a cancel before the lock is a deferral: %+v", row)
	}
}

func TestTopicsLowMemoryDefersWithoutAnAttempt(t *testing.T) {
	s, _ := newTopicsTestService(t)
	embedTopicTestControls(t, s)
	s.memory = func(context.Context) error {
		return &resourceUnavailableError{resource: "host memory", detail: "test"}
	}
	name := publishTopicTestMeeting(t, s, "job-a", "transcript A", 1)
	s.runOnce(context.Background())
	row := topicRow(t, s, name)
	if row.State != topicStateQueued || row.Attempts != 0 || row.RetryAt < time.Now().Add(4*time.Minute).Unix() {
		t.Fatalf("low memory must defer for minutes, unharmed: %+v", row)
	}
	if s.stats.MemoryDeferrals != 1 {
		t.Fatalf("deferrals = %d", s.stats.MemoryDeferrals)
	}
}

func TestTopicsFailuresBackOffThenBlockAndRetryRequeues(t *testing.T) {
	s, cli := newTopicsTestService(t)
	ctx := context.Background()
	embedTopicTestControls(t, s)
	name := publishTopicTestMeeting(t, s, "job-a", "transcript A", 1)
	cli.write("exit", "1")
	cli.write("stderr", "cassini topics embed: onnx runtime crashed")
	for i := 1; i <= topicMaxAttempts; i++ {
		s.store.db.Exec(`UPDATE topic_meeting SET retry_at=0 WHERE opus_name=?`, name)
		s.runOnce(ctx)
		row := topicRow(t, s, name)
		if i < topicMaxAttempts && (row.State != topicStateQueued || row.Attempts != i || row.RetryAt <= time.Now().Unix()) {
			t.Fatalf("attempt %d: %+v", i, row)
		}
	}
	row := topicRow(t, s, name)
	if row.State != topicStateBlocked || !strings.Contains(row.LastError, "crashed") {
		t.Fatalf("after %d failures: %+v", topicMaxAttempts, row)
	}
	rec := httptest.NewRecorder()
	s.rt.topicsSettingsHandler(rec, httptest.NewRequest(http.MethodPost, "/settings/topics/retry", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body)
	}
	if row := topicRow(t, s, name); row.State != topicStateQueued || row.Attempts != 0 {
		t.Fatalf("retry must queue it afresh: %+v", row)
	}
}

func TestTopicsTranscriptReasonsAndModelFaults(t *testing.T) {
	s, cli := newTopicsTestService(t)
	ctx := context.Background()
	embedTopicTestControls(t, s)
	name := publishTopicTestMeeting(t, s, "job-a", "transcript A", 1)
	cli.write("exit", "5")
	cli.write("stderr", "cassini topics embed: transcript-has-no-words: the default transcript has no words")
	s.runOnce(ctx)
	if row := topicRow(t, s, name); row.State != topicStateUnavailable || row.Reason != "transcript-has-no-words" {
		t.Fatalf("no words: %+v", row)
	}
	other := publishTopicTestMeeting(t, s, "job-b", "transcript B", 1)
	cli.write("exit", "4")
	cli.write("stderr", "cassini topics embed: model fault: NaN")
	s.runOnce(ctx)
	if row := topicRow(t, s, other); row.State != topicStateBlocked || row.Reason != topicReasonModelFault {
		t.Fatalf("model fault: %+v", row)
	}
}

func TestTopicsPublishDuringARunningEmbedQueuesItAgain(t *testing.T) {
	s, _ := newTopicsTestService(t)
	ctx := context.Background()
	name := publishTopicTestMeeting(t, s, "job-a", "transcript A", 1)
	row, ok, err := s.store.claimNext(ctx, true)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	publishTopicTestMeeting(t, s, "job-a", "transcript B", 2)
	inputs := topicScoringInputs{Topics: []topicForScoring{{TagID: "tag_x", Vector: []float32{1, 0, 0, 0}}}, Controls: [][]float32{{0, 0, 0, 1}}}
	scores := scoreMeetingTopics([]topicPassage{{WordCount: 90, Vector: []float32{1, 0, 0, 0}}}, inputs)
	if err := s.store.finishMeeting(ctx, row, nil, s.vectors.generation, topicEmbedResult{EmbedKey: "k1", PayloadSHA256: "p1"}, scores); err != nil {
		t.Fatal(err)
	}
	got := topicRow(t, s, name)
	if got.State != topicStateQueued || got.EmbedKey.String != "k1" {
		t.Fatalf("the newer publish must win: %+v", got)
	}
	var n int
	s.store.db.QueryRow(`SELECT COUNT(*) FROM topic_suggestion`).Scan(&n)
	if n != 0 {
		t.Fatal("the stale result must not write suggestions")
	}
	var digest string
	s.store.db.QueryRow(`SELECT delivered_digest FROM topic_meeting WHERE opus_name=?`, name).Scan(&digest)
	sum := sha256.Sum256([]byte("transcript B"))
	if digest != hex.EncodeToString(sum[:]) {
		t.Fatal("the newer delivery's digest is kept")
	}
}

func TestTopicsCrashLeftoversAreQueuedAgain(t *testing.T) {
	s, _ := newTopicsTestService(t)
	ctx := context.Background()
	name := publishTopicTestMeeting(t, s, "job-a", "transcript A", 1)
	s.store.db.Exec(`UPDATE topic_meeting SET state='running',claim='otherboot-1' WHERE opus_name=?`, name)
	other := publishTopicTestMeeting(t, s, "job-b", "transcript B", 1)
	if _, ok, _ := s.store.claimNext(ctx, true); !ok {
		t.Fatal("claim")
	}
	if n, err := s.store.recoverRunning(ctx); err != nil || n != 1 {
		t.Fatalf("recovered %d %v", n, err)
	}
	if topicRow(t, s, name).State != topicStateQueued || topicRow(t, s, other).State != topicStateRunning {
		t.Fatal("only the other boot's claim is a leftover")
	}
}

func TestTopicsLostSidecarQueuesDoneMeetingsAndKeepsTextVectors(t *testing.T) {
	s, _ := newTopicsTestService(t)
	ctx := context.Background()
	name := publishTopicTestMeeting(t, s, "job-a", "transcript A", 1)
	s.runOnce(ctx)
	path := s.vectors.path
	s.vectors.Close()
	if err := removeSidecarFiles(path); err != nil {
		t.Fatal(err)
	}
	vectors, err := openTopicsVectorStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { vectors.Close() })
	s.vectors = vectors
	if err := s.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if row := topicRow(t, s, name); row.State != topicStateQueued {
		t.Fatalf("a rebuilt sidecar must queue the meeting: %+v", row)
	}
	missing, err := s.store.missingTextVectors(ctx, topicVersions{ModelRevision: "embeddinggemma-2-text-int8@rev1", RecipeVersion: "r1"})
	if err != nil || len(missing) != 0 {
		t.Fatalf("control vectors live in the durable store: %v %v", missing, err)
	}
}

func TestTopicsLocalCopyMustBeTheDeliveredBuild(t *testing.T) {
	s, _ := newTopicsTestService(t)
	ctx := context.Background()
	var archived []string
	s.archive = func(ctx context.Context, name, dir string) (string, string, error) {
		archived = append(archived, name)
		path := filepath.Join(dir, name)
		return path, "d", os.WriteFile(path, []byte("archived transcript"), 0o600)
	}
	name := publishTopicTestMeeting(t, s, "job-a", "transcript A", 1)
	// A rerun built and promoted locally but its publish failed: the local
	// copy is not what was delivered.
	if err := os.WriteFile(canonicalOpusPath(s.rt.cfg.WorkRoot, "job-a"), []byte("rerun that never published"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.runOnce(ctx)
	if len(archived) != 1 || archived[0] != name || topicRow(t, s, name).State != topicStateDone {
		t.Fatalf("archive reads: %v, row %+v", archived, topicRow(t, s, name))
	}
}

func TestTopicsRetiredMeetingsAreNeverWritten(t *testing.T) {
	s, _ := newTopicsTestService(t)
	ctx := context.Background()
	name := publishTopicTestMeeting(t, s, "job-a", "transcript A", 1)
	retireTopicTestMeeting(t, s, name)
	// The worker finds it retired while it runs: no vectors, and no row left
	// behind that a restart would embed again.
	s.runOnce(ctx)
	if _, ok, _ := s.vectors.meeting(ctx, name); ok {
		t.Fatal("vectors were written for a retired meeting")
	}
	var n int
	s.store.db.QueryRow(`SELECT COUNT(*) FROM topic_meeting WHERE opus_name=?`, name).Scan(&n)
	if n != 0 {
		t.Fatal("the retired meeting's row is left behind")
	}
	// Every queueing path refuses it.
	if err := s.store.enqueuePublished(ctx, name, "job-a", "d", 2); !errors.Is(err, errMeetingRetired) {
		t.Fatalf("publish hook: %v", err)
	}
	if n, err := s.store.queueIfMissing(ctx, s.rt.store, searchBackfillTarget{JobID: "job-a", OpusName: name}, topicPriorityBackfill); err != nil || n != 0 {
		t.Fatalf("backfill: %d %v", n, err)
	}
}

func TestTopicsReconcileForgetsVectorsOfMeetingsRetiredUnseen(t *testing.T) {
	s, _ := newTopicsTestService(t)
	ctx := context.Background()
	name := publishTopicTestMeeting(t, s, "job-a", "transcript A", 1)
	s.runOnce(ctx)
	retireTopicTestMeeting(t, s, name)
	if err := s.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.vectors.meeting(ctx, name); ok {
		t.Fatal("a retired meeting's vectors survive the reconciler")
	}
}

func TestTopicsAModelChangeMidRunDefersInsteadOfFinishing(t *testing.T) {
	s, cli := newTopicsTestService(t)
	ctx := context.Background()
	publishTopicTestMeeting(t, s, "job-a", "transcript A", 1)
	s.runOnce(ctx) // caches rev1
	cli.write("revision", "embeddinggemma-2-text-int8@rev2")
	name := publishTopicTestMeeting(t, s, "job-b", "transcript B", 1)
	s.runOnce(ctx)
	if row := topicRow(t, s, name); row.State != topicStateQueued || row.Attempts != 0 {
		t.Fatalf("vectors from a model the worker did not read must not finish: %+v", row)
	}
	s.runOnce(ctx)
	if row := topicRow(t, s, name); row.State != topicStateDone {
		t.Fatalf("with fresh versions it finishes: %+v", row)
	}
}

func TestTopicsASkippedEmbedKeepsTheDigestAndLanguage(t *testing.T) {
	s, _ := newTopicsTestService(t)
	ctx := context.Background()
	name := publishTopicTestMeeting(t, s, "job-a", "transcript A", 1)
	s.runOnce(ctx)
	// The durable row forgets them (the meeting was unavailable once); the
	// vectors still hold the same transcript.
	s.store.db.Exec(`UPDATE topic_meeting SET payload_sha256=NULL,language=NULL,detector_version=NULL WHERE opus_name=?`, name)
	publishTopicTestMeeting(t, s, "job-a", "transcript A", 2)
	s.runOnce(ctx)
	var payload, language, detector sql.NullString
	s.store.db.QueryRow(`SELECT payload_sha256,language,detector_version FROM topic_meeting WHERE opus_name=?`, name).Scan(&payload, &language, &detector)
	if payload.String == "" || language.String != "en" || detector.String != "d1" {
		t.Fatalf("payload %q language %q detector %q", payload.String, language.String, detector.String)
	}
}

func TestTopicsSwitchingOffStopsAClaimThatWaitedForARecording(t *testing.T) {
	s, _ := newTopicsTestService(t)
	s.rt.cfg.RecordingPriority = true
	s.rt.cfg.RecordingIdleGrace = 0
	embedTopicTestControls(t, s)
	name := publishTopicTestMeeting(t, s, "job-a", "transcript A", 1)
	if !s.rt.reserveRecordSlot() {
		t.Fatal("no record slot")
	}
	done := make(chan bool)
	go func() { done <- s.runOnce(context.Background()) }()
	settings := s.rt.currentSettings()
	settings.TopicSuggestions.Enabled = false
	s.rt.setSettings(settings)
	s.rt.releaseRecordSlot()
	if <-done {
		t.Fatal("the worker claimed a meeting after suggestions were switched off")
	}
	if row := topicRow(t, s, name); row.State != topicStateQueued {
		t.Fatalf("row: %+v", row)
	}
}

func TestTopicsANewDeliveryUnblocksAndAModelFaultKeepsAPendingPublish(t *testing.T) {
	s, cli := newTopicsTestService(t)
	ctx := context.Background()
	embedTopicTestControls(t, s)
	name := publishTopicTestMeeting(t, s, "job-a", "transcript A", 1)
	row, _, _ := s.store.claimNext(ctx, true)
	publishTopicTestMeeting(t, s, "job-a", "transcript B", 2)
	if err := s.store.blockNow(ctx, row, topicReasonModelFault, "NaN"); err != nil {
		t.Fatal(err)
	}
	if got := topicRow(t, s, name); got.State != topicStateQueued {
		t.Fatalf("a publish during the faulty run wins: %+v", got)
	}
	cli.write("exit", "4")
	cli.write("stderr", "cassini topics embed: model fault")
	s.runOnce(ctx)
	if got := topicRow(t, s, name); got.State != topicStateBlocked {
		t.Fatalf("blocked: %+v", got)
	}
	cli.remove("exit")
	// Attempt 3 published and its hook never ran.
	s.rt.store.db.Exec(`INSERT INTO artifact_availability(job_id,published_attempt) VALUES('job-a',3) ON CONFLICT(job_id) DO UPDATE SET published_attempt=3`)
	if err := s.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := topicRow(t, s, name); got.State != topicStateQueued || got.Priority != topicPriorityPublish {
		t.Fatalf("a newer delivery gives a blocked meeting a new try: %+v", got)
	}
}

func TestTopicsStatusSaysNotReadyWhileOffWithoutAModel(t *testing.T) {
	s, cli := newTopicsTestService(t)
	settings := s.rt.currentSettings()
	settings.TopicSuggestions.Enabled = false
	s.rt.setSettings(settings)
	cli.setReady(false)
	s.rt.invalidateModelInventory()
	if st := s.rt.topicsStatus(context.Background()); st.Ready || st.Enabled || st.Reason != "" {
		t.Fatalf("status %+v", st)
	}
}

// embedTopicTestControls embeds the control definitions, so the next run's
// only CLI work is the meeting's embed.
func embedTopicTestControls(t *testing.T, s *topicsService) {
	t.Helper()
	if err := s.ensureTextVectors(context.Background(), topicVersions{ModelRevision: "embeddinggemma-2-text-int8@rev1", RecipeVersion: "r1"}); err != nil {
		t.Fatal(err)
	}
}

func retireTopicTestMeeting(t *testing.T, s *topicsService, name string) {
	t.Helper()
	var n int
	s.rt.store.db.QueryRow(`SELECT COUNT(*) FROM meeting_lifecycle`).Scan(&n)
	if _, err := s.rt.store.db.Exec(`INSERT INTO meeting_lifecycle(name,file_id,document_path,state,age_anchor,anchor_source) VALUES(?,?,?,?,?,?)`,
		name, 7+n, "/Cassini/meetings/"+name, "retired", time.Now().UTC().Format(time.RFC3339Nano), "test"); err != nil {
		t.Fatal(err)
	}
}

func TestTopicsRetirementScrubsBothStores(t *testing.T) {
	s, _ := newTopicsTestService(t)
	ctx := context.Background()
	name := publishTopicTestMeeting(t, s, "job-a", "transcript A", 1)
	s.runOnce(ctx)
	db := s.store.db
	for _, q := range []string{
		`INSERT INTO topic_suggestion(opus_name,tag_id,state,rule,support_words,definition_sha,scorer_key,created_at,updated_at) VALUES('job-a.opus','tag_x','pending','supporting-words',90,'d','s',1,1)`,
		`INSERT INTO topic_evidence(opus_name,tag_id,ordinal,start_ms,end_ms,word_count,counted) VALUES('job-a.opus','tag_x',0,0,9000,90,1)`,
		`INSERT INTO topic_verdict(opus_name,tag_id,verdict,source,actor_id,at) VALUES('job-a.opus','tag_y','rejected','review','alice',1)`,
		`INSERT INTO topic_receipt(caller,request_id,request_hash,opus_name,response,created_at) VALUES('alice','r1','h','job-a.opus','{"verdict":"rejected"}',1)`,
		`INSERT INTO topic_receipt(caller,request_id,request_hash,response,created_at) VALUES('alice','b1','h','{"results":[{"meetingId":"job-a","status":"confirmed"},{"meetingId":"job-b","status":"confirmed"}]}',1)`,
		`INSERT INTO topic_receipt_target(caller,request_id,opus_name) VALUES('alice','b1','job-a.opus'),('alice','b1','job-b.opus')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.vectors.forgetMeeting(ctx, name); err != nil {
		t.Fatal(err)
	}
	if err := s.store.inTx(ctx, func(tx *sql.Tx) error { return forgetTopicMeeting(ctx, tx, name) }); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"topic_meeting", "topic_suggestion", "topic_evidence", "topic_verdict"} {
		var n int
		db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE opus_name=?`, name).Scan(&n)
		if n != 0 {
			t.Fatalf("%s still holds the meeting", table)
		}
	}
	if _, ok, _ := s.vectors.meeting(ctx, name); ok {
		t.Fatal("vectors survive retirement")
	}
	var single, batch string
	db.QueryRow(`SELECT response FROM topic_receipt WHERE request_id='r1'`).Scan(&single)
	db.QueryRow(`SELECT response FROM topic_receipt WHERE request_id='b1'`).Scan(&batch)
	if single != `{"expired":true}` || strings.Contains(batch, `"meetingId":"job-a","status"`) || !strings.Contains(batch, `"meetingId":"job-b","status"`) {
		t.Fatalf("receipts: %s / %s", single, batch)
	}
	var targets int
	db.QueryRow(`SELECT COUNT(*) FROM topic_receipt_target WHERE opus_name=?`, name).Scan(&targets)
	if targets != 0 {
		t.Fatal("receipt targets keep the meeting")
	}
}

func TestTopicsSettingsEnableNeedsAReadyModelAndStatusHasNoContentCounts(t *testing.T) {
	s, cli := newTopicsTestService(t)
	settings := s.rt.currentSettings()
	settings.TopicSuggestions.Enabled = false
	s.rt.setSettings(settings)
	s.rt.settingsPath = filepath.Join(t.TempDir(), "settings.json")
	cli.setReady(false)
	s.rt.invalidateModelInventory()
	mux := http.NewServeMux()
	for _, route := range operatorAPIRoutes(s.rt, ExAppConfig{}) {
		mux.Handle(route.pattern, route.handler)
	}
	put := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/settings/topics", strings.NewReader(`{"enabled":true}`)))
		return rec
	}
	if rec := put(); rec.Code != http.StatusConflict {
		t.Fatalf("enabling without a ready model: %d %s", rec.Code, rec.Body)
	}
	cli.setReady(true)
	rec := put()
	if rec.Code != http.StatusOK {
		t.Fatalf("enabling with a ready model: %d %s", rec.Code, rec.Body)
	}
	var status map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil || status["enabled"] != true {
		t.Fatalf("status: %s %v", rec.Body, err)
	}
	if at, _ := s.store.meta(context.Background(), topicMetaEnabledAt); at == "" {
		t.Fatal("the first enable is recorded")
	}
	saved, err := LoadOrInitSettingsWithMigrationReporter(s.rt.settingsPath, nil)
	if err != nil || !saved.TopicSuggestions.Enabled {
		t.Fatalf("persisted: %+v %v", saved.TopicSuggestions, err)
	}
	get := httptest.NewRecorder()
	mux.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/settings/topics", nil))
	body := get.Body.String()
	if get.Code != http.StatusOK || !strings.Contains(body, `"scorerVersion"`) {
		t.Fatalf("the topics handler, not the LLM settings handler, answers: %d %s", get.Code, body)
	}
	for _, word := range []string{"suggestion", "verdict", "pending", "confirmed", "rejected"} {
		if strings.Contains(strings.ToLower(body), word) {
			t.Fatalf("ADMIN status mentions %q: %s", word, body)
		}
	}
	for _, path := range []string{"/settings/topics/backfill", "/settings/topics/retry"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("GET %s: %d", path, rec.Code)
		}
	}
}

func TestTopicsBackfillQueuesOnlyMeetingsWithNoRowSinceADate(t *testing.T) {
	s, _ := newTopicsTestService(t)
	ctx := context.Background()
	for _, job := range []struct{ id, created string }{{"old", "2026-01-05T10:00:00Z"}, {"new", "2026-09-05T10:00:00Z"}} {
		if _, err := s.rt.store.db.Exec(`INSERT INTO jobs(id,provider,request_json,stage,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, job.id, "talk", "{}", "done", "succeeded", job.created, job.created); err != nil {
			t.Fatal(err)
		}
	}
	done := publishTopicTestMeeting(t, s, "done", "transcript", 1)
	targets := []searchBackfillTarget{{"old", "old.opus"}, {"new", "new.opus"}, {"done", done}, {"unknown", "unknown.opus"}}
	since, _ := parseTopicSince("2026-06-01")
	result, err := queueTopicBackfill(ctx, s.store, s.rt.store, targets, since, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Queued != 2 || result.EstimateSeconds != 3*topicSecondsPerMeeting {
		t.Fatalf("result %+v", result)
	}
	var n int
	s.store.db.QueryRow(`SELECT COUNT(*) FROM topic_meeting WHERE opus_name='old.opus'`).Scan(&n)
	if n != 0 {
		t.Fatal("a meeting from before the date is not queued")
	}
	if row := topicRow(t, s, "new.opus"); row.Priority != topicPriorityBackfill {
		t.Fatalf("backfill priority: %+v", row)
	}
	if v, _ := s.store.meta(ctx, topicMetaBackfillSince); v != "2026-06-01" {
		t.Fatalf("backfill_since = %q", v)
	}
}

func TestRecordingStartCancelsEveryRunningPriorityTask(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.RecordingPriority = true
	first, doneFirst, err := rt.beginBackgroundBuild()
	if err != nil {
		t.Fatal(err)
	}
	defer doneFirst()
	second, doneSecond, err := rt.beginBackgroundBuild()
	if err != nil {
		t.Fatal(err)
	}
	defer doneSecond()
	if !rt.reserveRecordSlot() {
		t.Fatal("no record slot")
	}
	defer rt.releaseRecordSlot()
	for i, ctx := range []context.Context{first, second} {
		if !errors.Is(context.Cause(ctx), errRecordingPriority) {
			t.Fatalf("task %d was not cancelled: %v", i, context.Cause(ctx))
		}
	}
}

func TestTopicsRerunWhoseHookNeverRanIsQueuedByTheReconciler(t *testing.T) {
	s, _ := newTopicsTestService(t)
	ctx := context.Background()
	name := publishTopicTestMeeting(t, s, "job-a", "transcript A", 1)
	s.runOnce(ctx)
	// Attempt 2 published, then the operator died before the hook.
	if _, err := s.rt.store.db.Exec(`INSERT INTO artifact_availability(job_id,published_attempt) VALUES('job-a',2) ON CONFLICT(job_id) DO UPDATE SET published_attempt=2`); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	row := topicRow(t, s, name)
	if row.State != topicStateQueued || row.Priority != topicPriorityPublish {
		t.Fatalf("the missed rerun must be queued: %+v", row)
	}
	var attempt int
	var digest sql.NullString
	s.store.db.QueryRow(`SELECT published_attempt,delivered_digest FROM topic_meeting WHERE opus_name=?`, name).Scan(&attempt, &digest)
	if attempt != 2 || digest.Valid {
		t.Fatalf("published_attempt = %d, digest %v: the older delivery's digest must not be trusted", attempt, digest)
	}
}
