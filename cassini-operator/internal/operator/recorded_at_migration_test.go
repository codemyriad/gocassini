package operator

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func (f *fileWorkerFixture) setState(t *testing.T, state string) {
	t.Helper()
	if _, err := f.w.rt.meetingMetadata.db.Exec(`UPDATE recorded_at_file_backfill SET state=?`, state); err != nil {
		t.Fatal(err)
	}
}

func (f *fileWorkerFixture) addRow(t *testing.T, name, state string) {
	t.Helper()
	if _, err := f.w.rt.meetingMetadata.db.Exec(`INSERT INTO recorded_at_file_backfill(opus_name, recorded_at_utc, state, updated_at) VALUES(?, ?, ?, 'now')`, name, testRecordedAt, state); err != nil {
		t.Fatal(err)
	}
}

// The index pass only enumerates: a needed file is never taken by a worker,
// so nothing is downloaded, re-tagged or uploaded until the administrator
// starts the migration.
func TestRecordedAtFilesWaitForTheAdministrator(t *testing.T) {
	f := newFileWorkerFixture(t, 0)
	ctx := context.Background()
	f.setState(t, recordedAtStateNeeded)
	if n := f.w.drain(ctx); n != 0 {
		t.Fatalf("drained %d needed files", n)
	}
	if _, owner := f.nc.gets(); len(owner) != 0 || cliRuns(t, f.bin, "retag") != 0 || len(f.nc.ifMatches()) != 0 {
		t.Fatal("a needed file was read, re-tagged or uploaded")
	}
	// Even processed directly (a claim taken before the state changed), a
	// needed row is left alone.
	f.w.processOne(ctx, "MEETING1.opus")
	if r := f.row(t); r.state != recordedAtStateNeeded || cliRuns(t, f.bin, "retag") != 0 {
		t.Fatalf("row = %+v", r)
	}

	n, err := f.w.startMigration(ctx)
	if err != nil || n != 1 {
		t.Fatalf("started %d %v", n, err)
	}
	if r := f.row(t); r.state != recordedAtStatePending {
		t.Fatalf("row after start = %+v", r)
	}
	if n := f.w.drain(ctx); n != 1 {
		t.Fatalf("drained %d after start", n)
	}
	if r := f.row(t); r.state != recordedAtStateDone || f.remote(t)["recordedAtUtc"] != testRecordedAt {
		t.Fatalf("row = %+v remote = %v", r, f.remote(t))
	}
}

// Start also retries what was blocked, and wakes the workers.
func TestRecordedAtMigrationStartRetriesBlockedAndWakes(t *testing.T) {
	f := newFileWorkerFixture(t, 0)
	ctx := context.Background()
	f.setState(t, recordedAtStateBlocked)
	f.addRow(t, "DONE.opus", recordedAtStateDone)
	f.addRow(t, "NEEDED.opus", recordedAtStateNeeded)
	n, err := f.w.startMigration(ctx)
	if err != nil || n != 2 {
		t.Fatalf("started %d %v, want 2", n, err)
	}
	if r := f.row(t); r.state != recordedAtStatePending || r.attempts != 0 || r.lastError != "" {
		t.Fatalf("blocked row = %+v", r)
	}
	if len(f.w.wake) != recordedAtFileWorkers {
		t.Fatalf("woke %d workers, want %d", len(f.w.wake), recordedAtFileWorkers)
	}
	var done string
	if err := f.w.rt.meetingMetadata.db.QueryRow(`SELECT state FROM recorded_at_file_backfill WHERE opus_name='DONE.opus'`).Scan(&done); err != nil || done != recordedAtStateDone {
		t.Fatalf("done row = %q %v", done, err)
	}
}

// Paused, no new file starts — not after a restart's requeue of blocked
// files either — while a file whose upload awaits settling is still
// settled. Starting again lifts the pause.
func TestRecordedAtMigrationPauseStopsNewFiles(t *testing.T) {
	f := newFileWorkerFixture(t, 2)
	ctx := context.Background()
	if err := f.w.pauseMigration(ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.w.drain(ctx); n != 0 || cliRuns(t, f.bin, "retag") != 0 {
		t.Fatalf("paused drain processed %d", n)
	}
	f.setState(t, recordedAtStateBlocked)
	if _, err := f.w.requeueBlocked(ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.w.drain(ctx); n != 0 {
		t.Fatalf("a requeued blocked file ran while paused (%d)", n)
	}
	f.setState(t, recordedAtStateUploading)
	if n := f.w.drain(ctx); n != 1 {
		t.Fatalf("an unsettled upload was not settled while paused (%d)", n)
	}
	f.setState(t, recordedAtStatePending)
	if _, err := f.w.startMigration(ctx); err != nil {
		t.Fatal(err)
	}
	if paused, err := f.w.rt.meetingMetadata.recordedAtFilesPaused(ctx); err != nil || paused {
		t.Fatalf("paused after start = %v %v", paused, err)
	}
	if n := f.w.drain(ctx); n != 1 {
		t.Fatalf("drain after start processed %d", n)
	}
}

// Paused, a file whose upload has not landed is put back as pending without
// an upload: no new version is written after the pause. That covers an
// uploading row whose earlier PUT failed outright (a 412, a 5xx, a timeout
// before it was sent) and a pending file taken just before the pause.
func TestRecordedAtMigrationPauseWritesNoNewVersion(t *testing.T) {
	for _, state := range []string{recordedAtStateUploading, recordedAtStatePending} {
		t.Run(state, func(t *testing.T) {
			f := newFileWorkerFixture(t, 0)
			ctx := context.Background()
			f.w.rt.recordedAtIndexChecked.Store(true)
			f.setState(t, state)
			if err := f.w.pauseMigration(ctx); err != nil {
				t.Fatal(err)
			}
			if state == recordedAtStateUploading {
				if got := f.w.status(ctx, false); got.State != "pausing" || got.Settling != 1 {
					t.Fatalf("status with an upload to settle = %+v", got)
				}
				if n := f.w.drain(ctx); n != 1 {
					t.Fatalf("drained %d, want the upload settled", n)
				}
			} else {
				// Claimed before the pause: processed directly.
				f.w.processOne(ctx, "MEETING1.opus")
			}
			if matches := f.nc.ifMatches(); len(matches) != 0 {
				t.Fatalf("a paused migration uploaded: %v", matches)
			}
			if r := f.row(t); r.state != recordedAtStatePending {
				t.Fatalf("row = %+v; logs: %s", r, f.logs)
			}
			if _, carried := f.remote(t)["recordedAtUtc"]; carried {
				t.Fatalf("remote changed: %v", f.remote(t))
			}
			if got := f.w.status(ctx, false); got.State != "paused" || got.Queued != 1 || got.Settling != 0 {
				t.Fatalf("status after = %+v", got)
			}
			if n := f.w.drain(ctx); n != 0 {
				t.Fatalf("paused drain took %d more", n)
			}
		})
	}
}

// A pause during a file lets that file finish, consistently, and stops the
// drain before the next one — including one that came in during the pace
// between two files.
func TestRecordedAtMigrationPauseLetsTheCurrentFileFinish(t *testing.T) {
	t.Run("during a file", func(t *testing.T) {
		f := newFileWorkerFixture(t, 0)
		ctx := context.Background()
		f.addRow(t, "OTHER.opus", recordedAtStatePending)
		f.w.pause = func(context.Context) bool { return true }
		f.w.afterAttempt = func(string) {
			if err := f.w.pauseMigration(ctx); err != nil {
				t.Error(err)
			}
			if got := f.w.status(ctx, false); got.InProgress != 1 || got.State != "pausing" {
				t.Errorf("status during the paused file = %+v", got)
			}
		}
		if n := f.w.drain(ctx); n != 1 {
			t.Fatalf("drained %d, want the current file only", n)
		}
		if r := f.row(t); r.state != recordedAtStateDone || f.remote(t)["recordedAtUtc"] != testRecordedAt {
			t.Fatalf("the file in progress did not finish: %+v", r)
		}
		var other string
		if err := f.w.rt.meetingMetadata.db.QueryRow(`SELECT state FROM recorded_at_file_backfill WHERE opus_name='OTHER.opus'`).Scan(&other); err != nil || other != recordedAtStatePending {
			t.Fatalf("other = %q %v", other, err)
		}
		if got := f.w.status(ctx, false); got.State != "paused" || got.Queued != 1 || got.InProgress != 0 {
			t.Fatalf("status after = %+v", got)
		}
	})
	t.Run("during the pace", func(t *testing.T) {
		f := newFileWorkerFixture(t, 0)
		ctx := context.Background()
		f.addRow(t, "OTHER.opus", recordedAtStatePending)
		f.w.pause = func(ctx context.Context) bool {
			if err := f.w.pauseMigration(ctx); err != nil {
				t.Error(err)
			}
			return true
		}
		if n := f.w.drain(ctx); n != 1 {
			t.Fatalf("drained %d after a pause during the pace", n)
		}
		if name, err := f.w.next(ctx); err != nil || name != "" {
			t.Fatalf("paused next = %q %v", name, err)
		}
	})
}

// A republish keeps the administrator's decision: a file nobody asked for
// stays needed, one somebody did is pending again.
func TestStampPublishedRecordedAtKeepsTheRequest(t *testing.T) {
	f := newRecordedAtFixture(t)
	ctx := context.Background()
	name := startedJob + ".opus"
	entry := `{"id":"` + startedJob + `","dateLabel":"2026-07-17 10:33","audioPath":"./meetings/` + name + `"}`
	f.job(t, startedJob, "2026-07-17T10:33:05Z")
	f.put(t, 3, name, entry)
	if _, err := f.rt.backfillRecordedAtIndex(ctx); err != nil {
		t.Fatal(err)
	}
	state := func() string {
		var s string
		if err := f.rt.meetingMetadata.db.QueryRow(`SELECT state FROM recorded_at_file_backfill WHERE opus_name=?`, name).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if got := state(); got != recordedAtStateNeeded {
		t.Fatalf("index pass queued %q, want needed", got)
	}
	f.put(t, 3, name, entry)
	if err := f.rt.stampPublishedRecordedAt(ctx, 3, name); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != recordedAtStateNeeded {
		t.Fatalf("republish of an unrequested file = %q", got)
	}
	for _, requested := range []string{recordedAtStatePending, recordedAtStateUploading, recordedAtStateBlocked, recordedAtStateDone} {
		if _, err := f.rt.meetingMetadata.db.Exec(`UPDATE recorded_at_file_backfill SET state=?`, requested); err != nil {
			t.Fatal(err)
		}
		f.put(t, 3, name, entry)
		if err := f.rt.stampPublishedRecordedAt(ctx, 3, name); err != nil {
			t.Fatal(err)
		}
		if got := state(); got != recordedAtStatePending {
			t.Fatalf("republish of a %s file = %q, want pending", requested, got)
		}
	}
}

// A start that commits while a republish is being stamped is not lost: the
// file comes out pending, not needed again.
func TestStampPublishedRecordedAtKeepsAStartThatRaced(t *testing.T) {
	f := newRecordedAtFixture(t)
	ctx := context.Background()
	name := startedJob + ".opus"
	entry := `{"id":"` + startedJob + `","dateLabel":"2026-07-17 10:33","audioPath":"./meetings/` + name + `"}`
	f.job(t, startedJob, "2026-07-17T10:33:05Z")
	f.put(t, 3, name, entry)
	if _, err := f.rt.backfillRecordedAtIndex(ctx); err != nil {
		t.Fatal(err)
	}
	f.put(t, 3, name, entry)
	worker := &recordedAtFileWorker{rt: f.rt}
	f.rt.recordedAtBeforeRepublishStamp = func() {
		if n, err := worker.startMigration(ctx); err != nil || n != 1 {
			t.Errorf("start during the republish = %d %v", n, err)
		}
	}
	if err := f.rt.stampPublishedRecordedAt(ctx, 3, name); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := f.rt.meetingMetadata.db.QueryRow(`SELECT state FROM recorded_at_file_backfill WHERE opus_name=?`, name).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != recordedAtStatePending {
		t.Fatalf("state = %q, want the start kept as pending", state)
	}
}

func TestRecordedAtMigrationStateRule(t *testing.T) {
	for _, tc := range []struct {
		s    recordedAtMigrationStatus
		want string
	}{
		{recordedAtMigrationStatus{}, "not_applicable"},
		{recordedAtMigrationStatus{Applicable: true}, "checking"},
		{recordedAtMigrationStatus{Applicable: true, Checked: true}, "not_needed"},
		{recordedAtMigrationStatus{Applicable: true, Checked: true, Done: 3}, "done"},
		{recordedAtMigrationStatus{Applicable: true, Needed: 3}, "needed"},
		{recordedAtMigrationStatus{Applicable: true, Checked: true, Needed: 3, Queued: 1}, "running"},
		{recordedAtMigrationStatus{Applicable: true, Checked: true, InProgress: 1}, "running"},
		{recordedAtMigrationStatus{Applicable: true, Checked: true, Paused: true, InProgress: 1, Queued: 4}, "pausing"},
		{recordedAtMigrationStatus{Applicable: true, Checked: true, Paused: true, Queued: 4}, "paused"},
		{recordedAtMigrationStatus{Applicable: true, Checked: true, Paused: true, Settling: 1, Queued: 4}, "pausing"},
		{recordedAtMigrationStatus{Applicable: true, Checked: true, Paused: true, Needed: 4}, "needed"},
		{recordedAtMigrationStatus{Applicable: true, Checked: true, Blocked: 1, Done: 2}, "blocked"},
	} {
		if got := recordedAtMigrationStateFor(tc.s); got != tc.want {
			t.Errorf("%+v -> %q, want %q", tc.s, got, tc.want)
		}
	}
}

// The status counts each state, names the blocked files with their meeting
// title and reason, and — while there is something to decide — carries the
// notification report, read once and reused.
func TestRecordedAtMigrationStatusCountsAndNotices(t *testing.T) {
	f := newFileWorkerFixture(t, 0)
	ctx := context.Background()
	f.w.rt.recordedAtIndexChecked.Store(true)
	reads := 0
	f.w.capabilities = func(context.Context) (nextcloudChangeCapabilities, error) {
		reads++
		return nextcloudChangeCapabilities{Version: "33.0.9", ActivityApp: true, NotificationsApp: true, Versioning: true}, nil
	}
	f.setState(t, recordedAtStateNeeded)
	f.addRow(t, "NEEDED2.opus", recordedAtStateNeeded)
	f.addRow(t, "DONE.opus", recordedAtStateDone)
	f.addRow(t, "BLOCKED.opus", recordedAtStateBlocked)
	if _, err := f.w.rt.meetingMetadata.db.Exec(`UPDATE recorded_at_file_backfill SET last_error='recording is missing; restore it and retry' WHERE opus_name='BLOCKED.opus'`); err != nil {
		t.Fatal(err)
	}
	if err := f.w.rt.meetingMetadata.Put(ctx, 43, "BLOCKED.opus", json.RawMessage(`{"id":"BLOCKED","title":"Weekly sync","dateLabel":"2026-07-01 09:00","audioPath":"./meetings/BLOCKED.opus"}`)); err != nil {
		t.Fatal(err)
	}
	s := f.w.status(ctx, true)
	if s.State != "needed" || s.Needed != 2 || s.Done != 1 || s.Blocked != 1 || s.Queued != 0 || !s.Checked || s.Workers != 2 || s.PaceSeconds != 30 {
		t.Fatalf("status = %+v", s)
	}
	if len(s.BlockedItems) != 1 || s.BlockedItems[0] != (recordedAtBlockedFile{Meeting: "BLOCKED.opus", Title: "Weekly sync", DateLabel: "2026-07-01 09:00", Reason: "recording is missing; restore it and retry"}) {
		t.Fatalf("blocked = %+v", s.BlockedItems)
	}
	if s.Notices == nil || !s.Notices.Known || *s.Notices.ActivityApp != true || s.Notices.NextcloudVersion != "33.0.9" {
		t.Fatalf("notices = %+v", s.Notices)
	}
	f.w.status(ctx, true)
	if reads != 1 {
		t.Fatalf("capabilities read %d times, want once within the cache", reads)
	}

	// Nothing to decide: no Nextcloud round trip, no report.
	if _, err := f.w.rt.meetingMetadata.db.Exec(`UPDATE recorded_at_file_backfill SET state='done'`); err != nil {
		t.Fatal(err)
	}
	if s := f.w.status(ctx, true); s.State != "done" || s.Notices != nil {
		t.Fatalf("done status = %+v", s)
	}
}

func TestParseChangeCapabilities(t *testing.T) {
	raw := `{"core":{},"files":{"bigfilechunking":true,"versioning":true},"activity":{"apiv2":["filters"]},"notifications":{"ocs-endpoints":["list"],"push":["devices","webpush"]}}`
	got, err := parseChangeCapabilities("33.0.9", json.RawMessage(raw))
	if err != nil || got != (nextcloudChangeCapabilities{Version: "33.0.9", ActivityApp: true, NotificationsApp: true, WebPush: true, Versioning: true}) {
		t.Fatalf("web push without browsers: got %+v %v", got, err)
	}
	raw = `{"notifications":{"push":["devices","webpush","webpush-browsers"]}}`
	got, err = parseChangeCapabilities("", json.RawMessage(raw))
	if err != nil || got != (nextcloudChangeCapabilities{NotificationsApp: true, WebPush: true, BrowserPush: true}) {
		t.Fatalf("browser push: got %+v %v", got, err)
	}
	// Providers exist only while their app is enabled.
	got, err = parseChangeCapabilities("", json.RawMessage(`{"core":{},"files":{"bigfilechunking":true}}`))
	if err != nil || got != (nextcloudChangeCapabilities{}) {
		t.Fatalf("got %+v %v", got, err)
	}
}

// The capabilities are read as a user from the OCS endpoint; a document with
// no capabilities is not "every app is disabled".
func TestChangeCapabilitiesReadsTheOCSDocument(t *testing.T) {
	body := `{"ocs":{"data":{"version":{"string":"33.0.9"},"capabilities":{"activity":{},"files":{"versioning":false}}}}}`
	var asUser string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ocs/v2.php/cloud/capabilities" {
			http.NotFound(w, r)
			return
		}
		asUser = r.Header.Get("AUTHORIZATION-APP-API")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	cfg := testExAppConfig(server.URL)
	got, err := cfg.changeCapabilities(context.Background(), server.Client())
	if err != nil || got != (nextcloudChangeCapabilities{Version: "33.0.9", ActivityApp: true}) {
		t.Fatalf("got %+v %v", got, err)
	}
	if asUser == "" {
		t.Fatal("capabilities were not read as a user")
	}
	body = `{"ocs":{"data":{"version":{"string":"33.0.9"}}}}`
	if _, err := cfg.changeCapabilities(context.Background(), server.Client()); err == nil {
		t.Fatal("a document without capabilities read as all apps disabled")
	}
}

// The report says what it established and nothing it could not: per-person
// settings are named as unknowable, and the defaults it quotes are
// Nextcloud's.
func TestRecordedAtNoticesWording(t *testing.T) {
	all := func(doc *recordedAtNotificationsDoc) string { return strings.Join(doc.Details, "\n") }
	on := recordedAtNotices(nextcloudChangeCapabilities{ActivityApp: true, NotificationsApp: true, WebPush: true, BrowserPush: true, Versioning: true}, nil)
	for _, want := range []string{"cassini account", "earlier version", "does not count against that account's quota", "versions_retention_obligation", "half the account's free space", "Activity app is enabled", "not emailed and not sent as a notification", "Administration settings › Activity", "Cassini cannot read either", "gets one per recording", "frequency they chose rather than one per change", "for browsers too"} {
		if !strings.Contains(all(on), want) {
			t.Errorf("enabled report lacks %q:\n%s", want, all(on))
		}
	}
	for _, wrong := range []string{"counts toward its storage", "an email or a notification for each recording"} {
		if strings.Contains(all(on), wrong) {
			t.Errorf("enabled report claims %q:\n%s", wrong, all(on))
		}
	}
	// Web push allowed is not browser push.
	webOnly := recordedAtNotices(nextcloudChangeCapabilities{ActivityApp: true, NotificationsApp: true, WebPush: true}, nil)
	if !strings.Contains(all(webOnly), "but not for browsers") || strings.Contains(all(webOnly), "for browsers too") || *webOnly.BrowserPush || !*webOnly.WebPush {
		t.Errorf("web-push-only report:\n%s", all(webOnly))
	}
	off := recordedAtNotices(nextcloudChangeCapabilities{}, nil)
	for _, want := range []string{"versioning is not enabled", "Activity app is not enabled", "Notifications app is not enabled"} {
		if !strings.Contains(all(off), want) {
			t.Errorf("disabled report lacks %q:\n%s", want, all(off))
		}
	}
	if strings.Contains(all(off), "not emailed") {
		t.Error("the activity defaults were quoted with the Activity app disabled")
	}
	unknown := recordedAtNotices(nextcloudChangeCapabilities{}, errors.New("boom"))
	if unknown.Known || unknown.ActivityApp != nil || unknown.Error == "" || !strings.Contains(all(unknown), "could not read") || !strings.Contains(all(unknown), "Nextcloud's default") {
		t.Fatalf("unknown report = %+v", unknown)
	}
}

func TestRecordedAtMigrationHandler(t *testing.T) {
	f := newFileWorkerFixture(t, 0)
	f.w.rt.recordedAtFiles.Store(f.w)
	f.w.capabilities = func(context.Context) (nextcloudChangeCapabilities, error) {
		return nextcloudChangeCapabilities{ActivityApp: true}, nil
	}
	f.setState(t, recordedAtStateNeeded)
	serve := func(method, path string) (*httptest.ResponseRecorder, recordedAtMigrationStatus) {
		rec := httptest.NewRecorder()
		f.w.rt.recordedAtMigrationHandler(rec, httptest.NewRequest(method, path, nil))
		var s recordedAtMigrationStatus
		_ = json.Unmarshal(rec.Body.Bytes(), &s)
		return rec, s
	}
	if rec, _ := serve(http.MethodGet, recordedAtMigrationPath+"/start"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET start = %d", rec.Code)
	}
	if rec, _ := serve(http.MethodPost, recordedAtMigrationPath+"/resume"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown action = %d", rec.Code)
	}
	// Not before the first count has finished: it would ask for part of
	// the archive only.
	if rec, _ := serve(http.MethodPost, recordedAtMigrationPath+"/start"); rec.Code != http.StatusConflict {
		t.Fatalf("start before the count = %d %s", rec.Code, rec.Body)
	}
	if r := f.row(t); r.state != recordedAtStateNeeded {
		t.Fatalf("a refused start queued the file: %+v", r)
	}
	f.w.rt.recordedAtIndexChecked.Store(true)
	rec, s := serve(http.MethodPost, recordedAtMigrationPath+"/start")
	if rec.Code != http.StatusOK || s.State != "running" || s.Queued != 1 || s.Notices == nil {
		t.Fatalf("start = %d %s", rec.Code, rec.Body)
	}
	rec, s = serve(http.MethodPost, recordedAtMigrationPath+"/pause/")
	if rec.Code != http.StatusOK || s.State != "paused" || !s.Paused {
		t.Fatalf("pause = %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(f.logs.String(), "file migration started") || !strings.Contains(f.logs.String(), "file migration paused") {
		t.Fatalf("admin actions not logged: %s", f.logs)
	}
}

// Mounted under the base path with the rest of the operator API, answering
// 409 where nothing can be rewritten; /status says not_applicable there.
func TestRecordedAtMigrationRoutesMountedUnderBasePath(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.computeProbe = func(string) (bool, string) { return true, "ok" }
	rt.cfg.BasePath = "/operator"
	handler := newHTTPHandler(log.New(ioDiscard{}, "", 0), rt, ExAppConfig{})
	for _, action := range []string{"start", "pause"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/operator/migrations/recorded-at/"+action, nil))
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s = %d %s", action, rec.Code, rec.Body)
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/operator/status", nil))
	var resp statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.RecordedAtMigration.State != "not_applicable" || resp.RecordedAtMigration.Applicable {
		t.Fatalf("status migration = %+v", resp.RecordedAtMigration)
	}
}

func TestRecordedAtMigrationRouteIsAdminOnly(t *testing.T) {
	b, err := os.ReadFile("../../../appinfo/info.xml")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Routes []struct {
			URL    string `xml:"url"`
			Verb   string `xml:"verb"`
			Access string `xml:"access_level"`
		} `xml:"external-app>routes>route"`
	}
	if err := xml.Unmarshal(b, &manifest); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range manifest.Routes {
		if strings.Contains(r.URL, "recorded-at") {
			found = true
			if r.Verb != "POST" || r.Access != "ADMIN" {
				t.Fatalf("route %+v", r)
			}
		}
		if r.URL == `^operator\/status\/?$` && (r.Verb != "GET" || r.Access != "ADMIN") {
			t.Fatalf("status route changed: %+v", r)
		}
	}
	if !found {
		t.Fatal("recorded-at migration ADMIN route missing")
	}
}
