package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cassini-operator/internal/operator/appapi"
)

// seedCapture writes one promoted capture and backdates it, so a test can talk
// about age without waiting.
func seedCapture(t *testing.T, root, room, owner string, callStart int64, size int, age time.Duration) string {
	t.Helper()
	dir := captureUploadDir(root, room, owner, callStart)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, captureSidecarName), []byte(`{"format":"x"}`), 0o640); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "segment-0.webm"), make([]byte, size), 0o640); err != nil {
		t.Fatalf("write segment: %v", err)
	}
	backdate(t, dir, age)
	return dir
}

func backdate(t *testing.T, dir string, age time.Duration) {
	t.Helper()
	when := time.Now().Add(-age)
	if err := os.Chtimes(dir, when, when); err != nil {
		t.Fatalf("backdate %s: %v", dir, err)
	}
}

// stubCaptureFreeBytes makes the volume report whatever a test needs. Filling a
// real disk to exercise the floor is not an option in a unit test.
func stubCaptureFreeBytes(t *testing.T, free int64) {
	t.Helper()
	original := probeCaptureFreeBytes
	probeCaptureFreeBytes = func(string) (int64, error) { return free, nil }
	t.Cleanup(func() { probeCaptureFreeBytes = original })
}

func TestCaptureLimitsResolveFromEnvWithZeroMeaningNoLimit(t *testing.T) {
	limits := captureLimitsFromEnv()
	if limits.ownerQuota != defaultCaptureOwnerQuotaMB<<20 || limits.totalQuota != defaultCaptureTotalQuotaMB<<20 {
		t.Fatalf("unset quotas = %d/%d, want the defaults", limits.ownerQuota, limits.totalQuota)
	}
	if limits.minFreeDisk != defaultCaptureMinFreeDiskMB<<20 {
		t.Fatalf("unset free-disk floor = %d, want the default", limits.minFreeDisk)
	}
	if limits.maxAge != defaultCaptureMaxAgeHours*time.Hour {
		t.Fatalf("unset max age = %s, want the default", limits.maxAge)
	}

	t.Setenv(envCaptureOwnerQuotaMB, "7")
	t.Setenv(envCaptureMaxAgeHours, "0")
	limits = captureLimitsFromEnv()
	if limits.ownerQuota != 7<<20 {
		t.Fatalf("owner quota = %d, want 7MiB", limits.ownerQuota)
	}
	// Zero is the escape hatch, not a mistake: an installation that bounds the
	// volume some other way keeps everything.
	if limits.maxAge != 0 {
		t.Fatalf("max age = %s, want 0 (no age sweep)", limits.maxAge)
	}
}

func TestValidateCaptureLimitsRefusesAKnobNobodyCanHaveMeant(t *testing.T) {
	for _, tc := range []struct {
		name  string
		key   string
		value string
	}{
		{"a mistyped quota", envCaptureOwnerQuotaMB, "2O48"},
		{"a negative floor", envCaptureMinFreeDiskMB, "-1"},
		{"an age that is not a number", envCaptureMaxAgeHours, "two weeks"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			err := validateCaptureLimits()
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("validateCaptureLimits() = %v, want an error naming %s", err, tc.key)
			}
		})
	}
}

func TestLoadConfigRejectsAnUnreadableCaptureQuota(t *testing.T) {
	repoRoot := makeFakeOperatorRepoRoot(t)
	t.Setenv("CASSINI_REPO_ROOT", repoRoot)
	t.Setenv(envCaptureTotalQuotaMB, "lots")

	_, code, err := loadConfig(nil, ioDiscard{})
	if code != 2 || err == nil {
		t.Fatalf("loadConfig() code = %d err = %v, want exit 2 with an error", code, err)
	}
	if !strings.Contains(err.Error(), envCaptureTotalQuotaMB) {
		t.Fatalf("error %v must name the variable that is wrong", err)
	}
}

func TestMeasureCaptureRootSeparatesStoredCapturesFromTransientBytes(t *testing.T) {
	root := t.TempDir()
	seedCapture(t, root, "room-a", "alice", 1700, 1000, time.Hour)
	seedCapture(t, root, "room-a", "bob", 1700, 2000, 2*time.Hour)
	// A set-aside directory still occupies the volume, so it counts toward the
	// bytes; it is not a capture, so it must not count toward the total nor
	// become the "oldest" figure an admin reads.
	superseded := captureUploadDir(root, "room-a", "alice", 1600) + captureSupersededSuffix
	if err := os.MkdirAll(superseded, 0o750); err != nil {
		t.Fatalf("mkdir superseded: %v", err)
	}
	if err := os.WriteFile(filepath.Join(superseded, "segment-0.webm"), make([]byte, 500), 0o640); err != nil {
		t.Fatalf("write superseded segment: %v", err)
	}
	backdate(t, superseded, 99*time.Hour)
	// So does a staging directory, and it carries no owner in its name.
	staging := filepath.Join(root, captureStagingPrefix+"xyz")
	if err := os.MkdirAll(staging, 0o750); err != nil {
		t.Fatalf("mkdir staging: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staging, "part"), make([]byte, 300), 0o640); err != nil {
		t.Fatalf("write staging part: %v", err)
	}

	usage, err := measureCaptureRoot(root)
	if err != nil {
		t.Fatalf("measureCaptureRoot: %v", err)
	}
	if usage.Captures != 2 {
		t.Fatalf("captures = %d, want 2 (the set-aside and staging directories are not captures)", usage.Captures)
	}
	wantBytes := int64(1000 + 2000 + 500 + 300 + 2*len(`{"format":"x"}`))
	if usage.Bytes != wantBytes {
		t.Fatalf("bytes = %d, want %d (everything on the volume, transient included)", usage.Bytes, wantBytes)
	}
	// Bytes counts the volume; ByOwner does not count a set-aside copy. The
	// per-owner quota is the TERMINAL refusal — the client deletes its only
	// copy on it — and a set-aside directory is this participant's own previous
	// copy of a call they are re-uploading right now, about to be swept.
	// Charging it would let their old copy destroy their new one.
	if usage.ByOwner["alice"] != int64(1000+len(`{"format":"x"}`)) {
		t.Fatalf("alice's bytes = %d, want her live capture only, not the set-aside copy awaiting the sweep", usage.ByOwner["alice"])
	}
	if usage.ByOwner["bob"] != int64(2000+len(`{"format":"x"}`)) {
		t.Fatalf("bob's bytes = %d", usage.ByOwner["bob"])
	}
	if age := time.Since(usage.Oldest); age < 90*time.Minute || age > 150*time.Minute {
		t.Fatalf("oldest capture is %s old, want ~2h (the 99h set-aside directory is not a capture)", age)
	}
}

func TestMeasureCaptureRootDoesNotCreateWhatItMeasures(t *testing.T) {
	root := filepath.Join(t.TempDir(), "never-collected")

	usage, err := measureCaptureRoot(root)
	if err != nil {
		t.Fatalf("measureCaptureRoot: %v", err)
	}
	if usage.Captures != 0 || usage.Bytes != 0 {
		t.Fatalf("usage = %+v, want empty", usage)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("measuring an absent capture root created it (err=%v)", err)
	}
}

// The status codes are the load-bearing part: 413 makes the client delete its
// recording, anything else makes it keep the buffer and retry. A quota the
// owner spent themselves is terminal; a full volume is not theirs to fix.
func TestCaptureAdmissionStatusPerRefusalReason(t *testing.T) {
	t.Cleanup(resetCaptureAdmissions)
	resetCaptureAdmissions()
	root := t.TempDir()
	seedCapture(t, root, "room-a", "alice", 1700, 4<<20, time.Hour)
	limits := captureLimits{
		minFreeDisk: 1 << 30,
		ownerQuota:  4 << 20,
		totalQuota:  64 << 20,
	}

	t.Run("a spent owner quota is terminal", func(t *testing.T) {
		stubCaptureFreeBytes(t, 8<<30)
		_, refusal := admitCaptureUpload(root, "alice", 1024, limits)
		if refusal == nil || refusal.status != http.StatusRequestEntityTooLarge || refusal.reason != "owner_quota" {
			t.Fatalf("refusal = %+v, want 413 owner_quota", refusal)
		}
	})

	t.Run("a full installation is retryable", func(t *testing.T) {
		stubCaptureFreeBytes(t, 8<<30)
		tight := limits
		tight.totalQuota = 1 << 20
		_, refusal := admitCaptureUpload(root, "carol", 1024, tight)
		if refusal == nil || refusal.status != http.StatusInsufficientStorage || refusal.reason != "total_quota" {
			t.Fatalf("refusal = %+v, want 507 total_quota: another account's uploads are not this recording's fault", refusal)
		}
	})

	t.Run("a volume near its floor is retryable", func(t *testing.T) {
		stubCaptureFreeBytes(t, 1<<20)
		_, refusal := admitCaptureUpload(root, "carol", 1024, limits)
		if refusal == nil || refusal.status != http.StatusInsufficientStorage || refusal.reason != "disk_floor" {
			t.Fatalf("refusal = %+v, want 507 disk_floor", refusal)
		}
	})

	t.Run("an owner with room is admitted with what is left", func(t *testing.T) {
		stubCaptureFreeBytes(t, 8<<30)
		admission, refusal := admitCaptureUpload(root, "carol", 1024, limits)
		if refusal != nil {
			t.Fatalf("refused an admissible upload: %+v", refusal)
		}
		if admission.ownerRemaining != limits.ownerQuota {
			t.Fatalf("carol's remaining = %d, want her whole quota", admission.ownerRemaining)
		}
		// alice's 4MiB plus her sidecar is what the total has already spent.
		if admission.totalRemaining >= limits.totalQuota {
			t.Fatalf("total remaining = %d, want less than the cap: the root is not empty", admission.totalRemaining)
		}
	})
}

// A body with no Content-Length is charged the per-request ceiling against the
// floor, because the only honest assumption about an undeclared body is that it
// is as large as one is allowed to be.
func TestCaptureAdmissionChargesAnUndeclaredBodyTheWholeCeiling(t *testing.T) {
	t.Cleanup(resetCaptureAdmissions)
	resetCaptureAdmissions()
	root := t.TempDir()
	limits := captureLimits{minFreeDisk: 1 << 30}
	// Room for the floor, but not for the floor plus a maximum upload.
	stubCaptureFreeBytes(t, (1<<30)+(captureMaxUploadBytes/2))

	if _, refusal := admitCaptureUpload(root, "alice", 1024, limits); refusal != nil {
		t.Fatalf("a declared small upload fits and must be admitted: %+v", refusal)
	}
	_, refusal := admitCaptureUpload(root, "alice", -1, limits)
	if refusal == nil || refusal.reason != "disk_floor" {
		t.Fatalf("refusal = %+v, want disk_floor for a body that declares nothing", refusal)
	}
}

func TestCaptureUploadRefusesWhenTheVolumeIsNearItsFloor(t *testing.T) {
	rt := captureTestRuntime(t)
	stubCaptureFreeBytes(t, 1<<20)

	req := uploadRequest(t, validSidecar(), map[string][]byte{"segment-0.webm": []byte("audio")})
	req = req.WithContext(appapi.WithUserID(context.Background(), "bob"))
	rec := httptest.NewRecorder()

	rt.captureUploadHandler(nil, quietLogger())(rec, req)

	if rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("status = %d, want 507: a full volume is retryable, and 413 would make the client delete the recording", rec.Code)
	}
	if _, err := os.Stat(rt.cfg.CaptureRoot); err == nil {
		t.Fatal("an upload refused for want of space still wrote to the volume")
	}
}

func TestCaptureUploadRefusesAnOwnerWhoseQuotaIsSpent(t *testing.T) {
	rt := captureTestRuntime(t)
	stubCaptureFreeBytes(t, 8<<30)
	t.Setenv(envCaptureOwnerQuotaMB, "1")
	seedCapture(t, rt.cfg.CaptureRoot, "abc123", "bob", 1600, 1<<20, time.Hour)

	req := uploadRequest(t, validSidecar(), map[string][]byte{"segment-0.webm": []byte("audio")})
	req = req.WithContext(appapi.WithUserID(context.Background(), "bob"))
	rec := httptest.NewRecorder()

	rt.captureUploadHandler(nil, quietLogger())(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: an account's own spent quota cannot be retried away", rec.Code)
	}
	staged, _ := filepath.Glob(filepath.Join(rt.cfg.CaptureRoot, captureStagingPrefix+"*"))
	if len(staged) != 0 {
		t.Fatalf("a refused upload left staging behind: %v", staged)
	}
}

// The pre-check can only charge what the request declared. A client that
// declares one byte and sends two megabytes must still be stopped, or the quota
// is advisory.
func TestCaptureUploadEnforcesTheQuotaAgainstALyingContentLength(t *testing.T) {
	rt := captureTestRuntime(t)
	stubCaptureFreeBytes(t, 8<<30)
	t.Setenv(envCaptureOwnerQuotaMB, "1")

	req := uploadRequest(t, validSidecar(), map[string][]byte{
		"segment-0.webm": bytes.Repeat([]byte("x"), 2<<20),
	})
	req = req.WithContext(appapi.WithUserID(context.Background(), "bob"))
	req.ContentLength = 1
	rec := httptest.NewRecorder()

	rt.captureUploadHandler(nil, quietLogger())(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 for a body that outgrew the owner's quota mid-stream", rec.Code)
	}
	dir := captureUploadDir(rt.cfg.CaptureRoot, "abc123", "bob", validSidecar().CallStartWallMS)
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("an over-quota upload was promoted anyway")
	}
}

// The documented promise is that with collection off this feature touches no
// storage. A statfs or a quota walk is work done for a feature that is supposed
// to be inert, so the gate has to come first.
func TestCaptureGateIsDecidedBeforeAnyStorageWork(t *testing.T) {
	rt := captureTestRuntime(t)
	t.Setenv(envSourceCaptureEnabled, "0")
	original := probeCaptureFreeBytes
	probeCaptureFreeBytes = func(string) (int64, error) {
		t.Error("a disabled installation measured the volume for a capture upload")
		return 0, nil
	}
	t.Cleanup(func() { probeCaptureFreeBytes = original })

	req := uploadRequest(t, validSidecar(), map[string][]byte{"segment-0.webm": []byte("audio")})
	req = req.WithContext(appapi.WithUserID(context.Background(), "bob"))
	rec := httptest.NewRecorder()

	rt.captureUploadHandler(nil, quietLogger())(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestCaptureUploadLogsEveryRefusalWithItsOwnerAndReason(t *testing.T) {
	rt := captureTestRuntime(t)
	stubCaptureFreeBytes(t, 1<<20)
	var logged bytes.Buffer

	req := uploadRequest(t, validSidecar(), map[string][]byte{"segment-0.webm": []byte("audio")})
	req = req.WithContext(appapi.WithUserID(context.Background(), "bob"))
	rec := httptest.NewRecorder()

	rt.captureUploadHandler(nil, log.New(&logged, "", 0))(rec, req)

	line := logged.String()
	for _, want := range []string{"capture upload refused", "owner=bob", "status=507", "reason=disk_floor"} {
		if !strings.Contains(line, want) {
			t.Fatalf("refusal log %q must contain %q: a refusal nobody can see is a pilot nobody can diagnose", line, want)
		}
	}
}

func TestSweepCaptureRootRemovesOrphansAndAgedCaptures(t *testing.T) {
	root := t.TempDir()
	fresh := seedCapture(t, root, "room-a", "alice", 1700, 10, time.Hour)
	aged := seedCapture(t, root, "room-a", "alice", 1600, 10, 30*24*time.Hour)
	// A set-aside directory whose replacement landed: redundant, and discovery
	// already ignores it.
	live := seedCapture(t, root, "room-b", "bob", 1700, 10, time.Hour)
	stale := live + captureSupersededSuffix
	if err := os.MkdirAll(stale, 0o750); err != nil {
		t.Fatalf("mkdir stale: %v", err)
	}
	orphan := filepath.Join(root, captureStagingPrefix+"crashed")
	if err := os.MkdirAll(orphan, 0o750); err != nil {
		t.Fatalf("mkdir orphan: %v", err)
	}
	backdate(t, orphan, 48*time.Hour)

	removed, err := sweepCaptureRoot(root, 14*24*time.Hour, time.Now())
	if err != nil {
		t.Fatalf("sweepCaptureRoot: %v", err)
	}
	if len(removed) != 3 {
		t.Fatalf("removed %v, want the aged capture, the set-aside directory and the orphaned staging", removed)
	}
	assertGone(t, aged, "older than the configured age")
	assertGone(t, stale, "its replacement is in place")
	assertGone(t, orphan, "orphaned staging")
	assertExists(t, fresh, "younger than the configured age")
	assertExists(t, live, "the live capture a rerun would read")
	// Every removal has to say why, or a retention policy reads like data loss.
	for _, entry := range removed {
		if !strings.Contains(entry, "(") {
			t.Fatalf("removal %q carries no reason", entry)
		}
	}
}

// The one removal that would cost a recording: promoteCapture renames the old
// capture aside BEFORE it moves the new one in, so a crash between the two
// leaves the set-aside directory holding the only copy.
func TestSweepCaptureRootKeepsASetAsideCaptureWithNoReplacement(t *testing.T) {
	root := t.TempDir()
	orphaned := captureUploadDir(root, "room-a", "alice", 1700) + captureSupersededSuffix
	if err := os.MkdirAll(orphaned, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(orphaned, "segment-0.webm"), []byte("the only copy"), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}
	backdate(t, orphaned, 365*24*time.Hour)

	removed, err := sweepCaptureRoot(root, 24*time.Hour, time.Now())
	if err != nil {
		t.Fatalf("sweepCaptureRoot: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("removed %v, want nothing: a promotion that never finished leaves this holding the only copy of that audio", removed)
	}
	assertExists(t, orphaned, "no live sibling replaced it")
}

// A staging directory's mtime stops moving while one large segment streams into
// it, so an idle-looking one may well have a request attached.
func TestSweepCaptureRootLeavesAStagingDirectoryAnUploadCouldStillBeUsing(t *testing.T) {
	root := t.TempDir()
	inFlight := filepath.Join(root, captureStagingPrefix+"live")
	if err := os.MkdirAll(inFlight, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	backdate(t, inFlight, captureStagingGrace/2)

	removed, err := sweepCaptureRoot(root, 24*time.Hour, time.Now())
	if err != nil {
		t.Fatalf("sweepCaptureRoot: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("removed %v, want nothing: an upload may still be streaming into it", removed)
	}
	assertExists(t, inFlight, "younger than the staging grace")
}

func TestSweepCaptureRootWithNoAgeLimitRemovesNoCaptures(t *testing.T) {
	root := t.TempDir()
	ancient := seedCapture(t, root, "room-a", "alice", 1700, 10, 365*24*time.Hour)

	removed, err := sweepCaptureRoot(root, 0, time.Now())
	if err != nil {
		t.Fatalf("sweepCaptureRoot: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("removed %v, want nothing when the age sweep is switched off", removed)
	}
	assertExists(t, ancient, "the age sweep is off")
}

func TestStatusReportsWhatSourceCaptureIsHolding(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.CaptureRoot = filepath.Join(t.TempDir(), "capture")
	t.Setenv(envSourceCaptureEnabled, "1")
	t.Setenv(envSourceAudioIngestEnabled, "0")
	stubCaptureFreeBytes(t, 12<<30)
	seedCapture(t, rt.cfg.CaptureRoot, "room-a", "alice", 1700, 4096, 3*time.Hour)
	seedCapture(t, rt.cfg.CaptureRoot, "room-a", "bob", 1700, 2048, time.Hour)

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()
	rt.statusHandler(rec, req)

	var resp statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	capture := resp.SourceCapture
	if !capture.CollectionEnabled || capture.IngestEnabled {
		t.Fatalf("switches = collection %t / ingest %t, want collection on and ingest off", capture.CollectionEnabled, capture.IngestEnabled)
	}
	if capture.Captures != 2 {
		t.Fatalf("captures = %d, want 2", capture.Captures)
	}
	if capture.Bytes < 4096+2048 {
		t.Fatalf("bytes = %d, want at least the two segments", capture.Bytes)
	}
	oldest, err := time.Parse(time.RFC3339, capture.OldestReceivedAt)
	if err != nil {
		t.Fatalf("oldest_received_at = %q: %v", capture.OldestReceivedAt, err)
	}
	if age := time.Since(oldest); age < 2*time.Hour || age > 4*time.Hour {
		t.Fatalf("oldest capture is %s old, want ~3h", age)
	}
	if capture.FreeDiskBytes != 12<<30 {
		t.Fatalf("free_disk_bytes = %d, want the probed figure", capture.FreeDiskBytes)
	}
	if capture.OwnerQuotaBytes != defaultCaptureOwnerQuotaMB<<20 || capture.MinFreeDiskBytes != defaultCaptureMinFreeDiskMB<<20 {
		t.Fatalf("limits = %+v, want the configured policy so an admin can see how close intake is to refusing", capture)
	}
	// A pilot feature's storage must not decide whether the operator is healthy.
	if !resp.OK {
		t.Fatalf("status is unhealthy with a perfectly ordinary capture root: %#v", resp)
	}
}

func TestStatusReportsSourceCaptureWithCollectionOffWithoutCreatingTheRoot(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.CaptureRoot = filepath.Join(t.TempDir(), "capture")
	t.Setenv(envSourceCaptureEnabled, "0")
	stubCaptureFreeBytes(t, 12<<30)

	status := rt.sourceCaptureStatus()

	if status.CollectionEnabled {
		t.Fatal("collection is reported enabled while the switch is off")
	}
	if status.Captures != 0 || status.Bytes != 0 || status.Detail != "" {
		t.Fatalf("status = %+v, want empty figures and no complaint", status)
	}
	if _, err := os.Stat(rt.cfg.CaptureRoot); !os.IsNotExist(err) {
		t.Fatalf("reporting status created the capture root (err=%v)", err)
	}
}

func TestCaptureUsageProbeCoalescesAndCachesTheWalk(t *testing.T) {
	var walks int
	probe := newCaptureUsageProbe(time.Minute, func() (captureUsage, error) {
		walks++
		return captureUsage{Captures: 3}, nil
	})

	for i := 0; i < 5; i++ {
		usage, err := probe.check()
		if err != nil || usage.Captures != 3 {
			t.Fatalf("check() = %+v, %v", usage, err)
		}
	}
	if walks != 1 {
		t.Fatalf("walked the capture root %d times, want 1: /status is polled", walks)
	}
}

func TestSweepCaptureStorageNeverFailsTheCaller(t *testing.T) {
	var logged bytes.Buffer
	rt := &Runtime{
		cfg:    Config{CaptureRoot: filepath.Join(t.TempDir(), "gone")},
		logger: log.New(&logged, "", 0),
	}

	// A root that was never created is not a failure; it is a deployment that
	// never collected anything.
	rt.sweepCaptureStorage()
	if logged.Len() != 0 {
		t.Fatalf("sweeping an absent capture root logged %q", logged.String())
	}

	rt.cfg.CaptureRoot = t.TempDir()
	aged := seedCapture(t, rt.cfg.CaptureRoot, "room-a", "alice", 1700, 10, 365*24*time.Hour)
	rt.sweepCaptureStorage()
	if !strings.Contains(logged.String(), "capture retention removed") {
		t.Fatalf("sweep log = %q, want a line naming what it removed", logged.String())
	}
	assertGone(t, aged, "older than the default age")
}

// A publish is the moment the volume has just grown, and it is one of the two
// edges the sweep runs on. Without this the capture root only ever shrinks at
// startup, which on an ExApp that is never restarted is never.
func TestASuccessfulPublishSweepsTheCaptureRoot(t *testing.T) {
	rt, cleanup := newBarePublishRuntime(t, log.New(ioDiscard{}, "", 0))
	defer cleanup()
	rt.cfg.CaptureRoot = filepath.Join(t.TempDir(), "capture")
	aged := seedCapture(t, rt.cfg.CaptureRoot, "room-a", "alice", 1700, 32, 365*24*time.Hour)
	fresh := seedCapture(t, rt.cfg.CaptureRoot, "room-a", "alice", 1800, 32, time.Hour)

	attemptSite := filepath.Join(t.TempDir(), "attempt.site")
	rt.publishJobFn = func(context.Context, publishTask) (string, error) { return attemptSite, nil }
	rt.publishSink = &okPublishSink{location: "somewhere://else"}
	insertJob(t, rt.store.db, "pub-sweeps-capture", "2026-06-12T10:00:00Z")
	if err := rt.store.MarkPublishQueued(context.Background(), "pub-sweeps-capture", "/tmp/meeting", "/tmp/meeting", nowUTCString()); err != nil {
		t.Fatalf("MarkPublishQueued() error = %v", err)
	}

	rt.runPublishJob(publishTask{JobID: "pub-sweeps-capture", AttemptNumber: 1})

	assertGone(t, aged, "a publish sweeps the capture root")
	assertExists(t, fresh, "younger than the configured age")
}

// The staging prefix and the set-aside suffix are shared with the recorder's
// discovery filter and with promoteCapture. A sweep keyed on a different
// literal would either miss the leftovers or remove a live capture.
func TestCaptureSweepUsesTheSameNamesTheUploadHandlerWrites(t *testing.T) {
	rt := captureTestRuntime(t)
	stubCaptureFreeBytes(t, 8<<30)
	sidecar := validSidecar()
	req := uploadRequest(t, sidecar, map[string][]byte{"segment-0.webm": []byte("audio")})
	req = req.WithContext(appapi.WithUserID(context.Background(), "bob"))
	rec := httptest.NewRecorder()
	rt.captureUploadHandler(nil, quietLogger())(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	dir := captureUploadDir(rt.cfg.CaptureRoot, sidecar.RoomToken, "bob", sidecar.CallStartWallMS)
	backdate(t, dir, 30*24*time.Hour)
	removed, err := sweepCaptureRoot(rt.cfg.CaptureRoot, 14*24*time.Hour, time.Now())
	if err != nil {
		t.Fatalf("sweepCaptureRoot: %v", err)
	}
	if len(removed) != 1 || !strings.Contains(removed[0], filepath.Base(dir)) {
		t.Fatalf("removed %v, want the aged capture the handler just wrote at %s", removed, dir)
	}
}

func TestCaptureFreeBytesWalksUpToAnExistingAncestor(t *testing.T) {
	root := t.TempDir()

	free, err := captureFreeBytes(filepath.Join(root, "not", "created", "yet"))
	if err != nil {
		t.Fatalf("captureFreeBytes: %v", err)
	}
	if free <= 0 {
		t.Fatalf("free = %d, want a reading from the nearest existing ancestor", free)
	}
	if _, err := os.Stat(filepath.Join(root, "not")); !os.IsNotExist(err) {
		t.Fatalf("measuring free space created the missing path (err=%v)", err)
	}
}

// A capture root whose contents cannot be measured must refuse rather than let
// an unbounded upload through, and it must refuse retryably.
func TestCaptureAdmissionRefusesWhenTheRootCannotBeMeasured(t *testing.T) {
	t.Cleanup(resetCaptureAdmissions)
	resetCaptureAdmissions()
	root := t.TempDir()
	unreadable := filepath.Join(root, "room-a")
	if err := os.MkdirAll(unreadable, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o750) })
	if os.Geteuid() == 0 {
		t.Skip("root reads an unreadable directory anyway")
	}
	stubCaptureFreeBytes(t, 8<<30)

	_, refusal := admitCaptureUpload(root, "alice", 1024, captureLimits{ownerQuota: 1 << 30})
	if refusal == nil || refusal.status != http.StatusInsufficientStorage {
		t.Fatalf("refusal = %+v, want a retryable 507: a volume we cannot measure is one we must not fill", refusal)
	}
}

func TestCaptureAdmissionWithNoLimitsAdmitsUpToTheRequestCeiling(t *testing.T) {
	t.Cleanup(resetCaptureAdmissions)
	resetCaptureAdmissions()
	root := t.TempDir()
	seedCapture(t, root, "room-a", "alice", 1700, 1<<20, time.Hour)

	admission, refusal := admitCaptureUpload(root, "alice", 1024, captureLimits{})
	if refusal != nil {
		t.Fatalf("refused with every limit switched off: %+v", refusal)
	}
	if admission.remaining() != captureMaxUploadBytes {
		t.Fatalf("budget = %d, want the per-request ceiling", admission.remaining())
	}
	// And the budget never overflows when the copy loop adds one to it.
	if admission.remaining()+1 <= 0 {
		t.Fatalf("budget %d overflows when the read loop adds its detection byte", admission.remaining())
	}
}
