package operator

import (
	"context"
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
// other services. The governor: (1) caps STT threads below the available core
// count, (2) refuses to start a build while free RAM is below a floor, and (3)
// falls back from cuda to cpu when GPU VRAM is too low.

// Probes are package vars so tests can inject deterministic values.
var (
	probeOnlineCPUs   = detectOnlineCPUs
	probeAvailableMem = detectAvailableMemMB
	probeGPUFreeMB    = detectGPUFreeMB
)

type resourceLimits struct {
	cpuReserve   int           // cores to leave free for the rest of the host
	minFreeMemMB int           // do not start a build below this free RAM
	gpuMinFreeMB int           // fall back to cpu below this free VRAM
	memWaitMax   time.Duration // bound on how long to wait for RAM to free up
	memPoll      time.Duration
}

func resourceLimitsFromEnv() resourceLimits {
	cpus := probeOnlineCPUs()
	return resourceLimits{
		cpuReserve:   envIntDefault("CASSINI_BUILD_CPU_RESERVE", defaultCPUReserve(cpus)),
		minFreeMemMB: envIntDefault("CASSINI_BUILD_MIN_FREE_MEM_MB", 1536),
		gpuMinFreeMB: envIntDefault("CASSINI_GPU_MIN_FREE_MB", 4096),
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

// applyToEnv injects CASSINI_STT_NUM_THREADS (the safe budget) and, when the run
// would use cuda but the GPU lacks free VRAM, forces CASSINI_STT_DEVICE=cpu.
// logf is called with human-readable notes about any downgrade.
func (l resourceLimits) applyToEnv(env []string, intendsCUDA bool, logf func(format string, v ...any)) []string {
	env = setEnvKey(env, envSTTNumThreads, strconv.Itoa(l.threadBudget()))

	if intendsCUDA {
		if free, ok := probeGPUFreeMB(); ok && free < l.gpuMinFreeMB {
			logf("resource governor: GPU free %dMiB < %dMiB needed; forcing %s=cpu to avoid OOMing the GPU", free, l.gpuMinFreeMB, envSTTDevice)
			env = setEnvKey(env, envSTTDevice, "cpu")
		}
	}
	return env
}

// waitForMemory blocks (bounded) until free RAM clears the floor, so a build is
// never spawned into a near-OOM host during a transient memory spike. After
// memWaitMax it proceeds with a warning rather than wedging the queue forever.
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
			logf("resource governor: free mem still %dMiB (< %dMiB) after %s; starting build anyway", free, l.minFreeMemMB, l.memWaitMax)
			return nil
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

// buildIntendsCUDA mirrors the recorder's device resolution: an explicit cuda
// override means cuda; cpu means cpu; otherwise the recorder auto-detects a GPU
// from a device node, so we do the same.
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
