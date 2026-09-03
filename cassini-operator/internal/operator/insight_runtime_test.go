package operator

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// An insight is a document somebody asked for by name, so the two properties
// that matter are tested separately here: it can only ever be assembled out of
// meetings the requester could already read, and the answer lands in files the
// requester owns — never in the shared archive, and never on top of something
// already there.

// insightTestCatalog is the authoritative catalog the stub Nextcloud holds.
// alice may read MEETING1 and MEETING2; SECRET belongs to somebody else.
const insightTestCatalog = `{"version":"cassini.viewer.catalog.v1","meetings":[` +
	`{"id":"MEETING1","title":"Daily Standup","dateLabel":"2026-08-11 10:32",` +
	`"audioPath":"./meetings/MEETING1.opus","roomId":"rm_9f2a1c3d4e5b6a70","roomName":"Weekly Sync"},` +
	`{"id":"MEETING2","title":"Backlog Review","dateLabel":"2026-08-18 09:00",` +
	`"audioPath":"./meetings/MEETING2.opus","roomId":"rm_11bb22cc33dd44ee","roomName":"Backlog"},` +
	`{"id":"SECRET","title":"Someone else's","dateLabel":"2026-08-19 09:00",` +
	`"audioPath":"./meetings/SECRET.opus"}]}`

// insightDAV is a stub Nextcloud covering every call one run makes: the
// authoritative catalog as the owner, the caller's Depth-1 scan, a per-meeting
// GET as the caller, and the MKCOL/HEAD/PUT trio that delivers the answer into
// the caller's own home.
type insightDAV struct {
	server *httptest.Server

	mu sync.Mutex
	// calls counts every request that reached Nextcloud, so a test can assert
	// that a refusal cost nothing.
	calls int
	// existing is the caller's home before the run — a path here answers HEAD
	// 200, which is how "do not overwrite" is exercised.
	existing map[string]bool
	// put records each delivered path in order, with the bytes written.
	put []insightPut
	// mkcol records each collection the run created.
	mkcol []string
	// documents is the caller's home as a reader sees it, so the read path can
	// be exercised against a file the requester still has and one they moved.
	documents map[string]string
}

type insightPut struct {
	path string
	body string
}

func newInsightDAV(t *testing.T, catalog string, visible ...string) *insightDAV {
	t.Helper()
	dav := &insightDAV{existing: map[string]bool{}, documents: map[string]string{}}
	allowed := make(map[string]bool, len(visible))
	for _, name := range visible {
		allowed[name] = true
	}
	dav.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := filepath.Base(r.URL.Path)
		_, rel, isHome := strings.Cut(r.URL.Path, "/remote.php/dav/files/")
		dav.mu.Lock()
		dav.calls++
		dav.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && base == "catalog.json":
			_, _ = w.Write([]byte(catalog))
		case r.Method == "PROPFIND":
			w.WriteHeader(http.StatusMultiStatus)
			body := strings.Builder{}
			body.WriteString(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:">`)
			body.WriteString(`<d:response><d:href>/remote.php/dav/files/alice/Cassini/Recordings/meetings/</d:href></d:response>`)
			for _, name := range visible {
				body.WriteString(`<d:response><d:href>/remote.php/dav/files/alice/Cassini/Recordings/meetings/` + name + `</d:href></d:response>`)
			}
			body.WriteString(`</d:multistatus>`)
			_, _ = w.Write([]byte(body.String()))
		case r.Method == http.MethodGet && strings.HasSuffix(base, ".md"):
			dav.mu.Lock()
			body, held := dav.documents[rel]
			dav.mu.Unlock()
			if !held {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(body))
		case r.Method == http.MethodGet && strings.HasSuffix(base, ".opus"):
			if !strings.Contains(r.URL.Path, "/files/alice/") {
				t.Errorf("recording fetched as %s, want the caller", r.URL.Path)
			}
			if !allowed[base] {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte("OPUSBYTES"))
		case r.Method == "MKCOL":
			dav.mu.Lock()
			dav.mkcol = append(dav.mkcol, rel)
			dav.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodHead:
			dav.mu.Lock()
			taken := dav.existing[rel]
			dav.mu.Unlock()
			if taken {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPut:
			if !isHome || !strings.HasPrefix(rel, "alice/") {
				t.Errorf("insight PUT to %s, want the caller's own home", r.URL.Path)
			}
			body, _ := io.ReadAll(r.Body)
			dav.mu.Lock()
			dav.put = append(dav.put, insightPut{path: rel, body: string(body)})
			dav.existing[rel] = true
			dav.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(dav.server.Close)
	return dav
}

func (d *insightDAV) delivered() []insightPut {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]insightPut(nil), d.put...)
}

func (d *insightDAV) collectionsCreated() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.mkcol...)
}

func (d *insightDAV) requestCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func (d *insightDAV) holdDocument(relPath, body string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.documents[relPath] = body
	d.existing[relPath] = true
}

func (d *insightDAV) dropDocument(relPath string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.documents, relPath)
	delete(d.existing, relPath)
}

func (d *insightDAV) markExisting(relPath string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.existing[relPath] = true
}

// fakeInsightCassini stands in for the CLI. It answers both verbs a run makes,
// records the argv of each, and exits with the code the test wants from
// `insight run` — which is the only way the exit-code contract can be exercised
// without a model endpoint.
func fakeInsightCassini(t *testing.T, document string, insightExit int) (bin, argvPath string) {
	t.Helper()
	dir := t.TempDir()
	argvPath = filepath.Join(dir, "argv")
	docPath := filepath.Join(dir, "document")
	if err := os.WriteFile(docPath, []byte(document), 0o600); err != nil {
		t.Fatalf("write document: %v", err)
	}
	script := `printf '%s\n' "$@" >> ` + argvPath + `
verb="$1 $2"
if [ "$verb" = "meetings context" ]; then
  printf '%s' '{"version":"cassini.meetings.context.v1","meetings":[]}'
  exit 0
fi
record=""
while [ $# -gt 0 ]; do
  if [ "$1" = "--record" ]; then record="$2"; fi
  shift
done
if [ -n "$record" ]; then printf '%s' '{"provider":{"kind":"openai-compatible","baseUrl":"http://model.invalid/v1","model":"llama-3.1-8b"}}' > "$record"; fi
cat ` + docPath + `
exit ` + strconv.Itoa(insightExit) + `
`
	return writeFakeCassini(t, script), argvPath
}

// insightTestService builds a service over a hand-made Runtime: the run path
// needs a context, a CLI path, an LLM policy and a store, and nothing else the
// full NewRuntime brings with it.
func insightTestService(t *testing.T, ncURL, bin string, store insightRunStore) (*insightService, *bytes.Buffer) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	logs := &bytes.Buffer{}
	rt := &Runtime{
		ctx:    ctx,
		cancel: cancel,
		cfg:    Config{CassiniBin: bin},
		logger: log.New(logs, "", 0),
		llm: LLMSettings{
			Providers: []LLMProvider{{ID: "local", Name: "Local", BaseURL: "http://model.invalid/v1", APIKey: "sk-test"}},
			Summary:   LLMStep{Enabled: true, Provider: "local", Model: "llama-3.1-8b"},
		},
	}
	exapp := testExAppConfig(ncURL)
	exapp.PublishSink = publishSinkNextcloudFiles
	exapp.CassiniBin = bin
	return &insightService{
		rt:       rt,
		exapp:    exapp,
		store:    store,
		client:   &http.Client{},
		logger:   rt.logger,
		slots:    make(chan struct{}, maxConcurrentInsightRuns),
		now:      func() time.Time { return time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC) },
		newID:    func() (string, error) { return "ins_0123456789abcdef", nil },
		launchFn: func(string, bool) {},
	}, logs
}

func insightTestRun() InsightRun {
	return InsightRun{
		ID:            "ins_0123456789abcdef",
		CreatedBy:     "alice",
		Status:        insightStatusRunning,
		WorkflowID:    "summarise",
		MeetingIDs:    []string{"MEETING1", "MEETING2"},
		AttemptNumber: 1,
	}
}

// The delivery root must not be inside the recordings Team folder. Every account
// has "Cassini" mounted read-only from the Everyone group, so a path under it in
// a caller's home is not their own storage at all — it is a write into the
// shared archive, which the service account alone may perform and which is the
// wrong place for a personal document even where it is allowed.
func TestInsightsAreDeliveredOutsideTheRecordingsMount(t *testing.T) {
	if firstPathSegment(ncInsightsRoot) == ncRecordingsMount {
		t.Fatalf("ncInsightsRoot = %q is inside the %q Team-folder mount; an insight would be a write into the shared archive", ncInsightsRoot, ncRecordingsMount)
	}
	if got, want := insightFolderChain(), []string{ncInsightsRoot}; len(got) != len(want) || got[0] != want[0] {
		t.Errorf("insightFolderChain() = %v, want %v", got, want)
	}
}

func TestInsightDocumentNameCarriesTheDayTheWorkflowAndTheRun(t *testing.T) {
	name := insightDocumentBaseName(insightTestRun(), time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC))
	if name != "2026-09-03-summarise-ins_0123456789abcdef" {
		t.Errorf("insightDocumentBaseName() = %q", name)
	}
}

// The child that asks the model needs the endpoint credential and nothing else
// the operator holds. APP_SECRET is the sharpest case: a process carrying it can
// act as any Nextcloud account.
func TestInsightChildEnvKeepsTheModelCredentialAndDropsTheOperatorSecret(t *testing.T) {
	t.Setenv("APP_SECRET", "the-appapi-shared-secret")
	t.Setenv("CASSINI_TALK_RECORDING_SECRET", "talk-secret")
	t.Setenv("CASSINI_OPERATOR_API_TOKEN", "bearer-token")
	service, _ := insightTestService(t, "http://nextcloud.invalid", "/bin/true", nil)

	env := service.insightChildEnv()
	assertEnvAbsent(t, env, "APP_SECRET", "CASSINI_TALK_RECORDING_SECRET", "CASSINI_OPERATOR_API_TOKEN")
	assertEnvPresent(t, env, "SUMMARY_BASE_URL=http://model.invalid/v1", "SUMMARY_API_KEY=sk-test")
	// The child still has to be able to run at all.
	assertEnvKeyPresent(t, env, "PATH")
}

// The child that only assembles a bundle never calls a model, so an endpoint
// credential in its environment could only ever be a credential in one more
// place.
func TestContextChildEnvDropsTheModelCredentialToo(t *testing.T) {
	t.Setenv("APP_SECRET", "the-appapi-shared-secret")
	service, _ := insightTestService(t, "http://nextcloud.invalid", "/bin/true", nil)

	env := service.contextChildEnv()
	assertEnvAbsent(t, env, "APP_SECRET")
	for _, key := range []string{"SUMMARY_BASE_URL", "SUMMARY_API_KEY", "INSIGHT_BASE_URL", "OPENROUTER_API_KEY", "LLM_BASE_URL"} {
		assertEnvKeyAbsent(t, env, key)
	}
	assertEnvKeyPresent(t, env, "PATH")
}

func assertEnvPresent(t *testing.T, env []string, want ...string) {
	t.Helper()
	for _, entry := range want {
		found := false
		for _, kv := range env {
			if kv == entry {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("child environment is missing %q", entry)
		}
	}
}

func assertEnvAbsent(t *testing.T, env []string, keys ...string) {
	t.Helper()
	for _, key := range keys {
		assertEnvKeyAbsent(t, env, key)
	}
}

func assertEnvKeyAbsent(t *testing.T, env []string, key string) {
	t.Helper()
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			t.Errorf("child environment still carries %s", key)
		}
	}
}

func assertEnvKeyPresent(t *testing.T, env []string, key string) {
	t.Helper()
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			return
		}
	}
	t.Errorf("child environment lost %s, so the child cannot run", key)
}

// The exit code is the contract, so each one has to produce a different next
// action. A message that read the same for "no endpoint" and "the model timed
// out" would leave the reader with nothing to do.
func TestExplainInsightExitGivesEachCodeItsOwnAction(t *testing.T) {
	seen := map[string]int{}
	for _, code := range []int{1, 2, 3, 4, 5, -1} {
		message := explainInsightExit(context.Background(), code)
		if strings.TrimSpace(message) == "" {
			t.Fatalf("exit %d produced no message; a failed card with no message is a spinner that stopped", code)
		}
		if previous, repeated := seen[message]; repeated {
			t.Errorf("exit %d and exit %d produce the same message %q", previous, code, message)
		}
		seen[message] = code
	}
	if !strings.Contains(explainInsightExit(context.Background(), 3), "No AI endpoint is configured") {
		t.Error("exit 3 must say that no endpoint is configured")
	}
	// The app decides which failure this is off the token, never off the prose
	// (cassini-app/src/insights/client.ts, classifyRunError). Without it every
	// failure reads as "The insight failed", and "there is no endpoint" stops
	// being distinguishable from "the endpoint rejected the key" — which are
	// different things for the reader to do next.
	for code, reason := range map[int]string{
		2: insightReasonBadRequest,
		3: insightReasonNoProvider,
		4: insightReasonProviderRefused,
		5: insightReasonModelFailed,
	} {
		if !strings.HasPrefix(explainInsightExit(context.Background(), code), reason+": ") {
			t.Errorf("exit %d = %q, want it to name the reason %q the app classifies on", code, explainInsightExit(context.Background(), code), reason)
		}
	}
	// And nothing else is tagged: a token on an unclassified failure would have
	// the card show a confident cause nobody determined.
	for _, code := range []int{1, -1} {
		for _, reason := range []string{insightReasonBadRequest, insightReasonNoProvider, insightReasonProviderRefused, insightReasonModelFailed} {
			if strings.Contains(explainInsightExit(context.Background(), code), reason) {
				t.Errorf("exit %d claims reason %q, which nothing classified it as", code, reason)
			}
		}
	}
	// A cancelled context outranks whatever the killed child reported.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if !strings.Contains(explainInsightExit(cancelled, 5), "longer than") {
		t.Error("a run stopped by its own timeout must say so, not blame the model")
	}
}

func TestInsightCatalogRoomsAreFirstAppearanceAndUnique(t *testing.T) {
	rooms := insightCatalogRooms([]byte(insightTestCatalog), []string{"MEETING2", "MEETING1", "SECRET", "MEETING2"})
	want := []string{"rm_11bb22cc33dd44ee", "rm_9f2a1c3d4e5b6a70"}
	if len(rooms) != len(want) || rooms[0] != want[0] || rooms[1] != want[1] {
		t.Errorf("insightCatalogRooms() = %v, want %v", rooms, want)
	}
	if got := insightCatalogRooms([]byte("not json"), []string{"MEETING1"}); got == nil || len(got) != 0 {
		t.Errorf("an unreadable catalog must yield an empty list, not nil: %v", got)
	}
}

// The whole run: stage as the caller, ask the model, deliver into the caller's
// own home, and record which endpoint answered.
func TestPerformDeliversTheDocumentIntoTheRequestersOwnHome(t *testing.T) {
	dav := newInsightDAV(t, insightTestCatalog, "MEETING1.opus", "MEETING2.opus")
	const document = "---\nartifactId: \"ins_x\"\n---\n\n# What was decided\n"
	bin, argvPath := fakeInsightCassini(t, document, 0)
	service, _ := insightTestService(t, dav.server.URL, bin, nil)

	outcome := service.perform(context.Background(), insightTestRun())

	if outcome.Status != insightStatusSucceeded {
		t.Fatalf("status = %q, want succeeded (error %q)", outcome.Status, outcome.Error)
	}
	want := ncInsightsRoot + "/2026-09-03-summarise-ins_0123456789abcdef.md"
	if outcome.DocumentPath != want {
		t.Errorf("documentPath = %q, want %q", outcome.DocumentPath, want)
	}
	delivered := dav.delivered()
	if len(delivered) != 1 {
		t.Fatalf("delivered %d documents, want 1", len(delivered))
	}
	if delivered[0].path != "alice/"+want {
		t.Errorf("delivered to %q, want the caller's own home", delivered[0].path)
	}
	if delivered[0].body != document {
		t.Errorf("delivered body = %q, want the CLI's own bytes", delivered[0].body)
	}
	if created := dav.collectionsCreated(); len(created) != 1 || created[0] != "alice/"+ncInsightsRoot {
		t.Errorf("collections created = %v, want only the caller's own %q", created, ncInsightsRoot)
	}
	// The endpoint is recorded by id, never by URL: the run is USER-readable and
	// the base URL is ADMIN-only on the settings surface.
	if outcome.Provider != "local" {
		t.Errorf("provider = %q, want the configured provider id", outcome.Provider)
	}
	if strings.Contains(outcome.Provider, "://") {
		t.Errorf("provider = %q leaks an endpoint URL into a USER-readable run", outcome.Provider)
	}
	if outcome.Model != "llama-3.1-8b" {
		t.Errorf("model = %q, want the model the record named", outcome.Model)
	}

	argv, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatalf("read argv: %v", err)
	}
	for _, want := range []string{"meetings\ncontext\n--local", "insight\nrun\n--context", "--workflow\nsummarise", "--record"} {
		if !strings.Contains(string(argv), want) {
			t.Errorf("the CLI was not invoked with %q; argv was:\n%s", want, argv)
		}
	}
	if strings.Contains(string(argv), "--out") {
		t.Error("the document must come back on stdout so it can be capped as it is written")
	}
}

// A file already at the chosen name is never replaced. The run id makes a
// collision an out-of-band file rather than a second attempt, so the answer is a
// second name — and the run says which one it used.
func TestPerformNeverOverwritesADocumentAlreadyThere(t *testing.T) {
	dav := newInsightDAV(t, insightTestCatalog, "MEETING1.opus", "MEETING2.opus")
	taken := "alice/" + ncInsightsRoot + "/2026-09-03-summarise-ins_0123456789abcdef.md"
	dav.markExisting(taken)
	bin, _ := fakeInsightCassini(t, "# Answer\n", 0)
	service, _ := insightTestService(t, dav.server.URL, bin, nil)

	outcome := service.perform(context.Background(), insightTestRun())

	if outcome.Status != insightStatusSucceeded {
		t.Fatalf("status = %q, want succeeded (error %q)", outcome.Status, outcome.Error)
	}
	want := ncInsightsRoot + "/2026-09-03-summarise-ins_0123456789abcdef-2.md"
	if outcome.DocumentPath != want {
		t.Errorf("documentPath = %q, want the next free name %q", outcome.DocumentPath, want)
	}
	for _, put := range dav.delivered() {
		if put.path == taken {
			t.Fatal("the run overwrote a file that was already there")
		}
	}
}

// A model endpoint nobody configured is the single commonest failure, and the
// card has to name the fix rather than show a stopped spinner.
func TestPerformTurnsAnExitCodeIntoAnActionableFailure(t *testing.T) {
	dav := newInsightDAV(t, insightTestCatalog, "MEETING1.opus", "MEETING2.opus")
	bin, _ := fakeInsightCassini(t, "", 3)
	service, _ := insightTestService(t, dav.server.URL, bin, nil)

	outcome := service.perform(context.Background(), insightTestRun())

	if outcome.Status != insightStatusFailed {
		t.Fatalf("status = %q, want failed", outcome.Status)
	}
	if !strings.Contains(outcome.Error, "No AI endpoint is configured") {
		t.Errorf("error = %q, want the sentence exit 3 means", outcome.Error)
	}
	if outcome.DocumentPath != "" {
		t.Errorf("a failed run named a document at %q", outcome.DocumentPath)
	}
	if len(dav.delivered()) != 0 {
		t.Error("a failed run wrote into the caller's files")
	}
}

// Access is re-resolved on every attempt: a meeting the requester could read
// when they asked and cannot read now drops the run rather than the meeting.
func TestPerformRefusesAMeetingTheCallerCanNoLongerRead(t *testing.T) {
	dav := newInsightDAV(t, insightTestCatalog, "MEETING1.opus")
	bin, _ := fakeInsightCassini(t, "# Answer\n", 0)
	service, _ := insightTestService(t, dav.server.URL, bin, nil)

	outcome := service.perform(context.Background(), insightTestRun())

	if outcome.Status != insightStatusFailed {
		t.Fatalf("status = %q, want failed", outcome.Status)
	}
	if !strings.Contains(outcome.Error, "no longer available to you") {
		t.Errorf("error = %q", outcome.Error)
	}
	if len(dav.delivered()) != 0 {
		t.Error("a refused run wrote into the caller's files")
	}
}
