package transcribe

import (
	"runtime"
	"testing"
)

func TestParseCPUMax(t *testing.T) {
	cases := []struct {
		in     string
		want   int
		wantOK bool
	}{
		{"max 100000", 0, false},
		{"200000 100000", 2, true},
		{"150000 100000", 2, true}, // 1.5 -> 2
		{"100000 100000", 1, true},
		{"garbage", 0, false},
	}
	for _, c := range cases {
		got, ok := parseCPUMax(c.in)
		if got != c.want || ok != c.wantOK {
			t.Errorf("parseCPUMax(%q) = (%d,%t), want (%d,%t)", c.in, got, ok, c.want, c.wantOK)
		}
	}
}

func TestDetectOnlineCPUs(t *testing.T) {
	// Never returns < 1 and never exceeds the host core count.
	n := detectOnlineCPUs()
	if n < 1 || n > runtime.NumCPU() {
		t.Errorf("detectOnlineCPUs() = %d, want 1..%d", n, runtime.NumCPU())
	}
}

func TestResolveStreamConcurrency(t *testing.T) {
	orig := availableMemMB
	defer func() { availableMemMB = orig }()
	availableMemMB = func() int { return 100000 } // plenty of RAM
	t.Setenv("CASSINI_STT_STREAM_CONCURRENCY", "")
	t.Setenv("CASSINI_STT_MODEL_MEM_MB", "")

	if got := resolveStreamConcurrency(1, 8); got != 1 {
		t.Errorf("single stream must be sequential, got %d", got)
	}
	if got := resolveStreamConcurrency(4, 8); got != 4 {
		t.Errorf("4 streams / 8 threads -> 4, got %d", got)
	}
	if got := resolveStreamConcurrency(8, 4); got != 4 {
		t.Errorf("bounded by thread budget 4, got %d", got)
	}

	// Memory bound: 3600MB / 1800MB-per-model = 2.
	availableMemMB = func() int { return 3600 }
	if got := resolveStreamConcurrency(4, 8); got != 2 {
		t.Errorf("memory should cap concurrency to 2, got %d", got)
	}
	// Critically low RAM -> sequential.
	availableMemMB = func() int { return 500 }
	if got := resolveStreamConcurrency(4, 8); got != 1 {
		t.Errorf("low RAM should force sequential, got %d", got)
	}

	// Explicit override, capped by stream count.
	availableMemMB = func() int { return 100000 }
	t.Setenv("CASSINI_STT_STREAM_CONCURRENCY", "3")
	if got := resolveStreamConcurrency(8, 8); got != 3 {
		t.Errorf("override 3, got %d", got)
	}
	if got := resolveStreamConcurrency(2, 8); got != 2 {
		t.Errorf("override capped by streams=2, got %d", got)
	}
}
