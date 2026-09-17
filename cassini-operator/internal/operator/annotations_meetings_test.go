package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"cassini-operator/internal/operator/appapi"
)

// annotations/meetings/<id> (D-737). MEETING1 is alice's to read; SECRET is in
// the archive and belongs to someone else; NEVER does not exist. The service
// account's side of Nextcloud is fakeNCFiles, so ETags, If-Match and ACL rules
// behave exactly as they do for the publish sink's tests.

const (
	annTestRecording = "Cassini/Recordings/meetings/MEETING1.opus"
	annTestSecret    = "Cassini/Recordings/meetings/SECRET.opus"
	annTestNamespace = "urn:uuid:07e4eab6-4f5f-4cc4-8913-46432c5cd726"
	annTestMark      = `{"ops":[{"op":"mark","tag":{"label":"hiring"},"target":{"kind":"meeting"}}]}`

	annTestCatalog = `{"version":"cassini.viewer.catalog.v1","meetings":[` +
		`{"id":"MEETING1","title":"Daily Standup","audioPath":"./meetings/MEETING1.opus"},` +
		`{"id":"SECRET","title":"Someone else's","audioPath":"./meetings/SECRET.opus"}]}`

	// annTestApplied is what the stand-in CLI reports after an apply.
	annTestApplied = `{"format":"cassini.annotate.result.v1",` +
		`"annotations":{"format":"cassini.annotations.v1","revision":1,"tags":[{"id":"tag_known","label":"hiring"}],` +
		`"items":[{"id":"mk_1","tagId":"tag_known","target":{"kind":"meeting"},"actor":{"kind":"person","id":"alice"},"operationId":"op_1"}]},` +
		`"revision":1,"operationId":"op_1","added":["mk_1"],"resolved":true,"audioOpusSha256":"aa","containerSha256":"bb"}`
)

// annotationsNextcloud is fakeNCFiles behind a front that plays Nextcloud's
// per-caller ACL. The service account reaches the fake directly; a caller sees
// only the recordings named visible, both in their Depth-1 scan and when
// fetching one. What the service account writes is what a caller then reads,
// because both come out of the same fake.
type annotationsNextcloud struct {
	*fakeNCFiles
	url string

	frontMu    sync.Mutex
	visible    map[string]bool
	callerGETs []string
	ownerGETs  []string
	// interfere is how many owner GETs of MEETING1 are preceded by somebody
	// else's write: a concurrent writer landing between our PROPFIND and our
	// PUT, which is what makes our If-Match stale.
	interfere int
	// dropRulesOnPUT makes the leaf lose its rules as a PUT lands, which is what
	// a leaf deleted and re-created in between looks like: a new fileid with
	// the recording in it and no rules.
	dropRulesOnPUT bool
}

func newAnnotationsNextcloud(t *testing.T, visible ...string) *annotationsNextcloud {
	t.Helper()
	// Unresolved: the access-controlled model, read as the caller.
	resetStorageMode(t)
	nc := &annotationsNextcloud{fakeNCFiles: newFakeNCFiles(), visible: map[string]bool{}}
	for _, name := range visible {
		nc.visible[name] = true
	}
	nc.seed(ncACLRecordingsRoot+"/catalog.json", annTestCatalog, nil)
	nc.seed(annTestRecording, "OPUS-original", recordingACLRules(nil, false))
	nc.seed(annTestSecret, "OPUS-secret", recordingACLRules(nil, false))

	backend, err := url.Parse(nc.fakeNCFiles.server(t).URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(backend)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := path.Base(r.URL.Path)
		if strings.HasPrefix(r.URL.Path, "/remote.php/dav/files/"+ncRecordingsOwner+"/") {
			nc.beforeOwner(r, base)
			proxy.ServeHTTP(w, r)
			nc.afterOwner(r)
			return
		}
		// Anyone else is a caller, and sees only what visible names.
		switch {
		case r.Method == "PROPFIND":
			var body strings.Builder
			body.WriteString(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:">`)
			body.WriteString(`<d:response><d:href>` + r.URL.Path + `/</d:href></d:response>`)
			nc.frontMu.Lock()
			for name := range nc.visible {
				body.WriteString(`<d:response><d:href>` + r.URL.Path + "/" + name + `</d:href></d:response>`)
			}
			nc.frontMu.Unlock()
			body.WriteString(`</d:multistatus>`)
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(w, body.String())
		case r.Method == http.MethodGet && strings.HasSuffix(base, ".opus"):
			nc.frontMu.Lock()
			nc.callerGETs = append(nc.callerGETs, r.URL.Path)
			allowed := nc.visible[base]
			nc.frontMu.Unlock()
			if !allowed {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			proxy.ServeHTTP(w, r)
		default:
			t.Errorf("unexpected request as a caller: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(front.Close)
	nc.url = front.URL
	return nc
}

func (nc *annotationsNextcloud) seed(rel, body string, rules []aclRule) {
	nc.fakeNCFiles.mu.Lock()
	defer nc.fakeNCFiles.mu.Unlock()
	nc.files[rel] = []byte(body)
	nc.acls[rel] = rules
}

func (nc *annotationsNextcloud) beforeOwner(r *http.Request, base string) {
	if r.Method != http.MethodGet || !strings.HasSuffix(base, ".opus") {
		return
	}
	nc.frontMu.Lock()
	nc.ownerGETs = append(nc.ownerGETs, r.URL.Path)
	interfere := nc.interfere > 0 && strings.HasSuffix(r.URL.Path, "/"+annTestRecording)
	if interfere {
		nc.interfere--
	}
	nc.frontMu.Unlock()
	if interfere {
		nc.fakeNCFiles.mu.Lock()
		nc.files[annTestRecording] = []byte("OPUS-theirs")
		nc.etags[annTestRecording]++
		nc.fakeNCFiles.mu.Unlock()
	}
}

func (nc *annotationsNextcloud) afterOwner(r *http.Request) {
	nc.frontMu.Lock()
	drop := nc.dropRulesOnPUT && r.Method == http.MethodPut
	nc.frontMu.Unlock()
	if drop {
		nc.fakeNCFiles.mu.Lock()
		delete(nc.acls, annTestRecording)
		nc.fakeNCFiles.mu.Unlock()
	}
}

func (nc *annotationsNextcloud) setInterfere(n int) {
	nc.frontMu.Lock()
	defer nc.frontMu.Unlock()
	nc.interfere = n
}

func (nc *annotationsNextcloud) setDropRulesOnPUT() {
	nc.frontMu.Lock()
	defer nc.frontMu.Unlock()
	nc.dropRulesOnPUT = true
}

func (nc *annotationsNextcloud) gets() (caller, owner []string) {
	nc.frontMu.Lock()
	defer nc.frontMu.Unlock()
	return append([]string(nil), nc.callerGETs...), append([]string(nil), nc.ownerGETs...)
}

func (nc *annotationsNextcloud) recording(rel string) string {
	nc.fakeNCFiles.mu.Lock()
	defer nc.fakeNCFiles.mu.Unlock()
	return string(nc.files[rel])
}

func (nc *annotationsNextcloud) ifMatches() []string {
	nc.fakeNCFiles.mu.Lock()
	defer nc.fakeNCFiles.mu.Unlock()
	return append([]string(nil), nc.putIfMatch...)
}

// fakeAnnotationIndex stands in for annotations.sqlite3.
type fakeAnnotationIndex struct {
	mu          sync.Mutex
	labels      map[string]string
	recorded    []string
	unavailable []string
	recordErr   error
	resolveErr  error
}

func (f *fakeAnnotationIndex) Record(_ context.Context, opusName string, _ annotateResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recordErr != nil {
		return f.recordErr
	}
	f.recorded = append(f.recorded, opusName)
	return nil
}

func (f *fakeAnnotationIndex) MarkUnavailable(_ context.Context, opusName, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unavailable = append(f.unavailable, opusName)
	return nil
}

func (f *fakeAnnotationIndex) ResolveLabel(_ context.Context, label string, _ []string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.resolveErr != nil {
		return "", false, f.resolveErr
	}
	id, ok := f.labels[strings.ToLower(strings.TrimSpace(label))]
	return id, ok, nil
}

func (f *fakeAnnotationIndex) TagVisible(context.Context, string, []string) (bool, error) {
	return true, nil
}

func (f *fakeAnnotationIndex) Namespace(context.Context) (string, error) {
	return annTestNamespace, nil
}

func (f *fakeAnnotationIndex) state() (recorded, unavailable []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.recorded...), append([]string(nil), f.unavailable...)
}

// annTestCLIPrints is a stand-in `cassini annotate` that counts its runs, writes
// a rewritten recording wherever --out points, and prints result.
func annTestCLIPrints(result string) string {
	return `echo run >> "$0.runs"
out=""; prev=""
for a in "$@"; do
  if [ "$prev" = "--out" ]; then out="$a"; fi
  prev="$a"
done
if [ -n "$out" ]; then printf 'OPUS-annotated' > "$out"; fi
printf '%s' '` + result + `'`
}

// annTestCLIExits is a stand-in that refuses with code and says stderr.
func annTestCLIExits(code int, stderr string) string {
	return fmt.Sprintf("echo run >> \"$0.runs\"\nprintf '%%s\\n' '%s' >&2\nexit %d", stderr, code)
}

func annTestRuns(t *testing.T, bin string) int {
	t.Helper()
	raw, err := os.ReadFile(bin + ".runs")
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(raw), "run\n")
}

func annTestRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// annTestService mounts the routes against ncURL with the stand-in CLI. index is
// passed as a literal nil for "no projection" — a typed nil would not be one.
func annTestService(t *testing.T, ncURL, bin string, index annotationIndex) (http.Handler, *bytes.Buffer) {
	t.Helper()
	rt := &Runtime{}
	rt.cfg.CassiniBin = bin
	rt.annotations = index
	cfg := testExAppConfig(ncURL)
	cfg.PublishSink = publishSinkNextcloudFiles
	var logs bytes.Buffer
	s := newAnnotationService(rt, cfg, log.New(&logs, "", 0))
	if s == nil {
		t.Fatal("the service must mount for an AppAPI deployment on the Nextcloud sink with a CLI")
	}
	mux := http.NewServeMux()
	s.register(mux)
	return mux, &logs
}

func annTestCall(h http.Handler, method, id, caller, body string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/annotations/meetings/"+id, reader)
	if caller != "" {
		req = req.WithContext(appapi.WithUserID(req.Context(), caller))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func annTestDecode(t *testing.T, rec *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not a JSON object: %v (%s)", err, rec.Body.String())
	}
	return body
}

func annTestError(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error response is not JSON: %v (%s)", err, rec.Body.String())
	}
	return body.Error
}

// The whole point of the access model, on both verbs: a meeting someone may
// not read and a meeting that does not exist are the same answer, reached
// before anything is fetched or run.
func TestAnnotationsMeetingAnswersAnUnreadableMeetingAsAbsent(t *testing.T) {
	nc := newAnnotationsNextcloud(t, "MEETING1.opus")
	bin := fakeCassini(t, annTestCLIExits(9, "the CLI must not run for a meeting the caller cannot read"))
	h, _ := annTestService(t, nc.url, bin, &fakeAnnotationIndex{})

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		body := ""
		if method == http.MethodPost {
			body = annTestMark
		}
		hidden := annTestCall(h, method, "SECRET", "alice", body)
		absent := annTestCall(h, method, "NEVER", "alice", body)
		if hidden.Code != http.StatusNotFound || absent.Code != http.StatusNotFound {
			t.Fatalf("%s: hidden=%d absent=%d, want 404 for both", method, hidden.Code, absent.Code)
		}
		if hidden.Body.String() != absent.Body.String() || hidden.Header().Get("Content-Type") != absent.Header().Get("Content-Type") {
			t.Fatalf("%s: a hidden meeting must answer exactly as an absent one:\nhidden=%q\nabsent=%q", method, hidden.Body.String(), absent.Body.String())
		}
	}
	if runs := annTestRuns(t, bin); runs != 0 {
		t.Fatalf("the CLI ran %d times for meetings the caller cannot read", runs)
	}
	callerGETs, ownerGETs := nc.gets()
	if len(callerGETs) != 0 || len(ownerGETs) != 0 {
		t.Fatalf("nothing may be fetched for a meeting the caller cannot read: caller=%v owner=%v", callerGETs, ownerGETs)
	}
	if nc.indexOfOp("PROPFIND", annTestSecret) != -1 || nc.indexOfOp(http.MethodPut, annTestSecret) != -1 {
		t.Fatalf("the service account must not touch a recording the caller cannot read: %v", nc.opsFor(annTestSecret))
	}
}

func TestAnnotationsMeetingGETReadsAsTheCaller(t *testing.T) {
	t.Run("a meeting with marks", func(t *testing.T) {
		nc := newAnnotationsNextcloud(t, "MEETING1.opus")
		shown := strings.Replace(annTestApplied, `"revision":1,"operationId"`, `"revision":4,"operationId"`, 1)
		bin := fakeCassini(t, annTestCLIPrints(shown))
		h, _ := annTestService(t, nc.url, bin, &fakeAnnotationIndex{})

		rec := annTestCall(h, http.MethodGet, "MEETING1", "alice", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", got)
		}
		body := annTestDecode(t, rec)
		if string(body["meetingId"]) != `"MEETING1"` || string(body["revision"]) != "4" || string(body["resolved"]) != "true" {
			t.Fatalf("response = %s", rec.Body.String())
		}
		if !strings.Contains(string(body["annotations"]), `"tag_known"`) {
			t.Fatalf("the annotations must be the recording's own: %s", body["annotations"])
		}
		for _, key := range []string{"audioOpusSha256", "containerSha256", "format"} {
			if _, ok := body[key]; ok {
				t.Errorf("%s must not reach the wire: %s", key, rec.Body.String())
			}
		}

		callerGETs, ownerGETs := nc.gets()
		if len(callerGETs) != 1 || !strings.Contains(callerGETs[0], "/files/alice/"+annTestRecording) {
			t.Fatalf("the recording must be read as the caller, got caller=%v", callerGETs)
		}
		if len(ownerGETs) != 0 || nc.indexOfOp(http.MethodPut, annTestRecording) != -1 {
			t.Fatalf("a read must not touch the recording as the service account: owner GETs=%v ops=%v", ownerGETs, nc.opsFor(annTestRecording))
		}
		args := annTestRead(t, bin+".args")
		if !strings.HasPrefix(args, "annotate\nshow\n") || !strings.HasSuffix(args, "--json\n") {
			t.Fatalf("args = %q, want annotate show <file> --json", args)
		}
	})

	t.Run("a meeting with none", func(t *testing.T) {
		nc := newAnnotationsNextcloud(t, "MEETING1.opus")
		bin := fakeCassini(t, annTestCLIPrints(`{"format":"cassini.annotate.result.v1","annotations":null,"revision":0,"resolved":null,"audioOpusSha256":"aa","containerSha256":"bb"}`))
		h, _ := annTestService(t, nc.url, bin, nil)

		rec := annTestCall(h, http.MethodGet, "MEETING1", "alice", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("code = %d (%s)", rec.Code, rec.Body.String())
		}
		body := annTestDecode(t, rec)
		if string(body["annotations"]) != "null" || string(body["resolved"]) != "null" || string(body["revision"]) != "0" {
			t.Fatalf("no marks is null, not an empty document: %s", rec.Body.String())
		}
	})
}

func TestAnnotationsMeetingPOSTCommitsAsTheCaller(t *testing.T) {
	nc := newAnnotationsNextcloud(t, "MEETING1.opus")
	bin := fakeCassini(t, annTestCLIPrints(annTestApplied))
	index := &fakeAnnotationIndex{labels: map[string]string{"hiring": "tag_known"}}
	h, logs := annTestService(t, nc.url, bin, index)

	const body = `{"ops":[` +
		`{"op":"mark","tag":{"label":"Hiring"},"target":{"kind":"meeting"}},` +
		`{"op":"mark","tag":{"label":"severance for Bob"},"target":{"kind":"time-range","startMs":1000,"endMs":2000}},` +
		`{"op":"unmark","itemId":"mk_old"}],` +
		`"actorKind":"agent","actorId":"mallory","actor":{"kind":"agent","id":"mallory"},` +
		`"operationId":"op_run1","expectRevision":0}`
	rec := annTestCall(h, http.MethodPost, "MEETING1", "alice", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	got := annTestDecode(t, rec)
	for key, want := range map[string]string{
		"meetingId": `"MEETING1"`, "revision": "1", "operationId": `"op_1"`,
		"added": `["mk_1"]`, "removed": `[]`, "notFound": `[]`, "resolved": "true",
	} {
		if string(got[key]) != want {
			t.Errorf("%s = %s, want %s", key, got[key], want)
		}
	}
	if !strings.Contains(string(got["annotations"]), `"mk_1"`) {
		t.Errorf("the committed document must be returned: %s", got["annotations"])
	}
	for _, key := range []string{"audioOpusSha256", "containerSha256"} {
		if _, ok := got[key]; ok {
			t.Errorf("%s must not reach the wire", key)
		}
	}

	// The actor is the authenticated caller, whatever the body claims.
	args := annTestRead(t, bin+".args")
	for _, want := range []string{
		"annotate\napply\n", "--actor-id\nalice\n", "--actor-kind\nagent\n", "--operation-id\nop_run1\n",
		"--tag-namespace\n" + annTestNamespace + "\n", "--expect-revision\n0\n", "--json\n",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q do not carry %q", args, want)
		}
	}
	stdin := annTestRead(t, bin+".stdin")
	if strings.Contains(args, "mallory") || strings.Contains(stdin, "mallory") {
		t.Fatalf("an actor id from the body reached the CLI:\nargs=%q\nstdin=%s", args, stdin)
	}

	// The ops reach the CLI on stdin, and a label the archive already uses is
	// given its id; an unknown one is left for the CLI to mint.
	var sent struct {
		Ops []map[string]json.RawMessage `json:"ops"`
	}
	if err := json.Unmarshal([]byte(stdin), &sent); err != nil || len(sent.Ops) != 3 {
		t.Fatalf("stdin is not the ops document: %v (%s)", err, stdin)
	}
	if tag := string(sent.Ops[0]["tag"]); !strings.Contains(tag, `"id":"tag_known"`) || !strings.Contains(tag, `"label":"Hiring"`) {
		t.Errorf("a known label must be resolved to its id and keep its own label: %s", tag)
	}
	if tag := string(sent.Ops[1]["tag"]); strings.Contains(tag, `"id"`) {
		t.Errorf("an unknown label must be left for the CLI to mint: %s", tag)
	}
	if string(sent.Ops[2]["itemId"]) != `"mk_old"` {
		t.Errorf("an unmark must pass through untouched: %v", sent.Ops[2])
	}

	if got := nc.recording(annTestRecording); got != "OPUS-annotated" {
		t.Fatalf("the rewritten recording was not committed; Files holds %q", got)
	}
	if got := nc.ifMatches(); len(got) != 1 || got[0] != `"0"` {
		t.Fatalf("the write must be conditional on the ETag it read, If-Match = %v", got)
	}
	if recorded, _ := index.state(); len(recorded) != 1 || recorded[0] != "MEETING1.opus" {
		t.Fatalf("the projection must be keyed by the opus basename, recorded %v", recorded)
	}
	for _, private := range []string{"Hiring", "severance", "mk_old"} {
		if strings.Contains(logs.String(), private) {
			t.Fatalf("the log carries request content %q:\n%s", private, logs.String())
		}
	}
}

func TestAnnotationsMeetingPOSTRetriesAStaleETag(t *testing.T) {
	nc := newAnnotationsNextcloud(t, "MEETING1.opus")
	nc.setInterfere(1)
	bin := fakeCassini(t, annTestCLIPrints(annTestApplied))
	index := &fakeAnnotationIndex{}
	h, _ := annTestService(t, nc.url, bin, index)

	rec := annTestCall(h, http.MethodPost, "MEETING1", "alice", annTestMark)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 after one retry (%s)", rec.Code, rec.Body.String())
	}
	// The first PUT carried the ETag read before somebody else wrote; the retry
	// re-read and carried theirs.
	if got := nc.ifMatches(); len(got) != 2 || got[0] != `"0"` || got[1] != `"1"` {
		t.Fatalf("If-Match sequence = %v, want [\"0\" \"1\"]", got)
	}
	if runs := annTestRuns(t, bin); runs != 2 {
		t.Fatalf("the ops must be re-applied to the newer copy: %d runs, want 2", runs)
	}
	if got := nc.recording(annTestRecording); got != "OPUS-annotated" {
		t.Fatalf("Files holds %q", got)
	}
	if recorded, _ := index.state(); len(recorded) != 1 {
		t.Fatalf("recorded %v, want one entry", recorded)
	}
}

func TestAnnotationsMeetingPOSTGivesUpAfterThreeStaleETags(t *testing.T) {
	nc := newAnnotationsNextcloud(t, "MEETING1.opus")
	nc.setInterfere(annotateWriteAttempts)
	bin := fakeCassini(t, annTestCLIPrints(annTestApplied))
	index := &fakeAnnotationIndex{}
	h, _ := annTestService(t, nc.url, bin, index)

	rec := annTestCall(h, http.MethodPost, "MEETING1", "alice", annTestMark)
	if rec.Code != http.StatusConflict || annTestError(t, rec) != "conflict" {
		t.Fatalf("code = %d body = %s, want 409 conflict", rec.Code, rec.Body.String())
	}
	if runs := annTestRuns(t, bin); runs != annotateWriteAttempts {
		t.Fatalf("%d runs, want %d", runs, annotateWriteAttempts)
	}
	if got := nc.ifMatches(); len(got) != annotateWriteAttempts {
		t.Fatalf("%d conditional PUTs, want %d: %v", len(got), annotateWriteAttempts, got)
	}
	if got := nc.recording(annTestRecording); got != "OPUS-theirs" {
		t.Fatalf("the other writer's copy must stand; Files holds %q", got)
	}
	if recorded, unavailable := index.state(); len(recorded) != 0 || len(unavailable) != 0 {
		t.Fatalf("nothing was committed, so the projection must not change: recorded=%v unavailable=%v", recorded, unavailable)
	}
}

func TestAnnotationsMeetingPOSTMapsTheCLIRefusals(t *testing.T) {
	for _, tc := range []struct {
		name      string
		code      int
		stderr    string
		want      int
		wantError string
	}{
		{"expect-revision mismatch", annotateExitRevision, "annotations are at revision 4, expected 2", http.StatusConflict, "revision-conflict"},
		{"invalid ops", annotateExitInvalid, `ops[0].tag.label: "severance for Bob" is not allowed here`, http.StatusBadRequest, `ops[0].tag.label: "severance for Bob" is not allowed here`},
		{"unresolved marks", annotateExitUnresolved, "the marks are bound to different audio", http.StatusConflict, "unresolved"},
		{"a runtime failure", annotateExitRuntime, "open /var/lib/cassini/tmp/in-1.opus: input/output error", http.StatusBadGateway, "the recording could not be rewritten"},
		{"a usage error", annotateExitUsage, "unknown flag --bogus", http.StatusBadGateway, "the recording could not be rewritten"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nc := newAnnotationsNextcloud(t, "MEETING1.opus")
			bin := fakeCassini(t, annTestCLIExits(tc.code, tc.stderr))
			index := &fakeAnnotationIndex{}
			h, logs := annTestService(t, nc.url, bin, index)

			rec := annTestCall(h, http.MethodPost, "MEETING1", "alice", annTestMark)
			if rec.Code != tc.want || annTestError(t, rec) != tc.wantError {
				t.Fatalf("code = %d error = %q, want %d %q", rec.Code, annTestError(t, rec), tc.want, tc.wantError)
			}
			if nc.indexOfOp(http.MethodPut, annTestRecording) != -1 || nc.recording(annTestRecording) != "OPUS-original" {
				t.Fatalf("a refused batch must not be written: %v", nc.opsFor(annTestRecording))
			}
			if recorded, unavailable := index.state(); len(recorded) != 0 || len(unavailable) != 0 {
				t.Fatalf("a refused batch must not reach the projection: recorded=%v unavailable=%v", recorded, unavailable)
			}
			switch tc.code {
			case annotateExitInvalid:
				// The reason is the caller's and may quote a label: returned, never logged.
				if strings.Contains(logs.String(), "severance") {
					t.Fatalf("an invalid-ops reason reached the log:\n%s", logs.String())
				}
			case annotateExitRuntime:
				// A runtime reason may name local paths: logged, never returned.
				if strings.Contains(rec.Body.String(), "/var/lib") || !strings.Contains(logs.String(), "input/output error") {
					t.Fatalf("a runtime reason belongs in the log only: body=%s log=%s", rec.Body.String(), logs.String())
				}
			}
		})
	}
}

// Every one of these is refused before a single Nextcloud call, and none of
// them depends on which meeting was named.
func TestAnnotationsMeetingPOSTRefusesABadBodyBeforeCallingNextcloud(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("Nextcloud must not be called for a bad body: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	bin := fakeCassini(t, annTestCLIExits(9, "the CLI must not run for a bad body"))
	h, _ := annTestService(t, srv.URL, bin, &fakeAnnotationIndex{})

	huge := `{"ops":[` + strings.Repeat(" ", maxAnnotateBodyBytes) + `]}`
	const unmark = `{"op":"unmark","itemId":"mk_1"}`
	many := `{"ops":[` + strings.TrimSuffix(strings.Repeat(unmark+",", maxAnnotateOps+1), ",") + `]}`
	for _, tc := range []struct {
		name          string
		body          string
		unknownLength bool
		want          int
	}{
		{"larger than 64 KiB", huge, false, http.StatusRequestEntityTooLarge},
		{"larger than 64 KiB, sent with no length", huge, true, http.StatusRequestEntityTooLarge},
		{"more than 200 ops", many, false, http.StatusBadRequest},
		{"not JSON", "ops", false, http.StatusBadRequest},
		{"no ops", `{}`, false, http.StatusBadRequest},
		{"an empty batch", `{"ops":[]}`, false, http.StatusBadRequest},
		{"an unknown actor kind", `{"ops":[` + unmark + `],"actorKind":"robot"}`, false, http.StatusBadRequest},
		{"an operation id that is not an id", `{"ops":[` + unmark + `],"operationId":"--out"}`, false, http.StatusBadRequest},
		{"a negative revision", `{"ops":[` + unmark + `],"expectRevision":-1}`, false, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/annotations/meetings/MEETING1", strings.NewReader(tc.body))
			if tc.unknownLength {
				req.ContentLength = -1
			}
			req = req.WithContext(appapi.WithUserID(req.Context(), "alice"))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("code = %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
			if annTestError(t, rec) == "" {
				t.Fatalf("a refusal must say why: %s", rec.Body.String())
			}
		})
	}
	// A batch of exactly the limit is accepted as far as the body goes.
	exact := `{"ops":[` + strings.TrimSuffix(strings.Repeat(unmark+",", maxAnnotateOps), ",") + `]}`
	if _, refusal := readAnnotateWriteRequest(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(exact))); refusal != nil {
		t.Fatalf("%d ops must be accepted: %v", maxAnnotateOps, refusal)
	}
	if runs := annTestRuns(t, bin); runs != 0 {
		t.Fatalf("the CLI ran %d times for bad bodies", runs)
	}
}

// A re-PUT must never leave a recording without its rule, and never answer 200
// for a write it could not verify.
func TestAnnotationsMeetingPOSTNeverLeavesARecordingUnprotected(t *testing.T) {
	t.Run("the leaf loses its rule as the write lands", func(t *testing.T) {
		nc := newAnnotationsNextcloud(t, "MEETING1.opus")
		nc.setDropRulesOnPUT()
		index := &fakeAnnotationIndex{}
		h, logs := annTestService(t, nc.url, fakeCassini(t, annTestCLIPrints(annTestApplied)), index)

		rec := annTestCall(h, http.MethodPost, "MEETING1", "alice", annTestMark)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("code = %d, want 502 (%s)", rec.Code, rec.Body.String())
		}
		if !strings.Contains(logs.String(), "readable by every account") {
			t.Fatalf("the log must say what happened:\n%s", logs.String())
		}
		if recorded, unavailable := index.state(); len(recorded) != 0 || len(unavailable) != 1 || unavailable[0] != "MEETING1.opus" {
			t.Fatalf("the projection must stop claiming it describes the file: recorded=%v unavailable=%v", recorded, unavailable)
		}
	})

	t.Run("the upload is truncated", func(t *testing.T) {
		nc := newAnnotationsNextcloud(t, "MEETING1.opus")
		nc.fakeNCFiles.mu.Lock()
		nc.truncatePUT[annTestRecording] = 3
		nc.fakeNCFiles.mu.Unlock()
		index := &fakeAnnotationIndex{}
		h, logs := annTestService(t, nc.url, fakeCassini(t, annTestCLIPrints(annTestApplied)), index)

		rec := annTestCall(h, http.MethodPost, "MEETING1", "alice", annTestMark)
		if rec.Code != http.StatusBadGateway || !strings.Contains(logs.String(), "truncated") {
			t.Fatalf("code = %d, log:\n%s", rec.Code, logs.String())
		}
		if _, unavailable := index.state(); len(unavailable) != 1 {
			t.Fatalf("unavailable = %v, want the meeting", unavailable)
		}
	})

	t.Run("the leaf has no rule to begin with", func(t *testing.T) {
		nc := newAnnotationsNextcloud(t, "MEETING1.opus")
		nc.seed(annTestRecording, "OPUS-original", nil)
		bin := fakeCassini(t, annTestCLIPrints(annTestApplied))
		h, _ := annTestService(t, nc.url, bin, &fakeAnnotationIndex{})

		rec := annTestCall(h, http.MethodPost, "MEETING1", "alice", annTestMark)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("code = %d, want 502 (%s)", rec.Code, rec.Body.String())
		}
		if annTestRuns(t, bin) != 0 || nc.indexOfOp(http.MethodPut, annTestRecording) != -1 {
			t.Fatalf("an unprotected recording must not be rewritten: %v", nc.opsFor(annTestRecording))
		}
	})
}

func TestAnnotationsMeetingPOSTToleratesNoProjection(t *testing.T) {
	nc := newAnnotationsNextcloud(t, "MEETING1.opus")
	bin := fakeCassini(t, annTestCLIPrints(annTestApplied))
	h, logs := annTestService(t, nc.url, bin, nil)

	rec := annTestCall(h, http.MethodPost, "MEETING1", "alice", annTestMark)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 with no projection (%s)", rec.Code, rec.Body.String())
	}
	if nc.recording(annTestRecording) != "OPUS-annotated" {
		t.Fatal("the mark must still be committed")
	}
	if args := annTestRead(t, bin+".args"); strings.Contains(args, "--tag-namespace") {
		t.Fatalf("with no projection there is no installation namespace to pass: %q", args)
	}
	if stdin := annTestRead(t, bin+".stdin"); strings.Contains(stdin, `"id"`) {
		t.Fatalf("with no projection labels are left for the file to resolve: %s", stdin)
	}
	if !strings.Contains(logs.String(), "no projection") {
		t.Fatalf("the degraded mode must be logged:\n%s", logs.String())
	}
}

func TestAnnotationsMeetingPOSTSurvivesAFailingIndexWrite(t *testing.T) {
	nc := newAnnotationsNextcloud(t, "MEETING1.opus")
	index := &fakeAnnotationIndex{recordErr: errors.New("database is locked")}
	h, _ := annTestService(t, nc.url, fakeCassini(t, annTestCLIPrints(annTestApplied)), index)

	rec := annTestCall(h, http.MethodPost, "MEETING1", "alice", annTestMark)
	if rec.Code != http.StatusOK {
		t.Fatalf("the file is the record, so an index failure must not fail the write: %d (%s)", rec.Code, rec.Body.String())
	}
	if _, unavailable := index.state(); len(unavailable) != 1 || unavailable[0] != "MEETING1.opus" {
		t.Fatalf("unavailable = %v, want the meeting marked", unavailable)
	}
}

// A projection that is there and failing is not the same as one that is absent:
// proceeding would mint an id, or a namespace, into the file for good.
func TestAnnotationsMeetingPOSTRefusesWhenTheVocabularyCannotBeRead(t *testing.T) {
	nc := newAnnotationsNextcloud(t, "MEETING1.opus")
	bin := fakeCassini(t, annTestCLIPrints(annTestApplied))
	h, _ := annTestService(t, nc.url, bin, &fakeAnnotationIndex{resolveErr: errors.New("database is locked")})

	rec := annTestCall(h, http.MethodPost, "MEETING1", "alice", annTestMark)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502 (%s)", rec.Code, rec.Body.String())
	}
	if annTestRuns(t, bin) != 0 || nc.indexOfOp(http.MethodPut, annTestRecording) != -1 {
		t.Fatal("nothing may be written on the strength of a lookup that did not happen")
	}
}

// Two writes to one meeting through one operator take turns, so the second
// reads the first's ETag instead of losing a PUT and redoing everything.
func TestAnnotationsMeetingPOSTSerialisesWritesToOneMeeting(t *testing.T) {
	nc := newAnnotationsNextcloud(t, "MEETING1.opus")
	// The pause holds the first write inside its round long enough for the
	// second request to arrive; without the lock both would read ETag "0".
	bin := fakeCassini(t, "sleep 0.3\n"+annTestCLIPrints(annTestApplied))
	h, _ := annTestService(t, nc.url, bin, &fakeAnnotationIndex{})

	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = annTestCall(h, http.MethodPost, "MEETING1", "alice", annTestMark).Code
		}(i)
	}
	wg.Wait()
	if codes[0] != http.StatusOK || codes[1] != http.StatusOK {
		t.Fatalf("codes = %v, want both 200", codes)
	}
	if got := nc.ifMatches(); len(got) != 2 || got[0] != `"0"` || got[1] != `"1"` {
		t.Fatalf("If-Match sequence = %v: the second write must read the first's ETag, not race it", got)
	}
	if runs := annTestRuns(t, bin); runs != 2 {
		t.Fatalf("%d runs, want 2 — a lost PUT means the lock did not hold", runs)
	}
}

// In the default model there is no Team folder: the archive is the service
// account's own private root, read as the owner, and a leaf has no rules there
// by design — so the post-check must not demand one.
func TestAnnotationsMeetingUsesTheDefaultModelsPrivateRoot(t *testing.T) {
	nc := newAnnotationsNextcloud(t)
	setStorageMode(t, false)
	ncAccessSubstrate.reset()
	t.Cleanup(ncAccessSubstrate.reset)
	rel := ncDefaultRecordingsRoot + "/meetings/MEETING1.opus"
	nc.seed(ncDefaultRecordingsRoot+"/catalog.json", annTestCatalog, nil)
	nc.seed(rel, "OPUS-original", nil)
	bin := fakeCassini(t, annTestCLIPrints(annTestApplied))
	index := &fakeAnnotationIndex{}
	h, _ := annTestService(t, nc.url, bin, index)

	if rec := annTestCall(h, http.MethodGet, "MEETING1", "alice", ""); rec.Code != http.StatusOK {
		t.Fatalf("GET code = %d (%s)", rec.Code, rec.Body.String())
	}
	callerGETs, ownerGETs := nc.gets()
	if len(callerGETs) != 0 || len(ownerGETs) != 1 || !strings.HasSuffix(ownerGETs[0], "/"+rel) {
		t.Fatalf("the private root is read as its owner: caller=%v owner=%v", callerGETs, ownerGETs)
	}
	if rec := annTestCall(h, http.MethodPost, "MEETING1", "bob", annTestMark); rec.Code != http.StatusOK {
		t.Fatalf("POST code = %d (%s)", rec.Code, rec.Body.String())
	}
	if nc.recording(rel) != "OPUS-annotated" {
		t.Fatalf("the private root's copy was not rewritten: %q", nc.recording(rel))
	}
	if recorded, _ := index.state(); len(recorded) != 1 || recorded[0] != "MEETING1.opus" {
		t.Fatalf("recorded %v", recorded)
	}
}

func TestKeyedLocksHonourTheContext(t *testing.T) {
	var locks keyedLocks
	release, err := locks.acquire(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	other, err := locks.acquire(context.Background(), "b")
	if err != nil {
		t.Fatal("a different key must not wait")
	}
	other()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := locks.acquire(ctx, "a"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a held key must wait until the context ends, got %v", err)
	}
	release()
	again, err := locks.acquire(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	again()
	locks.mu.Lock()
	defer locks.mu.Unlock()
	if len(locks.locks) != 0 {
		t.Fatalf("released keys must be forgotten, %d remain", len(locks.locks))
	}
}
