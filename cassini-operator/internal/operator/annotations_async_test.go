package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// JSON stands in for a media container; this exercises actual staging, upload,
// ETags, content verification and durable state without invoking ffmpeg.
func snapshotCLI(t *testing.T) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "cassini")
	script := `#!/usr/bin/env python3
import sys,json,hashlib
args=sys.argv[1:]
if args[0]!='annotate':sys.exit(9)
if args[1]=='show':
 data=open(args[2],'rb').read();r=json.loads(data)
elif args[1]=='carry':
 old=json.load(open(args[2]));r=json.load(open(args[3]));doc=old.get('annotations')
 r.update(annotations=doc,revision=old.get('revision',0),resolved=not doc or doc['audioOpusSha256']==r['audioOpusSha256'])
 data=json.dumps(r).encode();open(args[args.index('--out')+1],'wb').write(data)
elif args[1]=='snapshot':
 out=args[args.index('--out')+1];r=json.load(open(args[-1]));doc=json.load(sys.stdin)
 r.update(annotations=doc,revision=doc['revision'],resolved=doc['audioOpusSha256']==r['audioOpusSha256'])
 data=json.dumps(r).encode();open(out,'wb').write(data)
else:sys.exit(9)
r['containerSha256']=hashlib.sha256(data).hexdigest()
print(json.dumps(r))
`
	if err := os.WriteFile(name, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return name
}
func asyncFixture(t *testing.T) (*annotationsNextcloud, *annotationService, http.Handler, *annotationStore) {
	t.Helper()
	nc := newAnnotationsNextcloud(t, "MEETING1.opus")
	store := newTestAnnotationStore(t)
	empty := annotateResult{Format: annotateResultFormat, AudioOpusSHA256: testAudioDigest, DurationMS: 60000}
	data, _ := json.Marshal(empty)
	nc.seed(annTestRecording, string(data), nil)
	recordMarks(t, store, "MEETING1.opus", empty)
	s, h := tagChangeService(t, nc.url, snapshotCLI(t), store)
	return nc, s, h, store
}
func postAsync(t *testing.T, h http.Handler, body string) annotationsWriteResponse {
	t.Helper()
	r := annTestCall(h, http.MethodPost, "MEETING1", "alice", body)
	if r.Code != 200 {
		t.Fatalf("POST: %d %s", r.Code, r.Body.String())
	}
	var result annotationsWriteResponse
	if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func markRequest(label, key string) string {
	return `{"requestId":"` + key + `","ops":[{"op":"mark","tag":{"label":"` + label + `"},"target":{"kind":"meeting"}}]}`
}
func TestAnnotationAsyncCommitCoalescesAndReadsLatest(t *testing.T) {
	nc, s, h, store := asyncFixture(t)
	first := postAsync(t, h, markRequest("one", "req1"))
	last := postAsync(t, h, markRequest("two", "req2"))
	if len(nc.ifMatches()) != 0 {
		t.Fatal("request uploaded media")
	}
	if first.Sync.State != "pending" || last.Sync.Desired <= first.Sync.Desired {
		t.Fatal("versions did not advance")
	}
	got, err := store.document(context.Background(), "MEETING1.opus")
	if err != nil || !sameAnnotationDocument(got.Annotations, last.Annotations) {
		t.Fatalf("latest read: %+v %v", got, err)
	}
	tags, err := store.Vocabulary(context.Background(), []string{"MEETING1.opus"})
	if err != nil || len(tags) != 2 {
		t.Fatalf("query rows not committed: %+v %v", tags, err)
	}
	if err = s.syncAnnotation(context.Background(), "MEETING1.opus"); err != nil {
		t.Fatal(err)
	}
	if err = s.syncAnnotation(context.Background(), "MEETING1.opus"); err != nil {
		t.Fatal(err)
	}
	if len(nc.ifMatches()) != 1 {
		t.Fatalf("wanted one coalesced upload, got %v", nc.ifMatches())
	}
	var archived annotateResult
	if err = json.Unmarshal([]byte(nc.recording(annTestRecording)), &archived); err != nil {
		t.Fatal(err)
	}
	if !sameAnnotationDocument(archived.Annotations, last.Annotations) {
		t.Fatal("wrong snapshot archived")
	}
}
func TestAnnotationRequestReplayAndRollback(t *testing.T) {
	_, _, h, store := asyncFixture(t)
	first := postAsync(t, h, markRequest("one", "req1"))
	second := postAsync(t, h, markRequest("two", "req2"))
	replay := postAsync(t, h, markRequest("one", "req1"))
	if replay.StateToken != first.StateToken || !sameAnnotationDocument(replay.Annotations, first.Annotations) {
		t.Fatal("replay did not return original receipt")
	}
	if r := annTestCall(h, http.MethodPost, "MEETING1", "alice", markRequest("collision", "req1")); r.Code != 409 {
		t.Fatalf("key reuse: %d", r.Code)
	}
	stale := strings.Replace(markRequest("stale", "req3"), `"ops":`, `"stateToken":"`+first.StateToken+`","ops":`, 1)
	if r := annTestCall(h, http.MethodPost, "MEETING1", "alice", stale); r.Code != 409 {
		t.Fatalf("stale token: %d", r.Code)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_item BEFORE INSERT ON annotation_item BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if r := annTestCall(h, http.MethodPost, "MEETING1", "alice", markRequest("three", "req3")); r.Code == 200 {
		t.Fatal("acknowledged failed transaction")
	}
	got, err := store.document(context.Background(), "MEETING1.opus")
	if err != nil || got.StateToken != second.StateToken {
		t.Fatalf("head changed on rollback: %+v %v", got, err)
	}
	var count int
	store.db.QueryRow(`SELECT COUNT(*) FROM annotation_receipt WHERE request_id='req3'`).Scan(&count)
	if count != 0 {
		t.Fatal("receipt escaped rollback")
	}
}

type annotationRoundTripper func(*http.Request) (*http.Response, error)

func (f annotationRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestAnnotationUploadConfirmsOnlyItsTarget(t *testing.T) {
	_, s, h, store := asyncFixture(t)
	first := postAsync(t, h, markRequest("one", "req1"))
	transport := s.client.Transport
	var latest annotationsWriteResponse
	s.client.Transport = annotationRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPut {
			latest = postAsync(t, h, markRequest("two", "req2"))
		}
		return transport.RoundTrip(r)
	})
	if err := s.syncAnnotation(context.Background(), "MEETING1.opus"); err != nil {
		t.Fatal(err)
	}
	got, err := store.document(context.Background(), "MEETING1.opus")
	if err != nil || got.Sync.Confirmed != first.Sync.Desired || got.Sync.Desired != latest.Sync.Desired || got.Sync.State != "pending" {
		t.Fatalf("wrong acknowledgement: %+v %v", got, err)
	}
	s.client.Transport = transport
	if err := s.syncAnnotation(context.Background(), "MEETING1.opus"); err != nil {
		t.Fatal(err)
	}
}
func TestAnnotationLostPUTResponseRecoversExactSnapshot(t *testing.T) {
	nc, s, h, store := asyncFixture(t)
	postAsync(t, h, markRequest("one", "req1"))
	transport := s.client.Transport
	s.client.Transport = annotationRoundTripper(func(r *http.Request) (*http.Response, error) {
		response, err := transport.RoundTrip(r)
		if r.Method == http.MethodPut && err == nil {
			drainClose(response.Body)
			return nil, errors.New("lost response")
		}
		return response, err
	})
	if err := s.syncAnnotation(context.Background(), "MEETING1.opus"); err == nil {
		t.Fatal("wanted ambiguous PUT")
	}
	s.client.Transport = transport
	postAsync(t, h, markRequest("two", "req2"))
	if err := s.syncAnnotation(context.Background(), "MEETING1.opus"); err != nil {
		t.Fatal(err)
	}
	if len(nc.ifMatches()) != 2 {
		t.Fatalf("replayed unknown PUT: %v", nc.ifMatches())
	}
	got, _ := store.document(context.Background(), "MEETING1.opus")
	if got.Sync.State != "saved" {
		t.Fatalf("not reconciled: %+v", got.Sync)
	}
}
func TestAnnotationPendingSurvivesReimportAndUnavailable(t *testing.T) {
	_, _, h, store := asyncFixture(t)
	last := postAsync(t, h, markRequest("one", "req1"))
	if err := store.MarkUnavailable(context.Background(), "MEETING1.opus", "read failed"); err != nil {
		t.Fatal(err)
	}
	delivered := annotateResult{Format: annotateResultFormat, AudioOpusSHA256: strings.Repeat("f", 64), DurationMS: 90000}
	if _, err := store.record(context.Background(), "MEETING1.opus", delivered, false); err != nil {
		t.Fatal(err)
	}
	got, err := store.document(context.Background(), "MEETING1.opus")
	if err != nil || !sameAnnotationDocument(got.Annotations, last.Annotations) || got.Resolved == nil || *got.Resolved {
		t.Fatalf("pending state lost or rebound: %+v %v", got, err)
	}
}
func TestAnnotationMissingStoreRefusesWrite(t *testing.T) {
	nc := newAnnotationsNextcloud(t, "MEETING1.opus")
	h, _ := annTestService(t, nc.url, "must-not-run", nil)
	if r := annTestCall(h, http.MethodPost, "MEETING1", "alice", annTestMark); r.Code != 503 {
		t.Fatalf("no durable store: %d", r.Code)
	}
	if len(nc.ifMatches()) > 0 {
		t.Fatal("wrote without a durable store")
	}
}

func TestAnnotationWorkerSelectsLatestAfterFileLock(t *testing.T) {
	nc, s, h, _ := asyncFixture(t)
	postAsync(t, h, markRequest("one", "req1"))
	release, err := annotationWriteLocks.acquire(context.Background(), "MEETING1.opus")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.syncAnnotation(context.Background(), "MEETING1.opus") }()
	latest := postAsync(t, h, markRequest("two", "req2"))
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var remote annotateResult
	json.Unmarshal([]byte(nc.recording(annTestRecording)), &remote)
	if !sameAnnotationDocument(remote.Annotations, latest.Annotations) {
		t.Fatal("worker selected before acquiring file lock")
	}
}
func TestAnnotationWorkerBlocksUnexpectedArchiveWithoutLosingDesired(t *testing.T) {
	nc, s, h, store := asyncFixture(t)
	last := postAsync(t, h, markRequest("one", "req1"))
	other := annotatedFile(t, "", testTagNamespaceA, []testTag{{"other", "external"}}, meetingMark("external", "other"))
	data, _ := json.Marshal(other)
	nc.seed(annTestRecording, string(data), nil)
	err := s.syncAnnotation(context.Background(), "MEETING1.opus")
	var blocked *annotationBlocked
	if !errors.As(err, &blocked) {
		t.Fatalf("wanted repair block, got %v", err)
	}
	s.retryAnnotation("MEETING1.opus", err)
	got, _ := store.document(context.Background(), "MEETING1.opus")
	if got.Sync.State != "blocked" || got.StateToken != last.StateToken {
		t.Fatalf("desired lost: %+v", got)
	}
	if len(nc.ifMatches()) != 0 {
		t.Fatal("overwrote unknown remote state")
	}
}

func TestAnnotationWorkerClassifiesRenderFailures(t *testing.T) {
	for _, tc := range []struct {
		code  int
		state string
	}{{annotateExitInvalid, "blocked"}, {annotateExitUsage, "blocked"}, {annotateExitUnresolved, "blocked"}, {annotateExitRuntime, "delayed"}} {
		t.Run(fmt.Sprint(tc.code), func(t *testing.T) {
			nc, s, h, store := asyncFixture(t)
			last := postAsync(t, h, markRequest("one", "req1"))
			script, err := os.ReadFile(s.bin)
			if err != nil {
				t.Fatal(err)
			}
			script = []byte(strings.Replace(string(script), "elif args[1]=='snapshot':", fmt.Sprintf("elif args[1]=='snapshot':\n sys.exit(%d)", tc.code), 1))
			if err := os.WriteFile(s.bin, script, 0755); err != nil {
				t.Fatal(err)
			}
			err = s.syncAnnotation(context.Background(), "MEETING1.opus")
			if err == nil {
				t.Fatal("render failure was ignored")
			}
			s.retryAnnotation("MEETING1.opus", err)
			got, err := store.document(context.Background(), "MEETING1.opus")
			if err != nil || got.Sync.State != tc.state || got.StateToken != last.StateToken {
				t.Fatalf("render failure lost desired state or retry classification: %+v %v", got, err)
			}
			if len(nc.ifMatches()) != 0 {
				t.Fatal("uploaded after failed render")
			}
		})
	}
}
func TestAnnotationDurableScanAndReceiptSurviveRestart(t *testing.T) {
	nc, s, h, store := asyncFixture(t)
	first := postAsync(t, h, markRequest("one", "req1"))
	dbPath := store.path
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openAnnotationStore(dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	s, h = tagChangeService(t, nc.url, snapshotCLI(t), reopened)
	name, err := s.nextAnnotation()
	if err != nil || name != "MEETING1.opus" {
		t.Fatalf("lost wakeup was not recovered: %q %v", name, err)
	}
	s.claimed.Delete(name)
	replay := postAsync(t, h, markRequest("one", "req1"))
	if replay.StateToken != first.StateToken {
		t.Fatal("restart lost request receipt")
	}
	if err = s.syncAnnotation(context.Background(), name); err != nil {
		t.Fatal(err)
	}
}
func TestAnnotationBulkResumeDoesNotReplayCompletedMutation(t *testing.T) {
	nc, s, h, store := asyncFixture(t)
	postAsync(t, h, markRequest("one", "req1"))
	doc, _ := store.document(context.Background(), "MEETING1.opus")
	var parsed projectedDocument
	json.Unmarshal(doc.Annotations, &parsed)
	op := json.RawMessage(`{"op":"relabel","tagId":"` + parsed.Tags[0].ID + `","label":"renamed"}`)
	job := &tagJob{ID: "resume_test", Actor: "alice", Kind: tagJobRename, TagID: parsed.Tags[0].ID, State: tagJobRunning, Total: 1}
	if err := s.persistTagJob(job, []string{"MEETING1.opus"}, op, nil); err != nil {
		t.Fatal(err)
	}
	next, _ := tagChangeService(t, nc.url, snapshotCLI(t), store)
	result := waitTagJob(t, next, "alice")
	if result.Done != 1 || len(result.Failed) != 0 {
		t.Fatalf("resume failed: %+v", result)
	}
	after, _ := store.document(context.Background(), "MEETING1.opus")
	if !strings.Contains(string(after.Annotations), "renamed") {
		t.Fatal("durable target not executed")
	}
}

func TestAnnotationReceiptRetentionAndSnapshotCollection(t *testing.T) {
	_, s, h, store := asyncFixture(t)
	first := postAsync(t, h, markRequest("one", "req1"))
	if _, err := store.db.Exec(`UPDATE annotation_receipt SET created_at=unixepoch()-700000`); err != nil {
		t.Fatal(err)
	}
	if err := store.collectSnapshots(context.Background()); err != nil {
		t.Fatal(err)
	}
	replay := postAsync(t, h, markRequest("one", "req1"))
	if replay.StateToken != first.StateToken {
		t.Fatal("pending receipt expired")
	}
	if err := s.syncAnnotation(context.Background(), "MEETING1.opus"); err != nil {
		t.Fatal(err)
	}
	if err := store.collectSnapshots(context.Background()); err != nil {
		t.Fatal(err)
	}
	var receipts, snapshots int
	store.db.QueryRow(`SELECT COUNT(*) FROM annotation_receipt`).Scan(&receipts)
	store.db.QueryRow(`SELECT COUNT(*) FROM annotation_snapshot`).Scan(&snapshots)
	if receipts != 0 || snapshots != 1 {
		t.Fatalf("unreferenced history retained: receipts=%d snapshots=%d", receipts, snapshots)
	}
}
func TestAnnotationRetryPreservesDocumentAndRequiresAccess(t *testing.T) {
	nc, _, h, store := asyncFixture(t)
	first := postAsync(t, h, markRequest("one", "req1"))
	store.db.Exec(`UPDATE annotation_head SET blocked=1,attempts=2,retry_at=unixepoch()+3600,last_error='repair'`)
	retried := postAsync(t, h, `{"ops":[],"retrySync":true}`)
	if retried.StateToken != first.StateToken || !sameAnnotationDocument(retried.Annotations, first.Annotations) || retried.Sync.State != "pending" {
		t.Fatalf("retry changed desired: %+v", retried)
	}
	nc.frontMu.Lock()
	delete(nc.visible, "MEETING1.opus")
	nc.frontMu.Unlock()
	if r := annTestCall(h, http.MethodPost, "MEETING1", "alice", `{"ops":[],"retrySync":true}`); r.Code != 404 {
		t.Fatalf("unauthorized retry: %d", r.Code)
	}
}

func TestAnnotationValidationIsAtomicAndDuplicateMarkIsNoop(t *testing.T) {
	_, _, h, store := asyncFixture(t)
	first := postAsync(t, h, markRequest("one", "req1"))
	again := postAsync(t, h, markRequest("one", "req2"))
	if again.StateToken != first.StateToken || len(again.Added) != 0 {
		t.Fatal("duplicate mark created another desired version")
	}
	for _, ops := range []string{
		`[{"op":"mark","tag":{"label":"bad range"},"target":{"kind":"time-range","startMs":0,"endMs":60001}}]`,
		`[{"op":"mark","tag":{"label":"valid"},"target":{"kind":"meeting"}},{"op":"unmark","itemid":"typo"}]`,
		`[{"op":"mark","tag":{"label":" "},"target":{"kind":"meeting"}}]`,
	} {
		if r := annTestCall(h, http.MethodPost, "MEETING1", "alice", `{"ops":`+ops+`}`); r.Code != 400 {
			t.Fatalf("invalid batch: %d %s", r.Code, r.Body.String())
		}
	}
	got, _ := store.document(context.Background(), "MEETING1.opus")
	if got.StateToken != first.StateToken {
		t.Fatal("invalid batch partially committed")
	}
}

func TestAnnotationDueRetryIsNotStarvedByFreshEdits(t *testing.T) {
	_, s, h, store := asyncFixture(t)
	postAsync(t, h, markRequest("one", "req1"))
	store.db.Exec(`UPDATE annotation_head SET attempts=1,retry_at=unixepoch()-1 WHERE opus_name='MEETING1.opus'`)
	empty := annotateResult{Format: annotateResultFormat, AudioOpusSHA256: testAudioDigest, DurationMS: 60000}
	recordMarks(t, store, "OTHER.opus", empty)
	if _, err := store.db.Exec(`INSERT INTO annotation_snapshot(opus_name,result_json) SELECT 'OTHER.opus',result_json FROM annotation_snapshot WHERE id=(SELECT desired FROM annotation_head WHERE opus_name='OTHER.opus');UPDATE annotation_head SET desired=last_insert_rowid() WHERE opus_name='OTHER.opus'`); err != nil {
		t.Fatal(err)
	}
	name, err := s.nextAnnotation()
	if err != nil || name != "MEETING1.opus" {
		t.Fatalf("due retry starved: %q %v", name, err)
	}
	s.claimed.Delete(name)
	next := postAsync(t, h, markRequest("two", "req2"))
	if next.Sync.State != "delayed" {
		t.Fatalf("response hides retry state: %+v", next.Sync)
	}
}
