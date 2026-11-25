package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
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

	// mu guards the three maps below and nothing else: which jobs are running
	// (and how to cancel each), which callers have a worker draining their
	// queue, and the disk space promised to running imports. It is never held across a Nextcloud request, so a slow Nextcloud
	// for one caller cannot stall every other caller's poll.
	mu sync.Mutex
	// running maps a running job's id to the cancel for its run.
	running map[string]context.CancelFunc
	// userWorkers holds the callers whose queue has a worker. Each caller gets
	// one worker, which runs their imports one at a time in the order they
	// were queued, so one person's imports never wait behind another's (#509).
	userWorkers map[string]bool
	// diskReserved maps a running import's id to the work-volume bytes it
	// may use (importDiskNeed). A new import starts only if the free space
	// covers these, its own need and importDiskReserve.
	diskReserved map[string]int64
	// diskWaiting is the line of imports waiting for disk space, in the
	// order they started waiting. Each import leaves room for every one ahead
	// of it, so a stream of small imports cannot keep a large one waiting
	// forever. The first in line competes only with running imports, so the
	// line cannot jam: with nothing running, it runs or fails.
	diskWaiting map[string]importDiskWaiter
	diskWaitSeq uint64
	// importSlots bounds how many imports run at once across all callers. A
	// slot is held for the whole import, not just the download: the staged
	// copy (up to maxImportUploadBytes) stays on the work volume, and the
	// validating `cassini publish` writes a second one, until the import
	// settles. Waiters are served in arrival order. Nil means unbounded
	// (handler-only tests).
	importSlots chan struct{}
	// perform runs one import; nil means performImport. A seam for tests.
	perform func(ctx context.Context, job ImportJob) (meetingImportResult, error)
	// userLocks serializes one caller's staging work — registering uploads,
	// confirming them, removing files, sweeping the folder — so two tabs cannot
	// race on one folder, while callers stay independent of each other.
	userLocks keyedLocks
}

// envMaxConcurrentImports bounds how many imports run at once across all
// callers. Each running import holds its staged file (up to
// maxImportUploadBytes) and the validation copy on the work volume, which
// recordings share, so raise it only with the disk to match.
const (
	envMaxConcurrentImports     = "CASSINI_MAX_CONCURRENT_IMPORTS"
	defaultMaxConcurrentImports = 3
)

// maxConcurrentImports reads envMaxConcurrentImports. Unset, unparsable or
// below 1 keeps the default: 0 would stop every import, and "unbounded" is not
// offered because each import costs disk the recordings also need.
func maxConcurrentImports(getenv func(string) string, logger *log.Logger) int {
	raw := strings.TrimSpace(getenv(envMaxConcurrentImports))
	if raw == "" {
		return defaultMaxConcurrentImports
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		if logger != nil {
			logger.Printf("imports: %s=%q is not a whole number of at least 1; using %d", envMaxConcurrentImports, raw, defaultMaxConcurrentImports)
		}
		return defaultMaxConcurrentImports
	}
	return n
}

// A staged-file download is abandoned when less than importFetchMinProgress
// arrives within importFetchIdleTimeout: a Nextcloud that stalls, or trickles
// a byte at a time, would otherwise hold an import slot until someone cancels.
// importFetchIdlePoll is how often the download's progress is checked.
var (
	importFetchIdleTimeout       = 2 * time.Minute
	importFetchMinProgress int64 = 64 << 10
	importFetchIdlePoll          = 5 * time.Second
)

// importTimeLimit bounds one import from its claim to its settling, so that no
// step — the download, `cassini publish`, the upload to Nextcloud, search
// indexing — can hold an import slot indefinitely. It grows with the file:
// a base allowance plus the time to move the file several times (fetch,
// publish copy, upload) at a slow 1 MiB/s. A 4 GiB file gets about 3h50m.
// A variable so tests can shorten it.
var importTimeLimit = func(job ImportJob) time.Duration {
	const base = 30 * time.Minute
	const bytesPerSecond = 1 << 20
	const passes = 3
	size := job.ExpectedBytes
	if size <= 0 || size > maxImportUploadBytes {
		size = maxImportUploadBytes
	}
	return base + time.Duration(passes*size/bytesPerSecond)*time.Second
}

// Claims that cannot be written back off from importClaimRetryMin, doubling
// to importClaimRetryMax, so a broken database is not retried every second.
var (
	importClaimRetryMin = time.Second
	importClaimRetryMax = time.Minute
)

// importDiskReserve is left free on the work volume for recordings: an import
// never starts if it could take the volume below it. Recordings write there
// too, and a full disk breaks a live recording.
var importDiskReserve int64 = 5 << 30

// importDiskFree reports the bytes an unprivileged writer can still use on
// the filesystem holding path. A variable so tests can set the disk.
var importDiskFree = func(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// importDiskHeld is what imports have written to the work volume so far:
// the files under every `.import-*` folder in dir, where fetches land and
// the importer stages. Their space comes back when the imports settle. A file
// linked under two names counts twice, which errs towards waiting.
var importDiskHeld = measureImportDiskHeld

func measureImportDiskHeld(dir string) (int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	var held int64
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".import-") {
			continue
		}
		err := filepath.WalkDir(filepath.Join(dir, e.Name()), func(_ string, d os.DirEntry, err error) error {
			if errors.Is(err, os.ErrNotExist) {
				return nil // an import settled while it was being counted
			}
			if err != nil {
				return err
			}
			if d.Type().IsRegular() {
				info, err := d.Info()
				if errors.Is(err, os.ErrNotExist) {
					return nil
				}
				if err != nil {
					return err
				}
				held += info.Size()
			}
			return nil
		})
		if err != nil {
			return 0, err
		}
	}
	return held, nil
}

// importDiskNeed is what one import may write to the work volume at its peak:
// the staged file it downloads plus the copy the validating `cassini publish`
// writes, which the importer deletes as soon as it has read the catalog.
// Delivery adds nothing: the file is hard-linked under its new name, and the
// check that Nextcloud holds what was uploaded hashes the read-back as it
// arrives (davHashFile). A size it was not told is taken as the largest
// allowed.
func importDiskNeed(job ImportJob) int64 {
	size := job.ExpectedBytes
	if size <= 0 || size > maxImportUploadBytes {
		size = maxImportUploadBytes
	}
	return 2 * size
}

// importDiskVerdict is whether an import's disk need fits right now.
type importDiskVerdict int

const (
	importDiskFits importDiskVerdict = iota
	// importDiskWait: it does not fit, but running imports hold space that
	// comes back when they settle.
	importDiskWait
	// importDiskShort: it does not fit even with nothing else importing.
	importDiskShort
)

// reserveImportDisk promises the job its disk need if the free space covers
// it on top of what running imports were promised and importDiskReserve. The
// promise is conservative: a running import's bytes already on disk count
// twice, once as used and once as promised. If the free space cannot be read
// the import goes ahead, logged, rather than stopping every import on a
// failed check.
func (s *importService) reserveImportDisk(job ImportJob) (release func(), verdict importDiskVerdict, detail string) {
	need := importDiskNeed(job)
	scratch := rtWorkScratchDir(s.rt.cfg.WorkRoot)
	free, err := importDiskFree(scratch)
	held, heldErr := importDiskHeld(scratch)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.logf("import %s: free disk space unknown (%v); importing anyway", job.ID, err)
	} else {
		var promised, ahead int64
		for _, n := range s.diskReserved {
			promised += n
		}
		// Room is left for every import ahead in the line: all of it for one
		// not in the line yet, which would join at the back.
		own, inLine := s.diskWaiting[job.ID]
		for id, w := range s.diskWaiting {
			if id != job.ID && (!inLine || w.seq < own.seq) {
				ahead += w.need
			}
		}
		if free-promised-ahead-importDiskReserve < need {
			detail = fmt.Sprintf("it needs %s, and %s is free, of which %s is kept for recordings", formatGiB(need), formatGiB(free), formatGiB(importDiskReserve))
			if promised > 0 || ahead > 0 {
				detail += fmt.Sprintf(", %s is in use by other imports and %s is held for imports ahead of it", formatGiB(promised), formatGiB(ahead))
				// What running imports have written comes back when they
				// settle. If even that would not make it fit, it waits outside
				// the line, so no other import has to leave room for it; it
				// fails once nothing else runs, when the count cannot shift
				// under it. The estimate only decides where it waits, never
				// that it fails: free space and the files are read a moment
				// apart, and a filesystem may return space after a delete.
				if heldErr != nil {
					s.logf("import %s: disk held by imports unknown (%v)", job.ID, heldErr)
					held = promised
				}
				if free+held-importDiskReserve < need {
					delete(s.diskWaiting, job.ID)
					return nil, importDiskWait, detail + "; it would not fit even once they finish, so it waits outside the line"
				}
				if !inLine {
					if s.diskWaiting == nil {
						s.diskWaiting = map[string]importDiskWaiter{}
					}
					s.diskWaitSeq++
					s.diskWaiting[job.ID] = importDiskWaiter{need: need, seq: s.diskWaitSeq}
				}
				return nil, importDiskWait, detail
			}
			delete(s.diskWaiting, job.ID)
			return nil, importDiskShort, detail
		}
	}
	delete(s.diskWaiting, job.ID)
	if s.diskReserved == nil {
		s.diskReserved = map[string]int64{}
	}
	s.diskReserved[job.ID] = need
	return func() {
		s.mu.Lock()
		delete(s.diskReserved, job.ID)
		s.mu.Unlock()
	}, importDiskFits, ""
}

// importDiskShortMessage is what the person importing reads when the server
// cannot take their file. How much space the server has is not theirs to see.
const importDiskShortMessage = "This file is too big to import right now. Please contact your administrator."

// importDiskWaiter is one import's place in the line for disk space.
type importDiskWaiter struct {
	need int64
	seq  uint64
}

// formatGiB renders a byte count for a person: "1.5 GiB".
func formatGiB(n int64) string {
	return fmt.Sprintf("%.1f GiB", float64(n)/float64(1<<30))
}

// importRunOutcome is what runImportJob did with a job.
type importRunOutcome int

const (
	// importNotStarted: the row was no longer queued (canceled, or settled by
	// another path) or could not be read. Nothing to retry here.
	importNotStarted importRunOutcome = iota
	// importClaimFailed: the row is still queued but the claim could not be
	// written, so the import did not run. Retry later.
	importClaimFailed
	// importDeferred: the row is still queued because the disk cannot take
	// it until running imports settle. Retry later.
	importDeferred
	// importSettled: the job ran, or its wait for a slot ended, and its row
	// says so (or the operator is shutting down).
	importSettled
)

// isRunning reports whether a worker is on this job right now.
func (s *importService) isRunning(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.running[id]
	return ok
}

// runningIDs is a snapshot of the jobs workers are on.
func (s *importService) runningIDs() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make(map[string]bool, len(s.running))
	for id := range s.running {
		ids[id] = true
	}
	return ids
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
	slots := maxConcurrentImports(os.Getenv, logger)
	service := &importService{
		rt:          rt,
		exapp:       exapp,
		logger:      logger,
		importer:    newMeetingImporter(rt.cfg, exapp, rt, logger),
		client:      &http.Client{Transport: importTransport(ncFilesProxyHeadersTTL, false)},
		importSlots: make(chan struct{}, slots),
	}
	if logger != nil {
		logger.Printf("imports: up to %d run at once", slots)
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
	setImportQueuePositions(jobs, s.runningIDs())
	writeJSON(w, http.StatusOK, map[string]any{"imports": jobs})
}

// importCreateRequest serves two calls. With prepare, it REGISTERS uploads: one
// row per file, with the source name, the expected size and the sharing the
// importer chose — that choice is final from here on. Without prepare, it
// SUBMITS registered uploads whose bytes have landed: a file then names only
// its staged path, and any sharing in the body is refused rather than silently
// ignored, so no client can believe submit decides it.
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
		if !request.Prepare && (file.Share || len(file.Audience) > 0 || file.ExpectedBytes != 0 || strings.TrimSpace(file.SourceName) != "") {
			writeJSONError(w, http.StatusBadRequest, "sharing is decided when the upload is registered; a submit names only staged paths")
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
				sharing.Audience = append(sharing.Audience, importPrincipal{Type: kind, ID: id, Label: strings.TrimSpace(principal.Label)})
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
		// A submit queues what prepare registered, with the sharing that was
		// chosen then. Nothing is created here: a path without a registered
		// upload is a client that skipped prepare, not a file to import.
		existing, err := s.rt.store.ImportJobsFor(r.Context(), caller)
		if err != nil {
			writeJSONError(w, 500, err.Error())
			return
		}
		for i := range jobs {
			found := false
			for _, old := range existing {
				if old.StagedPath != jobs[i].StagedPath {
					continue
				}
				found = true
				switch old.State {
				case importStateStaged:
					if err := s.rt.store.RetryImportJob(r.Context(), old.ID); err != nil {
						writeJSONError(w, 500, err.Error())
						return
					}
				case importStateUploading, importStateUploadFailed:
					writeJSONError(w, 409, "upload is not complete")
					return
				}
				// Any other state: already queued, running or settled — the
				// submit is a repeat, answered with the row as it is.
				jobs[i] = old
				break
			}
			if !found {
				writeJSONError(w, 409, fmt.Sprintf("no registered upload at %s; add the file again", jobs[i].StagedPath))
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
	if cancelRun := s.running[job.ID]; cancelRun != nil {
		cancelRun()
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
	if s.isRunning(job.ID) {
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
			s.dispatch()
			select {
			case <-rt.ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
}

// dispatch gives every caller with queued imports a worker, unless they
// already have one. It runs on the dispatcher goroutine, which holds a
// workerWG count of its own, so adding to the group here is safe.
func (s *importService) dispatch() {
	rt := s.rt
	creators, err := rt.store.queuedImportCreators(rt.ctx)
	if err != nil {
		if rt.ctx.Err() == nil {
			s.logf("import dispatcher: %v", err)
		}
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.userWorkers == nil {
		s.userWorkers = map[string]bool{}
	}
	for _, user := range creators {
		if s.userWorkers[user] {
			continue
		}
		s.userWorkers[user] = true
		rt.workerWG.Add(1)
		go s.drainUser(user)
	}
}

// drainUser runs one caller's queued imports, oldest first by when they were
// first queued (a retried import keeps its place), until none are left. A job
// queued after it gives up is picked up by the next dispatch.
func (s *importService) drainUser(user string) {
	rt := s.rt
	defer rt.workerWG.Done()
	defer func() {
		s.mu.Lock()
		delete(s.userWorkers, user)
		s.mu.Unlock()
	}()
	// A job this worker left waiting for disk space stops holding space back
	// from others once the worker moves on from it, whatever the reason.
	var waitingID string
	defer func() { s.stopDiskWait(waitingID) }()
	backoff := importClaimRetryMin
	// wait keeps this worker, so the dispatcher does not start another one a
	// second later, and backs off before the next attempt.
	wait := func() bool {
		select {
		case <-rt.ctx.Done():
			return false
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, importClaimRetryMax)
		return true
	}
	for rt.ctx.Err() == nil {
		job, ok, err := rt.store.nextQueuedImportFor(rt.ctx, user)
		if err != nil {
			if rt.ctx.Err() != nil {
				return
			}
			s.logf("import worker for %s: %v; trying again in %s", user, err, backoff)
			if !wait() {
				return
			}
			continue
		}
		if !ok {
			return
		}
		if waitingID != job.ID {
			s.stopDiskWait(waitingID)
			waitingID = ""
		}
		outcome := s.runImportJob(job)
		if outcome == importDeferred {
			waitingID = job.ID
		} else {
			s.stopDiskWait(job.ID)
			waitingID = ""
		}
		switch outcome {
		case importNotStarted:
			// The job moved on before this worker claimed it (a cancel), or
			// could not be read: the next dispatch looks again.
			return
		case importClaimFailed, importDeferred:
			s.logf("import %s: trying again in %s", job.ID, backoff)
			if !wait() {
				return
			}
		default:
			backoff = importClaimRetryMin
		}
	}
}

// stopDiskWait drops a job's hold on disk space it was waiting for.
func (s *importService) stopDiskWait(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	delete(s.diskWaiting, id)
	s.mu.Unlock()
}

// runImportJob takes one queued job through a slot to a settled state, and
// reports what became of it.
func (s *importService) runImportJob(job ImportJob) importRunOutcome {
	rt := s.rt
	ctx, cancel := context.WithCancel(rt.ctx)
	defer cancel()
	// Read and register under the lock. cancel() writes the row first and then
	// takes the lock to find the run, so a cancel either lands before this read
	// (and the job is not started) or finds the run registered here (and stops
	// it). The read is one indexed row; holding the lock across it is the price
	// of that ordering.
	s.mu.Lock()
	current, ok, err := rt.store.ImportJob(ctx, job.ID)
	if err != nil || !ok || current.State != importStateQueued {
		s.mu.Unlock()
		if err != nil {
			s.logf("import %s: read before start: %v", job.ID, err)
		}
		return importNotStarted
	}
	if s.running == nil {
		s.running = map[string]context.CancelFunc{}
	}
	s.running[job.ID] = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.running, job.ID)
		s.mu.Unlock()
	}()

	// Wait for one of the shared slots. The row stays queued meanwhile, so
	// the sheet keeps saying the import is waiting.
	release, err := s.acquireImportSlot(ctx)
	if err != nil {
		s.settleInterrupted(job)
		return importSettled
	}
	defer release()
	// Promise the import its disk space before anything is written. Short of
	// space while other imports run, it gives back its slot and waits for
	// them; short with nothing else running, waiting would not help.
	releaseDisk, verdict, detail := s.reserveImportDisk(job)
	switch verdict {
	case importDiskWait:
		s.logf("import %s: waiting for disk space: %s", job.ID, detail)
		return importDeferred
	case importDiskShort:
		// The sizes are for the administrator's log; the person importing
		// learns only what they can act on.
		s.logf("import %s (%s): not enough free disk space: %s", job.ID, job.SourceName, detail)
		s.saveState(job.ID, importStateFailed, "", importDiskShortMessage)
		return importSettled
	}
	defer releaseDisk()
	// Claim: move the row off queued. If that cannot be written, do not run
	// the import: the row still says queued, and nothing would say otherwise.
	if err := s.setState(job.ID, importStateFetching, "fetching", ""); err != nil {
		s.logf("import %s: claim: %v", job.ID, err)
		return importClaimFailed
	}

	perform := s.perform
	if perform == nil {
		perform = s.performImport
	}
	limit := importTimeLimit(job)
	errTooLong := fmt.Errorf("the import did not finish within %s", limit.Round(time.Minute))
	runCtx, cancelRun := context.WithTimeoutCause(ctx, limit, errTooLong)
	defer cancelRun()
	result, err := perform(runCtx, job)
	// A run that returned a result has put the meeting in Nextcloud: a
	// cancel or shutdown that arrived meanwhile came too late to stop it, so
	// the import is recorded as done rather than canceled or queued again.
	if err != nil && ctx.Err() != nil {
		s.settleInterrupted(job)
		return importSettled
	}
	if err != nil && runCtx.Err() != nil && errors.Is(context.Cause(runCtx), errTooLong) {
		// Its own time limit, not a cancel: a verdict the person can retry.
		// Only a failure is reworded: an import whose last, best-effort step
		// ran out of time still landed, and is recorded as done.
		err = errTooLong
	}
	if err != nil {
		s.logf("import %s (%s): %v", job.ID, job.SourceName, err)
		s.saveState(job.ID, importStateFailed, "", err.Error())
		return importSettled
	}
	if err := rt.store.SetImportJobProvenance(context.Background(), job.ID, result.Format, result.ContentID, result.SourceRoomID, result.CreatedAtUTC, result.RecordedAtLocal); err != nil {
		s.logf("import %s: persist provenance: %v", job.ID, err)
	}
	if err := rt.store.SetImportJobOutcome(context.Background(), job.ID, result.DeliveredName, result.FileID, result.ImportedAtUTC); err != nil {
		s.logf("import %s: persist outcome: %v", job.ID, err)
		return importSettled
	}
	s.logf("import succeeded id=%s source=%q delivered=%s file_id=%d shared_with=%d not_shared_with=%d searchable=%t",
		job.ID, job.SourceName, result.DeliveredName, result.FileID, result.SharedWith, len(result.NotSharedWith), result.SearchIndexed)
	// The staged copy is spent. Best effort: a leftover costs the caller some
	// quota and is theirs to see; it never fails a landed import.
	if err := s.exapp.davDeleteUserFile(context.Background(), s.client, job.CreatedBy, job.StagedPath); err != nil {
		s.logf("import %s: staged copy not removed (%v)", job.ID, err)
	} else if err := rt.store.MarkImportStagingRemoved(context.Background(), job.ID); err != nil {
		s.logf("import %s: record staged copy removed: %v", job.ID, err)
	}
	return importSettled
}

// settleInterrupted records a run that stopped because its context ended.
// Operator shutdown is not a verdict: the row goes back to queued for the
// recovery requeue. A user cancel has already written the row, and the guard
// inside SetImportJobState keeps any late write from resurrecting it.
func (s *importService) settleInterrupted(job ImportJob) {
	if s.rt.ctx.Err() != nil {
		s.saveState(job.ID, importStateQueued, "", "")
	}
}

// acquireImportSlot waits for one of the shared import slots.
func (s *importService) acquireImportSlot(ctx context.Context) (func(), error) {
	if s.importSlots == nil {
		return func() {}, nil
	}
	select {
	case s.importSlots <- struct{}{}:
		return func() { <-s.importSlots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// fetchStaged downloads the caller's staged file to local, abandoning the
// download when less than importFetchMinProgress arrives within
// importFetchIdleTimeout.
func (s *importService) fetchStaged(ctx context.Context, job ImportJob, local string) error {
	idleTimeout, minProgress, poll := importFetchIdleTimeout, importFetchMinProgress, importFetchIdlePoll
	fetchCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	errStalled := fmt.Errorf("the download from your Files stalled: less than %d KiB arrived in %s", minProgress>>10, idleTimeout)
	done := make(chan struct{})
	watched := make(chan struct{})
	// Stop the watchdog and wait for it, so it never outlives the download.
	defer func() {
		close(done)
		<-watched
	}()
	go func() {
		defer close(watched)
		// The file appears once headers arrive (those have their own timeout)
		// and grows as the body streams in. Progress counts only once the file
		// has grown by minProgress since the window began.
		var windowSize int64
		windowStart := time.Now()
		tick := time.NewTicker(poll)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-fetchCtx.Done():
				return
			case <-tick.C:
			}
			var size int64
			if info, err := os.Stat(local); err == nil {
				size = info.Size()
			}
			if size-windowSize >= minProgress {
				windowSize, windowStart = size, time.Now()
				continue
			}
			if time.Since(windowStart) >= idleTimeout {
				cancel(errStalled)
				return
			}
		}
	}()
	// The disk promise and the time limit were sized for the declared size,
	// so a staged file replaced by a larger one after it was confirmed is
	// refused rather than downloaded.
	limit := maxImportUploadBytes
	if job.ExpectedBytes > 0 && job.ExpectedBytes < limit {
		limit = job.ExpectedBytes
	}
	_, written, status, err := s.exapp.davDownloadFile(fetchCtx, s.client, job.CreatedBy, job.StagedPath, local, limit)
	if err == nil {
		return nil
	}
	if written > limit {
		return fmt.Errorf("the staged file %s changed after it was uploaded; upload it again", job.StagedPath)
	}
	if errors.Is(context.Cause(fetchCtx), errStalled) && ctx.Err() == nil {
		return errStalled
	}
	if status == http.StatusNotFound {
		return fmt.Errorf("the staged file %s is no longer in your Files", job.StagedPath)
	}
	return fmt.Errorf("fetch the staged file: %w", err)
}

func (s *importService) performImport(ctx context.Context, job ImportJob) (meetingImportResult, error) {
	if job.isCLI() {
		return meetingImportResult{}, fmt.Errorf("%s", importCLIRecovery)
	}
	staging, err := os.MkdirTemp(rtWorkScratchDir(s.rt.cfg.WorkRoot), ".import-fetch-")
	if err != nil {
		return meetingImportResult{}, err
	}
	defer func() {
		if err := os.RemoveAll(staging); err != nil {
			s.logf("import %s: remove scratch: %v", job.ID, err)
		}
	}()
	local := filepath.Join(staging, "staged")
	if err := s.fetchStaged(ctx, job, local); err != nil {
		return meetingImportResult{}, err
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
		s.saveState(job.ID, state, phase, "")
	})
}

func (s *importService) setState(id, state, phase, errText string) error {
	return s.rt.store.SetImportJobState(context.Background(), id, state, phase, errText)
}

// saveState is setState where the caller cannot act on a failure: it is
// logged, never dropped silently. A stale state is corrected by the next
// write or by the startup requeue.
func (s *importService) saveState(id, state, phase, errText string) {
	if err := s.setState(id, state, phase, errText); err != nil {
		s.logf("import %s: save state %s: %v", id, state, err)
	}
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
