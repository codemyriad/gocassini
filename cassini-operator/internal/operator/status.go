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
}

type statusSTT struct {
	Device       string `json:"device"`
	ModelID      string `json:"model_id,omitempty"`
	DeviceUsable bool   `json:"device_usable"`
	Detail       string `json:"detail,omitempty"`
}

type statusTalk struct {
	SecretConfigured             bool `json:"secret_configured"`
	BackendURLOverrideConfigured bool `json:"backend_url_override_configured"`
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
			SecretConfigured:             strings.TrimSpace(rt.cfg.TalkSharedSecret) != "",
			BackendURLOverrideConfigured: strings.TrimSpace(rt.cfg.TalkBackendURL) != "",
		},
		Storage: statusStorage{
			WorkRoot: storagePathCheck(rt.cfg.WorkRoot),
			SiteRoot: storagePathCheck(rt.cfg.SiteRoot),
		},
	}
	resp.ImageTag = resp.Version
	resp.DB = statusCheck{OK: true, Path: rt.cfg.DBPath}
	if err := rt.store.Ping(r.Context()); err != nil {
		resp.DB.OK = false
		resp.DB.Detail = err.Error()
	}
	resp.OK = resp.STT.DeviceUsable && resp.DB.OK && resp.Storage.WorkRoot.OK && resp.Storage.SiteRoot.OK

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
