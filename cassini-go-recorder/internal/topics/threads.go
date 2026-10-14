package topics

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ThreadsEnv sets how many CPU threads an embed uses. The operator sets it
// from its own CPU budget; an administrator can pin it in the ExApp's
// environment. It changes only the speed, never a vector.
const ThreadsEnv = "CASSINI_TOPICS_THREADS"

const (
	defaultThreads = 4
	maxThreads     = 16
)

// Threads reads $CASSINI_TOPICS_THREADS: min(4, cpus) when it is unset or 0,
// clamped to 1..16 otherwise. A value that is not an integer gives the
// default and an error saying so, for the caller to warn.
func Threads(cpus int) (int, error) {
	fallback := min(defaultThreads, max(cpus, 1))
	raw := strings.TrimSpace(os.Getenv(ThreadsEnv))
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback, fmt.Errorf("%s=%q is not an integer; using %d threads", ThreadsEnv, raw, fallback)
	}
	if n == 0 {
		return fallback, nil
	}
	return ClampThreads(n), nil
}

// ClampThreads keeps an explicit thread count within 1..16.
func ClampThreads(n int) int {
	return min(max(n, 1), maxThreads)
}
