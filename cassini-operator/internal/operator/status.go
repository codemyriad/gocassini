package operator

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// GET /status is the admin doctor endpoint (D-363): it answers "is this
// actually installed, proxied, configured, and ready for Talk" without shell
// access. Through the AppAPI proxy it is ADMIN-ACL'd by the
// ^operator\/status\/?$ route in appinfo/info.xml. It reports version/image
// tag, CPU-vs-CUDA mode and whether the device is actually usable, Talk
// backend config presence (booleans only — never secret values), and
// DB/storage health. All checks are cheap: no transcription, no doctor
// subprocess.

const (
	envSTTDevice = "CASSINI_STT_DEVICE"
	envSTTModel  = "CASSINI_STT_MODEL"

	envTalkSignalingInternalSecret = "CASSINI_TALK_SIGNALING_INTERNAL_SECRET"

	// signalingInternalSecretHint is the single actionable message shown to
	// admins when the Talk signaling internal secret is missing. It is the one
	// required input for invisible HPB-internal recording — it cannot be
	// auto-provisioned (unlike the recording secret), because an invisible
	// internal signaling client authenticates with the server's shared
	// internalsecret. Surfaced both at startup (run.go) and in /status so the
	// admin learns of it before a recording fails.
	signalingInternalSecretHint = "Talk recording needs CASSINI_TALK_SIGNALING_INTERNAL_SECRET " +
		"(the Talk signaling server's [clients] internalsecret). On Nextcloud AIO, read it with " +
		"`docker exec nextcloud-aio-talk printenv INTERNAL_SECRET`; on a standalone HPB it is the " +
		"[clients] internalsecret in the signaling server config. Set it in the External Apps deploy " +
		"options, or with `occ app_api:app:register <app> <daemon> --env " +
		"CASSINI_TALK_SIGNALING_INTERNAL_SECRET=<value>`. See docs/exapp-install.md."

	// recordHealthProbeTTL/Timeout throttle the deep /healthz?check=record
	// doctor exec: the endpoint is unauthenticated, so probes are
	// singleflighted, cached briefly, and exec-bounded (D-376).
	recordHealthProbeTTL     = 15 * time.Second
	recordHealthProbeTimeout = 30 * time.Second
)

type statusResponse struct {
	OK bool `json:"ok"`
	// Version is APP_VERSION as injected by AppAPI from the manifest. CI
	// enforces that releases pin info.xml's <image-tag> to the same value,
	// so it doubles as the image tag.
	Version     string        `json:"version,omitempty"`
	ImageTag    string        `json:"image_tag,omitempty"`
	VCSRevision string        `json:"vcs_revision,omitempty"`
	STT         statusSTT     `json:"stt"`
	Talk        statusTalk    `json:"talk"`
	DB          statusCheck   `json:"db"`
	Storage     statusStorage `json:"storage"`
	// RecordingsAccess reports whether the Nextcloud substrate that makes
	// recordings per-participant readable is actually there (D-554). It is the
	// last recorded outcome of provisioning, not a live probe.
	RecordingsAccess statusRecordingsAccess `json:"recordings_access"`
}

// statusRecordingsAccess answers "can this deployment actually restrict a
// recording to its participants". A missing group folder does not stop the
// operator answering requests — it stops anyone seeing their recordings — so
// it has to be asked for rather than noticed.
type statusRecordingsAccess struct {
	// Applicable is false for a standalone operator, which serves no recordings
	// from Nextcloud Files and cannot be broken for want of a substrate.
	Applicable bool `json:"applicable"`
	// PublishSink is reported because an ExApp pinned to the local sink keeps
	// its archive off Nextcloud, which makes a provisioned substrate inert
	// rather than wrong.
	PublishSink string `json:"publish_sink,omitempty"`
	// State is provisioned / degraded / unavailable / not_applicable / unknown
	// (D-585). OK is kept alongside it, derived, so anything that already reads
	// the boolean keeps working.
	State string `json:"state"`
	OK    bool   `json:"ok"`
	// Step names the provisioning step that stopped, in a stable machine-readable
	// form a monitor or a test can key on: "app_missing:group_everyone",
	// "administrator", "mount_mapping:everyone".
	Step   string `json:"step,omitempty"`
	Detail string `json:"detail,omitempty"`
	// AdminUser is the account provisioning resolved and acted as. Its absence
	// is itself the diagnosis when Step is "administrator".
	AdminUser string `json:"admin_user,omitempty"`
	// Prerequisites reports the native Nextcloud apps an ExApp cannot install
	// for itself, so a missing one is named rather than inferred.
	Prerequisites []statusPrerequisite `json:"prerequisites,omitempty"`
	CheckedAt     string               `json:"checked_at,omitempty"`
}

// statusPrerequisite is one native app the recordings substrate depends on.
type statusPrerequisite struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

type statusSTT struct {
	Device       string `json:"device"`
	ModelID      string `json:"model_id,omitempty"`
	DeviceUsable bool   `json:"device_usable"`
	Detail       string `json:"detail,omitempty"`
}

type statusTalk struct {
	SecretConfigured                  bool `json:"secret_configured"`
	SignalingInternalSecretConfigured bool `json:"signaling_internal_secret_configured"`
	BackendURLOverrideConfigured      bool `json:"backend_url_override_configured"`
	// SecretSource is where the recording secret came from ("env" | "generated"
	// | "unset"), so an admin can tell a self-generated secret (D-447) from one
	// they injected. Never the secret value itself.
	SecretSource string `json:"secret_source,omitempty"`
	// RecordingBackendURL is the recording_servers server value to register in
	// spreed (the AppAPI proxy base for this ExApp), derived from NEXTCLOUD_URL.
	// Not a secret; empty on standalone/dev deploys where it cannot be derived.
	RecordingBackendURL string `json:"recording_backend_url,omitempty"`
	// SignalingInternalSecretHint is set only when the internal secret is
	// missing: an actionable message telling the admin how to find and set it.
	// Empty when configured, so a healthy status stays quiet.
	SignalingInternalSecretHint string `json:"signaling_internal_secret_hint,omitempty"`
}

// signalingInternalSecretConfigured reports whether the Talk signaling internal
// secret (required for invisible HPB-internal recording) is set.
func signalingInternalSecretConfigured() bool {
	return strings.TrimSpace(os.Getenv(envTalkSignalingInternalSecret)) != ""
}

type statusCheck struct {
	OK     bool   `json:"ok"`
	Path   string `json:"path,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type statusStorage struct {
	WorkRoot statusCheck `json:"work_root"`
	SiteRoot statusCheck `json:"site_root"`
}

func (rt *Runtime) statusHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/status" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}

	usable, detail := rt.computeProbe()
	resp := statusResponse{
		Version:     strings.TrimSpace(os.Getenv(envAppVersion)),
		VCSRevision: buildVCSRevision(),
		STT: statusSTT{
			Device:       configuredSTTDevice(),
			ModelID:      strings.TrimSpace(os.Getenv(envSTTModel)),
			DeviceUsable: usable,
			Detail:       detail,
		},
		Talk: statusTalk{
			SecretConfigured:                  strings.TrimSpace(rt.cfg.TalkSharedSecret) != "",
			SignalingInternalSecretConfigured: signalingInternalSecretConfigured(),
			BackendURLOverrideConfigured:      strings.TrimSpace(rt.cfg.TalkBackendURL) != "",
			SecretSource:                      rt.cfg.TalkSecretSource,
			RecordingBackendURL:               rt.cfg.TalkRecordingBackendURL,
		},
		Storage: statusStorage{
			WorkRoot: storagePathCheck(rt.cfg.WorkRoot),
			SiteRoot: storagePathCheck(rt.cfg.SiteRoot),
		},
		RecordingsAccess: ncAccessSubstrate.snapshot(publishSinkNameOrDefault(rt.cfg.PublishSink)),
	}
	if !resp.Talk.SignalingInternalSecretConfigured {
		resp.Talk.SignalingInternalSecretHint = signalingInternalSecretHint
	}
	resp.ImageTag = resp.Version
	resp.DB = statusCheck{OK: true, Path: rt.cfg.DBPath}
	if err := rt.store.Ping(r.Context()); err != nil {
		resp.DB.OK = false
		resp.DB.Detail = err.Error()
	}
	// The substrate counts toward health only where it applies. An ExApp whose
	// group folder does not exist is genuinely broken and should say 503; a
	// standalone dev operator must not go 503 for a Nextcloud it never had.
	resp.OK = resp.STT.DeviceUsable && resp.DB.OK && resp.Storage.WorkRoot.OK &&
		resp.Storage.SiteRoot.OK && resp.RecordingsAccess.OK

	status := http.StatusOK
	if !resp.OK {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, resp)
}

// Ping verifies the job store answers queries.
func (s *Store) Ping(ctx context.Context) error {
	var one int
	if err := s.db.QueryRowContext(ctx, `SELECT 1`).Scan(&one); err != nil {
		return fmt.Errorf("job store query: %w", err)
	}
	return nil
}

// configuredSTTDevice mirrors how the recorder's transcribe stack consumes
// CASSINI_STT_DEVICE (transcribe.defaultDevice: unset means cpu; "auto"
// resolves to cpu as well).
func configuredSTTDevice() string {
	if v := strings.TrimSpace(os.Getenv(envSTTDevice)); v != "" {
		return v
	}
	return "cpu"
}

// probeComputeDevice reports whether the configured STT device is usable plus
// an actionable detail message. The CUDA probe is cheap — driver/device-node
// visibility and an optional `nvidia-smi -L` — never a model load or a
// transcription (D-363).
func probeComputeDevice(device string) (bool, string) {
	switch strings.ToLower(strings.TrimSpace(device)) {
	case "", "cpu", "auto":
		// The transcribe stack resolves "auto" to cpu; nothing to probe.
		return true, "cpu inference (no GPU required)"
	case "cuda":
		return probeCUDADevice()
	default:
		return false, fmt.Sprintf("unknown CASSINI_STT_DEVICE %q (expected cpu or cuda)", device)
	}
}

func probeCUDADevice() (bool, string) {
	_, driverErr := os.Stat("/proc/driver/nvidia/version")
	deviceNodes, _ := filepath.Glob("/dev/nvidia*")
	if driverErr != nil && len(deviceNodes) == 0 {
		return false, "CASSINI_STT_DEVICE=cuda but no NVIDIA driver (/proc/driver/nvidia/version) or device nodes (/dev/nvidia*) are visible — run the container with GPU access (docker --gpus all, or an AppAPI deploy daemon with the nvidia runtime), or set CASSINI_STT_DEVICE=cpu"
	}
	smi, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return true, "cuda: NVIDIA driver visible (nvidia-smi not present to verify further)"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, smi, "-L").Output()
	if err != nil {
		return false, fmt.Sprintf("CASSINI_STT_DEVICE=cuda but `nvidia-smi -L` failed: %v — the GPU is not usable from this container; fix GPU access or set CASSINI_STT_DEVICE=cpu", err)
	}
	gpus := strings.TrimSpace(string(out))
	if gpus == "" {
		return false, "CASSINI_STT_DEVICE=cuda but `nvidia-smi -L` lists no GPUs — the GPU is not usable from this container; fix GPU access or set CASSINI_STT_DEVICE=cpu"
	}
	if line, _, found := strings.Cut(gpus, "\n"); found {
		gpus = line + " (+more)"
	}
	return true, "cuda: " + gpus
}

// logComputeDeviceStatus logs the STT device once at startup, loudly when the
// configured device is unusable: a CUDA image without GPU access must fail
// visibly instead of silently transcribing on CPU (D-363).
func (rt *Runtime) logComputeDeviceStatus() {
	device := configuredSTTDevice()
	usable, detail := rt.computeProbe()
	if usable {
		rt.logger.Printf("stt_device -> %s (%s)", device, detail)
		return
	}
	rt.logger.Printf("ERROR: stt_device %s is not usable: %s", device, detail)
}

// storagePathCheck verifies a data root exists (creating it if needed) and is
// writable, via a throwaway probe file.
func storagePathCheck(path string) statusCheck {
	check := statusCheck{Path: path}
	if strings.TrimSpace(path) == "" {
		check.Detail = "path is not configured"
		return check
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		check.Detail = err.Error()
		return check
	}
	probe := filepath.Join(path, ".cassini-status-probe")
	if err := os.WriteFile(probe, []byte("ok\n"), 0o644); err != nil {
		check.Detail = fmt.Sprintf("not writable: %v", err)
		return check
	}
	_ = os.Remove(probe)
	check.OK = true
	return check
}

// buildVCSRevision returns the embedded git revision when the binary was
// built from a checkout, or "" (e.g. tests).
func buildVCSRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			return setting.Value
		}
	}
	return ""
}

// ttlProbe coalesces concurrent runs of an expensive check (singleflight) and
// caches the result for a short TTL. The deep /healthz?check=record path uses
// it so the unauthenticated endpoint cannot fan out doctor subprocesses
// (D-376).
type ttlProbe struct {
	ttl time.Duration
	run func() error

	mu       sync.Mutex
	inflight chan struct{}
	at       time.Time
	err      error
}

func newTTLProbe(ttl time.Duration, run func() error) *ttlProbe {
	return &ttlProbe{ttl: ttl, run: run}
}

// check returns the cached result while fresh; otherwise exactly one caller
// runs the probe and concurrent callers wait for that result.
func (p *ttlProbe) check() error {
	p.mu.Lock()
	for {
		if !p.at.IsZero() && time.Since(p.at) < p.ttl {
			err := p.err
			p.mu.Unlock()
			return err
		}
		if p.inflight == nil {
			break
		}
		wait := p.inflight
		p.mu.Unlock()
		<-wait
		p.mu.Lock()
	}
	done := make(chan struct{})
	p.inflight = done
	p.mu.Unlock()

	err := p.run()

	p.mu.Lock()
	p.err = err
	p.at = time.Now()
	p.inflight = nil
	close(done)
	p.mu.Unlock()
	return err
}
