package operator

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeNCFiles is a WebDAV stand-in that remembers what was written, so tests
// can assert on the archive rather than on request counts.
type fakeNCFiles struct {
	mu        sync.Mutex
	files     map[string][]byte
	order     []string
	failPUT   map[string]int
	proppatch int
}

func newFakeNCFiles() *fakeNCFiles {
	return &fakeNCFiles{files: map[string][]byte{}, failPUT: map[string]int{}}
}

func (f *fakeNCFiles) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Everything is addressed under remote.php/dav/files/<user>/...
		idx := strings.Index(r.URL.Path, "/Cassini")
		rel := r.URL.Path
		if idx >= 0 {
			rel = r.URL.Path[idx+1:]
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case "MKCOL":
			w.WriteHeader(http.StatusCreated)
		case "PROPPATCH":
			f.proppatch++
			w.WriteHeader(http.StatusMultiStatus)
		case http.MethodPut:
			if n := f.failPUT[rel]; n != 0 {
				if n > 0 {
					f.failPUT[rel] = n - 1
				}
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(http.StatusInsufficientStorage)
				return
			}
			body, _ := io.ReadAll(r.Body)
			_, existed := f.files[rel]
			f.files[rel] = body
			f.order = append(f.order, rel)
			if existed {
				w.WriteHeader(http.StatusNoContent)
			} else {
				w.WriteHeader(http.StatusCreated)
			}
		case http.MethodGet:
			body, ok := f.files[rel]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(body)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeNCFiles) catalogIDs(t *testing.T) []string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, ok := f.files["Cassini/Recordings/catalog.json"]
	if !ok {
		return nil
	}
	var catalog siteCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatalf("remote catalog is not JSON: %v", err)
	}
	ids := make([]string, 0, len(catalog.Meetings))
	for _, entry := range catalog.Meetings {
		id, err := catalogEntryID(entry)
		if err != nil {
			t.Fatalf("catalogEntryID() error = %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

func (f *fakeNCFiles) has(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.files[name]
	return ok
}

func newNCSink(t *testing.T, ncURL string, accessControl bool) *nextcloudFilesPublishSink {
	t.Helper()
	cfg := testExAppConfig(ncURL)
	cfg.AccessControl = accessControl
	return &nextcloudFilesPublishSink{
		cfg:    cfg,
		logger: log.New(ioDiscard{}, "", 0),
		client: &http.Client{},
	}
}

func deliverToNC(t *testing.T, sink *nextcloudFilesPublishSink, attemptSite, jobID string) (string, error) {
	t.Helper()
	return sink.Deliver(context.Background(), publishDelivery{
		AttemptSitePath: attemptSite,
		JobID:           jobID,
		AttemptNumber:   1,
		PublishedAtUTC:  "2026-06-12T00:00:00Z",
	})
}

func TestNCSinkDeliversTheMeetingAndIndexesItLast(t *testing.T) {
	nc := newFakeNCFiles()
	sink := newNCSink(t, nc.server(t).URL, false)
	attempt := writeAttemptSite(t, filepath.Join(t.TempDir(), "attempt"), "meeting-a")

	location, err := deliverToNC(t, sink, attempt, "meeting-a")
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	if location != ncRecordingsRoot {
		t.Fatalf("location = %q, want %q", location, ncRecordingsRoot)
	}
	if !nc.has("Cassini/Recordings/meetings/meeting-a.opus") {
		t.Fatalf("the .opus never reached Nextcloud")
	}
	if got := nc.catalogIDs(t); len(got) != 1 || got[0] != "meeting-a" {
		t.Fatalf("remote catalog ids = %v, want [meeting-a]", got)
	}
	// The index must be written after the object it names, so a partial
	// delivery never advertises audio that is not there.
	nc.mu.Lock()
	defer nc.mu.Unlock()
	if last := nc.order[len(nc.order)-1]; last != "Cassini/Recordings/catalog.json" {
		t.Fatalf("last write = %q, want the catalog", last)
	}
}

func TestNCSinkUpsertsTheRemoteCatalogRatherThanReplacingIt(t *testing.T) {
	// The attempt site names exactly one meeting. Uploading its catalog
	// verbatim would truncate the whole remote archive to that meeting — this
	// is the reason the sink reads, merges and writes back.
	nc := newFakeNCFiles()
	sink := newNCSink(t, nc.server(t).URL, false)

	for _, id := range []string{"meeting-a", "meeting-b"} {
		attempt := writeAttemptSite(t, filepath.Join(t.TempDir(), id), id)
		if _, err := deliverToNC(t, sink, attempt, id); err != nil {
			t.Fatalf("Deliver(%s) error = %v", id, err)
		}
	}

	got := nc.catalogIDs(t)
	if len(got) != 2 || got[0] != "meeting-a" || got[1] != "meeting-b" {
		t.Fatalf("remote catalog ids = %v, want both meetings", got)
	}
	if !nc.has("Cassini/Recordings/meetings/meeting-a.opus") || !nc.has("Cassini/Recordings/meetings/meeting-b.opus") {
		t.Fatalf("both recordings should be in the archive")
	}

	// A re-publish updates in place rather than duplicating.
	if _, err := deliverToNC(t, sink, writeAttemptSite(t, filepath.Join(t.TempDir(), "again"), "meeting-a"), "meeting-a"); err != nil {
		t.Fatalf("republish Deliver() error = %v", err)
	}
	if got := nc.catalogIDs(t); len(got) != 2 {
		t.Fatalf("remote catalog ids = %v, want no duplicate", got)
	}
}

func TestNCSinkFailsTheDeliveryWhenTheUploadFails(t *testing.T) {
	nc := newFakeNCFiles()
	nc.failPUT["Cassini/Recordings/meetings/meeting-a.opus"] = -1
	sink := newNCSink(t, nc.server(t).URL, false)
	attempt := writeAttemptSite(t, filepath.Join(t.TempDir(), "attempt"), "meeting-a")

	if _, err := deliverToNC(t, sink, attempt, "meeting-a"); err == nil {
		t.Fatalf("expected the delivery to fail when the .opus PUT fails")
	}
	// And critically: no catalog entry advertising a recording that is not there.
	if nc.has("Cassini/Recordings/catalog.json") {
		t.Fatalf("a failed upload must not publish a catalog")
	}
}

func TestNCSinkRefusesWhenTheAttemptSiteLacksTheRecording(t *testing.T) {
	// Without this precondition the upload loop would find nothing to do,
	// complete cleanly, and report a successful delivery of nothing.
	nc := newFakeNCFiles()
	sink := newNCSink(t, nc.server(t).URL, false)
	attempt := writeAttemptSite(t, filepath.Join(t.TempDir(), "attempt"), "meeting-a")
	if err := os.Remove(filepath.Join(attempt, "meetings", "meeting-a.opus")); err != nil {
		t.Fatalf("remove attempt opus: %v", err)
	}

	if _, err := deliverToNC(t, sink, attempt, "meeting-a"); err == nil {
		t.Fatalf("expected the delivery to refuse a missing asset")
	}
	if nc.has("Cassini/Recordings/catalog.json") {
		t.Fatalf("nothing should have been published")
	}
}

func TestNCSinkRefusesAMalformedRemoteCatalog(t *testing.T) {
	nc := newFakeNCFiles()
	nc.files["Cassini/Recordings/catalog.json"] = []byte("{not json")
	sink := newNCSink(t, nc.server(t).URL, false)
	attempt := writeAttemptSite(t, filepath.Join(t.TempDir(), "attempt"), "meeting-a")

	if _, err := deliverToNC(t, sink, attempt, "meeting-a"); err == nil {
		t.Fatalf("expected the delivery to refuse an unreadable remote catalog")
	}
	nc.mu.Lock()
	defer nc.mu.Unlock()
	if string(nc.files["Cassini/Recordings/catalog.json"]) != "{not json" {
		t.Fatalf("the unreadable catalog was overwritten")
	}
}

func TestNCSinkFailsWhenTheAudienceCannotBeWritten(t *testing.T) {
	// D-549: a recording whose audience could not be written is not a
	// successfully published recording.
	nc := newFakeNCFiles()
	sink := newNCSink(t, nc.server(t).URL, true)
	sink.applyAccess = func(context.Context, string) error {
		return errTestAccessApply
	}
	attempt := writeAttemptSite(t, filepath.Join(t.TempDir(), "attempt"), "meeting-a")

	_, err := deliverToNC(t, sink, attempt, "meeting-a")
	if err == nil {
		t.Fatalf("expected the delivery to fail when the ACL write fails")
	}
	if !strings.Contains(err.Error(), "access") {
		t.Fatalf("error = %v, want it to name the access step", err)
	}
}

func TestDefaultPublishSinkFollowsTheDeploymentShape(t *testing.T) {
	// An ExApp whose CASSINI_PUBLISH_SINK never arrives must not quietly keep
	// recordings on its own volume — AppAPI only injects variables the manifest
	// declares, and a dropped declaration is silent.
	if got := defaultPublishSinkFor(ExAppConfig{Active: true}); got != publishSinkNextcloudFiles {
		t.Fatalf("ExApp default = %q, want %q", got, publishSinkNextcloudFiles)
	}
	if got := defaultPublishSinkFor(ExAppConfig{}); got != publishSinkLocal {
		t.Fatalf("standalone default = %q, want %q", got, publishSinkLocal)
	}
}

func TestNextcloudSinkRefusesToStartWithoutAUsableTarget(t *testing.T) {
	// Selecting nextcloud-files without the credentials to reach Nextcloud is a
	// misconfiguration that must surface at startup, not as a failed publish
	// after the first recording.
	_, err := newPublishSinkFor(publishSinkNextcloudFiles, Config{}, ExAppConfig{Active: true}, nil, log.New(ioDiscard{}, "", 0))
	if err == nil {
		t.Fatalf("expected an error when the ExApp target is incomplete")
	}
	if !strings.Contains(err.Error(), "NEXTCLOUD_URL") || !strings.Contains(err.Error(), publishSinkLocal) {
		t.Fatalf("error = %v, want it to name the missing variable and the escape hatch", err)
	}
}

type testAccessErr string

func (e testAccessErr) Error() string { return string(e) }

const errTestAccessApply = testAccessErr("acl opus: PROPPATCH -> 500")
