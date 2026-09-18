package operator

import (
	"context"
	"errors"
	"time"
)

var errRecordingPriority = errors.New("build yielded to recording priority")

// reserveRecordSlot and beginBackgroundBuild share a lock: a new capture
// cannot race a background build's idle check. Canceling a build kills its
// process group, releasing model memory rather than freezing it in RAM.
func (rt *Runtime) reserveRecordSlot() bool {
	rt.priorityMu.Lock()
	defer rt.priorityMu.Unlock()
	select {
	case rt.recordSlots <- struct{}{}:
		if rt.priorityBuildCancel != nil && (rt.processingPolicy() == policyRecordingFirst || (rt.processingPolicy() == policyAuto && (!rt.autoFreshLocked() || !rt.autoCanContinue))) {
			rt.priorityBuildCancel(errRecordingPriority)
		}
		return true
	default:
		return false
	}
}

func (rt *Runtime) releaseRecordSlot() {
	rt.priorityMu.Lock()
	defer rt.priorityMu.Unlock()
	<-rt.recordSlots
	rt.lastRecordFinished = time.Now()
}

func (rt *Runtime) recordingIdleLocked() bool {
	return len(rt.recordSlots) == 0 && (rt.lastRecordFinished.IsZero() || time.Since(rt.lastRecordFinished) >= rt.cfg.RecordingIdleGrace)
}

// Wait before claiming the durable job. Intentional waiting is neither a
// running build nor a resource failure and must not consume retry attempts.
func (rt *Runtime) beginBackgroundBuild() (context.Context, func(), error) {
	for {
		if rt.ctx.Err() != nil {
			return nil, nil, rt.ctx.Err()
		}
		rt.priorityMu.Lock()
		if rt.processingPolicy() == policyConcurrent || rt.recordingIdleLocked() || (rt.processingPolicy() == policyAuto && rt.autoFreshLocked() && rt.autoCanStart) {
			ctx, cancel := context.WithCancelCause(rt.ctx)
			rt.priorityBuildCancel = cancel
			rt.priorityMu.Unlock()
			return ctx, func() {
				rt.priorityMu.Lock()
				rt.priorityBuildCancel = nil
				cancel(nil)
				rt.priorityMu.Unlock()
			}, nil
		}
		rt.priorityMu.Unlock()
		select {
		case <-rt.ctx.Done():
			return nil, nil, rt.ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (rt *Runtime) waitForRecordingIdle() error {
	for {
		if rt.ctx.Err() != nil {
			return rt.ctx.Err()
		}
		rt.priorityMu.Lock()
		ready := rt.processingPolicy() == policyConcurrent || rt.recordingIdleLocked() || (rt.processingPolicy() == policyAuto && rt.autoFreshLocked() && rt.autoCanContinue)
		rt.priorityMu.Unlock()
		if ready {
			return nil
		}
		select {
		case <-rt.ctx.Done():
			return rt.ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

type statusScheduling struct {
	Policy                    string  `json:"policy"`
	RecordingPriority         bool    `json:"recording_priority"`
	RecordingIdleGraceSeconds float64 `json:"recording_idle_grace_seconds"`
	RecordWorkers             int     `json:"record_workers"`
	BuildWorkers              int     `json:"build_workers"`
	ActiveRecordings          int     `json:"active_recordings"`
	AutoTelemetryFresh        bool    `json:"auto_telemetry_fresh"`
	AutoCanStart              bool    `json:"auto_can_start"`
	AutoFreeCPU               float64 `json:"auto_free_cpu_cores"`
	AutoFreeMemMB             int     `json:"auto_free_memory_mb"`
}

func (rt *Runtime) schedulingStatus() statusScheduling {
	rt.priorityMu.Lock()
	defer rt.priorityMu.Unlock()
	return statusScheduling{Policy: rt.processingPolicy(), RecordingPriority: rt.processingPolicy() == policyRecordingFirst, RecordingIdleGraceSeconds: rt.cfg.RecordingIdleGrace.Seconds(), RecordWorkers: rt.cfg.MaxRecordWorkers, BuildWorkers: rt.cfg.MaxBuildWorkers, ActiveRecordings: len(rt.recordSlots), AutoTelemetryFresh: rt.autoFreshLocked(), AutoCanStart: rt.autoCanStart, AutoFreeCPU: rt.autoFreeCPU, AutoFreeMemMB: rt.autoFreeMemMB}
}
