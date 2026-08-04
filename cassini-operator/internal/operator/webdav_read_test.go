package operator

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cassini-operator/internal/operator/appapi"
)

func aclProxyConfig(url string) ExAppConfig {
	cfg := testExAppConfig(url)
	return cfg
}

func callerReq(method, target, caller string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	if caller != "" {
		req = req.WithContext(appapi.WithUserID(req.Context(), caller))
	}
	return req
}

// The authoritative catalog lists two meetings; alice can see only JOB1, so the
// per-caller catalog must contain only JOB1 (and preserve unknown fields).
func TestNCFilesProxyServesPerCallerCatalog(t *testing.T) {
	catalog := `{"version":"cassini.viewer.catalog.v1","generatedAt":"2026-04-29T00:00:00Z","meetings":[` +
		`{"id":"a","title":"One","dateLabel":"d","audioPath":"./meetings/JOB1.opus",` +
		`"speakerCount":3,"segmentCount":12,"digestDurationMs":45000},` +
		`{"id":"b","title":"Two","dateLabel":"d","audioPath":"./meetings/JOB2.opus"}]}`
	var catalogGets, propfinds int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/catalog.json"):
			catalogGets++
			// Authoritative fetch is as the owner.
			if !strings.Contains(r.URL.Path, "/files/"+ncRecordingsOwner+"/") {
				t.Errorf("catalog fetched as %s, want owner", r.URL.Path)
			}
			_, _ = w.Write([]byte(catalog))
		case r.Method == "PROPFIND":
			propfinds++
			// Scan is as the caller alice.
			if !strings.Contains(r.URL.Path, "/files/alice/") {
				t.Errorf("scan path %s, want caller alice", r.URL.Path)
			}
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = w.Write([]byte(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:">` +
				`<d:response><d:href>/remote.php/dav/files/alice/Cassini/Recordings/meetings/</d:href></d:response>` +
				`<d:response><d:href>/remote.php/dav/files/alice/Cassini/Recordings/meetings/JOB1.opus</d:href></d:response>` +
				`<d:response><d:href>/remote.php/dav/files/alice/Cassini/Recordings/meetings/README.txt</d:href></d:response>` +
				`</d:multistatus>`))
		default:
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	proxy := aclProxyConfig(srv.URL).ncFilesProxy(nil)
	rec := httptest.NewRecorder()
	if !proxy(rec, callerReq(http.MethodGet, "/published/catalog.json", "alice"), "catalog.json") {
		t.Fatal("proxy should serve the per-caller catalog")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "JOB1.opus") || strings.Contains(body, "JOB2.opus") {
		t.Errorf("filtered catalog should contain only JOB1: %s", body)
	}
	if !strings.Contains(body, `"generatedAt":"2026-04-29T00:00:00Z"`) {
		t.Errorf("unknown top-level fields must be preserved: %s", body)
	}
	for _, want := range []string{`"speakerCount":3`, `"segmentCount":12`, `"digestDurationMs":45000`} {
		if !strings.Contains(body, want) {
			t.Errorf("catalog list metadata must be preserved (%s): %s", want, body)
		}
	}
	if catalogGets != 1 || propfinds != 1 {
		t.Errorf("upstream calls: catalog GET=%d PROPFIND=%d, want one each", catalogGets, propfinds)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("catalog Cache-Control = %q, want no-store", rec.Header().Get("Cache-Control"))
	}
}

func TestNCFilesProxyPlaybackActsAsCaller(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantAuth := base64.StdEncoding.EncodeToString([]byte("alice:sekret"))
		if r.Header.Get("AUTHORIZATION-APP-API") != wantAuth {
			t.Errorf("playback auth = %q, want caller alice", r.Header.Get("AUTHORIZATION-APP-API"))
		}
		if strings.HasSuffix(r.URL.Path, "GRANTED.opus") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("OPUSBYTES"))
			return
		}
		w.WriteHeader(http.StatusNotFound) // alice cannot read this one
	}))
	defer srv.Close()
	proxy := aclProxyConfig(srv.URL).ncFilesProxy(nil)

	rec := httptest.NewRecorder()
	if !proxy(rec, callerReq(http.MethodGet, "/published/meetings/GRANTED.opus", "alice"), "meetings/GRANTED.opus") {
		t.Fatal("proxy should serve")
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "OPUSBYTES" {
		t.Errorf("granted playback: code=%d body=%q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	proxy(rec, callerReq(http.MethodGet, "/published/meetings/DENIED.opus", "alice"), "meetings/DENIED.opus")
	if rec.Code != http.StatusNotFound {
		t.Errorf("denied playback code = %d, want 404", rec.Code)
	}
}

func TestNCFilesProxyFailsClosedWithoutCaller(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("upstream must not be called without a caller: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()
	proxy := aclProxyConfig(srv.URL).ncFilesProxy(nil)

	rec := httptest.NewRecorder()
	proxy(rec, callerReq(http.MethodGet, "/published/catalog.json", ""), "catalog.json")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"meetings":[]`) {
		t.Errorf("no-caller catalog should be empty-but-valid: code=%d body=%q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	proxy(rec, callerReq(http.MethodGet, "/published/meetings/x.opus", ""), "meetings/x.opus")
	if rec.Code != http.StatusNotFound {
		t.Errorf("no-caller playback code = %d, want 404", rec.Code)
	}
}

// Security-critical: a per-caller scan error must yield an EMPTY catalog, never
// the authoritative (unfiltered) one — otherwise every meeting leaks to every
// caller.
func TestNCFilesProxyFailsClosedOnScanError(t *testing.T) {
	catalog := `{"version":"cassini.viewer.catalog.v1","meetings":[` +
		`{"id":"a","title":"One","dateLabel":"d","audioPath":"./meetings/JOB1.opus"},` +
		`{"id":"b","title":"Two","dateLabel":"d","audioPath":"./meetings/JOB2.opus"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/catalog.json") {
			_, _ = w.Write([]byte(catalog))
			return
		}
		// The caller's PROPFIND scan fails.
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	proxy := aclProxyConfig(srv.URL).ncFilesProxy(nil)
	rec := httptest.NewRecorder()
	proxy(rec, callerReq(http.MethodGet, "/published/catalog.json", "alice"), "catalog.json")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (fail-closed empty catalog)", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "JOB1") || strings.Contains(body, "JOB2") {
		t.Errorf("scan failure must NOT leak the authoritative catalog: %s", body)
	}
	if !strings.Contains(body, `"meetings":[]`) {
		t.Errorf("want empty meetings list, got %s", body)
	}
}

func TestNCFilesProxyBadGatewayOnCatalogFetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // authoritative catalog fetch fails
	}))
	defer srv.Close()
	proxy := aclProxyConfig(srv.URL).ncFilesProxy(nil)
	rec := httptest.NewRecorder()
	proxy(rec, callerReq(http.MethodGet, "/published/catalog.json", "alice"), "catalog.json")
	if rec.Code != http.StatusBadGateway {
		t.Errorf("catalog fetch error code = %d, want 502", rec.Code)
	}
}

func TestFilterCatalogPreservesFieldsAndFilters(t *testing.T) {
	raw := []byte(`{"version":"cassini.viewer.catalog.v1","extra":"keep",` +
		`"meetings":[` +
		`{"id":"a","title":"One","audioPath":"./meetings/A.opus","weird":42},` +
		`{"id":"b","title":"Two","audioPath":"./meetings/B.opus"}]}`)
	out, err := filterCatalog(raw, func(base string) bool { return base == "A.opus" })
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "A.opus") || strings.Contains(s, "B.opus") {
		t.Errorf("filter kept wrong meetings: %s", s)
	}
	if !strings.Contains(s, `"extra":"keep"`) || !strings.Contains(s, `"weird":42`) {
		t.Errorf("unknown fields not preserved: %s", s)
	}
}
