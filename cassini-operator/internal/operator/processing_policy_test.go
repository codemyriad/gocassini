package operator

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAutoHeadroomDistinguishesSmallLargeAndGPU(t *testing.T) {
	for _, tt := range []struct {
		name, device     string
		freeCPU          float64
		freeMem, threads int
		start, keep      bool
	}{
		{"busy-small-cpu", deviceCPU, 1.5, 4000, 2, false, true},
		{"large-cpu", deviceCPU, 15, 16000, 12, true, true},
		{"gpu-host-budget", deviceCUDA, 2.5, 16000, 12, true, true},
		{"gpu-still-needs-host-cpu", deviceCUDA, .5, 16000, 12, false, false},
		{"gpu-still-needs-ram", deviceCUDA, 8, 256, 12, false, false},
		{"running-build-not-double-charged", deviceCPU, 1.5, 800, 12, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			start, keep := overlapHeadroom(tt.freeCPU, tt.freeMem, 2816, tt.threads, tt.device, 1, 512)
			if start != tt.start || keep != tt.keep {
				t.Fatalf("got %t/%t want %t/%t", start, keep, tt.start, tt.keep)
			}
		})
	}
}

func TestAutoUsesBothHostAndCgroupCPUHeadroom(t *testing.T) {
	before := cpuObservation{at: time.Now(), total: 1000, idle: 500, usage: 1000000, hostCPUs: 16, quota: 3}
	after := before
	after.at = before.at.Add(time.Second)
	after.total += 1000
	after.idle += 900
	after.usage += 2000000
	if got, ok := cpuHeadroom(before, after); !ok || got != 1 {
		t.Fatalf("quota ignored: %v %v", got, ok)
	}
	after.quota = 16
	after.idle = before.idle + 100
	if got, ok := cpuHeadroom(before, after); !ok || got != 1.6 {
		t.Fatalf("host pressure ignored: %v %v", got, ok)
	}
	after.at = before.at.Add(11 * time.Second)
	if _, ok := cpuHeadroom(before, after); ok {
		t.Fatal("stale counters accepted")
	}
}

func TestAutoAllowsRecordingOverlapAndPreemptsWithoutFreshHeadroom(t *testing.T) {
	rt := &Runtime{ctx: context.Background(), cfg: Config{ProcessingPolicy: policyAuto}, recordSlots: make(chan struct{}, 3), autoSampleAt: time.Now(), autoCanStart: true, autoCanContinue: true}
	rt.reserveRecordSlot()
	ctx, release, err := rt.beginBackgroundBuild()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	rt.reserveRecordSlot()
	if ctx.Err() != nil {
		t.Fatal("ample-capacity build interrupted", ctx.Err())
	}
	rt.priorityMu.Lock()
	rt.autoSampleAt = time.Now().Add(-10 * time.Second)
	rt.priorityMu.Unlock()
	rt.reserveRecordSlot()
	if !errors.Is(context.Cause(ctx), errRecordingPriority) {
		t.Fatal("stale headroom did not yield build")
	}
}

func TestProcessingPolicyDefaultsAndLegacyAlias(t *testing.T) {
	for _, tt := range []struct {
		cfg  Config
		want string
	}{
		{Config{}, policyConcurrent},
		{Config{RecordingPriority: true}, policyRecordingFirst},
		{Config{ProcessingPolicy: policyAuto}, policyAuto},
	} {
		rt := &Runtime{cfg: tt.cfg}
		if got := rt.processingPolicy(); got != tt.want {
			t.Fatalf("got %s want %s", got, tt.want)
		}
	}
}
