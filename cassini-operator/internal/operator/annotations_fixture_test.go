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

	"cassini-operator/internal/operator/appapi"
)

// annotations/meetings/<id> (D-737). MEETING1 is alice's to read; SECRET is in
// the archive and belongs to someone else; NEVER does not exist. The service
// account's side of Nextcloud is fakeNCFiles, so ETags, If-Match and shares
// behave exactly as they do for the publish sink's tests.

const (
	annTestRecording = "CassiniRecordings/meetings/MEETING1.opus"
	annTestSecret    = "CassiniRecordings/meetings/SECRET.opus"
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
// per-caller Files access. The service account reaches the fake directly; a caller sees
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

	nc := &annotationsNextcloud{fakeNCFiles: newFakeNCFiles(), visible: map[string]bool{}}
	for _, name := range visible {
		nc.visible[name] = true
	}
	nc.seed(annTestRecording, "OPUS-original", nil)
	nc.seed(annTestSecret, "OPUS-secret", nil)

	backend, err := url.Parse(nc.fakeNCFiles.server(t).URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(backend)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/files_sharing/api/v1/shares") {
			nc.frontMu.Lock()
			rows := make([]map[string]any, 0)
			for name := range nc.visible {
				id := 42
				if name == "SECRET.opus" {
					id = 43
				}
				rows = append(rows, map[string]any{"id": id, "share_type": 0, "file_source": id, "permissions": 1, "uid_file_owner": ncRecordingsOwner, "item_type": "file", "path": "/" + name})
			}
			nc.frontMu.Unlock()
			body, _ := json.Marshal(map[string]any{"ocs": map[string]any{"meta": map[string]any{"statuscode": 100}, "data": rows}})
			_, _ = w.Write(body)
			return
		}
		if r.Method == "PROPFIND" && strings.HasSuffix(r.URL.Path, "/"+ncRecordingsRoot+"/meetings") {
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = fmt.Fprintf(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:response><d:href>%s</d:href></d:response><d:response><d:href>%s/MEETING1.opus</d:href><d:propstat><d:prop><oc:fileid>42</oc:fileid></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response><d:response><d:href>%s/SECRET.opus</d:href><d:propstat><d:prop><oc:fileid>43</oc:fileid></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`, r.URL.Path, r.URL.Path, r.URL.Path)
			return
		}
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
			r.URL.Path = "/remote.php/dav/files/" + ncRecordingsOwner + "/" + ncRecordingsRoot + "/meetings/" + base
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

func (nc *annotationsNextcloud) seed(rel, body string, rules any) {
	nc.fakeNCFiles.mu.Lock()
	defer nc.fakeNCFiles.mu.Unlock()
	nc.files[rel] = []byte(body)
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
