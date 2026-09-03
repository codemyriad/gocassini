package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cassini-operator/internal/operator/appapi"
)

// The HTTP surface has one job the run path does not: to be honest about what
// went wrong without saying anything about what the caller may not see. So the
// tests below are mostly about status codes — which of 400, 404, 409 and 502
// each refusal is, and that none of them can be told apart from the answer a
// caller with no access would get.

// fakeInsightStore is the run store as the handlers use it. A fake rather than
// SQLite because none of these cases is about persistence: they are about which
// answer a request gets, and a store that can be told to be busy or absent
// exercises them directly.
type fakeInsightStore struct {
	mu      sync.Mutex
	runs    map[string]InsightRun
	order   []string
	created []InsightRun
	// listErr, getErr and beginErr force the failure paths.
	listErr  error
	getErr   error
	beginErr error
}

func newFakeInsightStore(runs ...InsightRun) *fakeInsightStore {
	store := &fakeInsightStore{runs: map[string]InsightRun{}}
	for _, run := range runs {
		store.runs[run.ID] = run
		store.order = append(store.order, run.ID)
	}
	return store
}

func (f *fakeInsightStore) CreateRun(_ context.Context, run InsightRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Mirrors the real store: a created run is queued at attempt 1 whatever the
	// caller set, because no attempt has happened yet.
	run.Status = insightStatusQueued
	run.AttemptNumber = 1
	f.created = append(f.created, run)
	f.runs[run.ID] = run
	f.order = append([]string{run.ID}, f.order...)
	return nil
}

func (f *fakeInsightStore) GetRun(_ context.Context, id string) (InsightRun, error) {
	if f.getErr != nil {
		return InsightRun{}, f.getErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[id]
	if !ok {
		return InsightRun{}, sql.ErrNoRows
	}
	return run, nil
}

func (f *fakeInsightStore) ListRuns(_ context.Context, createdBy string) ([]InsightRun, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	runs := []InsightRun{}
	for _, id := range f.order {
		if run := f.runs[id]; run.CreatedBy == createdBy {
			runs = append(runs, run)
		}
	}
	return runs, nil
}

func (f *fakeInsightStore) BeginAttempt(_ context.Context, id string) (InsightRun, error) {
	if f.beginErr != nil {
		return InsightRun{}, f.beginErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[id]
	if !ok {
		return InsightRun{}, sql.ErrNoRows
	}
	if run.Status != insightStatusQueued && run.Status != insightStatusFailed {
		return InsightRun{}, errInsightRunBusy
	}
	if run.Status == insightStatusFailed {
		run.AttemptNumber++
	}
	run.Status = insightStatusRunning
	run.Provider, run.Model, run.DocumentPath, run.Error = "", "", "", ""
	f.runs[id] = run
	return run, nil
}

func (f *fakeInsightStore) FinishAttempt(_ context.Context, id string, outcome InsightOutcome) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	run := f.runs[id]
	run.Status = outcome.Status
	run.Provider, run.Model, run.DocumentPath, run.Error = outcome.Provider, outcome.Model, outcome.DocumentPath, outcome.Error
	f.runs[id] = run
	return nil
}

// insightRegistryCassini answers `cassini insight workflows --json` with two
// workflows: one that asks its own question and one that needs the caller's, so
// both halves of the question rule can be exercised.
const insightRegistryJSON = `[` +
	`{"id":"summarise","version":"v0","sha256":"aaaa","name":"Meeting summary","question":"Summarise what happened.",` +
	`"description":"One document per meeting.","origin":"Built in","instruction":"Summarise these meetings."},` +
	`{"id":"ask","version":"v0","sha256":"bbbb","name":"Ask your own","question":"Whatever you want to know.",` +
	`"description":"An answer to your question.","origin":"Built in","instruction":"Answer this: {{QUESTION}}"}` +
	`]`

func insightRegistryCassini(t *testing.T) string {
	t.Helper()
	return writeFakeCassini(t, `if [ "$1 $2" = "insight workflows" ]; then
  printf '%s' '`+insightRegistryJSON+`'
  exit 0
fi
exit 9
`)
}

type insightHandlerHarness struct {
	service *insightService
	store   *fakeInsightStore
	dav     *insightDAV
	// launched records the runs the handlers started, so the background half is
	// asserted without running it.
	launched []string
}

func newInsightHandlerHarness(t *testing.T, runs ...InsightRun) *insightHandlerHarness {
	t.Helper()
	dav := newInsightDAV(t, insightTestCatalog, "MEETING1.opus", "MEETING2.opus")
	store := newFakeInsightStore(runs...)
	service, _ := insightTestService(t, dav.server.URL, insightRegistryCassini(t), store)
	harness := &insightHandlerHarness{service: service, store: store, dav: dav}
	service.launchFn = func(id string, _ bool) { harness.launched = append(harness.launched, id) }
	return harness
}

func (h *insightHandlerHarness) do(t *testing.T, method, target, caller, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if caller != "" {
		req = req.WithContext(appapi.WithUserID(req.Context(), caller))
	}
	mux := http.NewServeMux()
	h.service.register(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// Every refusal that is the caller's to fix is a 400, decided before anything
// reaches Nextcloud, and each one says what was wrong.
func TestCreateInsightRefusesABadRequestBeforeTouchingNextcloud(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"no meetings", `{"meetingIds":[]}`, "at least one meeting"},
		{"too many meetings", `{"meetingIds":` + manyMeetingIDs(maxInsightMeetings+1) + `}`, "at most"},
		{"a path in an id", `{"meetingIds":["../secret"]}`, "characters a meeting id cannot contain"},
		{"the same meeting twice", `{"meetingIds":["MEETING1","MEETING1"]}`, "more than once"},
		{"an unknown workflow", `{"meetingIds":["MEETING1"],"workflow":"decisions"}`, "no workflow called"},
		{"an unknown workflow says what there is", `{"meetingIds":["MEETING1"],"workflow":"decisions"}`, "this deployment ships summarise, ask"},
		{"a workflow id of the wrong shape", `{"meetingIds":["MEETING1"],"workflow":"a b/c"}`, "not a workflow id"},
		{"a question no workflow slot holds", `{"meetingIds":["MEETING1"],"question":"why?"}`, "takes none of yours"},
		{"a workflow with no question", `{"meetingIds":["MEETING1"],"workflow":"ask"}`, "needs a question"},
		{"a field nobody declared", `{"meetingIds":["MEETING1"],"timestamps":true}`, "not a valid insight request"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			harness := newInsightHandlerHarness(t)
			w := harness.do(t, http.MethodPost, "/insights", "alice", testCase.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("code = %d, want 400 (%s)", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), testCase.want) {
				t.Errorf("body = %s, want it to mention %q", w.Body.String(), testCase.want)
			}
			if len(harness.store.created) != 0 {
				t.Error("a refused request still created a run")
			}
			if harness.dav.requestCount() != 0 {
				t.Errorf("a refused request made %d Nextcloud calls; a bad request must cost none", harness.dav.requestCount())
			}
		})
	}
}

func manyMeetingIDs(n int) string {
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, "MEETING"+strconv.Itoa(i))
	}
	encoded, _ := json.Marshal(ids)
	return string(encoded)
}

// A meeting outside the caller's readable set answers exactly as one that does
// not exist. A recording you may not read must never reveal that it exists, and
// a run that named it would.
func TestCreateInsightAnswers404ForAMeetingTheCallerMayNotRead(t *testing.T) {
	harness := newInsightHandlerHarness(t)
	w := harness.do(t, http.MethodPost, "/insights", "alice", `{"meetingIds":["MEETING1","SECRET"]}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404 (%s)", w.Code, w.Body.String())
	}
	if len(harness.store.created) != 0 {
		t.Error("a run was created for a meeting the caller may not read")
	}
}

// No verified identity means the AppAPI middleware did not run. Neither "you may
// read nothing" nor "it does not exist" is true, and both are lies an outage can
// tell for ever.
func TestInsightRoutesRefuseARequestWithNoCallerIdentity(t *testing.T) {
	harness := newInsightHandlerHarness(t)
	for _, target := range []string{"/insights", "/insights/ins_0123456789abcdef"} {
		w := harness.do(t, http.MethodGet, target, "", "")
		if w.Code != http.StatusBadGateway {
			t.Errorf("GET %s code = %d, want 502", target, w.Code)
		}
	}
}

func TestCreateInsightAnswersAQueuedRunAtOnce(t *testing.T) {
	harness := newInsightHandlerHarness(t)
	w := harness.do(t, http.MethodPost, "/insights", "alice", `{"meetingIds":["MEETING2","MEETING1"]}`)

	if w.Code != http.StatusCreated {
		t.Fatalf("code = %d, want 201 (%s)", w.Code, w.Body.String())
	}
	var run InsightRun
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		t.Fatalf("decode the created run: %v (%s)", err, w.Body.String())
	}
	if run.Status != insightStatusQueued {
		t.Errorf("status = %q, want queued: the card has to appear before the answer does", run.Status)
	}
	if run.ID != "ins_0123456789abcdef" || run.CreatedBy != "alice" || run.AttemptNumber != 1 {
		t.Errorf("run = %+v", run)
	}
	// The workflow triple travels with the run: a document that cannot say which
	// prompt version and which bytes made it is a claim with no way to check it.
	if run.WorkflowID != "summarise" || run.WorkflowVersion != "v0" || run.WorkflowSHA256 != "aaaa" {
		t.Errorf("workflow = %s/%s/%s, want the registry's own triple", run.WorkflowID, run.WorkflowVersion, run.WorkflowSHA256)
	}
	if len(run.MeetingIDs) != 2 || run.MeetingIDs[0] != "MEETING2" {
		t.Errorf("meetingIds = %v, want the caller's own order", run.MeetingIDs)
	}
	if len(run.RoomIDs) != 2 || run.RoomIDs[0] != "rm_11bb22cc33dd44ee" {
		t.Errorf("roomIds = %v, want the rooms in first-appearance order", run.RoomIDs)
	}
	if len(harness.launched) != 1 || harness.launched[0] != run.ID {
		t.Errorf("launched = %v, want the created run started in the background", harness.launched)
	}
}

// An absent workflow falls back to the endpoint's configured template before the
// shipped default, so an administrator's choice is what an unqualified Generate
// actually runs.
func TestCreateInsightFallsBackToTheConfiguredTemplate(t *testing.T) {
	harness := newInsightHandlerHarness(t)
	llm := harness.service.rt.currentLLMSettings()
	llm.Insight = LLMStep{Enabled: true, Provider: "local", Template: "ask"}
	harness.service.rt.setLLMSettings(llm)

	w := harness.do(t, http.MethodPost, "/insights", "alice", `{"meetingIds":["MEETING1"],"question":"what changed?"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("code = %d, want 201 (%s)", w.Code, w.Body.String())
	}
	var run InsightRun
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if run.WorkflowID != "ask" {
		t.Errorf("workflowId = %q, want the configured insight template", run.WorkflowID)
	}
	if run.Question != "what changed?" {
		t.Errorf("question = %q", run.Question)
	}
}

func TestListInsightsServesOnlyTheCallersOwn(t *testing.T) {
	harness := newInsightHandlerHarness(t,
		InsightRun{ID: "ins_00000000000000a1", CreatedBy: "alice", Status: insightStatusSucceeded, MeetingIDs: []string{"MEETING1"}, CreatedAt: time.Now()},
		InsightRun{ID: "ins_00000000000000b2", CreatedBy: "bob", Status: insightStatusSucceeded, MeetingIDs: []string{"MEETING1"}, CreatedAt: time.Now()},
	)
	w := harness.do(t, http.MethodGet, "/insights", "alice", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var response insightListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(response.Insights) != 1 || response.Insights[0].ID != "ins_00000000000000a1" {
		t.Errorf("insights = %+v, want only alice's", response.Insights)
	}

	// An empty list is an answer, and it must not be the shape a failure takes.
	empty := harness.do(t, http.MethodGet, "/insights", "carol", "")
	if !strings.Contains(empty.Body.String(), `"insights":[]`) {
		t.Errorf("body = %s, want an empty list rather than null", empty.Body.String())
	}
}

func TestListInsightsAnswers502WhenTheStoreCannotBeRead(t *testing.T) {
	harness := newInsightHandlerHarness(t)
	harness.store.listErr = sql.ErrConnDone
	w := harness.do(t, http.MethodGet, "/insights", "alice", "")
	if w.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502: an empty list would say this caller has no insights", w.Code)
	}
}

// Somebody else's run answers exactly as one that does not exist. A run names
// the meetings it was built from, so a 403 would say both that it exists and
// that it is not yours.
func TestReadInsightHidesAnotherCallersRun(t *testing.T) {
	harness := newInsightHandlerHarness(t,
		InsightRun{ID: "ins_00000000000000b2", CreatedBy: "bob", Status: insightStatusSucceeded, MeetingIDs: []string{"MEETING1"}},
	)
	mine := harness.do(t, http.MethodGet, "/insights/ins_00000000000000b2", "bob", "")
	if mine.Code != http.StatusOK {
		t.Fatalf("the owner got %d (%s)", mine.Code, mine.Body.String())
	}
	theirs := harness.do(t, http.MethodGet, "/insights/ins_00000000000000b2", "alice", "")
	absent := harness.do(t, http.MethodGet, "/insights/ins_0000000000000fff", "alice", "")
	if theirs.Code != http.StatusNotFound || absent.Code != http.StatusNotFound {
		t.Errorf("another caller's run = %d, an absent one = %d; both must be 404", theirs.Code, absent.Code)
	}
}

// A succeeded run serves its document beside itself, read back out of the
// requester's own files as them — so a file they have since deleted answers as
// gone rather than as bytes the operator happened to keep, and the run is still
// served rather than turned into a failure it is not.
func TestReadInsightServesTheDocumentFromTheCallersOwnFiles(t *testing.T) {
	const relPath = ncInsightsRoot + "/2026-09-03-summarise-ins_00000000000000a1.md"
	harness := newInsightHandlerHarness(t,
		InsightRun{
			ID: "ins_00000000000000a1", CreatedBy: "alice", Status: insightStatusSucceeded,
			MeetingIDs: []string{"MEETING1"}, DocumentPath: relPath,
		},
	)
	harness.dav.holdDocument("alice/"+relPath, "# What was decided\n")

	w := harness.do(t, http.MethodGet, "/insights/ins_00000000000000a1", "alice", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var response insightDetailResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.ID != "ins_00000000000000a1" || response.Document != "# What was decided\n" {
		t.Errorf("response = %+v, want the run and its document", response)
	}

	// The requester moved it. The run still succeeded, and saying otherwise would
	// rewrite history over a file they own.
	harness.dav.dropDocument("alice/" + relPath)
	gone := harness.do(t, http.MethodGet, "/insights/ins_00000000000000a1", "alice", "")
	if gone.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", gone.Code)
	}
	if err := json.Unmarshal(gone.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.Status != insightStatusSucceeded || response.Document != "" {
		t.Errorf("response = %+v, want the run still succeeded with no document", response)
	}
}

func TestReadInsightRefusesAnIDOfTheWrongShape(t *testing.T) {
	harness := newInsightHandlerHarness(t)
	w := harness.do(t, http.MethodGet, "/insights/not-an-id", "alice", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", w.Code)
	}
}

// The status is the lock, so a retry against a run that is not failed is a
// refusal and never a second attempt against one document path.
func TestRetryIsRefusedWhileTheRunIsStillGoing(t *testing.T) {
	harness := newInsightHandlerHarness(t,
		InsightRun{ID: "ins_00000000000000a1", CreatedBy: "alice", Status: insightStatusRunning, MeetingIDs: []string{"MEETING1"}},
		InsightRun{ID: "ins_00000000000000a2", CreatedBy: "alice", Status: insightStatusQueued, MeetingIDs: []string{"MEETING1"}},
	)
	for _, id := range []string{"ins_00000000000000a1", "ins_00000000000000a2"} {
		w := harness.do(t, http.MethodPost, "/insights/"+id+"/retry", "alice", "")
		if w.Code != http.StatusConflict {
			t.Errorf("retry of %s = %d, want 409 (%s)", id, w.Code, w.Body.String())
		}
	}
	if len(harness.launched) != 0 {
		t.Errorf("a refused retry still started a run: %v", harness.launched)
	}
}

func TestRetryStartsAnotherAttemptOnAFailedRun(t *testing.T) {
	harness := newInsightHandlerHarness(t,
		InsightRun{
			ID: "ins_00000000000000a1", CreatedBy: "alice", Status: insightStatusFailed,
			MeetingIDs: []string{"MEETING1"}, AttemptNumber: 1,
			Provider: "gone", Model: "gone", Error: "No AI endpoint is configured.",
		},
	)
	w := harness.do(t, http.MethodPost, "/insights/ins_00000000000000a1/retry", "alice", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var run InsightRun
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if run.Status != insightStatusRunning || run.AttemptNumber != 2 {
		t.Errorf("run = %s attempt %d, want running attempt 2", run.Status, run.AttemptNumber)
	}
	// The previous attempt's endpoint must not survive: the next one re-resolves
	// from current settings, and a stale provider on the card would describe a
	// run that is no longer happening.
	if run.Provider != "" || run.Model != "" || run.Error != "" {
		t.Errorf("run still carries the last attempt: provider=%q model=%q error=%q", run.Provider, run.Model, run.Error)
	}
	if len(harness.launched) != 1 {
		t.Errorf("launched = %v, want one attempt", harness.launched)
	}
	// Somebody else's failed run is still a 404, before the lock is even reached.
	other := harness.do(t, http.MethodPost, "/insights/ins_00000000000000a1/retry", "bob", "")
	if other.Code != http.StatusNotFound {
		t.Errorf("another caller's retry = %d, want 404", other.Code)
	}
}

func TestInsightRoutesRefuseTheWrongMethod(t *testing.T) {
	harness := newInsightHandlerHarness(t,
		InsightRun{ID: "ins_00000000000000a1", CreatedBy: "alice", Status: insightStatusFailed, MeetingIDs: []string{"MEETING1"}},
	)
	cases := []struct{ method, target string }{
		{http.MethodDelete, "/insights"},
		{http.MethodPost, "/insights/ins_00000000000000a1"},
		{http.MethodGet, "/insights/ins_00000000000000a1/retry"},
	}
	for _, testCase := range cases {
		w := harness.do(t, testCase.method, testCase.target, "alice", "")
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 405", testCase.method, testCase.target, w.Code)
		}
	}
	if unknown := harness.do(t, http.MethodPost, "/insights/ins_00000000000000a1/rerun", "alice", ""); unknown.Code != http.StatusNotFound {
		t.Errorf("an undeclared sub-path = %d, want 404", unknown.Code)
	}
}

// The trailing-slash form is what AppAPI's `^insights\/?$` also matches, so it
// has to reach the collection rather than be read as a run with an empty id.
func TestInsightCollectionAnswersWithOrWithoutATrailingSlash(t *testing.T) {
	harness := newInsightHandlerHarness(t)
	for _, target := range []string{"/insights", "/insights/"} {
		w := harness.do(t, http.MethodGet, target, "alice", "")
		if w.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 (%s)", target, w.Code, w.Body.String())
		}
	}
}

// Every insight answer is uncacheable, the way the archive's other per-caller
// reads are. Two failures ride on this one header, and the second is the one
// that would survive every other test on this file: these bodies are per-caller
// under a single URL, and a run is polled — a cached `GET insights/<id>` keeps
// answering `queued` for as long as the entry lives, so the card never finishes
// for a run that did.
func TestInsightAnswersAreNeverCached(t *testing.T) {
	harness := newInsightHandlerHarness(t,
		InsightRun{
			ID: "ins_00000000000000a1", CreatedBy: "alice", Status: insightStatusFailed,
			MeetingIDs: []string{"MEETING1"}, AttemptNumber: 1, Error: "No AI endpoint is configured.",
		},
	)
	cases := []struct{ method, target, body string }{
		{http.MethodGet, "/insights", ""},
		{http.MethodGet, "/insights/ins_00000000000000a1", ""},
		{http.MethodPost, "/insights", `{"meetingIds":["MEETING1"]}`},
		{http.MethodPost, "/insights/ins_00000000000000a1/retry", ""},
		// A refusal must not be cached either: a 404 held for an hour would
		// outlive the run that fixed it.
		{http.MethodGet, "/insights/ins_0000000000000bad", ""},
	}
	for _, testCase := range cases {
		w := harness.do(t, testCase.method, testCase.target, "alice", testCase.body)
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s %s Cache-Control = %q, want no-store", testCase.method, testCase.target, got)
		}
	}
}
