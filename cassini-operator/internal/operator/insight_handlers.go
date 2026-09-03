package operator

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"cassini-operator/internal/operator/appapi"
)

// The insight HTTP surface (D-700):
//
//	POST insights                 create a run          201 {run}
//	GET  insights                 list the caller's     200 {"insights":[{run}, …]}
//	GET  insights/<id>            one run + document    200 {run, "document": "…"}
//	POST insights/<id>/retry      retry a failed run    200 {run} · 409 if busy
//
// Mounted on the ROOT mux beside /published/, not under the operator's
// BasePath, because they are their own top-level prefix in appinfo/info.xml.
// They are the app's first mutating USER routes, and that is precisely why they
// are not folded into `^published\/.+$`, which is declared GET,HEAD: a POST
// hidden inside a GET declaration is the change a reviewer would not see.
//
// # What each status code means, and what none of them may leak
//
//   - 400 — a request that could not be run as asked: no meetings, too many, a
//     malformed id, an unknown workflow, a question a workflow has no slot for.
//     Decided before anything reaches Nextcloud, so a bad request costs no
//     downloads and can never half-run.
//   - 404 — a meeting or a run the caller may not read, indistinguishable from
//     one that does not exist. This is the archive's standing rule (D-521): a
//     recording you may not read must never reveal that it exists, and the run
//     that names it inherits that.
//   - 409 — a retry against a run that is not failed. BeginAttempt is the lock,
//     so of two retries pressed at once one attempt starts and one is refused.
//   - 502 — a failed scan, or a request with no verified caller identity. The
//     second means the AppAPI middleware did not run: answering "you may read
//     nothing" would be a claim about a caller nobody identified, and 404 would
//     say the meetings do not exist. Both are lies an outage can tell for ever.
//
// A failure is never an empty 200. Every one of these routes would otherwise
// have a shape — an empty list, a run with no document — that reads as a fact
// about the archive rather than as a fault.

// insightCreateRequest is the create body. Everything in it is checked before
// the first call to Nextcloud.
type insightCreateRequest struct {
	MeetingIDs []string `json:"meetingIds"`
	// Workflow is optional: an absent one falls back to the configured insight
	// template and then to the shipped default. The set of ids lives in the
	// recorder, so the operator resolves this against what
	// `cassini insight workflows --json` prints rather than a list of its own.
	Workflow string `json:"workflow"`
	// Question is the caller's own text, for a workflow that takes one. Refused
	// both ways: given to a workflow with no slot for it, it would be silently
	// dropped, and withheld from one that needs it, the prompt would go out with
	// its placeholder still in it.
	Question string `json:"question"`
}

// insightDetailResponse is one run with the document it produced. The document
// is read back out of the requester's own files as them, so a file they have
// since moved or deleted answers as gone rather than as bytes the operator
// happened to keep.
type insightDetailResponse struct {
	InsightRun
	Document string `json:"document"`
}

type insightListResponse struct {
	Insights []InsightRun `json:"insights"`
}

// register mounts the four operations on the root mux. Two patterns, because
// Go's ServeMux resolves "/insights" and "/insights/" separately and the
// manifest declares both the bare and the trailing-slash form of each route.
func (s *insightService) register(root *http.ServeMux) {
	root.HandleFunc(insightsURLPath, insightNoStore(s.serveCollection))
	root.HandleFunc(insightsURLPath+"/", insightNoStore(s.serveItem))
}

// insightNoStore stamps every insight answer uncacheable, the same way the
// archive's own per-caller reads do (writeCatalogJSON in webdav_acl.go,
// serveMeetingsContext in published_context.go). Either reason alone would be
// enough. These bodies are per-caller — one URL, a different answer for every
// account — so anything between the browser and here that keyed a cache on the
// path would hand one person another's runs. And a run is POLLED: a cached
// `GET insights/<id>` keeps answering `queued` for as long as the entry lives,
// which is a card that never finishes for a run that did.
func insightNoStore(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next(w, r)
	}
}

func (s *insightService) serveCollection(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.caller(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.list(w, r, caller)
	case http.MethodPost:
		s.create(w, r, caller)
	default:
		writeMethodNotAllowed(w, "GET, POST")
	}
}

// serveItem routes the two per-run paths. The trailing-slash form of the
// collection lands here too — AppAPI's `^insights\/?$` matches it — so it is
// forwarded rather than treated as a run whose id is empty.
func (s *insightService) serveItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, insightsURLPath), "/")
	if rest == "" {
		s.serveCollection(w, r)
		return
	}
	caller, ok := s.caller(w, r)
	if !ok {
		return
	}
	id, action, _ := strings.Cut(rest, "/")
	if !isInsightRunID(id) {
		// A malformed id names nothing, and saying so is not a disclosure: it is
		// a statement about the id, not about what exists.
		writeJSONError(w, http.StatusBadRequest, "that is not an insight id")
		return
	}
	switch action {
	case "":
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		s.read(w, r, caller, id)
	case "retry":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		s.retry(w, r, caller, id)
	default:
		http.NotFound(w, r)
	}
}

// caller resolves the verified identity, or refuses. These routes are USER-gated
// by the manifest, so an absent identity means the AppAPI middleware did not run
// — an outage, not an answer (D-701).
func (s *insightService) caller(w http.ResponseWriter, r *http.Request) (string, bool) {
	caller := appapi.UserID(r.Context())
	if caller == "" {
		s.logf("insights: missing caller identity on %s %s — refusing to answer", r.Method, r.URL.Path)
		writeJSONError(w, http.StatusBadGateway, "no verified caller identity")
		return "", false
	}
	return caller, true
}

// --- POST insights -------------------------------------------------------------

func (s *insightService) create(w http.ResponseWriter, r *http.Request, caller string) {
	request, err := decodeInsightCreateRequest(w, r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	workflow, err := s.resolveWorkflow(r, request)
	if err != nil {
		var unavailable *insightRegistryUnavailableError
		if errors.As(err, &unavailable) {
			s.logf("insights: %v", err)
			writeJSONError(w, http.StatusBadGateway, "the workflow registry could not be read")
			return
		}
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// The first Nextcloud call, and the only access decision: the same per-caller
	// intersect the viewer's catalog goes through, so a second reader never means
	// a second access-control path.
	readable, catalog, ok := s.exapp.readableMeetingsForCaller(r.Context(), s.client, caller, s.logger)
	if !ok {
		writeJSONError(w, http.StatusBadGateway, "Nextcloud Files unavailable")
		return
	}
	for _, id := range request.MeetingIDs {
		if _, permitted := readable[id]; !permitted {
			// Denied and absent are the same answer on purpose. Logged, so an
			// operator can still tell them apart.
			s.logf("insights: caller=%s asked for meeting=%s, which is not in their readable set (served as 404)", caller, id)
			http.NotFound(w, r)
			return
		}
	}

	id, err := s.newID()
	if err != nil {
		s.logf("insights: %v", err)
		writeJSONError(w, http.StatusBadGateway, "the insight could not be started")
		return
	}
	run := InsightRun{
		ID:              id,
		CreatedBy:       caller,
		Status:          insightStatusQueued,
		WorkflowID:      workflow.ID,
		WorkflowVersion: workflow.Version,
		WorkflowSHA256:  workflow.SHA256,
		MeetingIDs:      request.MeetingIDs,
		RoomIDs:         insightCatalogRooms(catalog, request.MeetingIDs),
		Question:        request.Question,
		AttemptNumber:   1,
		CreatedAt:       s.now(),
	}
	if err := s.store.CreateRun(r.Context(), run); err != nil {
		s.logf("insights: create run=%s for caller=%s: %v", id, caller, err)
		writeJSONError(w, http.StatusBadGateway, "the insight could not be started")
		return
	}

	// 201 now, work later. A local model over five meetings takes minutes, and a
	// request that held the connection for that would be a proxy timeout every
	// time; the queued card is the honest answer and the only one that arrives.
	//
	// Read back rather than echoed, because the store owns the normalised row —
	// its stamps, its attempt number — and the card is going to be reconciled
	// against later reads of exactly that row.
	stored, err := s.store.GetRun(r.Context(), id)
	if err != nil {
		// The row was written; failing the response now would tell the caller
		// nothing happened while a run proceeds behind them. Answer with what was
		// asked for instead.
		s.logf("insights: read back run=%s after creating it: %v", id, err)
		stored = run
	}
	// The answer goes out BEFORE the run is started: the background attempt
	// claims the row the moment it begins, and a read racing with it would serve
	// `running` from a call whose whole contract is that it answers `queued`.
	writeJSON(w, http.StatusCreated, stored)
	s.launchFn(id, false)
}

// decodeInsightCreateRequest validates the body completely, and refuses on the
// first thing that is wrong, before anything reaches Nextcloud.
func decodeInsightCreateRequest(w http.ResponseWriter, r *http.Request) (insightCreateRequest, error) {
	var request insightCreateRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxInsightRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, fmt.Errorf("the request body is not a valid insight request: %w", err)
	}

	ids := make([]string, 0, len(request.MeetingIDs))
	seen := make(map[string]bool, len(request.MeetingIDs))
	for _, raw := range request.MeetingIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			return request, errors.New("a meeting id is empty")
		}
		if len(id) > maxContextIDLength {
			return request, fmt.Errorf("a meeting id is longer than %d characters", maxContextIDLength)
		}
		if !isPlainMeetingID(id) {
			// The id becomes a file name in the staging directory, so a path
			// separator or a dot segment is refused here rather than sanitised
			// somewhere further in.
			return request, errors.New("a meeting id contains characters a meeting id cannot contain")
		}
		if seen[id] {
			// The same meeting twice is context spent twice on one meeting while
			// reading as coverage of two; the CLI refuses it for the same reason.
			return request, fmt.Errorf("meeting %q was given more than once", id)
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return request, errors.New("an insight needs at least one meeting")
	}
	if len(ids) > maxInsightMeetings {
		return request, fmt.Errorf("an insight covers at most %d meetings, got %d", maxInsightMeetings, len(ids))
	}
	request.MeetingIDs = ids

	request.Workflow = strings.TrimSpace(request.Workflow)
	if request.Workflow != "" && !llmTemplateIDPattern.MatchString(request.Workflow) {
		// The id ends up on a `cassini insight run --workflow` command line, so
		// its shape is checked before the set it belongs to is even read.
		return request, errors.New("that is not a workflow id")
	}

	request.Question = strings.TrimSpace(request.Question)
	if utf8.RuneCountInString(request.Question) > maxInsightQuestionRunes {
		return request, fmt.Errorf("a question is at most %d characters", maxInsightQuestionRunes)
	}
	return request, nil
}

// insightRegistryUnavailableError separates "this deployment does not ship that
// workflow" (the caller's problem, 400) from "this deployment cannot say what it
// ships" (ours, 502). Collapsing the two would send somebody looking for a
// missing feature during an outage.
type insightRegistryUnavailableError struct{ err error }

func (e *insightRegistryUnavailableError) Error() string {
	return fmt.Sprintf("the workflow registry could not be read: %v", e.err)
}

func (e *insightRegistryUnavailableError) Unwrap() error { return e.err }

// resolveWorkflow decides which prompt this run sends, SERVER-side.
//
// In order: what the request named, else the configured insight template, else
// the shipped default. The registry lives in the recorder — the operator cannot
// import it and must not keep a second copy of the set — so the candidate is
// resolved against what `cassini insight workflows --json` prints, which is the
// same list the settings panel shows.
//
// The last fallback is the registry's FIRST entry rather than an id written
// here. The run row has to carry a concrete id, version and content hash — a
// document that cannot say which prompt made it is a claim with no way to check
// it — so the default has to resolve to a real entry at create time, and the
// registry's own listing order is the one statement of "the first one we offer"
// that the settings panel and this share.
func (s *insightService) resolveWorkflow(r *http.Request, request insightCreateRequest) (workflowView, error) {
	entries, err := s.rt.readWorkflowRegistry(r, strings.TrimSpace(s.rt.cfg.CassiniBin))
	if err != nil {
		return workflowView{}, &insightRegistryUnavailableError{err: err}
	}
	if len(entries) == 0 {
		return workflowView{}, &insightRegistryUnavailableError{err: errors.New("this deployment ships no workflows")}
	}

	wanted := request.Workflow
	if wanted == "" {
		wanted = strings.TrimSpace(s.rt.currentLLMSettings().Insight.Template)
	}

	chosen := entries[0]
	if wanted != "" {
		found := false
		for _, entry := range entries {
			if entry.ID == wanted {
				chosen, found = entry, true
				break
			}
		}
		if !found {
			ids := make([]string, 0, len(entries))
			for _, entry := range entries {
				ids = append(ids, entry.ID)
			}
			return workflowView{}, fmt.Errorf("no workflow called %q; this deployment ships %s", wanted, strings.Join(ids, ", "))
		}
	}

	// A question is refused both ways rather than dropped or defaulted. The
	// instruction is the spliced prompt itself, so the placeholder's presence in
	// it is the same fact insight.Run checks — read off the bytes rather than off
	// a flag somebody has to remember to set.
	takesQuestion := strings.Contains(chosen.Instruction, insightQuestionPlaceholder)
	switch {
	case request.Question != "" && !takesQuestion:
		return workflowView{}, fmt.Errorf("the %q workflow asks its own question, so it takes none of yours", chosen.ID)
	case request.Question == "" && takesQuestion:
		return workflowView{}, fmt.Errorf("the %q workflow needs a question to ask", chosen.ID)
	}
	return chosen, nil
}

// --- GET insights ---------------------------------------------------------------

func (s *insightService) list(w http.ResponseWriter, r *http.Request, caller string) {
	runs, err := s.store.ListRuns(r.Context(), caller)
	if err != nil {
		s.logf("insights: list runs for caller=%s: %v", caller, err)
		writeJSONError(w, http.StatusBadGateway, "your insights could not be read")
		return
	}
	if runs == nil {
		// "This caller has no insights" and "the read failed" must not be the
		// same shape on the wire.
		runs = []InsightRun{}
	}
	writeJSON(w, http.StatusOK, insightListResponse{Insights: runs})
}

// --- GET insights/<id> ------------------------------------------------------------

func (s *insightService) read(w http.ResponseWriter, r *http.Request, caller, id string) {
	run, ok := s.ownRun(w, r, caller, id)
	if !ok {
		return
	}
	response := insightDetailResponse{InsightRun: run}
	if run.Status == insightStatusSucceeded && strings.TrimSpace(run.DocumentPath) != "" {
		document, err := s.fetchInsightDocument(r.Context(), caller, run.DocumentPath)
		if err != nil {
			// The run succeeded and the row says so; the file is the requester's
			// own and they may have moved it. That is a fact about the document,
			// not a failure of the run, so the run is still served — with an empty
			// document, which is what the card renders as "no longer here".
			s.logf("insights: read document for run=%s caller=%s: %v", id, caller, err)
		}
		response.Document = document
	}
	writeJSON(w, http.StatusOK, response)
}

// --- POST insights/<id>/retry -------------------------------------------------------

// retry starts another attempt at a failed run.
//
// The attempt is claimed HERE, inside the request, because BeginAttempt is the
// lock: taking it in the background goroutine would make the 409 a guess about a
// race rather than the outcome of it. Nothing about the request is re-read — the
// workflow, the meetings and the question are fixed — and the endpoint is
// re-resolved from current settings by the run path, which is the whole point of
// the button: "no provider configured" and "401" are exactly the failures a
// replay of the stored provider could never fix.
func (s *insightService) retry(w http.ResponseWriter, r *http.Request, caller, id string) {
	existing, ok := s.ownRun(w, r, caller, id)
	if !ok {
		return
	}
	if existing.Status != insightStatusFailed {
		// Retry is defined only for a run that failed. BeginAttempt would happily
		// claim a queued one — that is how a create's own goroutine claims it —
		// so the refusal is here, and BeginAttempt below remains the lock that
		// settles two retries pressed at once.
		writeJSONError(w, http.StatusConflict, "this insight is already running")
		return
	}
	run, err := s.store.BeginAttempt(r.Context(), id)
	switch {
	case err == nil:
	case errors.Is(err, errInsightRunBusy):
		writeJSONError(w, http.StatusConflict, "this insight is already running")
		return
	case errors.Is(err, sql.ErrNoRows):
		http.NotFound(w, r)
		return
	default:
		s.logf("insights: retry run=%s for caller=%s: %v", id, caller, err)
		writeJSONError(w, http.StatusBadGateway, "the insight could not be retried")
		return
	}
	s.launchFn(id, true)
	writeJSON(w, http.StatusOK, run)
}

// ownRun loads a run the caller may see.
//
// Absent and somebody else's are one answer. A run names the meetings it was
// built from, so an id that answered 403 for another account's run would say
// both that it exists and that it is not yours — which is more than the archive
// tells anyone about a recording.
func (s *insightService) ownRun(w http.ResponseWriter, r *http.Request, caller, id string) (InsightRun, bool) {
	run, err := s.store.GetRun(r.Context(), id)
	switch {
	case err == nil:
	case errors.Is(err, sql.ErrNoRows):
		http.NotFound(w, r)
		return InsightRun{}, false
	default:
		s.logf("insights: read run=%s for caller=%s: %v", id, caller, err)
		writeJSONError(w, http.StatusBadGateway, "that insight could not be read")
		return InsightRun{}, false
	}
	if run.CreatedBy != caller {
		s.logf("insights: caller=%s asked for run=%s, which belongs to somebody else (served as 404)", caller, id)
		http.NotFound(w, r)
		return InsightRun{}, false
	}
	return run, true
}
