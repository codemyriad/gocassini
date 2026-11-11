package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// retagCLI stands in for `cassini retag --recorded-at-utc` and `cassini
// annotate show`. JSON stands in for the media container, as in snapshotCLI:
// retag sets the field and reports it the way the --json summary does; a file
// already carrying the value is reported unchanged. Every run is counted.
func retagCLI(t *testing.T, exitCode int) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "cassini")
	script := `#!/usr/bin/env python3
import sys,json,hashlib
open(sys.argv[0]+'.runs','a').write(' '.join(sys.argv[1:3])+'\n')
args=sys.argv[1:]
if args[0]=='retag':
 if ` + map[bool]string{true: "True", false: "False"}[exitCode != 0] + `:
  sys.stderr.write('flag provided but not defined: -recorded-at-utc\n');sys.exit(` + string(rune('0'+exitCode)) + `)
 src=args[1];out=args[args.index('--out')+1];value=args[args.index('--recorded-at-utc')+1]
 r=json.load(open(src));before=r.get('recordedAtUtc')
 changes=[] if before==value else [{'field':'recordedAtUtc','from':before,'to':value}]
 r['recordedAtUtc']=value
 open(out,'w').write(json.dumps(r))
 print(json.dumps({'input':src,'output':out,'meetingId':'MEETING1','changes':changes}))
 sys.exit(0)
if args[0]=='annotate' and args[1]=='show':
 data=open(args[2],'rb').read();r=json.loads(data)
 r['containerSha256']=hashlib.sha256(data).hexdigest()
 print(json.dumps(r));sys.exit(0)
sys.exit(9)
`
	if err := os.WriteFile(name, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return name
}

func cliRuns(t *testing.T, bin, verb string) int {
	t.Helper()
	raw, err := os.ReadFile(bin + ".runs")
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, verb) {
			n++
		}
	}
	return n
}

const testRecordedAt = "2026-11-09T11:32:27.181Z"

type fileWorkerFixture struct {
	nc     *annotationsNextcloud
	w      *recordedAtFileWorker
	store  *annotationStore
	bin    string
	clock  time.Time
	logs   *bytes.Buffer
	jobsDB *Store
}

func newFileWorkerFixture(t *testing.T, exitCode int) *fileWorkerFixture {
	t.Helper()
	f := &fileWorkerFixture{nc: newAnnotationsNextcloud(t, "MEETING1.opus"), store: newTestAnnotationStore(t), bin: retagCLI(t, exitCode)}
	delivered := annotateResult{Format: annotateResultFormat, AudioOpusSHA256: testAudioDigest, DurationMS: 60000}
	data, _ := json.Marshal(delivered)
	f.nc.seed(annTestRecording, string(data), nil)
	recordMarks(t, f.store, "MEETING1.opus", delivered)

	dir := t.TempDir()
	metadata, err := openMeetingMetadataStore(filepath.Join(dir, meetingMetadataFilename), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = metadata.Close() })
	jobs, err := OpenStore(filepath.Join(dir, "jobs.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = jobs.Close() })
	f.jobsDB = jobs
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	f.logs = &bytes.Buffer{}
	rt := &Runtime{ctx: ctx, meetingMetadata: metadata, annotations: f.store, logger: log.New(io.Discard, "", 0)}
	rt.cfg.CassiniBin = f.bin
	rt.cfg.WorkRoot = dir
	cfg := testExAppConfig(f.nc.url)
	cfg.PublishSink = publishSinkNextcloudFiles
	cfg.lifecycle = jobs
	f.w = newRecordedAtFileWorker(rt, cfg, &http.Client{Transport: http.DefaultTransport}, log.New(f.logs, "", 0))
	if f.w == nil {
		t.Fatal("worker must exist with an index, a CLI and a client")
	}
	f.clock = time.Now()
	f.w.now = func() time.Time { return f.clock }
	if err := metadata.Put(ctx, 42, "MEETING1.opus", json.RawMessage(`{"id":"MEETING1","dateLabel":"2026-11-09 11:31","audioPath":"./meetings/MEETING1.opus"}`)); err != nil {
		t.Fatal(err)
	}
	tx, err := metadata.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := queueRecordedAtFileTx(ctx, tx, "MEETING1.opus", testRecordedAt, recordedAtStatePending); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return f
}

type fileRow struct {
	state, lastError string
	attempts         int
	retryAt          int64
}

func (f *fileWorkerFixture) row(t *testing.T) fileRow {
	t.Helper()
	var r fileRow
	if err := f.w.rt.meetingMetadata.db.QueryRow(`SELECT state, attempts, retry_at, last_error FROM recorded_at_file_backfill WHERE opus_name='MEETING1.opus'`).Scan(&r.state, &r.attempts, &r.retryAt, &r.lastError); err != nil {
		t.Fatal(err)
	}
	return r
}

func (f *fileWorkerFixture) remote(t *testing.T) map[string]any {
	t.Helper()
	var r map[string]any
	if err := json.Unmarshal([]byte(f.nc.recording(annTestRecording)), &r); err != nil {
		t.Fatalf("remote is not the stand-in container: %v", err)
	}
	return r
}

func (f *fileWorkerFixture) indexedContainer(t *testing.T) string {
	t.Helper()
	return readAnnotationRow(t, f.store, "MEETING1.opus").container
}

func TestRecordedAtFileWorkerRetagsVerifiesAndReconciles(t *testing.T) {
	f := newFileWorkerFixture(t, 0)
	ctx := context.Background()
	if n := f.w.drain(ctx); n != 1 {
		t.Fatalf("drained %d", n)
	}
	if r := f.row(t); r.state != "done" || r.attempts != 0 {
		t.Fatalf("row = %+v; logs: %s", r, f.logs)
	}
	remote := f.remote(t)
	if remote["recordedAtUtc"] != testRecordedAt || remote["audioOpusSha256"] != testAudioDigest {
		t.Fatalf("remote = %v", remote)
	}
	if matches := f.nc.ifMatches(); len(matches) != 1 || matches[0] != `"0"` {
		t.Fatalf("PUT must be conditional on the ETag read: %v", matches)
	}
	// The annotation index now knows the new container, as after a republish.
	if want := fileSHA256Of(t, f.nc.recording(annTestRecording)); f.indexedContainer(t) != want {
		t.Fatalf("index container = %q, want %q", f.indexedContainer(t), want)
	}
	doc, err := f.store.document(ctx, "MEETING1.opus")
	if err != nil || doc.Sync.State != "saved" {
		t.Fatalf("annotation sync = %+v %v", doc.Sync, err)
	}

	// Done is done: nothing is due.
	if n := f.w.drain(ctx); n != 0 {
		t.Fatalf("second drain processed %d", n)
	}
	// Requeued (say after a crash before the state write): the file already
	// carries the value, so no upload happens.
	if _, err := f.w.rt.meetingMetadata.db.Exec(`UPDATE recorded_at_file_backfill SET state='pending'`); err != nil {
		t.Fatal(err)
	}
	f.w.drain(ctx)
	if r := f.row(t); r.state != "done" || len(f.nc.ifMatches()) != 1 {
		t.Fatalf("idempotent pass uploaded: row %+v, PUTs %v", r, f.nc.ifMatches())
	}
}

func fileSHA256Of(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	digest, err := fileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

// Somebody else wrote the file between our read and our PUT: If-Match refuses
// it, their write survives, and the row retries later with backoff. Nextcloud
// wrote nothing of ours, so the row is pending again, not an upload to settle.
func TestRecordedAtFileWorkerIfMatchConflictRetriesLater(t *testing.T) {
	f := newFileWorkerFixture(t, 0)
	ctx := context.Background()
	theirs := `{"format":"cassini.annotate.result.v1","audioOpusSha256":"` + testAudioDigest + `","durationMs":60000,"title":"theirs"}`
	interfered := false
	f.w.client.Transport = annotationRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPut && !interfered {
			interfered = true
			f.nc.fakeNCFiles.mu.Lock()
			f.nc.files[annTestRecording] = []byte(theirs)
			f.nc.etags[annTestRecording]++
			f.nc.fakeNCFiles.mu.Unlock()
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	f.w.drain(ctx)
	r := f.row(t)
	if r.state != recordedAtStatePending || r.attempts != 1 || r.retryAt <= f.clock.Unix() {
		t.Fatalf("row after conflict = %+v", r)
	}
	if f.nc.recording(annTestRecording) != theirs {
		t.Fatal("a refused PUT must leave their write in place")
	}
	if n := f.w.drain(ctx); n != 0 {
		t.Fatal("a backed-off row must not be retried before its time")
	}
	f.clock = time.Unix(r.retryAt, 0).Add(time.Second)
	f.w.drain(ctx)
	if r := f.row(t); r.state != "done" {
		t.Fatalf("retry = %+v; logs: %s", r, f.logs)
	}
	remote := f.remote(t)
	if remote["recordedAtUtc"] != testRecordedAt || remote["title"] != "theirs" {
		t.Fatalf("retry must re-tag their version: %v", remote)
	}
}

// The upload cannot be verified by re-download: the row does not advance and
// the annotation index is not told about bytes nobody confirmed. The next
// attempt settles it from what is actually stored.
func TestRecordedAtFileWorkerVerifyFailureDoesNotAdvance(t *testing.T) {
	f := newFileWorkerFixture(t, 0)
	ctx := context.Background()
	original := f.indexedContainer(t)
	puts := 0
	f.w.client.Transport = annotationRoundTripper(func(r *http.Request) (*http.Response, error) {
		response, err := http.DefaultTransport.RoundTrip(r)
		if r.Method == http.MethodPut {
			puts++
		}
		if err == nil && r.Method == http.MethodGet && puts == 1 {
			// The verifying download reads different bytes.
			drainClose(response.Body)
			response.Body = io.NopCloser(strings.NewReader(`{"corrupt":true}`))
		}
		return response, err
	})
	f.w.drain(ctx)
	r := f.row(t)
	if r.state == "done" || r.attempts != 1 {
		t.Fatalf("unverified upload advanced: %+v", r)
	}
	if f.indexedContainer(t) != original {
		t.Fatal("annotation index reconciled against unverified bytes")
	}
	f.w.client.Transport = http.DefaultTransport
	f.clock = time.Unix(r.retryAt, 0).Add(time.Second)
	f.w.drain(ctx)
	if r := f.row(t); r.state != "done" {
		t.Fatalf("recovery = %+v; logs: %s", r, f.logs)
	}
	if len(f.nc.ifMatches()) != 1 {
		t.Fatalf("the landed upload must not be sent again: %v", f.nc.ifMatches())
	}
	if want := fileSHA256Of(t, f.nc.recording(annTestRecording)); f.indexedContainer(t) != want {
		t.Fatal("recovery must reconcile the index with the stored file")
	}
}

// A mark waiting to be written owns the next version of the file; so does a
// retention transition. Neither is a failure: the row waits, untouched.
func TestRecordedAtFileWorkerDefersToInFlightWrites(t *testing.T) {
	t.Run("annotation write pending", func(t *testing.T) {
		f := newFileWorkerFixture(t, 0)
		if _, err := f.store.db.Exec(`UPDATE annotation_head SET in_flight=confirmed WHERE opus_name='MEETING1.opus'`); err != nil {
			t.Fatal(err)
		}
		f.w.drain(context.Background())
		r := f.row(t)
		if r.state != "pending" || r.attempts != 0 || r.retryAt <= f.clock.Unix() || !strings.Contains(r.lastError, "annotation write") {
			t.Fatalf("row = %+v", r)
		}
		if _, owner := f.nc.gets(); len(owner) != 0 || cliRuns(t, f.bin, "retag") != 0 {
			t.Fatalf("deferred file was read (%v) or re-tagged", owner)
		}
	})
	t.Run("retention transition pending", func(t *testing.T) {
		f := newFileWorkerFixture(t, 0)
		if _, err := f.jobsDB.db.Exec(`INSERT INTO meeting_lifecycle(name,file_id,document_path,state,age_anchor,anchor_source) VALUES('MEETING1.opus',42,?,'active','2026-11-09T11:31:00Z','createdAtUtc')`, annTestRecording); err != nil {
			t.Fatal(err)
		}
		if _, err := f.jobsDB.db.Exec(`INSERT INTO remote_retention_operation(name,operation_json,status,updated_at) VALUES('MEETING1.opus','{}','pending','now')`); err != nil {
			t.Fatal(err)
		}
		f.w.drain(context.Background())
		if r := f.row(t); r.state != "pending" || r.attempts != 0 || !strings.Contains(r.lastError, "retention") {
			t.Fatalf("row = %+v", r)
		}
		if cliRuns(t, f.bin, "retag") != 0 {
			t.Fatal("re-tagged under a retention transition")
		}
	})
}

// A file that already states a different instant keeps it, and the index
// follows the file.
func TestRecordedAtFileWorkerKeepsTheFilesOwnInstant(t *testing.T) {
	f := newFileWorkerFixture(t, 0)
	own := `{"format":"cassini.annotate.result.v1","audioOpusSha256":"` + testAudioDigest + `","durationMs":60000,"recordedAtUtc":"2026-11-09T11:32:28Z"}`
	f.nc.seed(annTestRecording, own, nil)
	f.w.drain(context.Background())
	if r := f.row(t); r.state != "done" {
		t.Fatalf("row = %+v", r)
	}
	if f.nc.recording(annTestRecording) != own || len(f.nc.ifMatches()) != 0 {
		t.Fatal("the file's own instant was overwritten")
	}
	var entry, column string
	if err := f.w.rt.meetingMetadata.db.QueryRow(`SELECT entry_json, recorded_at_utc FROM meeting_metadata WHERE file_id=42`).Scan(&entry, &column); err != nil {
		t.Fatal(err)
	}
	if column != "2026-11-09T11:32:28.000Z" || entryField(t, entry, "recordedAtUtc") != "2026-11-09T11:32:28Z" {
		t.Fatalf("index = %s %q", entry, column)
	}
}

// A CLI without the flag cannot ever succeed: blocked, said once, not retried.
func TestRecordedAtFileWorkerBlocksOnUnsupportedCLI(t *testing.T) {
	f := newFileWorkerFixture(t, 2)
	f.w.drain(context.Background())
	r := f.row(t)
	if r.state != "blocked" || !strings.Contains(r.lastError, "upgrade") {
		t.Fatalf("row = %+v", r)
	}
	// Beside the English, the code the app words it by (D-921).
	var code string
	if err := f.w.rt.meetingMetadata.db.QueryRow(`SELECT last_error_code FROM recorded_at_file_backfill WHERE opus_name='MEETING1.opus'`).Scan(&code); err != nil || code != "cli_too_old" {
		t.Fatalf("last_error_code = %q, %v", code, err)
	}
	if n := f.w.drain(context.Background()); n != 0 {
		t.Fatal("blocked rows are not retried")
	}
}

// writeLockHeld reports whether some caller holds the per-recording lock.
func writeLockHeld(name string) bool {
	annotationWriteLocks.mu.Lock()
	defer annotationWriteLocks.mu.Unlock()
	lock := annotationWriteLocks.locks[name]
	return lock != nil && len(lock.slot) == 1
}

// A publish replaces the file and requeues its row under the per-recording
// lock. An attempt's outcome is written under that lock too, so it can never
// land on the publish's fresh row: here a blocked outcome would otherwise
// turn the requeued file blocked for good.
func TestRecordedAtFileWorkerOutcomeCannotOverwriteAPublishRequeue(t *testing.T) {
	f := newFileWorkerFixture(t, 2)
	ctx := context.Background()
	var heldAtOutcome bool
	published := make(chan error, 1)
	f.w.afterAttempt = func(name string) {
		heldAtOutcome = writeLockHeld(name)
		go func() {
			release, err := annotationWriteLocks.acquire(ctx, name)
			if err != nil {
				published <- err
				return
			}
			defer release()
			db := f.w.rt.meetingMetadata.db
			if _, err := db.Exec(`DELETE FROM recorded_at_file_backfill WHERE opus_name=?`, name); err != nil {
				published <- err
				return
			}
			tx, err := db.Begin()
			if err != nil {
				published <- err
				return
			}
			if err := queueRecordedAtFileTx(ctx, tx, name, testRecordedAt, recordedAtStatePending); err != nil {
				_ = tx.Rollback()
				published <- err
				return
			}
			published <- tx.Commit()
		}()
	}
	f.w.processOne(ctx, "MEETING1.opus")
	if err := <-published; err != nil {
		t.Fatal(err)
	}
	if !heldAtOutcome {
		t.Fatal("the outcome was written after the recording's lock was released")
	}
	if r := f.row(t); r.state != "pending" {
		t.Fatalf("the publish's requeued row = %+v, want pending", r)
	}
}

// A blocked file is retried on the next operator start, once a person had
// the chance to fix what blocked it; other states are left as they are.
func TestRecordedAtFileWorkerRequeuesBlockedOnStart(t *testing.T) {
	f := newFileWorkerFixture(t, 2)
	ctx := context.Background()
	f.w.drain(ctx)
	if r := f.row(t); r.state != "blocked" {
		t.Fatalf("row = %+v, want blocked", r)
	}
	db := f.w.rt.meetingMetadata.db
	if _, err := db.Exec(`INSERT INTO recorded_at_file_backfill(opus_name, recorded_at_utc, state, updated_at) VALUES('DONE.opus', ?, 'done', 'now')`, testRecordedAt); err != nil {
		t.Fatal(err)
	}
	n, err := f.w.requeueBlocked(ctx)
	if err != nil || n != 1 {
		t.Fatalf("requeued = %d %v, want 1", n, err)
	}
	if r := f.row(t); r.state != "pending" || r.attempts != 0 || r.retryAt != 0 {
		t.Fatalf("row = %+v, want pending and due", r)
	}
	var done string
	if err := db.QueryRow(`SELECT state FROM recorded_at_file_backfill WHERE opus_name='DONE.opus'`).Scan(&done); err != nil || done != "done" {
		t.Fatalf("done row = %q %v", done, err)
	}
}

// A drain pauses between two files, never before the first, and stops when
// its pause is cut short: the backfill of an archive is paced, not a burst.
func TestRecordedAtFileWorkerPacesBetweenFiles(t *testing.T) {
	f := newFileWorkerFixture(t, 2)
	ctx := context.Background()
	db := f.w.rt.meetingMetadata.db
	for _, name := range []string{"OTHER1.opus", "OTHER2.opus"} {
		if _, err := db.Exec(`INSERT INTO recorded_at_file_backfill(opus_name, recorded_at_utc, state, updated_at) VALUES(?, ?, 'pending', 'now')`, name, testRecordedAt); err != nil {
			t.Fatal(err)
		}
	}
	pauses := 0
	f.w.pause = func(context.Context) bool { pauses++; return true }
	if n := f.w.drain(ctx); n != 3 || pauses != 2 {
		t.Fatalf("processed %d with %d pauses, want 3 with 2", n, pauses)
	}

	if _, err := db.Exec(`UPDATE recorded_at_file_backfill SET state='pending', retry_at=0`); err != nil {
		t.Fatal(err)
	}
	f.w.pause = func(context.Context) bool { return false }
	if n := f.w.drain(ctx); n != 1 {
		t.Fatalf("processed %d after a cut-short pause, want 1", n)
	}
	if name, err := f.w.next(ctx); err != nil || name == "" {
		t.Fatalf("the file left by the stopped drain is not claimable again: %q %v", name, err)
	}
}

// An earlier PUT whose outcome was unknown may still land. Here it lands just
// before the settle attempt's own PUT, which If-Match then refuses: the row
// must stay an upload to settle, so the next attempt reconciles the
// annotation index with the bytes now stored instead of just marking it done.
func TestRecordedAtFileWorkerLateLandingEarlierPutIsStillSettled(t *testing.T) {
	f := newFileWorkerFixture(t, 0)
	ctx := context.Background()
	if _, err := f.w.rt.meetingMetadata.db.Exec(`UPDATE recorded_at_file_backfill SET state='uploading'`); err != nil {
		t.Fatal(err)
	}
	landed := false
	f.w.client.Transport = annotationRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPut || landed {
			return http.DefaultTransport.RoundTrip(r)
		}
		landed = true
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		// The earlier PUT carried the same bytes; it lands now.
		f.nc.fakeNCFiles.mu.Lock()
		f.nc.files[annTestRecording] = body
		f.nc.etags[annTestRecording]++
		f.nc.fakeNCFiles.mu.Unlock()
		return &http.Response{StatusCode: http.StatusPreconditionFailed, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}, Request: r}, nil
	})
	f.w.drain(ctx)
	r := f.row(t)
	if r.state != recordedAtStateUploading || r.attempts != 1 {
		t.Fatalf("after a refused re-send of an unsettled upload, row = %+v, want still uploading", r)
	}
	f.clock = time.Unix(r.retryAt, 0).Add(time.Second)
	f.w.drain(ctx)
	if r := f.row(t); r.state != recordedAtStateDone {
		t.Fatalf("settle = %+v; logs: %s", r, f.logs)
	}
	if f.remote(t)["recordedAtUtc"] != testRecordedAt {
		t.Fatalf("remote = %v", f.remote(t))
	}
	if want := fileSHA256Of(t, f.nc.recording(annTestRecording)); f.indexedContainer(t) != want {
		t.Fatalf("index container = %q, want the stored file's %q", f.indexedContainer(t), want)
	}
}

// A file that already carries the value while the annotation index still
// names an older container gets the index reconciled, whatever route the row
// took to pending: here an unsettled upload (which had landed) was blocked by
// a CLI that could not run the retag, then requeued once the CLI was fixed.
func TestRecordedAtFileWorkerSettlesALandedUploadAfterABlock(t *testing.T) {
	f := newFileWorkerFixture(t, 2)
	ctx := context.Background()
	original := f.indexedContainer(t)
	var r map[string]any
	if err := json.Unmarshal([]byte(f.nc.recording(annTestRecording)), &r); err != nil {
		t.Fatal(err)
	}
	r["recordedAtUtc"] = testRecordedAt
	ours, _ := json.Marshal(r)
	f.nc.seed(annTestRecording, string(ours), nil)
	if _, err := f.w.rt.meetingMetadata.db.Exec(`UPDATE recorded_at_file_backfill SET state='uploading'`); err != nil {
		t.Fatal(err)
	}
	f.w.drain(ctx)
	if row := f.row(t); row.state != recordedAtStateBlocked {
		t.Fatalf("row = %+v, want blocked", row)
	}
	f.w.bin = retagCLI(t, 0)
	if _, err := f.w.requeueBlocked(ctx); err != nil {
		t.Fatal(err)
	}
	f.w.drain(ctx)
	if row := f.row(t); row.state != recordedAtStateDone {
		t.Fatalf("row = %+v; logs: %s", row, f.logs)
	}
	want := fileSHA256Of(t, string(ours))
	if want == original {
		t.Fatal("fixture: the index must start behind the file")
	}
	if got := f.indexedContainer(t); got != want {
		t.Fatalf("index container = %q, want the stored file's %q", got, want)
	}
	if len(f.nc.ifMatches()) != 0 {
		t.Fatalf("a file already carrying the value was uploaded: %v", f.nc.ifMatches())
	}
}

// A file whose marks the index recorded as unreadable is not "behind": the
// worker must not run `annotate show` on it (which refused it already) and
// must finish the row instead of retrying it forever.
func TestRecordedAtFileWorkerLeavesAnUnreadableIndexAlone(t *testing.T) {
	f := newFileWorkerFixture(t, 0)
	ctx := context.Background()
	var r map[string]any
	if err := json.Unmarshal([]byte(f.nc.recording(annTestRecording)), &r); err != nil {
		t.Fatal(err)
	}
	r["recordedAtUtc"] = testRecordedAt
	ours, _ := json.Marshal(r)
	f.nc.seed(annTestRecording, string(ours), nil)
	if err := f.store.MarkUnavailable(ctx, path.Base(annTestRecording), ""); err != nil {
		t.Fatal(err)
	}
	f.w.drain(ctx)
	if row := f.row(t); row.state != recordedAtStateDone {
		t.Fatalf("row = %+v, want done; logs: %s", row, f.logs)
	}
	if n := cliRuns(t, f.w.bin, "annotate show"); n != 0 {
		t.Fatalf("annotate show ran %d times on a file the index holds as unreadable", n)
	}
}

// refusingShowCLI is retagCLI whose `annotate show` refuses the file, as the
// real CLI does for unreadable marks.
func refusingShowCLI(t *testing.T) string {
	t.Helper()
	inner := retagCLI(t, 0)
	name := filepath.Join(t.TempDir(), "cassini")
	script := "#!/bin/sh\nif [ \"$1\" = annotate ] && [ \"$2\" = show ]; then echo 'unreadable annotations' >&2; exit 1; fi\nexec " + inner + " \"$@\"\n"
	if err := os.WriteFile(name, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return name
}

// A file whose marks the CLI refuses still finishes: rewritten once (or
// settled), recorded as unreadable in the index, never retried forever.
func TestRecordedAtFileWorkerFinishesAFileWithUnreadableMarks(t *testing.T) {
	t.Run("rewrite", func(t *testing.T) {
		f := newFileWorkerFixture(t, 0)
		f.w.bin = refusingShowCLI(t)
		f.w.drain(context.Background())
		if row := f.row(t); row.state != recordedAtStateDone {
			t.Fatalf("row = %+v, want done; logs: %s", row, f.logs)
		}
		if n := len(f.nc.ifMatches()); n != 1 {
			t.Fatalf("PUTs = %d, want 1", n)
		}
		f.requireUnavailable(t)
	})
	t.Run("settle", func(t *testing.T) {
		f := newFileWorkerFixture(t, 0)
		var r map[string]any
		if err := json.Unmarshal([]byte(f.nc.recording(annTestRecording)), &r); err != nil {
			t.Fatal(err)
		}
		r["recordedAtUtc"] = testRecordedAt
		ours, _ := json.Marshal(r)
		f.nc.seed(annTestRecording, string(ours), nil)
		if _, err := f.w.rt.meetingMetadata.db.Exec(`UPDATE recorded_at_file_backfill SET state='uploading'`); err != nil {
			t.Fatal(err)
		}
		f.w.bin = refusingShowCLI(t)
		f.w.drain(context.Background())
		if row := f.row(t); row.state != recordedAtStateDone {
			t.Fatalf("row = %+v, want done; logs: %s", row, f.logs)
		}
		if n := len(f.nc.ifMatches()); n != 0 {
			t.Fatalf("PUTs = %d, want 0", n)
		}
		f.requireUnavailable(t)
	})
}

func (f *fileWorkerFixture) requireUnavailable(t *testing.T) {
	t.Helper()
	var state string
	if err := f.store.db.QueryRow(`SELECT state FROM meeting_annotations WHERE opus_name=?`, path.Base(annTestRecording)).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != annotationsStateUnavailable {
		t.Fatalf("index state = %q, want %q", state, annotationsStateUnavailable)
	}
}
