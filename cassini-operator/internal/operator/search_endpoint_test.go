package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// searchUpstream stands in for Nextcloud: the owner's catalog, and the
// per-caller PROPFIND that decides what alice may read.
type searchUpstream struct {
	catalog        string
	visible        []string
	catalogStatus  int
	propfindStatus int
}

func (u searchUpstream) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/catalog.json"):
			if u.catalogStatus != 0 && u.catalogStatus != http.StatusOK {
				w.WriteHeader(u.catalogStatus)
				return
			}
			_, _ = w.Write([]byte(u.catalog))
		case r.Method == "PROPFIND":
			if u.propfindStatus != 0 && u.propfindStatus != http.StatusMultiStatus {
				w.WriteHeader(u.propfindStatus)
				return
			}
			var b strings.Builder
			b.WriteString(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:">`)
			b.WriteString(`<d:response><d:href>/remote.php/dav/files/alice/Cassini/Recordings/meetings/</d:href></d:response>`)
			for _, name := range u.visible {
				b.WriteString(`<d:response><d:href>/remote.php/dav/files/alice/Cassini/Recordings/meetings/` + name + `</d:href></d:response>`)
			}
			b.WriteString(`</d:multistatus>`)
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = w.Write([]byte(b.String()))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

const searchTestCatalog = `{"version":"cassini.viewer.catalog.v1","meetings":[
 {"id":"MEET-1","title":"Standup","dateLabel":"2026-09-01 09:00","roomId":"rm_1","roomName":"Daily","audioPath":"./meetings/JOB1.opus"},
 {"id":"MEET-2","title":"Retro","dateLabel":"2026-09-02 09:00","roomId":"rm_2","roomName":"Retro","audioPath":"./meetings/JOB2.opus"}]}`

// doSearch drives the proxy the way the AppAPI route does.
func doSearch(t *testing.T, cfg ExAppConfig, index *searchStore, query, caller string) *httptest.ResponseRecorder {
	t.Helper()
	target := "/published/" + searchURLPath
	if query != "" {
		target += "?" + query
	}
	rec := httptest.NewRecorder()
	proxy := cfg.ncFilesProxy(nil, searchDeps{index: index})
	if proxy == nil {
		t.Fatal("expected a proxy for an AppAPI config")
	}
	if !proxy(rec, callerReq(http.MethodGet, target, caller), searchURLPath) {
		t.Fatal("proxy declined the search request")
	}
	return rec
}

// doSearchWithDeps drives the proxy with explicit deps, for the cases that care
// about more than the index.
func doSearchWithDeps(t *testing.T, cfg ExAppConfig, deps searchDeps, query, caller string) *httptest.ResponseRecorder {
	t.Helper()
	target := "/published/" + searchURLPath
	if query != "" {
		target += "?" + query
	}
	rec := httptest.NewRecorder()
	proxy := cfg.ncFilesProxy(nil, deps)
	if !proxy(rec, callerReq(http.MethodGet, target, caller), searchURLPath) {
		t.Fatal("proxy declined the search request")
	}
	return rec
}

func decodeSearch(t *testing.T, rec *httptest.ResponseRecorder) searchResponse {
	t.Helper()
	var got searchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	return got
}

func searchTestConfig(url string) ExAppConfig {
	cfg := testExAppConfig(url)
	cfg.PublishSink = publishSinkNextcloudFiles
	return cfg
}

// A hit is a reference hydrated from the CALLER'S OWN catalog, and the meeting
// is named by its catalog id — not the .opus basename, which is what the index
// joins on but not what `meetings context` takes.
func TestSearchEndpointReturnsHydratedReferences(t *testing.T) {
	index := newTestSearchStore(t)
	seedSearchable(t, index, "JOB1.opus", seg("seg_7", "S1", 14_000, 18_500, "we discussed the acquisition"))
	srv := searchUpstream{catalog: searchTestCatalog, visible: []string{"JOB1.opus", "JOB2.opus"}}.server(t)
	defer srv.Close()

	rec := doSearch(t, searchTestConfig(srv.URL), index, "q=acquisition", "alice")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeSearch(t, rec)
	if len(got.Hits) != 1 {
		t.Fatalf("hits = %+v, want 1", got.Hits)
	}
	hit := got.Hits[0]
	if hit.MeetingID != "MEET-1" {
		t.Errorf("meetingId = %q, want the catalog id", hit.MeetingID)
	}
	if hit.Title != "Standup" || hit.DateLabel != "2026-09-01 09:00" || hit.RoomName != "Daily" {
		t.Errorf("hit = %+v, want hydration from the caller's catalog", hit)
	}
	if hit.SegmentID != "seg_7" || hit.StartMS != 14_000 || hit.EndMS != 18_500 {
		t.Errorf("hit = %+v, want the segment reference", hit)
	}
	if got.Coverage.Visible != 2 || got.Coverage.Searched != 1 {
		t.Errorf("coverage = %+v, want visible=2 searched=1", got.Coverage)
	}
}

// THE property that survived D-736, narrowed to what it was really protecting.
//
// This test used to assert that no transcript text appeared in a response at
// all. That was the v1 shape: results were references only, so a filtering bug
// could disclose that something existed but never what was said. D-736 puts a
// bounded snippet on the wire, because a meeting list that cannot say WHY a
// meeting matched reads as broken — so the blanket assertion had to go.
//
// What must NOT go with it is the half that was load-bearing: text from a
// meeting outside the caller's visible set must never reach them. Deleting the
// test wholesale would have dropped that silently, which is why it is rewritten
// here rather than removed.
func TestSearchResponseNeverCarriesInvisibleText(t *testing.T) {
	index := newTestSearchStore(t)
	seedSearchable(t, index, "JOB1.opus", seg("seg_7", "S1", 1000, 4000, "the roadmap for Friday"))
	seedSearchable(t, index, "JOB2.opus", seg("seg_9", "S9", 1000, 4000, "the roadmap and the severance package for Bob"))
	// alice can read JOB1 and not JOB2, though both match.
	srv := searchUpstream{catalog: searchTestCatalog, visible: []string{"JOB1.opus"}}.server(t)
	defer srv.Close()

	rec := doSearch(t, searchTestConfig(srv.URL), index, "q=roadmap", "alice")
	body := rec.Body.String()
	for _, leaked := range []string{"severance package", "for Bob", "severance"} {
		if strings.Contains(body, leaked) {
			t.Errorf("response leaked text from a meeting alice cannot read (%q): %s", leaked, body)
		}
	}
	// And the visible meeting's own snippet is present, so this test cannot
	// pass by the endpoint simply having stopped returning snippets.
	if !strings.Contains(body, "roadmap") {
		t.Errorf("response carries no snippet for the meeting alice CAN read: %s", body)
	}
}

// A snippet is a cut of the segment, never the whole transcript.
func TestSearchEndpointBoundsTheSnippet(t *testing.T) {
	index := newTestSearchStore(t)
	long := strings.Repeat("filler words here ", 60) + "the roadmap decision " + strings.Repeat("more filler ", 60)
	seedSearchable(t, index, "JOB1.opus", seg("seg_7", "S1", 1000, 4000, long))
	srv := searchUpstream{catalog: searchTestCatalog, visible: []string{"JOB1.opus"}}.server(t)
	defer srv.Close()

	rec := doSearch(t, searchTestConfig(srv.URL), index, "q=roadmap", "alice")
	var got searchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	snippet := got.Hits[0].Snippet
	if len([]rune(snippet)) > searchSnippetRunes+32 {
		t.Errorf("snippet is %d runes, want it bounded near %d", len([]rune(snippet)), searchSnippetRunes)
	}
	if !strings.Contains(snippet, "roadmap") {
		t.Errorf("snippet %q is not centred on the match", snippet)
	}
}

// A meeting the caller cannot read must not appear, even when it matches.
func TestSearchEndpointHidesInvisibleMeetings(t *testing.T) {
	index := newTestSearchStore(t)
	seedSearchable(t, index, "JOB1.opus", seg("s1", "S1", 1000, 4000, "the acquisition"))
	seedSearchable(t, index, "JOB2.opus", seg("s1", "S9", 1000, 4000, "the acquisition"))
	srv := searchUpstream{catalog: searchTestCatalog, visible: []string{"JOB1.opus"}}.server(t)
	defer srv.Close()

	got := decodeSearch(t, doSearch(t, searchTestConfig(srv.URL), index, "q=acquisition", "alice"))
	if len(got.Hits) != 1 || got.Hits[0].MeetingID != "MEET-1" {
		t.Fatalf("hits = %+v, want only the readable meeting", got.Hits)
	}
	if got.Coverage.Visible != 1 {
		t.Errorf("coverage.visible = %d, want 1", got.Coverage.Visible)
	}
}

// Substrate failures are loud. An agent must never read an outage as "nothing
// matched".
func TestSearchEndpointFailsLoudly(t *testing.T) {
	for _, tc := range []struct {
		name     string
		upstream searchUpstream
		want     int
	}{
		{"scan failed", searchUpstream{catalog: searchTestCatalog, propfindStatus: 500}, http.StatusBadGateway},
		{"no mount", searchUpstream{catalog: searchTestCatalog, propfindStatus: 404}, http.StatusBadGateway},
		{"archive unreachable", searchUpstream{catalogStatus: 500}, http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			index := newTestSearchStore(t)
			srv := tc.upstream.server(t)
			defer srv.Close()

			rec := doSearch(t, searchTestConfig(srv.URL), index, "q=acquisition", "alice")
			if rec.Code != tc.want {
				t.Fatalf("code = %d, want %d", rec.Code, tc.want)
			}
			if strings.Contains(rec.Body.String(), `"hits"`) {
				t.Errorf("an error must not look like a result: %s", rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "not an empty result") {
				t.Errorf("body should say this is not an empty result: %s", rec.Body.String())
			}
		})
	}
}

// A deployment with no index says so, rather than answering "nothing matched".
func TestSearchEndpointReportsAMissingIndex(t *testing.T) {
	srv := searchUpstream{catalog: searchTestCatalog, visible: []string{"JOB1.opus"}}.server(t)
	defer srv.Close()

	rec := doSearch(t, searchTestConfig(srv.URL), nil, "q=acquisition", "alice")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not an empty result") {
		t.Errorf("body should distinguish this from no matches: %s", rec.Body.String())
	}
}

// A broken AppAPI identity is plumbing, not a denial.
func TestSearchEndpointFailsLoudlyWithoutCaller(t *testing.T) {
	index := newTestSearchStore(t)
	srv := searchUpstream{catalog: searchTestCatalog}.server(t)
	defer srv.Close()

	rec := doSearch(t, searchTestConfig(srv.URL), index, "q=acquisition", "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502", rec.Code)
	}
}

// Caller errors are 400 — an agent must tell "fix your query" from "retry".
func TestSearchEndpointRejectsBadQueries(t *testing.T) {
	index := newTestSearchStore(t)
	srv := searchUpstream{catalog: searchTestCatalog, visible: []string{"JOB1.opus"}}.server(t)
	defer srv.Close()
	cfg := searchTestConfig(srv.URL)

	for _, tc := range []struct{ name, query string }{
		{"no q", ""},
		{"blank q", "q=%20%20"},
		{"punctuation only", "q=%21%21%21"},
		{"bad limit", "q=acquisition&limit=nope"},
		{"zero limit", "q=acquisition&limit=0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rec := doSearch(t, cfg, index, tc.query, "alice"); rec.Code != http.StatusBadRequest {
				t.Fatalf("code = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

// A caller who may read nothing gets a truthful empty answer — the one case
// that must stay distinguishable from every failure above.
func TestSearchEndpointEmptyForDeniedCaller(t *testing.T) {
	index := newTestSearchStore(t)
	seedSearchable(t, index, "JOB1.opus", seg("s1", "S1", 1000, 4000, "the acquisition"))
	srv := searchUpstream{catalog: searchTestCatalog, visible: nil}.server(t)
	defer srv.Close()

	rec := doSearch(t, searchTestConfig(srv.URL), index, "q=acquisition", "alice")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	got := decodeSearch(t, rec)
	if len(got.Hits) != 0 || got.Coverage.Visible != 0 {
		t.Fatalf("got %+v, want an empty answer with zero visible", got)
	}
	if !strings.Contains(rec.Body.String(), `"hits":[]`) {
		t.Errorf("hits must serialise as [] not null: %s", rec.Body.String())
	}
}

// Coverage tells the caller how much of what they can read was searched, so a
// partial index cannot pass for a complete answer.
func TestSearchEndpointReportsPartialCoverage(t *testing.T) {
	index := newTestSearchStore(t)
	seedSearchable(t, index, "JOB1.opus", seg("s1", "S1", 1000, 4000, "the acquisition"))
	if err := index.MarkUnavailable(context.Background(), "JOB2.opus", "ingest-failed"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	srv := searchUpstream{catalog: searchTestCatalog, visible: []string{"JOB1.opus", "JOB2.opus"}}.server(t)
	defer srv.Close()

	got := decodeSearch(t, doSearch(t, searchTestConfig(srv.URL), index, "q=acquisition", "alice"))
	if got.Coverage.Visible != 2 || got.Coverage.Searched != 1 {
		t.Fatalf("coverage = %+v, want visible=2 searched=1", got.Coverage)
	}
}

// Under the local sink the route is not served at all.
func TestSearchEndpointNotServedUnderLocalSink(t *testing.T) {
	index := newTestSearchStore(t)
	srv := searchUpstream{catalog: searchTestCatalog}.server(t)
	defer srv.Close()

	cfg := testExAppConfig(srv.URL)
	cfg.PublishSink = publishSinkLocal
	rec := httptest.NewRecorder()
	if cfg.ncFilesProxy(nil, searchDeps{index: index})(rec, callerReq(http.MethodGet, "/published/"+searchURLPath, "alice"), searchURLPath) {
		t.Fatal("the local sink must not serve search")
	}
}

// The query string must never reach the container log.
func TestRequestLoggerRedactsTheSearchQuery(t *testing.T) {
	var logs bytes.Buffer
	handler := requestLogger(log.New(&logs, "", 0), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	handler.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/published/"+searchURLPath+"?q=severance+package+for+Bob", nil))
	if strings.Contains(logs.String(), "severance") {
		t.Fatalf("the search query reached the log: %q", logs.String())
	}
	if !strings.Contains(logs.String(), "/published/"+searchURLPath) {
		t.Errorf("the route should still be identifiable: %q", logs.String())
	}

	// Other routes keep their query, which is what makes a request log useful.
	logs.Reset()
	handler.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/published/"+meetingsListPath+"?from=2026-08-01", nil))
	if !strings.Contains(logs.String(), "from=2026-08-01") {
		t.Errorf("non-search queries should still be logged: %q", logs.String())
	}
}

// search is a sibling of meetings/, never shadowed by a recording.
func TestSearchPathIsAnArchivePath(t *testing.T) {
	if !isPublishedArchivePath(searchURLPath) {
		t.Error("search should be an archive path, not an SPA fallback")
	}
	if isPublishedArchivePath("search-results") {
		t.Error("only the exact search path should match")
	}
}

// The cap is reachable from the wire, and refused politely when nonsense.
func TestSearchEndpointAppliesPerMeetingCap(t *testing.T) {
	index := newTestSearchStore(t)
	var loud []searchTranscriptSegment
	for i := 0; i < 20; i++ {
		at := int64(i) * 60_000
		loud = append(loud, seg(fmt.Sprintf("l%02d", i), "S1", at, at+5_000, "the roadmap again"))
	}
	seedSearchable(t, index, "JOB1.opus", loud...)
	seedSearchable(t, index, "JOB2.opus", seg("q0", "S2", 1000, 4000, "the roadmap once"))
	srv := searchUpstream{catalog: searchTestCatalog, visible: []string{"JOB1.opus", "JOB2.opus"}}.server(t)
	defer srv.Close()

	rec := doSearch(t, searchTestConfig(srv.URL), index, "q=roadmap&perMeeting=2", "alice")
	var got searchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	perMeeting := map[string]int{}
	for _, hit := range got.Hits {
		perMeeting[hit.MeetingID]++
	}
	for id, n := range perMeeting {
		if n > 2 {
			t.Errorf("meeting %s contributed %d hits, want at most 2", id, n)
		}
	}
	if len(perMeeting) < 2 {
		t.Errorf("only %d meeting(s) survived the cap: %+v", len(perMeeting), perMeeting)
	}
}

func TestSearchEndpointRejectsABadPerMeeting(t *testing.T) {
	index := newTestSearchStore(t)
	seedSearchable(t, index, "JOB1.opus", seg("s1", "S1", 1000, 4000, "the roadmap"))
	srv := searchUpstream{catalog: searchTestCatalog, visible: []string{"JOB1.opus"}}.server(t)
	defer srv.Close()

	rec := doSearch(t, searchTestConfig(srv.URL), index, "q=roadmap&perMeeting=0", "alice")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a nonsense cap", rec.Code)
	}
}
