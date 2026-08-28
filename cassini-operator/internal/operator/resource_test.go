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

	// CPU resolution is never a fallback for operator-managed recognition.
	probeGPUFreeMB = func() (int, bool) { return 0, false }
	out, err := l.applyToEnv([]string{"X=1"}, false)
	var unavailable *resourceUnavailableError
	if !errors.As(err, &unavailable) || unavailable.resource != "CUDA device" {
		t.Fatalf("CPU resolution error = %v, want CUDA resourceUnavailableError", err)
	}
	if out != nil {
		t.Errorf("CPU resolution env = %v, want no launch environment", out)
	}

	// CUDA intended, GPU full -> defer rather than silently using host CPU/RAM.
	probeGPUFreeMB = func() (int, bool) { return 1000, true }
	out, err = l.applyToEnv(nil, true)
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
	}, true)
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
	if _, err := (resourceLimits{gpuMinFreeMB: 5500}).applyToEnv(nil, true); err != nil {
		t.Errorf("VRAM exactly at floor must be eligible: %v", err)
	}
	probeGPUFreeMB = func() (int, bool) { return 5499, true }
	if _, err := (resourceLimits{gpuMinFreeMB: 5500}).applyToEnv(nil, true); err == nil {
		t.Error("VRAM one MiB below floor must be deferred")
	}

	// Unknown VRAM fails closed: launching without a usable capacity reading
	// cannot uphold the host-safety guarantee.
	probeGPUFreeMB = func() (int, bool) { return 0, false }
	out, err = l.applyToEnv(nil, true)
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
	if err := l.waitForMemory(context.Background(), noop); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Persistently low -> returns a transient resource error after memWaitMax;
	// the caller must not launch the build.
	probeAvailableMem = func() int { return 100 }
	l = resourceLimits{minFreeMemMB: 1536, memWaitMax: 20 * time.Millisecond, memPoll: 5 * time.Millisecond}
	start := time.Now()
	err := l.waitForMemory(context.Background(), noop)
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
	if err := l.waitForMemory(ctx, noop); err == nil {
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
