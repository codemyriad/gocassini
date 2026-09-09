package operator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
		[]searchBackfillTarget{{JobID: "JOB1", OpusName: "JOB1.opus"}}, nil)
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
		[]searchBackfillTarget{{JobID: "JOB1", OpusName: "JOB1.opus"}}, nil)
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
		[]searchBackfillTarget{{JobID: "JOB1", OpusName: "JOB1.opus"}}, nil)
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
		[]searchBackfillTarget{{JobID: "JOB1", OpusName: "JOB1.opus"}}, nil)
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

	if _, err := f.rt.backfillSearchIndex(context.Background(), targets, nil); err != nil {
		t.Fatalf("first run: %v", err)
	}
	report, err := f.rt.backfillSearchIndex(context.Background(), targets, nil)
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
	if _, err := f.rt.backfillSearchIndex(context.Background(), targets, nil); err != nil {
		t.Fatalf("first run: %v", err)
	}

	corrected := `{"version":"transcript.words.v1","segments":[
	  {"id":"seg_0001","speaker":"S1","startMs":1000,"endMs":4200,"text":"we discussed the merger","words":[]}]}`
	f.writeCurrent(t, "JOB1", "second-audio", corrected)
	if _, err := f.store.db.ExecContext(context.Background(),
		`UPDATE jobs SET artifact_opus_sha256 = ? WHERE id = 'JOB1'`, digestOf("second-audio")); err != nil {
		t.Fatalf("update digest: %v", err)
	}

	report, err := f.rt.backfillSearchIndex(context.Background(), targets, nil)
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

	report, err := f.rt.backfillSearchIndex(context.Background(),
		[]searchBackfillTarget{
			{JobID: "JOB1", OpusName: "JOB1.opus"},
			{JobID: "JOB2", OpusName: "JOB2.opus"},
			{JobID: "JOB3", OpusName: "JOB3.opus"},
		}, nil)
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
		[]searchBackfillTarget{{JobID: "JOB1", OpusName: "  "}}, nil)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if report.Failed != 1 {
		t.Fatalf("report = %+v, want failed=1", report)
	}
}

// stubArchive stands in for reading a published recording out of Nextcloud.
func stubArchive(words []searchTranscriptWord, digest string, err error) (searchArchiveReader, *int) {
	calls := 0
	return func(context.Context, string) ([]searchTranscriptWord, string, error) {
		calls++
		return words, digest, err
	}, &calls
}

var archiveWords = []searchTranscriptWord{
	{SpeakerID: "S1", StartMS: 1_000, EndMS: 1_200, Text: "we"},
	{SpeakerID: "S1", StartMS: 1_400, EndMS: 1_700, Text: "discussed"},
	{SpeakerID: "S1", StartMS: 1_900, EndMS: 2_100, Text: "acquisition"},
}

// THE case this exists for. On the demo archive 25 of 30 meetings had no job
// row: the archive outlives the operator's volume. Those must be indexed from
// the recording rather than left unsearchable.
func TestBackfillFallsBackToTheArchiveWhenTheJobRecordIsGone(t *testing.T) {
	f := newBackfillFixture(t)
	// No job row and no bundle — only the published recording exists.
	archive, calls := stubArchive(archiveWords, "archive-digest", nil)

	report, err := f.rt.backfillSearchIndex(context.Background(),
		[]searchBackfillTarget{{JobID: "GONE", OpusName: "GONE.opus"}}, archive)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if report.Indexed != 1 || report.Unavailable != 0 || report.Failed != 0 {
		t.Fatalf("report = %+v, want indexed=1 from the archive", report)
	}
	if *calls != 1 {
		t.Errorf("archive read %d times, want 1", *calls)
	}
	if got := matches(t, f.rt.searchStore, "acquisition"); len(got) != 1 {
		t.Fatalf("hits = %+v, want the archive-indexed meeting", got)
	}
	// Coarser rows, and the index says so rather than implying otherwise.
	var source, digest string
	if err := f.rt.searchStore.db.QueryRow(
		`SELECT row_source, opus_sha256 FROM meeting_index WHERE opus_name = 'GONE.opus'`).Scan(&source, &digest); err != nil {
		t.Fatalf("read row_source: %v", err)
	}
	if source != searchRowSourceWords {
		t.Errorf("row_source = %q, want %q", source, searchRowSourceWords)
	}
	// The digest recorded is the archive bytes actually read, not a job record.
	if digest != "archive-digest" {
		t.Errorf("digest = %q, want the archive's", digest)
	}
}

// The local bundle still wins when it can be trusted: it carries the producer's
// own segments, and no download is needed.
func TestBackfillPrefersTheLocalBundleOverTheArchive(t *testing.T) {
	f := newBackfillFixture(t)
	f.publishedJob(t, "JOB1", "sealed-audio-bytes", ingestTranscript)
	archive, calls := stubArchive(archiveWords, "archive-digest", nil)

	report, err := f.rt.backfillSearchIndex(context.Background(),
		[]searchBackfillTarget{{JobID: "JOB1", OpusName: "JOB1.opus"}}, archive)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if report.Indexed != 1 {
		t.Fatalf("report = %+v, want indexed=1", report)
	}
	if *calls != 0 {
		t.Errorf("archive was read %d times when the local bundle was usable", *calls)
	}
	var source string
	if err := f.rt.searchStore.db.QueryRow(
		`SELECT row_source FROM meeting_index WHERE opus_name = 'JOB1.opus'`).Scan(&source); err != nil {
		t.Fatalf("read row_source: %v", err)
	}
	if source != searchRowSourceSegments {
		t.Errorf("row_source = %q, want the producer's segments", source)
	}
}

// A bundle that is not the delivered artifact used to be a dead end. The
// archive holds what was actually delivered, so falling through RESOLVES the
// mismatch rather than working around it.
func TestBackfillResolvesAStaleBundleFromTheArchive(t *testing.T) {
	f := newBackfillFixture(t)
	f.publishedJob(t, "JOB1", "delivered-audio", ingestTranscript)
	f.writeCurrent(t, "JOB1", "rebuilt-audio-never-published", `{"version":"transcript.words.v1","segments":[
	  {"id":"seg_9999","speaker":"S1","startMs":1000,"endMs":2000,"text":"undelivered rewording","words":[]}]}`)
	archive, _ := stubArchive(archiveWords, "delivered-digest", nil)

	report, err := f.rt.backfillSearchIndex(context.Background(),
		[]searchBackfillTarget{{JobID: "JOB1", OpusName: "JOB1.opus"}}, archive)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if report.Indexed != 1 {
		t.Fatalf("report = %+v, want the archive to resolve it", report)
	}
	if got := matches(t, f.rt.searchStore, "undelivered"); len(got) != 0 {
		t.Error("the undelivered rewording was indexed")
	}
	if got := matches(t, f.rt.searchStore, "acquisition"); len(got) != 1 {
		t.Error("the delivered recording was not indexed")
	}
}

// With no archive access the specific local reason must survive — recording
// "bundle-newer-than-delivered" for a bundle that is simply absent is the kind
// of plausible-but-wrong reason an operator would chase.
func TestBackfillKeepsTheSpecificReasonWithoutAnArchive(t *testing.T) {
	f := newBackfillFixture(t)
	if _, err := f.store.db.ExecContext(context.Background(),
		`INSERT INTO jobs (id, provider, request_json, stage, state, created_at, updated_at, artifact_opus_sha256)
		 VALUES ('JOB1','test','{}','publish','succeeded',?,?,?)`,
		nowUTCString(), nowUTCString(), digestOf("gone")); err != nil {
		t.Fatalf("insert job: %v", err)
	}

	if _, err := f.rt.backfillSearchIndex(context.Background(),
		[]searchBackfillTarget{{JobID: "JOB1", OpusName: "JOB1.opus"}}, nil); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if got := reasonFor(t, f.rt.searchStore, "JOB1.opus"); got != searchBackfillReasonNoBundle {
		t.Errorf("reason = %q, want %q", got, searchBackfillReasonNoBundle)
	}
}

// A failed archive READ is a failure to verify, not a verdict on the meeting:
// it is counted failed and writes nothing, so re-running is always the fix.
func TestBackfillCountsAnUnreadableArchiveRecordingAsFailed(t *testing.T) {
	f := newBackfillFixture(t)
	archive, _ := stubArchive(nil, "", errors.New("504 from Nextcloud"))

	report, err := f.rt.backfillSearchIndex(context.Background(),
		[]searchBackfillTarget{{JobID: "GONE", OpusName: "GONE.opus"}}, archive)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if report.Failed != 1 || report.Unavailable != 0 {
		t.Fatalf("report = %+v, want failed=1", report)
	}
	var rows int
	if err := f.rt.searchStore.db.QueryRow(
		`SELECT COUNT(*) FROM meeting_index WHERE opus_name = 'GONE.opus'`).Scan(&rows); err != nil {
		t.Fatalf("count meeting_index: %v", err)
	}
	if rows != 0 {
		t.Errorf("a transient read failure was persisted as a verdict")
	}
}

// THE destructive case (review B2): a meeting that was searchable a minute ago
// must not lose its rows because one re-run hit one timeout. A read failure
// keeps the previous rows; only evidence about the content may replace them.
func TestBackfillKeepsRowsWhenTheArchiveReadFails(t *testing.T) {
	f := newBackfillFixture(t)
	targets := []searchBackfillTarget{{JobID: "GONE", OpusName: "GONE.opus"}}
	good, _ := stubArchive(archiveWords, "archive-digest", nil)
	if _, err := f.rt.backfillSearchIndex(context.Background(), targets, good); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if got := matches(t, f.rt.searchStore, "acquisition"); len(got) != 1 {
		t.Fatalf("seed run did not index: %+v", got)
	}

	failing, _ := stubArchive(nil, "", errors.New("timeout"))
	report, err := f.rt.backfillSearchIndex(context.Background(), targets, failing)
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if report.Failed != 1 || report.Unavailable != 0 {
		t.Fatalf("report = %+v, want failed=1 and nothing recorded", report)
	}
	if got := matches(t, f.rt.searchStore, "acquisition"); len(got) != 1 {
		t.Fatalf("hits = %+v — a transient failure deleted good rows", got)
	}
	if got := reasonFor(t, f.rt.searchStore, "GONE.opus"); got != "" {
		t.Errorf("reason = %q, want the indexed row left untouched", got)
	}
}

// The same protection without archive access: rows from an earlier run are not
// evidence of anything wrong, so they survive a run that cannot verify them.
func TestBackfillKeepsRowsWithoutArchiveAccess(t *testing.T) {
	f := newBackfillFixture(t)
	targets := []searchBackfillTarget{{JobID: "GONE", OpusName: "GONE.opus"}}
	good, _ := stubArchive(archiveWords, "archive-digest", nil)
	if _, err := f.rt.backfillSearchIndex(context.Background(), targets, good); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	report, err := f.rt.backfillSearchIndex(context.Background(), targets, nil)
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if report.Failed != 1 || report.Unavailable != 0 {
		t.Fatalf("report = %+v, want failed=1", report)
	}
	if got := matches(t, f.rt.searchStore, "acquisition"); len(got) != 1 {
		t.Fatalf("hits = %+v — verification absence deleted good rows", got)
	}
}

// Re-runnable against the archive too: the same recording is not downloaded and
// rewritten on every pass.
func TestBackfillSkipsAnArchiveMeetingAlreadyIndexed(t *testing.T) {
	f := newBackfillFixture(t)
	archive, calls := stubArchive(archiveWords, "archive-digest", nil)
	targets := []searchBackfillTarget{{JobID: "GONE", OpusName: "GONE.opus"}}

	if _, err := f.rt.backfillSearchIndex(context.Background(), targets, archive); err != nil {
		t.Fatalf("first run: %v", err)
	}
	report, err := f.rt.backfillSearchIndex(context.Background(), targets, archive)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if report.Unchanged != 1 || report.Indexed != 0 {
		t.Fatalf("report = %+v, want unchanged=1", report)
	}
	// It still has to read the recording to learn the digest — but it must not
	// rewrite rows it already holds.
	if *calls != 2 {
		t.Errorf("archive read %d times, want 2", *calls)
	}
}

// The "converge" half: nothing else prunes, so a deleted recording's rows would
// stay forever and its words stay readable to anyone who can run SQL on the file.
func TestBackfillForgetsMeetingsTheArchiveNoLongerHolds(t *testing.T) {
	f := newBackfillFixture(t)
	f.publishedJob(t, "JOB1", "audio-one", ingestTranscript)
	f.publishedJob(t, "JOB2", "audio-two", ingestTranscript)
	both := []searchBackfillTarget{
		{JobID: "JOB1", OpusName: "JOB1.opus"},
		{JobID: "JOB2", OpusName: "JOB2.opus"},
	}
	if _, err := f.rt.backfillSearchIndex(context.Background(), both, nil); err != nil {
		t.Fatalf("first run: %v", err)
	}

	report, err := f.rt.backfillSearchIndex(context.Background(), both[:1], nil)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if report.Forgotten != 1 {
		t.Fatalf("report = %+v, want forgotten=1", report)
	}
	for _, hit := range matches(t, f.rt.searchStore, "acquisition") {
		if hit.Text == "JOB2.opus" {
			t.Error("a deleted recording is still searchable")
		}
	}
}

// An archive read that came back empty is indistinguishable from an empty
// archive, so convergence must not erase the index on a transient failure.
func TestBackfillDoesNotForgetEverythingOnAnEmptyTargetList(t *testing.T) {
	f := newBackfillFixture(t)
	f.publishedJob(t, "JOB1", "audio-one", ingestTranscript)
	targets := []searchBackfillTarget{{JobID: "JOB1", OpusName: "JOB1.opus"}}
	if _, err := f.rt.backfillSearchIndex(context.Background(), targets, nil); err != nil {
		t.Fatalf("first run: %v", err)
	}
	report, err := f.rt.backfillSearchIndex(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("empty run: %v", err)
	}
	if report.Forgotten != 0 || len(matches(t, f.rt.searchStore, "acquisition")) != 1 {
		t.Fatalf("an empty target list pruned the index: %+v", report)
	}
}

// A locked job database must not look like a missing job row: that would send a
// backfill against a busy operator down the archive path for every meeting,
// overwriting good segment rows with coarse word rows.
func TestBackfillTreatsAnUnreadableJobStoreAsRetryable(t *testing.T) {
	f := newBackfillFixture(t)
	f.publishedJob(t, "JOB1", "audio-one", ingestTranscript)
	if err := f.store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	archive, calls := stubArchive(archiveWords, "archive-digest", nil)

	report, err := f.rt.backfillSearchIndex(context.Background(),
		[]searchBackfillTarget{{JobID: "JOB1", OpusName: "JOB1.opus"}}, archive)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if report.Failed != 1 || report.Indexed != 0 {
		t.Fatalf("report = %+v, want failed=1", report)
	}
	if *calls != 0 {
		t.Errorf("archive was read %d times for a job-store outage", *calls)
	}
}

// A meeting first indexed from the archive is upgraded to segment rows once its
// bundle is available: the digest matches either way, so the source must be
// compared too or it keeps the coarse rows forever.
func TestBackfillUpgradesArchiveRowsWhenTheBundleReturns(t *testing.T) {
	f := newBackfillFixture(t)
	f.publishedJob(t, "JOB1", "sealed-audio-bytes", ingestTranscript)
	targets := []searchBackfillTarget{{JobID: "JOB1", OpusName: "JOB1.opus"}}

	// Pretend an earlier run indexed it from the archive, at the same digest.
	if err := f.rt.searchStore.ReplaceMeeting(context.Background(), "JOB1.opus",
		digestOf("sealed-audio-bytes"), searchRowSourceWords,
		deriveSearchRowsFromWords(archiveWords)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	report, err := f.rt.backfillSearchIndex(context.Background(), targets, nil)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if report.Indexed != 1 {
		t.Fatalf("report = %+v, want the meeting upgraded, not skipped", report)
	}
	var source string
	if err := f.rt.searchStore.db.QueryRow(
		`SELECT row_source FROM meeting_index WHERE opus_name = 'JOB1.opus'`).Scan(&source); err != nil {
		t.Fatalf("read row_source: %v", err)
	}
	if source != searchRowSourceSegments {
		t.Errorf("row_source = %q, want the bundle's segments", source)
	}
}
