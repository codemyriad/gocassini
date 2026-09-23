package operator

import (
	"fmt"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	policyRecordingFirst = "recording-first"
	policyConcurrent     = "concurrent"
	policyAuto           = "auto"
)

func (rt *Runtime) processingPolicy() string {
	if rt.cfg.ProcessingPolicy != "" {
		return rt.cfg.ProcessingPolicy
	}
	if rt.cfg.RecordingPriority {
		return policyRecordingFirst
	}
	return policyConcurrent
}

type cpuObservation struct {
	at                 time.Time
	total, idle, usage uint64
	hostCPUs, quota    float64
}

func observeCPU() (cpuObservation, error) {
	o := cpuObservation{at: time.Now()}
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return o, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "cpu" {
			if len(fields) < 9 {
				return o, fmt.Errorf("incomplete CPU counters")
			}
			for i := 1; i <= 8; i++ {
				n, e := strconv.ParseUint(fields[i], 10, 64)
				if e != nil {
					return o, e
				}
				o.total += n
				if i == 4 {
					o.idle = n
				}
			}
		} else if strings.HasPrefix(fields[0], "cpu") {
			o.hostCPUs++
		}
	}
	if o.hostCPUs == 0 {
		return o, fmt.Errorf("no CPU counters")
	}
	o.quota = math.Min(o.hostCPUs, float64(runtime.NumCPU()))
	raw, err = os.ReadFile("/sys/fs/cgroup/cpu.max")
	if err != nil {
		return o, err
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 2 {
		return o, fmt.Errorf("incomplete cgroup CPU quota")
	}
	if fields[0] != "max" {
		quota, e1 := strconv.ParseFloat(fields[0], 64)
		period, e2 := strconv.ParseFloat(fields[1], 64)
		if e1 != nil || e2 != nil || quota <= 0 || period <= 0 {
			return o, fmt.Errorf("invalid cgroup CPU quota")
		}
		o.quota = math.Min(o.quota, quota/period)
	}
	raw, err = os.ReadFile("/sys/fs/cgroup/cpu.stat")
	if err != nil {
		return o, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "usage_usec" {
			o.usage, err = strconv.ParseUint(fields[1], 10, 64)
			return o, err
		}
	}
	return o, fmt.Errorf("missing cgroup CPU usage")
}

func cpuHeadroom(before, after cpuObservation) (float64, bool) {
	elapsed := after.at.Sub(before.at).Seconds()
	if before.at.IsZero() || elapsed <= 0 || elapsed > 10 || after.total <= before.total || after.idle < before.idle || after.usage < before.usage {
		return 0, false
	}
	hostFree := float64(after.idle-before.idle) / float64(after.total-before.total) * after.hostCPUs
	groupFree := after.quota - float64(after.usage-before.usage)/elapsed/1e6
	return math.Max(0, math.Min(hostFree, groupFree)), true
}

// The running-build threshold excludes the build's already-allocated budget.
// Charging the same CPU/model twice would oscillate between start and cancel.
func overlapHeadroom(freeCPU float64, freeMem, startMem, threads int, device string, reserveCPU float64, reserveMem int) (start, keep bool) {
	buildCPU := float64(threads)
	if isCUDA(device) {
		buildCPU = 1
	} // existing governor pins GPU host inference to one thread
	keep = freeCPU >= reserveCPU && freeMem >= reserveMem
	start = freeCPU >= buildCPU+reserveCPU && freeMem >= startMem && keep
	return
}

func (rt *Runtime) autoFreshLocked() bool {
	return !rt.autoSampleAt.IsZero() && time.Since(rt.autoSampleAt) < 5*time.Second
}

func (rt *Runtime) startProcessingMonitor() {
	if rt.processingPolicy() != policyAuto {
		return
	}
	rt.workerWG.Add(1)
	go func() {
		defer rt.workerWG.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		before, _ := observeCPU()
		startSamples, badSamples := 0, 0
		for {
			select {
			case <-rt.ctx.Done():
				return
			case <-ticker.C:
			}
			after, err := observeCPU()
			freeCPU, valid := cpuHeadroom(before, after)
			before = after
			start, keep := false, false
			freeMem := probeAvailableMem()
			if err == nil && valid {

				settings := rt.currentSettings()
				device, deviceErr := resolveDeviceForSettings(settings)
				model := ""
				if settings.TranscriptionEnabled && deviceErr == nil {
					model, _ = rt.admitModelForDevice(settings, device)
				}
				if model == "" {
					device = deviceCPU
				}
				limits := resourceLimitsFromEnv()
				start, keep = overlapHeadroom(freeCPU, freeMem, limits.minFreeMemForBuild(device, model), limits.threadBudget(), device, rt.cfg.ProcessingCPUReserve, rt.cfg.ProcessingMemReserveMB)

			}
			if start {
				startSamples++
			} else {
				startSamples = 0
			}
			if keep {
				badSamples = 0
			} else {
				badSamples++
			}
			rt.priorityMu.Lock()
			rt.autoSampleAt = time.Now()
			rt.autoFreeCPU = freeCPU
			rt.autoFreeMemMB = freeMem
			rt.autoCanStart = startSamples >= 2
			// A fresh recording is allowed to overlap only a currently healthy build.
			rt.autoCanContinue = keep
			if len(rt.recordSlots) > 0 && badSamples >= 3 && rt.priorityBuildCancel != nil {
				rt.priorityBuildCancel(errRecordingPriority)
			}
			rt.priorityMu.Unlock()
		}
	}()
}
