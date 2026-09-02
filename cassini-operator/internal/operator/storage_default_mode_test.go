package operator

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// The default model's two halves — writing without ACLs and reading as the
// owner — and the guard that keeps the second from failing open.

// In the default model the leaf reservation dance is not merely unnecessary,
// it is fatal: `nc:acl-list` is only settable inside a Team folder with
// advanced ACL, so a PROPPATCH outside one answers 207 with a 403 propstat and
// the publish fails. Nothing may be sent.
func TestDefaultModePublishWritesTheBytesAndNoACLs(t *testing.T) {
	setStorageMode(t, false)
	nc := newFakeNCFiles()
	sink := newNCSink(t, nc.server(t).URL)
	attempt := writeAttemptSite(t, filepath.Join(t.TempDir(), "attempt"), "meeting-a")

	if _, err := deliverToNC(t, sink, attempt, "meeting-a"); err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	if !nc.has("Cassini/Recordings/meetings/meeting-a.opus") {
		t.Fatal("the recording never reached Nextcloud")
	}
	if got := nc.catalogIDs(t); len(got) != 1 || got[0] != "meeting-a" {
		t.Fatalf("catalog ids = %v, want [meeting-a]", got)
	}
	for _, op := range ncOpsByMethod(nc, "PROPPATCH") {
		t.Errorf("the default model PROPPATCHed %s — outside a Team folder that is rejected, and would fail every publish", op.path)
	}
}

// ncOpsByMethod is opsFor's mirror image: opsFor answers "what happened to this
// path", and the assertions here need "did this method happen at all".
func ncOpsByMethod(f *fakeNCFiles, method string) []ncFilesOp {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ncFilesOp
	for _, op := range f.ops {
		if op.method == method {
			out = append(out, op)
		}
	}
	return out
}

// The empty reservation exists only to get a rule onto a leaf before it holds
// audio. With no rules to write, it is a wasted round-trip per asset and a
// window in which a zero-byte recording is listed.
func TestDefaultModePublishDoesNotReserveAnEmptyLeaf(t *testing.T) {
	setStorageMode(t, false)
	nc := newFakeNCFiles()
	sink := newNCSink(t, nc.server(t).URL)
	attempt := writeAttemptSite(t, filepath.Join(t.TempDir(), "attempt"), "meeting-a")

	if _, err := deliverToNC(t, sink, attempt, "meeting-a"); err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	for _, op := range ncOpsByMethod(nc, http.MethodPut) {
		if op.path == "Cassini/Recordings/meetings/meeting-a.opus" && op.body == "" {
			t.Fatal("the default model reserved an empty leaf before writing the audio")
		}
	}
}

// Access control on is the unchanged path, and this is the regression guard
// for it: the branch above must not have made the rules conditional on
// anything but the mode.
func TestAccessControlledPublishStillWritesTheLeafACL(t *testing.T) {
	setStorageMode(t, true)
	nc := newFakeNCFiles()
	sink := newNCSink(t, nc.server(t).URL)
	attempt := writeAttemptSite(t, filepath.Join(t.TempDir(), "attempt"), "meeting-a")

	if _, err := deliverToNC(t, sink, attempt, "meeting-a"); err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	if len(nc.aclBodiesFor("Cassini/Recordings/meetings/meeting-a.opus")) == 0 {
		t.Fatal("no ACL was written onto the recording under access control")
	}
}

// The read path. In the default model no account has a mount of the service
// account's home, so reading as the caller does not restrict the archive — it
// hides all of it from everyone.
func TestDefaultModeProxyReadsAsTheServiceAccount(t *testing.T) {
	setStorageMode(t, false)
	catalog := `{"version":"cassini.viewer.catalog.v1","meetings":[` +
		`{"id":"a","audioPath":"./meetings/JOB1.opus"},{"id":"b","audioPath":"./meetings/JOB2.opus"}]}`
	var catalogGetAs, opusGetAs string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PROPFIND":
			t.Errorf("the default model must not run a per-caller scan; got PROPFIND %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		case strings.HasSuffix(r.URL.Path, "/catalog.json"):
			catalogGetAs = davUserOf(r.URL.Path)
			_, _ = w.Write([]byte(catalog))
		default:
			opusGetAs = davUserOf(r.URL.Path)
			_, _ = w.Write([]byte("opus-bytes"))
		}
	}))
	defer srv.Close()
	proxy := aclProxyConfig(srv.URL).ncFilesProxy(log.New(ioDiscard{}, "", 0))

	rec := httptest.NewRecorder()
	if !proxy(rec, callerReq(http.MethodGet, "/published/catalog.json", "alice"), "catalog.json") {
		t.Fatal("the proxy declined to handle catalog.json")
	}
	if catalogGetAs != ncRecordingsOwner {
		t.Fatalf("catalog fetched as %q, want %q", catalogGetAs, ncRecordingsOwner)
	}
	var served siteCatalog
	if err := json.Unmarshal(rec.Body.Bytes(), &served); err != nil {
		t.Fatalf("served catalog is not JSON: %v (%s)", err, rec.Body.String())
	}
	if len(served.Meetings) != 2 {
		t.Fatalf("served %d meetings, want the whole archive (2) — the default model restricts nothing", len(served.Meetings))
	}

	rec = httptest.NewRecorder()
	if !proxy(rec, callerReq(http.MethodGet, "/published/meetings/JOB1.opus", "alice"), "meetings/JOB1.opus") {
		t.Fatal("the proxy declined to handle the recording")
	}
	if opusGetAs != ncRecordingsOwner {
		t.Fatalf("recording fetched as %q, want %q", opusGetAs, ncRecordingsOwner)
	}
	if rec.Body.String() != "opus-bytes" {
		t.Fatalf("body = %q, want the recording", rec.Body.String())
	}
}

// The fail-open guard. Between a container restart and the next enabled edge
// the mode is unresolved, and taking the owner path there would hand every
// account the whole archive. Unresolved must behave exactly like access
// control: read as the caller, and let Nextcloud decide.
func TestUnresolvedModeProxyStillReadsAsTheCaller(t *testing.T) {
	resetStorageMode(t)
	var opusGetAs string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		opusGetAs = davUserOf(r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	proxy := aclProxyConfig(srv.URL).ncFilesProxy(log.New(ioDiscard{}, "", 0))

	rec := httptest.NewRecorder()
	proxy(rec, callerReq(http.MethodGet, "/published/meetings/JOB1.opus", "alice"), "meetings/JOB1.opus")
	if opusGetAs != "alice" {
		t.Fatalf("an unresolved storage mode read as %q; it must stay per-caller until a preflight decides", opusGetAs)
	}
}

// An anonymous request gets nothing in either mode: the owner path is about
// which identity Nextcloud is asked as, never about skipping the USER gate.
func TestDefaultModeProxyStillRequiresACaller(t *testing.T) {
	setStorageMode(t, false)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("an anonymous request reached Nextcloud: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()
	proxy := aclProxyConfig(srv.URL).ncFilesProxy(log.New(ioDiscard{}, "", 0))

	rec := httptest.NewRecorder()
	proxy(rec, callerReq(http.MethodGet, "/published/meetings/JOB1.opus", ""), "meetings/JOB1.opus")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("anonymous recording request = %d, want 404", rec.Code)
	}
}

// davUserOf pulls the account a WebDAV path is addressed to out of
// /remote.php/dav/files/<user>/...
func davUserOf(path string) string {
	const prefix = "/remote.php/dav/files/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(path, prefix)
	user, _, _ := strings.Cut(rest, "/")
	return user
}

// Refusing a recording only when a prerequisite is NAMED. A container that
// restarted an hour ago reports `unknown`, is very probably fine, and must not
// turn a reboot into an outage.
func TestRecordingRefusalOnlyFiresOnANamedMissingPrerequisite(t *testing.T) {
	ncAccessSubstrate.reset()
	t.Cleanup(ncAccessSubstrate.reset)
	ncAccessSubstrate.markApplicable()

	if got := ncAccessSubstrate.recordingRefusal(); got != "" {
		t.Fatalf("an unchecked substrate refused a recording: %q", got)
	}
	ncAccessSubstrate.degraded("root_acl", errTransitionNotReady)
	if got := ncAccessSubstrate.recordingRefusal(); got != "" {
		t.Fatalf("a degraded substrate refused a recording: %q", got)
	}
	ncAccessSubstrate.reset()
	ncAccessSubstrate.markApplicable()
	ncAccessSubstrate.unavailable(storageStepServiceAccount, errTransitionNotReady)
	if got := ncAccessSubstrate.recordingRefusal(); got == "" {
		t.Fatal("a missing service account did not refuse the recording")
	}
	ncAccessSubstrate.succeed()
	if got := ncAccessSubstrate.recordingRefusal(); got == "" {
		t.Fatal("succeed() cleared a recorded unavailability, which it must never do")
	}

	// A standalone operator has no substrate to be missing.
	ncAccessSubstrate.reset()
	ncAccessSubstrate.unavailable(storageStepServiceAccount, errTransitionNotReady)
	if got := ncAccessSubstrate.recordingRefusal(); got != "" {
		t.Fatalf("a deployment with no Nextcloud substrate refused a recording: %q", got)
	}
}

// A refused Talk start creates no job and claims no room, and it answers 200 —
// Talk never reads the body, and a 4xx or 5xx there produces either three
// retries or a blank 500 with no message for the moderator at all.
func TestTalkStartIsRefusedWithoutCreatingAJob(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	ncAccessSubstrate.reset()
	t.Cleanup(ncAccessSubstrate.reset)
	ncAccessSubstrate.markApplicable()
	ncAccessSubstrate.unavailable(storageStepServiceAccount, errTransitionNotReady)

	talk := newFakeTalkServer(t)
	defer talk.Close()
	rt.cfg.TalkSharedSecret = "shared"

	rec := httptest.NewRecorder()
	rt.handleTalkStart(rec, httptest.NewRequest(http.MethodPost, "/api/v1/room/tok", nil),
		talkRequestAuth{BackendURL: talk.server.URL},
		"tok",
		talkRoomRequest{Type: "start", Start: &talkStartData{Owner: "alice", Actor: &talkActorData{Type: "users", ID: "alice"}}})

	if rec.Code != http.StatusOK {
		t.Fatalf("refusal answered %d; Talk ignores the body and retries a 5xx three times, so it must be a 200", rec.Code)
	}
	jobs, err := rt.store.ListJobs(context.Background())
	if err != nil {
		t.Fatalf("ListJobs() error = %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("a refused start created %d jobs, want 0", len(jobs))
	}
	if _, claimed := rt.lookupTalkRoomState(talkRoomKey(talk.server.URL, "tok")); claimed {
		t.Fatal("a refused start left the room claimed, so the next attempt would be swallowed as a duplicate")
	}
}
