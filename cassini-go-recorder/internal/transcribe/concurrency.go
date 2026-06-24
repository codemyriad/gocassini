package transcribe

import (
	"os"
	"strconv"
	"strings"
)

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
// parallel. Each worker gets its OWN recognizer (so there is no shared-state
// thread-safety question), which is why concurrency must stay bounded by:
//   - the number of streams,
//   - the thread budget (at least one intra-op thread per worker), and
//   - free RAM divided by the per-model footprint (never OOM the host).
//
// CASSINI_STT_STREAM_CONCURRENCY forces an explicit value (still capped by the
// stream count). 1 means the original sequential path.
func resolveStreamConcurrency(numStreams, numThreads int) int {
	if numStreams <= 1 {
		return 1
	}
	if v := envInt("CASSINI_STT_STREAM_CONCURRENCY"); v > 0 {
		return min(v, numStreams)
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
