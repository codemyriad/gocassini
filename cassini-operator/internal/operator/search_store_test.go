package operator

import (
	"bytes"
	"context"
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestSearchStore(t *testing.T) *searchStore {
	t.Helper()
	store, err := openSearchStore(filepath.Join(t.TempDir(), searchStoreFilename), nil)
	if err != nil {
		t.Fatalf("open search index: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// matches runs the query the search handler will run, minus the visibility
// join, and returns the references it resolved to.
func matches(t *testing.T, store *searchStore, expression string) []searchRow {
	t.Helper()
	rows, err := store.db.Query(`
SELECT r.opus_name, r.segment_id, r.start_ms, r.end_ms, r.speaker_id
  FROM segment_fts f
  JOIN segment_ref r ON r.rowid_ = f.rowid
 WHERE segment_fts MATCH ?
 ORDER BY r.opus_name, r.start_ms`, expression)
	if err != nil {
		t.Fatalf("match %q: %v", expression, err)
	}
	defer rows.Close()
	var out []searchRow
	for rows.Next() {
		var name string
		var row searchRow
		if err := rows.Scan(&name, &row.SegmentID, &row.StartMS, &row.EndMS, &row.SpeakerID); err != nil {
			t.Fatalf("scan match: %v", err)
		}
		row.Text = name
		out = append(out, row)
	}
	return out
}

func TestSearchStoreIndexesAndResolvesReferences(t *testing.T) {
	store := newTestSearchStore(t)
	ctx := context.Background()

	// The artifact's own readable segments, as they come out of a .opus.
	rows := searchRowsFromSegments([]searchReadableSegment{
		{ID: "seg_0007", SpeakerID: "S1", StartMS: 1_000, EndMS: 4_200,
			Text: "we discussed the acquisition at some length"},
		{ID: "seg_0008", SpeakerID: "S2", StartMS: 62_000, EndMS: 64_500,
			Text: "and then the roadmap"},
	})
	if err := store.ReplaceMeeting(ctx, "JOB1.opus", "sha-1", searchRowSourceSegments, rows); err != nil {
		t.Fatalf("replace: %v", err)
	}

	hits := matches(t, store, "acquisition")
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1: %+v", len(hits), hits)
	}
	// A hit names the producer's own segment and its real bounds, so it points
	// at the same unit the viewer renders.
	if hits[0].Text != "JOB1.opus" || hits[0].SegmentID != "seg_0007" {
		t.Errorf("hit = %+v, want JOB1.opus / seg_0007", hits[0])
	}
	if hits[0].StartMS != 1_000 || hits[0].EndMS != 4_200 {
		t.Errorf("bounds = [%d,%d], want the segment's own [1000,4200]", hits[0].StartMS, hits[0].EndMS)
	}
	if hits[0].SpeakerID != "S1" {
		t.Errorf("speaker = %q, want S1", hits[0].SpeakerID)
	}
	if got := matches(t, store, "roadmap"); len(got) == 0 || got[0].SegmentID != "seg_0008" {
		t.Errorf("second segment not indexed: %+v", got)
	}
	if got := matches(t, store, `"discussed the acquisition"`); len(got) == 0 {
		t.Error("phrase query found nothing inside a segment")
	}
}

// A meeting with no readable transcript is still searchable, from words, and
// the index records that its rows are the coarser kind.
func TestSearchStoreIndexesAWordDerivedFallback(t *testing.T) {
	store := newTestSearchStore(t)
	ctx := context.Background()

	rows := deriveSearchRowsFromWords([]searchTranscriptWord{
		{SpeakerID: "S1", StartMS: 1_000, EndMS: 1_200, Text: "we"},
		{SpeakerID: "S1", StartMS: 1_400, EndMS: 1_700, Text: "discussed"},
		{SpeakerID: "S1", StartMS: 1_900, EndMS: 2_100, Text: "acquisition"},
	})
	if err := store.ReplaceMeeting(ctx, "JOB2.opus", "sha-2", searchRowSourceWords, rows); err != nil {
		t.Fatalf("replace: %v", err)
	}

	hits := matches(t, store, "acquisition")
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1: %+v", len(hits), hits)
	}
	// Even a derived row cites the speech it holds, never the window's bounds.
	if hits[0].StartMS != 1_000 || hits[0].EndMS != 2_100 {
		t.Errorf("bounds = [%d,%d], want the speech span [1000,2100]", hits[0].StartMS, hits[0].EndMS)
	}
	var source string
	if err := store.db.QueryRow(
		`SELECT row_source FROM meeting_index WHERE opus_name = 'JOB2.opus'`).Scan(&source); err != nil {
		t.Fatalf("read row_source: %v", err)
	}
	if source != searchRowSourceWords {
		t.Errorf("row_source = %q, want %q — a coarser index must say so", source, searchRowSourceWords)
	}
}

// makes references-only a property of the store rather than a rule a handler
// has to remember.
func TestSearchStoreCannotEmitIndexedText(t *testing.T) {
	store := newTestSearchStore(t)
	if err := store.ReplaceMeeting(context.Background(), "JOB1.opus", "", searchRowSourceSegments, []searchRow{
		{SegmentID: "s1", StartMS: 0, EndMS: 30_000, SpeakerID: "S1", Text: "severance package details"},
	}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	var text sql.NullString
	if err := store.db.QueryRow(`SELECT text FROM segment_fts WHERE segment_fts MATCH 'severance'`).Scan(&text); err != nil {
		t.Fatalf("select text: %v", err)
	}
	if text.Valid {
		t.Fatalf("the store returned indexed text %q; content='' should make it NULL", text.String)
	}
	var snippet sql.NullString
	if err := store.db.QueryRow(
		`SELECT snippet(segment_fts, 0, '[', ']', '…', 8) FROM segment_fts WHERE segment_fts MATCH 'severance'`).Scan(&snippet); err != nil {
		t.Fatalf("snippet: %v", err)
	}
	if snippet.Valid {
		t.Fatalf("snippet() returned %q; it should be NULL on a contentless table", snippet.String)
	}
}

// Re-indexing replaces rather than appends: a rerun must not leave the previous
// attempt's rows searchable beside the current ones.
func TestSearchStoreReplacesRatherThanAppends(t *testing.T) {
	store := newTestSearchStore(t)
	ctx := context.Background()

	if err := store.ReplaceMeeting(ctx, "JOB1.opus", "sha-1", searchRowSourceSegments, []searchRow{
		{SegmentID: "s1", StartMS: 0, EndMS: 30_000, SpeakerID: "S1", Text: "original wording"},
	}); err != nil {
		t.Fatalf("first replace: %v", err)
	}
	if err := store.ReplaceMeeting(ctx, "JOB1.opus", "sha-2", searchRowSourceSegments, []searchRow{
		{SegmentID: "s1", StartMS: 0, EndMS: 30_000, SpeakerID: "S1", Text: "corrected wording"},
	}); err != nil {
		t.Fatalf("second replace: %v", err)
	}

	if got := matches(t, store, "original"); len(got) != 0 {
		t.Errorf("the previous attempt is still searchable: %+v", got)
	}
	if got := matches(t, store, "corrected"); len(got) != 1 {
		t.Errorf("the current attempt is not searchable: %+v", got)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM segment_ref WHERE opus_name = 'JOB1.opus'`).Scan(&count); err != nil {
		t.Fatalf("count windows: %v", err)
	}
	if count != 1 {
		t.Errorf("segment_ref rows = %d, want 1 — the old row was orphaned", count)
	}
}

// An orphaned posting in a contentless index is unreachable garbage that still
// matches, so dropping a meeting has to reach segment_fts too — which no foreign
// key can do for it.
func TestSearchStoreForgetLeavesNoOrphanedPostings(t *testing.T) {
	store := newTestSearchStore(t)
	ctx := context.Background()

	if err := store.ReplaceMeeting(ctx, "JOB1.opus", "", searchRowSourceSegments, []searchRow{
		{SegmentID: "s1", StartMS: 0, EndMS: 30_000, SpeakerID: "S1", Text: "quarterly numbers"},
	}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if err := store.ForgetMeeting(ctx, "JOB1.opus"); err != nil {
		t.Fatalf("forget: %v", err)
	}

	if got := matches(t, store, "quarterly"); len(got) != 0 {
		t.Errorf("a forgotten meeting still matches: %+v", got)
	}
	var postings int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM segment_fts WHERE segment_fts MATCH 'quarterly'`).Scan(&postings); err != nil {
		t.Fatalf("count postings: %v", err)
	}
	if postings != 0 {
		t.Errorf("orphaned postings = %d, want 0", postings)
	}
}

// A failed ingest must drop the meeting out of the covered count, so an answer
// degrades to partial coverage rather than a confident false negative.
func TestSearchStoreUnavailableDropsRowsAndCoverage(t *testing.T) {
	store := newTestSearchStore(t)
	ctx := context.Background()

	for _, name := range []string{"JOB1.opus", "JOB2.opus"} {
		if err := store.ReplaceMeeting(ctx, name, "", searchRowSourceSegments, []searchRow{
			{SegmentID: "s1", StartMS: 0, EndMS: 30_000, SpeakerID: "S1", Text: "budget"},
		}); err != nil {
			t.Fatalf("replace %s: %v", name, err)
		}
	}
	if err := store.MarkUnavailable(ctx, "JOB2.opus", "ingest-failed"); err != nil {
		t.Fatalf("mark unavailable: %v", err)
	}

	coverage, err := store.Coverage(ctx)
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	if coverage.Indexed != 1 || coverage.Unavailable != 1 {
		t.Fatalf("coverage = %+v, want indexed=1 unavailable=1", coverage)
	}
	// Its rows go too: a meeting that is not indexed must not keep answering
	// from whatever it held before.
	for _, hit := range matches(t, store, "budget") {
		if hit.Text == "JOB2.opus" {
			t.Error("an unavailable meeting is still searchable")
		}
	}
}

// The disposability invariant, as a test: a version mismatch deletes and
// rebuilds rather than migrating or refusing to start.
func TestSearchStoreRebuildsOnSchemaVersionMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), searchStoreFilename)
	store, err := openSearchStore(path, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.ReplaceMeeting(context.Background(), "JOB1.opus", "", searchRowSourceSegments, []searchRow{
		{SegmentID: "s1", StartMS: 0, EndMS: 30_000, SpeakerID: "S1", Text: "stale index content"},
	}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	// Pretend the file was written by another build, in both directions: a
	// NEWER index must rebuild rather than abort, which is the failure mode
	// real migrations would have.
	if _, err := store.db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	var logs bytes.Buffer
	reopened, err := openSearchStore(path, log.New(&logs, "", 0))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	version, err := reopened.userVersion()
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if version != searchSchemaVersion {
		t.Fatalf("version = %d, want %d", version, searchSchemaVersion)
	}
	if got := matches(t, reopened, "stale"); len(got) != 0 {
		t.Errorf("the stale index survived a version change: %+v", got)
	}
	if !strings.Contains(logs.String(), "deleting and rebuilding") {
		t.Errorf("a discarded index was not logged: %q", logs.String())
	}
}

// A fresh file reads user_version 0 and must be stamped silently — that is not
// a mismatch worth warning anyone about.
func TestSearchStoreFirstOpenIsSilent(t *testing.T) {
	var logs bytes.Buffer
	store, err := openSearchStore(filepath.Join(t.TempDir(), searchStoreFilename), log.New(&logs, "", 0))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	if logs.Len() != 0 {
		t.Errorf("first open logged %q, want silence", logs.String())
	}
}

// Deleting the file must cost nothing but a rebuild — the invariant the whole
// sidecar rests on.
func TestSearchStoreSurvivesFileDeletion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, searchStoreFilename)
	store, err := openSearchStore(path, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := removeSearchStoreFiles(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("index still present after removal: %v", err)
	}
	rebuilt, err := openSearchStore(path, nil)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	defer rebuilt.Close()
	coverage, err := rebuilt.Coverage(context.Background())
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	if coverage.Indexed != 0 || coverage.Unavailable != 0 {
		t.Fatalf("rebuilt index is not empty: %+v", coverage)
	}
}

// The sidecar is a separate file beside the job database, never inside it.
func TestSearchStorePathIsASiblingOfTheJobDatabase(t *testing.T) {
	got := searchStorePath("/var/lib/cassini/state/jobs.sqlite3")
	want := "/var/lib/cassini/state/" + searchStoreFilename
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
}

// WAL is what lets a search read while ingest writes; without it every query
// would serialise behind the publish worker, which is the whole reason this is
// not a table in jobs.sqlite3.
func TestSearchStoreOpensWithConcurrencyPragmas(t *testing.T) {
	store := newTestSearchStore(t)
	for _, tc := range []struct{ pragma, want string }{
		{"journal_mode", "wal"},
		{"busy_timeout", "5000"},
		{"foreign_keys", "1"},
	} {
		var got string
		if err := store.db.QueryRow("PRAGMA " + tc.pragma).Scan(&got); err != nil {
			t.Errorf("read %s: %v", tc.pragma, err)
			continue
		}
		if !strings.EqualFold(got, tc.want) {
			t.Errorf("%s = %q, want %q", tc.pragma, got, tc.want)
		}
	}
}
