package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cassini-operator/internal/operator/appapi"

	"github.com/oklog/ulid/v2"
)

// The imports HTTP surface (D-828): the Import tab's backend.
//
// The browser stages each dropped file into the CALLER'S OWN Files over
// Nextcloud's WebDAV (`.CassiniImports/<name>`), then POSTs only small
// JSON here — staged paths plus the sharing selection — keeping the operator
// API's small-body invariant. The worker pulls each staged file server-side AS
// THE CALLER, so a request can only ever name files its own author can read,
// and removes the staged copy once the import lands.
//
// Like insights, the surface is per-user: a job is listed, read, canceled and
// retried only by its creator (404 for anyone else), while the delivered
// meeting itself is shared state governed by the archive's shares.

const importsURLPath = "/imports"

// importStagingDir is the staging folder the viewer uploads into, inside the
// caller's own Files. Hidden by the leading dot, like any app staging area.
const importStagingDir = ".CassiniImports"

// maxImportUploadBytes bounds one staged recording. Multi-hour meetings at the
// D-850 encode sizes reach the hundreds of MB; 4 GiB is far above any real
// meeting while still refusing an unbounded disk write.
const maxImportUploadBytes = int64(4) << 30

const maxImportRequestBytes = 64 << 10

// importBatchLimit bounds one submit. The dialog imports a handful of files; a
// larger drop arrives as several batches.
const importBatchLimit = 50

type importService struct {
	rt     *Runtime
	exapp  ExAppConfig
	logger *log.Logger
	// importer owns the ingest; the service owns fetching staged bytes and the
	// durable job rows around it.
	importer *meetingImporter
	// client fetches staged files as the caller. No overall timeout: bodies
	// stream and each pull runs under the worker's own context.
	client *http.Client

	// mu guards the two fields below and nothing else: which job the worker is
	// on and how to cancel it. It is never held across a database call or a
	// Nextcloud request, so a slow Nextcloud for one caller cannot stall every
	// other caller's poll.
	mu        sync.Mutex
	runningID string
	cancelRun context.CancelFunc
	// userLocks serializes one caller's staging work — registering uploads,
	// confirming them, removing files, sweeping the folder — so two tabs cannot
	// race on one folder, while callers stay independent of each other.
	userLocks keyedLocks
}

// running reports the job the worker is on, or "" between jobs.
func (s *importService) running() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runningID
}

// withUserLock runs fn holding the caller's staging lock, bounded by the
// deadline on ctx so a failed Nextcloud cannot hold it indefinitely.
func (s *importService) withUserLock(ctx context.Context, user string, fn func(ctx context.Context) error) error {
	release, err := s.userLocks.acquire(ctx, user)
	if err != nil {
		return fmt.Errorf("staging is busy for %s: %w", user, err)
	}
	defer release()
	return fn(ctx)
}

// newImportService wires the Import surface, or reports (nil) that it cannot be
// served: no AppAPI identity, no Nextcloud archive, or no CLI to validate with.
// A route that always fails is worse than a route that is not there.
func newImportService(rt *Runtime, exapp ExAppConfig, logger *log.Logger) *importService {
	if rt == nil || rt.store == nil || !exapp.appAPIActive() || exapp.PublishSink != publishSinkNextcloudFiles {
		return nil
	}
	if strings.TrimSpace(rt.cfg.CassiniBin) == "" {
		if logger != nil {
			logger.Printf("imports: no cassini binary configured — %s is not served", importsURLPath)
		}
		return nil
	}
	service := &importService{
		rt:       rt,
		exapp:    exapp,
		logger:   logger,
		importer: newMeetingImporter(rt.cfg, exapp, rt, logger),
		client:   &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: ncFilesProxyHeadersTTL}},
	}
	service.startWorker()
	service.startStagingGC()
	return service
}

func (s *importService) register(root *http.ServeMux) {
	root.HandleFunc(importsURLPath, insightNoStore(s.serveCollection))
	root.HandleFunc(importsURLPath+"/", insightNoStore(s.serveItem))
}

func (s *importService) serveCollection(w http.ResponseWriter, r *http.Request) {
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

func (s *importService) serveItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, importsURLPath), "/")
	if rest == "" {
		s.serveCollection(w, r)
		return
	}
	caller, ok := s.caller(w, r)
	if !ok {
		return
	}
	if rest == "staging/clear" {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		s.reconcileUploads(r.Context(), caller)
		if err := s.clearUserStaging(r.Context(), caller); err != nil {
			writeJSONError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"accepted": true})
		return
	}
	id, action, _ := strings.Cut(rest, "/")
	job, ok := s.ownJob(w, r, caller, id)
	if !ok {
		return
	}
	switch action {
	case "":
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		writeJSON(w, http.StatusOK, job)
	case "heartbeat", "staged", "upload-failed", "remove":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		s.stagingAction(w, r, job, action)
	case "cancel":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		s.cancel(w, r, job)
	case "retry":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		s.retry(w, r, job)
	default:
		http.NotFound(w, r)
	}
}

func (s *importService) caller(w http.ResponseWriter, r *http.Request) (string, bool) {
	caller := appapi.UserID(r.Context())
	if caller == "" {
		s.logf("imports: missing caller identity on %s %s — refusing to answer", r.Method, r.URL.Path)
		writeJSONError(w, http.StatusBadGateway, "no verified caller identity")
		return "", false
	}
	return caller, true
}

// ownJob answers 404 for an id that does not exist OR belongs to someone else —
// the same non-disclosure every insights/<id> route makes.
func (s *importService) ownJob(w http.ResponseWriter, r *http.Request, caller, id string) (ImportJob, bool) {
	job, ok, err := s.rt.store.ImportJob(r.Context(), id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return ImportJob{}, false
	}
	if !ok || job.CreatedBy != caller {
		http.NotFound(w, r)
		return ImportJob{}, false
	}
	return job, true
}

func (s *importService) list(w http.ResponseWriter, r *http.Request, caller string) {
	s.reconcileUploads(r.Context(), caller)
	jobs, err := s.rt.store.ImportJobsFor(r.Context(), caller)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"imports": jobs})
}

type importCreateRequest struct {
	Prepare bool `json:"prepare"`
	Files   []struct {
		ExpectedBytes int64             `json:"expectedBytes"`
		StagedPath    string            `json:"stagedPath"`
		SourceName    string            `json:"sourceName"`
		Share         bool              `json:"share"`
		Audience      []importPrincipal `json:"audience"`
	} `json:"files"`
}

func (s *importService) create(w http.ResponseWriter, r *http.Request, caller string) {
	var request importCreateRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxImportRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid import request")
		return
	}
	if len(request.Files) == 0 || len(request.Files) > importBatchLimit {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("an import names between 1 and %d staged files", importBatchLimit))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	release, err := s.userLocks.acquire(ctx, caller)
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "staging is busy; try again")
		return
	}
	defer release()
	r = r.WithContext(ctx)
	batchID := ulid.Make().String()
	jobs := make([]ImportJob, 0, len(request.Files))
	for _, file := range request.Files {
		staged, err := sanitizeStagedImportPath(file.StagedPath)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		sharing := importAudience{Share: file.Share}
		if file.Share {
			for _, principal := range file.Audience {
				kind := strings.TrimSpace(principal.Type)
				id := strings.TrimSpace(principal.ID)
				if _, ok := shareTypeForPrincipal(kind); !ok || id == "" {
					writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid share recipient %q %q", principal.Type, principal.ID))
					return
				}
				sharing.Audience = append(sharing.Audience, importPrincipal{Type: kind, ID: id})
			}
		}
		sourceName := strings.TrimSpace(file.SourceName)
		if sourceName == "" {
			sourceName = path.Base(staged)
		}
		if request.Prepare && (file.ExpectedBytes <= 0 || file.ExpectedBytes > maxImportUploadBytes) {
			writeJSONError(w, http.StatusBadRequest, "upload must be between 1 byte and 4 GiB")
			return
		}
		state, stagingState := "", ""
		if request.Prepare {
			taken, err := s.rt.store.HasImportJobForPath(r.Context(), caller, staged)
			if err != nil {
				writeJSONError(w, 500, err.Error())
				return
			}
			if taken {
				writeJSONError(w, 409, "staging path already belongs to an upload; add the file again")
				return
			}
			state, stagingState = importStateUploading, "pending"
		}
		jobs = append(jobs, ImportJob{
			State: state, StagingState: stagingState, ExpectedBytes: file.ExpectedBytes,
			ID: ulid.Make().String(), BatchID: batchID, CreatedBy: caller,
			SourceName: sourceName, StagedPath: staged, Sharing: sharing,
		})
	}
	if !request.Prepare {
		existing, err := s.rt.store.ImportJobsFor(r.Context(), caller)
		if err != nil {
			writeJSONError(w, 500, err.Error())
			return
		}
		newJobs := []ImportJob{}
		for i := range jobs {
			found := false
			for _, old := range existing {
				if old.StagedPath != jobs[i].StagedPath {
					continue
				}
				found = true
				if old.State == importStateStaged {
					if err := s.rt.store.RetryImportJob(r.Context(), old.ID); err != nil {
						writeJSONError(w, 500, err.Error())
						return
					}
				} else if old.State == importStateUploading || old.State == importStateUploadFailed {
					writeJSONError(w, 409, "upload is not complete")
					return
				}
				jobs[i] = old
				break
			}
			if !found {
				newJobs = append(newJobs, jobs[i])
			}
		}
		if len(newJobs) > 0 {
			if err := s.rt.store.CreateImportJobs(r.Context(), newJobs); err != nil {
				writeJSONError(w, 500, err.Error())
				return
			}
		}
	} else if err := s.rt.store.CreateImportJobs(r.Context(), jobs); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	created, err := s.rt.store.ImportJobsFor(r.Context(), caller)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	byID := map[string]ImportJob{}
	for _, job := range created {
		byID[job.ID] = job
	}
	response := make([]ImportJob, 0, len(jobs))
	for _, job := range jobs {
		response = append(response, byID[job.ID])
	}
	// 202, not 201 with a Location: under AppAPI a Location header turns into a
	// 302 out of the proxy (see modelsHandler), and the dialog polls the
	// collection anyway.
	writeJSON(w, http.StatusAccepted, map[string]any{"batchId": batchID, "imports": response})
}

func (s *importService) cancel(w http.ResponseWriter, r *http.Request, job ImportJob) {
	if job.isCLI() {
		writeJSONError(w, http.StatusConflict, importCLIRecovery)
		return
	}
	if job.State == importStateUploading {
		writeJSONError(w, 409, "upload is still active")
		return
	}
	if importStateTerminal(job.State) {
		writeJSON(w, http.StatusOK, job)
		return
	}
	if err := s.rt.store.CancelImportJob(r.Context(), job.ID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.mu.Lock()
	if s.runningID == job.ID && s.cancelRun != nil {
		s.cancelRun()
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
}

func (s *importService) retry(w http.ResponseWriter, r *http.Request, job ImportJob) {
	current, ok, err := s.rt.store.ImportJob(r.Context(), job.ID)
	if err != nil {
		writeJSONError(w, 500, err.Error())
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	job = current
	if job.isCLI() {
		writeJSONError(w, http.StatusConflict, importCLIRecovery)
		return
	}
	if job.ClearedAt != "" || job.StagingState == "removed" || (job.ExpectedBytes > 0 && job.StagingState != "present") {
		writeJSONError(w, 409, "staged file was removed; upload the file again")
		return
	}
	if job.State != importStateFailed && job.State != importStateCanceled && job.State != importStateStaged {
		writeJSON(w, http.StatusOK, job)
		return
	}
	// A cancel lands on the row before the worker has let go of the job; a
	// retry in that window would be claimed by the dying run's bookkeeping.
	if s.running() == job.ID {
		writeJSONError(w, http.StatusConflict, "cancellation is still finishing; retry in a moment")
		return
	}
	if err := s.rt.store.RetryImportJob(r.Context(), job.ID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
}

// sanitizeStagedImportPath accepts only a file directly inside the caller's
// import staging folder. The pull runs as the caller, so this is tidiness and
// legibility, not the access control — that is Nextcloud's.
func sanitizeStagedImportPath(raw string) (string, error) {
	staged := strings.TrimPrefix(strings.TrimSpace(raw), "/")
	dir, name := path.Split(staged)
	if strings.Trim(dir, "/") != importStagingDir || name == "" || name == "." || name == ".." || strings.ContainsAny(name, "\\\x00\r\n") {
		return "", fmt.Errorf("staged files live directly in %s/", importStagingDir)
	}
	return importStagingDir + "/" + name, nil
}

// --- the worker ---------------------------------------------------------------

func (s *importService) startWorker() {
	rt := s.rt
	rt.workerWG.Add(1)
	go func() {
		defer rt.workerWG.Done()
		// Uploaded imports resume after process death. Interrupted CLI imports
		// become failures with local-source recovery guidance; the web worker
		// cannot fetch those files. Terminal rows stay terminal.
		if n, err := rt.store.RequeueUnfinishedImports(rt.ctx); err != nil {
			s.logf("import job recovery: %v", err)
		} else if n > 0 {
			s.logf("import job recovery: re-queued %d interrupted import(s)", n)
		}
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			if rt.ctx.Err() != nil {
				return
			}
			if job, ok, err := rt.store.nextQueuedImport(rt.ctx); err == nil && ok {
				s.runImportJob(job)
			} else if err != nil && rt.ctx.Err() == nil {
				s.logf("import worker: %v", err)
			}
			select {
			case <-rt.ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
}

func (s *importService) runImportJob(job ImportJob) {
	rt := s.rt
	ctx, cancel := context.WithCancel(rt.ctx)
	defer cancel()
	// Recheck under the lock so a cancel that landed after the queue read wins.
	s.mu.Lock()
	current, ok, err := rt.store.ImportJob(ctx, job.ID)
	if err != nil || !ok || current.State != importStateQueued {
		s.mu.Unlock()
		return
	}
	s.runningID, s.cancelRun = job.ID, cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.runningID, s.cancelRun = "", nil
		s.mu.Unlock()
	}()

	result, err := s.performImport(ctx, job)
	if ctx.Err() != nil && rt.ctx.Err() != nil {
		// Operator shutdown, not a verdict: leave the row for recovery requeue.
		_ = s.setState(job.ID, importStateQueued, "", "")
		return
	}
	if ctx.Err() != nil {
		// A user cancel: the row is already canceled, and the guard inside
		// SetImportJobState keeps any late write from resurrecting it.
		return
	}
	if err != nil {
		s.logf("import %s (%s): %v", job.ID, job.SourceName, err)
		_ = s.setState(job.ID, importStateFailed, "", err.Error())
		return
	}
	_ = rt.store.SetImportJobProvenance(context.Background(), job.ID, result.Format, result.ContentID, result.SourceRoomID, result.CreatedAtUTC, result.RecordedAtLocal)
	if err := rt.store.SetImportJobOutcome(context.Background(), job.ID, result.DeliveredName, result.FileID, result.ImportedAtUTC); err != nil {
		s.logf("import %s: persist outcome: %v", job.ID, err)
		return
	}
	s.logf("import succeeded id=%s source=%q delivered=%s file_id=%d shared_with=%d searchable=%t",
		job.ID, job.SourceName, result.DeliveredName, result.FileID, result.SharedWith, result.SearchIndexed)
	// The staged copy is spent. Best effort: a leftover costs the caller some
	// quota and is theirs to see; it never fails a landed import.
	if err := s.exapp.davDeleteUserFile(context.Background(), s.client, job.CreatedBy, job.StagedPath); err != nil {
		s.logf("import %s: staged copy not removed (%v)", job.ID, err)
	} else {
		_ = rt.store.MarkImportStagingRemoved(context.Background(), job.ID)
	}
}

func (s *importService) performImport(ctx context.Context, job ImportJob) (meetingImportResult, error) {
	if job.isCLI() {
		return meetingImportResult{}, fmt.Errorf("%s", importCLIRecovery)
	}
	_ = s.setState(job.ID, importStateFetching, "fetching", "")
	staging, err := os.MkdirTemp(rtWorkScratchDir(s.rt.cfg.WorkRoot), ".import-fetch-")
	if err != nil {
		return meetingImportResult{}, err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	local := filepath.Join(staging, "staged")
	if _, _, status, err := s.exapp.davDownloadFile(ctx, s.client, job.CreatedBy, job.StagedPath, local, maxImportUploadBytes); err != nil {
		if status == http.StatusNotFound {
			return meetingImportResult{}, fmt.Errorf("the staged file %s is no longer in your Files", job.StagedPath)
		}
		return meetingImportResult{}, fmt.Errorf("fetch the staged file: %w", err)
	}
	audience := make([]sharePrincipal, 0, len(job.Sharing.Audience))
	if job.Sharing.Share {
		for _, principal := range job.Sharing.Audience {
			audience = append(audience, sharePrincipal{Type: principal.Type, ID: principal.ID})
		}
	}
	return s.importer.run(ctx, meetingImportRequest{
		ImportID: job.ID, CreatedBy: job.CreatedBy, LocalPath: local, Audience: audience,
	}, func(state, phase string) {
		_ = s.setState(job.ID, state, phase, "")
	})
}

func (s *importService) setState(id, state, phase, errText string) error {
	return s.rt.store.SetImportJobState(context.Background(), id, state, phase, errText)
}

func (s *importService) logf(format string, args ...any) {
	if s.logger != nil {
		s.logger.Printf(format, args...)
	}
}

// rtWorkScratchDir is where import fetches land: beside the pipeline's own
// scratch, on the volume sized for recordings.
func rtWorkScratchDir(workRoot string) string {
	return workRoot
}
