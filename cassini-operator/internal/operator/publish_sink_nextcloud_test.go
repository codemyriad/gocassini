package operator

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
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
	acls    map[string][]aclRule
	order   []string
	ops     []ncFilesOp
	failPUT map[string]int
	// truncatePUT makes a PUT store fewer bytes than were sent while still
	// answering 2xx — an interrupted upload as Nextcloud commits it: the fileid
	// and the ACL survive, only the content is short.
	truncatePUT map[string]int
	// failGET answers a path with the given status and a JSON body, which is
	// what a gateway in front of Nextcloud does and what Sabre never does.
	failGET map[string]int
}

// ncFilesOp is one mutating request the fake saw.
type ncFilesOp struct {
	method string
	path   string
	body   string
}

func newFakeNCFiles() *fakeNCFiles {
	return &fakeNCFiles{
		files:       map[string][]byte{},
		acls:        map[string][]aclRule{},
		failPUT:     map[string]int{},
		truncatePUT: map[string]int{},
		failGET:     map[string]int{},
	}
}

// leafMultistatus renders the Depth-0 response davPropfindLeafState parses,
// including the separate 404 propstat Nextcloud emits for a property the
// resource does not carry.
func leafMultistatus(href string, size int, rules []aclRule) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:nc="http://nextcloud.org/ns"><d:response><d:href>`)
	b.WriteString(href)
	b.WriteString(`</d:href><d:propstat><d:prop><d:getcontentlength>`)
	fmt.Fprintf(&b, "%d", size)
	b.WriteString(`</d:getcontentlength>`)
	if len(rules) > 0 {
		b.WriteString(`<nc:acl-list>`)
		for _, r := range rules {
			fmt.Fprintf(&b, `<nc:acl><nc:acl-mapping-type>%s</nc:acl-mapping-type><nc:acl-mapping-id>%s</nc:acl-mapping-id><nc:acl-mask>%d</nc:acl-mask><nc:acl-permissions>%d</nc:acl-permissions></nc:acl>`,
				r.Type, r.ID, r.Mask, r.Permissions)
		}
		b.WriteString(`</nc:acl-list>`)
	}
	b.WriteString(`</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>`)
	if len(rules) == 0 {
		b.WriteString(`<d:propstat><d:prop><nc:acl-list/></d:prop><d:status>HTTP/1.1 404 Not Found</d:status></d:propstat>`)
	}
	b.WriteString(`</d:response></d:multistatus>`)
	return b.String()
}

// parseACLRulesFromPropertyUpdate reads back what aclRulesXML wrote, so the fake
// stores the rules a later PROPFIND has to report.
func parseACLRulesFromPropertyUpdate(body string) []aclRule {
	var pu struct {
		ACLs []struct {
			Type        string `xml:"acl-mapping-type"`
			ID          string `xml:"acl-mapping-id"`
			Mask        int    `xml:"acl-mask"`
			Permissions int    `xml:"acl-permissions"`
		} `xml:"set>prop>acl-list>acl"`
	}
	if err := xml.Unmarshal([]byte(body), &pu); err != nil {
		return nil
	}
	out := make([]aclRule, 0, len(pu.ACLs))
	for _, a := range pu.ACLs {
		out = append(out, aclRule{Type: a.Type, ID: a.ID, Mask: a.Mask, Permissions: a.Permissions})
	}
	return out
}

// aclBodiesFor returns every ACL body PROPPATCHed onto path, in order.
//
// A delivered recording legitimately receives two: the owner-only deny that
// covers it before it holds any audio, then the audience. What must never
// happen is a *third* on a re-delivery, which is what would silently reset an
// audience someone had widened by hand — asserted directly by the republish
// tests rather than inferred from a count here.
func (f *fakeNCFiles) aclBodiesFor(path string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var bodies []string
	for _, op := range f.ops {
		if op.method == "PROPPATCH" && op.path == path {
			bodies = append(bodies, op.body)
		}
	}
	return bodies
}

// aclBodyFor returns the last ACL body PROPPATCHed onto path — the rule set the
// archive actually ended up with — failing if none was sent at all.
func (f *fakeNCFiles) aclBodyFor(t *testing.T, path string) string {
	t.Helper()
	bodies := f.aclBodiesFor(path)
	if len(bodies) == 0 {
		t.Fatalf("no PROPPATCH for %s", path)
	}
	return bodies[len(bodies)-1]
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

// lastIndexOfOp is indexOfOp for the case where the LAST occurrence is the one
// that means something — a leaf is PUT twice, and it is the second one that
// advertises it.
func (f *fakeNCFiles) lastIndexOfOp(method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.ops) - 1; i >= 0; i-- {
		if f.ops[i].method == method && f.ops[i].path == path {
			return i
		}
	}
	return -1
}

// sequenceFor renders every request the fake saw against one path as
// "METHOD" or "PUT/<bytes>", so a test can assert the whole shape of a leaf's
// delivery rather than the relative position of two of its requests.
//
// The distinction is what tells a reservation from a content write, and it is
// the only thing that does: both are a PUT to the same path, and an assertion
// phrased over methods and ordering alone stays green when the reservation is
// deleted.
func (f *fakeNCFiles) sequenceFor(path string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var seq []string
	for _, op := range f.ops {
		if op.path != path {
			continue
		}
		if op.method == http.MethodPut {
			seq = append(seq, fmt.Sprintf("PUT/%d", len(op.body)))
			continue
		}
		seq = append(seq, op.method)
	}
	return seq
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
			f.acls[rel] = parseACLRulesFromPropertyUpdate(string(body))
			w.WriteHeader(http.StatusMultiStatus)
		case "PROPFIND":
			// Real Nextcloud answers a leaf PROPFIND with a multistatus carrying
			// the length and the stored nc:acl-list. Serving a bare 200 here —
			// which is what the old `default` arm did — would make every health
			// gate read "absent" and every assertion below pass vacuously.
			// Recorded before the existence check: a health gate that probed an
			// absent leaf still asked, and the request sequence assertions are
			// about what the sink did, not about what it found.
			f.ops = append(f.ops, ncFilesOp{method: "PROPFIND", path: rel})
			body, ok := f.files[rel]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(w, leafMultistatus(r.URL.Path, len(body), f.acls[rel]))
		case http.MethodDelete:
			f.ops = append(f.ops, ncFilesOp{method: http.MethodDelete, path: rel})
			if _, ok := f.files[rel]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			delete(f.files, rel)
			delete(f.acls, rel)
			w.WriteHeader(http.StatusNoContent)
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
			if n, ok := f.truncatePUT[rel]; ok && n < len(body) {
				body = body[:n]
			}
			f.files[rel] = body
			f.order = append(f.order, rel)
			f.ops = append(f.ops, ncFilesOp{method: http.MethodPut, path: rel, body: string(body)})
			if existed {
				w.WriteHeader(http.StatusNoContent)
			} else {
				w.WriteHeader(http.StatusCreated)
			}
		case http.MethodGet:
			// A gateway answering non-2xx with a JSON body — the one shape that
			// gets past davGetBytes, which reports a nil error for any status.
			if status, ok := f.failGET[rel]; ok {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"message":"upstream unavailable"}`)
				return
			}
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

// catalogIDs reads the archive index of whichever storage model is in force —
// the two have separate roots, so a helper pinned to one of them silently
// reports "no meetings" for the other.
func (f *fakeNCFiles) catalogIDs(t *testing.T) []string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, ok := f.files[ncArchiveRoot()+"/catalog.json"]
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
	if location != ncACLRecordingsRoot {
		t.Fatalf("location = %q, want %q", location, ncACLRecordingsRoot)
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
	//
	// EVERY rule write is checked, not the one the archive ended up with.
	// aclBodyFor reads the last PROPPATCH, so an assertion phrased through it
	// says nothing about the create-time deny — the write that has to be right,
	// because it is the only one that precedes the content. Clearing it would be
	// fail-OPEN: an empty rule list leaves the leaf inheriting the container's
	// read grant for the whole upload, which is the exposure this ordering
	// exists to close.
	catalogACLs := nc.aclBodiesFor(catalogPath)
	if len(catalogACLs) != 2 {
		t.Fatalf("catalog rule writes = %d, want the create-time deny and the one after the content", len(catalogACLs))
	}
	for i, acl := range catalogACLs {
		for _, want := range []string{
			"<nc:acl-mapping-id>" + ncRecordingsEveryoneGroup + "</nc:acl-mapping-id>",
			denyAll,
			"<nc:acl-mapping-id>" + ncRecordingsOwner + "</nc:acl-mapping-id>",
			grantAll,
		} {
			if !strings.Contains(acl, want) {
				t.Errorf("catalog ACL %d/%d missing %q: %s", i+1, len(catalogACLs), want, acl)
			}
		}
	}

	// Ordering: deny the recording, then advertise it. The advertisement is the
	// write that puts meetings in the index, which is the LAST PUT on the
	// catalog — the first one is the empty reservation asserted below.
	opusDeny := nc.indexOfOp("PROPPATCH", opusPath)
	catalogWrite := nc.lastIndexOfOp(http.MethodPut, catalogPath)
	if opusDeny < 0 || catalogWrite < 0 {
		t.Fatalf("missing operations: opus deny=%d catalog PUT=%d", opusDeny, catalogWrite)
	}
	if opusDeny > catalogWrite {
		t.Fatalf("recording was advertised at op %d before it was protected at op %d", catalogWrite, opusDeny)
	}

	// The catalog is born under the same rule as a recording, and this is what
	// pins it. Every assertion above survives deleting the reservation: the ACL
	// checks are about rule CONTENT and are satisfied by whichever writes remain,
	// and the ordering check compares two positions that move together. Only the
	// shape of the sequence — an empty PUT before the deny, the body after it —
	// can tell the difference.
	//
	// The whole sequence rather than its first two entries, because the trailing
	// PROPPATCH is load-bearing on its own: the reserve-and-deny is gated on the
	// catalog being absent, so on every delivery into an archive that already has
	// one, that trailing write is the ONLY rule write on the path — and the one
	// that repairs a catalog left unruled by an earlier failure, which nothing
	// else does, since selfHealLeafProtection never visits a non-`.opus` leaf.
	//
	// It has to hold on the ordinary path, not a corner: provisioning never
	// creates catalog.json (it calls a missing one "the normal fresh-install
	// state"), so the first publish into any new instance takes this branch.
	assertLeafSequence(t, catalogPath, nc.sequenceFor(catalogPath), "PUT/0", "PROPPATCH", "PUT/+", "PROPPATCH")
}

// assertLeafSequence compares a leaf's request sequence against a shape, where
// "PUT/+" matches a PUT carrying any non-zero number of bytes and every other
// entry must match exactly.
//
// The byte counts are the point. A reservation and a content write are the same
// method on the same path, so a shape written in methods alone cannot say which
// came first — which is exactly the assertion D-594 needs and exactly the one
// that silently stopped holding when the reservation was added.
func assertLeafSequence(t *testing.T, path string, got []string, want ...string) {
	t.Helper()
	ok := len(got) == len(want)
	for i := 0; ok && i < len(want); i++ {
		if want[i] == "PUT/+" {
			ok = strings.HasPrefix(got[i], "PUT/") && got[i] != "PUT/0"
			continue
		}
		ok = got[i] == want[i]
	}
	if !ok {
		t.Errorf("%s request sequence = %v, want %v", path, got, want)
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

// A catalog GET that is not a 404 and not a success is not an empty archive.
//
// davGetBytes reports a nil error for any status, so a status the reader does
// not branch on reaches json.Unmarshal — and siteCatalog.Meetings is a slice,
// so any JSON object without a `meetings` key parses cleanly as "no meetings
// here". Since the merge seeds from what was read and the result is PUT whole,
// believing that answer replaces every meeting in the archive with the one
// being delivered, and nothing repairs it afterwards: later publishes append to
// the truncated file and backfill refuses a populated destination.
func TestNCSinkRefusesToTreatAFailedCatalogReadAsAnEmptyArchive(t *testing.T) {
	nc := newFakeNCFiles()
	sink := newNCSink(t, nc.server(t).URL)

	for _, id := range []string{"meeting-a", "meeting-b"} {
		attempt := writeAttemptSite(t, filepath.Join(t.TempDir(), id), id)
		if _, err := deliverToNC(t, sink, attempt, id); err != nil {
			t.Fatalf("Deliver(%s) error = %v", id, err)
		}
	}

	nc.failGET["Cassini/Recordings/catalog.json"] = http.StatusServiceUnavailable
	_, err := deliverToNC(t, sink, writeAttemptSite(t, filepath.Join(t.TempDir(), "c"), "meeting-c"), "meeting-c")
	if err == nil {
		t.Fatal("a 503 carrying a JSON body was accepted as the archive's index")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("the failure should name the status it got, not a parse error: %v", err)
	}

	// The point of failing: the index still names everything it did before.
	if got := nc.catalogIDs(t); len(got) != 2 || got[0] != "meeting-a" || got[1] != "meeting-b" {
		t.Fatalf("remote catalog ids = %v, want the archive intact", got)
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

// The last link in the seal chain, on the sink that production actually uses
// (D-583 acceptance criterion 3). The local sink has checked this since #169;
// this one did not, because #153 landed it before the seal stage existed and
// the criterion stopped being satisfiable by one PR.
//
// A mismatch must fail before anything is uploaded: the sink's whole contract is
// that a meeting either reaches Nextcloud whole or does not reach it at all, and
// a half-delivered archive with a recording nothing indexes is the state it was
// built to prevent.
func TestNCSinkRefusesAnAssetThatIsNotTheSealedArtifact(t *testing.T) {
	nc := newFakeNCFiles()
	sink := newNCSink(t, nc.server(t).URL)
	attempt := writeAttemptSite(t, filepath.Join(t.TempDir(), "attempt"), "meeting-a")

	_, err := sink.Deliver(context.Background(), publishDelivery{
		AttemptSitePath: attempt,
		JobID:           "meeting-a",
		AttemptNumber:   1,
		PublishedAtUTC:  "2026-06-12T00:00:00Z",
		AssetDigests:    map[string]string{"meetings/meeting-a.opus": "not-the-sealed-digest"},
	})
	if err == nil {
		t.Fatal("Deliver() succeeded with an asset that does not match the sealed artifact")
	}
	if !strings.Contains(err.Error(), "sealed") {
		t.Errorf("error does not say what is wrong: %v", err)
	}

	// Nothing may have been written — not the recording, and above all not the
	// catalog, which is what makes a recording visible.
	for _, path := range []string{
		"Cassini/Recordings/meetings/meeting-a.opus",
		"Cassini/Recordings/catalog.json",
	} {
		if nc.has(path) {
			t.Errorf("a refused delivery left %s in Nextcloud", path)
		}
	}
}

// The digest that matches is delivered, so the check cannot be satisfied by
// refusing everything.
func TestNCSinkDeliversAnAssetMatchingTheSealedArtifact(t *testing.T) {
	nc := newFakeNCFiles()
	sink := newNCSink(t, nc.server(t).URL)
	attempt := writeAttemptSite(t, filepath.Join(t.TempDir(), "attempt"), "meeting-a")

	digest, err := fileSHA256(filepath.Join(attempt, "meetings", "meeting-a.opus"))
	if err != nil {
		t.Fatalf("fileSHA256() error = %v", err)
	}
	if _, err := sink.Deliver(context.Background(), publishDelivery{
		AttemptSitePath: attempt,
		JobID:           "meeting-a",
		AttemptNumber:   1,
		PublishedAtUTC:  "2026-06-12T00:00:00Z",
		AssetDigests:    map[string]string{"meetings/meeting-a.opus": digest},
	}); err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	if !nc.has("Cassini/Recordings/meetings/meeting-a.opus") {
		t.Error("a matching asset was not delivered")
	}
}

// ---------------------------------------------------------------------------
// D-594: the object must never be reachable with content in it and no rules on
// it, and a re-delivery must replace content without touching access.
// ---------------------------------------------------------------------------

// assertLeafProtected fails unless the archive ENDED UP with a broad-group rule
// on path.
//
// This is the assertion that actually encodes D-594, and every test that
// delivers a recording makes it. Asserting only on the request sequence is not
// enough: a delivery can deny a leaf, delete it, and then re-create it with a
// bare content PUT, which reads as a perfectly ordered transcript and leaves the
// recording readable by every account.
func assertLeafProtected(t *testing.T, nc *fakeNCFiles, path string) {
	t.Helper()
	nc.mu.Lock()
	rules := nc.acls[path]
	nc.mu.Unlock()
	if !nc.has(path) {
		t.Fatalf("%s is not in the archive at all", path)
	}
	if !hasExplicitEveryoneGroupRule(rules) {
		t.Fatalf("%s ended up with no %q rule — it is readable by every account: %+v",
			path, ncRecordingsEveryoneGroup, rules)
	}
}

// opsFor returns the fake's mutation sequence for one path, methods only.
func (f *fakeNCFiles) opsFor(path string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, op := range f.ops {
		if op.path == path {
			out = append(out, op.method)
		}
	}
	return out
}

func TestNCSinkRulesTheRecordingBeforeItHasAnyAudioInIt(t *testing.T) {
	nc := newFakeNCFiles()
	sink := newNCSink(t, nc.server(t).URL)
	sink.applyAccess = func(ctx context.Context, jobID string) error {
		return sink.cfg.davProppatchACLRules(ctx, sink.client, ncRecordingsOwner,
			ncACLRecordingsRoot+"/meetings/"+jobID+".opus",
			recordingACLRules([]aclMapping{{Type: "user", ID: "alice"}}, false))
	}
	attempt := writeAttemptSite(t, filepath.Join(t.TempDir(), "attempt"), "meeting-a")

	if _, err := deliverToNC(t, sink, attempt, "meeting-a"); err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}

	opus := "Cassini/Recordings/meetings/meeting-a.opus"
	// The empty reservation, then the deny, then the audio. A PUT of content
	// before the PROPPATCH is the bug this ticket is about.
	got := nc.opsFor(opus)
	want := []string{"PROPFIND", http.MethodPut, "PROPPATCH", http.MethodPut, "PROPFIND", "PROPPATCH"}
	if len(got) != len(want) {
		t.Fatalf("op sequence for %s = %v, want %v", opus, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("op sequence for %s = %v, want %v", opus, got, want)
		}
	}

	// And the reservation really was empty: had it carried the audio, the deny
	// that follows it would be closing the door after the fact.
	nc.mu.Lock()
	var reservation string
	for _, op := range nc.ops {
		if op.method == http.MethodPut && op.path == opus {
			reservation = op.body
			break
		}
	}
	nc.mu.Unlock()
	if reservation != "" {
		t.Fatalf("the leaf was reserved with %d bytes of content, want 0", len(reservation))
	}
	assertLeafProtected(t, nc, opus)
}

func TestNCSinkRepublishReplacesContentButNotAccess(t *testing.T) {
	nc := newFakeNCFiles()
	sink := newNCSink(t, nc.server(t).URL)
	applied := 0
	sink.applyAccess = func(ctx context.Context, jobID string) error {
		applied++
		return sink.cfg.davProppatchACLRules(ctx, sink.client, ncRecordingsOwner,
			ncACLRecordingsRoot+"/meetings/"+jobID+".opus",
			recordingACLRules([]aclMapping{{Type: "user", ID: "alice"}}, false))
	}
	opus := "Cassini/Recordings/meetings/meeting-a.opus"

	if _, err := deliverToNC(t, sink, writeAttemptSite(t, filepath.Join(t.TempDir(), "one"), "meeting-a"), "meeting-a"); err != nil {
		t.Fatalf("first Deliver() error = %v", err)
	}
	if applied != 1 {
		t.Fatalf("audience applied %d times on the first delivery, want 1", applied)
	}

	// An administrator widens the recording by hand in the Files UI.
	widened := append(recordingACLRules([]aclMapping{{Type: "user", ID: "alice"}}, false),
		aclRule{Type: "user", ID: "carol", Mask: aclMaskAll, Permissions: aclPermRead})
	if err := sink.cfg.davProppatchACLRules(context.Background(), sink.client, ncRecordingsOwner,
		ncACLRecordingsRoot+"/meetings/meeting-a.opus", widened); err != nil {
		t.Fatalf("hand-widening the ACL failed: %v", err)
	}
	before := len(nc.aclBodiesFor(opus))

	if _, err := deliverToNC(t, sink, writeAttemptSite(t, filepath.Join(t.TempDir(), "two"), "meeting-a"), "meeting-a"); err != nil {
		t.Fatalf("republish Deliver() error = %v", err)
	}

	if applied != 1 {
		t.Fatalf("audience applied %d times, want 1 — a re-delivery must not rewrite access", applied)
	}
	if after := len(nc.aclBodiesFor(opus)); after != before {
		t.Fatalf("republish sent %d further ACL writes, want 0", after-before)
	}
	// Carol's grant is still there, and the audio was still replaced.
	nc.mu.Lock()
	rules := nc.acls[opus]
	nc.mu.Unlock()
	found := false
	for _, r := range rules {
		if r.Type == "user" && r.ID == "carol" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the hand-added grant was reset by a re-delivery: %+v", rules)
	}
	if !nc.has(opus) {
		t.Fatalf("the recording disappeared across a re-delivery")
	}
	assertLeafProtected(t, nc, opus)
}

func TestNCSinkFinishesAnAudienceThatNeverLanded(t *testing.T) {
	// The publish died between the content PUT and the audience PROPPATCH, so
	// the leaf carries only the owner-only baseline. It is protected, but the
	// meeting is invisible to its own participants — a re-delivery has to
	// finish the job rather than read "already ruled" and skip it.
	nc := newFakeNCFiles()
	sink := newNCSink(t, nc.server(t).URL)
	fail := true
	applied := 0
	sink.applyAccess = func(ctx context.Context, jobID string) error {
		if fail {
			return fmt.Errorf("talk is down")
		}
		applied++
		return sink.cfg.davProppatchACLRules(ctx, sink.client, ncRecordingsOwner,
			ncACLRecordingsRoot+"/meetings/"+jobID+".opus",
			recordingACLRules([]aclMapping{{Type: "user", ID: "alice"}}, false))
	}

	if _, err := deliverToNC(t, sink, writeAttemptSite(t, filepath.Join(t.TempDir(), "one"), "meeting-a"), "meeting-a"); err == nil {
		t.Fatalf("expected the first delivery to fail when the audience cannot be written")
	}
	if nc.has("Cassini/Recordings/catalog.json") {
		t.Fatalf("a meeting whose audience did not land must not be advertised")
	}

	fail = false
	if _, err := deliverToNC(t, sink, writeAttemptSite(t, filepath.Join(t.TempDir(), "two"), "meeting-a"), "meeting-a"); err != nil {
		t.Fatalf("second Deliver() error = %v", err)
	}
	if applied != 1 {
		t.Fatalf("audience applied %d times, want 1 — the unfinished publish was not resumed", applied)
	}
}

func TestNCSinkRepairsARecordingDeliveredWithoutAnyRule(t *testing.T) {
	// The pre-fix archive state, and what any half-written delivery leaves: a
	// recording sitting in meetings/ with no ACL rows at all, readable by every
	// account through the container's grant.
	nc := newFakeNCFiles()
	opus := "Cassini/Recordings/meetings/meeting-a.opus"
	nc.files[opus] = []byte("leaked audio")
	sink := newNCSink(t, nc.server(t).URL)
	sink.applyAccess = func(ctx context.Context, jobID string) error { return nil }

	if _, err := deliverToNC(t, sink, writeAttemptSite(t, filepath.Join(t.TempDir(), "attempt"), "meeting-a"), "meeting-a"); err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}

	got := nc.opsFor(opus)
	// The deny MUST precede the DELETE. Deleting an unruled recording moves the
	// bytes into a Team-folder trash that every account can list and download,
	// so a bare DELETE relocates the exposure instead of ending it.
	deny, del := -1, -1
	for i, m := range got {
		if m == "PROPPATCH" && deny < 0 {
			deny = i
		}
		if m == http.MethodDelete {
			del = i
		}
	}
	if deny < 0 || del < 0 {
		t.Fatalf("op sequence for %s = %v, want a PROPPATCH and a DELETE", opus, got)
	}
	if deny > del {
		t.Fatalf("op sequence for %s = %v — the leaf was deleted before it was denied", opus, got)
	}
	// And it has to be a DENY. Asserting only that "a PROPPATCH came first"
	// passes just as happily for one that grants — which would put a
	// world-readable copy of the recording into the Team-folder trash, the exact
	// outcome the ordering exists to prevent.
	first := nc.aclBodiesFor(opus)[0]
	if !hasEveryoneGroupDeny(parseACLRulesFromPropertyUpdate(first)) {
		t.Fatalf("the rule set written before the DELETE does not deny %q, so the trash copy stays readable: %s",
			ncRecordingsEveryoneGroup, first)
	}
	if string(nc.files[opus]) == "leaked audio" {
		t.Fatalf("the unprotected recording was left in place")
	}
	// And the replacement is protected. Without this the whole test passes for a
	// delivery that denies, deletes, and then re-creates the leaf with a bare
	// content PUT — which is D-594 again, with a tidier transcript.
	assertLeafProtected(t, nc, opus)
}

func TestNCSinkRefusesToPublishATruncatedUpload(t *testing.T) {
	nc := newFakeNCFiles()
	opus := "Cassini/Recordings/meetings/meeting-a.opus"
	nc.truncatePUT[opus] = 3
	sink := newNCSink(t, nc.server(t).URL)
	sink.applyAccess = func(ctx context.Context, jobID string) error { return nil }

	_, err := deliverToNC(t, sink, writeAttemptSite(t, filepath.Join(t.TempDir(), "attempt"), "meeting-a"), "meeting-a")
	if err == nil {
		t.Fatalf("expected a truncated upload to fail the publish")
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("error = %v, want it to name the truncation", err)
	}
	if nc.has("Cassini/Recordings/catalog.json") {
		t.Fatalf("a truncated recording must not be advertised")
	}
}

func TestNCSinkDoesNotUndoAHandNarrowedAudience(t *testing.T) {
	// A leaf sitting at exactly the owner-only baseline is ambiguous: it is what
	// a publish that died before the audience landed leaves, and equally what an
	// administrator leaves by narrowing a recording the documented way. Getting
	// that wrong re-derives the audience and silently reverts their decision —
	// and for a PUBLIC room it re-grants `everyone` read on a recording somebody
	// had just made private. The catalog breaks the tie.
	for _, tc := range []struct {
		name   string
		public bool
	}{
		{"private meeting narrowed to owner-only", false},
		{"public meeting made private by hand", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nc := newFakeNCFiles()
			sink := newNCSink(t, nc.server(t).URL)
			applied := 0
			sink.applyAccess = func(ctx context.Context, jobID string) error {
				applied++
				return sink.cfg.davProppatchACLRules(ctx, sink.client, ncRecordingsOwner,
					ncACLRecordingsRoot+"/meetings/"+jobID+".opus",
					recordingACLRules([]aclMapping{{Type: "user", ID: "bob"}}, tc.public))
			}
			opus := "Cassini/Recordings/meetings/meeting-a.opus"

			if _, err := deliverToNC(t, sink, writeAttemptSite(t, filepath.Join(t.TempDir(), "one"), "meeting-a"), "meeting-a"); err != nil {
				t.Fatalf("first Deliver() error = %v", err)
			}

			// The admin narrows it to exactly the shape recordingACLRules(nil,
			// false) produces — no participants, everyone denied.
			if err := sink.cfg.davProppatchACLRules(context.Background(), sink.client, ncRecordingsOwner,
				ncACLRecordingsRoot+"/meetings/meeting-a.opus", recordingACLRules(nil, false)); err != nil {
				t.Fatalf("hand-narrowing failed: %v", err)
			}

			if _, err := deliverToNC(t, sink, writeAttemptSite(t, filepath.Join(t.TempDir(), "two"), "meeting-a"), "meeting-a"); err != nil {
				t.Fatalf("republish Deliver() error = %v", err)
			}

			if applied != 1 {
				t.Fatalf("audience applied %d times, want 1 — the re-publish reverted a deliberate narrowing", applied)
			}
			nc.mu.Lock()
			after := nc.acls[opus]
			nc.mu.Unlock()
			for _, r := range after {
				if r.Type == "group" && r.ID == ncRecordingsEveryoneGroup && r.Permissions&aclPermRead != 0 {
					t.Fatalf("the recording is world-readable again after a re-publish: %+v", after)
				}
				if r.Type == "user" && r.ID == "bob" {
					t.Fatalf("the removed participant grant came back: %+v", after)
				}
			}
			assertLeafProtected(t, nc, opus)
		})
	}
}
