package operator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
)

// backfillFixture stands up a runtime with a job store, a promoted bundle in
// current/, and a search index — the three things backfill reads.
type backfillFixture struct {
	rt       *Runtime
	workRoot string
	store    *Store
}

func newBackfillFixture(t *testing.T) *backfillFixture {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	index, err := openSearchStore(filepath.Join(dir, searchStoreFilename), nil)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	t.Cleanup(func() { _ = index.Close() })

	workRoot := filepath.Join(dir, "work")
	return &backfillFixture{
		rt: &Runtime{
			cfg:         Config{WorkRoot: workRoot},
			logger:      log.New(io.Discard, "", 0),
			store:       store,
			searchStore: index,
		},
		workRoot: workRoot,
		store:    store,
	}
}

// publishedJob writes a job whose delivered artifact has the digest of the
// bytes given, plus the promoted bundle in current/.
func (f *backfillFixture) publishedJob(t *testing.T, jobID, opusBytes, transcript string) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.store.db.ExecContext(ctx,
		`INSERT INTO jobs (id, provider, request_json, stage, state, created_at, updated_at, artifact_opus_sha256)
		 VALUES (?, 'test', '{}', 'publish', 'succeeded', ?, ?, ?)`,
		jobID, nowUTCString(), nowUTCString(), digestOf(opusBytes)); err != nil {
		t.Fatalf("insert job: %v", err)
	}
	f.writeCurrent(t, jobID, opusBytes, transcript)
}

// writeCurrent lays down current/<job>.opus and current/<job>.meeting.
func (f *backfillFixture) writeCurrent(t *testing.T, jobID, opusBytes, transcript string) {
	t.Helper()
	if err := os.MkdirAll(currentRoot(f.workRoot), 0o755); err != nil {
		t.Fatalf("mkdir current: %v", err)
	}
	if opusBytes != "" {
		if err := os.WriteFile(canonicalOpusPath(f.workRoot, jobID), []byte(opusBytes), 0o644); err != nil {
			t.Fatalf("write opus: %v", err)
		}
	}
	if transcript != "" {
		bundle := canonicalMeetingPath(f.workRoot, jobID)
		if err := os.MkdirAll(bundle, 0o755); err != nil {
			t.Fatalf("mkdir bundle: %v", err)
		}
		if err := os.WriteFile(filepath.Join(bundle, "transcript.words.v1.json"), []byte(transcript), 0o644); err != nil {
			t.Fatalf("write transcript: %v", err)
		}
	}
}

func digestOf(content string) string {
	sum := sha256.New()
	_, _ = io.WriteString(sum, content)
	return hex.EncodeToString(sum.Sum(nil))
}

func reasonFor(t *testing.T, index *searchStore, opusName string) string {
	t.Helper()
	var reason string
	if err := index.db.QueryRow(
		`SELECT reason FROM meeting_index WHERE opus_name = ?`, opusName).Scan(&reason); err != nil {
		t.Fatalf("read reason for %s: %v", opusName, err)
	}
	return reason
}

func TestBackfillIndexesAPublishedMeeting(t *testing.T) {
	f := newBackfillFixture(t)
	f.publishedJob(t, "JOB1", "sealed-audio-bytes", ingestTranscript)

	report, err := f.rt.backfillSearchIndex(context.Background(),
		[]searchBackfillTarget{{JobID: "JOB1", OpusName: "JOB1.opus"}})
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if report.Indexed != 1 || report.Failed != 0 || report.Unavailable != 0 {
		t.Fatalf("report = %+v, want indexed=1", report)
	}
	hits := matches(t, f.rt.searchStore, "acquisition")
	if len(hits) != 1 || hits[0].SegmentID != "seg_0001" {
		t.Fatalf("hits = %+v, want the producer's segment", hits)
	}
}

// THE check. current/ tracks the last attempt that BUILT; a rerun that built
// and then failed to publish leaves a transcript there that does not match the
// delivered .opus. Indexing it would have search cite words nobody can play.
func TestBackfillRefusesABundleNewerThanWhatWasDelivered(t *testing.T) {
	f := newBackfillFixture(t)
	f.publishedJob(t, "JOB1", "delivered-audio", ingestTranscript)
	// A later build promoted a different artifact and its transcript.
	f.writeCurrent(t, "JOB1", "rebuilt-audio-never-published", `{"version":"transcript.words.v1","segments":[
	  {"id":"seg_9999","speaker":"S1","startMs":1000,"endMs":2000,"text":"undelivered rewording","words":[]}]}`)

	report, err := f.rt.backfillSearchIndex(context.Background(),
		[]searchBackfillTarget{{JobID: "JOB1", OpusName: "JOB1.opus"}})
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if report.Unavailable != 1 || report.Indexed != 0 {
		t.Fatalf("report = %+v, want unavailable=1", report)
	}
	if got := reasonFor(t, f.rt.searchStore, "JOB1.opus"); got != searchBackfillReasonStaleBundle {
		t.Errorf("reason = %q, want %q", got, searchBackfillReasonStaleBundle)
	}
	if got := matches(t, f.rt.searchStore, "undelivered"); len(got) != 0 {
		t.Errorf("indexed a transcript that was never delivered: %+v", got)
	}
}

// A meeting whose bundle is gone is recorded, not silently skipped: absent from
// the covered count is a partial answer, absent from the index entirely with
// coverage still counting it is a false one.
func TestBackfillRecordsAMissingBundle(t *testing.T) {
	f := newBackfillFixture(t)
	if _, err := f.store.db.ExecContext(context.Background(),
		`INSERT INTO jobs (id, provider, request_json, stage, state, created_at, updated_at, artifact_opus_sha256)
		 VALUES ('JOB1','test','{}','publish','succeeded',?,?,?)`,
		nowUTCString(), nowUTCString(), digestOf("gone")); err != nil {
		t.Fatalf("insert job: %v", err)
	}

	report, err := f.rt.backfillSearchIndex(context.Background(),
		[]searchBackfillTarget{{JobID: "JOB1", OpusName: "JOB1.opus"}})
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if report.Unavailable != 1 {
		t.Fatalf("report = %+v, want unavailable=1", report)
	}
	if got := reasonFor(t, f.rt.searchStore, "JOB1.opus"); got != searchBackfillReasonNoBundle {
		t.Errorf("reason = %q, want %q", got, searchBackfillReasonNoBundle)
	}
}

// Without the delivered digest there is no way to tell whether the local bundle
// is the published one, and guessing is the failure the check exists to stop.
func TestBackfillRefusesWhenTheDeliveredDigestIsUnknown(t *testing.T) {
	f := newBackfillFixture(t)
	if _, err := f.store.db.ExecContext(context.Background(),
		`INSERT INTO jobs (id, provider, request_json, stage, state, created_at, updated_at)
		 VALUES ('JOB1','test','{}','publish','succeeded',?,?)`,
		nowUTCString(), nowUTCString()); err != nil {
		t.Fatalf("insert job: %v", err)
	}
	f.writeCurrent(t, "JOB1", "some-audio", ingestTranscript)

	report, err := f.rt.backfillSearchIndex(context.Background(),
		[]searchBackfillTarget{{JobID: "JOB1", OpusName: "JOB1.opus"}})
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if report.Unavailable != 1 || report.Indexed != 0 {
		t.Fatalf("report = %+v, want unavailable=1", report)
	}
	if got := reasonFor(t, f.rt.searchStore, "JOB1.opus"); got != searchBackfillReasonNoDigest {
		t.Errorf("reason = %q, want %q", got, searchBackfillReasonNoDigest)
	}
}

// A backfill is expected to be re-runnable: a meeting already indexed from the
// same delivered artifact is left alone rather than re-read and rewritten.
func TestBackfillSkipsWhatIsAlreadyCurrent(t *testing.T) {
	f := newBackfillFixture(t)
	f.publishedJob(t, "JOB1", "sealed-audio-bytes", ingestTranscript)
	targets := []searchBackfillTarget{{JobID: "JOB1", OpusName: "JOB1.opus"}}

	if _, err := f.rt.backfillSearchIndex(context.Background(), targets); err != nil {
		t.Fatalf("first run: %v", err)
	}
	report, err := f.rt.backfillSearchIndex(context.Background(), targets)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if report.Unchanged != 1 || report.Indexed != 0 {
		t.Fatalf("report = %+v, want unchanged=1 on a re-run", report)
	}
}

// A re-publish with a different artifact re-indexes rather than being skipped.
func TestBackfillReindexesWhenTheDeliveredArtifactChanged(t *testing.T) {
	f := newBackfillFixture(t)
	f.publishedJob(t, "JOB1", "first-audio", ingestTranscript)
	targets := []searchBackfillTarget{{JobID: "JOB1", OpusName: "JOB1.opus"}}
	if _, err := f.rt.backfillSearchIndex(context.Background(), targets); err != nil {
		t.Fatalf("first run: %v", err)
	}

	corrected := `{"version":"transcript.words.v1","segments":[
	  {"id":"seg_0001","speaker":"S1","startMs":1000,"endMs":4200,"text":"we discussed the merger","words":[]}]}`
	f.writeCurrent(t, "JOB1", "second-audio", corrected)
	if _, err := f.store.db.ExecContext(context.Background(),
		`UPDATE jobs SET artifact_opus_sha256 = ? WHERE id = 'JOB1'`, digestOf("second-audio")); err != nil {
		t.Fatalf("update digest: %v", err)
	}

	report, err := f.rt.backfillSearchIndex(context.Background(), targets)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if report.Indexed != 1 {
		t.Fatalf("report = %+v, want indexed=1", report)
	}
	if got := matches(t, f.rt.searchStore, "acquisition"); len(got) != 0 {
		t.Errorf("the superseded transcript is still searchable: %+v", got)
	}
	if got := matches(t, f.rt.searchStore, "merger"); len(got) != 1 {
		t.Errorf("the current transcript is not searchable: %+v", got)
	}
}

// One bad meeting must not stop the rest of an archive being indexed.
func TestBackfillContinuesPastOneFailure(t *testing.T) {
	f := newBackfillFixture(t)
	f.publishedJob(t, "JOB1", "audio-one", ingestTranscript)
	f.publishedJob(t, "JOB2", "audio-two", "{not json")
	f.publishedJob(t, "JOB3", "audio-three", ingestTranscript)

	report, err := f.rt.backfillSearchIndex(context.Background(), []searchBackfillTarget{
		{JobID: "JOB1", OpusName: "JOB1.opus"},
		{JobID: "JOB2", OpusName: "JOB2.opus"},
		{JobID: "JOB3", OpusName: "JOB3.opus"},
	})
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if report.Indexed != 2 || report.Unavailable != 1 {
		t.Fatalf("report = %+v, want indexed=2 unavailable=1", report)
	}
	if got := reasonFor(t, f.rt.searchStore, "JOB2.opus"); got != searchBackfillReasonUnreadable {
		t.Errorf("reason = %q, want %q", got, searchBackfillReasonUnreadable)
	}
}

// A target with no join key cannot have a row or a failure recorded against it,
// so it is counted as failed rather than silently dropped.
func TestBackfillCountsATargetWithNoJoinKeyAsFailed(t *testing.T) {
	f := newBackfillFixture(t)
	report, err := f.rt.backfillSearchIndex(context.Background(),
		[]searchBackfillTarget{{JobID: "JOB1", OpusName: "  "}})
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if report.Failed != 1 {
		t.Fatalf("report = %+v, want failed=1", report)
	}
}
