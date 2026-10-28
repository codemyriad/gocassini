package operator

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fakeTemplateRecorder stands in for `cassini insight workflows --json` over a
// templates directory: one built-in, then one row per <id>.md file. A file
// carrying BROKEN is listed with an error, the way the recorder lists a file
// it cannot run; the version is read off the file's own front matter.
const fakeTemplateRecorder = `dir="$CASSINI_TEMPLATES_DIR"
printf '[{"id":"summarise","version":"v0","sha256":"aaaa","name":"Meeting summary","question":"Summarise.","description":"d","origin":"Built in","instruction":"Summarise."}'
for f in "$dir"/*.md; do
  [ -e "$f" ] || continue
  id=$(basename "$f" .md)
  v=$(sed -n 's/^version: //p' "$f")
  if grep -q BROKEN "$f"; then
    printf ',{"id":"%s","version":"","sha256":"","name":"%s","question":"","description":"","origin":"Custom","instruction":"","error":"the instruction is BROKEN"}' "$id" "$id"
  else
    printf ',{"id":"%s","version":"v%s","sha256":"bbbb","name":"%s","question":"Q","description":"","origin":"Custom","instruction":"I"}' "$id" "$v" "$id"
  fi
done
printf ']'
`

func templateWriteRuntime(t *testing.T) (*Runtime, string) {
	t.Helper()
	rt, cleanup := newTestRuntime(t)
	t.Cleanup(cleanup)
	rt.cfg.TemplatesRoot = filepath.Join(t.TempDir(), "templates")
	rt.cfg.CassiniBin = writeFakeCassini(t, fakeTemplateRecorder)
	return rt, rt.cfg.TemplatesRoot
}

func doTemplateRequest(rt *Runtime, method, id, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	for _, route := range operatorAPIRoutes(rt, ExAppConfig{}) {
		mux.Handle(route.pattern, route.handler)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, "/settings/workflows/"+id, strings.NewReader(body)))
	return rec
}

const spanishTemplate = `{"name":"Resumen","question":"Resume la reunión.","description":"En español.","instruction":"Resume la reunión en español.\r\n\r\n## Resumen\n"}`

func TestPutWorkflowWritesTheTemplateFileAndBumpsItsVersion(t *testing.T) {
	rt, dir := templateWriteRuntime(t)

	rec := doTemplateRequest(rt, http.MethodPut, "resumen", spanishTemplate)
	if rec.Code != http.StatusOK {
		t.Fatalf("first save = %d: %s", rec.Code, rec.Body.String())
	}
	var saved workflowView
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.ID != "resumen" || saved.Version != "v1" || saved.Origin != "Custom" {
		t.Errorf("saved = %+v, want resumen v1 Custom", saved)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "resumen.md"))
	if err != nil {
		t.Fatal(err)
	}
	want := "---\nname: \"Resumen\"\nquestion: \"Resume la reunión.\"\ndescription: \"En español.\"\nversion: 1\n---\n\nResume la reunión en español.\n\n## Resumen\n"
	if string(raw) != want {
		t.Errorf("file =\n%s\nwant\n%s", raw, want)
	}

	rec = doTemplateRequest(rt, http.MethodPut, "resumen", spanishTemplate)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"version":"v2"`) {
		t.Fatalf("second save = %d %s, want v2", rec.Code, rec.Body.String())
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".*"))
	if len(leftovers) != 0 {
		t.Errorf("temporary files left behind: %v", leftovers)
	}
}

func TestPutWorkflowRefusesAndRevertsWhatTheRecorderWillNotRun(t *testing.T) {
	rt, dir := templateWriteRuntime(t)
	if rec := doTemplateRequest(rt, http.MethodPut, "resumen", spanishTemplate); rec.Code != http.StatusOK {
		t.Fatalf("seed save = %d: %s", rec.Code, rec.Body.String())
	}
	before, _ := os.ReadFile(filepath.Join(dir, "resumen.md"))

	broken := `{"name":"Resumen","question":"Q","instruction":"BROKEN"}`
	rec := doTemplateRequest(rt, http.MethodPut, "resumen", broken)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not saved: the instruction is BROKEN") {
		t.Fatalf("broken save = %d %s, want 400 with the recorder's reason", rec.Code, rec.Body.String())
	}
	after, _ := os.ReadFile(filepath.Join(dir, "resumen.md"))
	if string(after) != string(before) {
		t.Errorf("the refused save was not reverted:\n%s", after)
	}

	rec = doTemplateRequest(rt, http.MethodPut, "nuevo", broken)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("broken new save = %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "nuevo.md")); !os.IsNotExist(err) {
		t.Errorf("a refused new template left its file behind: %v", err)
	}
}

func TestPutWorkflowRefusesBadRequests(t *testing.T) {
	rt, dir := templateWriteRuntime(t)
	cases := []struct {
		name, id, body, want string
		code                 int
	}{
		{"a built-in", "summarise", spanishTemplate, "built-in template and cannot be changed", 400},
		{"an id of the wrong shape", ".hidden", spanishTemplate, "not a template id", 400},
		{"no name", "x", `{"instruction":"I","question":"Q"}`, "needs a name", 400},
		{"no instruction", "x", `{"name":"X","question":"Q"}`, "needs an instruction", 400},
		{"a two-line name", "x", `{"name":"X\nY","question":"Q","instruction":"I"}`, "one line", 400},
		{"an unknown field", "x", `{"name":"X","instruction":"I","version":9}`, "could not be read", 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doTemplateRequest(rt, http.MethodPut, tc.id, tc.body)
			if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("= %d %s, want %d mentioning %q", rec.Code, rec.Body.String(), tc.code, tc.want)
			}
		})
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("refused saves wrote files: %v", entries)
	}
}

func TestDeleteWorkflowRemovesACustomTemplateOnly(t *testing.T) {
	rt, dir := templateWriteRuntime(t)
	if rec := doTemplateRequest(rt, http.MethodPut, "resumen", spanishTemplate); rec.Code != http.StatusOK {
		t.Fatalf("seed = %d", rec.Code)
	}

	if rec := doTemplateRequest(rt, http.MethodDelete, "summarise", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("deleting a built-in = %d, want 400", rec.Code)
	}
	if rec := doTemplateRequest(rt, http.MethodDelete, "nothing", ""); rec.Code != http.StatusNotFound {
		t.Errorf("deleting a missing template = %d, want 404", rec.Code)
	}

	// In use as the summary template: refused until the step points elsewhere.
	settings := rt.currentLLMSettings()
	settings.Summary.Template = "resumen"
	rt.llmMu.Lock()
	rt.llm = settings
	rt.llmMu.Unlock()
	if rec := doTemplateRequest(rt, http.MethodDelete, "resumen", ""); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "meeting summaries") {
		t.Errorf("deleting the summary template = %d %s, want 409", rec.Code, rec.Body.String())
	}
	settings.Summary.Template = ""
	rt.llmMu.Lock()
	rt.llm = settings
	rt.llmMu.Unlock()

	if rec := doTemplateRequest(rt, http.MethodDelete, "resumen", ""); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "resumen.md")); !os.IsNotExist(err) {
		t.Errorf("the file is still there: %v", err)
	}
	if rec := doTemplateRequest(rt, http.MethodPost, "resumen", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want 405", rec.Code)
	}
}

func TestTheRecorderReadsBackWhatThePanelWrites(t *testing.T) {
	// The file format is the recorder's; the operator writes it with Go quoting
	// so the recorder's parser reads every value back exactly. Pinned here on
	// the rendered bytes, and in the recorder on the parse.
	request := workflowWriteRequest{Name: `Say "hi" — ¡ya!`, Question: "", Description: `a\b`, Instruction: "Answer {{QUESTION}}"}
	file := request.file(4)
	if !strings.Contains(file, `name: "Say \"hi\" — ¡ya!"`) || !strings.Contains(file, `description: "a\\b"`) || !strings.Contains(file, "version: 4\n") {
		t.Errorf("file = %s", file)
	}
}

// The manifest has to let an administrator PUT and DELETE one template while
// keeping both ADMIN. Matched the way AppAPI matches: the first route whose
// pattern and verb both fit.
func TestManifestRoutesTemplateWritesAsAdmin(t *testing.T) {
	routes := manifestRoutes(t)
	for _, tc := range []struct{ path, verb, want string }{
		{"operator/settings/workflows", "GET", "ADMIN"},
		{"operator/settings/workflows/resumen", "PUT", "ADMIN"},
		{"operator/settings/workflows/resumen", "DELETE", "ADMIN"},
	} {
		if got := manifestAccess(routes, tc.path, tc.verb); got != tc.want {
			t.Errorf("%s %s = %q, want %q", tc.verb, tc.path, got, tc.want)
		}
	}
}

type manifestRoute struct {
	URL    string `xml:"url"`
	Verb   string `xml:"verb"`
	Access string `xml:"access_level"`
}

func manifestRoutes(t *testing.T) []manifestRoute {
	t.Helper()
	b, err := os.ReadFile("../../../appinfo/info.xml")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		External struct {
			Routes []manifestRoute `xml:"routes>route"`
		} `xml:"external-app"`
	}
	if err := xml.Unmarshal(b, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest.External.Routes
}

// manifestAccess is the access level of the first route whose pattern and
// verb list both match, or "" when none does (the proxy refuses the request).
func manifestAccess(routes []manifestRoute, path, verb string) string {
	for _, route := range routes {
		if !regexp.MustCompile("(?i)" + route.URL).MatchString(path) {
			continue
		}
		for _, v := range strings.Split(route.Verb, ",") {
			if strings.EqualFold(strings.TrimSpace(v), verb) {
				return route.Access
			}
		}
	}
	return ""
}

// D-819 V4: everybody may pick a template by name, and nobody but an
// administrator sees what it sends.
func TestInsightWorkflowsListsNamesOnlyForEveryone(t *testing.T) {
	rt, dir := templateWriteRuntime(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "roto.md"), []byte("BROKEN"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := doTemplateRequest(rt, http.MethodPut, "resumen", spanishTemplate); rec.Code != http.StatusOK {
		t.Fatalf("seed = %d %s", rec.Code, rec.Body.String())
	}

	mux := http.NewServeMux()
	for _, route := range operatorAPIRoutes(rt, ExAppConfig{}) {
		mux.Handle(route.pattern, route.handler)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/insights/workflows", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("the list may be cached by the proxy")
	}
	if strings.Contains(rec.Body.String(), "instruction") || strings.Contains(rec.Body.String(), "sha256") {
		t.Errorf("the USER list carries prompt bytes or hashes: %s", rec.Body.String())
	}
	var choices []workflowChoice
	if err := json.Unmarshal(rec.Body.Bytes(), &choices); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, choice := range choices {
		ids = append(ids, choice.Origin+":"+choice.ID)
	}
	if strings.Join(ids, ",") != "Built in:summarise,Custom:resumen" {
		t.Errorf("choices = %v, want the runnable ones only", ids)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/insights/workflows", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want 405", rec.Code)
	}
}

func TestInsightWorkflowsSaysWhichTakeAQuestion(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.CassiniBin = insightRegistryCassini(t)
	rec := httptest.NewRecorder()
	rt.insightWorkflowsHandler(rec, httptest.NewRequest(http.MethodGet, "/insights/workflows", nil))
	var choices []workflowChoice
	if err := json.Unmarshal(rec.Body.Bytes(), &choices); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	takes := map[string]bool{}
	for _, choice := range choices {
		takes[choice.ID] = choice.TakesQuestion
	}
	if takes["summarise"] || !takes["ask"] || len(takes) != 2 {
		t.Errorf("takesQuestion = %v, want ask only (and no broken roto)", takes)
	}
}

func TestManifestRoutesTheTemplateNamesForEveryUser(t *testing.T) {
	if got := manifestAccess(manifestRoutes(t), "operator/insights/workflows", "GET"); got != "USER" {
		t.Errorf("GET operator/insights/workflows = %q, want USER", got)
	}
}
