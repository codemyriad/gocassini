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
	// An aged probe keeps its verdict and carries its age (D-798).
	//
	// This previously asserted the opposite — that an expired pass stopped
	// being presented at all. That made the verdict and its freshness the same
	// field, and since nothing re-probes on its own (readiness is a read; only
	// checkRecordingReadiness probes), "expired" was the resting state of any
	// idle panel rather than an exception. The concern it was written for is
	// kept, not dropped: a stale pass must never look current, which is now
	// enforced by requiring the timestamp rather than by deleting the result.
	probedAt := time.Now().Add(-2 * readinessTTL)
	rt.recordingSetup.checkedAt = probedAt
	report = rt.readiness(context.Background())
	aged := false
	for _, c := range report.Checks {
		if c.Code != "hpb_authenticated" {
			continue
		}
		aged = true
		if c.State != "passed" {
			t.Fatalf("aged probe lost its verdict: %+v", c)
		}
		if c.CheckedAt == "" {
			t.Fatalf("aged probe presented as current, with no age: %+v", c)
		}
		if got, want := c.CheckedAt, probedAt.UTC().Format(time.RFC3339); got != want {
			t.Fatalf("aged probe CheckedAt = %q, want when the probe ran (%q)", got, want)
		}
	}
	if !aged {
		t.Fatal("aged probe dropped entirely")
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
	if got, want := rt.publicRecordingState(context.Background()), rt.readiness(context.Background()).RecordingState; got != want || got == "passed" {
		t.Fatalf("public status=%s admin=%s; expired results must not pass", got, want)
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

func TestReadinessCachedNetworkFindingsNeverVetoRepairedConfiguration(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	putRecordingSetup(t, rt, `{"internal_secret":"internal","test_room_url":"https://cloud.test/call/testroom"}`, 200)
	req := TriggerRequest{TalkAuthMode: talkAuthModeHPBInternal, URL: "https://cloud.test/call/anotherroom"}
	for _, code := range []string{"hpb_missing", "hpb_unsupported", "signaling_auth_failed", "internal_clients_disabled", "recording_auth_rejected", "signaling_backend_rejected"} {
		rt.recordingSetup.checkedAt = time.Now()
		rt.recordingSetup.checks = []readinessCheck{{State: "needs_action", Code: code, Message: "Earlier diagnostic failed."}}
		if got := rt.recordingConfigurationRefusal(req); got != "" {
			t.Fatalf("cached %s vetoed possibly repaired setup: %s", code, got)
		}
	}
}

func TestReadinessUnreadableSetupDoesNotClaimMissingSecret(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	rt.recordingSetup.loaded = true
	rt.recordingSetup.loadFailed = true
	if got := rt.recordingConfigurationRefusal(TriggerRequest{TalkAuthMode: talkAuthModeHPBInternal}); !strings.Contains(got, "could not read") {
		t.Fatalf("misleading refusal: %s", got)
	}
	for _, c := range rt.readiness(context.Background()).Checks {
		if c.Code == "internal_secret_missing" {
			t.Fatal("unreadable credential reported as absent")
		}
	}
}

func TestReadinessSearchCoverageKeepsArchiveScopeUnknownAndRemediesSpecific(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	ctx := context.Background()
	searchCheck := func() readinessCheck {
		t.Helper()
		for _, check := range rt.readiness(ctx).Checks {
			if check.ID == "archive.search" {
				return check
			}
		}
		t.Fatal("archive.search check missing")
		return readinessCheck{}
	}
	hasBackfillCommand := func(check readinessCheck) bool {
		return strings.Contains(check.Message, "cassini-operator backfill-search")
	}

	if check := searchCheck(); check.Code != "search_coverage_empty" || check.State != "not_verified" {
		t.Fatalf("empty index check = %+v", check)
	}
	seedSearchable(t, rt.searchStore, "INDEXED.opus", seg("s1", "S1", 0, 1000, "words"))
	if check := searchCheck(); check.Code != "search_coverage_scope_unknown" || check.State != "not_verified" || !strings.Contains(check.Message, "archive has not been listed") {
		t.Fatalf("index alone claimed archive completeness: %+v", check)
	}
	if err := rt.searchStore.MarkUnavailable(ctx, "MISSING.opus", searchIngestReasonNoTranscript); err != nil {
		t.Fatal(err)
	}
	if check := searchCheck(); check.Code != "search_coverage_partial" || !strings.Contains(check.Message, "1 with missing transcripts") || hasBackfillCommand(check) {
		t.Fatalf("missing transcript got wrong reason or backfill remedy: %+v", check)
	}
	for name, reason := range map[string]string{
		"SILENT.opus":   searchIngestReasonCompletedEmpty,
		"DISABLED.opus": searchIngestReasonDisabled,
		"NO-MODEL.opus": searchIngestReasonModelUnavailable,
		"FAILED.opus":   searchIngestReasonTranscriptionFail,
	} {
		if err := rt.searchStore.MarkUnavailable(ctx, name, reason); err != nil {
			t.Fatal(err)
		}
	}
	check := searchCheck()
	for _, fragment := range []string{"1 silent after completed transcription", "1 intentionally without transcription", "1 without transcription because a model was unavailable", "1 with failed transcription"} {
		if !strings.Contains(check.Message, fragment) {
			t.Errorf("check message %q missing %q", check.Message, fragment)
		}
	}
	if hasBackfillCommand(check) {
		t.Errorf("untranscribed recordings were prescribed index backfill: %+v", check)
	}
	if err := rt.searchStore.MarkUnavailable(ctx, "NO-BUNDLE.opus", searchBackfillReasonNoBundle); err != nil {
		t.Fatal(err)
	}
	if check := searchCheck(); !hasBackfillCommand(check) || !strings.Contains(check.Message, "1 awaiting archive or bundle verification") {
		t.Fatalf("real backfill candidate lacks its remedy: %+v", check)
	}
}

func TestReadinessSearchCoverageCanPassAgainstLocalCatalogAndDetectLegacyGaps(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	if err := os.MkdirAll(rt.cfg.SiteRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCatalog := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(rt.cfg.SiteRoot, "catalog.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	check := func() readinessCheck {
		t.Helper()
		for _, row := range rt.readiness(context.Background()).Checks {
			if row.ID == "archive.search" {
				return row
			}
		}
		t.Fatal("archive.search missing")
		return readinessCheck{}
	}
	seedSearchable(t, rt.searchStore, "LIVE.opus", seg("s1", "S1", 0, 1000, "words"))
	seedSearchable(t, rt.searchStore, "STALE.opus", seg("s1", "S1", 0, 1000, "old words"))
	writeCatalog(`{"meetings":[{"id":"LIVE","audioPath":"./meetings/LIVE.opus"}]}`)
	if row := check(); row.State != "passed" || row.Code != "search_archive_files_accounted_for" || row.CheckedAt == "" || strings.Contains(row.Message, "2 indexed") {
		t.Fatalf("catalog-matched coverage = %+v; stale index rows must not offset archive names", row)
	}
	writeCatalog(`{"meetings":[{"id":"LIVE","audioPath":"./meetings/LIVE.opus"},{"id":"OLD","audioPath":"./meetings/OLD.opus"}]}`)
	if row := check(); row.State != "warn" || !strings.Contains(row.Message, "1 archive Opus recordings without index rows") {
		t.Fatalf("untracked legacy Opus meeting = %+v", row)
	}
	writeCatalog(`{"meetings":[{"id":"LIVE","audioPath":"./meetings/LIVE.opus"},{"id":"LEGACY","artifactPath":"./meetings/LEGACY"}]}`)
	if row := check(); row.State != "warn" || !strings.Contains(row.Message, "1 legacy or non-Opus archive entries outside search") {
		t.Fatalf("legacy directory-shaped meeting = %+v", row)
	}
}

// Holding the only search DB connection turns any coverage query into a wait.
// The public route needs only the core recording verdict, so it must finish
// without consulting the archive even when no admin coverage result is cached.
func TestPublicSetupDoesNotQuerySearchCoverage(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	rt.searchStore.db.SetMaxOpenConns(1)
	conn, err := rt.searchStore.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		rt.setupHandler(rec, httptest.NewRequest(http.MethodGet, "/setup", nil))
		done <- rec
	}()
	select {
	case rec := <-done:
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"recording_state"`) {
			t.Fatalf("public setup = %d %s", rec.Code, rec.Body.String())
		}
	case <-time.After(time.Second):
		_ = conn.Close()
		<-done
		t.Fatal("public setup waited for the search database")
	}
}

func TestReadinessPollReusesCoverageAndIndexWritesInvalidateIt(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	if err := os.MkdirAll(rt.cfg.SiteRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rt.cfg.SiteRoot, "catalog.json"), []byte(`{"meetings":[{"audioPath":"./meetings/LIVE.opus"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	seedSearchable(t, rt.searchStore, "LIVE.opus", seg("s1", "S1", 0, 1000, "words"))
	searchCheck := func() readinessCheck {
		t.Helper()
		for _, check := range rt.readiness(ctx).Checks {
			if check.ID == "archive.search" {
				return check
			}
		}
		t.Fatal("archive.search check missing")
		return readinessCheck{}
	}
	if check := searchCheck(); check.State != "passed" {
		t.Fatalf("initial coverage = %+v", check)
	}
	rt.searchStore.db.SetMaxOpenConns(1)
	conn, err := rt.searchStore.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		rt.readinessHandler(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		done <- rec
	}()
	select {
	case rec := <-done:
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"search_archive_files_accounted_for"`) {
			t.Fatalf("cached health = %d %s", rec.Code, rec.Body.String())
		}
	case <-time.After(time.Second):
		_ = conn.Close()
		<-done
		t.Fatal("repeated health GET waited for a second coverage scan")
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rt.searchStore.MarkUnavailable(ctx, "LIVE.opus", searchIngestReasonNoTranscript); err != nil {
		t.Fatal(err)
	}
	if check := searchCheck(); check.State != "warn" || check.Code != "search_coverage_partial" {
		t.Fatalf("index write left a cached green verdict: %+v", check)
	}
	seedSearchable(t, rt.searchStore, "LIVE.opus", seg("s2", "S2", 0, 1000, "new words"))
	if check := searchCheck(); check.State != "passed" {
		t.Fatalf("replacement did not refresh coverage: %+v", check)
	}
	if err := rt.searchStore.ForgetMeeting(ctx, "LIVE.opus"); err != nil {
		t.Fatal(err)
	}
	if check := searchCheck(); check.State != "warn" || !strings.Contains(check.Message, "without index rows") {
		t.Fatalf("forget left a cached green verdict: %+v", check)
	}
}

func TestExplicitHealthCheckRefreshesCoverageAfterExternalIndexWrite(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	if check := rt.readiness(context.Background()); check.State == "" {
		t.Fatal("initial readiness missing")
	}
	// A separate index writer cannot increment this Runtime's in-memory
	// revision. A normal GET may use the bounded cache; explicit check must not.
	_, err := rt.searchStore.db.Exec(`INSERT INTO meeting_index (opus_name, state, reason, indexed_at) VALUES (?, ?, ?, ?)`,
		"MISSING.opus", searchStateUnavailable, searchIngestReasonNoTranscript, nowUTCString())
	if err != nil {
		t.Fatal(err)
	}
	rt.recordingSetup.mu.Lock()
	rt.recordingSetup.checkedAt = time.Now() // coalesce host/network work in this test
	rt.recordingSetup.mu.Unlock()
	get := httptest.NewRecorder()
	rt.readinessHandler(get, httptest.NewRequest(http.MethodGet, "/health", nil))
	if strings.Contains(get.Body.String(), `"search_coverage_partial"`) {
		t.Fatalf("ordinary GET unexpectedly bypassed coverage cache: %s", get.Body.String())
	}
	post := httptest.NewRecorder()
	rt.readinessHandler(post, httptest.NewRequest(http.MethodPost, "/health/check", nil))
	if post.Code != http.StatusOK || !strings.Contains(post.Body.String(), `"search_coverage_partial"`) {
		t.Fatalf("explicit check did not refresh archive coverage: %d %s", post.Code, post.Body.String())
	}
}

func TestSearchCoverageCacheExpiresForExternalIndexWrites(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	ctx := context.Background()
	if check := rt.cachedSearchReadinessCheck(ctx); check.Code != "search_coverage_empty" {
		t.Fatalf("initial search coverage = %+v", check)
	}
	// Model a second process writing the sidecar: it has no access to this
	// Runtime's revision counter, so the TTL is the fallback invalidation.
	_, err := rt.searchStore.db.Exec(`INSERT INTO meeting_index (opus_name, state, reason, indexed_at) VALUES (?, ?, ?, ?)`,
		"MISSING.opus", searchStateUnavailable, searchIngestReasonNoTranscript, nowUTCString())
	if err != nil {
		t.Fatal(err)
	}
	if check := rt.cachedSearchReadinessCheck(ctx); check.Code != "search_coverage_empty" {
		t.Fatalf("unexpired cache unexpectedly changed: %+v", check)
	}
	rt.searchReadiness.mu.Lock()
	rt.searchReadiness.at = time.Now().Add(-searchReadinessCacheTTL - time.Second)
	rt.searchReadiness.mu.Unlock()
	if check := rt.cachedSearchReadinessCheck(ctx); check.Code != "search_coverage_partial" {
		t.Fatalf("expired cache did not reveal external index write: %+v", check)
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
	// main moved route registration into a table, so mounting now takes the
	// patterns explicitly rather than deriving them (operatorAPIRoutes). Naming
	// them here makes the test stricter than it was: it now fails if a readiness
	// route is dropped from the table, not merely if the mount is wrong.
	readinessRoutes := []string{"/health", "/health/check", "/talk/setup"}
	for _, base := range []string{"", "/", "/operator"} {
		root := http.NewServeMux()
		mountBasePathOnto(root, base, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }), readinessRoutes)
		for _, route := range readinessRoutes {
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
	putRecordingSetup(t, rt, `{"internal_secret":"internal","test_room_url":"https://cloud.test/call/testroom"}`, 200)
	rt.recordingSetup.checkedAt = time.Now()
	for _, checks := range [][]readinessCheck{
		{{State: "not_verified"}, {State: "needs_action"}},
		{{State: "needs_action"}, {State: "not_verified"}},
	} {
		rt.recordingSetup.checks = checks
		if rt.publicRecordingState(context.Background()) != "needs_action" {
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

func TestReadinessStorageCarriesTheAgeAndApplicabilityOfItsEvidence(t *testing.T) {
	resetSubstrateRecord(t)
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	ncAccessSubstrate.mu.Lock()
	ncAccessSubstrate.applicable = false
	ncAccessSubstrate.mu.Unlock()
	storage := func() readinessCheck {
		for _, c := range rt.readiness(context.Background()).Checks {
			if c.ID == "storage" {
				return c
			}
		}
		t.Fatal("missing storage check")
		return readinessCheck{}
	}
	if c := storage(); c.State != "not_verified" || c.Code != "storage_not_probed" {
		t.Fatalf("inapplicable check claimed storage readiness: %+v", c)
	}
	ncAccessSubstrate.markApplicable()
	ncAccessSubstrate.record(ncSubstrateProvisioned, "test preflight", nil)
	if c := storage(); c.State != "passed" || c.CheckedAt == "" {
		t.Fatalf("fresh preflight missing timestamp: %+v", c)
	}
	// Aged evidence keeps its verdict and carries its age (D-798), rather than
	// being erased into "not verified". The property this test was written for
	// — stale evidence must not masquerade as current — is enforced by
	// requiring the timestamp, which is what lets a reader judge it.
	//
	// Safe because this route reports rather than authorises: admission is
	// decided by ncAccessSubstrate.recordingRefusal(), checked earlier in
	// readiness() and not bounded by this TTL. See the admission-block test
	// below, which still requires that path to be actionable.
	aged := time.Now().Add(-2 * readinessTTL).UTC().Format(time.RFC3339)
	ncAccessSubstrate.mu.Lock()
	ncAccessSubstrate.checkedAtUTC = aged
	ncAccessSubstrate.mu.Unlock()
	if c := storage(); c.State != "passed" || c.CheckedAt != aged {
		t.Fatalf("aged preflight lost its verdict or its age: %+v", c)
	}

	// Never checked at all is an absence, not a verdict: no parseable
	// timestamp means no check has run here.
	ncAccessSubstrate.mu.Lock()
	ncAccessSubstrate.checkedAtUTC = ""
	ncAccessSubstrate.mu.Unlock()
	if c := storage(); c.State != "not_verified" || c.Code != "storage_not_checked" {
		t.Fatalf("unchecked storage reported as a verdict: %+v", c)
	}
}

func TestReadinessKeepsCurrentStorageAdmissionBlockActionable(t *testing.T) {
	resetSubstrateRecord(t)
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	ncAccessSubstrate.record(ncSubstrateUnavailable, "storage prerequisite", nil)
	ncAccessSubstrate.mu.Lock()
	ncAccessSubstrate.checkedAtUTC = time.Now().Add(-2 * readinessTTL).UTC().Format(time.RFC3339)
	ncAccessSubstrate.mu.Unlock()
	for _, c := range rt.readiness(context.Background()).Checks {
		if c.ID == "storage" {
			if c.State != "needs_action" || c.Code != "storage_admission_blocked" {
				t.Fatalf("hidden admission block: %+v", c)
			}
			return
		}
	}
	t.Fatal("missing storage check")
}

// D-798 V2: media host checks come from `cassini doctor --json`, read by id.
func TestRunDoctorProbeMapsTheDoctorLadder(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	rt.cfg.CassiniBin = writeFakeDoctorBin(t, `[
	  {"id":"ffmpeg","status":"ok","summary":"ffmpeg available"},
	  {"id":"workdir.space","status":"warn","summary":"low disk space","advice":"free disk space"},
	  {"id":"tmpdir.space","status":"fail","summary":"out of space","advice":"free some"}]`)

	checks, err := rt.runDoctorProbe(context.Background())
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	want := map[string]string{"host.ffmpeg": "passed", "host.workdir.space": "warn", "host.tmpdir.space": "needs_action"}
	got := map[string]string{}
	for _, c := range checks {
		got[c.ID] = c.State
	}
	for id, state := range want {
		if got[id] != state {
			t.Errorf("%s = %q, want %q", id, got[id], state)
		}
	}
	// A reader told what is wrong is owed what to do about it (R0.1).
	for _, c := range checks {
		if c.State == "passed" {
			continue
		}
		if !strings.Contains(c.Message, "—") {
			t.Errorf("%s dropped doctor's advice: %q", c.ID, c.Message)
		}
	}
}

func TestRunDoctorProbeChecksMediaOnTheRecordingWorkVolume(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	workVolume := t.TempDir()
	rt.cfg.WorkRoot = filepath.Join(workVolume, "jobs")
	bin := filepath.Join(t.TempDir(), "cassini")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '[{\"id\":\"workdir\",\"status\":\"ok\",\"summary\":\"%s|%s\"}]\\n' \"$(pwd)\" \"$*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt.cfg.CassiniBin = bin
	checks, err := rt.runDoctorProbe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := workVolume + "|doctor --target media --json"
	if len(checks) != 1 || checks[0].Message != want {
		t.Fatalf("doctor checked %v, want %q", checks, want)
	}
}

func TestRunDoctorProbeDoesNotPassOperatorSecrets(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	for name := range operatorOnlySecretEnv() {
		t.Setenv(name, "sensitive")
	}
	bin := filepath.Join(t.TempDir(), "cassini")
	body := `#!/bin/sh
if [ -n "${APP_SECRET+x}" ] || [ -n "${CASSINI_TALK_RECORDING_SECRET+x}" ] || [ -n "${TALK_RECORDING_SECRET+x}" ] || [ -n "${CASSINI_TALK_SIGNALING_INTERNAL_SECRET+x}" ] || [ -n "${CASSINI_OPERATOR_API_TOKEN+x}" ]; then
  printf '[{"id":"secrets","status":"fail","summary":"secret leaked"}]\n'
else
  printf '[{"id":"secrets","status":"ok","summary":"operator secrets absent"}]\n'
fi
`
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	rt.cfg.CassiniBin = bin
	checks, err := rt.runDoctorProbe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].State != "passed" {
		t.Fatalf("doctor received operator secrets: %+v", checks)
	}
}

// A doctor this operator cannot read is not evidence the host is healthy.
func TestRunDoctorProbeRefusesWhatItCannotUnderstand(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	for name, body := range map[string]string{
		"unknown status": `[{"id":"ffmpeg","status":"probably-fine","summary":"x"}]`,
		"no id":          `[{"id":"","status":"ok","summary":"x"}]`,
		"empty array":    `[]`,
		"not json":       `not json at all`,
	} {
		rt.cfg.CassiniBin = writeFakeDoctorBin(t, body)
		if _, err := rt.runDoctorProbe(context.Background()); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// R2.3: a source that could not be reached is a finding about the check, never
// a verdict about the thing it was going to check.
func TestReadinessReportsUnreachableHostChecksAsAWarning(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	rt.cfg.CassiniBin = filepath.Join(t.TempDir(), "does-not-exist")
	rt.checkRecordingReadiness(context.Background())

	report := rt.readiness(context.Background())
	found := false
	for _, c := range report.Checks {
		if c.Code == "host_checks_unavailable" {
			found = true
			if c.State != "warn" {
				t.Errorf("unreachable host checks reported as %q, want warn", c.State)
			}
		}
	}
	if !found {
		t.Fatal("an unreachable doctor was reported as nothing at all")
	}
}

func TestHealthGETUsesCachedMediaHostAndExplicitCheckRefreshesIt(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	rt.cfg.CassiniBin = writeFakeDoctorBin(t, `[{"id":"ffmpeg","status":"ok","summary":"ffmpeg available"}]`)
	rt.checkRecordingReadiness(context.Background())

	// Removing the binary proves GET does not invoke doctor again.
	rt.cfg.CassiniBin = filepath.Join(t.TempDir(), "does-not-exist")
	for i := 0; i < 2; i++ {
		report := rt.readiness(context.Background())
		found := false
		for _, check := range report.Checks {
			if check.ID == "host.ffmpeg" {
				found = true
				if check.State != "passed" || check.CheckedAt == "" {
					t.Fatalf("cached host finding lost its verdict or age: %+v", check)
				}
			}
		}
		if !found {
			t.Fatal("GET did not return the cached host finding")
		}
	}

	// The next explicit check refreshes the finding. The short coalescing
	// window applies to rapid duplicate clicks, so age it for this test.
	rt.recordingSetup.mu.Lock()
	rt.recordingSetup.checkedAt = time.Time{}
	rt.recordingSetup.mu.Unlock()
	rt.checkRecordingReadiness(context.Background())
	for _, check := range rt.readiness(context.Background()).Checks {
		if check.Code == "host_checks_unavailable" {
			if check.State != "warn" || check.CheckedAt == "" {
				t.Fatalf("explicit refresh did not report inaccessible doctor: %+v", check)
			}
			return
		}
	}
	t.Fatal("explicit check retained stale host pass")
}

// warn sits between passed and needs_action and must not collapse into either.
func TestWorstReadinessStateOrdersByWhatItCostsToIgnore(t *testing.T) {
	check := func(state string) readinessCheck { return readinessCheck{State: state} }
	for _, tc := range []struct {
		name   string
		checks []readinessCheck
		want   string
	}{
		{"all passing", []readinessCheck{check("passed"), check("passed")}, "passed"},
		{"a warning is not a pass", []readinessCheck{check("passed"), check("warn")}, "warn"},
		{"a warning is not a failure", []readinessCheck{check("warn")}, "warn"},
		{"a failure outranks a warning", []readinessCheck{check("warn"), check("needs_action")}, "needs_action"},
		{"a warning outranks nothing-established", []readinessCheck{check("not_verified"), check("warn")}, "warn"},
		{"nothing-established outranks a pass", []readinessCheck{check("passed"), check("not_verified")}, "not_verified"},
		{"order does not matter", []readinessCheck{check("needs_action"), check("passed"), check("warn")}, "needs_action"},
		{"nothing at all", nil, "passed"},
	} {
		if got := worstReadinessState(tc.checks); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestRecordingCapabilityStateExcludesOptionalProcessingAndArchive(t *testing.T) {
	checks := []readinessCheck{
		{ID: "storage", State: "passed"},
		{ID: "talk.hpb", State: "passed"},
		{ID: "processing", State: "warn", Code: "transcription_unavailable"},
		{ID: "archive.search", State: "warn", Code: "search_coverage_partial"},
	}
	if got := recordingCapabilityState(checks); got != "passed" {
		t.Fatalf("optional warnings made audio recording look broken: %s", got)
	}
	checks = append(checks, readinessCheck{ID: "host.ffmpeg", State: "needs_action"})
	if got := recordingCapabilityState(checks); got != "needs_action" {
		t.Fatalf("recording host failure was hidden: %s", got)
	}
}

func TestUnavailableEnabledTranscriptionWarnsWithoutBlockingAudio(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	rt.setSettings(STTSettings{
		TranscriptionEnabled: true,
		Quality:              sttQualityBalanced,
		DeviceOverride:       deviceCUDA,
	})
	report := rt.readiness(context.Background())
	for _, check := range report.Checks {
		if check.ID != "processing" {
			continue
		}
		if check.State != "warn" || check.Code != "transcription_unavailable" || check.Action != "settings" {
			t.Fatalf("enabled but unavailable transcription: %+v", check)
		}
		if report.RecordingState != recordingCapabilityState(report.Checks) {
			t.Fatalf("recording state included optional processing: %s", report.RecordingState)
		}
		return
	}
	t.Fatal("no optional processing check")
}

func writeFakeDoctorBin(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cassini")
	if err := os.WriteFile(path, []byte("#!/bin/sh\ncat <<'JSON'\n"+body+"\nJSON\n"), 0o755); err != nil {
		t.Fatalf("write fake doctor: %v", err)
	}
	return path
}

// The route table and the handler's own branching must name the same paths.
//
// They did not, briefly: the table was renamed to /health while the handler
// still matched /readiness, which answers 405 for every request the mux sends
// it. The mount test could not catch that — it checks that a path REACHES the
// handler, not that the handler accepts it.
func TestHealthHandlerAnswersAtTheRouteItIsRegisteredAt(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()

	registered := []string{}
	for _, route := range operatorAPIRoutes(rt, ExAppConfig{}) {
		if route.pattern == "/health" || route.pattern == "/health/check" {
			registered = append(registered, route.pattern)
		}
	}
	if len(registered) != 2 {
		t.Fatalf("health routes registered = %v, want /health and /health/check", registered)
	}

	rec := httptest.NewRecorder()
	rt.readinessHandler(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /health = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	rt.readinessHandler(rec, httptest.NewRequest(http.MethodPost, "/health/check", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("POST /health/check = %d, want 200", rec.Code)
	}

	// The old name is gone, not aliased: an alias is debt, and this route had
	// one caller when it was renamed.
	rec = httptest.NewRecorder()
	rt.readinessHandler(rec, httptest.NewRequest(http.MethodGet, "/readiness", nil))
	if rec.Code == http.StatusOK {
		t.Error("the old /readiness path still answers")
	}
}
