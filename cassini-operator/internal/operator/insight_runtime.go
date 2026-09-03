package operator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Insights: one question asked of several meetings, answered by the model this
// deployment is configured with, and kept in the requester's own Nextcloud
// Files (D-700).
//
// # The identity walk, which decides the whole shape
//
// A browser reaches a declared USER route, so AppAPI has verified who is asking
// and appapi.UserID(ctx) names them. Nothing below that point carries the
// caller: a recorder subprocess runs as the container's process user, and the
// operator holds no app password for the caller and cannot mint one. So the
// operator does what published/meetings-context already does — it fetches each
// recording ITSELF, as the caller, over WebDAV, and hands the child a file path.
// Nextcloud enforces the per-file ACL on every one of those fetches, which is
// why a run can only ever be assembled out of meetings the requester could
// already read, and why granting them the answer asserts nothing new.
//
// The model call is the one hop with no user on it at all: it uses the
// instance's stored key, so an insight is attributable to the deployment and
// never to the person who asked for it. That belongs in docs/privacy.md rather
// than being left to be discovered.
//
// # Why the answer is 201 and not the document
//
// A local model over five meetings takes minutes. Holding the connection for
// that turns every insight into a proxy timeout, so the run record exists before
// its content does: create, answer `queued`, and do the work on a goroutine. The
// status vocabulary and the `ins_` id scheme are fixed in gocassini's
// internal/insight package doc; they are re-expressed below because this module
// cannot import that one, and nothing here invents a second version of either.
//
// # Where the document lands, and where it must not
//
// The requester's own Nextcloud home, under ncInsightsRoot. Correct ownership by
// construction — the person who asked owns the file, Nextcloud's own share UI
// works on it, and there is no ACL arithmetic to get wrong.
//
// NOT under "Cassini/". That name is the recordings Team-folder mount point
// (ncRecordingsMount), which every account has mounted read-only through the
// Everyone group, so "Cassini/Insights" in a caller's home is not a folder in
// their own storage at all — it is a write into the shared archive, which is
// denied for everybody but the service account and would be the wrong place even
// if it were allowed. insight_runtime_test.go pins the two names apart.
const (
	// insightsURLPath is where the routes are mounted, matching the
	// ^insights\/… block in appinfo/info.xml. Their own prefix rather than a
	// corner of published/, because create and retry are the app's first
	// mutating USER routes and `^published\/.+$` is declared GET,HEAD — a POST
	// hidden inside a GET declaration is exactly the change nobody would read.
	insightsURLPath = "/insights"

	// ncInsightsRoot is the folder an insight is delivered into, relative to the
	// requester's own WebDAV home. Deliberately not a child of
	// ncRecordingsMount; see the note above.
	ncInsightsRoot = "Cassini Insights"
)

const (
	// maxInsightMeetings caps how many meetings one insight may name. It is the
	// context bundle's own cap because the cost is the same staging work, and
	// because a bundle that overruns every model's context window is not made
	// answerable by the model call that follows it.
	maxInsightMeetings = maxContextMeetings

	// maxInsightRequestBytes bounds the create body. It carries ids, a workflow
	// name and a question — kilobytes — so this is generous, and it exists so an
	// unbounded POST cannot be read into memory.
	maxInsightRequestBytes = 64 << 10

	// maxInsightQuestionRunes bounds a freeform question. Long enough for a
	// paragraph of framing, short enough that the question cannot become the
	// context.
	maxInsightQuestionRunes = 2000

	// maxInsightDocumentBytes bounds what one run may produce. An insight is
	// prose over a handful of meetings; four megabytes is orders of magnitude
	// past that, and the bound is a refusal rather than a truncation — half an
	// answer with nothing in the bytes saying so is worse than no answer.
	maxInsightDocumentBytes = 4 << 20

	// maxInsightRecordBytes bounds the run record the child writes beside the
	// document. It is a fixed-shape JSON object of a few kilobytes.
	maxInsightRecordBytes = 1 << 20

	// insightRunTimeout is the backstop for one attempt: staging, one
	// `meetings context`, one `insight run`. It is NOT the model's leash — each
	// endpoint carries its own TimeoutSec (D-696) because a CPU-bound local model
	// is legitimately slow — it is the bound past which a child has stopped
	// making progress and is holding a slot and a staging directory.
	insightRunTimeout = 60 * time.Minute

	// insightStageTimeout bounds the preparation half on its own, so a Nextcloud
	// outage fails in minutes instead of eating the whole run budget before the
	// model is ever asked.
	insightStageTimeout = 10 * time.Minute

	// insightFinishTimeout bounds the one write that records how an attempt
	// ended. It runs on a context detached from the attempt's, because a run that
	// timed out must still be able to say that it timed out.
	insightFinishTimeout = 30 * time.Second

	// maxConcurrentInsightRuns bounds how many runs may hold a staging directory
	// and a pair of subprocesses at once. Runs past it stay `queued`, which is
	// what the status word means; the alternative is an unbounded number of
	// whole-archive downloads started by one afternoon's clicking.
	maxConcurrentInsightRuns = 2

	// maxInsightDeliveryAttempts bounds the search for a free file name in the
	// requester's folder. A run id is unique, so a taken name means a file put
	// there out of band; a handful of suffixes covers that without ever
	// overwriting anything.
	maxInsightDeliveryAttempts = 5
)

// insightQuestionPlaceholder is the token a workflow carries where a caller's
// own question goes (insight.QuestionPlaceholder in the recorder). The operator
// cannot import it — separate modules — so it matches the literal against the
// instruction the registry prints, exactly as settings_workflows.go mirrors the
// rest of that JSON rather than relaying it.
const insightQuestionPlaceholder = "{{QUESTION}}"

// The status vocabulary, fixed in gocassini's internal/insight package doc and
// re-expressed here because cassini-operator cannot import that module. These
// four words are the wire values the API serves AND the values insight_store.go
// persists — one declaration, because a status the database holds and a status
// the app has a card for that were allowed to drift would be a run nobody can
// render. There is deliberately no fifth.
const (
	insightStatusQueued    = "queued"
	insightStatusRunning   = "running"
	insightStatusSucceeded = "succeeded"
	insightStatusFailed    = "failed"
)

// newInsightRunID mints a run id in the scheme internal/insight fixes: "ins_"
// and sixteen lowercase hex characters, random rather than derived from the
// inputs, so two deliberate runs over the same meetings are two insights and
// never silently one. The id is stable across attempts, which is why it cannot
// encode the attempt.
func newInsightRunID() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("mint an insight id: %w", err)
	}
	return "ins_" + hex.EncodeToString(buf[:]), nil
}

// isInsightRunID reports whether id has the shape this service mints. Checked
// before any lookup, so a malformed id is refused without a database round trip
// and nothing a caller typed reaches a file name unvalidated.
func isInsightRunID(id string) bool {
	const prefix = "ins_"
	if len(id) != len(prefix)+16 || !strings.HasPrefix(id, prefix) {
		return false
	}
	for _, c := range id[len(prefix):] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// insightRunStore is what the run path needs of the run store (D-720). An
// interface rather than the concrete type so this half is testable without a
// database: every failure mode here is a denial, a timeout or a child's exit
// code, and none of them is easier to provoke through SQLite.
type insightRunStore interface {
	CreateRun(ctx context.Context, run InsightRun) error
	GetRun(ctx context.Context, id string) (InsightRun, error)
	ListRuns(ctx context.Context, createdBy string) ([]InsightRun, error)
	BeginAttempt(ctx context.Context, id string) (InsightRun, error)
	FinishAttempt(ctx context.Context, id string, outcome InsightOutcome) error
}

// insightService is the whole feature: the HTTP surface, the staging, the two
// child invocations and the delivery.
//
// It holds the Runtime for the store, the LLM policy and the lifecycle context,
// and the ExAppConfig for the WebDAV primitives — the same split
// published/meetings-context has, where the ExApp config decides access and the
// operator does the work.
type insightService struct {
	rt     *Runtime
	exapp  ExAppConfig
	store  insightRunStore
	client *http.Client
	logger *log.Logger
	// slots bounds concurrent runs. Buffered and never closed: a send is the
	// claim, the deferred receive is the release.
	slots chan struct{}
	// now and newID are the two values that would otherwise make every run
	// unreproducible. Tests pin them; production gets the real thing.
	now   func() time.Time
	newID func() (string, error)
	// launchFn starts one attempt. In production it spawns the goroutine that
	// does the work; it is a seam for the same reason buildJobFn is one — a
	// handler's job is the answer it gives, and driving a whole run through it
	// to check a status code would leave the status codes tested through a
	// subprocess and the subprocess tested through nothing.
	launchFn func(id string, claimed bool)
}

// newInsightService returns the service, or nil when this deployment cannot run
// an insight at all.
//
// Nil rather than a surface that always fails, exactly as meetingsContextHandler
// decides: outside an AppAPI deployment there is no caller to stage as, under the
// local sink there is no Nextcloud to stage from or deliver into, and with no CLI
// there is nothing to run. In each case the routes are simply not mounted, which
// is the same answer the rest of the app gives there.
func newInsightService(rt *Runtime, exapp ExAppConfig, logger *log.Logger) *insightService {
	if rt == nil || rt.store == nil || !exapp.appAPIActive() || exapp.PublishSink != publishSinkNextcloudFiles {
		return nil
	}
	if strings.TrimSpace(rt.cfg.CassiniBin) == "" {
		if logger != nil {
			logger.Printf("insights: no cassini binary configured — %s is not served", insightsURLPath)
		}
		return nil
	}
	service := &insightService{
		rt:    rt,
		exapp: exapp,
		// The run store D-720 owns. Constructed here rather than hung on the
		// Runtime so the run path has exactly one place that knows the concrete
		// type; the crash reconciliation it needs happens inside its own reads.
		store: newInsightStore(rt.store),
		// Same client shape as the read proxy: no overall timeout, because the
		// staging bodies stream and each run's own context governs; a hung
		// upstream is bounded on headers.
		client: &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: ncFilesProxyHeadersTTL}},
		logger: logger,
		slots:  make(chan struct{}, maxConcurrentInsightRuns),
		now:    func() time.Time { return time.Now().UTC() },
		newID:  newInsightRunID,
	}
	service.launchFn = func(id string, claimed bool) { go service.launch(id, claimed) }
	return service
}

// --- the run path ------------------------------------------------------------

// launch performs one attempt in the background.
//
// TWO CONTEXTS, deliberately different. The work runs under rt.ctx — the
// operator's lifecycle — and never under the request's, because the request is
// long gone by the time the model answers and a browser that navigated away must
// not cancel a run somebody asked for. rt.ctx is what stops a run outliving the
// operator: on shutdown it is cancelled and every child dies with its process
// group.
//
// claimed says whether a handler already took the attempt. A retry claims inside
// the request, because BeginAttempt is the lock that makes its 409 exact; a
// create answers 201/queued and claims here, which is what lets a run wait for a
// slot under the status word that describes it.
func (s *insightService) launch(id string, claimed bool) {
	select {
	case s.slots <- struct{}{}:
	case <-s.rt.ctx.Done():
		return
	}
	defer func() { <-s.slots }()

	run, err := s.claim(id, claimed)
	if err != nil {
		// A busy run is not an error here: another attempt holds it, and the one
		// thing this goroutine must not do is run a second one beside it.
		if !errors.Is(err, errInsightRunBusy) {
			s.logf("insights: claim run=%s: %v", id, err)
		}
		return
	}

	ctx, cancel := context.WithTimeout(s.rt.ctx, insightRunTimeout)
	defer cancel()
	outcome := s.perform(ctx, run)

	// Detached from the attempt's context on purpose: a run that was cancelled or
	// timed out has to be able to record that it was, and it cannot do that
	// through the context that just expired. Still bounded, and a row a killed
	// operator left `running` is repaired by the store's own startup sweep.
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), insightFinishTimeout)
	defer finishCancel()
	if err := s.store.FinishAttempt(finishCtx, id, outcome); err != nil {
		s.logf("insights: record the outcome of run=%s: %v", id, err)
	}
}

func (s *insightService) claim(id string, claimed bool) (InsightRun, error) {
	if claimed {
		return s.store.GetRun(s.rt.ctx, id)
	}
	return s.store.BeginAttempt(s.rt.ctx, id)
}

// perform does one attempt and says how it ended. It never returns an error:
// every way this can fail is a fact about the run that the person who asked has
// to be able to read off the card, so it comes back as an outcome rather than as
// something a caller must decide how to render.
func (s *insightService) perform(ctx context.Context, run InsightRun) InsightOutcome {
	// The endpoint this attempt will reach, read now rather than after the fact.
	// The child receives it through rt.childEnv() and the run row records which
	// of the configured providers answered — never its URL, which is ADMIN-only
	// on the settings surface and must not become USER-readable here.
	provider := s.resolvedInsightProvider()

	staging, err := os.MkdirTemp("", "cassini-insight-*")
	if err != nil {
		s.logf("insights: create staging directory for run=%s: %v", run.ID, err)
		return insightFailure("Cassini could not prepare the meetings on its own disk. An administrator can check the app's storage.")
	}
	// Every path, including a cancelled one: the directory holds whole
	// recordings, and one leaked per abandoned run is an archive on the ExApp
	// volume outside the access model.
	defer os.RemoveAll(staging)

	bundlePath, outcome, ok := s.stageBundle(ctx, staging, run)
	if !ok {
		return outcome
	}

	documentPath, recordPath, outcome, ok := s.runWorkflow(ctx, staging, run, bundlePath)
	if !ok {
		outcome.Provider = provider.id
		return outcome
	}

	model := provider.model
	if recorded := s.readRunRecord(recordPath, run.ID); recorded != "" {
		// What actually answered beats what was configured: an endpoint with no
		// model pinned resolves one in the recorder, and the run row has to name
		// the one that produced these bytes.
		model = recorded
	}

	delivered, err := s.deliver(ctx, run, documentPath)
	if err != nil {
		s.logf("insights: deliver run=%s for caller=%s: %v", run.ID, run.CreatedBy, err)
		return InsightOutcome{
			Status:   insightStatusFailed,
			Provider: provider.id,
			Model:    model,
			Error:    "The insight was written but could not be saved into your Nextcloud files. Check that you have space, then try again.",
		}
	}
	return InsightOutcome{
		Status:       insightStatusSucceeded,
		Provider:     provider.id,
		Model:        model,
		DocumentPath: delivered,
	}
}

// stageBundle downloads the run's meetings as the requester and renders them
// into one cassini.meetings.context.v1 bundle on disk.
//
// Access is re-resolved on every attempt rather than trusted from the create: a
// retry can be minutes or days later, and a meeting the requester could read then
// and cannot read now has to drop out here rather than be handed to a model.
func (s *insightService) stageBundle(ctx context.Context, staging string, run InsightRun) (string, InsightOutcome, bool) {
	ctx, cancel := context.WithTimeout(ctx, insightStageTimeout)
	defer cancel()

	readable, catalog, ok := s.exapp.readableMeetingsForCaller(ctx, s.client, run.CreatedBy, s.logger)
	if !ok {
		return "", insightFailure("Cassini could not read your meeting list from Nextcloud. Try again in a moment."), false
	}

	catalogPath := filepath.Join(staging, "catalog.json")
	if err := os.WriteFile(catalogPath, catalog, 0o600); err != nil {
		s.logf("insights: stage catalog for run=%s: %v", run.ID, err)
		return "", insightFailure("Cassini could not prepare the meetings on its own disk. An administrator can check the app's storage."), false
	}

	budget := int64(maxContextStagedBytes)
	staged := make([]string, 0, len(run.MeetingIDs))
	for _, id := range run.MeetingIDs {
		source, permitted := readable[id]
		if !permitted {
			// Denied and absent are one answer everywhere else in this archive and
			// they are one answer here: the run says the meeting is no longer
			// available to it, and the log says which case it was.
			s.logf("insights: run=%s caller=%s asked for id=%s, which is not in their readable set", run.ID, run.CreatedBy, id)
			return "", insightFailure("One of these meetings is no longer available to you, so the insight was not run."), false
		}
		destPath := filepath.Join(staging, id+".opus")
		status, err := s.exapp.stageMeetingForContext(ctx, s.client, run.CreatedBy, source, destPath, &budget)
		switch {
		case err == nil:
		case status == http.StatusNotFound || status == http.StatusUnauthorized || status == http.StatusForbidden:
			s.logf("insights: run=%s caller=%s denied id=%s at fetch -> %d", run.ID, run.CreatedBy, id, status)
			return "", insightFailure("One of these meetings is no longer available to you, so the insight was not run."), false
		default:
			s.logf("insights: run=%s stage id=%s for caller=%s: %v", run.ID, id, run.CreatedBy, err)
			return "", insightFailure("Cassini could not download one of these meetings from Nextcloud. Try again in a moment."), false
		}
		staged = append(staged, destPath)
	}

	bundlePath := filepath.Join(staging, "context.json")
	args := append([]string{"meetings", "context", "--local", "--catalog", catalogPath, "--json"}, staged...)
	// The bundle is the same document published/meetings-context serves over the
	// same recordings, so it gets that endpoint's bound.
	if _, err := s.runCassini(ctx, args, s.contextChildEnv(), bundlePath, maxContextDocumentBytes, run.ID); err != nil {
		return "", insightFailure("Cassini could not assemble these meetings into one document. An administrator can check the app log."), false
	}
	return bundlePath, InsightOutcome{}, true
}

// runWorkflow asks the model, once, and returns the paths of the document and
// the run record the child wrote.
//
// The document goes to stdout rather than to --out so it can be capped as it is
// written: `cassini insight run` prints the document to stdout and its notes to
// stderr, and a bound that fails the write is the difference between a loud
// refusal and a wedged child filling the ExApp volume.
func (s *insightService) runWorkflow(ctx context.Context, staging string, run InsightRun, bundlePath string) (documentPath, recordPath string, outcome InsightOutcome, ok bool) {
	documentPath = filepath.Join(staging, "insight.md")
	recordPath = filepath.Join(staging, "record.json")
	args := []string{"insight", "run", "--context", bundlePath, "--workflow", run.WorkflowID, "--record", recordPath}
	if question := strings.TrimSpace(run.Question); question != "" {
		args = append(args, "--question", question)
	}

	// No --model. The endpoint and its model reach the child through
	// rt.childEnv(), which reads the CURRENT LLM policy at spawn time, and that
	// is what makes a retry re-resolve rather than replay: if the stored provider
	// were replayed, "no provider configured" and "401" would be exactly the two
	// failures the retry button could never fix.
	code, err := s.runCassini(ctx, args, s.insightChildEnv(), documentPath, maxInsightDocumentBytes, run.ID)
	if err != nil {
		return "", "", InsightOutcome{Status: insightStatusFailed, Error: explainInsightExit(ctx, code)}, false
	}
	return documentPath, recordPath, InsightOutcome{}, true
}

// explainInsightExit turns `cassini insight run`'s exit code into a sentence the
// person who asked can act on.
//
// The code space is the contract — it is documented in the command's own help
// precisely so a caller need not read the message — so this switches on it and
// never on the text. A message-derived classification changes silently whenever a
// message is reworded, which is the failure this avoids.
func explainInsightExit(ctx context.Context, code int) string {
	if ctx.Err() != nil {
		return fmt.Sprintf("The insight took longer than %d minutes and was stopped. A smaller selection, or a faster endpoint, will finish.", int(insightRunTimeout.Minutes()))
	}
	switch code {
	case 1:
		return "The insight was produced but Cassini could not write it down. An administrator can check the app's storage."
	case 2:
		return insightReasonBadRequest + ": This insight could not be run as it was asked for. An administrator can check the app log."
	case 3:
		return insightReasonNoProvider + ": No AI endpoint is configured, so there was nothing to ask. An administrator can add one in Cassini's AI settings."
	case 4:
		return insightReasonProviderRefused + ": The AI endpoint refused the request — its key, its quota, or the model name. An administrator can fix it in Cassini's AI settings."
	case 5:
		return insightReasonModelFailed + ": The model did not answer. Trying again is a reasonable response to this one."
	default:
		return "The insight run stopped unexpectedly. An administrator can check the app log."
	}
}

// The four words `internal/insight` classifies a failure with, carried on the
// front of the sentence the exit code produced.
//
// The token is there for the app, which reads it to decide WHICH failure this
// is — "no endpoint configured" and "the endpoint rejected the key" are two
// different things to do next, and only one of them is worth offering an
// administrator a link for. It is deliberately not a second field on {run}: the
// contract's run object is fixed, and a classification carried beside the
// sentence it classifies is a second thing that can disagree with it.
//
// Only the codes insight.Reason actually names are tagged. Exit 1 (produced but
// unwritable), a run stopped on its own deadline and an unrecognised code are
// none of the four, and inventing a token for them would let the app show a
// confident cause for a failure nobody classified.
const (
	insightReasonBadRequest      = "bad-request"
	insightReasonNoProvider      = "no-provider"
	insightReasonProviderRefused = "provider-refused"
	insightReasonModelFailed     = "model-failed"
)

// deliver PUTs the document into the requester's own Nextcloud home and returns
// the path it was written to.
//
// It never overwrites: each candidate name is checked first, and a run that
// cannot find a free one fails rather than replacing bytes somebody kept. The run
// id is in the name, so a collision means a file placed there out of band.
func (s *insightService) deliver(ctx context.Context, run InsightRun, documentPath string) (string, error) {
	info, err := os.Stat(documentPath)
	if err != nil {
		return "", err
	}
	if info.Size() == 0 {
		// An exit 0 with no bytes would read as "these meetings say nothing",
		// which is a claim no failure is allowed to make.
		return "", errors.New("the insight document is empty")
	}
	for _, dir := range insightFolderChain() {
		if err := s.exapp.davMkcol(ctx, s.client, run.CreatedBy, dir); err != nil {
			return "", err
		}
	}
	base := insightDocumentBaseName(run, s.now())
	for attempt := 1; attempt <= maxInsightDeliveryAttempts; attempt++ {
		name := base + ".md"
		if attempt > 1 {
			name = fmt.Sprintf("%s-%d.md", base, attempt)
		}
		relPath := ncInsightsRoot + "/" + name
		taken, err := s.davExists(ctx, run.CreatedBy, relPath)
		if err != nil {
			return "", err
		}
		if taken {
			s.logf("insights: run=%s found %s already in caller=%s's files; it was not overwritten", run.ID, relPath, run.CreatedBy)
			continue
		}
		if _, err := s.exapp.davPutFileStatus(ctx, s.client, run.CreatedBy, relPath, documentPath, "text/markdown; charset=utf-8"); err != nil {
			return "", err
		}
		return relPath, nil
	}
	return "", fmt.Errorf("%s already holds %d files named like this insight", ncInsightsRoot, maxInsightDeliveryAttempts)
}

// insightFolderChain is the collections that must exist before the PUT, outermost
// first. MKCOL creates one level at a time, and ncInsightsRoot is allowed to be a
// nested path so that constant can change without this changing.
func insightFolderChain() []string {
	parts := strings.Split(strings.Trim(ncInsightsRoot, "/"), "/")
	chain := make([]string, 0, len(parts))
	for i := range parts {
		chain = append(chain, strings.Join(parts[:i+1], "/"))
	}
	return chain
}

// insightDocumentBaseName names the file the requester will see in their own
// files: the day it was asked for, what was asked, and the run it came from. The
// run id is what makes it unique and what ties the file back to the card.
func insightDocumentBaseName(run InsightRun, now time.Time) string {
	workflow := strings.TrimSpace(run.WorkflowID)
	if workflow == "" {
		workflow = "insight"
	}
	return fmt.Sprintf("%s-%s-%s", now.UTC().Format("2006-01-02"), workflow, run.ID)
}

// davExists reports whether relPath is already a file in userID's home. A HEAD
// rather than a GET: the answer is the status line, and the body would be a
// document this run has no business reading.
func (s *insightService) davExists(ctx context.Context, userID, relPath string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, s.exapp.davFileURL(userID, relPath), nil)
	if err != nil {
		return false, err
	}
	s.exapp.setAppAPIDAVHeadersForUser(req, userID)
	resp, err := s.client.Do(req)
	if err != nil {
		return false, err
	}
	defer drainClose(resp.Body)
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return false, nil
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return true, nil
	default:
		// Anything else is an answer this cannot interpret, and reading an
		// uninterpretable answer as "free" is how a file gets overwritten.
		return false, fmt.Errorf("HEAD %s -> %d", relPath, resp.StatusCode)
	}
}

// fetchInsightDocument reads a delivered insight back out of the requester's own
// files, as them, so GET insights/<id> serves the document beside the run and a
// file the requester has since deleted answers as gone rather than as bytes the
// operator happened to keep.
func (s *insightService) fetchInsightDocument(ctx context.Context, caller, relPath string) (string, error) {
	body, status, err := s.exapp.davGetBytes(ctx, s.client, caller, relPath)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("GET %s -> %d", relPath, status)
	}
	if len(body) > maxInsightDocumentBytes {
		return "", fmt.Errorf("%s exceeds the %d MiB an insight may be", relPath, maxInsightDocumentBytes>>20)
	}
	return string(body), nil
}

// --- the children -------------------------------------------------------------

// runCassini runs one CLI verb, writing its stdout to outPath under a hard byte
// cap, and returns the child's exit code.
//
// Staged to a file rather than streamed anywhere, because the child's exit code
// is only known after its last byte: anything that committed to an answer before
// finding out the run failed would turn a failure into a successful empty one.
func (s *insightService) runCassini(ctx context.Context, args, env []string, outPath string, maxBytes int64, runID string) (int, error) {
	out, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	var stderr truncatingBuffer
	stderr.remaining = 8 << 10

	cmd := exec.CommandContext(ctx, s.rt.cfg.CassiniBin, args...)
	cmd.Stdout = &cappedWriter{out: out, remaining: maxBytes}
	cmd.Stderr = &stderr
	cmd.Env = env
	// Kill the whole process group on cancel so the ffprobe grandchildren the
	// reader spawns do not outlive an abandoned run.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd.Process) }
	runErr := cmd.Run()
	if closeErr := out.Close(); runErr == nil {
		runErr = closeErr
	}
	if runErr == nil {
		return 0, nil
	}
	code := -1
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		code = exitErr.ExitCode()
	}
	// The verb and the exit code, never the whole argv: a caller's own question
	// travels on that command line, and a run's log line is not the place for it.
	s.logf("insights: run=%s cassini %s exit=%d: %v: %s", runID, strings.Join(args[:2], " "), code, runErr, strings.TrimSpace(stderr.buf.String()))
	return code, runErr
}

// insightChildEnv is what `cassini insight run` receives: the operator's own
// environment with the STT and LLM policies applied — it needs the LLM
// credentials, which is the whole point of the call — minus the operator's own
// secrets, which no `cassini` verb reads and which a child has no business being
// able to spend.
//
// APP_SECRET matters most: it is the AppAPI shared secret, and a process holding
// it can act as ANY Nextcloud account. Handing it to a child that only has to
// read three staged files would make every insight run a full impersonation
// capability for the length of the run.
func (s *insightService) insightChildEnv() []string {
	return withoutEnv(s.rt.childEnv(), operatorOnlySecretEnv())
}

// contextChildEnv is what `cassini meetings context` receives: the same, and
// every LLM variable as well. That child reads local files and prints a
// document; it never calls a model, so an endpoint credential in its environment
// could only ever be a credential in one more place.
func (s *insightService) contextChildEnv() []string {
	drop := operatorOnlySecretEnv()
	for name := range inheritedLLMEnv() {
		drop[name] = true
	}
	return withoutEnv(s.rt.childEnv(), drop)
}

// operatorOnlySecretEnv names the credentials that belong to the operator and to
// nothing it spawns for an insight.
func operatorOnlySecretEnv() map[string]bool {
	return map[string]bool{
		envAppSecret:                    true,
		"CASSINI_TALK_RECORDING_SECRET": true,
		"TALK_RECORDING_SECRET":         true,
		envTalkSignalingInternalSecret:  true,
		"CASSINI_OPERATOR_API_TOKEN":    true,
	}
}

// withoutEnv removes the named variables from a KEY=VALUE environment.
//
// A denylist rather than an allowlist on purpose: PATH, HOME, TMPDIR and the
// loader's own variables are what make the child able to run at all, and an
// allowlist that forgets one fails as a mystery instead of as a missing
// credential.
func withoutEnv(env []string, drop map[string]bool) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		if drop[key] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// --- what settings and the child said -----------------------------------------

// insightProviderRef is the endpoint an attempt resolved to, as the run row may
// record it: the provider's id and model, never its base URL. The URL is served
// only on the ADMIN settings surface, and a USER-readable run must not be the
// way it becomes public.
type insightProviderRef struct {
	id    string
	model string
}

func (s *insightService) resolvedInsightProvider() insightProviderRef {
	step := s.rt.currentLLMSettings().view().Effective.Insight
	if step == nil {
		return insightProviderRef{}
	}
	return insightProviderRef{id: step.Provider, model: step.Model}
}

// readRunRecord returns the model named by the record the child wrote beside the
// document, or "" when there is none to read.
//
// Mirrored rather than relayed, like the workflow registry, so a deployment
// pointed at a binary printing something else records nothing rather than
// recording whatever it printed. Everything else in that record — the workflow
// triple, the context hash, the timings — is already in the document's own
// frontmatter, which is the copy that has to survive.
//
// A missing or unreadable record is not a failure: the document exists and is
// what was asked for, and the run simply cannot name the model that produced it.
func (s *insightService) readRunRecord(recordPath, runID string) string {
	info, err := os.Stat(recordPath)
	if err != nil {
		s.logf("insights: run=%s wrote no run record: %v", runID, err)
		return ""
	}
	if info.Size() > maxInsightRecordBytes {
		s.logf("insights: run=%s wrote a %d byte run record, past the %d byte cap", runID, info.Size(), maxInsightRecordBytes)
		return ""
	}
	raw, err := os.ReadFile(recordPath)
	if err != nil {
		s.logf("insights: run=%s read the run record: %v", runID, err)
		return ""
	}
	var record struct {
		Provider struct {
			Model string `json:"model"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		s.logf("insights: run=%s decode the run record: %v", runID, err)
		return ""
	}
	return strings.TrimSpace(record.Provider.Model)
}

// --- helpers ------------------------------------------------------------------

// insightFailure is a terminal outcome carrying a sentence the requester can act
// on. Nothing built here names a provider or a model, because these are the
// failures that happen before or instead of the model call.
func insightFailure(message string) InsightOutcome {
	return InsightOutcome{Status: insightStatusFailed, Error: message}
}

func (s *insightService) logf(format string, args ...any) {
	if s.logger != nil {
		s.logger.Printf(format, args...)
	}
}

// insightCatalogRooms reads the rooms the requested meetings belong to out of the
// caller's own catalog, in first-appearance order and without repeats.
//
// The run carries them because an insight over several meetings is routinely an
// insight across several rooms, and a list of meeting ids alone cannot be read by
// the person who asked for it. Empty for a meeting published before the field
// existed, which is a gap and not an error.
func insightCatalogRooms(catalog []byte, ids []string) []string {
	var document struct {
		Meetings []struct {
			ID     string `json:"id"`
			RoomID string `json:"roomId"`
		} `json:"meetings"`
	}
	out := []string{}
	if err := json.Unmarshal(catalog, &document); err != nil {
		return out
	}
	rooms := make(map[string]string, len(document.Meetings))
	for _, entry := range document.Meetings {
		if id := strings.TrimSpace(entry.ID); id != "" {
			rooms[id] = strings.TrimSpace(entry.RoomID)
		}
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		room := rooms[id]
		if room == "" || seen[room] {
			continue
		}
		seen[room] = true
		out = append(out, room)
	}
	return out
}
