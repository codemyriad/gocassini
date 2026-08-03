package operator

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTalkRoomNameFetcherFetchesDisplayNameAsOwner(t *testing.T) {
	var gotPath, gotAuth, gotAppID, gotOCSHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("AUTHORIZATION-APP-API")
		gotAppID = r.Header.Get("EX-APP-ID")
		gotOCSHeader = r.Header.Get("OCS-APIRequest")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ocs":{"meta":{"status":"ok"},"data":{"token":"tok123","name":"daily","displayName":"Daily Meeting"}}}`))
	}))
	defer server.Close()

	cfg := ExAppConfig{NextcloudURL: server.URL, AppSecret: "s3cret", AppID: "gocassini", AppVersion: "1.0.0"}
	fetch := cfg.talkRoomNameFetcher()
	if fetch == nil {
		t.Fatal("talkRoomNameFetcher() = nil, want active fetcher")
	}

	info, err := fetch(context.Background(), "alice", "tok123")
	if err != nil {
		t.Fatalf("fetch() error = %v", err)
	}
	if info.Name != "Daily Meeting" {
		t.Errorf("name = %q, want %q", info.Name, "Daily Meeting")
	}
	if gotPath != "/ocs/v2.php/apps/spreed/api/v4/room/tok123" {
		t.Errorf("path = %q", gotPath)
	}
	// The call must run as the recording owner: AppAPI decodes the auth
	// header as base64("<userId>:<secret>").
	wantAuth := base64.StdEncoding.EncodeToString([]byte("alice:s3cret"))
	if gotAuth != wantAuth {
		t.Errorf("AUTHORIZATION-APP-API = %q, want %q", gotAuth, wantAuth)
	}
	if gotAppID != "gocassini" {
		t.Errorf("EX-APP-ID = %q", gotAppID)
	}
	if gotOCSHeader != "true" {
		t.Errorf("OCS-APIRequest = %q", gotOCSHeader)
	}
}

func TestTalkRoomNameFetcherFallsBackToRoomName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ocs":{"data":{"displayName":"","name":"ops-standup"}}}`))
	}))
	defer server.Close()

	fetch := ExAppConfig{NextcloudURL: server.URL, AppSecret: "s", AppID: "a"}.talkRoomNameFetcher()
	info, err := fetch(context.Background(), "alice", "tok")
	if err != nil {
		t.Fatalf("fetch() error = %v", err)
	}
	if info.Name != "ops-standup" {
		t.Errorf("name = %q, want %q", info.Name, "ops-standup")
	}
}

func TestTalkRoomNameFetcherErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ocs/v2.php/apps/spreed/api/v4/room/gone":
			http.Error(w, "not found", http.StatusNotFound)
		default:
			_, _ = w.Write([]byte(`{"ocs":{"data":{"displayName":""}}}`))
		}
	}))
	defer server.Close()

	fetch := ExAppConfig{NextcloudURL: server.URL, AppSecret: "s", AppID: "a"}.talkRoomNameFetcher()
	if _, err := fetch(context.Background(), "alice", "gone"); err == nil {
		t.Error("fetch(gone) error = nil, want HTTP error")
	}
	if _, err := fetch(context.Background(), "alice", "unnamed"); err == nil {
		t.Error("fetch(unnamed) error = nil, want missing-name error")
	}
	if _, err := fetch(context.Background(), "", "tok"); err == nil {
		t.Error("fetch with empty owner error = nil, want error")
	}
}

func TestTalkRoomNameFetcherNilWithoutAppAPIEnv(t *testing.T) {
	if fetch := (ExAppConfig{}).talkRoomNameFetcher(); fetch != nil {
		t.Error("talkRoomNameFetcher() with empty config != nil")
	}
	if fetch := (ExAppConfig{NextcloudURL: "http://nc", AppID: "a"}).talkRoomNameFetcher(); fetch != nil {
		t.Error("talkRoomNameFetcher() without APP_SECRET != nil")
	}
}

func TestSanitizeTalkRoomName(t *testing.T) {
	long := strings.Repeat("é", talkRoomNameMaxLen+50)
	if got := len([]rune(sanitizeTalkRoomName(long))); got != talkRoomNameMaxLen {
		t.Errorf("clamped rune length = %d, want %d", got, talkRoomNameMaxLen)
	}
	if got := sanitizeTalkRoomName("Daily Meeting"); got != "Daily Meeting" {
		t.Errorf("short name changed: %q", got)
	}
	// Control characters must not survive into OpusTags / catalog entries.
	if got := sanitizeTalkRoomName("Daily\nMeeting\t\x00notes"); got != "Daily Meeting notes" {
		t.Errorf("control chars not collapsed: %q", got)
	}
	if got := sanitizeTalkRoomName("\x01\x02"); got != "" {
		t.Errorf("control-only name = %q, want empty", got)
	}
}

// seedTalkJob inserts a queued job with a bound Talk room so the room-name
// flow has real store rows and in-memory state to work against.
func seedTalkJob(t *testing.T, rt *Runtime, jobID string) *talkRoomState {
	t.Helper()
	now := nowUTCString()
	if err := rt.store.InsertQueuedJob(context.Background(), Job{
		ID:                   jobID,
		Provider:             "nextcloud-talk",
		RequestJSON:          "{}",
		Stage:                "record",
		State:                "queued",
		CurrentAttemptNumber: 1,
		CreatedAt:            now,
		UpdatedAt:            now,
	}); err != nil {
		t.Fatalf("InsertQueuedJob() error = %v", err)
	}
	state := &talkRoomState{
		RoomKey:    "backend|tok123",
		BackendURL: "http://backend",
		RoomToken:  "tok123",
		Owner:      "alice",
	}
	if !rt.reserveTalkRoom(state) {
		t.Fatal("reserveTalkRoom() = false")
	}
	rt.bindTalkRoomJob(state, jobID)
	bindingJSON, err := encodeTalkBinding(state)
	if err != nil {
		t.Fatalf("encodeTalkBinding() error = %v", err)
	}
	if err := rt.store.SetJobTalkBinding(context.Background(), jobID, bindingJSON); err != nil {
		t.Fatalf("SetJobTalkBinding() error = %v", err)
	}
	return state
}

func TestResolveTalkRoomNameStoresAndPersists(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	seedTalkJob(t, rt, "job-room-name")

	calls := 0
	rt.talkRoomNameRetryGap = time.Millisecond
	rt.fetchTalkRoomName = func(_ context.Context, owner, roomToken string) (talkRoomInfo, error) {
		calls++
		if owner != "alice" || roomToken != "tok123" {
			t.Errorf("fetch called with owner=%q token=%q", owner, roomToken)
		}
		if calls == 1 {
			// First attempt fails; the resolver must retry.
			return talkRoomInfo{}, errors.New("nextcloud briefly unreachable")
		}
		return talkRoomInfo{Name: "Daily Meeting"}, nil
	}

	rt.resolveTalkRoomName("job-room-name", "alice", "tok123")

	if got := rt.talkRoomNameForJob("job-room-name"); got != "Daily Meeting" {
		t.Errorf("talkRoomNameForJob() = %q, want %q (in-memory)", got, "Daily Meeting")
	}

	// The name must survive the in-memory state being dropped (operator
	// restart before build): talkRoomNameForJob falls back to the persisted
	// Talk binding.
	rt.recordMu.Lock()
	delete(rt.talkJobs, "job-room-name")
	rt.recordMu.Unlock()
	if got := rt.talkRoomNameForJob("job-room-name"); got != "Daily Meeting" {
		t.Errorf("talkRoomNameForJob() = %q, want %q (persisted binding)", got, "Daily Meeting")
	}
}

func TestResolveTalkRoomNameGivesUpAfterRetries(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	seedTalkJob(t, rt, "job-no-name")

	calls := 0
	rt.talkRoomNameRetryGap = time.Millisecond
	rt.fetchTalkRoomName = func(context.Context, string, string) (talkRoomInfo, error) {
		calls++
		return talkRoomInfo{}, errors.New("boom")
	}

	rt.resolveTalkRoomName("job-no-name", "alice", "tok123")

	if calls != talkRoomNameAttempts {
		t.Errorf("fetch calls = %d, want %d", calls, talkRoomNameAttempts)
	}
	if got := rt.talkRoomNameForJob("job-no-name"); got != "" {
		t.Errorf("talkRoomNameForJob() = %q, want empty after failed resolution", got)
	}
}

func TestResolveTalkRoomNameNoopWithoutFetcher(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	// fetchTalkRoomName stays nil (standalone deploy); must not panic.
	rt.resolveTalkRoomName("job-x", "alice", "tok123")
}

func TestRunBuildJobStampsTalkRoomNameIntoPromotedBundle(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	// The post-build pack goroutine shells out to cfg.CassiniBin inside the
	// test's temp WorkRoot; use an instant fake and wait for the goroutine so
	// TempDir cleanup never races an in-flight ffmpeg.
	rt.cfg.CassiniBin = writeFakeCassini(t, `printf 'opus-bytes' > "$4"; exit 0`)
	jobID := "job-titled-build"
	seedTalkJob(t, rt, jobID)
	rt.fetchTalkRoomName = func(context.Context, string, string) (talkRoomInfo, error) {
		return talkRoomInfo{Name: "Daily Meeting"}, nil
	}
	rt.resolveTalkRoomName(jobID, "alice", "tok123")

	runPath := attemptRunPath(rt.cfg.WorkRoot, jobID, 1)
	if err := rt.store.MarkBuildQueued(context.Background(), jobID, runPath, runPath, nowUTCString()); err != nil {
		t.Fatalf("MarkBuildQueued() error = %v", err)
	}

	// runBuildJob must stamp the room name into the promoted bundle's
	// manifest BEFORE publish is enqueued — `cassini publish` re-packs the
	// `.meeting` when the durable `.opus` isn't there yet, and the manifest
	// title is what names the very first publish of a fresh recording.
	rt.runBuildJob(buildTask{JobID: jobID, AttemptNumber: 1, ArtifactRunPath: runPath}, 1)
	rt.opusPackWG.Wait()

	manifest, ok, err := LoadMeetingBundleManifest(canonicalMeetingPath(rt.cfg.WorkRoot, jobID))
	if err != nil || !ok {
		t.Fatalf("LoadMeetingBundleManifest() = ok=%t err=%v", ok, err)
	}
	if manifest.Title != "Daily Meeting" {
		t.Errorf("promoted bundle title = %q, want %q", manifest.Title, "Daily Meeting")
	}
}

func TestTalkBindingRoundTripsRoomName(t *testing.T) {
	encoded, err := encodeTalkBinding(&talkRoomState{
		BackendURL: "http://backend",
		RoomToken:  "tok123",
		Owner:      "alice",
		RoomName:   "Daily Meeting",
	})
	if err != nil {
		t.Fatalf("encodeTalkBinding() error = %v", err)
	}
	state, err := decodeTalkBinding(encoded)
	if err != nil {
		t.Fatalf("decodeTalkBinding() error = %v", err)
	}
	if state.RoomName != "Daily Meeting" {
		t.Errorf("RoomName = %q, want %q", state.RoomName, "Daily Meeting")
	}
}
