package operator

import (
	"context"
	"crypto/subtle"
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

	"cassini-operator/internal/operator/appapi"
)

const (
	defaultBind                  = "0.0.0.0:4000"
	defaultOperatorBasePath      = "/"
	defaultRecordWorkerCount     = 1
	nextcloudTalkProvider        = "nextcloud-talk"
	talkAuthModeGuestParticipant = "guest-participant"
	talkAuthModeHPBInternal      = "hpb-internal"
	defaultTalkAuthMode          = talkAuthModeHPBInternal
)

type Config struct {
	RepoRoot         string
	BindAddr         string
	BasePath         string
	DBPath           string
	WorkRoot         string
	SiteRoot         string
	CassiniBin       string
	TalkSharedSecret string
	TalkBackendURL   string
	// TalkSecretSource records where TalkSharedSecret came from ("env" when
	// supplied via flag/env, "generated" when the operator self-generated and
	// persisted it, "unset" when none is available). Resolved at startup by
	// resolveTalkProvisioning (D-447); reported by /status. Not a flag.
	TalkSecretSource string
	// TalkRecordingBackendURL is the recording_servers server value to register
	// in spreed — the AppAPI proxy base for this ExApp, derived from
	// NEXTCLOUD_URL + APP_ID. Empty on standalone/dev deploys. Not a flag.
	TalkRecordingBackendURL string
	MaxRecordWorkers        int
	MaxBuildWorkers         int
	// BundledModelRoot is the read-only directory where the image baked its
	// models, and ModelCacheRoot is the writable cache that receives a tier the
	// image does not carry. Under an AppAPI deploy the cache lands on the
	// persistent volume, so a downloaded model survives a container recreate.
	// Both live here rather than being read from the process environment at
	// admission time, so the policy is fixed once at startup and a developer
	// whose shell exports CASSINI_* for the recorder cannot change what the
	// operator decides.
	BundledModelRoot string
	ModelCacheRoot   string
	// DisallowModelDownload is the air-gap switch. When an administrator sets
	// it, the operator blocks a tier the image does not bake instead of
	// fetching it, and the build child receives the same instruction. The
	// operator sets the variable for every child from this value, so an
	// inherited one can neither enable nor defeat the policy by accident.
	DisallowModelDownload bool
	// APIToken (CASSINI_OPERATOR_API_TOKEN) optionally guards the operator
	// JSON API with bearer auth for standalone deploys; empty disables it
	// and AppAPI-authenticated requests bypass it (D-376).
	APIToken string
	// PublishSink names where published meetings go (publish_sink.go, D-533).
	// Empty means unset, which resolves to the default; a non-empty unknown
	// name is rejected at startup.
	PublishSink string
	// ArtifactRetention names which attempt-scoped payloads under runs/ are
	// pruned (retention.go, D-583). Empty means unset, which resolves to the
	// default; a non-empty unknown name is rejected at startup.
	ArtifactRetention string
}

type Runtime struct {
	ctx context.Context
	// cancel stops rt.ctx; workerWG tracks the pipeline worker goroutines
	// NewRuntime spawns (build, publish, requeue dispatch) so Shutdown can
	// await their exit instead of leaving them writing under WorkRoot.
	cancel       context.CancelFunc
	workerWG     sync.WaitGroup
	store        *Store
	cfg          Config
	logger       *log.Logger
	stdout       io.Writer
	stderr       io.Writer
	recordSlots  chan struct{}
	buildQueue   chan buildTask
	sealQueue    chan sealTask
	publishQueue chan publishTask
	events       *eventHub
	recordMu     sync.Mutex
	recordJobs   map[string]*recordProcessState
	recordWG     sync.WaitGroup
	// buildExecutionMu is the final admission gate around the whole build. The
	// configured worker count may exceed one, but CUDA recognizers and the RAM/
	// VRAM headroom probe are not safely reservable between concurrent workers.
	// Serializing here makes the resource check and ensuing launch atomic with
	// respect to every other build in this operator process.
	buildExecutionMu sync.Mutex
	talkRooms        map[string]*talkRoomState
	talkJobs         map[string]*talkRoomState
	recordJobFn      func(context.Context, Job, TriggerRequest) (recordResult, error)
	buildJobFn       func(context.Context, buildTask) (string, error)
	// buildResourceRetryDelay bounds transient RAM/VRAM retry frequency.
	// Tests shorten it; production uses defaultBuildResourceRetryDelay.
	buildResourceRetryDelay time.Duration
	// maxBuildResourceDeferrals bounds transient retry churn. Permanent
	// conditions block immediately; repeated pressure blocks at this ceiling.
	maxBuildResourceDeferrals int
	// sealJobFn packs one attempt's `.meeting` into its immutable `.opus`
	// (D-583). It is a seam for the same reason buildJobFn and publishJobFn
	// are: tests drive the pipeline without an ffmpeg subprocess.
	sealJobFn    func(context.Context, sealTask) (string, error)
	publishJobFn func(context.Context, publishTask) (string, error)
	// publishSink is where a published meeting is delivered (publish_sink.go,
	// D-533). Exactly one, selected by name; runPublishJob knows nothing about
	// the destination. Never nil — an unset name resolves to the default.
	publishSink publishSink
	// fetchTalkRoomName resolves a Talk room's display name via the
	// Nextcloud OCS API (talk_room_name.go). Nil outside AppAPI deployments;
	// tests stub it. talkRoomNameRetryGap defaults to the package constant;
	// tests shrink it.
	fetchTalkRoomName    talkRoomNameFetcher
	talkRoomNameRetryGap time.Duration
	// talkAudienceRetryGap paces the recording-audience retry ladder
	// (talk_participants.go, D-553). Zero means the package default; tests
	// shrink it.
	talkAudienceRetryGap time.Duration
	// fetchTalkParticipants resolves a Talk room's grantable ACL principals and
	// applyNCFilesAccessFn writes the per-meeting advanced-ACL grants
	// (talk_participants.go / webdav_acl.go, D-534). Both nil unless AppAPI is
	// active — inside an ExApp per-participant access is the only model there
	// is (D-554), so there is nothing left to opt into.
	fetchTalkParticipants talkParticipantsFetcher
	applyNCFilesAccessFn  ncFilesAccessApplier
	// recordStopAckGrace and recordStopFinalizeGrace default to the package
	// constants; tests shrink them to exercise stop enforcement quickly.
	recordStopAckGrace      time.Duration
	recordStopFinalizeGrace time.Duration
	// Talk status-callback tuning: a dedicated bounded client (a hung
	// Nextcloud connection must not stall the post-record path, D-352) and a
	// bounded retry schedule. Tests shrink them.
	talkJSONClient  *http.Client
	talkRetryDelays []time.Duration
	// requeueKick nudges the requeue dispatcher to re-scan the DB for
	// queued build/seal/publish rows the channels could not accept (D-367).
	requeueKick chan struct{}
	// sealJobTimeout bounds one `cassini pack` run; without it a hung pack
	// wedges the seal worker forever, and nothing behind it can publish
	// (D-583). Tests shrink it.
	sealJobTimeout time.Duration
	// publishJobTimeout bounds one `cassini publish` run; without it a hung
	// publish wedges the single publish worker forever (D-367). Tests shrink it.
	publishJobTimeout time.Duration
	// computeProbe reports whether the effective STT device is usable. It takes
	// the device selected from the current in-memory settings snapshot so
	// readiness cannot drift from the policy used by newly spawned builds.
	// Tests stub it.
	computeProbe func(device string) (usable bool, detail string)
	// computeReadiness coalesces and briefly caches the nvidia-smi-backed
	// readiness probe so status polling cannot create a subprocess storm.
	computeReadiness *computeStatusProbe
	// recordHealth throttles the deep /healthz?check=record doctor exec with
	// singleflight + a short TTL cache + an exec timeout, so the unauthenticated
	// health endpoint cannot fan out doctor subprocesses (D-376).
	recordHealth        *ttlProbe
	recordHealthTimeout time.Duration
	// settings is the operator-owned STT policy, injected into every
	// record/build/doctor subprocess. settingsMu guards in-memory reads (at job
	// spawn time) against PUT /settings writes; settingsPath is where it
	// persists, on the same volume as the DB (D-435).
	settingsMu   sync.RWMutex
	settings     STTSettings
	settingsPath string
}

type TriggerRequest struct {
	Platform              string  `json:"platform"`
	BaseURL               string  `json:"baseURL,omitempty"`
	TalkConnectURL        string  `json:"talkConnectURL,omitempty"`
	RoomToken             string  `json:"roomToken,omitempty"`
	URL                   string  `json:"url,omitempty"`
	TalkAuthMode          string  `json:"talkAuthMode"`
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
	// A leading non-flag word selects a maintenance command instead of the
	// server. Everything else is the server with flags, as it has always been —
	// the binary's job is to be the operator, and a subcommand is the exception.
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		if args[0] == backfillNCFilesCommand {
			return runBackfillNCFiles(ctx, args[1:], stdout, stderr)
		}
		fmt.Fprintf(stderr, "unknown command %q (known commands: %s)\n", args[0], backfillNCFilesCommand)
		return 2
	}

	logger := log.New(stderr, "cassini-operator: ", log.LstdFlags)

	cfg, exitCode, err := loadConfig(args, stderr)
	if err != nil {
		if exitCode == 0 {
			return 0
		}
		fmt.Fprintf(stderr, "%v\n", err)
		return exitCode
	}

	exappCfg, err := LoadExAppConfig()
	if err != nil {
		fmt.Fprintf(stderr, "exapp config: %v\n", err)
		return 1
	}
	cfg.BindAddr = exappCfg.applyToBindAddr(cfg.BindAddr)

	// Zero-config Talk recording (D-447): generate + persist a recording secret
	// when none was supplied, and derive the backend URL to register in spreed.
	// An explicit CASSINI_TALK_RECORDING_SECRET still wins. The persisted secret
	// lives next to the DB, i.e. on the AppAPI volume, so it survives restart.
	prov := resolveTalkProvisioning(filepath.Dir(cfg.DBPath), cfg.TalkSharedSecret, exappCfg.NextcloudURL, exappCfg.AppID, logger)
	cfg.TalkSharedSecret = prov.Secret
	cfg.TalkSecretSource = prov.SecretSource
	cfg.TalkRecordingBackendURL = prov.BackendURL

	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(stderr, "open store: %v\n", err)
		return 1
	}
	defer store.Close()
	interruptedAt := nowUTCString()
	interrupted, err := store.MarkIncompleteJobsInterrupted(context.Background(), interruptedAt)
	if err != nil {
		fmt.Fprintf(stderr, "mark interrupted jobs: %v\n", err)
		return 1
	}
	if interrupted > 0 {
		logger.Printf("startup interrupted_jobs -> %d", interrupted)
	}

	runtime := NewRuntime(ctx, store, cfg, logger, stdout, stderr)
	runtime.fetchTalkRoomName = exappCfg.talkRoomNameFetcher()
	runtime.fetchTalkParticipants = exappCfg.talkParticipantsFetcher()
	runtime.applyNCFilesAccessFn = exappCfg.ncFilesAccessApplier(logger)

	// Resolve the publish destination. An explicit name always wins; an unset
	// one is resolved from the deployment shape, and in an ExApp that must be
	// Nextcloud Files — defaulting an ExApp to `local` would silently keep
	// recordings on the app's own volume, which is the exact failure D-549
	// exists to prevent. Non-ExApp deployments keep the local site.
	sinkName := cfg.PublishSink
	if sinkName == "" {
		sinkName = defaultPublishSinkFor(exappCfg)
	}
	sink, err := newPublishSinkFor(sinkName, cfg, exappCfg, runtime, logger)
	if err != nil {
		fmt.Fprintf(stderr, "publish sink: %v\n", err)
		return 1
	}
	runtime.publishSink = sink

	// Only a deployment that actually serves recordings from Nextcloud Files is
	// expected to have a substrate. AppAPI can also run with an ExApp deliberately
	// pinned to `local`; that escape hatch must not report an unhealthy substrate
	// for a destination it serves nothing from.
	//
	// Marked HERE rather than only inside the provisioner so a container that
	// restarts without seeing another enabled edge reports `unknown` instead of
	// passing itself off as a standalone operator with nothing to provision. That
	// makes D-541's restart-convergence gap visible; it does not close it.
	if exappCfg.appAPIActive() && sink.Name() == publishSinkNextcloudFiles {
		ncAccessSubstrate.markApplicable()
	}

	// The storage mode (D-616) is read here, at startup, and not only on the
	// enabled edge. The edge is what PROVES the mode against Nextcloud; this is
	// what makes a plain container restart keep serving the archive the way the
	// administrator chose, instead of falling back to the access-controlled read
	// path for an instance that has no Team folder to read through. The file is
	// local, so it costs no round-trip and cannot fail for want of Nextcloud.
	//
	// It deliberately does NOT make the substrate usable: publishing still waits
	// for the edge (nc_access_status.go), because a recorded mode is a decision,
	// not evidence that the storage behind it is still there.
	ncStorage.setPath(storageSettingsPath(cfg))
	if settings, err := LoadStorageSettings(ncStorage.settingsPath()); err != nil {
		logger.Printf("ERROR: storage_settings load failed (%v); access control stays on until the preflight can re-read it", err)
		ncStorage.set(true, storageModeSourceConfigured)
	} else if settings.Configured() {
		ncStorage.set(settings.AccessControlled(), storageModeSourceConfigured)
		logger.Printf("storage_mode -> %s (configured)", settings.Mode())
	}

	// The preflight remains tied to the AppAPI enabled edge, but not to the
	// eager whole-archive uploader removed by D-613.
	exappCfg.onEnabled = exappCfg.enabledCallback(runtime.ctx, logger)
	if interrupted > 0 {
		// A restart mid-recording leaves spreed convinced the room is still
		// recording; tell it the recording failed so the room state converges
		// instead of waiting for a moderator's stop click to surface
		// RECORDING_FAILED (D-352/D-362).
		go runtime.NotifyInterruptedTalkRecordings(interruptedAt)
	}

	exappCfg.PublishedDir = cfg.SiteRoot
	exappCfg.PublishSink = sink.Name()
	warnIfEphemeral(logger, filepath.Dir(cfg.DBPath), cfg.SiteRoot)

	server := &http.Server{
		Handler:           newHTTPHandler(logger, runtime, exappCfg),
		ReadHeaderTimeout: 10 * time.Second,
	}

	listener, err := net.Listen("tcp", cfg.BindAddr)
	if err != nil {
		fmt.Fprintf(stderr, "listen %s: %v\n", cfg.BindAddr, err)
		return 1
	}
	defer listener.Close()

	fmt.Fprintf(stdout, "listening -> http://%s\n", listener.Addr().String())
	logger.Printf("base_path -> %s", cfg.BasePath)
	logger.Printf("db -> %s", cfg.DBPath)
	logger.Printf("work_root -> %s", cfg.WorkRoot)
	logger.Printf("site_root -> %s", cfg.SiteRoot)
	// Report the destination we actually constructed. cfg.PublishSink stays raw
	// (and is commonly empty in an ExApp), so applying the standalone default to
	// it here would misreport the resolved nextcloud-files sink as local.
	logger.Printf("publish_sink -> %s", sink.Name())
	logger.Printf("artifact_retention -> %s", artifactRetentionOrDefault(cfg.ArtifactRetention))
	if persistRoot := persistentStorageRoot(); persistRoot != "" {
		logger.Printf("app_persistent_storage -> %s", persistRoot)
	}
	logger.Printf("cassini_bin -> %s", cfg.CassiniBin)
	logger.Printf("talk_shared_secret_set -> %t (source=%s)", strings.TrimSpace(cfg.TalkSharedSecret) != "", cfg.TalkSecretSource)
	// The signaling internal secret is the one input we cannot self-provision
	// (invisible HPB-internal recording authenticates with it). Surface its
	// absence loudly at startup — not silently at record time — so an admin
	// learns of it before the first recording fails (see docs/exapp-install.md).
	if signalingInternalSecretConfigured() {
		logger.Printf("talk_signaling_internal_secret_set -> true")
	} else {
		logger.Printf("WARNING: talk_signaling_internal_secret_set -> false: %s", signalingInternalSecretHint)
	}
	if cfg.TalkRecordingBackendURL != "" {
		logger.Printf("talk_recording_backend_url -> %s", cfg.TalkRecordingBackendURL)
	}
	logger.Printf("max_record_workers -> %d", cfg.MaxRecordWorkers)
	logger.Printf("max_build_workers -> %d", cfg.MaxBuildWorkers)
	logger.Printf("operator_api_token_auth -> %t", cfg.APIToken != "")
	// A CUDA image without GPU access must fail loudly, not silently fall
	// back to CPU (D-363).
	runtime.logComputeDeviceStatus()
	if exappCfg.Active {
		logger.Printf("exapp_appapi -> active (app_id=%s app_version=%s)", exappCfg.AppID, exappCfg.AppVersion)
	} else {
		logger.Printf("exapp_appapi -> inactive (APP_SECRET unset)")
	}
	if exappCfg.ViewerDist != "" {
		logger.Printf("exapp_viewer_dist -> %s", exappCfg.ViewerDist)
	}

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
		// The operator is the container's main process: returning while a
		// record job is still finalizing would SIGKILL the recorder
		// mid-compose and destroy the recording (D-350). In-flight stops are
		// enforced per process, so this wait is bounded.
		if !runtime.WaitForRecordJobs(recordShutdownWait) {
			logger.Printf("shutdown abandoned record jobs still running after %s", recordShutdownWait)
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
	repoRoot, repoRootErr := findRepoRoot()
	if repoRootErr != nil {
		repoRoot = ""
	}

	defaultDataRoot := defaultOperatorDataRoot(repoRoot)
	defaultMaxRecordWorkers, err := parsePositiveIntEnvAny([]string{"CASSINI_MAX_RECORD_WORKERS", "MAX_RECORD_WORKERS"}, defaultRecordWorkerCount)
	if err != nil {
		return Config{}, 2, err
	}
	defaultMaxBuildWorkers, err := parsePositiveIntEnvAny([]string{"CASSINI_MAX_BUILD_WORKERS", "MAX_BUILD_WORKERS"}, defaultBuildWorkerCount)
	if err != nil {
		return Config{}, 2, err
	}

	fs := flag.NewFlagSet("cassini-operator", flag.ContinueOnError)
	fs.SetOutput(stderr)

	cfg := Config{RepoRoot: repoRoot}
	// Not flags: these describe the image the operator is running inside, not a
	// choice an invocation makes.
	cfg.BundledModelRoot = envOrDefaultAny([]string{"CASSINI_BUNDLED_MODEL_ROOT"}, "")
	cfg.DisallowModelDownload = envBool("CASSINI_DISALLOW_MODEL_DOWNLOAD")
	fs.StringVar(&cfg.BindAddr, "bind", envOrDefaultAny([]string{"CASSINI_OPERATOR_BIND_ADDR"}, defaultBind), "HTTP bind address")
	fs.StringVar(&cfg.BasePath, "base-path", envOrDefaultAny([]string{"CASSINI_OPERATOR_BASE_PATH"}, defaultOperatorBasePath), "HTTP route prefix")
	// Data path defaults are persistent-storage aware: under an AppAPI docker
	// deploy (APP_PERSISTENT_STORAGE set) paths left unset or still at their
	// baked image defaults land on the AppAPI volume instead of overlayfs.
	persistRoot := persistentStorageRoot()
	fs.StringVar(&cfg.DBPath, "db", exAppDataPathDefault(persistRoot,
		envOrDefaultAny([]string{"CASSINI_OPERATOR_DB_PATH"}, ""),
		imageDefaultDBPath, "operator/jobs.sqlite3",
		filepath.Join(defaultDataRoot, "jobs.sqlite3")), "SQLite database path")
	fs.StringVar(&cfg.WorkRoot, "work-root", exAppDataPathDefault(persistRoot,
		envOrDefaultAny([]string{"CASSINI_OPERATOR_WORK_ROOT", "WORK_ROOT"}, ""),
		imageDefaultWorkRoot, "operator/jobs",
		filepath.Join(defaultDataRoot, "jobs")), "per-job artifact root")
	fs.StringVar(&cfg.SiteRoot, "site-root", defaultSiteRoot(persistRoot, defaultDataRoot), "published site output root")
	fs.StringVar(&cfg.ModelCacheRoot, "model-cache-root", exAppDataPathDefault(persistRoot,
		envOrDefaultAny([]string{"CASSINI_CACHE_ROOT"}, ""),
		imageDefaultCacheRoot, "operator/models",
		filepath.Join(defaultDataRoot, "models")), "writable cache for models the image does not bundle")
	fs.StringVar(&cfg.CassiniBin, "cassini-bin", envOrDefaultAny([]string{"CASSINI_BIN"}, defaultCassiniBinPath(repoRoot)), "Cassini CLI binary path")
	fs.StringVar(&cfg.TalkSharedSecret, "talk-shared-secret", envOrDefaultAny([]string{"CASSINI_TALK_RECORDING_SECRET", "TALK_RECORDING_SECRET"}, ""), "shared secret for Talk recording backend requests")
	fs.StringVar(&cfg.TalkBackendURL, "talk-backend-url", envOrDefaultAny([]string{"CASSINI_TALK_BACKEND_URL", "TALK_BACKEND_URL"}, ""), "Nextcloud Talk base URL for operator-to-Nextcloud calls")
	fs.StringVar(&cfg.PublishSink, "sink", envOrDefaultAny([]string{"CASSINI_PUBLISH_SINK"}, ""), "where published meetings are delivered (known sinks: "+strings.Join(publishSinkNames(), ", ")+"; default "+defaultPublishSink+")")
	fs.StringVar(&cfg.ArtifactRetention, "artifact-retention", envOrDefaultAny([]string{"CASSINI_ARTIFACT_RETENTION"}, ""), "which attempt artifacts under runs/ are pruned (policies: "+strings.Join(artifactRetentionNames(), ", ")+"; default "+defaultArtifactRetention+")")
	fs.IntVar(&cfg.MaxRecordWorkers, "max-record-workers", defaultMaxRecordWorkers, "maximum concurrent record workers")
	fs.IntVar(&cfg.MaxBuildWorkers, "max-build-workers", defaultMaxBuildWorkers, "maximum concurrent build workers")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Cassini Operator runs the V2 live-record job API and worker runtime.

Usage:
  cassini-operator
  cassini-operator --bind 0.0.0.0:4000
  cassini-operator --base-path /operator --db ./cassini-operator/runtime/jobs.sqlite3

Commands:
  `+backfillNCFilesCommand+`   one-shot migration of a legacy in-container archive
                       into Nextcloud Files (run by hand after an update;
                       see --help on the command itself)

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

	// Env-only on purpose: a flag would leak the token into process listings.
	cfg.APIToken = strings.TrimSpace(os.Getenv("CASSINI_OPERATOR_API_TOKEN"))

	cfg.BindAddr = strings.TrimSpace(cfg.BindAddr)
	if cfg.BindAddr == "" {
		return Config{}, 2, errors.New("--bind must not be empty")
	}
	cfg.BasePath, err = normalizeBasePath(cfg.BasePath)
	if err != nil {
		return Config{}, 2, err
	}
	cfg.DBPath = resolveConfigPath(repoRoot, cfg.DBPath)
	cfg.WorkRoot = resolveConfigPath(repoRoot, cfg.WorkRoot)
	cfg.SiteRoot = resolveConfigPath(repoRoot, cfg.SiteRoot)
	cfg.CassiniBin = resolveConfigPath(repoRoot, cfg.CassiniBin)
	cfg.TalkBackendURL = strings.TrimRight(strings.TrimSpace(cfg.TalkBackendURL), "/")
	if cfg.MaxRecordWorkers < 1 {
		return Config{}, 2, errors.New("--max-record-workers must be >= 1")
	}
	if cfg.MaxBuildWorkers < 1 {
		return Config{}, 2, errors.New("--max-build-workers must be >= 1")
	}
	cfg.PublishSink = strings.TrimSpace(cfg.PublishSink)
	if err := validatePublishSinkName(cfg.PublishSink); err != nil {
		return Config{}, 2, err
	}
	cfg.ArtifactRetention = strings.TrimSpace(cfg.ArtifactRetention)
	if err := validateArtifactRetentionName(cfg.ArtifactRetention); err != nil {
		return Config{}, 2, err
	}
	if strings.TrimSpace(cfg.CassiniBin) == "" {
		return Config{}, 2, errors.New("--cassini-bin must not be empty")
	}
	if err := validateExecutable(cfg.CassiniBin); err != nil {
		return Config{}, 2, fmt.Errorf("cassini binary: %w", err)
	}

	return cfg, 0, nil
}

// defaultSiteRoot resolves where the published site lives when no --site-root
// is given. Shared with the backfill command (nc_backfill.go) rather than
// re-derived there: a backfill that reads a different directory than the one
// the operator writes would silently sync nothing and report success.
func defaultSiteRoot(persistRoot, dataRoot string) string {
	return exAppDataPathDefault(persistRoot,
		envOrDefaultAny([]string{"CASSINI_OPERATOR_SITE_ROOT", "SITE_ROOT"}, ""),
		imageDefaultSiteRoot, "site/published",
		filepath.Join(dataRoot, "site"))
}

func parsePositiveIntEnvAny(names []string, fallback int) (int, error) {
	for _, name := range names {
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			continue
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
	return fallback, nil
}

func envOrDefaultAny(names []string, fallback string) string {
	for _, name := range names {
		value := strings.TrimSpace(os.Getenv(name))
		if value != "" {
			return value
		}
	}
	return fallback
}

func resolveConfigPath(base, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	if strings.TrimSpace(base) != "" {
		return filepath.Join(base, path)
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return resolved
}

func defaultOperatorDataRoot(repoRoot string) string {
	if strings.TrimSpace(repoRoot) != "" {
		return filepath.Join(repoRoot, "cassini-operator", "runtime")
	}
	return "/var/lib/cassini-operator"
}

func defaultCassiniBinPath(repoRoot string) string {
	if strings.TrimSpace(repoRoot) != "" {
		return filepath.Join(repoRoot, "bin", "cassini")
	}
	return "/usr/local/bin/cassini"
}

func normalizeBasePath(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	normalized := trimmed
	if normalized == "" {
		normalized = defaultOperatorBasePath
	}
	normalized = strings.TrimRight(normalized, "/")
	if normalized == "" {
		normalized = "/"
	}
	if !strings.HasPrefix(normalized, "/") {
		return "", errors.New("--base-path must start with '/'")
	}
	return normalized, nil
}

func NewRuntime(ctx context.Context, store *Store, cfg Config, logger *log.Logger, stdout, stderr io.Writer) *Runtime {
	queueCapacity := cfg.MaxBuildWorkers * 16
	if queueCapacity < 16 {
		queueCapacity = 16
	}
	ctx, cancel := context.WithCancel(ctx)
	rt := &Runtime{
		ctx:          ctx,
		cancel:       cancel,
		store:        store,
		cfg:          cfg,
		logger:       logger,
		stdout:       stdout,
		stderr:       stderr,
		recordSlots:  make(chan struct{}, cfg.MaxRecordWorkers),
		buildQueue:   make(chan buildTask, queueCapacity),
		sealQueue:    make(chan sealTask, queueCapacity),
		publishQueue: make(chan publishTask, 16),
		events:       newEventHub(),
		recordJobs:   map[string]*recordProcessState{},
		talkRooms:    map[string]*talkRoomState{},
		talkJobs:     map[string]*talkRoomState{},

		recordStopAckGrace:      recordStopAckGrace,
		recordStopFinalizeGrace: recordStopFinalizeGrace,

		talkJSONClient:       &http.Client{Timeout: talkJSONRequestTimeout},
		talkRetryDelays:      talkDeliveryRetryDelays,
		talkRoomNameRetryGap: talkRoomNameRetryGap,
		talkAudienceRetryGap: talkAudienceRetryGap,

		requeueKick:               make(chan struct{}, 1),
		buildResourceRetryDelay:   defaultBuildResourceRetryDelay,
		maxBuildResourceDeferrals: defaultMaxBuildResourceDeferrals,
		sealJobTimeout:            defaultSealJobTimeout,
		publishJobTimeout:         defaultPublishJobTimeout,
		recordHealthTimeout:       recordHealthProbeTimeout,
		settingsPath:              settingsPath(cfg),
	}
	// Detect hardware on first start (or track it under an auto default) and
	// load the persisted STT policy. A failure here must not take the operator
	// down: fall back to an in-memory auto default so jobs still run (D-435).
	if settings, err := LoadOrInitSettingsWithMigrationReporter(rt.settingsPath, rt.reportSettingsMigration); err != nil {
		logger.Printf("stt_settings load failed (%v); using in-memory auto default", err)
		rt.settings = detectSettings()
	} else {
		rt.settings = settings
	}
	store.SetStateChangePublisher(rt.publishStateChangeEvent)
	rt.recordJobFn = rt.executeRecordCLI
	rt.buildJobFn = rt.executeBuildCLI
	rt.sealJobFn = rt.executeSealCLIWithTimeout
	rt.publishJobFn = rt.executePublishCLIWithTimeout
	// loadConfig already rejected an unknown name; a Config built directly (as
	// tests do) carries an empty name, which resolves to the default. So this
	// cannot fail in practice — but fall back rather than panic if it ever does.
	sink, err := newPublishSink(cfg.PublishSink, cfg, logger)
	if err != nil {
		logger.Printf("publish sink %q unavailable (%v); using %s", cfg.PublishSink, err, defaultPublishSink)
		sink, _ = newPublishSink(defaultPublishSink, cfg, logger)
	}
	rt.publishSink = sink
	// Keep readiness live without spawning nvidia-smi for every status request:
	// a short keyed TTL still reflects driver resets and device-policy changes.
	rt.computeProbe = probeComputeDevice
	rt.computeReadiness = newComputeStatusProbe(computeReadinessProbeTTL, func(device string) (bool, string) {
		return rt.computeProbe(device)
	})
	rt.recordHealth = newTTLProbe(recordHealthProbeTTL, func() error {
		probeCtx := rt.ctx
		if timeout := rt.recordHealthTimeout; timeout > 0 {
			var cancel context.CancelFunc
			probeCtx, cancel = context.WithTimeout(probeCtx, timeout)
			defer cancel()
		}
		return rt.runRecordDoctorContext(probeCtx)
	})
	rt.startBuildWorkers()
	rt.startSealWorker()
	rt.startPublishWorker()
	rt.workerWG.Add(1)
	go rt.requeueDispatcher()
	return rt
}

// Shutdown stops the pipeline worker goroutines and blocks until they have
// exited, so nothing keeps writing under WorkRoot afterwards. It does not wait
// for in-flight record jobs; that is WaitForRecordJobs. Tests must call it
// before their temp WorkRoot is removed.
//
// Every goroutine NewRuntime spawns must be registered on workerWG, or this
// promise is only partly kept. It covers the build workers, the seal worker,
// the publish worker and the requeue dispatcher — the four that write under
// WorkRoot. D-583 removed the detached `cassini pack` goroutine this used to
// wait on separately; sealing is now one of those registered workers.
func (rt *Runtime) Shutdown() {
	rt.cancel()
	rt.workerWG.Wait()
}

func newHTTPHandler(logger *log.Logger, rt *Runtime, exappCfg ExAppConfig) http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("/jobs", rt.jobsHandler)
	api.HandleFunc("/jobs/", rt.jobDetailHandler)
	api.HandleFunc("/events", rt.eventsHandler)
	api.HandleFunc("/status", rt.statusHandler)
	api.HandleFunc("/setup", rt.setupHandler)
	api.HandleFunc("/settings", rt.settingsHandler)
	api.Handle("/storage", exappCfg.storageHandler(rt))
	api.HandleFunc("/talk/provisioning", rt.talkProvisioningHandler)

	// Optional bearer auth for the standalone job API (CASSINI_OPERATOR_API_TOKEN,
	// off by default). Requests that already passed the AppAPI middleware are
	// untouched, so an ExApp deploy may set the token for direct port access
	// without breaking the Nextcloud proxy path (D-376).
	var apiHandler http.Handler = api
	if strings.TrimSpace(rt.cfg.APIToken) != "" {
		apiHandler = requireBearerToken(strings.TrimSpace(rt.cfg.APIToken), api)
	}

	root := http.NewServeMux()
	// ExApp lifecycle + static prefixes (no-op when their env paths are unset).
	exappCfg.installRoutes(root, filepath.Dir(rt.cfg.DBPath), logger)
	// Operator JSON API under BasePath ("/" or "/operator", etc).
	mountBasePathOnto(root, rt.cfg.BasePath, apiHandler)

	// /heartbeat, /healthz, and the Talk recording-backend endpoints must answer
	// without AppAPI auth headers — Talk uses its own HMAC scheme (Talk-Recording-
	// Backend/Random/Checksum) and the heartbeat / healthz probes are unauthed.
	// Mount them on an outer mux so the AppAPI middleware never sees them.
	outer := http.NewServeMux()
	outer.Handle("/heartbeat", heartbeatHandler())
	outer.HandleFunc("/healthz", rt.healthzHandler)
	outer.HandleFunc("/api/v1/welcome", rt.talkWelcomeHandler)
	outer.HandleFunc("/api/v1/room/", rt.talkRoomHandler)
	outer.Handle("/", exappCfg.wrap(root, logger))

	return requestLogger(logger, outer)
}

// mountBasePathOnto registers the operator api mux under basePath on the given
// root mux. When basePath is "/" the API is mounted at the root.
func mountBasePathOnto(root *http.ServeMux, basePath string, api http.Handler) {
	if basePath == "" || basePath == "/" {
		root.Handle("/jobs", api)
		root.Handle("/jobs/", api)
		root.Handle("/events", api)
		root.Handle("/status", api)
		root.Handle("/setup", api)
		root.Handle("/settings", api)
		root.Handle("/storage", api)
		root.Handle("/talk/provisioning", api)
		return
	}
	root.Handle(basePath, http.StripPrefix(basePath, api))
	root.Handle(basePath+"/", http.StripPrefix(basePath, api))
}

// requireBearerToken guards an API handler with a static bearer token for
// standalone deployments where no AppAPI middleware fronts the operator.
// Requests carrying a verified AppAPI identity pass through untouched; token
// comparison is constant-time and the token value is never logged (D-376).
func requireBearerToken(token string, next http.Handler) http.Handler {
	expected := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if appapi.Authenticated(r.Context()) {
			next.ServeHTTP(w, r)
			return
		}
		const prefix = "Bearer "
		auth := r.Header.Get("Authorization")
		if len(auth) <= len(prefix) ||
			!strings.EqualFold(auth[:len(prefix)], prefix) ||
			subtle.ConstantTimeCompare([]byte(strings.TrimSpace(auth[len(prefix):])), expected) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="cassini-operator"`)
			writeJSONError(w, http.StatusUnauthorized, "missing or invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// appapiUserForLog returns the AppAPI-authenticated user id for job-mutation
// log lines, or "-" when the request carried no user (standalone deploy or
// system request) (D-376).
func appapiUserForLog(r *http.Request) string {
	if user := appapi.UserID(r.Context()); user != "" {
		return user
	}
	return "-"
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
	resp, err := rt.acceptRecordJob(r.Context(), provider, requestBody, req)
	if err != nil {
		status, msg := recordAcceptError(err)
		rt.logger.Printf("reject provider=%s target=%s user=%s err=%v", provider, req.logTarget(), appapiUserForLog(r), err)
		writeJSONError(w, status, msg)
		return
	}
	rt.logger.Printf("accepted id=%s provider=%s target=%s user=%s", resp.ID, provider, req.logTarget(), appapiUserForLog(r))
	writeJSON(w, http.StatusAccepted, resp)
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
	defer rt.recordWG.Done()
	// The record slot is freed as soon as the record subprocess exits (the
	// releaseSlot call below): post-record bookkeeping — Talk delivery with
	// its retry schedule, the build handoff — must not hold recording
	// capacity hostage (D-367). The deferred call covers early-error returns.
	releaseSlot := sync.OnceFunc(func() { <-rt.recordSlots })
	defer releaseSlot()
	defer rt.clearTalkRoomJobByID(job.ID)

	// Open the stop bracket before the store publishes record/running and close
	// it only after the stage has left that state. Stoppability is answered from
	// two places — the store's stage/state and this in-memory registration — and
	// a stop is only honest if both agree. Registering after the recorder spawned
	// (and dropping the entry when it exited) left the registration strictly
	// inside the store's claim at both ends, so a stop landing in either gap was
	// refused with "job is not stoppable" against a job the API had just reported
	// as running (D-501).
	rt.registerRecordJob(job.ID)
	defer rt.unregisterRecordJob(job.ID)

	startedAt := nowUTCString()
	if err := rt.store.MarkRecordRunning(context.Background(), job.ID, startedAt); err != nil {
		rt.logger.Printf("record start update failed id=%s: %v", job.ID, err)
		return
	}
	rt.logger.Printf("record started id=%s", job.ID)

	result, err := rt.recordJobFn(rt.ctx, job, req)
	releaseSlot()
	finishedAt := nowUTCString()
	if err != nil {
		rt.logger.Printf("record failed id=%s: %v", job.ID, err)
		if talkState, ok := rt.lookupTalkJobState(job.ID); ok {
			if notifyErr := rt.notifyTalkFailed(talkState); notifyErr != nil {
				rt.logger.Printf("talk failed callback failed id=%s: %v", job.ID, notifyErr)
			}
		}
		if updateErr := rt.store.MarkRecordFailed(context.Background(), job.ID, err.Error(), result, finishedAt); updateErr != nil {
			rt.logger.Printf("record fail update failed id=%s: %v", job.ID, updateErr)
		}
		return
	}
	canonicalRunPath, promoteErr := promoteRunBundle(rt.cfg.WorkRoot, result.ArtifactRunPath, job.ID)
	if promoteErr != nil {
		rt.logger.Printf("record promote failed id=%s run=%s: %v", job.ID, result.ArtifactRunPath, promoteErr)
		if failErr := rt.store.MarkRecordFailed(context.Background(), job.ID, promoteErr.Error(), result, finishedAt); failErr != nil {
			rt.logger.Printf("record promote failure update failed id=%s: %v", job.ID, failErr)
		}
		return
	}
	if updateErr := rt.store.UpdateRecordOutcome(context.Background(), job.ID, result, canonicalRunPath, finishedAt); updateErr != nil {
		rt.logger.Printf("record outcome update failed id=%s: %v", job.ID, updateErr)
		if failErr := rt.store.MarkRecordFailed(context.Background(), job.ID, updateErr.Error(), result, finishedAt); failErr != nil {
			rt.logger.Printf("record outcome failure update failed id=%s: %v", job.ID, failErr)
		}
		return
	}
	// Tell spreed the recording stopped. Status only — the meeting itself
	// goes to Nextcloud as the published .opus, never through Talk's
	// recording store (D-551). Retried with backoff but never fails the
	// record stage: the recording is already safe in the canonical run
	// bundle, so a Nextcloud hiccup must not strand it (D-352).
	if talkState, ok := rt.lookupTalkJobState(job.ID); ok {
		rt.reportTalkRecordingStopped(job.ID, talkState)
	}
	if err := rt.enqueueBuildJob(job.ID, job.CurrentAttemptNumber, canonicalRunPath, result.ArtifactRunPath, finishedAt); err != nil {
		rt.logger.Printf("build queue update failed id=%s: %v", job.ID, err)
		if updateErr := rt.store.MarkBuildFailed(context.Background(), job.ID, "", err.Error(), finishedAt); updateErr != nil {
			rt.logger.Printf("build queue failure update failed id=%s: %v", job.ID, updateErr)
		}
		return
	}
	rt.logger.Printf("record succeeded id=%s attempt_run=%s canonical_run=%s build_queued_at=%s stop_reason=%s", job.ID, result.ArtifactRunPath, canonicalRunPath, finishedAt, strings.TrimSpace(result.StopReason))
}

func (rt *Runtime) acceptRecordJob(ctx context.Context, provider, requestBody string, req TriggerRequest) (createJobResponse, error) {
	resp, startRecord, err := rt.prepareRecordJob(ctx, provider, requestBody, req)
	if err != nil {
		return createJobResponse{}, err
	}
	startRecord()
	return resp, nil
}

// prepareRecordJob reserves a record slot and persists the queued job but
// does not spawn the record goroutine: the returned start func does. Callers
// that must attach state the goroutine's deferred cleanup depends on — the
// Talk room binding, keyed by job ID — do so between prepare and start, so a
// fast-failing job can never race past a not-yet-bound room entry (D-364).
func (rt *Runtime) prepareRecordJob(ctx context.Context, provider, requestBody string, req TriggerRequest) (createJobResponse, func(), error) {
	select {
	case rt.recordSlots <- struct{}{}:
	default:
		return createJobResponse{}, nil, errRecordBusy
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
	if err := rt.store.InsertQueuedJob(ctx, job); err != nil {
		<-rt.recordSlots
		return createJobResponse{}, nil, fmt.Errorf("create job: %w", err)
	}

	startRecord := func() {
		rt.recordWG.Add(1)
		go rt.runRecordJob(job, req)
	}
	return createJobResponse{ID: jobID}, startRecord, nil
}

// WaitForRecordJobs blocks until in-flight record jobs — including their
// post-record bookkeeping (bundle promotion, Talk callbacks, build enqueue)
// — have finished, or the timeout elapses. Operator shutdown calls this so
// the process does not exit, SIGKILLing recorder children mid-compose, while
// a recording is still being finalized (D-350).
func (rt *Runtime) WaitForRecordJobs(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		rt.recordWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
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
	db                   *sql.DB
	stateChangePublisher stateChangePublisher
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
	// ArtifactOpusPath is the canonical portable meeting, current/<id>.opus,
	// promoted from the attempt the seal stage sealed. ArtifactOpusSHA256 is
	// that file's digest, which the publish worker re-checks before delivering
	// and the sink re-checks before committing the asset (D-583).
	ArtifactOpusPath    *string `json:"artifact_opus_path"`
	ArtifactOpusSHA256  *string `json:"artifact_opus_sha256"`
	ArtifactSitePath    *string `json:"artifact_site_path"`
	Error               *string `json:"error"`
	StopReason          *string `json:"stop_reason"`
	StopRequestedAt     *string `json:"stop_requested_at"`
	StopSignalSentAt    *string `json:"stop_signal_sent_at"`
	RecordExitCode      *int    `json:"record_exit_code"`
	RecordStopDetail    *string `json:"record_stop_detail"`
	CreatedAt           string  `json:"created_at"`
	UpdatedAt           string  `json:"updated_at"`
	RecordQueuedAt      *string `json:"record_queued_at"`
	RecordStartedAt     *string `json:"record_started_at"`
	RecordFinishedAt    *string `json:"record_finished_at"`
	BuildQueuedAt       *string `json:"build_queued_at"`
	BuildRetryNotBefore *string `json:"build_retry_not_before"`
	BuildDeferralCount  int     `json:"build_deferral_count"`
	BuildStartedAt      *string `json:"build_started_at"`
	BuildFinishedAt     *string `json:"build_finished_at"`
	SealQueuedAt        *string `json:"seal_queued_at"`
	SealStartedAt       *string `json:"seal_started_at"`
	SealFinishedAt      *string `json:"seal_finished_at"`
	PublishQueuedAt     *string `json:"publish_queued_at"`
	PublishStartedAt    *string `json:"publish_started_at"`
	PublishFinishedAt   *string `json:"publish_finished_at"`
	InterruptedAt       *string `json:"interrupted_at"`
	CompletedAt         *string `json:"completed_at"`
	// TalkBinding is the persisted Talk room binding (backend URL, token,
	// owner, actor) for jobs started through the Talk recording backend. It
	// is internal plumbing for crash-safe delivery, not API surface.
	TalkBinding *string `json:"-"`
	// TalkStoppedAt records that spreed acknowledged the stopped callback for
	// this recording (D-551 repointed it from the retired Talk upload).
	TalkStoppedAt *string `json:"talk_stopped_at"`
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
	s.emitStateChange(ctx, "job.created", job.ID, 1)
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
	s.emitStateChange(ctx, "job.updated", id, attemptNumber)
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
	s.emitStateChange(ctx, "attempt.updated", id, attemptNumber)
	return nil
}

// UpdateRecordOutcome persists the record stop metadata and — crucially for
// crash/delivery durability (D-352) — the canonical run pointer as soon as
// the promoted bundle exists. The pointer used to be written only when the
// build was queued, so any failure between promotion and build-queue left a
// fully recorded job that rerun rejected (409) forever.
func (s *Store) UpdateRecordOutcome(ctx context.Context, id string, result recordResult, canonicalRunPath, updatedAt string) error {
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
    artifact_run_path = ?,
    updated_at = ?
WHERE id = ?`, nullableString(result.StopReason), intOrNil(result.ExitCode), nullableString(result.StopDetail), canonicalRunPath, updatedAt, id)
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
    artifact_run_path = ?,
    updated_at = ?
WHERE job_id = ? AND attempt_number = ?`, nullableString(result.StopReason), intOrNil(result.ExitCode), nullableString(result.StopDetail), nullableString(result.ArtifactRunPath), updatedAt, id, attemptNumber); err != nil {
		return fmt.Errorf("update attempt record outcome: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit record outcome update: %w", err)
	}
	s.emitStateChange(ctx, "attempt.updated", id, attemptNumber)
	return nil
}

func (s *Store) MarkRecordSucceededTerminal(ctx context.Context, id, canonicalRunPath, attemptRunPath string, result recordResult, finishedAt string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin record success update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?,
    artifact_run_path = ?, artifact_meeting_path = NULL, artifact_site_path = NULL,
    error = NULL,
    stop_reason = ?, record_exit_code = ?, record_stop_detail = ?,
    updated_at = ?, record_finished_at = ?, completed_at = ?,
    build_queued_at = NULL, build_started_at = NULL, build_finished_at = NULL,
    publish_queued_at = NULL, publish_started_at = NULL, publish_finished_at = NULL,
    interrupted_at = NULL
WHERE id = ?`,
		"done", "succeeded",
		canonicalRunPath,
		nullableString(result.StopReason), intOrNil(result.ExitCode), nullableString(result.StopDetail),
		finishedAt, finishedAt, finishedAt,
		id)
	if err != nil {
		return fmt.Errorf("update record success: %w", err)
	}
	attemptNumber, err := currentAttemptNumberTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE job_attempts
SET stage = ?, state = ?,
    artifact_run_path = ?,
    error = NULL,
    stop_reason = ?, record_exit_code = ?, record_stop_detail = ?,
    updated_at = ?, record_finished_at = ?, completed_at = ?,
    interrupted_at = NULL
WHERE job_id = ? AND attempt_number = ?`,
		"done", "succeeded",
		attemptRunPath,
		nullableString(result.StopReason), intOrNil(result.ExitCode), nullableString(result.StopDetail),
		finishedAt, finishedAt, finishedAt,
		id, attemptNumber); err != nil {
		return fmt.Errorf("update attempt record success: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit record success update: %w", err)
	}
	s.emitStateChange(ctx, "job.updated", id, attemptNumber)
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
SET stage = ?, state = ?, artifact_run_path = ?, error = ?, stop_reason = ?, record_exit_code = ?, record_stop_detail = ?, updated_at = ?, record_finished_at = ?, completed_at = ?
WHERE job_id = ? AND attempt_number = ?`, "done", "failed", nullableString(result.ArtifactRunPath), strings.TrimSpace(errText), nullableString(result.StopReason), intOrNil(result.ExitCode), nullableString(firstNonEmpty(result.StopDetail, errText)), finishedAt, finishedAt, finishedAt, id, attemptNumber); err != nil {
		return fmt.Errorf("update attempt record failure: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit record failure update: %w", err)
	}
	s.emitStateChange(ctx, "attempt.updated", id, attemptNumber)
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
       artifact_run_path, artifact_meeting_path, artifact_opus_path, artifact_opus_sha256, artifact_site_path, error,
       stop_reason, stop_requested_at, stop_signal_sent_at, record_exit_code, record_stop_detail,
       created_at, updated_at,
       record_queued_at, record_started_at, record_finished_at,
       build_queued_at, build_retry_not_before, build_deferral_count, build_started_at, build_finished_at,
       seal_queued_at, seal_started_at, seal_finished_at,
       publish_queued_at, publish_started_at, publish_finished_at,
       interrupted_at, completed_at,
       talk_binding, talk_stopped_at
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
       artifact_run_path, artifact_meeting_path, artifact_opus_path, artifact_opus_sha256, artifact_site_path, error,
       stop_reason, stop_requested_at, stop_signal_sent_at, record_exit_code, record_stop_detail,
       created_at, updated_at,
       record_queued_at, record_started_at, record_finished_at,
       build_queued_at, build_retry_not_before, build_deferral_count, build_started_at, build_finished_at,
       seal_queued_at, seal_started_at, seal_finished_at,
       publish_queued_at, publish_started_at, publish_finished_at,
       interrupted_at, completed_at,
       talk_binding, talk_stopped_at
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
	var artifactOpusPath sql.NullString
	var artifactOpusSHA256 sql.NullString
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
	var buildRetryNotBefore sql.NullString
	var buildStartedAt sql.NullString
	var buildFinishedAt sql.NullString
	var sealQueuedAt sql.NullString
	var sealStartedAt sql.NullString
	var sealFinishedAt sql.NullString
	var publishQueuedAt sql.NullString
	var publishStartedAt sql.NullString
	var publishFinishedAt sql.NullString
	var interruptedAt sql.NullString
	var completedAt sql.NullString
	var talkBinding sql.NullString
	var talkStoppedAt sql.NullString

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
		&artifactOpusPath,
		&artifactOpusSHA256,
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
		&buildRetryNotBefore,
		&job.BuildDeferralCount,
		&buildStartedAt,
		&buildFinishedAt,
		&sealQueuedAt,
		&sealStartedAt,
		&sealFinishedAt,
		&publishQueuedAt,
		&publishStartedAt,
		&publishFinishedAt,
		&interruptedAt,
		&completedAt,
		&talkBinding,
		&talkStoppedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Job{}, err
		}
		return Job{}, fmt.Errorf("scan job: %w", err)
	}

	job.ArtifactRunPath = nullableStringPtr(artifactRunPath)
	job.ArtifactMeetingPath = nullableStringPtr(artifactMeetingPath)
	job.ArtifactOpusPath = nullableStringPtr(artifactOpusPath)
	job.ArtifactOpusSHA256 = nullableStringPtr(artifactOpusSHA256)
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
	job.BuildRetryNotBefore = nullableStringPtr(buildRetryNotBefore)
	job.BuildStartedAt = nullableStringPtr(buildStartedAt)
	job.BuildFinishedAt = nullableStringPtr(buildFinishedAt)
	job.SealQueuedAt = nullableStringPtr(sealQueuedAt)
	job.SealStartedAt = nullableStringPtr(sealStartedAt)
	job.SealFinishedAt = nullableStringPtr(sealFinishedAt)
	job.PublishQueuedAt = nullableStringPtr(publishQueuedAt)
	job.PublishStartedAt = nullableStringPtr(publishStartedAt)
	job.PublishFinishedAt = nullableStringPtr(publishFinishedAt)
	job.InterruptedAt = nullableStringPtr(interruptedAt)
	job.CompletedAt = nullableStringPtr(completedAt)
	job.TalkBinding = nullableStringPtr(talkBinding)
	job.TalkStoppedAt = nullableStringPtr(talkStoppedAt)
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

// sortableUTCLayout keeps every persisted timestamp at the same precision.
// RFC3339Nano trims trailing fractional zeros, so its strings are not ordered
// chronologically within the same second (for example, .85Z sorts before .8Z).
// Fixed-width timestamps remain valid RFC3339Nano while also sorting correctly.
const sortableUTCLayout = "2006-01-02T15:04:05.000000000Z"

func formatUTCString(value time.Time) string {
	return value.UTC().Format(sortableUTCLayout)
}

func nowUTCString() string {
	return formatUTCString(time.Now())
}
