package operator

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseCPUMax(t *testing.T) {
	cases := []struct {
		in     string
		want   int
		wantOK bool
	}{
		{"max 100000", 0, false},   // unlimited
		{"200000 100000", 2, true}, // exactly 2 CPUs
		{"150000 100000", 2, true}, // 1.5 -> round up to 2
		{"100000 100000", 1, true}, // 1 CPU
		{"50000 100000", 1, true},  // 0.5 -> round up to 1
		{"garbage", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := parseCPUMax(c.in)
		if got != c.want || ok != c.wantOK {
			t.Errorf("parseCPUMax(%q) = (%d,%t), want (%d,%t)", c.in, got, ok, c.want, c.wantOK)
		}
	}
}

func TestThreadBudget(t *testing.T) {
	orig := probeOnlineCPUs
	defer func() { probeOnlineCPUs = orig }()

	cases := []struct {
		cpus, reserve, want int
	}{
		{8, 2, 6},
		{8, 0, 8},
		{1, 1, 1},   // never below 1
		{2, 4, 1},   // reserve > cpus -> 1
		{32, 2, 16}, // capped at 16
	}
	for _, c := range cases {
		probeOnlineCPUs = func() int { return c.cpus }
		l := resourceLimits{cpuReserve: c.reserve}
		if got := l.threadBudget(); got != c.want {
			t.Errorf("threadBudget(cpus=%d,reserve=%d) = %d, want %d", c.cpus, c.reserve, got, c.want)
		}
	}
}

func TestDefaultCPUReserve(t *testing.T) {
	for cpus, want := range map[int]int{1: 1, 2: 1, 4: 1, 8: 2, 16: 4} {
		if got := defaultCPUReserve(cpus); got != want {
			t.Errorf("defaultCPUReserve(%d) = %d, want %d", cpus, got, want)
		}
	}
}

func TestSetEnvKey(t *testing.T) {
	env := []string{"A=1", "CASSINI_STT_DEVICE=cuda", "B=2"}
	out := setEnvKey(env, "CASSINI_STT_DEVICE", "cpu")
	if countKey(out, "CASSINI_STT_DEVICE=") != 1 {
		t.Fatalf("expected exactly one device entry, got %v", out)
	}
	if !contains(out, "CASSINI_STT_DEVICE=cpu") {
		t.Fatalf("device not overridden: %v", out)
	}
	if !contains(out, "A=1") || !contains(out, "B=2") {
		t.Fatalf("unrelated vars dropped: %v", out)
	}
}

func TestApplyToEnv(t *testing.T) {
	origCPU, origGPU := probeOnlineCPUs, probeGPUFreeMB
	defer func() { probeOnlineCPUs, probeGPUFreeMB = origCPU, origGPU }()
	probeOnlineCPUs = func() int { return 8 }
	noop := func(string, ...any) {}
	l := resourceLimits{cpuReserve: 2, gpuMinFreeMB: 4096}

	// CPU-only run: threads injected, no device forced.
	probeGPUFreeMB = func() (int, bool) { return 0, false }
	out := l.applyToEnv([]string{"X=1"}, false, noop)
	if !contains(out, "CASSINI_STT_NUM_THREADS=6") {
		t.Errorf("thread budget not injected: %v", out)
	}
	if countKey(out, "CASSINI_STT_DEVICE=") != 0 {
		t.Errorf("device should not be forced on a cpu run: %v", out)
	}

	// cuda intended, GPU full -> forced to cpu.
	probeGPUFreeMB = func() (int, bool) { return 1000, true }
	out = l.applyToEnv(nil, true, noop)
	if !contains(out, "CASSINI_STT_DEVICE=cpu") {
		t.Errorf("expected cuda->cpu fallback when VRAM low: %v", out)
	}

	// cuda intended, GPU has room -> not forced.
	probeGPUFreeMB = func() (int, bool) { return 8000, true }
	out = l.applyToEnv(nil, true, noop)
	if countKey(out, "CASSINI_STT_DEVICE=") != 0 {
		t.Errorf("device should not be forced when VRAM is sufficient: %v", out)
	}

	// cuda intended but nvidia-smi absent -> can't verify, don't force.
	probeGPUFreeMB = func() (int, bool) { return 0, false }
	out = l.applyToEnv(nil, true, noop)
	if countKey(out, "CASSINI_STT_DEVICE=") != 0 {
		t.Errorf("device should not be forced when GPU free is unknown: %v", out)
	}
}

func TestWaitForMemory(t *testing.T) {
	orig := probeAvailableMem
	defer func() { probeAvailableMem = orig }()
	noop := func(string, ...any) {}

	// Immediately above the floor -> returns nil fast.
	probeAvailableMem = func() int { return 8000 }
	l := resourceLimits{minFreeMemMB: 1536, memWaitMax: time.Second, memPoll: 10 * time.Millisecond}
	if err := l.waitForMemory(context.Background(), noop); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Persistently low -> proceeds after memWaitMax (does not hang or error).
	probeAvailableMem = func() int { return 100 }
	l = resourceLimits{minFreeMemMB: 1536, memWaitMax: 20 * time.Millisecond, memPoll: 5 * time.Millisecond}
	start := time.Now()
	if err := l.waitForMemory(context.Background(), noop); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if time.Since(start) < 15*time.Millisecond {
		t.Errorf("expected to wait ~memWaitMax before proceeding")
	}

	// Context cancellation is honoured.
	probeAvailableMem = func() int { return 100 }
	l = resourceLimits{minFreeMemMB: 1536, memWaitMax: time.Minute, memPoll: 5 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	if err := l.waitForMemory(ctx, noop); err == nil {
		t.Errorf("expected context error when RAM never frees")
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func countKey(ss []string, prefix string) int {
	n := 0
	for _, s := range ss {
		if strings.HasPrefix(s, prefix) {
			n++
		}
	}
	return n
}
