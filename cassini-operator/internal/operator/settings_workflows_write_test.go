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
	"time"
)

// fakeTemplateRecorder stands in for `cassini insight workflows --json` over a
// templates directory: one built-in, then one row per <id>.md file. A file
// carrying BROKEN is listed with an error, the way the recorder lists a file
// it cannot run; the version is read off the file's own front matter.
// fakeTemplateRecorder lists a broken file with the version its front matter
// still names, as the recorder's salvageCustomTemplate does.
const fakeTemplateRecorder = `dir="$CASSINI_TEMPLATES_DIR"
printf '[{"id":"summarise","version":"v0","sha256":"aaaa","name":"Meeting summary","description":"d","origin":"Built in","instruction":"Summarise."}'
for f in "$dir"/*.md; do
  [ -e "$f" ] || continue
  id=$(basename "$f" .md)
  v=$(sed -n 's/^version: //p' "$f")
  if grep -q BROKEN "$f"; then
    printf ',{"id":"%s","version":"%s","sha256":"","name":"%s","description":"","origin":"Custom","instruction":"","error":"the instruction is BROKEN"}' "$id" "${v:+v$v}" "$id"
  else
    printf ',{"id":"%s","version":"v%s","sha256":"bbbb","name":"%s","description":"","origin":"Custom","instruction":"I"}' "$id" "$v" "$id"
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

const spanishTemplate = `{"name":"Resumen","description":"En español.","instruction":"Resume la reunión en español.\r\n\r\n## Resumen\n"}`

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
	want := "---\nname: \"Resumen\"\ndescription: \"En español.\"\nversion: 1\n---\n\nResume la reunión en español.\n\n## Resumen\n"
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

	broken := `{"name":"Resumen","instruction":"BROKEN"}`
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
		{"no name", "x", `{"instruction":"I"}`, "needs a name", 400},
		{"no instruction", "x", `{"name":"X"}`, "needs an instruction", 400},
		{"a two-line name", "x", `{"name":"X\nY","instruction":"I"}`, "one line", 400},
		{"a two-line description", "x", `{"name":"X","description":"a\nb","instruction":"I"}`, "one line", 400},
		{"a question", "x", `{"name":"X","question":"Q","instruction":"I"}`, "could not be read", 400},
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
	request := workflowWriteRequest{Name: `Say "hi" — ¡ya!`, Description: `a\b`, Instruction: "Answer {{QUESTION}}"}
	file := request.file(4)
	want := "---\nname: \"Say \\\"hi\\\" — ¡ya!\"\ndescription: \"a\\\\b\"\nversion: 4\n---\n\nAnswer {{QUESTION}}\n"
	if file != want {
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
	var listing workflowChoices
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if listing.Default != "summarise" {
		t.Errorf("default = %q, want summarise with no insight template configured", listing.Default)
	}
	ids := []string{}
	for _, choice := range listing.Templates {
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
	custom := `,{"id":"resumen","version":"v1","sha256":"cccc","name":"Resumen","description":"En español.","origin":"Custom","instruction":"Resume la reunión."}]`
	listing := strings.TrimSuffix(insightRegistryJSON, "]") + custom
	rt.cfg.CassiniBin = writeFakeCassini(t, `printf '%s' '`+listing+`'`)
	rec := httptest.NewRecorder()
	rt.insightWorkflowsHandler(rec, httptest.NewRequest(http.MethodGet, "/insights/workflows", nil))
	var choices workflowChoices
	if err := json.Unmarshal(rec.Body.Bytes(), &choices); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	takes := map[string]bool{}
	for _, choice := range choices.Templates {
		takes[choice.ID] = choice.TakesQuestion
	}
	if takes["summarise"] || !takes["ask"] || takes["resumen"] || len(takes) != 3 {
		t.Errorf("takesQuestion = %v, want ask only (a custom template takes none, and no broken roto)", takes)
	}
}

func TestManifestRoutesTheTemplateNamesForEveryUser(t *testing.T) {
	if got := manifestAccess(manifestRoutes(t), "operator/insights/workflows", "GET"); got != "USER" {
		t.Errorf("GET operator/insights/workflows = %q, want USER", got)
	}
}

// The Generate card preselects the deployment's default: the configured
// insight template when it can run, else the built-in summary (D-819).
func TestInsightWorkflowsNamesTheDeploymentDefault(t *testing.T) {
	rt, dir := templateWriteRuntime(t)
	if rec := doTemplateRequest(rt, http.MethodPut, "resumen", spanishTemplate); rec.Code != http.StatusOK {
		t.Fatalf("seed = %d %s", rec.Code, rec.Body.String())
	}
	if err := os.WriteFile(filepath.Join(dir, "roto.md"), []byte("BROKEN"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ configured, want string }{
		{"", "summarise"},
		{"resumen", "resumen"},
		{"roto", "summarise"},
		{"missing", "summarise"},
	} {
		settings := rt.currentLLMSettings()
		settings.Insight.Template = tc.configured
		rt.setLLMSettings(settings)
		rec := httptest.NewRecorder()
		rt.insightWorkflowsHandler(rec, httptest.NewRequest(http.MethodGet, "/insights/workflows", nil))
		var listing workflowChoices
		if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
			t.Fatalf("%v: %s", err, rec.Body.String())
		}
		if listing.Default != tc.want {
			t.Errorf("configured %q: default = %q, want %q", tc.configured, listing.Default, tc.want)
		}
	}
}

// countingTemplateRuntime is templateWriteRuntime with a recorder that counts
// how often it is spawned.
func countingTemplateRuntime(t *testing.T) (*Runtime, string, func() int) {
	t.Helper()
	rt, dir := templateWriteRuntime(t)
	count := filepath.Join(t.TempDir(), "spawns")
	rt.cfg.CassiniBin = writeFakeCassini(t, "echo x >> '"+count+"'\n"+fakeTemplateRecorder)
	return rt, dir, func() int {
		raw, err := os.ReadFile(count)
		if os.IsNotExist(err) {
			return 0
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(raw), "x")
	}
}

func getTemplateChoices(t *testing.T, rt *Runtime) []string {
	t.Helper()
	rec := httptest.NewRecorder()
	rt.insightWorkflowsHandler(rec, httptest.NewRequest(http.MethodGet, "/insights/workflows", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET = %d (Cache-Control %q) %s", rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
	}
	var listing workflowChoices
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, choice := range listing.Templates {
		ids = append(ids, choice.ID)
	}
	return ids
}

// A GET reads the cached listing while nothing it was read from changed; a
// save lists twice — the candidate alone in staging, then the templates root
// once it is in place — and that second listing is what the next GET serves.
func TestWorkflowListingIsCachedUntilATemplateChanges(t *testing.T) {
	rt, _, spawns := countingTemplateRuntime(t)
	getTemplateChoices(t, rt)
	getTemplateChoices(t, rt)
	if n := spawns(); n != 1 {
		t.Fatalf("two GETs spawned the recorder %d times, want 1", n)
	}

	if rec := doTemplateRequest(rt, http.MethodPut, "resumen", spanishTemplate); rec.Code != http.StatusOK {
		t.Fatalf("save = %d %s", rec.Code, rec.Body.String())
	}
	if n := spawns(); n != 3 {
		t.Errorf("a save spawned the recorder %d times, want twice (the staged check, then the listing kept)", n-1)
	}
	if ids := getTemplateChoices(t, rt); strings.Join(ids, ",") != "summarise,resumen" {
		t.Errorf("after the save = %v", ids)
	}
	if n := spawns(); n != 3 {
		t.Errorf("the GET after a save spawned the recorder again (%d)", n)
	}

	if rec := doTemplateRequest(rt, http.MethodDelete, "resumen", ""); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	if ids := getTemplateChoices(t, rt); strings.Join(ids, ",") != "summarise" {
		t.Errorf("after the delete = %v", ids)
	}
}

// A file changed behind the operator's back (an administrator's shell) is
// picked up on the next GET.
func TestWorkflowListingRefreshesWhenTheDirectoryChanges(t *testing.T) {
	rt, dir, spawns := countingTemplateRuntime(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	getTemplateChoices(t, rt)
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("---\nversion: 1\n---\nA\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(dir, later, later); err != nil {
		t.Fatal(err)
	}
	if ids := getTemplateChoices(t, rt); strings.Join(ids, ",") != "summarise,a" {
		t.Errorf("after a file appeared = %v", ids)
	}
	if n := spawns(); n != 2 {
		t.Errorf("spawns = %d, want 2", n)
	}
}

// A file the recorder cannot parse is listed with the version its front
// matter still names, and saving over it continues from there.
func TestPutWorkflowContinuesTheVersionOfABrokenFile(t *testing.T) {
	rt, dir := templateWriteRuntime(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "resumen.md"), []byte("---\nname: \"Resumen\"\nversion: 3\nBROKEN: yes\n---\n\nResume.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := doTemplateRequest(rt, http.MethodPut, "resumen", spanishTemplate)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"version":"v4"`) {
		t.Fatalf("save over a broken v3 = %d %s, want v4", rec.Code, rec.Body.String())
	}
}

// JSON escaping makes an instruction's body larger than the instruction: one
// within the limit is saved, one over it is refused by the size check, not by
// the body cap.
func TestPutWorkflowMeasuresTheInstructionNotItsJSON(t *testing.T) {
	rt, _ := templateWriteRuntime(t)
	body := func(n int) string {
		raw, err := json.Marshal(workflowWriteRequest{Name: "Long", Instruction: strings.Repeat("a\n", n/2)})
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	if rec := doTemplateRequest(rt, http.MethodPut, "long", body(190<<10)); rec.Code != http.StatusOK {
		t.Errorf("190 KiB instruction = %d %s", rec.Code, rec.Body.String())
	}
	rec := doTemplateRequest(rt, http.MethodPut, "longer", body(210<<10))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "the instruction is longer than") {
		t.Errorf("210 KiB instruction = %d %s, want the size check's message", rec.Code, rec.Body.String())
	}
}

// A PUT with If-None-Match: * creates and never replaces: the panel sends it
// for a new template, whose id comes from a list that may be stale.
func TestPutWorkflowCreateOnlyRefusesAnExistingFile(t *testing.T) {
	rt, dir := templateWriteRuntime(t)
	put := func(createOnly bool) *httptest.ResponseRecorder {
		mux := http.NewServeMux()
		for _, route := range operatorAPIRoutes(rt, ExAppConfig{}) {
			mux.Handle(route.pattern, route.handler)
		}
		req := httptest.NewRequest(http.MethodPut, "/settings/workflows/resumen", strings.NewReader(spanishTemplate))
		if createOnly {
			req.Header.Set("If-None-Match", "*")
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := put(true); rec.Code != http.StatusOK {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	before, _ := os.ReadFile(filepath.Join(dir, "resumen.md"))
	if rec := put(true); rec.Code != http.StatusPreconditionFailed || !strings.Contains(rec.Body.String(), "already exists") {
		t.Errorf("second create = %d %s, want 412", rec.Code, rec.Body.String())
	}
	if after, _ := os.ReadFile(filepath.Join(dir, "resumen.md")); string(after) != string(before) {
		t.Errorf("the refused create changed the file:\n%s", after)
	}
	if rec := put(false); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"version":"v2"`) {
		t.Errorf("an edit = %d %s, want v2", rec.Code, rec.Body.String())
	}
}

// A broken file that shares a built-in's id never runs, so deleting it is
// allowed while that built-in is a step's template: the built-in still
// answers to the id.
func TestDeleteWorkflowRemovesAFileShadowingTheConfiguredBuiltIn(t *testing.T) {
	rt, dir := templateWriteRuntime(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "summarise.md"), []byte("---\nversion: 1\n---\nShadow\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	settings := rt.currentLLMSettings()
	settings.Summary.Template = "summarise"
	rt.setLLMSettings(settings)
	if rec := doTemplateRequest(rt, http.MethodDelete, "summarise", ""); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "summarise.md")); !os.IsNotExist(err) {
		t.Errorf("the shadowing file is still there: %v", err)
	}
}

// A recorder binary replaced at the same path is listed afresh, without a
// template change or a restart.
func TestWorkflowListingRefreshesWhenTheBinaryChanges(t *testing.T) {
	rt, _, spawns := countingTemplateRuntime(t)
	getTemplateChoices(t, rt)
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(rt.cfg.CassiniBin, later, later); err != nil {
		t.Fatal(err)
	}
	getTemplateChoices(t, rt)
	if n := spawns(); n != 2 {
		t.Errorf("spawns = %d, want 2: the replaced binary was not listed again", n)
	}
}

// A candidate is checked alone in a staging directory and only a file the
// recorder will run reaches the templates root: a refused save is never
// where a summary build or an insight run could read it.
func TestPutWorkflowChecksTheCandidateBeforeItIsLive(t *testing.T) {
	rt, dir := templateWriteRuntime(t)
	listed := filepath.Join(t.TempDir(), "listed")
	// Log the directory each listing was asked for, and whether the live
	// root held the candidate at that moment.
	rt.cfg.CassiniBin = writeFakeCassini(t, `echo "$CASSINI_TEMPLATES_DIR $(cat '`+dir+`/resumen.md' 2>/dev/null | grep -c BROKEN)" >> '`+listed+`'
`+fakeTemplateRecorder)
	broken := `{"name":"Resumen","instruction":"BROKEN"}`
	if rec := doTemplateRequest(rt, http.MethodPut, "resumen", broken); rec.Code != http.StatusBadRequest {
		t.Fatalf("broken save = %d %s, want 400", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "resumen.md")); !os.IsNotExist(err) {
		t.Errorf("the refused file reached the templates root: %v", err)
	}
	raw, err := os.ReadFile(listed)
	if err != nil {
		t.Fatal(err)
	}
	staged := false
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] != "0" {
			t.Errorf("a listing saw the candidate in the live root: %s", line)
		}
		if strings.HasPrefix(fields[0], filepath.Join(dir, ".check-")) {
			staged = true
		}
	}
	if !staged {
		t.Errorf("no listing was made from a staging directory:\n%s", raw)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".check-*")); len(leftovers) != 0 {
		t.Errorf("staging directories left behind: %v", leftovers)
	}
}

// An id that is deleted and created again continues its versions: earlier
// documents recorded v1 and v2 with other bytes.
func TestPutWorkflowDoesNotReuseTheVersionsOfADeletedTemplate(t *testing.T) {
	rt, _ := templateWriteRuntime(t)
	for i := 0; i < 2; i++ {
		if rec := doTemplateRequest(rt, http.MethodPut, "resumen", spanishTemplate); rec.Code != http.StatusOK {
			t.Fatalf("save %d = %d %s", i, rec.Code, rec.Body.String())
		}
	}
	if rec := doTemplateRequest(rt, http.MethodDelete, "resumen", ""); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	rec := doTemplateRequest(rt, http.MethodPut, "resumen", spanishTemplate)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"version":"v3"`) {
		t.Errorf("recreated = %d %s, want v3", rec.Code, rec.Body.String())
	}
}

// A file whose stem is not a usable id is listed broken under that stem; it
// cannot be saved under it, but it can be deleted. A path is never accepted.
func TestDeleteWorkflowRemovesAFileWithAnUnusableName(t *testing.T) {
	rt, dir := templateWriteRuntime(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Team notes.md"), []byte("BROKEN"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := doTemplateRequest(rt, http.MethodPut, "Team%20notes", spanishTemplate); rec.Code != http.StatusBadRequest {
		t.Errorf("PUT to an unusable id = %d, want 400", rec.Code)
	}
	if rec := doTemplateRequest(rt, http.MethodDelete, "Team%20notes", ""); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "Team notes.md")); !os.IsNotExist(err) {
		t.Errorf("the file is still there: %v", err)
	}
	for _, id := range []string{".versions", "..%2Fx", "a%2Fb"} {
		if rec := doTemplateRequest(rt, http.MethodDelete, id, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("DELETE %s = %d, want 400", id, rec.Code)
		}
	}
}

// The 409 names the step that uses the template in a field of its own, so
// the panel never reads it out of a sentence that also quotes the id.
func TestDeleteWorkflowInUseNamesTheStep(t *testing.T) {
	rt, _ := templateWriteRuntime(t)
	if rec := doTemplateRequest(rt, http.MethodPut, "weekly-summary", spanishTemplate); rec.Code != http.StatusOK {
		t.Fatalf("seed = %d %s", rec.Code, rec.Body.String())
	}
	settings := rt.currentLLMSettings()
	settings.Insight.Template = "weekly-summary"
	rt.setLLMSettings(settings)
	rec := doTemplateRequest(rt, http.MethodDelete, "weekly-summary", "")
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusConflict || body["step"] != "insight" {
		t.Errorf("delete in use = %d %v, want 409 naming the insight step", rec.Code, body)
	}
}

// A step can only be pointed at a template that runs: one deleted a moment
// ago is refused, so the delete's in-use check cannot be raced.
func TestPutLLMSettingsRefusesAStepTemplateThatCannotRun(t *testing.T) {
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
	put := func(template string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		rt.llmSettingsHandler(rec, httptest.NewRequest(http.MethodPut, "/settings/llm", strings.NewReader(`{"summary":{"template":"`+template+`"}}`)))
		return rec
	}
	for template, want := range map[string]int{"missing": http.StatusBadRequest, "roto": http.StatusBadRequest, "resumen": http.StatusOK} {
		if rec := put(template); rec.Code != want {
			t.Errorf("summary template %q = %d %s, want %d", template, rec.Code, rec.Body.String(), want)
		}
	}
}

// A directory whose templates break the listing is put right by deleting one,
// so a delete goes ahead without the listing. A step that names the id still
// stops it: with no listing the id is taken to be no built-in's.
func TestDeleteWorkflowGoesAheadWhenTheListingFails(t *testing.T) {
	rt, dir := templateWriteRuntime(t)
	if rec := doTemplateRequest(rt, http.MethodPut, "resumen", spanishTemplate); rec.Code != http.StatusOK {
		t.Fatalf("save = %d %s", rec.Code, rec.Body.String())
	}
	if rec := doTemplateRequest(rt, http.MethodPut, "otro", spanishTemplate); rec.Code != http.StatusOK {
		t.Fatalf("save = %d %s", rec.Code, rec.Body.String())
	}
	rt.cfg.CassiniBin = writeFakeCassini(t, `echo "listing too large" >&2; exit 1`)
	rt.workflowListing.invalidate()

	settings := rt.currentLLMSettings()
	settings.Insight.Template = "otro"
	rt.setLLMSettings(settings)
	if rec := doTemplateRequest(rt, http.MethodDelete, "otro", ""); rec.Code != http.StatusConflict {
		t.Errorf("delete of the insight step's template = %d %s, want 409", rec.Code, rec.Body.String())
	}
	if rec := doTemplateRequest(rt, http.MethodDelete, "resumen", ""); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "resumen.md")); !os.IsNotExist(err) {
		t.Errorf("the file is still there: %v", err)
	}
}

// The ledger only keeps versions from going back. One that does not decode is
// read as empty, so saves and deletes keep working, and the next delete
// rewrites it.
func TestADamagedVersionLedgerDoesNotStopSavesOrDeletes(t *testing.T) {
	rt, dir := templateWriteRuntime(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ledger := filepath.Join(dir, ".versions.json")
	if err := os.WriteFile(ledger, []byte(`{"resumen": 4`), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := doTemplateRequest(rt, http.MethodPut, "resumen", spanishTemplate); rec.Code != http.StatusOK {
		t.Fatalf("save = %d %s", rec.Code, rec.Body.String())
	}
	if rec := doTemplateRequest(rt, http.MethodDelete, "resumen", ""); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	raw, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]int
	if err := json.Unmarshal(raw, &got); err != nil || got["resumen"] != 1 {
		t.Errorf("ledger = %s (%v), want it rewritten with resumen: 1", raw, err)
	}
}

// A staging directory left by a save that was killed mid-check is removed by
// the next save, rather than staying on the volume for good.
func TestPutWorkflowSweepsStagingLeftByAKilledSave(t *testing.T) {
	rt, dir := templateWriteRuntime(t)
	leftover := filepath.Join(dir, ".check-123456")
	if err := os.MkdirAll(leftover, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leftover, "resumen.md"), []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := doTemplateRequest(rt, http.MethodPut, "resumen", spanishTemplate); rec.Code != http.StatusOK {
		t.Fatalf("save = %d %s", rec.Code, rec.Body.String())
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".check-*")); len(leftovers) != 0 {
		t.Errorf("staging directories left behind: %v", leftovers)
	}
}
