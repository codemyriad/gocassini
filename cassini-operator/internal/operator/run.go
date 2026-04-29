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
	FixturePath      string
	FixtureURL       string
	CassiniBin       string
	MaxRecordWorkers int
	MaxBuildWorkers  int
}

type Runtime struct {
	ctx         context.Context
	store       *Store
	cfg         Config
	logger      *log.Logger
	stdout      io.Writer
	stderr      io.Writer
	recordSlots chan struct{}
	buildQueue  chan buildTask
	fixtureMu   sync.Mutex
	recordJobFn func(context.Context, Job, TriggerRequest) (string, error)
	buildJobFn  func(context.Context, buildTask) (string, error)
}

type TriggerRequest struct {
	Platform string `json:"platform"`
	URL      string `json:"url"`
}

type createJobResponse struct {
	ID string `json:"id"`
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

	runtime := NewRuntime(ctx, store, cfg, logger, stdout, stderr)

	mux := http.NewServeMux()
	mux.HandleFunc("/jobs", runtime.jobsHandler)
	mux.HandleFunc("/jobs/", runtime.jobDetailHandler)

	server := &http.Server{
		Handler:           requestLogger(logger, mux),
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
	logger.Printf("fixture_path -> %s", cfg.FixturePath)
	logger.Printf("fixture_url -> %s", cfg.FixtureURL)
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

	defaultDataRoot := filepath.Join(repoRoot, "cassini-operator", ".runtime")
	defaultDBPath := filepath.Join(defaultDataRoot, "jobs.sqlite3")
	defaultWorkRoot := filepath.Join(defaultDataRoot, "jobs")
	defaultFixturePath := filepath.Join(defaultDataRoot, "operator-fixture.mkv")
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
	fs.StringVar(&cfg.WorkRoot, "work-root", defaultWorkRoot, "per-job artifact root")
	fs.StringVar(&cfg.FixturePath, "fixture-path", envOrDefault("FIXTURE_PATH", defaultFixturePath), "fixture MKV path")
	fs.StringVar(&cfg.FixtureURL, "fixture-url", strings.TrimSpace(os.Getenv("FIXTURE_URL")), "fixture download URL (used when fixture path is missing)")
	fs.StringVar(&cfg.CassiniBin, "cassini-bin", defaultCassiniBin, "Cassini CLI binary path")
	fs.IntVar(&cfg.MaxRecordWorkers, "max-record-workers", defaultMaxRecordWorkers, "maximum concurrent record workers")
	fs.IntVar(&cfg.MaxBuildWorkers, "max-build-workers", defaultMaxBuildWorkers, "maximum concurrent build workers")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Cassini Operator runs the V1 job API and worker runtime.

Usage:
  cassini-operator
  cassini-operator --bind 127.0.0.1:8080
  cassini-operator --db ./cassini-operator/.runtime/jobs.sqlite3

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
	cfg.FixturePath = resolveRepoRelativePath(repoRoot, cfg.FixturePath)
	cfg.CassiniBin = resolveRepoRelativePath(repoRoot, cfg.CassiniBin)
	cfg.FixtureURL = strings.TrimSpace(cfg.FixtureURL)
	if cfg.MaxRecordWorkers < 1 {
		return Config{}, 2, errors.New("--max-record-workers must be >= 1")
	}
	if cfg.MaxBuildWorkers < 1 {
		return Config{}, 2, errors.New("--max-build-workers must be >= 1")
	}
	if err := validateExecutable(cfg.CassiniBin); err != nil {
		return Config{}, 2, fmt.Errorf("cassini binary: %w", err)
	}
	if !strings.HasSuffix(strings.ToLower(cfg.FixturePath), ".mkv") {
		return Config{}, 2, fmt.Errorf("fixture path must end with .mkv: %s", cfg.FixturePath)
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
		ctx:         ctx,
		store:       store,
		cfg:         cfg,
		logger:      logger,
		stdout:      stdout,
		stderr:      stderr,
		recordSlots: make(chan struct{}, cfg.MaxRecordWorkers),
		buildQueue:  make(chan buildTask, queueCapacity),
	}
	rt.recordJobFn = rt.recordFromFixture
	rt.buildJobFn = rt.executeBuildCLI
	rt.startBuildWorkers()
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
		ID:             jobID,
		Provider:       provider,
		RequestJSON:    requestBody,
		Stage:          "record",
		State:          "queued",
		CreatedAt:      now,
		UpdatedAt:      now,
		RecordQueuedAt: &now,
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
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "", TriggerRequest{}, errors.New("request body is required")
	}
	var req TriggerRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return "", TriggerRequest{}, fmt.Errorf("invalid request JSON: %w", err)
	}
	if strings.TrimSpace(req.URL) == "" {
		return "", TriggerRequest{}, errors.New("url is required")
	}
	return trimmed, req, nil
}

func (rt *Runtime) runRecordJob(job Job, req TriggerRequest) {
	defer func() { <-rt.recordSlots }()

	startedAt := nowUTCString()
	if err := rt.store.MarkRecordRunning(context.Background(), job.ID, startedAt); err != nil {
		rt.logger.Printf("record start update failed id=%s: %v", job.ID, err)
		return
	}
	rt.logger.Printf("record started id=%s", job.ID)

	artifactRunPath, err := rt.recordJobFn(rt.ctx, job, req)
	finishedAt := nowUTCString()
	if err != nil {
		rt.logger.Printf("record failed id=%s: %v", job.ID, err)
		if updateErr := rt.store.MarkRecordFailed(context.Background(), job.ID, err.Error(), finishedAt); updateErr != nil {
			rt.logger.Printf("record fail update failed id=%s: %v", job.ID, updateErr)
		}
		return
	}
	if err := rt.enqueueBuildJob(job.ID, artifactRunPath, finishedAt); err != nil {
		rt.logger.Printf("build queue update failed id=%s: %v", job.ID, err)
		if updateErr := rt.store.MarkBuildFailed(context.Background(), job.ID, "", err.Error(), finishedAt); updateErr != nil {
			rt.logger.Printf("build queue failure update failed id=%s: %v", job.ID, updateErr)
		}
		return
	}
	rt.logger.Printf("record succeeded id=%s run=%s build_queued_at=%s", job.ID, artifactRunPath, finishedAt)
}

func (rt *Runtime) recordFromFixture(ctx context.Context, job Job, req TriggerRequest) (string, error) {
	fixturePath, err := rt.ensureFixture(ctx)
	if err != nil {
		return "", err
	}

	runPath := filepath.Join(rt.cfg.WorkRoot, job.ID+".run")
	bundle, err := PrepareRunBundle(runPath, false)
	if err != nil {
		return "", fmt.Errorf("prepare run bundle: %w", err)
	}
	if err := copyFile(fixturePath, bundle.RecordingPath); err != nil {
		_ = UpdateRunBundleStatus(bundle, bundleStateFailed, "record", err.Error())
		return "", fmt.Errorf("copy fixture to run bundle: %w", err)
	}
	if err := FinalizeRunBundle(bundle, RunManifest{SourceMode: "talk", RecorderName: "CassiniOperatorFixture"}); err != nil {
		return "", fmt.Errorf("finalize run bundle: %w", err)
	}
	return bundle.RootDir, nil
}

func (rt *Runtime) ensureFixture(ctx context.Context) (string, error) {
	rt.fixtureMu.Lock()
	defer rt.fixtureMu.Unlock()

	if info, err := os.Stat(rt.cfg.FixturePath); err == nil {
		if info.IsDir() {
			return "", fmt.Errorf("fixture path is a directory: %s", rt.cfg.FixturePath)
		}
		return rt.cfg.FixturePath, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat fixture: %w", err)
	}
	if rt.cfg.FixtureURL == "" {
		return "", fmt.Errorf("fixture missing at %s and FIXTURE_URL is not set", rt.cfg.FixturePath)
	}

	if err := os.MkdirAll(filepath.Dir(rt.cfg.FixturePath), 0o755); err != nil {
		return "", fmt.Errorf("create fixture dir: %w", err)
	}
	partPath := rt.cfg.FixturePath + ".part"
	_ = os.Remove(partPath)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rt.cfg.FixtureURL, nil)
	if err != nil {
		return "", fmt.Errorf("build fixture request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download fixture: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("download fixture: unexpected status %s", resp.Status)
	}

	out, err := os.Create(partPath)
	if err != nil {
		return "", fmt.Errorf("create fixture temp file: %w", err)
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		_ = out.Close()
		return "", fmt.Errorf("write fixture temp file: %w", err)
	}
	if err := out.Close(); err != nil {
		return "", fmt.Errorf("close fixture temp file: %w", err)
	}
	if err := os.Rename(partPath, rt.cfg.FixturePath); err != nil {
		return "", fmt.Errorf("activate fixture: %w", err)
	}
	return rt.cfg.FixturePath, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create destination dir: %w", err)
	}
	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create destination: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy bytes: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close destination: %w", err)
	}
	return nil
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

func (s *Store) ensureSchema() error {
	const schema = `
CREATE TABLE IF NOT EXISTS jobs (
  id TEXT PRIMARY KEY NOT NULL,
  provider TEXT NOT NULL,
  request_json TEXT NOT NULL,
  stage TEXT NOT NULL,
  state TEXT NOT NULL,
  artifact_run_path TEXT,
  artifact_meeting_path TEXT,
  artifact_site_path TEXT,
  error TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  record_queued_at TEXT,
  record_started_at TEXT,
  record_finished_at TEXT,
  build_queued_at TEXT,
  build_started_at TEXT,
  build_finished_at TEXT,
  publish_queued_at TEXT,
  publish_started_at TEXT,
  publish_finished_at TEXT,
  interrupted_at TEXT,
  completed_at TEXT
);
CREATE INDEX IF NOT EXISTS jobs_created_desc ON jobs(created_at DESC, id DESC);
`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("ensure schema: %w", err)
	}
	return nil
}

type Job struct {
	ID                  string  `json:"id"`
	Provider            string  `json:"provider"`
	RequestJSON         string  `json:"request_json"`
	Stage               string  `json:"stage"`
	State               string  `json:"state"`
	ArtifactRunPath     *string `json:"artifact_run_path"`
	ArtifactMeetingPath *string `json:"artifact_meeting_path"`
	ArtifactSitePath    *string `json:"artifact_site_path"`
	Error               *string `json:"error"`
	CreatedAt           string  `json:"created_at"`
	UpdatedAt           string  `json:"updated_at"`
	RecordQueuedAt      *string `json:"record_queued_at"`
	RecordStartedAt     *string `json:"record_started_at"`
	RecordFinishedAt    *string `json:"record_finished_at"`
	BuildQueuedAt       *string `json:"build_queued_at"`
	BuildStartedAt      *string `json:"build_started_at"`
	BuildFinishedAt     *string `json:"build_finished_at"`
	PublishQueuedAt     *string `json:"publish_queued_at"`
	PublishStartedAt    *string `json:"publish_started_at"`
	PublishFinishedAt   *string `json:"publish_finished_at"`
	InterruptedAt       *string `json:"interrupted_at"`
	CompletedAt         *string `json:"completed_at"`
}

func (s *Store) InsertQueuedJob(ctx context.Context, job Job) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO jobs (
  id, provider, request_json, stage, state,
  created_at, updated_at, record_queued_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID,
		job.Provider,
		job.RequestJSON,
		job.Stage,
		job.State,
		job.CreatedAt,
		job.UpdatedAt,
		stringOrNil(job.RecordQueuedAt),
	)
	if err != nil {
		return fmt.Errorf("insert job: %w", err)
	}
	return nil
}

func (s *Store) MarkRecordRunning(ctx context.Context, id, startedAt string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, updated_at = ?, record_started_at = ?
WHERE id = ?`, "record", "running", startedAt, startedAt, id)
	if err != nil {
		return fmt.Errorf("update record running: %w", err)
	}
	return nil
}

func (s *Store) MarkRecordSucceeded(ctx context.Context, id, artifactRunPath, finishedAt string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, artifact_run_path = ?, updated_at = ?, record_finished_at = ?, completed_at = ?, error = NULL
WHERE id = ?`, "done", "succeeded", artifactRunPath, finishedAt, finishedAt, finishedAt, id)
	if err != nil {
		return fmt.Errorf("update record success: %w", err)
	}
	return nil
}

func (s *Store) MarkRecordFailed(ctx context.Context, id, errText, finishedAt string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, error = ?, updated_at = ?, record_finished_at = ?, completed_at = ?
WHERE id = ?`, "done", "failed", strings.TrimSpace(errText), finishedAt, finishedAt, finishedAt, id)
	if err != nil {
		return fmt.Errorf("update record failure: %w", err)
	}
	return nil
}

func stringOrNil(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func (s *Store) ListJobs(ctx context.Context) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, provider, request_json, stage, state,
       artifact_run_path, artifact_meeting_path, artifact_site_path, error,
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
       artifact_run_path, artifact_meeting_path, artifact_site_path, error,
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
		&artifactRunPath,
		&artifactMeetingPath,
		&artifactSitePath,
		&jobError,
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

func (rt *Runtime) jobDetailHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/jobs/")
	if id == "" || id == r.URL.Path || strings.Contains(id, "/") {
		http.NotFound(w, r)
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
	writeJSON(w, http.StatusOK, job)
}

func requestLogger(logger *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.Printf("%s %s", r.Method, r.URL.RequestURI())
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
