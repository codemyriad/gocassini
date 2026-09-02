package operator

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestResourceLimitDefaultsCoverMeasuredWorkingSet(t *testing.T) {
	orig := probeOnlineCPUs
	defer func() { probeOnlineCPUs = orig }()
	probeOnlineCPUs = func() int { return 8 }
	t.Setenv("CASSINI_BUILD_MIN_FREE_MEM_MB", "")
	t.Setenv("CASSINI_GPU_MIN_FREE_MB", "")

	limits := resourceLimitsFromEnv()
	if limits.minFreeMemMB != 6144 {
		t.Fatalf("default RAM floor = %dMiB, want 6144MiB", limits.minFreeMemMB)
	}
	if limits.gpuMinFreeMB != 5500 {
		t.Fatalf("default VRAM floor = %dMiB, want 5500MiB", limits.gpuMinFreeMB)
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
	l := resourceLimits{cpuReserve: 2, gpuMinFreeMB: 4096}

	// CPU: no VRAM probe, device pinned so the child cannot re-detect, and the
	// thread budget (cores minus reserve) rather than the CUDA single thread.
	probeGPUFreeMB = func() (int, bool) { return 0, false }
	out, err := l.applyToEnv([]string{"X=1", "CASSINI_STT_DEVICE=cuda"}, deviceCPU, modelParakeetV3Int8)
	if err != nil {
		t.Fatalf("CPU applyToEnv() error = %v", err)
	}
	if !contains(out, "CASSINI_STT_DEVICE=cpu") ||
		!contains(out, "CASSINI_STT_NUM_THREADS=6") ||
		!contains(out, "CASSINI_STT_STREAM_CONCURRENCY=1") ||
		!contains(out, "X=1") {
		t.Errorf("CPU policy not applied (device=cpu, 6 threads, one recognizer): %v", out)
	}
	if countKey(out, "CASSINI_STT_DEVICE=") != 1 {
		t.Errorf("stale device override survived: %v", out)
	}
	// The governor sized this build for a model; the child must load exactly
	// that one rather than re-deriving it from the tier.
	if !contains(out, "CASSINI_STT_MODEL="+modelParakeetV3Int8) {
		t.Errorf("admitted model was not pinned into the child environment: %v", out)
	}

	var unavailable *resourceUnavailableError

	// CUDA intended, GPU full -> defer rather than silently using host CPU/RAM.
	probeGPUFreeMB = func() (int, bool) { return 1000, true }
	out, err = l.applyToEnv(nil, deviceCUDA, modelParakeetV3Fp32)
	unavailable = nil
	if !errors.As(err, &unavailable) || unavailable.resource != "GPU memory" {
		t.Fatalf("low VRAM error = %v, want GPU resourceUnavailableError", err)
	}
	if out != nil {
		t.Errorf("low VRAM env = %v, want no launch environment", out)
	}

	// CUDA intended and GPU has room -> explicitly pinned to CUDA and one host
	// thread, even when the input environment was auto-detecting the device.
	probeGPUFreeMB = func() (int, bool) { return 8000, true }
	out, err = l.applyToEnv([]string{
		"CASSINI_STT_NUM_THREADS=12",
		"CASSINI_STT_STREAM_CONCURRENCY=4",
	}, deviceCUDA, modelParakeetV3Fp32)
	if err != nil {
		t.Fatalf("CUDA applyToEnv() error = %v", err)
	}
	if !contains(out, "CASSINI_STT_DEVICE=cuda") ||
		!contains(out, "CASSINI_STT_NUM_THREADS=1") ||
		!contains(out, "CASSINI_STT_STREAM_CONCURRENCY=1") {
		t.Errorf("CUDA policy not pinned to cuda/one thread/one recognizer: %v", out)
	}
	if countKey(out, "CASSINI_STT_STREAM_CONCURRENCY=") != 1 {
		t.Errorf("stale CUDA stream concurrency override survived: %v", out)
	}
	probeGPUFreeMB = func() (int, bool) { return 5500, true }
	if _, err := (resourceLimits{gpuMinFreeMB: 5500}).applyToEnv(nil, deviceCUDA, modelParakeetV3Fp32); err != nil {
		t.Errorf("VRAM exactly at floor must be eligible: %v", err)
	}
	probeGPUFreeMB = func() (int, bool) { return 5499, true }
	if _, err := (resourceLimits{gpuMinFreeMB: 5500}).applyToEnv(nil, deviceCUDA, modelParakeetV3Fp32); err == nil {
		t.Error("VRAM one MiB below floor must be deferred")
	}

	// Unknown VRAM fails closed: launching without a usable capacity reading
	// cannot uphold the host-safety guarantee.
	probeGPUFreeMB = func() (int, bool) { return 0, false }
	out, err = l.applyToEnv(nil, deviceCUDA, modelParakeetV3Fp32)
	unavailable = nil
	if !errors.As(err, &unavailable) || unavailable.resource != "GPU memory" {
		t.Fatalf("unknown VRAM error = %v, want GPU memory resourceUnavailableError", err)
	}
	if out != nil {
		t.Errorf("unknown VRAM env = %v, want no launch environment", out)
	}
}

func TestExecuteBuildCLIDoesNotLaunchCassiniWithoutCUDARuntime(t *testing.T) {
	// An impossible RAM floor proves CUDA capability is checked first. A
	// portable image must block immediately, not spend five minutes waiting for
	// memory that cannot make its missing execution provider appear.
	t.Setenv("CASSINI_BUILD_MIN_FREE_MEM_MB", "999999")
	t.Setenv("CASSINI_BUILD_MEM_WAIT_SECS", "300")
	t.Setenv(envSTTCUDACapable, "0")
	tmp := t.TempDir()
	marker := filepath.Join(tmp, "cassini-started")
	t.Setenv("CASSINI_TEST_MARKER", marker)
	cassiniBin := filepath.Join(tmp, "cassini-marker")
	if err := os.WriteFile(cassiniBin, []byte("#!/bin/sh\n: > \"$CASSINI_TEST_MARKER\"\n"), 0o755); err != nil {
		t.Fatalf("write marker binary: %v", err)
	}

	store, err := OpenStore(filepath.Join(tmp, "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()
	const jobID = "cpu-must-not-launch"
	insertJob(t, store.db, jobID, nowUTCString())
	runPath := seedReadyRunBundle(t, filepath.Join(tmp, "jobs"), jobID)
	if err := store.MarkBuildQueued(context.Background(), jobID, runPath, runPath, nowUTCString()); err != nil {
		t.Fatalf("MarkBuildQueued() error = %v", err)
	}

	rt := &Runtime{
		store:  store,
		cfg:    Config{CassiniBin: cassiniBin, WorkRoot: filepath.Join(tmp, "jobs")},
		logger: log.New(io.Discard, "", 0),
		settings: STTSettings{
			// Emulate a portable image installed on a GPU daemon. Visible GPU
			// hardware must not override the missing CUDA execution provider;
			// admission must fail before cmd.Run can start the ASR process.
			DeviceOverride: "cuda",
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = rt.executeBuildCLI(ctx, buildTask{
		JobID: jobID, AttemptNumber: 1, ArtifactRunPath: runPath,
	})
	var unavailable *resourceUnavailableError
	if !errors.As(err, &unavailable) || unavailable.resource != "CUDA runtime" || !unavailable.permanent {
		t.Fatalf("executeBuildCLI() error = %v, want permanent CUDA runtime resourceUnavailableError", err)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Cassini subprocess ran on CPU (marker stat error = %v)", statErr)
	}
}

func TestWaitForMemory(t *testing.T) {
	orig := probeAvailableMem
	defer func() { probeAvailableMem = orig }()
	noop := func(string, ...any) {}

	// Immediately above the floor -> returns nil fast.
	probeAvailableMem = func() int { return 8000 }
	l := resourceLimits{minFreeMemMB: 1536, memWaitMax: time.Second, memPoll: 10 * time.Millisecond}
	if err := l.waitForMemory(context.Background(), 1536, noop); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Persistently low -> returns a transient resource error after memWaitMax;
	// the caller must not launch the build.
	probeAvailableMem = func() int { return 100 }
	l = resourceLimits{minFreeMemMB: 1536, memWaitMax: 20 * time.Millisecond, memPoll: 5 * time.Millisecond}
	start := time.Now()
	err := l.waitForMemory(context.Background(), 1536, noop)
	var unavailable *resourceUnavailableError
	if !errors.As(err, &unavailable) || unavailable.resource != "host memory" {
		t.Fatalf("low RAM error = %v, want host memory resourceUnavailableError", err)
	}
	if time.Since(start) < 15*time.Millisecond {
		t.Errorf("expected to wait ~memWaitMax before proceeding")
	}

	// Context cancellation is honoured.
	probeAvailableMem = func() int { return 100 }
	l = resourceLimits{minFreeMemMB: 1536, memWaitMax: time.Minute, memPoll: 5 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	if err := l.waitForMemory(ctx, 1536, noop); err == nil {
		t.Errorf("expected context error when RAM never frees")
	}
}

func TestRunBuildJobDefersTransientResourceError(t *testing.T) {
	tmp := t.TempDir()
	store, err := OpenStore(filepath.Join(tmp, "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt := &Runtime{
		ctx:        ctx,
		store:      store,
		cfg:        Config{WorkRoot: filepath.Join(tmp, "jobs")},
		logger:     log.New(io.Discard, "", 0),
		buildQueue: make(chan buildTask, 1),
		// Leave enough wall-clock room to inspect the durable queue before the
		// retry becomes eligible. A 20 ms window made this assertion depend on
		// host load rather than the persisted backoff semantics.
		buildResourceRetryDelay: 250 * time.Millisecond,
	}

	const jobID = "resource-deferred"
	insertJob(t, rt.store.db, jobID, "2026-08-28T10:00:00Z")
	runPath := seedReadyRunBundle(t, rt.cfg.WorkRoot, jobID)
	if err := rt.store.MarkBuildQueued(context.Background(), jobID, runPath, runPath, nowUTCString()); err != nil {
		t.Fatalf("MarkBuildQueued() error = %v", err)
	}
	rt.buildJobFn = func(context.Context, buildTask) (string, error) {
		return "", &resourceUnavailableError{resource: "GPU memory", detail: "test pressure"}
	}

	started := time.Now()
	rt.runBuildJob(buildTask{JobID: jobID, AttemptNumber: 1, ArtifactRunPath: runPath}, 1)

	var stage, state string
	var completedAt, buildStartedAt *string
	if err := rt.store.db.QueryRow(`
SELECT stage, state, completed_at, build_started_at FROM jobs WHERE id = ?`, jobID).
		Scan(&stage, &state, &completedAt, &buildStartedAt); err != nil {
		t.Fatalf("query deferred job: %v", err)
	}
	if stage != "build" || state != "queued" || completedAt != nil || buildStartedAt != nil {
		t.Fatalf("deferred job = stage %q state %q completed=%v started=%v", stage, state, completedAt, buildStartedAt)
	}
	if tasks, err := rt.store.ListQueuedBuildTasks(context.Background()); err != nil {
		t.Fatalf("ListQueuedBuildTasks() error = %v", err)
	} else if len(tasks) != 0 {
		t.Fatalf("deferred task became dispatcher-eligible before backoff: %#v", tasks)
	}
	if err := rt.store.db.QueryRow(`
SELECT stage, state, completed_at, build_started_at
FROM job_attempts WHERE job_id = ? AND attempt_number = 1`, jobID).
		Scan(&stage, &state, &completedAt, &buildStartedAt); err != nil {
		t.Fatalf("query deferred attempt: %v", err)
	}
	if stage != "build" || state != "queued" || completedAt != nil || buildStartedAt != nil {
		t.Fatalf("deferred attempt = stage %q state %q completed=%v started=%v", stage, state, completedAt, buildStartedAt)
	}
	job, err := rt.store.GetJob(context.Background(), jobID)
	if err != nil {
		t.Fatalf("GetJob() deferred job error = %v", err)
	}
	if job.BuildRetryNotBefore == nil || strings.TrimSpace(*job.BuildRetryNotBefore) == "" {
		t.Fatalf("deferred job does not expose build_retry_not_before: %#v", job)
	}
	if job.BuildDeferralCount != 1 || job.Error == nil || !strings.Contains(*job.Error, "test pressure") {
		t.Fatalf("deferred job does not expose count/reason: %#v", job)
	}
	attempts, err := rt.store.ListJobAttempts(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ListJobAttempts() deferred job error = %v", err)
	}
	if len(attempts) != 1 || attempts[0].BuildRetryNotBefore == nil || strings.TrimSpace(*attempts[0].BuildRetryNotBefore) == "" || attempts[0].BuildDeferralCount != 1 {
		t.Fatalf("deferred attempt does not expose build_retry_not_before: %#v", attempts)
	}
	attempt, err := rt.store.GetJobAttempt(context.Background(), jobID, 1)
	if err != nil {
		t.Fatalf("GetJobAttempt() deferred job error = %v", err)
	}
	if attempt.BuildRetryNotBefore == nil || strings.TrimSpace(*attempt.BuildRetryNotBefore) == "" {
		t.Fatalf("deferred event attempt does not expose build_retry_not_before: %#v", attempt)
	}

	select {
	case retry := <-rt.buildQueue:
		if retry.JobID != jobID || retry.AttemptNumber != 1 || retry.DeferralCount != 1 {
			t.Fatalf("retry task = %#v", retry)
		}
		if time.Since(started) < 200*time.Millisecond {
			t.Fatalf("resource retry was delivered without backoff")
		}
	case <-time.After(time.Second):
		t.Fatal("deferred build was not redelivered")
	}
}

func TestRunBuildJobBlocksPermanentAndRetryCeiling(t *testing.T) {
	for _, tc := range []struct {
		name          string
		initialCount  int
		maxDeferrals  int
		unavailable   *resourceUnavailableError
		wantRerunTest bool
	}{
		{
			name:          "permanent image incompatibility",
			maxDeferrals:  8,
			unavailable:   &resourceUnavailableError{resource: "CUDA runtime", detail: "portable image", permanent: true},
			wantRerunTest: true,
		},
		{
			name:         "transient retry ceiling",
			initialCount: 2,
			maxDeferrals: 2,
			unavailable:  &resourceUnavailableError{resource: "GPU memory", detail: "still busy"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			store, err := OpenStore(filepath.Join(tmp, "jobs.sqlite3"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rt := &Runtime{
				ctx:                       ctx,
				store:                     store,
				cfg:                       Config{WorkRoot: filepath.Join(tmp, "jobs")},
				logger:                    log.New(io.Discard, "", 0),
				buildQueue:                make(chan buildTask, 1),
				maxBuildResourceDeferrals: tc.maxDeferrals,
			}

			jobID := "blocked-" + strings.ReplaceAll(tc.name, " ", "-")
			insertJob(t, store.db, jobID, nowUTCString())
			runPath := seedReadyRunBundle(t, rt.cfg.WorkRoot, jobID)
			if err := store.MarkBuildQueued(context.Background(), jobID, runPath, runPath, nowUTCString()); err != nil {
				t.Fatal(err)
			}
			if tc.initialCount > 0 {
				if _, err := store.db.Exec(`
UPDATE jobs SET build_deferral_count = ? WHERE id = ?;
UPDATE job_attempts SET build_deferral_count = ? WHERE job_id = ? AND attempt_number = 1`,
					tc.initialCount, jobID, tc.initialCount, jobID); err != nil {
					t.Fatalf("seed deferral count: %v", err)
				}
			}
			rt.buildJobFn = func(context.Context, buildTask) (string, error) { return "", tc.unavailable }
			task := buildTask{JobID: jobID, AttemptNumber: 1, ArtifactRunPath: runPath, DeferralCount: tc.initialCount}
			rt.runBuildJob(task, 1)

			job, err := store.GetJob(context.Background(), jobID)
			if err != nil {
				t.Fatal(err)
			}
			if job.Stage != "build" || job.State != "blocked" || job.BuildRetryNotBefore != nil || job.BuildStartedAt != nil || job.CompletedAt != nil {
				t.Fatalf("blocked job = %#v", job)
			}
			if job.BuildDeferralCount != tc.initialCount || job.Error == nil || !strings.Contains(*job.Error, tc.unavailable.detail) {
				t.Fatalf("blocked job lost count/reason: %#v", job)
			}
			attempt, err := store.GetJobAttempt(context.Background(), jobID, 1)
			if err != nil {
				t.Fatal(err)
			}
			if attempt.State != "blocked" || attempt.BuildDeferralCount != tc.initialCount || attempt.BuildRetryNotBefore != nil {
				t.Fatalf("blocked attempt = %#v", attempt)
			}
			select {
			case retry := <-rt.buildQueue:
				t.Fatalf("blocked build scheduled retry: %#v", retry)
			default:
			}

			if tc.wantRerunTest {
				rerun, err := store.QueueRerunAttempt(context.Background(), job, nowUTCString())
				if err != nil {
					t.Fatalf("QueueRerunAttempt(blocked) error = %v", err)
				}
				if rerun.Stage != "build" || rerun.State != "queued" || rerun.CurrentAttemptNumber != 2 || rerun.BuildDeferralCount != 0 || rerun.Error != nil {
					t.Fatalf("blocked rerun = %#v", rerun)
				}
			}
		})
	}
}

func TestExponentialBuildRetryDelayCapsAtFifteenMinutes(t *testing.T) {
	for _, tc := range []struct {
		count int
		want  time.Duration
	}{
		{count: 1, want: 15 * time.Second},
		{count: 2, want: 30 * time.Second},
		{count: 6, want: 8 * time.Minute},
		{count: 7, want: 15 * time.Minute},
		{count: 100, want: 15 * time.Minute},
	} {
		if got := exponentialBuildRetryDelay(15*time.Second, tc.count); got != tc.want {
			t.Errorf("count %d delay = %s, want %s", tc.count, got, tc.want)
		}
	}
	var defaultWindow time.Duration
	for count := 1; count <= defaultMaxBuildResourceDeferrals; count++ {
		defaultWindow += exponentialBuildRetryDelay(defaultBuildResourceRetryDelay, count)
	}
	if want := 2*time.Hour + 45*time.Minute + 45*time.Second; defaultWindow != want {
		t.Fatalf("default transient-resource window = %s, want %s", defaultWindow, want)
	}
}

func TestRunBuildJobSerializesConcurrentWorkers(t *testing.T) {
	tmp := t.TempDir()
	store, err := OpenStore(filepath.Join(tmp, "jobs.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt := &Runtime{
		ctx:        ctx,
		store:      store,
		cfg:        Config{WorkRoot: filepath.Join(tmp, "jobs")},
		logger:     log.New(io.Discard, "", 0),
		buildQueue: make(chan buildTask, 2),
	}

	tasks := []buildTask{
		{JobID: "serial-a", AttemptNumber: 1, ArtifactRunPath: "/run/a"},
		{JobID: "serial-b", AttemptNumber: 1, ArtifactRunPath: "/run/b"},
	}
	for _, task := range tasks {
		insertJob(t, store.db, task.JobID, nowUTCString())
		if err := store.MarkBuildQueued(context.Background(), task.JobID, task.ArtifactRunPath, task.ArtifactRunPath, nowUTCString()); err != nil {
			t.Fatalf("queue %s: %v", task.JobID, err)
		}
	}

	entered := make(chan string, 2)
	release := make(chan struct{}, 2)
	var stateMu sync.Mutex
	active, maxActive := 0, 0
	rt.buildJobFn = func(_ context.Context, task buildTask) (string, error) {
		stateMu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		stateMu.Unlock()
		entered <- task.JobID
		<-release
		stateMu.Lock()
		active--
		stateMu.Unlock()
		return "", errors.New("intentional test stop")
	}

	var wg sync.WaitGroup
	wg.Add(2)
	for i, task := range tasks {
		go func(worker int, task buildTask) {
			defer wg.Done()
			rt.runBuildJob(task, worker)
		}(i+1, task)
	}

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first build did not enter")
	}
	select {
	case second := <-entered:
		t.Fatalf("second build %s entered before the first released its GPU admission", second)
	case <-time.After(40 * time.Millisecond):
	}
	release <- struct{}{}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("second build did not enter after the first released")
	}
	release <- struct{}{}
	wg.Wait()
	stateMu.Lock()
	defer stateMu.Unlock()
	if maxActive != 1 {
		t.Fatalf("maximum simultaneous builds = %d, want 1", maxActive)
	}
}

func TestScheduleDeferredBuildStopsOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rt := &Runtime{
		ctx:                     ctx,
		logger:                  log.New(io.Discard, "", 0),
		buildQueue:              make(chan buildTask, 1),
		buildResourceRetryDelay: 10 * time.Millisecond,
	}
	cancel()
	rt.scheduleDeferredBuild(buildTask{JobID: "shutdown", AttemptNumber: 1}, time.Now().Add(10*time.Millisecond))

	select {
	case task := <-rt.buildQueue:
		t.Fatalf("retry delivered after shutdown: %#v", task)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestBuildRetryNotBeforeSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.sqlite3")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	seedJobRow(t, store.db, seededJobRow{ID: "restart-deferred", Stage: "record", State: "queued", CreatedAt: "2026-08-28T10:00:00Z"})
	if err := store.MarkBuildQueued(context.Background(), "restart-deferred", "/run/restart", "/run/restart", nowUTCString()); err != nil {
		t.Fatalf("MarkBuildQueued() error = %v", err)
	}
	task := buildTask{JobID: "restart-deferred", AttemptNumber: 1, ArtifactRunPath: "/run/restart"}
	if claimed, err := store.ClaimBuildRunning(context.Background(), task, nowUTCString()); err != nil || !claimed {
		t.Fatalf("ClaimBuildRunning() = (%v, %v), want (true, nil)", claimed, err)
	}
	if deferred, err := store.MarkBuildDeferred(
		context.Background(), task, 1, "resource governor: test pressure",
		nowUTCString(), "2099-01-01T00:00:00.000000000Z",
	); err != nil || !deferred {
		t.Fatalf("MarkBuildDeferred() = (%v, %v), want (true, nil)", deferred, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	store, err = OpenStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer store.Close()
	if tasks, err := store.ListQueuedBuildTasks(context.Background()); err != nil {
		t.Fatalf("ListQueuedBuildTasks() after restart error = %v", err)
	} else if len(tasks) != 0 {
		t.Fatalf("future retry became eligible after restart: %#v", tasks)
	}
	if claimed, err := store.ClaimBuildRunning(context.Background(), task, nowUTCString()); err != nil || claimed {
		t.Fatalf("early ClaimBuildRunning() = (%v, %v), want (false, nil)", claimed, err)
	}
	if _, err := store.db.Exec(`
UPDATE jobs SET build_retry_not_before = '2000-01-01T00:00:00.000000000Z' WHERE id = 'restart-deferred';
UPDATE job_attempts SET build_retry_not_before = '2000-01-01T00:00:00.000000000Z'
WHERE job_id = 'restart-deferred' AND attempt_number = 1;`); err != nil {
		t.Fatalf("make retry eligible: %v", err)
	}
	eligibleTask := buildTask{JobID: task.JobID, AttemptNumber: task.AttemptNumber, ArtifactRunPath: task.ArtifactRunPath, DeferralCount: 1}
	if tasks, err := store.ListQueuedBuildTasks(context.Background()); err != nil {
		t.Fatalf("ListQueuedBuildTasks() eligible error = %v", err)
	} else if len(tasks) != 1 || tasks[0] != eligibleTask {
		t.Fatalf("eligible tasks = %#v, want %#v", tasks, eligibleTask)
	}
	if claimed, err := store.ClaimBuildRunning(context.Background(), task, nowUTCString()); err != nil || claimed {
		t.Fatalf("stale deferral task ClaimBuildRunning() = (%v, %v), want (false, nil)", claimed, err)
	}
	if claimed, err := store.ClaimBuildRunning(context.Background(), eligibleTask, nowUTCString()); err != nil || !claimed {
		t.Fatalf("current deferral task ClaimBuildRunning() = (%v, %v), want (true, nil)", claimed, err)
	}
}

func TestBuildRetryEligibilityUsesFixedWidthTextTime(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()

	seedJobRow(t, store.db, seededJobRow{ID: "fractional-retry", Stage: "record", State: "queued", CreatedAt: "2026-08-28T10:00:00Z"})
	if err := store.MarkBuildQueued(context.Background(), "fractional-retry", "/run/fractional", "/run/fractional", "2026-08-28T10:00:00Z"); err != nil {
		t.Fatalf("MarkBuildQueued() error = %v", err)
	}
	if _, err := store.db.Exec(`
UPDATE jobs SET build_retry_not_before = '2026-08-28T10:00:00.800000000Z' WHERE id = 'fractional-retry';
UPDATE job_attempts SET build_retry_not_before = '2026-08-28T10:00:00.800000000Z'
WHERE job_id = 'fractional-retry' AND attempt_number = 1;`); err != nil {
		t.Fatalf("seed fixed-width retry timestamp: %v", err)
	}

	// Migration 0007 and all new writes use a fixed-width UTC representation,
	// so direct TEXT comparison is chronological and can use the queue index.
	task := buildTask{JobID: "fractional-retry", AttemptNumber: 1, ArtifactRunPath: "/run/fractional"}
	claimed, err := store.ClaimBuildRunning(context.Background(), task, "2026-08-28T10:00:00.850000000Z")
	if err != nil || !claimed {
		t.Fatalf("chronologically due ClaimBuildRunning() = (%v, %v), want (true, nil)", claimed, err)
	}
}

func TestFormatUTCStringIsFixedWidthAndLexicallySortable(t *testing.T) {
	earlier := time.Date(2026, 8, 28, 10, 0, 0, 800_000_000, time.UTC)
	later := time.Date(2026, 8, 28, 10, 0, 0, 850_000_000, time.UTC)
	earlierText := formatUTCString(earlier)
	laterText := formatUTCString(later)
	if len(earlierText) != len(laterText) || len(earlierText) != len("2026-08-28T10:00:00.000000000Z") {
		t.Fatalf("timestamp widths = %d/%d: %q / %q", len(earlierText), len(laterText), earlierText, laterText)
	}
	if earlierText >= laterText {
		t.Fatalf("fixed-width timestamps are not chronologically sortable: %q >= %q", earlierText, laterText)
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

func TestResolveBuildDevice(t *testing.T) {
	// A CPU-only host must transcribe, not block: the recording is already made
	// and CPU inference is a supported (slower) outcome (D-702).
	cases := []struct {
		name        string
		cudaCapable string
		override    string
		wantDevice  string
		wantErr     string
		wantPerm    bool
	}{
		{name: "auto on a portable image falls back to cpu", cudaCapable: "0", wantDevice: deviceCPU},
		{name: "auto without the marker falls back to cpu", cudaCapable: "", wantDevice: deviceCPU},
		{name: "explicit cpu is honoured", cudaCapable: "1", override: "cpu", wantDevice: deviceCPU},
		{name: "explicit cuda on a portable image is permanent", cudaCapable: "0", override: "cuda", wantErr: "CUDA runtime", wantPerm: true},
		{name: "nonsense override is permanent", cudaCapable: "1", override: "tpu", wantErr: "device policy", wantPerm: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(envSTTCUDACapable, c.cudaCapable)
			rt := &Runtime{settings: STTSettings{DeviceOverride: c.override}}
			device, err := rt.resolveBuildDevice()
			if c.wantErr != "" {
				var unavailable *resourceUnavailableError
				if !errors.As(err, &unavailable) || unavailable.resource != c.wantErr {
					t.Fatalf("resolveBuildDevice() error = %v, want %s resourceUnavailableError", err, c.wantErr)
				}
				if unavailable.permanent != c.wantPerm {
					t.Errorf("permanent = %v, want %v", unavailable.permanent, c.wantPerm)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveBuildDevice() error = %v", err)
			}
			if device != c.wantDevice {
				t.Errorf("device = %q, want %q", device, c.wantDevice)
			}
		})
	}
}

func TestResolveBuildDeviceExplicitCUDANeedsAVisibleDevice(t *testing.T) {
	// Only meaningful on a host without NVIDIA device nodes; on a GPU box the
	// override is satisfiable and there is nothing to assert.
	if detectGPU() {
		t.Skip("host has an NVIDIA device; the unsatisfiable-override path cannot be exercised")
	}
	t.Setenv(envSTTCUDACapable, "1")
	rt := &Runtime{settings: STTSettings{DeviceOverride: "cuda"}}
	_, err := rt.resolveBuildDevice()
	var unavailable *resourceUnavailableError
	if !errors.As(err, &unavailable) || unavailable.resource != "CUDA device" {
		t.Fatalf("resolveBuildDevice() error = %v, want CUDA device resourceUnavailableError", err)
	}
	// Transient, not permanent: a GPU can come back, and the administrator
	// asked for one explicitly — this must never silently become a CPU run.
	if unavailable.permanent {
		t.Error("a missing GPU is transient; blocking permanently strands a recoverable host")
	}
}

func TestMinFreeMemForBuild(t *testing.T) {
	l := resourceLimits{minFreeMemMB: 6144, cpuMemHeadroomMB: 1024}
	cases := []struct {
		device, model string
		want          int
	}{
		{deviceCUDA, modelParakeetV3Fp32, 6144},
		{deviceCPU, modelParakeet110M, 512 + 1024},
		{deviceCPU, modelParakeetV3Int8, 1792 + 1024},
		{deviceCPU, modelParakeetV3Fp32, 3584 + 1024},
		// An unrecognised model is charged the largest known footprint: the
		// governor exists to keep a build from OOMing Nextcloud and Talk.
		{deviceCPU, "some-future-model", 3584 + 1024},
	}
	for _, c := range cases {
		if got := l.minFreeMemForBuild(c.device, c.model); got != c.want {
			t.Errorf("minFreeMemForBuild(%s, %s) = %d, want %d", c.device, c.model, got, c.want)
		}
	}
}

func TestExecuteBuildCLIRunsOnCPUWithoutCUDARuntime(t *testing.T) {
	// The regression this whole change exists for: a portable image on a host
	// with no GPU must actually launch the recorder instead of blocking the
	// build (D-702). The inverse of
	// TestExecuteBuildCLIDoesNotLaunchCassiniWithoutCUDARuntime, which keeps an
	// explicit CUDA override loud.
	origMem, origCPU := probeAvailableMem, probeOnlineCPUs
	defer func() { probeAvailableMem, probeOnlineCPUs = origMem, origCPU }()
	probeAvailableMem = func() int { return 64000 }
	probeOnlineCPUs = func() int { return 4 }

	t.Setenv(envSTTCUDACapable, "0")
	tmp := t.TempDir()
	marker := filepath.Join(tmp, "cassini-started")
	t.Setenv("CASSINI_TEST_MARKER", marker)
	cassiniBin := filepath.Join(tmp, "cassini-marker")
	if err := os.WriteFile(cassiniBin, []byte("#!/bin/sh\n: > \"$CASSINI_TEST_MARKER\"\n"), 0o755); err != nil {
		t.Fatalf("write marker binary: %v", err)
	}

	store, err := OpenStore(filepath.Join(tmp, "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()
	const jobID = "cpu-must-launch"
	insertJob(t, store.db, jobID, nowUTCString())
	runPath := seedReadyRunBundle(t, filepath.Join(tmp, "jobs"), jobID)
	if err := store.MarkBuildQueued(context.Background(), jobID, runPath, runPath, nowUTCString()); err != nil {
		t.Fatalf("MarkBuildQueued() error = %v", err)
	}

	rt := &Runtime{
		store:    store,
		cfg:      Config{CassiniBin: cassiniBin, WorkRoot: filepath.Join(tmp, "jobs")},
		logger:   log.New(io.Discard, "", 0),
		settings: STTSettings{Quality: sttQualityBalanced},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = rt.executeBuildCLI(ctx, buildTask{
		JobID: jobID, AttemptNumber: 1, ArtifactRunPath: runPath,
	})
	var unavailable *resourceUnavailableError
	if errors.As(err, &unavailable) {
		t.Fatalf("executeBuildCLI() refused a CPU build: %v", err)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("recorder was never launched on CPU (marker stat error = %v)", statErr)
	}
}

func TestCPUFloorsRankByTierCost(t *testing.T) {
	// The floors encode measured peaks (device.go). If an edit ever makes a
	// heavier model look cheaper, the governor would admit a build it cannot
	// hold — so assert the ordering rather than only the individual numbers.
	l := resourceLimitsFromEnv()
	fast := l.minFreeMemForBuild(deviceCPU, modelParakeet110M)
	balanced := l.minFreeMemForBuild(deviceCPU, modelParakeetV3Int8)
	best := l.minFreeMemForBuild(deviceCPU, modelParakeetV3Fp32)
	if !(fast < balanced && balanced < best) {
		t.Fatalf("CPU floors are not ordered by tier cost: fast=%d balanced=%d best=%d", fast, balanced, best)
	}
	if fast <= 0 {
		t.Fatalf("fast tier floor = %d, want a positive floor", fast)
	}
}

func TestModelNeedsDownload(t *testing.T) {
	// Each image carries the models for the device it serves. A tier outside
	// that set arrives by one download into the persistent cache (D-704).
	bundledRoot := t.TempDir()
	cacheRoot := t.TempDir()
	rt := &Runtime{cfg: Config{BundledModelRoot: bundledRoot, ModelCacheRoot: cacheRoot}}

	seed := func(root, model string, complete bool) {
		t.Helper()
		dir := filepath.Join(root, "models", model)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "encoder.onnx"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if complete {
			if err := os.WriteFile(filepath.Join(dir, modelCompletionMarker), []byte("t\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	if !rt.modelNeedsDownload(modelParakeetV3Int8) {
		t.Error("a model in neither root must read as a download")
	}

	seed(bundledRoot, modelParakeetV3Fp32, false)
	if rt.modelNeedsDownload(modelParakeetV3Fp32) {
		t.Error("a model baked into the image must not be downloaded")
	}

	// An interrupted download leaves files without the marker. That directory
	// must still read as a download, or the operator would promise a build the
	// recorder cannot start.
	seed(cacheRoot, modelParakeetV3Int8, false)
	if !rt.modelNeedsDownload(modelParakeetV3Int8) {
		t.Error("an unfinished cache directory must still read as a download")
	}

	seed(cacheRoot, modelParakeetV3Int8, true)
	if rt.modelNeedsDownload(modelParakeetV3Int8) {
		t.Error("a completed download must not be fetched again")
	}
}

func TestAdmitModelForDeviceHonoursTheAirGapSwitch(t *testing.T) {
	// An administrator who forbids downloads must get a blocked build with a
	// message, not a build that reaches for the network and fails there.
	bundledRoot := t.TempDir()
	rt := &Runtime{cfg: Config{
		BundledModelRoot:      bundledRoot,
		ModelCacheRoot:        t.TempDir(),
		DisallowModelDownload: true,
	}}

	_, err := rt.admitModelForDevice(STTSettings{Quality: sttQualityBest}, deviceCPU)
	var unavailable *resourceUnavailableError
	if !errors.As(err, &unavailable) || unavailable.resource != "model bundle" {
		t.Fatalf("admission error = %v, want a model bundle refusal", err)
	}
	if !unavailable.permanent {
		t.Error("no download can arrive while the switch is set; want a permanent block")
	}
	if !strings.Contains(unavailable.detail, "CASSINI_DISALLOW_MODEL_DOWNLOAD") {
		t.Errorf("detail %q does not name the setting that blocks the build", unavailable.detail)
	}

	// The tier the image bakes still runs on the same air-gapped host.
	dir := filepath.Join(bundledRoot, "models", modelParakeetV3Int8)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "encoder.int8.onnx"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.admitModelForDevice(STTSettings{Quality: sttQualityBalanced}, deviceCPU); err != nil {
		t.Fatalf("a bundled tier was blocked on an air-gapped host: %v", err)
	}

	// Without the switch the same missing model is a download, not a block.
	open := &Runtime{cfg: Config{BundledModelRoot: bundledRoot, ModelCacheRoot: t.TempDir()}}
	if _, err := open.admitModelForDevice(STTSettings{Quality: sttQualityBest}, deviceCPU); err != nil {
		t.Fatalf("admission blocked a downloadable tier: %v", err)
	}
}
