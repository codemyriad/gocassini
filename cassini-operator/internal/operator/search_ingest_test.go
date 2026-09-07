package operator

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
)

// writeAttemptArtifacts lays out the two directories ingest reads: the exported
// site (which names the delivered .opus) and the attempt bundle (which holds
// the transcript).
func writeAttemptArtifacts(t *testing.T, workRoot, jobID string, attempt int, catalog, transcript string) string {
	t.Helper()
	siteDir := attemptSitePath(workRoot, jobID, attempt)
	if err := os.MkdirAll(siteDir, 0o755); err != nil {
		t.Fatalf("mkdir site: %v", err)
	}
	if catalog != "" {
		if err := os.WriteFile(filepath.Join(siteDir, "catalog.json"), []byte(catalog), 0o644); err != nil {
			t.Fatalf("write catalog: %v", err)
		}
	}
	bundleDir := attemptMeetingPath(workRoot, jobID, attempt)
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	if transcript != "" {
		if err := os.WriteFile(filepath.Join(bundleDir, "transcript.words.v1.json"), []byte(transcript), 0o644); err != nil {
			t.Fatalf("write transcript: %v", err)
		}
	}
	return siteDir
}

func ingestRuntime(t *testing.T) (*Runtime, string) {
	t.Helper()
	workRoot := t.TempDir()
	store, err := openSearchStore(filepath.Join(t.TempDir(), searchStoreFilename), nil)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	rt := &Runtime{
		cfg:         Config{WorkRoot: workRoot},
		logger:      log.New(io.Discard, "", 0),
		searchStore: store,
	}
	return rt, workRoot
}

const ingestCatalog = `{"version":"cassini.viewer.catalog.v1","meetings":[
  {"id":"JOB1","title":"Standup","dateLabel":"2026-09-01 09:00","audioPath":"./meetings/JOB1.opus"}]}`

const ingestTranscript = `{"version":"transcript.words.v1","segments":[
  {"id":"seg_0001","speaker":"S1","startMs":1000,"endMs":4200,"text":"we discussed the acquisition","words":[
    {"startMs":1000,"endMs":1200,"text":"we"}]},
  {"id":"seg_0002","speaker":"S2","startMs":9000,"endMs":11000,"text":"and then the roadmap","words":[
    {"startMs":9000,"endMs":9300,"text":"and"}]}]}`

func TestIngestIndexesADeliveredMeeting(t *testing.T) {
	rt, workRoot := ingestRuntime(t)
	siteDir := writeAttemptArtifacts(t, workRoot, "JOB1", 1, ingestCatalog, ingestTranscript)

	task := publishTask{JobID: "JOB1", AttemptNumber: 1, OpusSHA256: "sha-1"}
	if err := rt.indexPublishedMeeting(context.Background(), task, siteDir); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	hits := matches(t, rt.searchStore, "acquisition")
	if len(hits) != 1 {
		t.Fatalf("hits = %+v, want 1", hits)
	}
	// The join key is the delivered .opus basename, which is what the per-caller
	// visibility scan returns.
	if hits[0].Text != "JOB1.opus" {
		t.Errorf("join key = %q, want JOB1.opus", hits[0].Text)
	}
	if hits[0].SegmentID != "seg_0001" || hits[0].SpeakerID != "S1" {
		t.Errorf("hit = %+v, want the producer's own segment and speaker", hits[0])
	}
	if hits[0].StartMS != 1_000 || hits[0].EndMS != 4_200 {
		t.Errorf("bounds = [%d,%d], want the segment's own", hits[0].StartMS, hits[0].EndMS)
	}
	coverage, err := rt.searchStore.Coverage(context.Background())
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	if coverage.Indexed != 1 || coverage.Unavailable != 0 {
		t.Errorf("coverage = %+v, want indexed=1", coverage)
	}
}

// THE trap. `current/` tracks the last attempt that BUILT; the attempt bundle
// tracks the one that was DELIVERED. Ingest must read the attempt, or a rerun
// whose build succeeded and whose publish failed would have search cite words
// that are not in the recording anyone can play.
func TestIngestReadsTheAttemptBundleNotCurrent(t *testing.T) {
	rt, workRoot := ingestRuntime(t)
	siteDir := writeAttemptArtifacts(t, workRoot, "JOB1", 1, ingestCatalog, ingestTranscript)

	// A newer, undelivered transcript sitting in current/, as a later build
	// would leave it.
	currentDir := filepath.Join(workRoot, "current", "JOB1.meeting")
	if err := os.MkdirAll(currentDir, 0o755); err != nil {
		t.Fatalf("mkdir current: %v", err)
	}
	newer := `{"version":"transcript.words.v1","segments":[
	  {"id":"seg_9999","speaker":"S1","startMs":1000,"endMs":2000,"text":"undelivered rewording","words":[]}]}`
	if err := os.WriteFile(filepath.Join(currentDir, "transcript.words.v1.json"), []byte(newer), 0o644); err != nil {
		t.Fatalf("write current transcript: %v", err)
	}

	task := publishTask{JobID: "JOB1", AttemptNumber: 1, OpusSHA256: "sha-1"}
	if err := rt.indexPublishedMeeting(context.Background(), task, siteDir); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	if got := matches(t, rt.searchStore, "undelivered"); len(got) != 0 {
		t.Fatalf("indexed a transcript that was never delivered: %+v", got)
	}
	if got := matches(t, rt.searchStore, "acquisition"); len(got) != 1 {
		t.Fatalf("did not index the delivered transcript: %+v", got)
	}
}

// The join key comes from the catalog entry, never composed from the job id —
// those coincide by convention only, and a composed name that drifts matches
// nothing and fails silently.
func TestIngestTakesTheJoinKeyFromTheCatalogEntry(t *testing.T) {
	rt, workRoot := ingestRuntime(t)
	catalog := `{"version":"cassini.viewer.catalog.v1","meetings":[
	  {"id":"JOB1","audioPath":"./meetings/JOB1--attempt-002.opus"}]}`
	siteDir := writeAttemptArtifacts(t, workRoot, "JOB1", 2, catalog, ingestTranscript)

	task := publishTask{JobID: "JOB1", AttemptNumber: 2}
	if err := rt.indexPublishedMeeting(context.Background(), task, siteDir); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	hits := matches(t, rt.searchStore, "acquisition")
	if len(hits) != 1 || hits[0].Text != "JOB1--attempt-002.opus" {
		t.Fatalf("join key = %+v, want the catalog's own audioPath basename", hits)
	}
}

// A failed ingest must drop the meeting OUT of coverage, so an answer degrades
// to partial rather than reporting a confident "no match" over a transcript
// that was never indexed.
func TestIngestRecordsAMissingTranscriptAsUnavailable(t *testing.T) {
	rt, workRoot := ingestRuntime(t)
	siteDir := writeAttemptArtifacts(t, workRoot, "JOB1", 1, ingestCatalog, "")

	task := publishTask{JobID: "JOB1", AttemptNumber: 1}
	if err := rt.indexPublishedMeeting(context.Background(), task, siteDir); err == nil {
		t.Fatal("expected an error for a missing transcript")
	}

	coverage, err := rt.searchStore.Coverage(context.Background())
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	if coverage.Indexed != 0 || coverage.Unavailable != 1 {
		t.Fatalf("coverage = %+v, want unavailable=1", coverage)
	}
	var reason string
	if err := rt.searchStore.db.QueryRow(
		`SELECT reason FROM meeting_index WHERE opus_name = 'JOB1.opus'`).Scan(&reason); err != nil {
		t.Fatalf("read reason: %v", err)
	}
	if reason != searchIngestReasonNoTranscript {
		t.Errorf("reason = %q, want %q", reason, searchIngestReasonNoTranscript)
	}
}

// Re-indexing a rerun replaces the previous attempt's rows rather than leaving
// both attempts searchable.
func TestIngestReplacesAPreviousAttempt(t *testing.T) {
	rt, workRoot := ingestRuntime(t)
	siteDir := writeAttemptArtifacts(t, workRoot, "JOB1", 1, ingestCatalog, ingestTranscript)
	if err := rt.indexPublishedMeeting(context.Background(),
		publishTask{JobID: "JOB1", AttemptNumber: 1}, siteDir); err != nil {
		t.Fatalf("first ingest: %v", err)
	}

	corrected := `{"version":"transcript.words.v1","segments":[
	  {"id":"seg_0001","speaker":"S1","startMs":1000,"endMs":4200,"text":"we discussed the merger","words":[]}]}`
	siteDir2 := writeAttemptArtifacts(t, workRoot, "JOB1", 2, ingestCatalog, corrected)
	if err := rt.indexPublishedMeeting(context.Background(),
		publishTask{JobID: "JOB1", AttemptNumber: 2}, siteDir2); err != nil {
		t.Fatalf("second ingest: %v", err)
	}

	if got := matches(t, rt.searchStore, "acquisition"); len(got) != 0 {
		t.Errorf("the superseded attempt is still searchable: %+v", got)
	}
	if got := matches(t, rt.searchStore, "merger"); len(got) != 1 {
		t.Errorf("the current attempt is not searchable: %+v", got)
	}
}

// A legacy directory-shaped entry has no basename any visibility scan can
// return, so indexing it would make it permanently unreachable. Refused, and
// nothing is written — there is no key to record a failure against.
func TestIngestRefusesAnEntryWithNoAudioPath(t *testing.T) {
	rt, workRoot := ingestRuntime(t)
	catalog := `{"version":"cassini.viewer.catalog.v1","meetings":[
	  {"id":"JOB1","artifactPath":"./meetings/JOB1"}]}`
	siteDir := writeAttemptArtifacts(t, workRoot, "JOB1", 1, catalog, ingestTranscript)

	if err := rt.indexPublishedMeeting(context.Background(),
		publishTask{JobID: "JOB1", AttemptNumber: 1}, siteDir); err == nil {
		t.Fatal("expected an error for a directory-shaped entry")
	}
	coverage, err := rt.searchStore.Coverage(context.Background())
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	if coverage.Indexed != 0 || coverage.Unavailable != 0 {
		t.Fatalf("coverage = %+v, want nothing recorded", coverage)
	}
}

// An operator with no index still publishes.
func TestIngestIsANoOpWithoutAnIndex(t *testing.T) {
	rt, workRoot := ingestRuntime(t)
	rt.searchStore = nil
	siteDir := writeAttemptArtifacts(t, workRoot, "JOB1", 1, ingestCatalog, ingestTranscript)

	if err := rt.indexPublishedMeeting(context.Background(),
		publishTask{JobID: "JOB1", AttemptNumber: 1}, siteDir); err != nil {
		t.Fatalf("ingest without an index should be a no-op, got %v", err)
	}
}

// A segment whose own bounds are unusable cites its words instead of collapsing
// to a zero-length reference.
func TestIngestWidensASegmentFromItsWords(t *testing.T) {
	rt, workRoot := ingestRuntime(t)
	transcript := `{"version":"transcript.words.v1","segments":[
	  {"id":"seg_0001","speaker":"S1","startMs":0,"endMs":0,"text":"quarterly numbers","words":[
	    {"startMs":5000,"endMs":5400,"text":"quarterly"},
	    {"startMs":5400,"endMs":6100,"text":"numbers"}]}]}`
	siteDir := writeAttemptArtifacts(t, workRoot, "JOB1", 1, ingestCatalog, transcript)

	if err := rt.indexPublishedMeeting(context.Background(),
		publishTask{JobID: "JOB1", AttemptNumber: 1}, siteDir); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	hits := matches(t, rt.searchStore, "quarterly")
	if len(hits) != 1 {
		t.Fatalf("hits = %+v, want 1", hits)
	}
	if hits[0].StartMS != 5_000 || hits[0].EndMS != 6_100 {
		t.Errorf("bounds = [%d,%d], want the words' span [5000,6100]", hits[0].StartMS, hits[0].EndMS)
	}
}

func TestIngestEncodesTheJSONShapeItReads(t *testing.T) {
	// Guards the decode against a silent shape drift: if the bundle's field
	// names change, this fails here rather than producing an empty index.
	var decoded searchIngestBundleTranscript
	if err := json.Unmarshal([]byte(ingestTranscript), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded.Segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(decoded.Segments))
	}
	if decoded.Segments[0].ID != "seg_0001" || decoded.Segments[0].Speaker != "S1" {
		t.Errorf("segment = %+v, want id and speaker decoded", decoded.Segments[0])
	}
	if decoded.Segments[0].StartMS != 1000 || decoded.Segments[0].EndMS != 4200 {
		t.Errorf("bounds not decoded: %+v", decoded.Segments[0])
	}
}
