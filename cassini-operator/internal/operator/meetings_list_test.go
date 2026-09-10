package operator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// meetingsListConfig is a proxy config for an ExApp publishing to Nextcloud
// Files — the only deployment where the list endpoint is served.
func meetingsListConfig(url string) ExAppConfig {
	cfg := testExAppConfig(url)
	cfg.PublishSink = publishSinkNextcloudFiles
	return cfg
}

// meetingsListCatalog is the authoritative archive used across these tests:
// four meetings, three dated and one whose dateLabel is a raw filename slug
// (the D-588 archive defect), spread over two rooms.
const meetingsListCatalog = `{"version":"cassini.viewer.catalog.v1","generatedAt":"2026-09-01T00:00:00Z","meetings":[` +
	`{"id":"a","title":"Standup","dateLabel":"2026-08-01 09:00","audioPath":"./meetings/JOB1.opus","roomId":"rm_aaaaaaaaaaaaaaaa","speakerCount":3},` +
	`{"id":"b","title":"Retro","dateLabel":"2026-08-15","audioPath":"./meetings/JOB2.opus","roomId":"rm_bbbbbbbbbbbbbbbb"},` +
	`{"id":"c","title":"Planning","dateLabel":"2026-08-31 23:30","audioPath":"./meetings/JOB3.opus","roomId":"rm_aaaaaaaaaaaaaaaa"},` +
	`{"id":"d","title":"Daily","dateLabel":"daily-standup-raw-slug","audioPath":"./meetings/JOB4.opus","roomId":"rm_aaaaaaaaaaaaaaaa"}]}`

// meetingsListUpstream stands in for Nextcloud. visible names the .opus files
// the calling account may read; propfindStatus and catalogStatus force the
// substrate failures the endpoint has to report loudly.
type meetingsListUpstream struct {
	visible        []string
	catalogStatus  int
	propfindStatus int
	catalogBody    string
}

func (u meetingsListUpstream) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/catalog.json"):
			if u.catalogStatus != 0 && u.catalogStatus != http.StatusOK {
				w.WriteHeader(u.catalogStatus)
				return
			}
			body := u.catalogBody
			if body == "" {
				body = meetingsListCatalog
			}
			_, _ = w.Write([]byte(body))
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
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// getMeetingsList drives the proxy the way the AppAPI route does.
func getMeetingsList(t *testing.T, cfg ExAppConfig, query, caller string) *httptest.ResponseRecorder {
	t.Helper()
	target := "/published/" + meetingsListPath
	if query != "" {
		target += "?" + query
	}
	rec := httptest.NewRecorder()
	proxy := cfg.ncFilesProxy(nil)
	if proxy == nil {
		t.Fatal("expected a proxy for an AppAPI config")
	}
	if !proxy(rec, callerReq(http.MethodGet, target, caller), meetingsListPath) {
		t.Fatal("proxy declined the meetings list request")
	}
	return rec
}

func decodeMeetingsList(t *testing.T, rec *httptest.ResponseRecorder) meetingsListResponse {
	t.Helper()
	var got meetingsListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, rec.Body.String())
	}
	return got
}

func listedIDs(t *testing.T, resp meetingsListResponse) []string {
	t.Helper()
	ids := make([]string, 0, len(resp.Meetings))
	for _, raw := range resp.Meetings {
		var probe struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			t.Fatalf("decode entry: %v", err)
		}
		ids = append(ids, probe.ID)
	}
	return ids
}

// The endpoint lists only the meetings the caller may read, and a date range
// narrows within that set — never outside it.
func TestMeetingsListFiltersByDateWithinVisibleSet(t *testing.T) {
	// alice may read JOB1, JOB2 and JOB4 — never JOB3, which is inside the
	// requested range and must still not appear.
	srv := meetingsListUpstream{visible: []string{"JOB1.opus", "JOB2.opus", "JOB4.opus"}}.server(t)
	defer srv.Close()

	rec := getMeetingsList(t, meetingsListConfig(srv.URL), "from=2026-08-01&to=2026-08-31", "alice")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeMeetingsList(t, rec)
	ids := listedIDs(t, got)
	if strings.Join(ids, ",") != "a,b" {
		t.Fatalf("ids = %v, want [a b] — c is invisible, d is undated", ids)
	}
	if got.Version != "cassini.viewer.catalog.v1" {
		t.Fatalf("version = %q, want the catalog envelope version", got.Version)
	}
	if got.Excluded == nil || got.Excluded.Undated != 1 {
		t.Fatalf("excluded = %+v, want undated=1 for the raw-slug dateLabel", got.Excluded)
	}
	if got.Filter == nil || got.Filter.From != "2026-08-01 00:00:00" || got.Filter.To != "2026-08-31 23:59:59" {
		t.Fatalf("filter echo = %+v, want the bare `to` date to cover the whole day", got.Filter)
	}
}

// A meeting on the last day of the range is included: a bare `to` means the end
// of that day, not midnight at its start.
func TestMeetingsListToDateCoversWholeDay(t *testing.T) {
	srv := meetingsListUpstream{visible: []string{"JOB1.opus", "JOB2.opus", "JOB3.opus"}}.server(t)
	defer srv.Close()

	rec := getMeetingsList(t, meetingsListConfig(srv.URL), "from=2026-08-31&to=2026-08-31", "alice")
	ids := listedIDs(t, decodeMeetingsList(t, rec))
	if strings.Join(ids, ",") != "c" {
		t.Fatalf("ids = %v, want [c] — 23:30 on the `to` day must be inside the range", ids)
	}
}

// Entries are re-emitted verbatim: a field this endpoint has never heard of
// survives, because the exporter owns the entry shape.
func TestMeetingsListPreservesUnknownEntryFields(t *testing.T) {
	srv := meetingsListUpstream{visible: []string{"JOB1.opus"}}.server(t)
	defer srv.Close()

	rec := getMeetingsList(t, meetingsListConfig(srv.URL), "", "alice")
	if !strings.Contains(rec.Body.String(), `"speakerCount":3`) {
		t.Fatalf("unknown entry field was dropped: %s", rec.Body.String())
	}
	got := decodeMeetingsList(t, rec)
	if got.Filter != nil || got.Excluded != nil {
		t.Fatalf("an unfiltered listing must not carry filter/excluded: %+v %+v", got.Filter, got.Excluded)
	}
}

func TestMeetingsListFiltersByRoom(t *testing.T) {
	srv := meetingsListUpstream{visible: []string{"JOB1.opus", "JOB2.opus", "JOB3.opus", "JOB4.opus"}}.server(t)
	defer srv.Close()

	rec := getMeetingsList(t, meetingsListConfig(srv.URL), "room=rm_bbbbbbbbbbbbbbbb", "alice")
	ids := listedIDs(t, decodeMeetingsList(t, rec))
	if strings.Join(ids, ",") != "b" {
		t.Fatalf("ids = %v, want [b]", ids)
	}
}

// THE security property. A failed per-caller scan means "which meetings this
// caller may read is unknown" — it must never be served as an empty list, which
// an agent would read as a truthful "you have no meetings".
func TestMeetingsListFailsLoudlyOnScanError(t *testing.T) {
	srv := meetingsListUpstream{propfindStatus: http.StatusInternalServerError}.server(t)
	defer srv.Close()

	rec := getMeetingsList(t, meetingsListConfig(srv.URL), "from=2026-08-01", "alice")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502 — a scan failure must not read as an empty list", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `"meetings"`) {
		t.Fatalf("error body must not look like a catalog: %s", rec.Body.String())
	}
}

// A caller with no recordings mount is a substrate fault, not a denial.
func TestMeetingsListFailsLoudlyWithoutMount(t *testing.T) {
	srv := meetingsListUpstream{propfindStatus: http.StatusNotFound}.server(t)
	defer srv.Close()

	rec := getMeetingsList(t, meetingsListConfig(srv.URL), "", "alice")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502", rec.Code)
	}
}

func TestMeetingsListFailsLoudlyWhenArchiveUnreachable(t *testing.T) {
	srv := meetingsListUpstream{catalogStatus: http.StatusInternalServerError}.server(t)
	defer srv.Close()

	rec := getMeetingsList(t, meetingsListConfig(srv.URL), "", "alice")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502", rec.Code)
	}
}

// An absent catalog is genuinely "nothing has been published", so it is the one
// substrate condition that IS an empty 200.
func TestMeetingsListEmptyWhenNothingPublished(t *testing.T) {
	srv := meetingsListUpstream{catalogStatus: http.StatusNotFound}.server(t)
	defer srv.Close()

	rec := getMeetingsList(t, meetingsListConfig(srv.URL), "", "alice")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	got := decodeMeetingsList(t, rec)
	if len(got.Meetings) != 0 {
		t.Fatalf("meetings = %v, want empty", got.Meetings)
	}
	if !strings.Contains(rec.Body.String(), `"meetings":[]`) {
		t.Fatalf("meetings must serialise as [] not null: %s", rec.Body.String())
	}
}

// A caller who may read nothing gets a truthful, genuinely empty 200 — this is
// the case that MUST stay distinguishable from every failure above.
func TestMeetingsListEmptyForDeniedCaller(t *testing.T) {
	srv := meetingsListUpstream{visible: nil}.server(t)
	defer srv.Close()

	rec := getMeetingsList(t, meetingsListConfig(srv.URL), "", "alice")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if len(decodeMeetingsList(t, rec).Meetings) != 0 {
		t.Fatal("a denied caller must see no meetings")
	}
}

// A broken AppAPI identity is plumbing. Answering it with an empty catalog would
// claim the caller may read nothing; answering 404 would be phrased downstream
// as a denial. Both are lies about permissions.
func TestMeetingsListFailsLoudlyWithoutCaller(t *testing.T) {
	srv := meetingsListUpstream{}.server(t)
	defer srv.Close()

	rec := getMeetingsList(t, meetingsListConfig(srv.URL), "", "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502 — missing identity is not a denial", rec.Code)
	}
}

// Caller errors are 400, not 502: an agent must be able to tell "fix your query"
// from "retry later".
func TestMeetingsListRejectsBadQueries(t *testing.T) {
	srv := meetingsListUpstream{}.server(t)
	defer srv.Close()
	cfg := meetingsListConfig(srv.URL)

	for _, tc := range []struct{ name, query, want string }{
		{"inverted range", "from=2026-08-31&to=2026-08-01", "swap"},
		{"malformed date", "from=31%2F08%2F2026", "not a date"},
		{"timezone offered", "from=2026-08-01T09:00:00Z", "not a date"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := getMeetingsList(t, cfg, tc.query, "alice")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("code = %d, want 400", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("body %q should explain the problem (%q)", rec.Body.String(), tc.want)
			}
		})
	}
}

// A bad query is rejected BEFORE any call to Nextcloud: making a caller wait on
// two round trips to be told they typed a date wrong is pure cost.
func TestMeetingsListValidatesBeforeCallingNextcloud(t *testing.T) {
	var upstreamCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rec := getMeetingsList(t, meetingsListConfig(srv.URL), "from=nonsense", "alice")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream called %d times, want 0", upstreamCalls)
	}
}

// Under the local sink the Nextcloud archive is not the one being written, so
// the endpoint must decline rather than answer from the wrong source. Declining
// falls through to the local file server, which has no such file.
func TestMeetingsListNotServedUnderLocalSink(t *testing.T) {
	srv := meetingsListUpstream{}.server(t)
	defer srv.Close()

	cfg := testExAppConfig(srv.URL)
	cfg.PublishSink = publishSinkLocal
	proxy := cfg.ncFilesProxy(nil)
	rec := httptest.NewRecorder()
	if proxy(rec, callerReq(http.MethodGet, "/published/"+meetingsListPath, "alice"), meetingsListPath) {
		t.Fatal("the local sink must not serve the meetings list from Nextcloud")
	}
}

// The endpoint is a sibling of meetings/, never shadowed by a recording.
func TestPublishedArchivePathClassification(t *testing.T) {
	for _, p := range []string{"catalog.json", meetingsListPath, "meetings/JOB1.opus"} {
		if !isPublishedArchivePath(p) {
			t.Errorf("%q should be an archive path", p)
		}
	}
	for _, p := range []string{"index.html", "assets/app.js", "meetings-list-extra"} {
		if isPublishedArchivePath(p) {
			t.Errorf("%q should not be an archive path", p)
		}
	}
}

// The sink gate is checked before the caller guard, so a deployment that does
// not serve this route answers identically no matter who asks.
func TestMeetingsListNotServedUnderLocalSinkWithoutCaller(t *testing.T) {
	srv := meetingsListUpstream{}.server(t)
	defer srv.Close()

	cfg := testExAppConfig(srv.URL)
	cfg.PublishSink = publishSinkLocal
	rec := httptest.NewRecorder()
	if cfg.ncFilesProxy(nil)(rec, callerReq(http.MethodGet, "/published/"+meetingsListPath, ""), meetingsListPath) {
		t.Fatal("local sink must decline the list route for an unidentified caller too")
	}
}
