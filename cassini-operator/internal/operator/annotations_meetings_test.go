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
		case (r.Method == http.MethodGet || r.Method == http.MethodHead) && strings.HasSuffix(base, ".opus"):
			nc.frontMu.Lock()
			if r.Method == http.MethodGet {
				nc.callerGETs = append(nc.callerGETs, r.URL.Path)
			}
			allowed := nc.visible[base]
			nc.frontMu.Unlock()
			if !allowed {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if r.Method == http.MethodHead {
				w.WriteHeader(200)
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

func TestAnnotationsMeetingGETReadsDurableDocument(t *testing.T) {
	nc := newAnnotationsNextcloud(t, "MEETING1.opus")
	store, err := openAnnotationStore(path.Join(t.TempDir(), "annotations.sqlite3"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var result annotateResult
	if err := json.Unmarshal([]byte(annTestApplied), &result); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(context.Background(), "MEETING1.opus", result); err != nil {
		t.Fatal(err)
	}
	h, _ := annTestService(t, nc.url, "must-not-run", store)
	for i := 0; i < 2; i++ {
		rec := annTestCall(h, http.MethodGet, "MEETING1", "alice", "")
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "tag_known") {
			t.Fatalf("GET: %d %s", rec.Code, rec.Body.String())
		}
	}
	caller, owner := nc.gets()
	if len(caller)+len(owner) != 0 {
		t.Fatalf("downloaded media: %v %v", caller, owner)
	}
	nc.frontMu.Lock()
	delete(nc.visible, "MEETING1.opus")
	nc.frontMu.Unlock()
	if rec := annTestCall(h, http.MethodGet, "MEETING1", "alice", ""); rec.Code != 404 {
		t.Fatalf("revoked access: %d", rec.Code)
	}
}

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
