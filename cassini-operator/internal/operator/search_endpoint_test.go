package operator

import (
	"bytes"
	"context"
	"encoding/json"
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
	proxy := cfg.ncFilesProxy(nil, index)
	if proxy == nil {
		t.Fatal("expected a proxy for an AppAPI config")
	}
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

// The response carries no transcript text anywhere.
func TestSearchEndpointNeverReturnsText(t *testing.T) {
	index := newTestSearchStore(t)
	seedSearchable(t, index, "JOB1.opus", seg("seg_7", "S1", 1000, 4000, "the severance package for Bob"))
	srv := searchUpstream{catalog: searchTestCatalog, visible: []string{"JOB1.opus"}}.server(t)
	defer srv.Close()

	rec := doSearch(t, searchTestConfig(srv.URL), index, "q=severance", "alice")
	for _, leaked := range []string{"severance package", "for Bob", "the severance"} {
		if strings.Contains(rec.Body.String(), leaked) {
			t.Errorf("response leaked transcript text %q: %s", leaked, rec.Body.String())
		}
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
	if cfg.ncFilesProxy(nil, index)(rec, callerReq(http.MethodGet, "/published/"+searchURLPath, "alice"), searchURLPath) {
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
