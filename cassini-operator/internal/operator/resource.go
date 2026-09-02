package operator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// The resource governor sizes each build subprocess so it never starves or OOMs
// the host. The deployed ExApp runs uncapped (cgroup cpu.max=max, memory.max=max)
// on the SAME host as Nextcloud + Talk, so a transcription that grabs every core
// or all of RAM degrades the call server. On GPU hosts the shared VRAM can OOM
// other services.
//
// The governor first resolves the device a build will run on — CUDA when the
// image carries the CUDA runtime and an NVIDIA device is visible, CPU otherwise
// — and then sizes the build for it: CUDA is pinned to one host thread and
// needs a trustworthy VRAM reading, CPU gets the core budget minus a reserve.
// Either way it refuses to start while free RAM is below the floor that
// device/model pair needs, and defers transient pressure rather than launching
// into a near-OOM host.
//
// Falling back to CPU is a deliberate, reported decision, not a silent one
// (D-702): the resolved device reaches /status, Cassini Admin and the build log
// before any audio is decoded. A device the administrator asked for explicitly
// is never swapped underneath them — device_override=cuda on a host with no
// usable GPU is an error, not a CPU run.

// Probes are package vars so tests can inject deterministic values.
var (
	probeOnlineCPUs   = detectOnlineCPUs
	probeAvailableMem = detectAvailableMemMB
	probeGPUFreeMB    = detectGPUFreeMB
)

type resourceLimits struct {
	cpuReserve       int           // cores to leave free for the rest of the host
	minFreeMemMB     int           // do not start a CUDA build below this free RAM
	cpuMemHeadroomMB int           // free RAM a CPU build needs on top of its model weights
	gpuMinFreeMB     int           // defer a CUDA build below this free VRAM
	memWaitMax       time.Duration // bound on how long to wait for RAM to free up
	memPoll          time.Duration
}

func resourceLimitsFromEnv() resourceLimits {
	cpus := probeOnlineCPUs()
	return resourceLimits{
		cpuReserve: envIntDefault("CASSINI_BUILD_CPU_RESERVE", defaultCPUReserve(cpus)),
		// Full-run measurements peaked at ~5.2GiB cgroup RAM and 4.63GiB
		// VRAM. Keep conservative launch floors above those working sets so
		// the build can load without consuming the host's last reserve.
		minFreeMemMB: envIntDefault("CASSINI_BUILD_MIN_FREE_MEM_MB", 6144),
		// A CPU build's working set is dominated by the model it loads, which
		// differs by an order of magnitude across the quality tiers, so its
		// floor is derived per model (minFreeMemForBuild) rather than fixed:
		// the model's measured peak plus this margin for the rest of the host.
		cpuMemHeadroomMB: envIntDefault("CASSINI_BUILD_CPU_MEM_HEADROOM_MB", 1024),
		gpuMinFreeMB:     envIntDefault("CASSINI_GPU_MIN_FREE_MB", 5500),
		memWaitMax:       time.Duration(envIntDefault("CASSINI_BUILD_MEM_WAIT_SECS", 300)) * time.Second,
		memPoll:          3 * time.Second,
	}
}

// minFreeMemForBuild is the free-RAM floor for a build of model on device.
// CUDA keeps the measured full-run floor. A CPU build holds the recognizer in
// host RAM instead of VRAM, so its floor is that model's measured peak plus a
// margin: 1.5GiB for the 110M CTC model, 2.75GiB for 0.6B int8, 4.5GiB for
// 0.6B fp32. A 4-core/8GiB host therefore runs the fast and balanced tiers and
// declines "best" with an actionable message instead of OOMing Talk.
func (l resourceLimits) minFreeMemForBuild(device, model string) int {
	if isCUDA(device) {
		return l.minFreeMemMB
	}
	return modelBuildPeakMB(model) + l.cpuMemHeadroomMB
}

// defaultCPUReserve leaves ~a quarter of the cores (at least one) free so the
// build never pins the whole box.
func defaultCPUReserve(cpus int) int {
	r := cpus / 4
	if r < 1 {
		r = 1
	}
	return r
}

// threadBudget is the intra-op thread count a build may use: available cores
// minus the reserve, clamped to [1,16] (sherpa's useful intra-op ceiling).
func (l resourceLimits) threadBudget() int {
	n := probeOnlineCPUs() - l.cpuReserve
	switch {
	case n < 1:
		return 1
	case n > 16:
		return 16
	default:
		return n
	}
}

// resourceUnavailableError marks resource admission failures. The build worker
// either restores transient capacity pressure to the durable queue or preserves
// a permanent/exhausted condition in a visible, manually rerunnable blocked
// state instead of turning a valid recording into a generic failure.
type resourceUnavailableError struct {
	resource string
	detail   string
	// permanent means waiting cannot make this runtime eligible (for example,
	// the portable image does not contain CUDA libraries). The worker moves the
	// job to build/blocked immediately instead of scheduling retries forever.
	permanent bool
}

func (e *resourceUnavailableError) Error() string {
	return fmt.Sprintf("resource governor: %s unavailable: %s", e.resource, e.detail)
}

// admitModelForDevice returns the model an admitted build will load: the model
// of the quality tier on the resolved device. Every tier can run on every
// image, because a model the image does not carry is downloaded once into the
// persistent cache (D-704).
func (rt *Runtime) admitModelForDevice(settings STTSettings, device string) (string, error) {
	return settings.modelForDevice(device), nil
}

// modelNeedsDownload reports whether a build must fetch this model before it
// can transcribe. Each image carries the models for the device it serves, so a
// tier outside that set arrives by download. The operator only reports the
// wait: the recorder performs the download, and fails with an actionable error
// when the host has no network egress.
func (rt *Runtime) modelNeedsDownload(model string) bool {
	present := func(root string) bool {
		if strings.TrimSpace(root) == "" {
			return false
		}
		entries, err := os.ReadDir(filepath.Join(root, "models", model))
		return err == nil && len(entries) > 0
	}
	return !present(rt.cfg.BundledModelRoot) && !present(rt.cfg.ModelCacheRoot)
}

// applyToEnv injects the STT execution policy for the device resolveBuildDevice
// admitted. The device is always written explicitly so the child process cannot
// re-detect a different one between admission and launch. CUDA builds are
// pinned to one host thread and require a trustworthy VRAM reading at or above
// the configured floor; CPU builds get the host thread budget and no VRAM probe.
func (l resourceLimits) applyToEnv(env []string, device, model string) ([]string, error) {
	// The recorder intentionally exposes a stream-concurrency escape hatch for
	// manual benchmarking. It is not safe in the operator: every worker owns a
	// separate recognizer/model allocation, and concurrent CUDA provider setup
	// has crashed in practice. Override even a stale inherited value.
	env = setEnvKey(env, envSTTStreamConcurrency, "1")

	// Pin the model the governor sized this build for. The recorder would
	// derive the same one from the tier and device, but deriving it twice makes
	// admission and execution independent copies of one policy: pinning it here
	// makes them identical by construction, so a future edit to either mapping
	// cannot admit a build for one model and then load a larger one.
	if strings.TrimSpace(model) != "" {
		env = setEnvKey(env, envSTTModel, model)
	}

	if !isCUDA(device) {
		env = setEnvKey(env, envSTTDevice, "cpu")
		// Cores minus the reserve: a CPU transcription is the one workload that
		// would otherwise pin every core on the host running Nextcloud + Talk.
		env = setEnvKey(env, envSTTNumThreads, strconv.Itoa(l.threadBudget()))
		return env, nil
	}

	env = setEnvKey(env, envSTTNumThreads, "1")
	env = setEnvKey(env, envSTTDevice, "cuda")
	free, ok := probeGPUFreeMB()
	if !ok {
		return nil, &resourceUnavailableError{
			resource: "GPU memory",
			detail:   "free VRAM could not be measured; refusing an unbounded CUDA launch",
		}
	}
	if free < l.gpuMinFreeMB {
		return nil, &resourceUnavailableError{
			resource: "GPU memory",
			detail:   fmt.Sprintf("free %dMiB is below the %dMiB floor", free, l.gpuMinFreeMB),
		}
	}
	return env, nil
}

// waitForMemory blocks (bounded) until free RAM clears the floor, so a build is
// never spawned into a near-OOM host during a transient memory spike. After
// memWaitMax it returns a transient capacity error so the worker can defer the
// job without launching or terminally failing it.
func (l resourceLimits) waitForMemory(ctx context.Context, floorMB int, logf func(format string, v ...any)) error {
	if floorMB <= 0 {
		return nil
	}
	deadline := time.Now().Add(l.memWaitMax)
	warned := false
	for {
		free := probeAvailableMem()
		if free >= floorMB {
			return nil
		}
		if time.Now().After(deadline) {
			return &resourceUnavailableError{
				resource: "host memory",
				detail:   fmt.Sprintf("free %dMiB is below the %dMiB floor after waiting %s", free, floorMB, l.memWaitMax),
			}
		}
		if !warned {
			logf("resource governor: free mem %dMiB < %dMiB; deferring build start", free, floorMB)
			warned = true
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(l.memPoll):
		}
	}
}

// resolveBuildDevice decides which device this build runs on, separating
// permanent policy/runtime failures from transient device pressure before the
// worker waits for host RAM. The later applyToEnv call still takes a fresh
// free-VRAM snapshot immediately before launch.
//
// Auto (the default) prefers CUDA and settles for CPU rather than stranding the
// recording: a host with no GPU still gets its transcript, more slowly. An
// explicit override is honoured exactly or fails loudly — asking for CUDA on a
// box that cannot provide it must not quietly become a CPU run, because the
// throughput difference is what the administrator was choosing between.
func (rt *Runtime) resolveBuildDevice() (string, error) {
	return resolveDeviceForSettings(rt.currentSettings())
}

// resolveDeviceForSettings is resolveBuildDevice against one settings snapshot,
// so a caller that also needs the quality tier decides both from the same
// policy: reading rt.currentSettings() twice could straddle a PUT /settings and
// size a build for a device the tier was not chosen against.
func resolveDeviceForSettings(settings STTSettings) (string, error) {
	cudaCapable, cudaDetail := imageCUDACapability()
	switch override := strings.ToLower(strings.TrimSpace(settings.DeviceOverride)); override {
	case "cpu":
		return deviceCPU, nil
	case "cuda":
		if !cudaCapable {
			return "", &resourceUnavailableError{resource: "CUDA runtime", detail: cudaDetail, permanent: true}
		}
		if !probeNVIDIADevice() {
			return "", &resourceUnavailableError{
				resource: "CUDA device",
				detail:   "device_override=cuda but no NVIDIA device is currently visible; clear the override to transcribe on the CPU instead",
			}
		}
		return deviceCUDA, nil
	case "", "auto":
		if cudaCapable && probeNVIDIADevice() {
			return deviceCUDA, nil
		}
		return deviceCPU, nil
	default:
		return "", &resourceUnavailableError{
			resource:  "device policy",
			detail:    fmt.Sprintf("stored device_override=%q is not a device a build can run on", override),
			permanent: true,
		}
	}
}

// --- detection ---------------------------------------------------------------

// detectOnlineCPUs returns the CPU count this process may actually use: the
// cgroup CPU quota when one is set, else the online core count. (runtime.NumCPU
// reports host cores even inside a quota-limited container.)
func detectOnlineCPUs() int {
	if n, ok := cgroupCPUQuota(); ok {
		if n < 1 {
			n = 1
		}
		if n < runtime.NumCPU() {
			return n
		}
	}
	return runtime.NumCPU()
}

// cgroupCPUQuota reads a quota (in whole CPUs, rounded up) from cgroup v2 or v1.
// ok is false when there is no quota ("max" / unlimited / unreadable).
func cgroupCPUQuota() (int, bool) {
	if data, err := os.ReadFile("/sys/fs/cgroup/cpu.max"); err == nil { // cgroup v2
		return parseCPUMax(string(data))
	}
	// cgroup v1
	quota, ok1 := readIntFile("/sys/fs/cgroup/cpu/cpu.cfs_quota_us")
	period, ok2 := readIntFile("/sys/fs/cgroup/cpu/cpu.cfs_period_us")
	if ok1 && ok2 && quota > 0 && period > 0 {
		return ceilDiv(quota, period), true
	}
	return 0, false
}

// detectAvailableMemMB returns the smaller of the cgroup memory headroom (limit
// minus current usage) and the host's MemAvailable, in MiB.
func detectAvailableMemMB() int {
	host := procMemAvailableMB()
	limit, okL := readIntFile("/sys/fs/cgroup/memory.max") // "max" -> unparseable -> not ok
	used, okU := readIntFile("/sys/fs/cgroup/memory.current")
	if okL && okU && limit > 0 {
		cgFree := int((int64(limit) - int64(used)) / (1024 * 1024))
		if cgFree < 0 {
			cgFree = 0
		}
		if host <= 0 || cgFree < host {
			return cgFree
		}
	}
	return host
}

func procMemAvailableMB() int {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemAvailable:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if kb, err := strconv.Atoi(fields[1]); err == nil {
					return kb / 1024
				}
			}
		}
	}
	return 0
}

// detectGPUFreeMB queries nvidia-smi for the first GPU's free memory (MiB).
// ok is false when nvidia-smi is absent or fails (e.g. CPU-only host).
func detectGPUFreeMB() (int, bool) {
	smi, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return 0, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, smi, "--query-gpu=memory.free", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return 0, false
	}
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0])
	if mb, err := strconv.Atoi(strings.TrimSpace(line)); err == nil {
		return mb, true
	}
	return 0, false
}

// --- helpers -----------------------------------------------------------------

func setEnvKey(env []string, key, val string) []string {
	prefix := key + "="
	out := env[:0:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, prefix+val)
}

func envIntDefault(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return def
}

func readIntFile(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	return n, true
}

// parseCPUMax parses a cgroup v2 "cpu.max" body ("<quota> <period>" or
// "max <period>") into whole CPUs (rounded up). ok is false for unlimited.
func parseCPUMax(content string) (int, bool) {
	fields := strings.Fields(content)
	if len(fields) == 2 && fields[0] != "max" {
		quota, err1 := strconv.Atoi(fields[0])
		period, err2 := strconv.Atoi(fields[1])
		if err1 == nil && err2 == nil && quota > 0 && period > 0 {
			return ceilDiv(quota, period), true
		}
	}
	return 0, false
}

func ceilDiv(a, b int) int {
	if b <= 0 {
		return 0
	}
	return (a + b - 1) / b
}
