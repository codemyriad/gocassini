package operator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// The resource governor sizes each build subprocess so it never starves or OOMs
// the host. The deployed ExApp runs uncapped (cgroup cpu.max=max, memory.max=max)
// on the SAME host as Nextcloud + Talk, so a transcription that grabs every core
// or all of RAM degrades the call server. On GPU hosts the shared VRAM can OOM
// other services. Production builds are GPU-only: the governor (1) pins CUDA
// STT to one host thread, (2) refuses to start while free RAM is below a floor,
// and (3) defers when CUDA or a trustworthy free-VRAM reading is unavailable.
// It never moves recognition onto CPU/RAM as a fallback.

// Probes are package vars so tests can inject deterministic values.
var (
	probeOnlineCPUs   = detectOnlineCPUs
	probeAvailableMem = detectAvailableMemMB
	probeGPUFreeMB    = detectGPUFreeMB
)

type resourceLimits struct {
	cpuReserve   int           // cores to leave free for the rest of the host
	minFreeMemMB int           // do not start a build below this free RAM
	gpuMinFreeMB int           // defer a CUDA build below this free VRAM
	memWaitMax   time.Duration // bound on how long to wait for RAM to free up
	memPoll      time.Duration
}

func resourceLimitsFromEnv() resourceLimits {
	cpus := probeOnlineCPUs()
	return resourceLimits{
		cpuReserve: envIntDefault("CASSINI_BUILD_CPU_RESERVE", defaultCPUReserve(cpus)),
		// Full-run measurements peaked at ~5.2GiB cgroup RAM and 4.63GiB
		// VRAM. Keep conservative launch floors above those working sets so
		// the build can load without consuming the host's last reserve.
		minFreeMemMB: envIntDefault("CASSINI_BUILD_MIN_FREE_MEM_MB", 6144),
		gpuMinFreeMB: envIntDefault("CASSINI_GPU_MIN_FREE_MB", 5500),
		memWaitMax:   time.Duration(envIntDefault("CASSINI_BUILD_MEM_WAIT_SECS", 300)) * time.Second,
		memPoll:      3 * time.Second,
	}
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

// resourceUnavailableError marks transient capacity pressure. The build worker
// recognizes it and restores the claimed job to the durable queue instead of
// turning a valid recording into a terminal failure.
type resourceUnavailableError struct {
	resource string
	detail   string
}

func (e *resourceUnavailableError) Error() string {
	return fmt.Sprintf("resource governor: %s unavailable: %s", e.resource, e.detail)
}

// applyToEnv injects the GPU-only STT execution policy. A CPU resolution means
// CUDA is absent or was explicitly disabled; either case is a transient build
// capacity failure, never permission to run speech recognition on host CPU.
// CUDA builds are pinned to one host thread and require a trustworthy VRAM
// reading at or above the configured floor.
func (l resourceLimits) applyToEnv(env []string, intendsCUDA bool) ([]string, error) {
	if !intendsCUDA {
		return nil, &resourceUnavailableError{
			resource: "CUDA device",
			detail:   "GPU-only speech recognition is required but the configured/automatic device resolved to CPU",
		}
	}

	env = setEnvKey(env, envSTTNumThreads, "1")
	// The recorder intentionally exposes a stream-concurrency escape hatch for
	// manual benchmarking. It is not safe in the operator: every worker owns a
	// separate recognizer/model allocation, and concurrent CUDA provider setup
	// has crashed in practice. Override even a stale inherited value.
	env = setEnvKey(env, envSTTStreamConcurrency, "1")
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
func (l resourceLimits) waitForMemory(ctx context.Context, logf func(format string, v ...any)) error {
	if l.minFreeMemMB <= 0 {
		return nil
	}
	deadline := time.Now().Add(l.memWaitMax)
	warned := false
	for {
		free := probeAvailableMem()
		if free >= l.minFreeMemMB {
			return nil
		}
		if time.Now().After(deadline) {
			return &resourceUnavailableError{
				resource: "host memory",
				detail:   fmt.Sprintf("free %dMiB is below the %dMiB floor after waiting %s", free, l.minFreeMemMB, l.memWaitMax),
			}
		}
		if !warned {
			logf("resource governor: free mem %dMiB < %dMiB; deferring build start", free, l.minFreeMemMB)
			warned = true
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(l.memPoll):
		}
	}
}

// buildIntendsCUDA mirrors the recorder's device resolution. applyToEnv treats a
// false result as unavailable capacity, so neither an explicit CPU override nor
// a temporarily missing GPU node can trigger CPU speech recognition.
func (rt *Runtime) buildIntendsCUDA() bool {
	switch strings.ToLower(strings.TrimSpace(rt.currentSettings().DeviceOverride)) {
	case "cpu":
		return false
	case "cuda":
		return true
	default:
		return detectGPU()
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
