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
//
// `order` is the PUT sequence — what the archive gained, and in which order.
// `ops` is every mutating request including PROPPATCH, which is what the
// access-control assertions need: a recording's deny has to be recorded before
// the catalog that advertises it, and "an ACL was sent" is not the same claim
// as "the ACL denied the right principal".
type fakeNCFiles struct {
	mu      sync.Mutex
	files   map[string][]byte
	order   []string
	ops     []ncFilesOp
	failPUT map[string]int
}

// ncFilesOp is one mutating request the fake saw.
type ncFilesOp struct {
	method string
	path   string
	body   string
}

func newFakeNCFiles() *fakeNCFiles {
	return &fakeNCFiles{files: map[string][]byte{}, failPUT: map[string]int{}}
}

// aclBodyFor returns the single ACL body PROPPATCHed onto path, failing unless
// exactly one was sent — a second write would mean a later pass silently
// replaced the protection the first one established.
func (f *fakeNCFiles) aclBodyFor(t *testing.T, path string) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var bodies []string
	for _, op := range f.ops {
		if op.method == "PROPPATCH" && op.path == path {
			bodies = append(bodies, op.body)
		}
	}
	if len(bodies) != 1 {
		t.Fatalf("PROPPATCH count for %s = %d, want exactly 1", path, len(bodies))
	}
	return bodies[0]
}

// indexOfOp reports where a request sits in the mutation sequence, or -1.
func (f *fakeNCFiles) indexOfOp(method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, op := range f.ops {
		if op.method == method && op.path == path {
			return i
		}
	}
	return -1
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
			body, _ := io.ReadAll(r.Body)
			f.ops = append(f.ops, ncFilesOp{method: "PROPPATCH", path: rel, body: string(body)})
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
			f.ops = append(f.ops, ncFilesOp{method: http.MethodPut, path: rel, body: string(body)})
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

func newNCSink(t *testing.T, ncURL string) *nextcloudFilesPublishSink {
	t.Helper()
	// Deliver refuses to write unless provisioning recorded a usable substrate
	// (D-585). Every test below is about a deployment whose substrate IS there,
	// so seed it; the refusal itself has its own test.
	ncAccessSubstrate.reset()
	ncAccessSubstrate.markApplicable()
	ncAccessSubstrate.succeed()
	t.Cleanup(ncAccessSubstrate.reset)
	return &nextcloudFilesPublishSink{
		cfg:    testExAppConfig(ncURL),
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
	sink := newNCSink(t, nc.server(t).URL)
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

// The delivery's access-control contract, asserted on the wire bodies rather
// than on the fact that some PROPPATCH happened.
//
// Both leaves the sink creates inherit the container's read grant to the
// virtual all-users group, so both must override it, and the recording's
// override has to land BEFORE the catalog names the file — otherwise there is a
// window in which the archive advertises audio every logged-in account can
// read. This coverage used to live on the whole-archive mirror, which is the
// only place the catalog protection body was ever checked; the mirror is gone
// (D-613) and the assertions belong here, on the path that actually runs.
func TestNCSinkProtectsWhatItWritesBeforeAdvertisingIt(t *testing.T) {
	nc := newFakeNCFiles()
	sink := newNCSink(t, nc.server(t).URL)
	attempt := writeAttemptSite(t, filepath.Join(t.TempDir(), "attempt"), "meeting-a")

	if _, err := deliverToNC(t, sink, attempt, "meeting-a"); err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}

	const (
		opusPath    = "Cassini/Recordings/meetings/meeting-a.opus"
		catalogPath = "Cassini/Recordings/catalog.json"
		denyAll     = "<nc:acl-permissions>0</nc:acl-permissions>"
		grantAll    = "<nc:acl-permissions>31</nc:acl-permissions>"
	)

	// A newly created recording is owner-only: the all-users group is denied
	// every bit, so it cannot inherit the container's traversal grant.
	opusACL := nc.aclBodyFor(t, opusPath)
	for _, want := range []string{
		"<nc:acl-mapping-id>" + ncRecordingsEveryoneGroup + "</nc:acl-mapping-id>",
		denyAll,
		"<nc:acl-mapping-id>" + ncRecordingsOwner + "</nc:acl-mapping-id>",
		grantAll,
	} {
		if !strings.Contains(opusACL, want) {
			t.Errorf("new recording ACL missing %q: %s", want, opusACL)
		}
	}

	// The authoritative catalog stays private to the owner unconditionally: the
	// operator reads it as the owner and serves each caller a filtered view, so
	// no account has any reason to read the unfiltered index directly.
	catalogACL := nc.aclBodyFor(t, catalogPath)
	for _, want := range []string{
		"<nc:acl-mapping-id>" + ncRecordingsEveryoneGroup + "</nc:acl-mapping-id>",
		denyAll,
		"<nc:acl-mapping-id>" + ncRecordingsOwner + "</nc:acl-mapping-id>",
		grantAll,
	} {
		if !strings.Contains(catalogACL, want) {
			t.Errorf("catalog ACL missing %q: %s", want, catalogACL)
		}
	}

	// Ordering: deny the recording, then advertise it.
	opusDeny := nc.indexOfOp("PROPPATCH", opusPath)
	catalogWrite := nc.indexOfOp(http.MethodPut, catalogPath)
	if opusDeny < 0 || catalogWrite < 0 {
		t.Fatalf("missing operations: opus deny=%d catalog PUT=%d", opusDeny, catalogWrite)
	}
	if opusDeny > catalogWrite {
		t.Fatalf("recording was advertised at op %d before it was protected at op %d", catalogWrite, opusDeny)
	}
}

func TestNCSinkUpsertsTheRemoteCatalogRatherThanReplacingIt(t *testing.T) {
	// The attempt site names exactly one meeting. Uploading its catalog
	// verbatim would truncate the whole remote archive to that meeting — this
	// is the reason the sink reads, merges and writes back.
	nc := newFakeNCFiles()
	sink := newNCSink(t, nc.server(t).URL)

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
	sink := newNCSink(t, nc.server(t).URL)
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
	sink := newNCSink(t, nc.server(t).URL)
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
	sink := newNCSink(t, nc.server(t).URL)
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
	sink := newNCSink(t, nc.server(t).URL)
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

// The silent third outcome D-585 forbids: with no Team folder mounted at
// `Cassini`, MKCOL creates an ordinary directory in the service account's own
// home and returns 201, the PUTs succeed, the publish reports success, and every
// caller 404s forever. WebDAV never says no, so the recorded substrate state has
// to.
func TestNCSinkRefusesToDeliverWhenTheSubstrateIsUnavailable(t *testing.T) {
	var mu sync.Mutex
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method)
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	sink := newNCSink(t, srv.URL)
	// newNCSink seeded a healthy substrate; take it away.
	ncAccessSubstrate.reset()
	ncAccessSubstrate.markApplicable()
	ncAccessSubstrate.unavailable("app_missing:"+ncAppGroupFolders, nil)
	t.Cleanup(ncAccessSubstrate.reset)

	attempt := writeAttemptSite(t, filepath.Join(t.TempDir(), "attempt"), "meeting-a")
	_, err := deliverToNC(t, sink, attempt, "meeting-a")
	if err == nil {
		t.Fatal("Deliver succeeded into an unprovisioned substrate")
	}
	// The message has to carry both escapes: where to look, and how to opt out.
	for _, want := range []string{"app_missing:" + ncAppGroupFolders, "recordings_access", envPublishSinkName + "=local"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	// Nothing may have been written — not even a directory. A partial write is
	// how the private-home tree gets created in the first place.
	mu.Lock()
	defer mu.Unlock()
	if len(methods) != 0 {
		t.Fatalf("an unprovisioned substrate was written to: %v", methods)
	}
}

// A standalone operator, or an ExApp pinned to the local sink, never records an
// applicable substrate — the gate must not fire for them.
func TestNCSinkDeliversWhenNoSubstrateIsExpected(t *testing.T) {
	nc := newFakeNCFiles()
	sink := newNCSink(t, nc.server(t).URL)
	ncAccessSubstrate.reset() // applicable == false
	t.Cleanup(ncAccessSubstrate.reset)

	attempt := writeAttemptSite(t, filepath.Join(t.TempDir(), "attempt"), "meeting-a")
	if _, err := deliverToNC(t, sink, attempt, "meeting-a"); err != nil {
		t.Fatalf("Deliver refused a deployment that expects no substrate: %v", err)
	}
}
