package operator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func readinessRuntime(t *testing.T) (*Runtime, func()) {
	t.Helper()
	rt, cleanup := newTestRuntime(t)
	t.Setenv(envTalkSignalingInternalSecret, "")
	t.Setenv(envNextcloudURL, "https://cloud.test")
	t.Setenv(envAppID, "gocassini")
	t.Setenv(envSTTCUDACapable, "0")
	rt.setSettings(STTSettings{Quality: sttQualityBalanced, DeviceOverride: deviceCPU})
	rt.cfg.TalkSharedSecret = "recording-credential"
	return rt, cleanup
}

func putRecordingSetup(t *testing.T, rt *Runtime, body string, want int) string {
	t.Helper()
	rec := httptest.NewRecorder()
	rt.recordingSetupHandler(rec, httptest.NewRequest("PUT", "/talk/setup", strings.NewReader(body)))
	if rec.Code != want {
		t.Fatalf("PUT status=%d want=%d body=%s", rec.Code, want, rec.Body.String())
	}
	return rec.Body.String()
}

func TestRecordingSetupSecretPersistenceAndRedaction(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	body := putRecordingSetup(t, rt, `{"internal_secret":"saved-internal-credential","test_room_url":"https://cloud.test/call/testroom"}`, 200)
	if strings.Contains(body, "saved-internal-credential") || strings.Contains(body, "recording-credential") {
		t.Fatal("secret leaked")
	}
	info, err := os.Stat(rt.recordingSetupPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions %o", info.Mode().Perm())
	}
	restarted := &Runtime{cfg: rt.cfg}
	if value, source := restarted.signalingSecret(); value != "saved-internal-credential" || source != "setup" {
		t.Fatalf("secret=%q source=%q", value, source)
	}
	if !restarted.recordingSetup.checkedAt.IsZero() {
		t.Fatal("reused live evidence after restart")
	}
	if got := lookupEnv(rt.recordChildEnv(), envTalkSignalingInternalSecret); got != "saved-internal-credential" {
		t.Fatal("child did not receive saved secret")
	}
	t.Setenv(envTalkSignalingInternalSecret, "deployment-credential")
	if value, source := rt.signalingSecret(); value != "deployment-credential" || source != "env" {
		t.Fatal("environment precedence lost")
	}
	putRecordingSetup(t, rt, `{"internal_secret":"replacement"}`, 409)
}

func TestRecordingSetupRejectsMalformedTargetsAndStore(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	for _, room := range []string{"javascript://cloud.test/call/testroom", "https://cloud.test/index.php/call/", "https://cloud.test/call/room/extra", "https://user:pass@cloud.test/call/testroom", "https://cloud.test/call/testroom?secret=x", "https://cloud.test/call/../admin"} {
		body, _ := json.Marshal(map[string]string{"test_room_url": room})
		putRecordingSetup(t, rt, string(body), 400)
	}
	t.Setenv(envNextcloudURL, "https://cloud.test/nextcloud")
	putRecordingSetup(t, rt, `{"test_room_url":"https://cloud.test/nextcloud/call/testroom"}`, 200)
	path := filepath.Join(t.TempDir(), "jobs.db")
	broken := &Runtime{cfg: Config{DBPath: path}}
	if err := os.WriteFile(broken.recordingSetupPath(), []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	broken.signalingSecret()
	if !broken.recordingSetup.loadFailed {
		t.Fatal("corrupt configuration silently ignored")
	}
	putRecordingSetup(t, broken, `{"internal_secret":"new"}`, 409)
}

func TestReadinessChecksCoalesceExpireAndInvalidateOnEdit(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	putRecordingSetup(t, rt, `{"internal_secret":"internal","test_room_url":"https://cloud.test/call/testroom"}`, 200)
	var calls atomic.Int32
	rt.recordingSetup.probe = func(ctx context.Context, room string) ([]readinessCheck, error) {
		calls.Add(1)
		time.Sleep(10 * time.Millisecond)
		return []readinessCheck{{ID: "talk.hpb", State: "passed", Code: "hpb_authenticated", Message: "connected"}}, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); rt.checkRecordingReadiness(context.Background()) }()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("probes=%d", calls.Load())
	}
	report := rt.readiness(context.Background())
	found := false
	for _, c := range report.Checks {
		if c.Code == "hpb_authenticated" {
			found = true
		}
	}
	if !found {
		t.Fatal("no successful result")
	}
	rt.recordingSetup.checkedAt = time.Now().Add(-2 * readinessTTL)
	report = rt.readiness(context.Background())
	for _, c := range report.Checks {
		if c.Code == "hpb_authenticated" {
			t.Fatal("expired pass presented as current")
		}
	}
	putRecordingSetup(t, rt, `{"internal_secret":"changed"}`, 200)
	if !rt.recordingSetup.checkedAt.IsZero() || len(rt.recordingSetup.checks) != 0 {
		t.Fatal("credential edit retained stale pass")
	}
}

func TestReadinessAdmissionAndPublicResponse(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	req := TriggerRequest{TalkAuthMode: talkAuthModeHPBInternal}
	if rt.recordingConfigurationRefusal(req) == "" {
		t.Fatal("missing credential admitted")
	}
	report := rt.readiness(context.Background())
	if report.State != "needs_action" {
		t.Fatalf("state=%s", report.State)
	}
	putRecordingSetup(t, rt, `{"internal_secret":"private-internal","test_room_url":"https://cloud.test/call/privateroom"}`, 200)
	rec := httptest.NewRecorder()
	rt.setupHandler(rec, httptest.NewRequest("GET", "/setup", nil))
	for _, value := range []string{"private-internal", "privateroom", "secret_source", "recording-credential"} {
		if strings.Contains(rec.Body.String(), value) {
			t.Fatalf("public response leaked %s", value)
		}
	}
	if !strings.Contains(rec.Body.String(), "recording_state") {
		t.Fatal("no public guidance")
	}
	rt.recordingSetup.checkedAt = time.Now().Add(-2 * readinessTTL)
	if rt.publicRecordingState() != "not_verified" {
		t.Fatal("expired results are not unknown")
	}
}

func TestReadinessTestRequiresTalkPublicationAndPlayback(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	putRecordingSetup(t, rt, `{"test_room_url":"https://cloud.test/call/testroom","action":"arm_test"}`, 200)
	now := nowUTCString()
	token, binding := "testroom", `{"room_token":"testroom"}`
	job := Job{ID: "test-recording", Provider: nextcloudTalkProvider, RequestJSON: "{}", Stage: "record", State: "queued", CurrentAttemptNumber: 1, CreatedAt: now, UpdatedAt: now, RoomToken: &token}
	if err := rt.store.InsertQueuedJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if rt.readiness(context.Background()).Test.JobID != "" {
		t.Fatal("operator-created recording counted as handoff evidence")
	}
	if _, err := rt.store.db.Exec(`UPDATE jobs SET talk_binding=?,room_token=? WHERE id=?`, binding, token, job.ID); err != nil {
		t.Fatal(err)
	}
	putRecordingSetup(t, rt, `{"action":"confirm_playback","job_id":"test-recording"}`, 409)
	if _, err := rt.store.db.Exec(`UPDATE jobs SET stage='done',state='succeeded',publish_finished_at=?,completed_at=? WHERE id=?`, now, now, job.ID); err != nil {
		t.Fatal(err)
	}
	putRecordingSetup(t, rt, `{"action":"confirm_playback","job_id":"another-recording"}`, 409)
	putRecordingSetup(t, rt, `{"action":"confirm_playback","job_id":"test-recording"}`, 200)
	report := rt.readiness(context.Background())
	if !report.Test.Published || report.Test.PlaybackVerifiedAt == "" || report.Test.ViewerURL != "#meeting=test-recording" {
		t.Fatalf("test=%+v", report.Test)
	}
	// Restart retains history but clears all live handoff evidence.
	restart := &Runtime{cfg: rt.cfg, store: rt.store}
	restart.signalingSecret()
	test := restart.readinessTest(context.Background(), restart.recordingSetup.state)
	if test.PlaybackVerifiedAt == "" {
		t.Fatal("test history lost on restart")
	}
	if !restart.recordingSetup.inboundAt.IsZero() {
		t.Fatal("old callback treated as current")
	}
}

func TestReadinessAdmissionUsesOnlyCurrentDefinitiveFailures(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	putRecordingSetup(t, rt, `{"internal_secret":"internal","test_room_url":"https://cloud.test/call/testroom"}`, 200)
	req := TriggerRequest{TalkAuthMode: talkAuthModeHPBInternal, URL: "https://cloud.test/call/anotherroom"}
	rt.recordingSetup.checkedAt = time.Now()
	rt.recordingSetup.checks = []readinessCheck{{State: "needs_action", Code: "signaling_auth_failed", Message: "Rejected internal credential."}}
	if got := rt.recordingConfigurationRefusal(req); !strings.Contains(got, "Rejected internal credential") {
		t.Fatalf("refusal=%s", got)
	}
	// A pasted browser hostname cannot extend cached vetoes to another backend.
	rt.recordingSetup.state.TestRoomURL = "https://other.test/index.php/call/testroom"
	foreign := TriggerRequest{TalkAuthMode: talkAuthModeHPBInternal, URL: "https://other.test/call/anotherroom"}
	if got := rt.recordingConfigurationRefusal(foreign); got != "" {
		t.Fatalf("foreign backend vetoed: %s", got)
	}
	foreign.TalkConnectURL = "https://cloud.test"
	if got := rt.recordingConfigurationRefusal(foreign); got == "" {
		t.Fatal("explicit trusted connection missed cached failure")
	}
	rt.recordingSetup.checkedAt = time.Now().Add(-2 * readinessTTL)
	if got := rt.recordingConfigurationRefusal(req); got != "" {
		t.Fatalf("stale finding vetoed recording: %s", got)
	}
	rt.recordingSetup.checkedAt = time.Now()
	rt.recordingSetup.checks = []readinessCheck{{State: "not_verified", Code: "signaling_unreachable", Message: "Temporary network error."}}
	if got := rt.recordingConfigurationRefusal(req); got != "" {
		t.Fatalf("transient finding vetoed recording: %s", got)
	}
}

func TestRecordingSetupRefusalIsAConflictBeforeJobCreation(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	req := TriggerRequest{TalkAuthMode: talkAuthModeHPBInternal}
	_, _, err := rt.prepareRecordJob(context.Background(), nextcloudTalkProvider, "{}", req)
	if err == nil {
		t.Fatal("unconfigured recording admitted")
	}
	status, _ := recordAcceptError(err)
	if status != 409 {
		t.Fatalf("status=%d", status)
	}
	jobs, err := rt.store.ListJobs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatal("created a doomed job")
	}
}

func TestReadinessRoutesMountedAtRootAndPrefix(t *testing.T) {
	for _, base := range []string{"", "/", "/operator"} {
		root := http.NewServeMux()
		mountBasePathOnto(root, base, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
		for _, route := range []string{"/readiness", "/readiness/check", "/talk/setup"} {
			rec := httptest.NewRecorder()
			root.ServeHTTP(rec, httptest.NewRequest("GET", strings.TrimRight(base, "/")+route, nil))
			if rec.Code != http.StatusNoContent {
				t.Errorf("base=%q route=%s status=%d", base, route, rec.Code)
			}
		}
	}
}

func TestReadinessPublicLinksNeverSelectProbeHost(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	t.Setenv(envNextcloudURL, "http://nextcloud:80/nextcloud")
	for _, room := range []string{"https://cloud.example/nextcloud/call/token123", "https://cloud.example/nextcloud/index.php/call/token123", "https://untrusted.example/call/token123"} {
		if !rt.validTestRoom(room) {
			t.Fatalf("rejected browser link %s", room)
		}
		args, err := rt.connectionProbeArgs(room)
		want := []string{"talk-check", "--call", room, "--connect-url", "http://nextcloud:80/nextcloud"}
		if err != nil || !reflect.DeepEqual(args, want) {
			t.Fatalf("probe target=%v err=%v", args, err)
		}
	}
	t.Setenv(envNextcloudURL, "")
	if rt.validTestRoom("https://cloud.example/call/token123") {
		t.Fatal("accepted probe without trusted backend")
	}
	rt.cfg.TalkBackendURL = "https://trusted.example/nc"
	args, err := rt.connectionProbeArgs("https://cloud.example/index.php/call/token123")
	if err != nil || args[4] != rt.cfg.TalkBackendURL || args[2] != "https://cloud.example/index.php/call/token123" {
		t.Fatalf("standalone target=%v err=%v", args, err)
	}
}

func TestReadinessPublicStatePrioritizesActionRegardlessOfOrder(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	putRecordingSetup(t, rt, `{"internal_secret":"internal"}`, 200)
	rt.recordingSetup.checkedAt = time.Now()
	for _, checks := range [][]readinessCheck{
		{{State: "not_verified"}, {State: "needs_action"}},
		{{State: "needs_action"}, {State: "not_verified"}},
	} {
		rt.recordingSetup.checks = checks
		if rt.publicRecordingState() != "needs_action" {
			t.Fatal("masked action with unknown status")
		}
	}
}

func TestReadinessExpiredHandoffOffersTestWithoutInventingPass(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	putRecordingSetup(t, rt, `{"internal_secret":"internal","test_room_url":"https://cloud.test/call/room"}`, 200)
	rt.recordingSetup.inboundAt = time.Now().Add(-2 * readinessTTL)
	rt.recordingSetup.probe = func(context.Context, string) ([]readinessCheck, error) {
		return []readinessCheck{{ID: "talk.hpb", State: "passed", Code: "hpb_authenticated"}}, nil
	}
	rt.checkRecordingReadiness(context.Background())
	for _, check := range rt.readiness(context.Background()).Checks {
		if check.ID == "talk.handoff" {
			if check.State != "not_verified" || check.Action != "test_recording" {
				t.Fatalf("handoff=%+v", check)
			}
			return
		}
	}
	t.Fatal("missing handoff check")
}
