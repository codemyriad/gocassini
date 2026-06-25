package transcribe

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

// detectOnlineCPUs returns the CPU count this process may actually use: the
// cgroup CPU quota when one is set, else the host core count. runtime.NumCPU()
// reports host cores even inside a quota-limited container, which would
// over-subscribe transcription threads — so a containerised recorder stays
// within its share of the host.
func detectOnlineCPUs() int {
	if n, ok := cgroupCPUQuota(); ok && n >= 1 && n < runtime.NumCPU() {
		return n
	}
	return runtime.NumCPU()
}

func cgroupCPUQuota() (int, bool) {
	if data, err := os.ReadFile("/sys/fs/cgroup/cpu.max"); err == nil { // cgroup v2
		return parseCPUMax(string(data))
	}
	quota, ok1 := readIntFile("/sys/fs/cgroup/cpu/cpu.cfs_quota_us") // cgroup v1
	period, ok2 := readIntFile("/sys/fs/cgroup/cpu/cpu.cfs_period_us")
	if ok1 && ok2 && quota > 0 && period > 0 {
		return ceilDiv(quota, period), true
	}
	return 0, false
}

// parseCPUMax parses a cgroup v2 "cpu.max" body ("<quota> <period>" or
// "max <period>") into whole CPUs (rounded up). ok is false for unlimited.
func parseCPUMax(content string) (int, bool) {
	fields := strings.Fields(content)
	if len(fields) == 2 && fields[0] != "max" {
		quota, e1 := strconv.Atoi(fields[0])
		period, e2 := strconv.Atoi(fields[1])
		if e1 == nil && e2 == nil && quota > 0 && period > 0 {
			return ceilDiv(quota, period), true
		}
	}
	return 0, false
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

func ceilDiv(a, b int) int {
	if b <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

// availableMemMB is overridable in tests.
var availableMemMB = procMemAvailableMB

// procMemAvailableMB returns the host's MemAvailable in MiB (0 if unknown).
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

// estModelMemMB is the assumed peak resident memory of one recognizer instance.
// Used to bound parallel-stream concurrency so N recognizers never OOM the host.
func estModelMemMB() int {
	if v := envInt("CASSINI_STT_MODEL_MEM_MB"); v > 0 {
		return v
	}
	return 1800
}

// resolveStreamConcurrency decides how many speaker streams to transcribe in
// parallel. Each worker gets its OWN recognizer, which is why concurrency must
// stay bounded by:
//   - the device: GPU stays serial (one recognizer already saturates the
//     device, and concurrent CUDA recognizer creation is unsafe / VRAM-bound),
//   - the number of streams,
//   - the thread budget (at least one intra-op thread per worker), and
//   - free RAM divided by the per-model footprint (never OOM the host).
//
// The parallelism is a CPU-efficiency optimization (D-436); on GPU it is both
// unnecessary and unsafe, so cuda resolves to 1 unless explicitly overridden.
// CASSINI_STT_STREAM_CONCURRENCY forces a value (still capped by the stream
// count). 1 means the original sequential path.
func resolveStreamConcurrency(numStreams, numThreads int, device string) int {
	if numStreams <= 1 {
		return 1
	}
	if v := envInt("CASSINI_STT_STREAM_CONCURRENCY"); v > 0 {
		return min(v, numStreams)
	}
	// GPU transcription stays serial: each worker would load its own ~2.4GB
	// fp32 recognizer into finite VRAM and call SherpaOnnxCreateOfflineRecognizer
	// concurrently, and concurrent CUDA execution-provider init in
	// sherpa-onnx/onnxruntime is not safe — it crashed recognizer creation on
	// the self-hosted GPU runner and reddened the GPU Talk e2e on main. The env
	// override above is the explicit escape hatch for tuned GPU setups.
	if strings.EqualFold(device, "cuda") {
		return 1
	}
	threads := numThreads
	if threads < 1 {
		threads = DefaultNumThreads()
	}
	c := min(numStreams, threads)
	if mem := availableMemMB(); mem > 0 {
		if memCap := mem / estModelMemMB(); memCap < c {
			c = memCap
		}
	}
	if c < 1 {
		c = 1
	}
	return c
}
