package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	_ "modernc.org/sqlite"
)

const (
	defaultBind              = "127.0.0.1:8080"
	defaultRecordWorkerCount = 1
	nextcloudTalkProvider    = "nextcloud-talk"
)

type Config struct {
	RepoRoot         string
	BindAddr         string
	DBPath           string
	WorkRoot         string
	SiteRoot         string
	CassiniBin       string
	MaxRecordWorkers int
	MaxBuildWorkers  int
}

type Runtime struct {
	ctx          context.Context
	store        *Store
	cfg          Config
	logger       *log.Logger
	stdout       io.Writer
	stderr       io.Writer
	recordSlots  chan struct{}
	buildQueue   chan buildTask
	publishQueue chan publishTask
	recordMu     sync.Mutex
	recordJobs   map[string]*recordProcessState
	recordJobFn  func(context.Context, Job, TriggerRequest) (recordResult, error)
	buildJobFn   func(context.Context, buildTask) (string, error)
	publishJobFn func(context.Context, publishTask) (string, error)
}

type TriggerRequest struct {
	Platform              string  `json:"platform"`
	URL                   string  `json:"url"`
	GuestName             string  `json:"guestName"`
	DurationSeconds       *int    `json:"duration,omitempty"`
	StopWhenRoomEmpty     bool    `json:"stopWhenRoomEmpty"`
	RoomEmptyGraceSeconds float64 `json:"roomEmptyGrace"`
	StopWhenRoomEmptySet  bool    `json:"-"`
	RoomEmptyGraceSet     bool    `json:"-"`
}

type createJobResponse struct {
	ID string `json:"id"`
}

type jobDetailResponse struct {
	Job      Job          `json:"job"`
	Attempts []JobAttempt `json:"attempts"`
}

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	logger := log.New(stderr, "cassini-operator: ", log.LstdFlags)

	cfg, exitCode, err := loadConfig(args, stderr)
	if err != nil {
		if exitCode == 0 {
			return 0
		}
		fmt.Fprintf(stderr, "%v\n", err)
		return exitCode
	}

	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(stderr, "open store: %v\n", err)
		return 1
	}
	defer store.Close()
	if interrupted, err := store.MarkIncompleteJobsInterrupted(context.Background(), nowUTCString()); err != nil {
		fmt.Fprintf(stderr, "mark interrupted jobs: %v\n", err)
		return 1
	} else if interrupted > 0 {
		logger.Printf("startup interrupted_jobs -> %d", interrupted)
	}

	runtime := NewRuntime(ctx, store, cfg, logger, stdout, stderr)

	mux := http.NewServeMux()
	mux.HandleFunc("/jobs", runtime.jobsHandler)
	mux.HandleFunc("/jobs/", runtime.jobDetailHandler)

	server := &http.Server{
		Handler:           requestLogger(logger, corsMiddleware(mux)),
		ReadHeaderTimeout: 10 * time.Second,
	}

	listener, err := net.Listen("tcp", cfg.BindAddr)
	if err != nil {
		fmt.Fprintf(stderr, "listen %s: %v\n", cfg.BindAddr, err)
		return 1
	}
	defer listener.Close()

	fmt.Fprintf(stdout, "listening -> http://%s\n", listener.Addr().String())
	logger.Printf("db -> %s", cfg.DBPath)
	logger.Printf("work_root -> %s", cfg.WorkRoot)
	logger.Printf("site_root -> %s", cfg.SiteRoot)
	logger.Printf("cassini_bin -> %s", cfg.CassiniBin)
	logger.Printf("max_record_workers -> %d", cfg.MaxRecordWorkers)
	logger.Printf("max_build_workers -> %d", cfg.MaxBuildWorkers)

	serveErrCh := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErrCh <- err
			return
		}
		serveErrCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(stderr, "shutdown server: %v\n", err)
			return 1
		}
		if err := <-serveErrCh; err != nil {
			fmt.Fprintf(stderr, "serve: %v\n", err)
			return 1
		}
		return 0
	case err := <-serveErrCh:
		if err != nil {
			fmt.Fprintf(stderr, "serve: %v\n", err)
			return 1
		}
		return 0
	}
}

func loadConfig(args []string, stderr io.Writer) (Config, int, error) {
	repoRoot, err := findRepoRoot()
	if err != nil {
		return Config{}, 1, fmt.Errorf("resolve repo root: %w", err)
	}

	defaultDataRoot := filepath.Join(repoRoot, "cassini-operator", "runtime")
	defaultDBPath := filepath.Join(defaultDataRoot, "jobs.sqlite3")
	defaultWorkRoot := filepath.Join(defaultDataRoot, "jobs")
	defaultSiteRoot := filepath.Join(defaultDataRoot, "site")
	defaultMaxRecordWorkers, err := parsePositiveIntEnv("MAX_RECORD_WORKERS", defaultRecordWorkerCount)
	if err != nil {
		return Config{}, 2, err
	}
	defaultMaxBuildWorkers, err := parsePositiveIntEnv("MAX_BUILD_WORKERS", defaultBuildWorkerCount)
	if err != nil {
		return Config{}, 2, err
	}
	defaultCassiniBin := envOrDefault("CASSINI_BIN", filepath.Join(repoRoot, "bin", "cassini"))

	fs := flag.NewFlagSet("cassini-operator", flag.ContinueOnError)
	fs.SetOutput(stderr)

	cfg := Config{RepoRoot: repoRoot}
	fs.StringVar(&cfg.BindAddr, "bind", defaultBind, "HTTP bind address")
	fs.StringVar(&cfg.DBPath, "db", defaultDBPath, "SQLite database path")
	fs.StringVar(&cfg.WorkRoot, "work-root", envOrDefault("WORK_ROOT", defaultWorkRoot), "per-job artifact root")
	fs.StringVar(&cfg.SiteRoot, "site-root", envOrDefault("SITE_ROOT", defaultSiteRoot), "published site output root")
	fs.StringVar(&cfg.CassiniBin, "cassini-bin", defaultCassiniBin, "Cassini CLI binary path")
	fs.IntVar(&cfg.MaxRecordWorkers, "max-record-workers", defaultMaxRecordWorkers, "maximum concurrent record workers")
	fs.IntVar(&cfg.MaxBuildWorkers, "max-build-workers", defaultMaxBuildWorkers, "maximum concurrent build workers")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Cassini Operator runs the V2 live-record job API and worker runtime.

Usage:
  cassini-operator
  cassini-operator --bind 127.0.0.1:8080
  cassini-operator --db ./cassini-operator/runtime/jobs.sqlite3

Flags:
`)
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return Config{}, 0, err
		}
		return Config{}, 2, err
	}
	if fs.NArg() != 0 {
		return Config{}, 2, fmt.Errorf("unexpected positional arguments: %v", fs.Args())
	}

	cfg.DBPath = resolveRepoRelativePath(repoRoot, cfg.DBPath)
	cfg.WorkRoot = resolveRepoRelativePath(repoRoot, cfg.WorkRoot)
	cfg.SiteRoot = resolveRepoRelativePath(repoRoot, cfg.SiteRoot)
	cfg.CassiniBin = resolveRepoRelativePath(repoRoot, cfg.CassiniBin)
	if cfg.MaxRecordWorkers < 1 {
		return Config{}, 2, errors.New("--max-record-workers must be >= 1")
	}
	if cfg.MaxBuildWorkers < 1 {
		return Config{}, 2, errors.New("--max-build-workers must be >= 1")
	}
	if err := validateExecutable(cfg.CassiniBin); err != nil {
		return Config{}, 2, fmt.Errorf("cassini binary: %w", err)
	}

	return cfg, 0, nil
}

func parsePositiveIntEnv(name string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	if n < 1 {
		return 0, fmt.Errorf("%s must be >= 1", name)
	}
	return n, nil
}

func envOrDefault(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func resolveRepoRelativePath(repoRoot, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(repoRoot, path)
}

func NewRuntime(ctx context.Context, store *Store, cfg Config, logger *log.Logger, stdout, stderr io.Writer) *Runtime {
	queueCapacity := cfg.MaxBuildWorkers * 16
	if queueCapacity < 16 {
		queueCapacity = 16
	}
	rt := &Runtime{
		ctx:          ctx,
		store:        store,
		cfg:          cfg,
		logger:       logger,
		stdout:       stdout,
		stderr:       stderr,
		recordSlots:  make(chan struct{}, cfg.MaxRecordWorkers),
		buildQueue:   make(chan buildTask, queueCapacity),
		publishQueue: make(chan publishTask, 16),
		recordJobs:   map[string]*recordProcessState{},
	}
	rt.recordJobFn = rt.executeRecordCLI
	rt.buildJobFn = rt.executeBuildCLI
	rt.publishJobFn = rt.executePublishCLI
	rt.startBuildWorkers()
	rt.startPublishWorker()
	return rt
}

func (rt *Runtime) jobsHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/jobs" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		jobs, err := rt.store.ListJobs(r.Context())
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("list jobs: %v", err))
			return
		}
		writeJSON(w, http.StatusOK, jobs)
	case http.MethodPost:
		rt.handleCreateJob(w, r)
	default:
		writeMethodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
	}
}

func (rt *Runtime) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	provider := strings.TrimSpace(r.URL.Query().Get("provider"))
	if provider != nextcloudTalkProvider {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("unknown provider %q", provider))
		return
	}

	requestBody, req, err := decodeTriggerRequest(r.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Platform != nextcloudTalkProvider {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("platform must be %q", nextcloudTalkProvider))
		return
	}

	select {
	case rt.recordSlots <- struct{}{}:
	default:
		rt.logger.Printf("busy provider=%s url=%s", provider, req.URL)
		writeJSONError(w, http.StatusServiceUnavailable, "max record workers exceeded")
		return
	}

	jobID := ulid.Make().String()
	now := nowUTCString()
	job := Job{
		ID:                   jobID,
		Provider:             provider,
		RequestJSON:          requestBody,
		Stage:                "record",
		State:                "queued",
		CurrentAttemptNumber: 1,
		RerunCount:           0,
		CreatedAt:            now,
		UpdatedAt:            now,
		RecordQueuedAt:       &now,
	}
	if err := rt.store.InsertQueuedJob(r.Context(), job); err != nil {
		<-rt.recordSlots
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("create job: %v", err))
		return
	}

	rt.logger.Printf("accepted id=%s provider=%s url=%s", jobID, provider, req.URL)
	go rt.runRecordJob(job, req)
	writeJSON(w, http.StatusAccepted, createJobResponse{ID: jobID})
}

func decodeTriggerRequest(body io.ReadCloser) (string, TriggerRequest, error) {
	defer body.Close()
	raw, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return "", TriggerRequest{}, fmt.Errorf("read request body: %w", err)
	}
	if strings.TrimSpace(string(raw)) == "" {
		return "", TriggerRequest{}, errors.New("request body is required")
	}
	var input triggerRequestInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return "", TriggerRequest{}, fmt.Errorf("invalid request JSON: %w", err)
	}
	req, err := parseTriggerRequest(input)
	if err != nil {
		return "", TriggerRequest{}, err
	}
	requestBody, err := encodeTriggerRequest(req)
	if err != nil {
		return "", TriggerRequest{}, err
	}
	return requestBody, req, nil
}

func (rt *Runtime) runRecordJob(job Job, req TriggerRequest) {
	defer func() { <-rt.recordSlots }()

	startedAt := nowUTCString()
	if err := rt.store.MarkRecordRunning(context.Background(), job.ID, startedAt); err != nil {
		rt.logger.Printf("record start update failed id=%s: %v", job.ID, err)
		return
	}
	rt.logger.Printf("record started id=%s", job.ID)

	result, err := rt.recordJobFn(rt.ctx, job, req)
	finishedAt := nowUTCString()
	if err != nil {
		rt.logger.Printf("record failed id=%s: %v", job.ID, err)
		if updateErr := rt.store.MarkRecordFailed(context.Background(), job.ID, err.Error(), result, finishedAt); updateErr != nil {
			rt.logger.Printf("record fail update failed id=%s: %v", job.ID, updateErr)
		}
		return
	}
	if updateErr := rt.store.UpdateRecordOutcome(context.Background(), job.ID, result, finishedAt); updateErr != nil {
		rt.logger.Printf("record outcome update failed id=%s: %v", job.ID, updateErr)
		if failErr := rt.store.MarkRecordFailed(context.Background(), job.ID, updateErr.Error(), result, finishedAt); failErr != nil {
			rt.logger.Printf("record outcome failure update failed id=%s: %v", job.ID, failErr)
		}
		return
	}
	if err := rt.enqueueBuildJob(job.ID, job.CurrentAttemptNumber, result.ArtifactRunPath, finishedAt); err != nil {
		rt.logger.Printf("build queue update failed id=%s: %v", job.ID, err)
		if updateErr := rt.store.MarkBuildFailed(context.Background(), job.ID, "", err.Error(), finishedAt); updateErr != nil {
			rt.logger.Printf("build queue failure update failed id=%s: %v", job.ID, updateErr)
		}
		return
	}
	rt.logger.Printf("record succeeded id=%s run=%s build_queued_at=%s stop_reason=%s", job.ID, result.ArtifactRunPath, finishedAt, strings.TrimSpace(result.StopReason))
}

func findRepoRoot() (string, error) {
	if repoRoot := strings.TrimSpace(os.Getenv("CASSINI_REPO_ROOT")); repoRoot != "" {
		if looksLikeRepoRoot(repoRoot) {
			return repoRoot, nil
		}
		return "", fmt.Errorf("CASSINI_REPO_ROOT=%q is not a Cassini repo root", repoRoot)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}

	for dir := cwd; dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if looksLikeRepoRoot(dir) {
			return dir, nil
		}
	}
	if looksLikeRepoRoot(cwd) {
		return cwd, nil
	}
	return "", errors.New("could not locate repo root; set CASSINI_REPO_ROOT")
}

func looksLikeRepoRoot(dir string) bool {
	if dir == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "cassini-go-recorder", "go.mod")); err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "bin", "cassini")); err != nil {
		return false
	}
	return true
}

type Store struct {
	db *sql.DB
}

func OpenStore(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("db path must not be empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir db dir: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("sql open: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}

	store := &Store{db: db}
	if err := store.ensureSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

type Job struct {
	ID                   string  `json:"id"`
	Provider             string  `json:"provider"`
	RequestJSON          string  `json:"request_json"`
	Stage                string  `json:"stage"`
	State                string  `json:"state"`
	CurrentAttemptNumber int     `json:"current_attempt_number"`
	RerunCount           int     `json:"rerun_count"`
	ArtifactRunPath      *string `json:"artifact_run_path"`
	ArtifactMeetingPath  *string `json:"artifact_meeting_path"`
	ArtifactSitePath     *string `json:"artifact_site_path"`
	Error                *string `json:"error"`
	StopReason           *string `json:"stop_reason"`
	StopRequestedAt      *string `json:"stop_requested_at"`
	StopSignalSentAt     *string `json:"stop_signal_sent_at"`
	RecordExitCode       *int    `json:"record_exit_code"`
	RecordStopDetail     *string `json:"record_stop_detail"`
	CreatedAt            string  `json:"created_at"`
	UpdatedAt            string  `json:"updated_at"`
	RecordQueuedAt       *string `json:"record_queued_at"`
	RecordStartedAt      *string `json:"record_started_at"`
	RecordFinishedAt     *string `json:"record_finished_at"`
	BuildQueuedAt        *string `json:"build_queued_at"`
	BuildStartedAt       *string `json:"build_started_at"`
	BuildFinishedAt      *string `json:"build_finished_at"`
	PublishQueuedAt      *string `json:"publish_queued_at"`
	PublishStartedAt     *string `json:"publish_started_at"`
	PublishFinishedAt    *string `json:"publish_finished_at"`
	InterruptedAt        *string `json:"interrupted_at"`
	CompletedAt          *string `json:"completed_at"`
}

func (s *Store) InsertQueuedJob(ctx context.Context, job Job) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin insert job: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
INSERT INTO jobs (
  id, provider, request_json, stage, state,
  current_attempt_number, rerun_count,
  created_at, updated_at, record_queued_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID,
		job.Provider,
		job.RequestJSON,
		job.Stage,
		job.State,
		job.CurrentAttemptNumber,
		job.RerunCount,
		job.CreatedAt,
		job.UpdatedAt,
		stringOrNil(job.RecordQueuedAt),
	)
	if err != nil {
		return fmt.Errorf("insert job: %w", err)
	}
	if err := insertInitialAttemptTx(tx, job); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit insert job: %w", err)
	}
	return nil
}

func (s *Store) MarkRecordRunning(ctx context.Context, id, startedAt string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin record running update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, updated_at = ?, record_started_at = ?
WHERE id = ?`, "record", "running", startedAt, startedAt, id)
	if err != nil {
		return fmt.Errorf("update record running: %w", err)
	}
	attemptNumber, err := currentAttemptNumberTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE job_attempts
SET stage = ?, state = ?, updated_at = ?, record_started_at = ?
WHERE job_id = ? AND attempt_number = ?`, "record", "running", startedAt, startedAt, id, attemptNumber); err != nil {
		return fmt.Errorf("update attempt record running: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit record running update: %w", err)
	}
	return nil
}

func (s *Store) MarkRecordStopRequested(ctx context.Context, id, requestedAt, signalSentAt string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin stop request update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
UPDATE jobs
SET stop_requested_at = COALESCE(stop_requested_at, ?),
    stop_signal_sent_at = COALESCE(stop_signal_sent_at, ?),
    updated_at = ?
WHERE id = ?`, requestedAt, signalSentAt, signalSentAt, id)
	if err != nil {
		return fmt.Errorf("update record stop request: %w", err)
	}
	attemptNumber, err := currentAttemptNumberTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE job_attempts
SET stop_requested_at = COALESCE(stop_requested_at, ?),
    stop_signal_sent_at = COALESCE(stop_signal_sent_at, ?),
    updated_at = ?
WHERE job_id = ? AND attempt_number = ?`, requestedAt, signalSentAt, signalSentAt, id, attemptNumber); err != nil {
		return fmt.Errorf("update attempt stop request: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit stop request update: %w", err)
	}
	return nil
}

func (s *Store) UpdateRecordOutcome(ctx context.Context, id string, result recordResult, updatedAt string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin record outcome update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
UPDATE jobs
SET stop_reason = ?,
    record_exit_code = ?,
    record_stop_detail = ?,
    updated_at = ?
WHERE id = ?`, nullableString(result.StopReason), intOrNil(result.ExitCode), nullableString(result.StopDetail), updatedAt, id)
	if err != nil {
		return fmt.Errorf("update record outcome: %w", err)
	}
	attemptNumber, err := currentAttemptNumberTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE job_attempts
SET stop_reason = ?,
    record_exit_code = ?,
    record_stop_detail = ?,
    updated_at = ?
WHERE job_id = ? AND attempt_number = ?`, nullableString(result.StopReason), intOrNil(result.ExitCode), nullableString(result.StopDetail), updatedAt, id, attemptNumber); err != nil {
		return fmt.Errorf("update attempt record outcome: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit record outcome update: %w", err)
	}
	return nil
}

func (s *Store) MarkRecordFailed(ctx context.Context, id, errText string, result recordResult, finishedAt string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin record failure update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, error = ?, stop_reason = ?, record_exit_code = ?, record_stop_detail = ?, updated_at = ?, record_finished_at = ?, completed_at = ?
WHERE id = ?`, "done", "failed", strings.TrimSpace(errText), nullableString(result.StopReason), intOrNil(result.ExitCode), nullableString(firstNonEmpty(result.StopDetail, errText)), finishedAt, finishedAt, finishedAt, id)
	if err != nil {
		return fmt.Errorf("update record failure: %w", err)
	}
	attemptNumber, err := currentAttemptNumberTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE job_attempts
SET stage = ?, state = ?, error = ?, stop_reason = ?, record_exit_code = ?, record_stop_detail = ?, updated_at = ?, record_finished_at = ?, completed_at = ?
WHERE job_id = ? AND attempt_number = ?`, "done", "failed", strings.TrimSpace(errText), nullableString(result.StopReason), intOrNil(result.ExitCode), nullableString(firstNonEmpty(result.StopDetail, errText)), finishedAt, finishedAt, finishedAt, id, attemptNumber); err != nil {
		return fmt.Errorf("update attempt record failure: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit record failure update: %w", err)
	}
	return nil
}

func stringOrNil(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableString(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func intOrNil(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func (s *Store) ListJobs(ctx context.Context) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, provider, request_json, stage, state,
       current_attempt_number, rerun_count,
       artifact_run_path, artifact_meeting_path, artifact_site_path, error,
       stop_reason, stop_requested_at, stop_signal_sent_at, record_exit_code, record_stop_detail,
       created_at, updated_at,
       record_queued_at, record_started_at, record_finished_at,
       build_queued_at, build_started_at, build_finished_at,
       publish_queued_at, publish_started_at, publish_finished_at,
       interrupted_at, completed_at
FROM jobs
ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("query jobs: %w", err)
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate jobs: %w", err)
	}
	if jobs == nil {
		jobs = []Job{}
	}
	return jobs, nil
}

func (s *Store) GetJob(ctx context.Context, id string) (Job, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, provider, request_json, stage, state,
       current_attempt_number, rerun_count,
       artifact_run_path, artifact_meeting_path, artifact_site_path, error,
       stop_reason, stop_requested_at, stop_signal_sent_at, record_exit_code, record_stop_detail,
       created_at, updated_at,
       record_queued_at, record_started_at, record_finished_at,
       build_queued_at, build_started_at, build_finished_at,
       publish_queued_at, publish_started_at, publish_finished_at,
       interrupted_at, completed_at
FROM jobs
WHERE id = ?`, id)
	job, err := scanJob(row)
	if err != nil {
		return Job{}, err
	}
	return job, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanJob(scanner rowScanner) (Job, error) {
	var job Job
	var artifactRunPath sql.NullString
	var artifactMeetingPath sql.NullString
	var artifactSitePath sql.NullString
	var jobError sql.NullString
	var stopReason sql.NullString
	var stopRequestedAt sql.NullString
	var stopSignalSentAt sql.NullString
	var recordExitCode sql.NullInt64
	var recordStopDetail sql.NullString
	var recordQueuedAt sql.NullString
	var recordStartedAt sql.NullString
	var recordFinishedAt sql.NullString
	var buildQueuedAt sql.NullString
	var buildStartedAt sql.NullString
	var buildFinishedAt sql.NullString
	var publishQueuedAt sql.NullString
	var publishStartedAt sql.NullString
	var publishFinishedAt sql.NullString
	var interruptedAt sql.NullString
	var completedAt sql.NullString

	err := scanner.Scan(
		&job.ID,
		&job.Provider,
		&job.RequestJSON,
		&job.Stage,
		&job.State,
		&job.CurrentAttemptNumber,
		&job.RerunCount,
		&artifactRunPath,
		&artifactMeetingPath,
		&artifactSitePath,
		&jobError,
		&stopReason,
		&stopRequestedAt,
		&stopSignalSentAt,
		&recordExitCode,
		&recordStopDetail,
		&job.CreatedAt,
		&job.UpdatedAt,
		&recordQueuedAt,
		&recordStartedAt,
		&recordFinishedAt,
		&buildQueuedAt,
		&buildStartedAt,
		&buildFinishedAt,
		&publishQueuedAt,
		&publishStartedAt,
		&publishFinishedAt,
		&interruptedAt,
		&completedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Job{}, err
		}
		return Job{}, fmt.Errorf("scan job: %w", err)
	}

	job.ArtifactRunPath = nullableStringPtr(artifactRunPath)
	job.ArtifactMeetingPath = nullableStringPtr(artifactMeetingPath)
	job.ArtifactSitePath = nullableStringPtr(artifactSitePath)
	job.Error = nullableStringPtr(jobError)
	job.StopReason = nullableStringPtr(stopReason)
	job.StopRequestedAt = nullableStringPtr(stopRequestedAt)
	job.StopSignalSentAt = nullableStringPtr(stopSignalSentAt)
	job.RecordExitCode = nullableIntPtr(recordExitCode)
	job.RecordStopDetail = nullableStringPtr(recordStopDetail)
	job.RecordQueuedAt = nullableStringPtr(recordQueuedAt)
	job.RecordStartedAt = nullableStringPtr(recordStartedAt)
	job.RecordFinishedAt = nullableStringPtr(recordFinishedAt)
	job.BuildQueuedAt = nullableStringPtr(buildQueuedAt)
	job.BuildStartedAt = nullableStringPtr(buildStartedAt)
	job.BuildFinishedAt = nullableStringPtr(buildFinishedAt)
	job.PublishQueuedAt = nullableStringPtr(publishQueuedAt)
	job.PublishStartedAt = nullableStringPtr(publishStartedAt)
	job.PublishFinishedAt = nullableStringPtr(publishFinishedAt)
	job.InterruptedAt = nullableStringPtr(interruptedAt)
	job.CompletedAt = nullableStringPtr(completedAt)
	return job, nil
}

func nullableStringPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	value := v.String
	return &value
}

func nullableIntPtr(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	value := int(v.Int64)
	return &value
}

func (rt *Runtime) jobDetailHandler(w http.ResponseWriter, r *http.Request) {
	id, action, ok := parseJobPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if action == "stop" {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		rt.handleStopJob(w, r, id)
		return
	}
	if action == "rerun" {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		rt.handleRerunJob(w, r, id)
		return
	}
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	job, err := rt.store.GetJob(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "job not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("get job: %v", err))
		return
	}
	attempts, err := rt.store.ListJobAttempts(r.Context(), id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("list job attempts: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, jobDetailResponse{Job: job, Attempts: attempts})
}

func requestLogger(logger *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.Printf("%s %s", r.Method, r.URL.RequestURI())
		next.ServeHTTP(w, r)
	})
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeMethodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		http.Error(w, fmt.Sprintf("encode json: %v", err), http.StatusInternalServerError)
	}
}

func nowUTCString() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
