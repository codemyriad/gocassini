package operator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// tagService is the annotation service against a fake Nextcloud, with store as
// its projection (nil for none).
func tagService(upstreamURL string, store *annotationStore) *annotationService {
	rt := &Runtime{}
	if store != nil {
		rt.annotations = store
	}
	return &annotationService{
		rt: rt, exapp: searchTestConfig(upstreamURL), client: &http.Client{},
		logger: log.New(io.Discard, "", 0),
	}
}

func getTags(t *testing.T, s *annotationService, caller string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	s.register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, callerReq(http.MethodGet, "/annotations/tags", caller))
	return rec
}

func decodeTags(t *testing.T, rec *httptest.ResponseRecorder) tagVocabularyResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var got tagVocabularyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	return got
}

// untouchedUpstream fails the test if Nextcloud is asked anything.
func untouchedUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("Nextcloud was asked %s %s for a request that could not be answered", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// THE security property of the vocabulary: a tag on a meeting the caller
// cannot open never appears, and that meeting's marks move no count.
func TestTagVocabularyCountsOnlyVisibleMeetings(t *testing.T) {
	store := newTestAnnotationStore(t)
	recordMarks(t, store, "JOB1.opus", annotatedFile(t, "c1", testTagNamespaceA, []testTag{{"tag_h", "hiring"}},
		meetingMark("m1", "tag_h"), rangeMark("m2", "tag_h", 0, 1000)))
	recordMarks(t, store, "JOB2.opus", annotatedFile(t, "c2", testTagNamespaceA, []testTag{{"tag_h", "hiring"}, {"tag_s", "layoffs"}},
		meetingMark("m1", "tag_h"), rangeMark("m2", "tag_h", 0, 1), rangeMark("m3", "tag_h", 1, 2), meetingMark("m4", "tag_s")))
	srv := searchUpstream{catalog: searchTestCatalog, visible: []string{"JOB1.opus"}}.server(t)
	defer srv.Close()

	rec := getTags(t, tagService(srv.URL, store), "alice")
	got := decodeTags(t, rec)
	want := tagVocabularyEntry{TagID: "tag_h", Namespace: testTagNamespaceA, Label: "hiring", Meetings: 1, Marks: 2}
	if len(got.Tags) != 1 || got.Tags[0] != want {
		t.Fatalf("tags = %+v, want only %+v", got.Tags, want)
	}
	if strings.Contains(rec.Body.String(), "layoffs") || strings.Contains(rec.Body.String(), "tag_s") {
		t.Errorf("a hidden meeting's tag reached the answer: %s", rec.Body.String())
	}
	if got.Coverage != (annotationCoverage{Visible: 1, Indexed: 1}) {
		t.Errorf("coverage = %+v, want visible=1 indexed=1", got.Coverage)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("a per-caller answer must be uncacheable")
	}
}

// A meeting whose marks could not be read is outside coverage, so the counts
// say they are partial.
func TestTagVocabularyReportsPartialCoverage(t *testing.T) {
	store := newTestAnnotationStore(t)
	recordMarks(t, store, "JOB1.opus", annotatedFile(t, "c1", testTagNamespaceA, []testTag{{"tag_h", "hiring"}}, meetingMark("m", "tag_h")))
	if err := store.MarkUnavailable(t.Context(), "JOB2.opus", "annotations-unreadable"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	srv := searchUpstream{catalog: searchTestCatalog, visible: []string{"JOB1.opus", "JOB2.opus"}}.server(t)
	defer srv.Close()

	got := decodeTags(t, getTags(t, tagService(srv.URL, store), "alice"))
	if got.Coverage != (annotationCoverage{Visible: 2, Indexed: 1}) {
		t.Fatalf("coverage = %+v, want visible=2 indexed=1", got.Coverage)
	}
}

// A caller who may read nothing gets a truthful empty answer.
func TestTagVocabularyEmptyForDeniedCaller(t *testing.T) {
	store := newTestAnnotationStore(t)
	recordMarks(t, store, "JOB1.opus", annotatedFile(t, "c1", testTagNamespaceA, []testTag{{"tag_h", "hiring"}}, meetingMark("m", "tag_h")))
	srv := searchUpstream{catalog: searchTestCatalog, visible: nil}.server(t)
	defer srv.Close()

	rec := getTags(t, tagService(srv.URL, store), "alice")
	got := decodeTags(t, rec)
	if len(got.Tags) != 0 || got.Coverage != (annotationCoverage{}) {
		t.Fatalf("got %+v, want nothing and zero coverage", got)
	}
	if !strings.Contains(rec.Body.String(), `"tags":[]`) {
		t.Errorf("tags must serialise as [] not null: %s", rec.Body.String())
	}
}

// No projection is "ask again later", not "nothing is tagged" — and it is
// answered before Nextcloud is asked anything.
func TestTagVocabularyWithoutAnIndexIs503(t *testing.T) {
	rec := getTags(t, tagService(untouchedUpstream(t).URL, nil), "alice")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not an empty result") {
		t.Errorf("body should distinguish this from an empty vocabulary: %s", rec.Body.String())
	}
}

// Substrate failures are loud: Nextcloud, and the projection itself.
func TestTagVocabularyFailsLoudly(t *testing.T) {
	for _, tc := range []struct {
		name     string
		upstream searchUpstream
	}{
		{"scan failed", searchUpstream{catalog: searchTestCatalog, propfindStatus: 500}},
		{"no mount", searchUpstream{catalog: searchTestCatalog, propfindStatus: 404}},
		{"archive unreachable", searchUpstream{catalogStatus: 500}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := tc.upstream.server(t)
			defer srv.Close()
			rec := getTags(t, tagService(srv.URL, newTestAnnotationStore(t)), "alice")
			if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), `"tags"`) {
				t.Fatalf("code = %d body=%s, want a 502 that does not look like a result", rec.Code, rec.Body.String())
			}
		})
	}
	t.Run("projection unreadable", func(t *testing.T) {
		store := newTestAnnotationStore(t)
		_ = store.Close()
		srv := searchUpstream{catalog: searchTestCatalog, visible: []string{"JOB1.opus"}}.server(t)
		defer srv.Close()
		rec := getTags(t, tagService(srv.URL, store), "alice")
		if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "not an empty result") {
			t.Fatalf("code = %d body=%s, want 502", rec.Code, rec.Body.String())
		}
	})
}

// --- tag narrowing: search ----------------------------------------------------

// THE narrowing property, search's bury test with tags: the one visible, tagged
// meeting is ranked below a full page of visible untagged matches, behind 200
// tagged matches the caller cannot open. Narrowing the visible set BEFORE the
// statement finds it; filtering the page afterwards — the Option 1 defect —
// would report nothing.
func TestSearchTagNarrowingHappensBeforeTheLimit(t *testing.T) {
	index := newTestSearchStore(t)
	tags := newTestAnnotationStore(t)
	hiring := []testTag{{"tag_hiring", "hiring"}}

	var entries []string
	var visible []string
	add := func(id, opusName string) {
		entries = append(entries, fmt.Sprintf(`{"id":%q,"title":"t","dateLabel":"2026-09-01 09:00","audioPath":"./meetings/%s"}`, id, opusName))
	}
	for i := 0; i < 200; i++ {
		name := fmt.Sprintf("HIDDEN%03d.opus", i)
		seedSearchable(t, index, name, seg("s1", "S9", 1000, 4000, "quarterly revenue discussion"))
		recordMarks(t, tags, name, annotatedFile(t, "c", testTagNamespaceA, hiring, meetingMark("m", "tag_hiring")))
		add("MEET-"+name, name)
	}
	for i := 0; i < 50; i++ {
		name := fmt.Sprintf("OPEN%03d.opus", i)
		seedSearchable(t, index, name, seg("s1", "S1", 1000, 4000, "quarterly revenue discussion"))
		recordMarks(t, tags, name, annotateResult{ContainerSHA256: "c"})
		add("MEET-"+name, name)
		visible = append(visible, name)
	}
	seedSearchable(t, index, "MINE.opus", seg("s1", "S1", 5000, 9000,
		"quarterly revenue discussion "+strings.Repeat("and then a great many other things were said ", 20)))
	recordMarks(t, tags, "MINE.opus", annotatedFile(t, "c", testTagNamespaceA, hiring, meetingMark("m", "tag_hiring")))
	add("MEET-MINE", "MINE.opus")
	visible = append(visible, "MINE.opus")

	catalog := `{"version":"cassini.viewer.catalog.v1","meetings":[` + strings.Join(entries, ",") + `]}`
	srv := searchUpstream{catalog: catalog, visible: visible}.server(t)
	defer srv.Close()
	cfg := searchTestConfig(srv.URL)
	deps := searchDeps{index: index, annotations: tags}

	// The premise: without the tag, the page is full and MINE is not on it.
	plain := decodeSearch(t, doSearchWithDeps(t, cfg, deps, "q=quarterly+revenue&limit=5", "alice"))
	if len(plain.Hits) != 5 {
		t.Fatalf("premise: hits = %d, want a full page of 5", len(plain.Hits))
	}
	for _, hit := range plain.Hits {
		if hit.MeetingID == "MEET-MINE" {
			t.Fatalf("premise: MINE ranked on the first page, so this test buries nothing")
		}
	}

	rec := doSearchWithDeps(t, cfg, deps, "q=quarterly+revenue&limit=5&tag=hiring", "alice")
	got := decodeSearch(t, rec)
	if len(got.Hits) == 0 {
		t.Fatalf("hits = none, want the one visible tagged meeting (body=%s)", rec.Body.String())
	}
	for _, hit := range got.Hits {
		if hit.MeetingID != "MEET-MINE" {
			t.Fatalf("hit %q, want only MEET-MINE: untagged and hidden meetings must not appear", hit.MeetingID)
		}
	}
}

// A tag carried only by meetings the caller cannot open narrows to exactly
// what a tag nobody uses narrows to — its existence is not observable.
func TestSearchTagOnlyOnHiddenMeetingsIsIndistinguishableFromNone(t *testing.T) {
	index := newTestSearchStore(t)
	seedSearchable(t, index, "JOB1.opus", seg("s1", "S1", 1000, 4000, "the acquisition"))
	seedSearchable(t, index, "JOB2.opus", seg("s1", "S9", 1000, 4000, "the acquisition"))
	tags := newTestAnnotationStore(t)
	recordMarks(t, tags, "JOB1.opus", annotateResult{ContainerSHA256: "c1"})
	recordMarks(t, tags, "JOB2.opus", annotatedFile(t, "c2", testTagNamespaceA, []testTag{{"tag_s", "layoffs"}}, meetingMark("m", "tag_s")))
	srv := searchUpstream{catalog: searchTestCatalog, visible: []string{"JOB1.opus"}}.server(t)
	defer srv.Close()
	cfg := searchTestConfig(srv.URL)
	deps := searchDeps{index: index, annotations: tags}

	hidden := doSearchWithDeps(t, cfg, deps, "q=acquisition&tag=layoffs", "alice")
	unknown := doSearchWithDeps(t, cfg, deps, "q=acquisition&tag=nosuchtag", "alice")
	if hidden.Code != http.StatusOK || len(decodeSearch(t, hidden).Hits) != 0 {
		t.Fatalf("code = %d body=%s, want 200 with no hits", hidden.Code, hidden.Body.String())
	}
	if hidden.Body.String() != unknown.Body.String() {
		t.Fatalf("a hidden tag is observable:\n hidden:  %s\n unknown: %s", hidden.Body.String(), unknown.Body.String())
	}
}

// tag= matches a label in any case, or an id exactly.
func TestSearchTagMatchesALabelOrAnID(t *testing.T) {
	index := newTestSearchStore(t)
	seedSearchable(t, index, "JOB1.opus", seg("s1", "S1", 1000, 4000, "the acquisition"))
	tags := newTestAnnotationStore(t)
	recordMarks(t, tags, "JOB1.opus", annotatedFile(t, "c1", testTagNamespaceA, []testTag{{"tag_u", "Übergabe"}}, meetingMark("m", "tag_u")))
	srv := searchUpstream{catalog: searchTestCatalog, visible: []string{"JOB1.opus", "JOB2.opus"}}.server(t)
	defer srv.Close()
	deps := searchDeps{index: index, annotations: tags}

	for _, tag := range []string{"%C3%BCbergabe", "%C3%9CBERGABE", "tag_u"} {
		got := decodeSearch(t, doSearchWithDeps(t, searchTestConfig(srv.URL), deps, "q=acquisition&tag="+tag, "alice"))
		if len(got.Hits) != 1 || got.Hits[0].MeetingID != "MEET-1" {
			t.Errorf("tag=%s: hits = %+v, want MEET-1", tag, got.Hits)
		}
	}
}

// Each hit carries the tags whose ranges it overlaps. Touching at a boundary
// is not overlapping, and a whole-meeting tag is not a range.
func TestSearchHitsCarryTheMarksTheyOverlap(t *testing.T) {
	index := newTestSearchStore(t)
	seedSearchable(t, index, "JOB1.opus",
		seg("s1", "S1", 1000, 4000, "the acquisition"),
		seg("s2", "S2", 10_000, 14_000, "the acquisition again"))
	// JOB2's marks were never read: its hit must not claim it has none.
	seedSearchable(t, index, "JOB2.opus", seg("s9", "S1", 1000, 4000, "the acquisition"))
	tags := newTestAnnotationStore(t)
	recordMarks(t, tags, "JOB1.opus", annotatedFile(t, "c1", testTagNamespaceA,
		[]testTag{{"tag_b", "budget"}, {"tag_h", "hiring"}, {"tag_w", "strategy"}},
		meetingMark("mk_w", "tag_w"),
		rangeMark("mk_b", "tag_b", 3999, 4001),
		rangeMark("mk_h", "tag_h", 4000, 10_000)))
	srv := searchUpstream{catalog: searchTestCatalog, visible: []string{"JOB1.opus", "JOB2.opus"}}.server(t)
	defer srv.Close()

	rec := doSearchWithDeps(t, searchTestConfig(srv.URL), searchDeps{index: index, annotations: tags}, "q=acquisition", "alice")
	got := decodeSearch(t, rec)
	bySegment := map[string][]searchHitMark{}
	for _, hit := range got.Hits {
		if (hit.Marks == nil) != (hit.SegmentID == "s9") {
			t.Fatalf("hit %s: marks = %v, want them only where the meeting's marks are known: %s", hit.SegmentID, hit.Marks, rec.Body.String())
		}
		if hit.Marks != nil {
			bySegment[hit.SegmentID] = *hit.Marks
		}
	}
	if marks := bySegment["s1"]; len(marks) != 1 || marks[0] != (searchHitMark{TagID: "tag_b", Label: "budget"}) {
		t.Errorf("s1 marks = %+v, want only budget (hiring only touches it)", marks)
	}
	if marks, ok := bySegment["s2"]; !ok || len(marks) != 0 {
		t.Errorf("s2 marks = %+v, want none (hiring ends where it starts)", marks)
	}
	if !strings.Contains(rec.Body.String(), `"marks":[]`) {
		t.Errorf("no marks must serialise as [], not be omitted: %s", rec.Body.String())
	}
}

// Without a projection plain search still answers, and a hit says nothing
// about marks rather than claiming it has none.
func TestSearchHitsOmitMarksWithoutAnIndex(t *testing.T) {
	index := newTestSearchStore(t)
	seedSearchable(t, index, "JOB1.opus", seg("s1", "S1", 1000, 4000, "the acquisition"))
	srv := searchUpstream{catalog: searchTestCatalog, visible: []string{"JOB1.opus"}}.server(t)
	defer srv.Close()

	rec := doSearch(t, searchTestConfig(srv.URL), index, "q=acquisition", "alice")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"marks"`) || strings.Contains(rec.Body.String(), "tagCoverage") {
		t.Fatalf("code = %d body=%s, want hits with no marks field", rec.Code, rec.Body.String())
	}
}

// Narrowing that cannot be done is 503, never a silently unnarrowed answer;
// a tag no label or id could equal is the caller's to fix.
func TestSearchTagRefusals(t *testing.T) {
	index := newTestSearchStore(t)
	srv := searchUpstream{catalog: searchTestCatalog, visible: []string{"JOB1.opus"}}.server(t)
	defer srv.Close()
	cfg := searchTestConfig(srv.URL)

	if rec := doSearch(t, cfg, index, "q=acquisition&tag=hiring", "alice"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no tag index: code = %d, want 503", rec.Code)
	}
	long := strings.Repeat("x", maxTagParamRunes+1)
	deps := searchDeps{index: index, annotations: newTestAnnotationStore(t)}
	if rec := doSearchWithDeps(t, cfg, deps, "q=acquisition&tag="+long, "alice"); rec.Code != http.StatusBadRequest {
		t.Errorf("overlong tag: code = %d, want 400", rec.Code)
	}
	broken := newTestAnnotationStore(t)
	_ = broken.Close()
	if rec := doSearchWithDeps(t, cfg, searchDeps{index: index, annotations: broken}, "q=acquisition&tag=hiring", "alice"); rec.Code != http.StatusBadGateway {
		t.Errorf("unreadable tag index: code = %d, want 502", rec.Code)
	}
}

// --- tag narrowing: the meeting list ----------------------------------------

func getMeetingsListWithTags(t *testing.T, cfg ExAppConfig, tags *annotationStore, query, caller string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	proxy := cfg.ncFilesProxy(nil, searchDeps{annotations: tags})
	if !proxy(rec, callerReq(http.MethodGet, "/published/"+meetingsListPath+"?"+query, caller), meetingsListPath) {
		t.Fatal("proxy declined the meetings list request")
	}
	return rec
}

// The list narrows to the caller's visible meetings carrying the tag — a
// hidden meeting carrying it stays hidden.
func TestMeetingsListNarrowsToATag(t *testing.T) {
	tags := newTestAnnotationStore(t)
	hiring := []testTag{{"tag_h", "hiring"}}
	recordMarks(t, tags, "JOB1.opus", annotatedFile(t, "c1", testTagNamespaceA, hiring, rangeMark("m", "tag_h", 0, 1000)))
	recordMarks(t, tags, "JOB2.opus", annotatedFile(t, "c2", testTagNamespaceA, []testTag{{"tag_b", "budget"}}, meetingMark("m", "tag_b")))
	recordMarks(t, tags, "JOB3.opus", annotatedFile(t, "c3", testTagNamespaceA, hiring, meetingMark("m", "tag_h")))
	// JOB4 is visible but its marks were never read.
	srv := meetingsListUpstream{visible: []string{"JOB1.opus", "JOB2.opus", "JOB4.opus"}}.server(t)
	defer srv.Close()
	cfg := meetingsListConfig(srv.URL)

	rec := getMeetingsListWithTags(t, cfg, tags, "tag=Hiring", "alice")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeMeetingsList(t, rec)
	if ids := listedIDs(t, got); strings.Join(ids, ",") != "a" {
		t.Fatalf("ids = %v, want [a] — c carries the tag but is hidden", ids)
	}
	if got.Excluded == nil || got.Excluded.Total != 2 {
		t.Errorf("excluded = %+v, want total=2 (b untagged, d unread)", got.Excluded)
	}
	if got.Filter == nil || got.Filter.Tag != "Hiring" {
		t.Errorf("filter echo = %+v, want the tag", got.Filter)
	}

	// The tag composes with the other filters.
	both := decodeMeetingsList(t, getMeetingsListWithTags(t, cfg, tags, "tag=hiring&room=rm_bbbbbbbbbbbbbbbb", "alice"))
	if ids := listedIDs(t, both); len(ids) != 0 {
		t.Errorf("tag+room ids = %v, want none", ids)
	}
	plain := decodeMeetingsList(t, getMeetingsListWithTags(t, cfg, tags, "", "alice"))
	if len(listedIDs(t, plain)) != 3 {
		t.Errorf("plain list = %+v, want all three", plain)
	}
}

func TestMeetingsListTagRefusals(t *testing.T) {
	srv := untouchedUpstream(t)
	cfg := meetingsListConfig(srv.URL)
	if rec := getMeetingsListWithTags(t, cfg, nil, "tag=hiring", "alice"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no tag index: code = %d, want 503 (body=%s)", rec.Code, rec.Body.String())
	}
	long := strings.Repeat("x", maxTagParamRunes+1)
	if rec := getMeetingsListWithTags(t, cfg, newTestAnnotationStore(t), "tag="+long, "alice"); rec.Code != http.StatusBadRequest {
		t.Errorf("overlong tag: code = %d, want 400", rec.Code)
	}
}

// --- redaction ----------------------------------------------------------------

// A tag label is user content: it must never reach the container log.
func TestRequestLoggerRedactsTags(t *testing.T) {
	var logs bytes.Buffer
	handler := requestLogger(log.New(&logs, "", 0), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for _, target := range []string{
		"/published/" + meetingsListPath + "?tag=redundancies",
		"/published/" + meetingsListPath + "?from=2026-08-01&tag=redundancies",
		"/published/" + meetingsListPath + "?TAG=redundancies",
		"/published/" + meetingsListPath + "?tag=%zzredundancies",
		"/published/" + searchURLPath + "?q=x&tag=redundancies",
		annotationsURLPath + "/tags?tag=redundancies",
	} {
		logs.Reset()
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
		if strings.Contains(logs.String(), "redundancies") {
			t.Errorf("%s: the tag reached the log: %q", target, logs.String())
		}
		if path, _, _ := strings.Cut(target, "?"); !strings.Contains(logs.String(), path) {
			t.Errorf("%s: the route should still be identifiable: %q", target, logs.String())
		}
	}
	// A list without a tag keeps its query, which is what makes the log useful.
	logs.Reset()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/published/"+meetingsListPath+"?room=rm_1", nil))
	if !strings.Contains(logs.String(), "room=rm_1") {
		t.Errorf("an untagged list query should still be logged: %q", logs.String())
	}
}
