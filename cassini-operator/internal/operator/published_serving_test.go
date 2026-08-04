package operator

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Where meetings are read from must mirror where they were written (D-550).
// Under the nextcloud-files sink nothing is ever written to the local site
// root, so serving it can only ever hand back pre-sink leftovers. These pin
// that the local surface is genuinely unreachable rather than merely unused —
// publishedHandler had no test at all before this.

// stubProxy answers archive paths, recording what it was asked for.
func stubProxy(t *testing.T, body string) (ncFilesProxyFunc, *[]string) {
	t.Helper()
	var seen []string
	return func(w http.ResponseWriter, _ *http.Request, relPath string) bool {
		seen = append(seen, relPath)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
		return true
	}, &seen
}

func TestPublishedHandlerServesArchiveFromNextcloudAndNothingFromDisk(t *testing.T) {
	// The local dir is populated to prove it is not consulted — a stale site
	// root left behind by a pre-sink deployment must not surface.
	stale := t.TempDir()
	writeFile(t, filepath.Join(stale, "catalog.json"), `{"stale":true}`)
	writeFile(t, filepath.Join(stale, "cassini.json"), `{"kind":"site","meeting_count":42}`)
	proxy, seen := stubProxy(t, `{"version":"cassini.viewer.catalog.v1","meetings":[]}`)

	// dir == "" is what installRoutes passes under the nextcloud-files sink.
	h := publishedHandler("", "/published", log.New(&bytes.Buffer{}, "", 0), proxy)

	r := httptest.NewRequest(http.MethodGet, "/published/catalog.json", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "cassini.viewer.catalog.v1") {
		t.Fatalf("archive path not served from Nextcloud: %d %s", w.Code, w.Body.String())
	}
	if len(*seen) != 1 || (*seen)[0] != "catalog.json" {
		t.Fatalf("proxy saw %v, want [catalog.json]", *seen)
	}

	// cassini.json is not an archive path, so the proxy does not handle it. It
	// carries meeting counts and job ids, and used to be readable by any
	// logged-in user straight off the ExApp volume.
	r = httptest.NewRequest(http.MethodGet, "/published/cassini.json", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cassini.json got %d, want 404 — the site manifest must not be served", w.Code)
	}
	if strings.Contains(w.Body.String(), "meeting_count") {
		t.Fatalf("the site manifest leaked: %s", w.Body.String())
	}
}

func TestPublishedHandlerStillServesLocallyUnderTheLocalSink(t *testing.T) {
	// No regression for standalone/dev, where the on-disk site is the source.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "catalog.json"), `{"version":"cassini.viewer.catalog.v1","meetings":[]}`)
	h := publishedHandler(dir, "/published", log.New(&bytes.Buffer{}, "", 0), nil)

	r := httptest.NewRequest(http.MethodGet, "/published/catalog.json", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "cassini.viewer.catalog.v1") {
		t.Fatalf("local sink must still serve from disk: %d %s", w.Code, w.Body.String())
	}
}

func TestViewerHandlerNeverAnswersAnArchiveMissWithTheSPA(t *testing.T) {
	// A JSON or audio fetch answered with index.html is worse than a miss: the
	// viewer parses HTML as a catalog and reports something unintelligible.
	viewerDir := t.TempDir()
	writeFile(t, filepath.Join(viewerDir, "index.html"), "<html>viewer</html>")
	missingProxy := ncFilesProxyFunc(func(w http.ResponseWriter, r *http.Request, _ string) bool {
		http.NotFound(w, r)
		return true
	})

	h := viewerHandler(viewerDir, "", "/viewer", log.New(&bytes.Buffer{}, "", 0), missingProxy)

	for _, path := range []string{"/viewer/catalog.json", "/viewer/meetings/absent.opus"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s got %d, want 404", path, w.Code)
		}
		if strings.Contains(w.Body.String(), "<html>") {
			t.Fatalf("%s fell through to the SPA: %s", path, w.Body.String())
		}
	}

	// The SPA itself is still served, which is the whole point of the handler.
	r := httptest.NewRequest(http.MethodGet, "/viewer/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<html>viewer</html>") {
		t.Fatalf("SPA not served: %d %s", w.Code, w.Body.String())
	}
}

func TestViewerHandlerWithNoArchiveAtAllStillServesTheSPA(t *testing.T) {
	viewerDir := t.TempDir()
	writeFile(t, filepath.Join(viewerDir, "index.html"), "<html>viewer</html>")
	h := viewerHandler(viewerDir, "", "/viewer", log.New(&bytes.Buffer{}, "", 0), nil)

	r := httptest.NewRequest(http.MethodGet, "/viewer/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
}

func TestNCProxyServesADeniedReadAsNotFound(t *testing.T) {
	// The access model's guarantee is that a recording you may not read never
	// reveals that it exists. A 403 would leak exactly that; a 502 would blame
	// an outage that is not happening.
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		defer srv.Close()

		var logs bytes.Buffer
		cfg := testExAppConfig(srv.URL)
		proxy := cfg.ncFilesProxy(log.New(&logs, "", 0))
		if proxy == nil {
			t.Fatalf("expected a proxy for an AppAPI-active config")
		}

		// Every read is served as the caller now (D-554), so the request needs
		// an identity to get past the fail-closed guard and reach upstream.
		r := callerReq(http.MethodGet, "/published/meetings/secret.opus", "alice")
		w := httptest.NewRecorder()
		if !proxy(w, r, "meetings/secret.opus") {
			t.Fatalf("proxy did not handle the request")
		}
		if w.Code != http.StatusNotFound {
			t.Fatalf("upstream %d served as %d, want 404", status, w.Code)
		}
		// A denial is still distinguishable in the log, so an operator can tell
		// a permissions problem from a genuine miss.
		if status != http.StatusNotFound && !strings.Contains(logs.String(), "denied") {
			t.Fatalf("upstream %d was not logged as a denial: %s", status, logs.String())
		}
	}
}

func TestNCProxyStillReportsAnUpstreamOutage(t *testing.T) {
	// The flip side: a real failure must not be disguised as a miss, or the
	// viewer shows an empty archive during an outage.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := testExAppConfig(srv.URL)
	proxy := cfg.ncFilesProxy(log.New(&bytes.Buffer{}, "", 0))
	r := callerReq(http.MethodGet, "/published/meetings/x.opus", "alice")
	w := httptest.NewRecorder()
	proxy(w, r, "meetings/x.opus")
	if w.Code != http.StatusBadGateway {
		t.Fatalf("upstream 500 served as %d, want 502", w.Code)
	}
}

func TestPublishRemovesTheStagingSiteOnlyAfterASuccessfulDelivery(t *testing.T) {
	// The attempt site is a full copy of the recording. Keeping it after
	// delivery means the ExApp retains every meeting it has ever published, on
	// its own volume, outside the access model (D-550).
	for _, tc := range []struct {
		name       string
		sink       publishSink
		wantExists bool
	}{
		{name: "delivered", sink: &okPublishSink{location: "somewhere://else"}, wantExists: false},
		// A failed publish keeps it: the failure detail is read from its
		// manifest, and a rerun may want to inspect it.
		{name: "failed", sink: errPublishSink{err: errTestDeliver}, wantExists: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, cleanup := newBarePublishRuntime(t, log.New(&bytes.Buffer{}, "", 0))
			defer cleanup()

			attemptSite := filepath.Join(t.TempDir(), "attempt.site")
			writeFile(t, filepath.Join(attemptSite, "catalog.json"), `{"meetings":[]}`)
			writeFile(t, filepath.Join(attemptSite, "meetings", "m.opus"), "opus-bytes")
			rt.publishJobFn = func(context.Context, publishTask) (string, error) { return attemptSite, nil }
			rt.publishSink = tc.sink

			insertJob(t, rt.store.db, "stage-"+tc.name, "2026-06-12T10:00:00Z")
			if err := rt.store.MarkPublishQueued(context.Background(), "stage-"+tc.name, "/tmp/m", "/tmp/m", nowUTCString()); err != nil {
				t.Fatalf("MarkPublishQueued() error = %v", err)
			}
			rt.runPublishJob(publishTask{JobID: "stage-" + tc.name, AttemptNumber: 1})

			_, err := os.Stat(attemptSite)
			exists := err == nil
			if exists != tc.wantExists {
				t.Fatalf("attempt site exists = %v, want %v", exists, tc.wantExists)
			}
		})
	}
}

const errTestDeliver = testAccessErr("destination unreachable")
