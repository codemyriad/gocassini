package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testTagNamespaceA = "urn:uuid:07e4eab6-4f5f-4cc4-8913-46432c5cd726"
	testTagNamespaceB = "urn:uuid:5b0c2f1e-9a4d-4c1b-8e2f-3d4c5b6a7980"
)

var testAudioDigest = strings.Repeat("ab", 32)

func newTestAnnotationStore(t *testing.T) *annotationStore {
	t.Helper()
	store, err := openAnnotationStore(filepath.Join(t.TempDir(), annotationsStoreFilename), nil)
	if err != nil {
		t.Fatalf("open annotations index: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

type testTag struct{ id, label string }

type testMark struct {
	id, tagID, kind string
	start, end      int64
}

func meetingMark(id, tagID string) testMark {
	return testMark{id: id, tagID: tagID, kind: annotationsTargetMeeting}
}

func rangeMark(id, tagID string, start, end int64) testMark {
	return testMark{id: id, tagID: tagID, kind: annotationsTargetTimeRange, start: start, end: end}
}

// annotatedFile is what `cassini annotate show` reports for a file carrying
// these marks, resolved against its own audio.
func annotatedFile(t *testing.T, container, namespace string, tags []testTag, marks ...testMark) annotateResult {
	t.Helper()
	type target struct {
		Kind    string `json:"kind"`
		StartMS *int64 `json:"startMs,omitempty"`
		EndMS   *int64 `json:"endMs,omitempty"`
	}
	type item struct {
		ID           string            `json:"id"`
		TagID        string            `json:"tagId"`
		Target       target            `json:"target"`
		CreatedAtUTC string            `json:"createdAtUtc"`
		Actor        map[string]string `json:"actor"`
		OperationID  string            `json:"operationId"`
	}
	tagList := []map[string]string{}
	for _, tag := range tags {
		tagList = append(tagList, map[string]string{"id": tag.id, "label": tag.label})
	}
	items := []item{}
	for _, mark := range marks {
		it := item{
			ID: mark.id, TagID: mark.tagID, Target: target{Kind: mark.kind},
			CreatedAtUTC: "2026-09-10T11:23:54Z", Actor: map[string]string{"kind": "person", "id": "alice"},
			OperationID: "op_1",
		}
		if mark.kind == annotationsTargetTimeRange {
			start, end := mark.start, mark.end
			it.Target.StartMS, it.Target.EndMS = &start, &end
		}
		items = append(items, it)
	}
	raw, err := json.Marshal(map[string]any{
		"format": annotationsDocFormatV1, "revision": 3, "audioOpusSha256": testAudioDigest,
		"tagNamespace": namespace, "tags": tagList, "items": items,
	})
	if err != nil {
		t.Fatalf("encode annotations: %v", err)
	}
	resolved := true
	return annotateResult{
		Format: annotateResultFormat, Annotations: raw, Revision: 3, Resolved: &resolved,
		AudioOpusSHA256: testAudioDigest, ContainerSHA256: container,
	}
}

func recordMarks(t *testing.T, store *annotationStore, opusName string, result annotateResult) {
	t.Helper()
	if err := store.Record(context.Background(), opusName, result); err != nil {
		t.Fatalf("record %s: %v", opusName, err)
	}
}

type annotationRow struct {
	state, container string
	marks            int
}

func readAnnotationRow(t *testing.T, store *annotationStore, opusName string) annotationRow {
	t.Helper()
	var row annotationRow
	if err := store.db.QueryRow(
		`SELECT state, container_sha256 FROM meeting_annotations WHERE opus_name = ?`, opusName,
	).Scan(&row.state, &row.container); err != nil {
		t.Fatalf("read %s: %v", opusName, err)
	}
	if err := store.db.QueryRow(
		`SELECT COUNT(*) FROM annotation_item WHERE opus_name = ?`, opusName).Scan(&row.marks); err != nil {
		t.Fatalf("count marks for %s: %v", opusName, err)
	}
	return row
}

// Record replaces, never merges: a mark removed from the file leaves the
// projection too, or the vocabulary would keep counting it forever.
func TestAnnotationStoreRecordReplacesAMeetingsRows(t *testing.T) {
	store := newTestAnnotationStore(t)
	ctx := context.Background()
	recordMarks(t, store, "JOB1.opus", annotatedFile(t, "c1", testTagNamespaceA,
		[]testTag{{"tag_hiring", "hiring"}}, meetingMark("mk_1", "tag_hiring"), rangeMark("mk_2", "tag_hiring", 1000, 2000)))
	recordMarks(t, store, "JOB1.opus", annotatedFile(t, "c2", testTagNamespaceA,
		[]testTag{{"tag_budget", "budget"}}, rangeMark("mk_3", "tag_budget", 5000, 6000)))

	vocab, err := store.Vocabulary(ctx, []string{"JOB1.opus"})
	if err != nil {
		t.Fatalf("vocabulary: %v", err)
	}
	if len(vocab) != 1 || vocab[0].TagID != "tag_budget" || vocab[0].Marks != 1 || vocab[0].Meetings != 1 {
		t.Fatalf("vocabulary = %+v, want only the file's current tag", vocab)
	}
	if _, ok, _ := store.ResolveLabel(ctx, "hiring", allRecorded(t, store)); ok {
		t.Error("a tag removed from the file must leave the projection")
	}
	if row := readAnnotationRow(t, store, "JOB1.opus"); row.container != "c2" || row.marks != 1 || row.state != annotationsStateIndexed {
		t.Errorf("row = %+v, want the second file's bytes and one mark", row)
	}
}

// A tolerant reader: one mark it cannot interpret costs that mark, not the
// meeting.
func TestAnnotationStoreSkipsWhatItCannotRead(t *testing.T) {
	store := newTestAnnotationStore(t)
	recordMarks(t, store, "JOB1.opus", annotatedFile(t, "c1", testTagNamespaceA,
		[]testTag{{"tag_a", "alpha"}, {"", "no id"}, {"tag_blank", "   "}},
		rangeMark("mk_ok", "tag_a", 1000, 2000),
		testMark{id: "mk_kind", tagID: "tag_a", kind: "speaker-turn"},
		rangeMark("mk_empty", "tag_a", 3000, 3000),
		rangeMark("mk_negative", "tag_a", -5, 10),
		rangeMark("mk_orphan", "tag_undefined", 1000, 2000),
		rangeMark("mk_ok", "tag_a", 4000, 5000),
	))
	row := readAnnotationRow(t, store, "JOB1.opus")
	if row.state != annotationsStateIndexed || row.marks != 1 {
		t.Fatalf("row = %+v, want the meeting indexed with its one readable mark", row)
	}
	if _, ok, _ := store.ResolveLabel(context.Background(), "no id", allRecorded(t, store)); ok {
		t.Error("a tag with no id was indexed")
	}
}

// A document in a format this reader does not know may carry marks it cannot
// see. Recording it as "no marks" would be a confident false zero; it is
// outside coverage instead.
func TestAnnotationStoreUnknownFormatIsOutsideCoverage(t *testing.T) {
	ctx := context.Background()
	for name, raw := range map[string]string{
		"newer format": `{"format":"cassini.annotations.v2","tags":[{"id":"tag_x","label":"hiring"}]}`,
		"malformed v1": `{"format":"cassini.annotations.v1","tags":"nope"}`,
		"not a doc":    `[1,2,3]`,
	} {
		t.Run(name, func(t *testing.T) {
			store := newTestAnnotationStore(t)
			if err := store.Record(ctx, "JOB1.opus", annotateResult{Annotations: json.RawMessage(raw), ContainerSHA256: "c1"}); err != nil {
				t.Fatalf("record: %v", err)
			}
			row := readAnnotationRow(t, store, "JOB1.opus")
			if row.state != annotationsStateUnavailable || row.marks != 0 {
				t.Fatalf("row = %+v, want unavailable and no marks", row)
			}
			// The bytes are recorded, so a rebuild does not download a file it
			// already knows it cannot read.
			if row.container != "c1" {
				t.Errorf("container = %q, want c1", row.container)
			}
			coverage, err := store.Coverage(ctx, []string{"JOB1.opus"})
			if err != nil {
				t.Fatalf("coverage: %v", err)
			}
			if coverage != (annotationCoverage{Visible: 1, Indexed: 0}) {
				t.Errorf("coverage = %+v, want visible=1 indexed=0", coverage)
			}
		})
	}
}

// A recording with no annotations member is fully read: it has no marks, and
// that is a complete answer.
func TestAnnotationStoreNoAnnotationsIsIndexedWithNoMarks(t *testing.T) {
	store := newTestAnnotationStore(t)
	ctx := context.Background()
	recordMarks(t, store, "JOB1.opus", annotateResult{Annotations: json.RawMessage("null"), ContainerSHA256: "c1"})
	recordMarks(t, store, "JOB2.opus", annotateResult{ContainerSHA256: "c2"})
	coverage, err := store.Coverage(ctx, []string{"JOB1.opus", "JOB2.opus"})
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	if coverage != (annotationCoverage{Visible: 2, Indexed: 2}) {
		t.Fatalf("coverage = %+v, want both indexed", coverage)
	}
	vocab, err := store.Vocabulary(ctx, []string{"JOB1.opus", "JOB2.opus"})
	if err != nil || len(vocab) != 0 || vocab == nil {
		t.Fatalf("vocabulary = %#v (%v), want an empty non-nil list", vocab, err)
	}
}

func TestAnnotationStoreMarkUnavailableDropsMarksAndCoverage(t *testing.T) {
	store := newTestAnnotationStore(t)
	ctx := context.Background()
	recordMarks(t, store, "JOB1.opus", annotatedFile(t, "c1", testTagNamespaceA,
		[]testTag{{"tag_h", "hiring"}}, meetingMark("mk_1", "tag_h")))
	if err := store.MarkUnavailable(ctx, "JOB1.opus", "annotate show exited 1"); err != nil {
		t.Fatalf("mark unavailable: %v", err)
	}
	row := readAnnotationRow(t, store, "JOB1.opus")
	if row.state != annotationsStateUnavailable || row.marks != 0 {
		t.Fatalf("row = %+v, want unavailable and no marks", row)
	}
	// Which bytes the marks came from is no longer known, so a rebuild must
	// read the file again rather than skip it.
	if row.container != "" {
		t.Errorf("container = %q, want it forgotten", row.container)
	}
	coverage, _ := store.Coverage(ctx, []string{"JOB1.opus"})
	if coverage.Indexed != 0 {
		t.Errorf("coverage = %+v, want indexed=0", coverage)
	}
	if vocab, _ := store.Vocabulary(ctx, []string{"JOB1.opus"}); len(vocab) != 0 {
		t.Errorf("vocabulary = %+v, want nothing from an unavailable meeting", vocab)
	}
}

// SQLite's NOCASE folds ASCII only; the projection folds with Go, so a
// non-ASCII label matches in any case too.
func TestAnnotationStoreResolveLabelFoldsCaseBeyondASCII(t *testing.T) {
	store := newTestAnnotationStore(t)
	recordMarks(t, store, "JOB1.opus", annotatedFile(t, "c1", testTagNamespaceA,
		[]testTag{{"tag_u", "Übergabe"}, {"tag_h", "Hiring"}},
		meetingMark("mk_1", "tag_u"), meetingMark("mk_2", "tag_h")))
	for _, tc := range []struct {
		label, want string
	}{
		{"übergabe", "tag_u"},
		{"  ÜBERGABE  ", "tag_u"},
		{"Übergabe", "tag_u"},
		{"hiring", "tag_h"},
		{"HIRING", "tag_h"},
		{"uebergabe", ""},
		{"", ""},
	} {
		got, ok, err := store.ResolveLabel(context.Background(), tc.label, allRecorded(t, store))
		if err != nil {
			t.Fatalf("resolve %q: %v", tc.label, err)
		}
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("resolve %q = %q/%v, want %q", tc.label, got, ok, tc.want)
		}
	}
}

// Files tagged before the projection existed may each have minted an id for
// the same word. One wins, stably: the one on the most meetings.
func TestAnnotationStoreResolveLabelPrefersTheMostUsedID(t *testing.T) {
	store := newTestAnnotationStore(t)
	recordMarks(t, store, "JOB1.opus", annotatedFile(t, "c1", testTagNamespaceA, []testTag{{"tag_b", "hiring"}}, meetingMark("m", "tag_b")))
	recordMarks(t, store, "JOB2.opus", annotatedFile(t, "c2", testTagNamespaceA, []testTag{{"tag_z", "Hiring"}}, meetingMark("m", "tag_z")))
	recordMarks(t, store, "JOB3.opus", annotatedFile(t, "c3", testTagNamespaceA, []testTag{{"tag_z", "hiring"}}, meetingMark("m", "tag_z")))
	got, ok, err := store.ResolveLabel(context.Background(), "hiring", allRecorded(t, store))
	if err != nil || !ok || got != "tag_z" {
		t.Fatalf("resolve = %q/%v/%v, want tag_z (two meetings beat one)", got, ok, err)
	}
}

// A label resolves only within the namespace most of the archive carries.
func TestAnnotationStoreResolveLabelStaysInTheArchivesNamespace(t *testing.T) {
	store := newTestAnnotationStore(t)
	recordMarks(t, store, "JOB1.opus", annotatedFile(t, "c1", testTagNamespaceB, []testTag{{"tag_b", "hiring"}}, meetingMark("m", "tag_b")))
	recordMarks(t, store, "JOB2.opus", annotatedFile(t, "c2", testTagNamespaceB, []testTag{{"tag_b", "hiring"}}, meetingMark("m", "tag_b")))
	recordMarks(t, store, "JOB3.opus", annotatedFile(t, "c3", testTagNamespaceA, []testTag{{"tag_a", "hiring"}}, meetingMark("m", "tag_a")))
	if got, ok, _ := store.ResolveLabel(context.Background(), "hiring", []string{"JOB1.opus", "JOB3.opus"}); !ok || got != "tag_b" {
		t.Fatalf("resolve = %q/%v, want tag_b from the archive's namespace", got, ok)
	}
}

// The namespace is the one most of the archive's files carry, ignoring one that
// is not well formed; with none, it is empty and the CLI mints one.
func TestAnnotationStoreNamespaceIsTheArchives(t *testing.T) {
	store := newTestAnnotationStore(t)
	ctx := context.Background()
	if got, err := store.Namespace(ctx); err != nil || got != "" {
		t.Fatalf("empty archive: namespace = %q (%v), want none", got, err)
	}
	for _, name := range []string{"JOB1.opus", "JOB2.opus"} {
		recordMarks(t, store, name, annotatedFile(t, "c", testTagNamespaceA, []testTag{{"tag_a", "a"}}, meetingMark("m", "tag_a")))
	}
	recordMarks(t, store, "JOB3.opus", annotatedFile(t, "c", testTagNamespaceB, []testTag{{"tag_b", "b"}}, meetingMark("m", "tag_b")))
	for _, name := range []string{"BAD1.opus", "BAD2.opus", "BAD3.opus"} {
		recordMarks(t, store, name, annotatedFile(t, "c", "urn:uuid:NOT-A-UUID", []testTag{{"tag_c", "c"}}, meetingMark("m", "tag_c")))
	}
	if got, err := store.Namespace(ctx); err != nil || got != testTagNamespaceA {
		t.Fatalf("namespace = %q (%v), want the archive's most common well-formed one", got, err)
	}
}

// The vocabulary counts only the caller's visible meetings: a hidden meeting's
// tag never appears, and its marks move no count.
func TestAnnotationStoreVocabularyCountsOnlyVisibleMeetings(t *testing.T) {
	store := newTestAnnotationStore(t)
	ctx := context.Background()
	hiring := testTag{"tag_h", "hiring"}
	recordMarks(t, store, "V1.opus", annotatedFile(t, "c", testTagNamespaceA, []testTag{hiring},
		meetingMark("m1", "tag_h"), rangeMark("m2", "tag_h", 0, 1000)))
	recordMarks(t, store, "V2.opus", annotatedFile(t, "c", testTagNamespaceA, []testTag{hiring, {"tag_b", "budget"}},
		rangeMark("m1", "tag_h", 0, 1000), meetingMark("m2", "tag_b")))
	recordMarks(t, store, "HIDDEN.opus", annotatedFile(t, "c", testTagNamespaceA, []testTag{hiring, {"tag_s", "layoffs"}},
		meetingMark("m1", "tag_h"), rangeMark("m2", "tag_h", 0, 1), rangeMark("m3", "tag_h", 1, 2), meetingMark("m4", "tag_s")))

	vocab, err := store.Vocabulary(ctx, []string{"V1.opus", "V2.opus", "V1.opus"})
	if err != nil {
		t.Fatalf("vocabulary: %v", err)
	}
	want := []tagVocabularyEntry{
		{TagID: "tag_b", Namespace: testTagNamespaceA, Label: "budget", Meetings: 1, Marks: 1},
		{TagID: "tag_h", Namespace: testTagNamespaceA, Label: "hiring", Meetings: 2, Marks: 3},
	}
	if len(vocab) != len(want) {
		t.Fatalf("vocabulary = %+v, want %+v", vocab, want)
	}
	for i := range want {
		if vocab[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, vocab[i], want[i])
		}
	}
	if empty, err := store.Vocabulary(ctx, nil); err != nil || empty == nil || len(empty) != 0 {
		t.Errorf("vocabulary for nobody = %#v (%v), want an empty list", empty, err)
	}
}

// Relabel is per file, so one id can carry several labels; the vocabulary
// shows the one most meetings use.
func TestAnnotationStoreVocabularyShowsTheMostCommonLabel(t *testing.T) {
	store := newTestAnnotationStore(t)
	for name, label := range map[string]string{"A.opus": "Recruiting", "B.opus": "Recruiting", "C.opus": "hiring"} {
		recordMarks(t, store, name, annotatedFile(t, "c", testTagNamespaceA, []testTag{{"tag_r", label}}, meetingMark("m", "tag_r")))
	}
	vocab, err := store.Vocabulary(context.Background(), []string{"A.opus", "B.opus", "C.opus"})
	if err != nil || len(vocab) != 1 || vocab[0].Label != "Recruiting" || vocab[0].Meetings != 3 {
		t.Fatalf("vocabulary = %+v (%v), want one tag labelled Recruiting over 3 meetings", vocab, err)
	}
}

// A search hit cites the tags whose ranges overlap it — half-open, so ranges
// that merely touch do not — only from resolved documents, and never a
// whole-meeting mark.
func TestAnnotationStoreMarksOverlappingIsHalfOpen(t *testing.T) {
	store := newTestAnnotationStore(t)
	recordMarks(t, store, "JOB1.opus", annotatedFile(t, "c1", testTagNamespaceA,
		[]testTag{{"tag_b", "budget"}, {"tag_h", "hiring"}, {"tag_w", "strategy"}},
		meetingMark("mk_w", "tag_w"),
		rangeMark("mk_b1", "tag_b", 10_000, 20_000),
		rangeMark("mk_b2", "tag_b", 12_000, 18_000),
		rangeMark("mk_h", "tag_h", 30_000, 40_000)))
	unresolved := annotatedFile(t, "c2", testTagNamespaceA, []testTag{{"tag_x", "stale"}}, rangeMark("mk_x", "tag_x", 0, 100_000))
	no := false
	unresolved.Resolved = &no
	recordMarks(t, store, "JOB2.opus", unresolved)

	spans := []markSpan{
		{"JOB1.opus", 0, 10_000},      // ends where budget starts
		{"JOB1.opus", 9_999, 10_001},  // straddles budget's start
		{"JOB1.opus", 11_000, 13_000}, // inside two budget marks: one entry
		{"JOB1.opus", 20_000, 30_000}, // touches budget's end and hiring's start
		{"JOB1.opus", 19_999, 30_001}, // overlaps both by a millisecond
		{"JOB2.opus", 0, 50_000},      // unresolved: never cited
		{"UNKNOWN.opus", 0, 50_000},
	}
	got, err := store.marksOverlapping(context.Background(), spans)
	if err != nil {
		t.Fatalf("marks: %v", err)
	}
	// nil: the meeting's marks are unknown, which is not the same as none.
	want := [][]string{{}, {"tag_b"}, {"tag_b"}, {}, {"tag_b", "tag_h"}, nil, nil}
	for i := range spans {
		ids := []string{}
		for _, mark := range got[i] {
			ids = append(ids, mark.TagID)
		}
		if strings.Join(ids, ",") != strings.Join(want[i], ",") {
			t.Errorf("span %+v: marks = %v, want %v", spans[i], ids, want[i])
		}
		if (got[i] == nil) != (want[i] == nil) {
			t.Errorf("span %d: marks = %#v, want known=%v", i, got[i], want[i] != nil)
		}
	}
	if got[1][0].Label != "budget" {
		t.Errorf("mark = %+v, want its label", got[1][0])
	}
}

func TestAnnotationStoreRebuildsOnSchemaVersionMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), annotationsStoreFilename)
	store, err := openAnnotationStore(path, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	recordMarks(t, store, "JOB1.opus", annotatedFile(t, "c1", testTagNamespaceA, []testTag{{"tag_h", "hiring"}}, meetingMark("m", "tag_h")))
	// Written by another build, newer: it must rebuild rather than refuse.
	if _, err := store.db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	var logs bytes.Buffer
	reopened, err := openAnnotationStore(path, log.New(&logs, "", 0))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	if version, _ := reopened.userVersion(); version != annotationsSchemaVersion {
		t.Fatalf("version = %d, want %d", version, annotationsSchemaVersion)
	}
	if coverage, _ := reopened.Coverage(context.Background(), []string{"JOB1.opus"}); coverage.Indexed != 0 {
		t.Errorf("the stale projection survived a version change: %+v", coverage)
	}
	if !strings.Contains(logs.String(), "deleting and rebuilding") {
		t.Errorf("a discarded projection was not logged: %q", logs.String())
	}
}

func TestAnnotationStoreFirstOpenIsSilent(t *testing.T) {
	var logs bytes.Buffer
	store, err := openAnnotationStore(filepath.Join(t.TempDir(), annotationsStoreFilename), log.New(&logs, "", 0))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	if logs.Len() != 0 {
		t.Errorf("first open logged %q, want silence", logs.String())
	}
}

// NewRuntime-style wiring must never put a nil store behind the interface.
func TestAnnotationReadsIsNilWithoutAStore(t *testing.T) {
	if (&Runtime{}).annotationReads() != nil {
		t.Fatal("a runtime with no projection must have no reads")
	}
	var nilRuntime *Runtime
	if nilRuntime.annotationReads() != nil {
		t.Fatal("a nil runtime must have no reads")
	}
	store := newTestAnnotationStore(t)
	if (&Runtime{annotations: store}).annotationReads() != store {
		t.Fatal("the concrete store must be reachable for the read routes")
	}
}
