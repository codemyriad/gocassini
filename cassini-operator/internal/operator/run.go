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
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const defaultBind = "127.0.0.1:8080"

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	logger := log.New(stderr, "cassini-operator: ", log.LstdFlags)

	fs := flag.NewFlagSet("cassini-operator", flag.ContinueOnError)
	fs.SetOutput(stderr)

	defaultDBPath, err := resolveDefaultDBPath()
	if err != nil {
		fmt.Fprintf(stderr, "resolve default db path: %v\n", err)
		return 1
	}

	var bindAddr string
	var dbPath string
	fs.StringVar(&bindAddr, "bind", defaultBind, "HTTP bind address")
	fs.StringVar(&dbPath, "db", defaultDBPath, "SQLite database path")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Cassini Operator runs the V1 job API and worker runtime.

Usage:
  cassini-operator
  cassini-operator --bind 127.0.0.1:8080
  cassini-operator --db ./runs/operator/jobs.sqlite3

Flags:
`)
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected positional arguments: %v\n\n", fs.Args())
		fs.Usage()
		return 2
	}

	store, err := OpenStore(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "open store: %v\n", err)
		return 1
	}
	defer store.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/jobs", jobsHandler(store))
	mux.HandleFunc("/jobs/", jobDetailHandler(store))

	server := &http.Server{
		Handler:           requestLogger(logger, mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	listener, err := net.Listen("tcp", bindAddr)
	if err != nil {
		fmt.Fprintf(stderr, "listen %s: %v\n", bindAddr, err)
		return 1
	}
	defer listener.Close()

	fmt.Fprintf(stdout, "listening -> http://%s\n", listener.Addr().String())
	logger.Printf("db -> %s", dbPath)

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

func resolveDefaultDBPath() (string, error) {
	repoRoot, err := findRepoRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(repoRoot, "runs", "operator", "jobs.sqlite3"), nil
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

func jobsHandler(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/jobs" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		jobs, err := store.ListJobs(r.Context())
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("list jobs: %v", err))
			return
		}
		writeJSON(w, http.StatusOK, jobs)
	}
}

func jobDetailHandler(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/jobs/")
		if id == "" || id == r.URL.Path || strings.Contains(id, "/") {
			http.NotFound(w, r)
			return
		}
		job, err := store.GetJob(r.Context(), id)
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
}

func requestLogger(logger *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.Printf("%s %s", r.Method, r.URL.Path)
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
