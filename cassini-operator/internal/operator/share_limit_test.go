package operator

import (
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func (f *directPublishFixture) setAllow(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.allow = n
}

// takePosted returns the creates asked for since the last call.
func (f *directPublishFixture) takePosted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.posted
	f.posted = nil
	return out
}

func (f *directPublishFixture) unshare(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.shares, key)
}

func ids(entries []skippedShare) []string {
	out := []string{}
	for _, entry := range entries {
		out = append(out, entry.Type+"/"+entry.ID)
	}
	return out
}

// Nextcloud answers its limit on new shares with HTTP 429 and nothing else.
// That is its own error, not a reply Cassini failed to read.
func TestTheShareLimitIsItsOwnErrorNotAnUnreadableReply(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	cfg := ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppVersion: "1", AppSecret: "secret"}
	_, err := cfg.shareCall(context.Background(), server.Client(), ncRecordingsOwner, http.MethodPost, cfg.shareAPIURL(), nil)
	if !shareRateLimited(err) || strings.Contains(err.Error(), "decode") || recipientShareRefusal(err) {
		t.Fatalf("429 = %v", err)
	}
	if !strings.Contains(err.Error(), "limit on new shares") {
		t.Fatalf("429 does not say what it is: %v", err)
	}
}

// The live report: a room past 20 recipients. Publication shares with as many
// as the limit allows, keeps who Nextcloud refused apart from who the limit
// held back, asks for nothing after the limit answers, and succeeds.
func TestPublishPastTheShareLimitSucceedsAndDefersTheRest(t *testing.T) {
	// The captured room is walked groups first, then people, by name.
	f := newDirectPublishFixture(t, []sharePrincipal{
		{Type: "user", ID: "bob"}, {Type: "user", ID: "carol"}, {Type: "user", ID: "dave"},
		{Type: "user", ID: "erin"}, {Type: "group", ID: "marketing"},
	}, "bob")
	// alice and marketing land, bob is refused (and counts), carol meets the
	// limit.
	f.setAllow(3)
	if err := f.deliver(); err != nil {
		t.Fatalf("publication failed at the share limit: %v", err)
	}
	if got := f.takePosted(); !reflect.DeepEqual(got, []string{"alice", "marketing", "bob", "carol"}) {
		t.Fatalf("creates asked for = %v; want nothing retried or tried after the limit", got)
	}
	outcome := f.outcome(t)
	if outcome == nil || outcome.Status != shareOutcomePartial ||
		!reflect.DeepEqual(ids(outcome.Skipped), []string{"user/bob"}) ||
		!reflect.DeepEqual(ids(outcome.Deferred), []string{"user/carol", "user/dave", "user/erin"}) {
		t.Fatalf("outcome = %+v", outcome)
	}
	if outcome.Deferred[0].ShareLimit != "" || outcome.Deferred[0].Reason != "" {
		t.Fatalf("a deferred entry carries storage details: %+v", outcome.Deferred[0])
	}
	// The run list flags everyone not shared with, the deferred included,
	// so an administrator can find the meeting to re-share.
	if counts, err := f.rt.store.partiallySharedJobs(context.Background()); err != nil || counts[directPublishJob] != 4 {
		t.Fatalf("run list count = %v, %v", counts, err)
	}
	if !f.shared("0/alice") || !f.shared("1/marketing") || f.shared("0/carol") {
		t.Fatalf("shares = %v", f.shares)
	}
}

// The limit is not a refusal of the starter: the meeting publishes, and the
// starter waits with everyone else.
func TestARateLimitedStarterStillPublishes(t *testing.T) {
	f := newDirectPublishFixture(t, []sharePrincipal{{Type: "user", ID: "bob"}})
	f.setAllow(0)
	if err := f.deliver(); err != nil {
		t.Fatalf("publication failed when the starter met the limit: %v", err)
	}
	if got := f.takePosted(); !reflect.DeepEqual(got, []string{"alice"}) {
		t.Fatalf("creates asked for = %v", got)
	}
	outcome := f.outcome(t)
	if outcome == nil || outcome.Status != shareOutcomeDeferred || len(outcome.Skipped) != 0 ||
		!reflect.DeepEqual(ids(outcome.Deferred), []string{"user/alice", "user/bob"}) {
		t.Fatalf("outcome = %+v", outcome)
	}
	if counts, _ := f.rt.store.partiallySharedJobs(context.Background()); counts[directPublishJob] != 2 {
		t.Fatalf("a deferral-only outcome is not flagged for a re-share: %v", counts)
	}
}

// The administrator's re-share is the way to the deferred: one that meets the
// limit again leaves the rest deferred, one that gets through clears them,
// and the refusals stand apart throughout.
func TestAReshareFinishesTheDeferredOnceTheLimitAllows(t *testing.T) {
	f := newDirectPublishFixture(t, []sharePrincipal{
		{Type: "user", ID: "bob"}, {Type: "user", ID: "carol"}, {Type: "user", ID: "dave"}, {Type: "group", ID: "marketing"},
	}, "dave")
	f.setAllow(2)
	if err := f.deliver(); err != nil {
		t.Fatal(err)
	}
	f.markJobSucceeded(t)
	// alice and marketing landed; bob met the limit.
	if got := f.takePosted(); !reflect.DeepEqual(got, []string{"alice", "marketing", "bob"}) {
		t.Fatalf("publication asked for %v", got)
	}

	// Room for one more: bob lands, carol meets the limit again.
	f.setAllow(1)
	rec, body := f.reshare(t)
	if got := f.takePosted(); !reflect.DeepEqual(got, []string{"bob", "carol"}) {
		t.Fatalf("first re-share asked for %v", got)
	}
	if rec.Code != http.StatusOK || body.Sharing == nil || body.Sharing.Status != shareOutcomeDeferred ||
		!reflect.DeepEqual(ids(body.Sharing.Deferred), []string{"user/carol", "user/dave"}) {
		t.Fatalf("re-share at the limit again = %d %s", rec.Code, rec.Body.String())
	}
	if summary, _ := f.rt.store.shareLimitedSummary(context.Background()); summary == nil || summary.Jobs != 1 {
		t.Fatalf("share limit summary while waiting = %+v", summary)
	}

	// The limit allows it: carol lands, dave is refused, nobody waits.
	f.setAllow(-1)
	rec, body = f.reshare(t)
	if rec.Code != http.StatusOK || body.Sharing == nil || body.Sharing.Status != shareOutcomePartial ||
		len(body.Sharing.Deferred) != 0 || !reflect.DeepEqual(ids(body.Sharing.Skipped), []string{"user/dave"}) {
		t.Fatalf("re-share past the limit = %d %s", rec.Code, rec.Body.String())
	}
	if !f.shared("0/bob") || !f.shared("0/carol") {
		t.Fatalf("shares = %v", f.shares)
	}
	// Nothing waiting: Doctor's line goes.
	if summary, _ := f.rt.store.shareLimitedSummary(context.Background()); summary != nil {
		t.Fatalf("share limit summary once nobody waits = %+v", summary)
	}

	// The policy is fixed too: complete.
	f.setRefused()
	if _, body = f.reshare(t); body.Sharing == nil || body.Sharing.Status != shareOutcomeComplete {
		t.Fatalf("re-share with nobody left = %+v", body.Sharing)
	}
}

// An administrator's re-share at the limit answers with who is still to
// come, not with an error.
func TestAReshareAtTheLimitReturnsWhoIsDeferred(t *testing.T) {
	f := newDirectPublishFixture(t, []sharePrincipal{{Type: "user", ID: "bob"}, {Type: "user", ID: "carol"}})
	if err := f.deliver(); err != nil {
		t.Fatal(err)
	}
	f.markJobSucceeded(t)
	f.unshare("0/bob")
	f.unshare("0/carol")
	f.setAllow(0)
	rec, body := f.reshare(t)
	if rec.Code != http.StatusOK || body.Sharing == nil || body.Sharing.Status != shareOutcomeDeferred ||
		len(body.Sharing.Skipped) != 0 || !reflect.DeepEqual(ids(body.Sharing.Deferred), []string{"user/bob", "user/carol"}) {
		t.Fatalf("reshare at the limit = %d %s", rec.Code, rec.Body.String())
	}
}

func resetShareLimit() {
	ncShareLimit.mu.Lock()
	ncShareLimit.last = time.Time{}
	ncShareLimit.now = nil
	ncShareLimit.mu.Unlock()
}

// shareServer is a Nextcloud share API that lets allow creates through, then
// answers with its limit. It counts the creates asked for and keeps the
// shares made, which a list returns.
type shareServer struct {
	mu     sync.Mutex
	allow  int
	posts  []string
	shares map[string]bool // "type/id"
	// broken makes every call fail with a 500, and counts them.
	broken bool
	calls  int
}

func (f *shareServer) serve(t *testing.T) (*httptest.Server, ExAppConfig) {
	t.Helper()
	ncAccessSubstrate.reset()
	t.Cleanup(ncAccessSubstrate.reset)
	resetShareLimit()
	t.Cleanup(resetShareLimit)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls++
		if f.broken {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			kind := map[string]string{"0": "user", "1": "group", "7": "circle"}[r.Form.Get("shareType")]
			who := kind + "/" + r.Form.Get("shareWith")
			f.posts = append(f.posts, who)
			if f.allow <= 0 {
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			f.allow--
			f.shares[who] = true
			_, _ = w.Write([]byte(`{"ocs":{"meta":{"statuscode":100},"data":{"id":"1","share_type":` + map[string]string{"user": "0", "group": "1", "circle": "7"}[kind] + `,"share_with":"` + r.Form.Get("shareWith") + `","permissions":17}}}`))
			return
		}
		data := []string{}
		for who := range f.shares {
			kind, id, _ := strings.Cut(who, "/")
			data = append(data, `{"id":"1","share_type":`+map[string]string{"user": "0", "group": "1", "circle": "7"}[kind]+`,"share_with":"`+id+`","permissions":17}`)
		}
		_, _ = w.Write([]byte(`{"ocs":{"meta":{"statuscode":100},"data":[` + strings.Join(data, ",") + `]}}`))
	}))
	t.Cleanup(server.Close)
	return server, ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppVersion: "1", AppSecret: "secret"}
}

// #484: an import that meets the limit is added anyway; who the limit held
// back is returned as waiting, the importer too, and nothing is asked after
// the limit answers.
func TestAnImportAtTheShareLimitDefersTheRest(t *testing.T) {
	f := &shareServer{allow: 1, shares: map[string]bool{}}
	server, cfg := f.serve(t)
	audience := []sharePrincipal{{Type: "user", ID: "alice"}, {Type: "user", ID: "bob"}, {Type: "group", ID: "staff"}}
	skipped, err := cfg.reconcileRecordingShares(context.Background(), server.Client(), ncRecordingsRoot+"/meetings/a.opus", audience, true, shareImport, "")
	if err != nil {
		t.Fatalf("import at the limit = %v", err)
	}
	if !reflect.DeepEqual(f.posts, []string{"user/alice", "user/bob"}) {
		t.Fatalf("creates asked for = %v", f.posts)
	}
	if got := ids(deferredShares(skipped)); !reflect.DeepEqual(got, []string{"user/bob", "group/staff"}) || len(refusedShares(skipped)) != 0 {
		t.Fatalf("skipped = %+v", skipped)
	}
	if !ncShareLimit.holding() {
		t.Fatal("the limit was met and the clock does not hold")
	}
	// While it holds, the next import asks nothing: every refused attempt
	// counts against the limit too. The importer, shared with already, is
	// not waiting.
	f.posts = nil
	f.allow = 10
	f.shares["user/carol"] = true
	skipped, err = cfg.reconcileRecordingShares(context.Background(), server.Client(), ncRecordingsRoot+"/meetings/b.opus", []sharePrincipal{{Type: "user", ID: "carol"}, {Type: "user", ID: "dave"}}, true, shareImport, "")
	if err != nil || len(f.posts) != 0 || !reflect.DeepEqual(ids(deferredShares(skipped)), []string{"user/dave"}) {
		t.Fatalf("while the limit holds: posts %v, skipped %+v, %v", f.posts, skipped, err)
	}
	// Once the window has passed, imports share again.
	ncShareLimit.mu.Lock()
	ncShareLimit.now = func() time.Time { return time.Now().Add(shareLimitWindow) }
	ncShareLimit.mu.Unlock()
	if ncShareLimit.holding() {
		t.Fatal("the clock still holds after the window")
	}
}

// The completion pass shares with those waiting and nobody else: the importer
// named first is never shared with unless waiting, so a share they removed is
// not made again.
func TestCompletingSharesReachesOnlyThoseWaiting(t *testing.T) {
	f := &shareServer{allow: 10, shares: map[string]bool{}}
	server, cfg := f.serve(t)
	// alice (the importer) removed her share; bob is waiting.
	skipped, err := cfg.reconcileRecordingShares(context.Background(), server.Client(), ncRecordingsRoot+"/meetings/a.opus",
		[]sharePrincipal{{Type: "user", ID: "alice"}, {Type: "user", ID: "bob"}}, true, shareComplete, "")
	if err != nil || len(skipped) != 0 || !reflect.DeepEqual(f.posts, []string{"user/bob"}) {
		t.Fatalf("posts %v, skipped %+v, %v", f.posts, skipped, err)
	}
	// An importer the limit held back is listed again, and shared with.
	f.posts = nil
	if _, err := cfg.reconcileRecordingShares(context.Background(), server.Client(), ncRecordingsRoot+"/meetings/b.opus",
		[]sharePrincipal{{Type: "user", ID: "alice"}, {Type: "user", ID: "alice"}}, true, shareComplete, ""); err != nil || !reflect.DeepEqual(f.posts, []string{"user/alice"}) {
		t.Fatalf("waiting importer: posts %v, %v", f.posts, err)
	}
}

// The person's own view: someone the limit held back is still being shared
// with, so they are not told it was refused.
func TestSetupDoesNotCountTheDeferredAsRefused(t *testing.T) {
	rt := sharingRuntime(t, &sharingFake{groups: []string{"staff"}, pending: []map[string]any{}})
	ctx := context.Background()
	for _, id := range []string{"J1", "J2"} {
		seedJobRow(t, rt.store.db, seededJobRow{ID: id, Stage: "done", State: "succeeded", CreatedAt: "2026-10-30T09:00:00Z"})
	}
	record := func(id string, entries ...skippedShare) {
		t.Helper()
		if err := rt.store.RecordJobShareOutcome(ctx, id, shareOutcomeStatus(entries), entries); err != nil {
			t.Fatal(err)
		}
	}
	record("J1", skippedShare{Type: "user", ID: "alice", ShareLimit: shareLimitDeferred})
	record("J2", skippedShare{Type: "user", ID: "bob"}, skippedShare{Type: "group", ID: "staff", ShareLimit: shareLimitDeferred})
	if sharing := getSetup(t, rt, "alice").Sharing; !sharing.Checked || sharing.Refused != 0 || sharing.RefusedStarted != 0 {
		t.Fatalf("alice's view = %+v", sharing)
	}
}

// Doctor's unshared count leaves the deferred out, and its share-limit fact
// counts the meetings with someone still waiting.
func TestDoctorCountsShareLimitedMeetingsApartFromUnshared(t *testing.T) {
	resetDirectSubstrate(t)
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	ctx := context.Background()
	ncAccessSubstrate.record(ncSubstrateProvisioned, "test preflight", nil)
	record := func(id string, entries ...skippedShare) {
		t.Helper()
		if err := rt.store.RecordJobShareOutcome(ctx, id, shareOutcomeStatus(entries), entries); err != nil {
			t.Fatal(err)
		}
	}
	seedJobRow(t, rt.store.db, seededJobRow{ID: "J1", Stage: "done", State: "succeeded", CreatedAt: "2026-10-12T09:00:00Z"})
	setJobTimes(t, rt, "J1", "2026-10-12T09:14:00.000000000Z", "2026-10-12T10:00:00.000000000Z")
	record("J1", skippedShare{Type: "user", ID: "bob", ShareLimit: shareLimitDeferred})
	record("J2", skippedShare{Type: "user", ID: "carol", ShareLimit: shareLimitDeferred}, skippedShare{Type: "user", ID: "frank"})
	record("J3", skippedShare{Type: "user", ID: "dave"})
	record("J4")
	// An expired meeting is nobody's any more.
	record("J5", skippedShare{Type: "user", ID: "erin", ShareLimit: shareLimitDeferred})
	retireMeeting(t, rt, "J5.opus")

	row, _ := sharingRow(t, rt)
	if row.Unshared == nil || row.Unshared.Jobs != 2 || row.Unshared.Users != 2 {
		t.Fatalf("unshared = %+v; want J2's and J3's refusals alone", row.Unshared)
	}
	if row.ShareLimited == nil || row.ShareLimited.Jobs != 2 || row.ShareLimited.Since != "2026-10-12T09:14:00Z" {
		t.Fatalf("share limited = %+v", row.ShareLimited)
	}
}

// #484, end to end: an import the limit held back is finished by Cassini's
// own pass once the limit allows, one window at a time, and its row stops
// waiting; a retired meeting has nobody left to share with.
func TestTheCompletionPassFinishesWaitingImports(t *testing.T) {
	f := &shareServer{allow: 1, shares: map[string]bool{"user/alice": true}}
	server, cfg := f.serve(t)
	store, err := OpenStore(filepath.Join(t.TempDir(), "jobs.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	cfg.lifecycle = store
	rt := &Runtime{store: store, ctx: ctx, logger: log.New(testWriter{t}, "", 0)}
	s := &importService{rt: rt, logger: rt.logger, importer: &meetingImporter{sink: &nextcloudFilesPublishSink{cfg: cfg, client: server.Client(), rt: rt}}}
	add := func(id string, waiting ...string) {
		t.Helper()
		job := ImportJob{ID: id, BatchID: "b", CreatedBy: "alice", SourceName: id + ".opus", StagedPath: ".CassiniImports/" + id + ".opus",
			Sharing: importAudience{Share: true, Audience: []importPrincipal{{Type: "user", ID: "bob", Label: "Bob"}, {Type: "user", ID: "carol"}, {Type: "group", ID: "staff"}}}}
		if err := store.CreateImportJobs(ctx, []ImportJob{job}); err != nil {
			t.Fatal(err)
		}
		if err := store.SetImportJobOutcome(ctx, id, id+".opus", 7, nowUTCString()); err != nil {
			t.Fatal(err)
		}
		held := []skippedShare{}
		for _, who := range waiting {
			kind, name, _ := strings.Cut(who, "/")
			held = append(held, skippedShare{Type: kind, ID: name, ShareLimit: shareLimitDeferred})
		}
		if err := store.RecordImportShares(ctx, id, held, nil); err != nil {
			t.Fatal(err)
		}
	}
	add("IMP1", "user/bob", "user/carol")
	add("IMP2", "group/staff")
	waitingOn := func(id string) []string {
		t.Helper()
		job, _, err := store.ImportJob(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, p := range job.Sharing.Waiting {
			out = append(out, p.Type+"/"+p.ID)
		}
		return out
	}
	if job, _, _ := store.ImportJob(ctx, "IMP1"); job.Sharing.Waiting[0].Label != "Bob" {
		t.Fatalf("the label the importer saw is lost: %+v", job.Sharing.Waiting)
	}

	// One share gets through, then the limit: the pass stops there.
	s.completeWaitingShares(ctx)
	if !reflect.DeepEqual(f.posts, []string{"user/bob", "user/carol"}) || !reflect.DeepEqual(waitingOn("IMP1"), []string{"user/carol"}) || !reflect.DeepEqual(waitingOn("IMP2"), []string{"group/staff"}) {
		t.Fatalf("first pass: posts %v, IMP1 %v, IMP2 %v", f.posts, waitingOn("IMP1"), waitingOn("IMP2"))
	}
	// While the limit holds, a pass asks nothing.
	f.posts, f.allow = nil, 10
	s.completeWaitingShares(ctx)
	if len(f.posts) != 0 {
		t.Fatalf("a pass asked %v while the limit held", f.posts)
	}
	// Once the window has passed, everyone left is reached; alice, who has
	// her share, is not asked for again.
	ncShareLimit.mu.Lock()
	ncShareLimit.now = func() time.Time { return time.Now().Add(shareLimitWindow) }
	ncShareLimit.mu.Unlock()
	s.completeWaitingShares(ctx)
	if !reflect.DeepEqual(f.posts, []string{"user/carol", "group/staff"}) || len(waitingOn("IMP1")) != 0 || len(waitingOn("IMP2")) != 0 {
		t.Fatalf("second window: posts %v, IMP1 %v, IMP2 %v", f.posts, waitingOn("IMP1"), waitingOn("IMP2"))
	}
	if left, err := store.ImportsWaitingToShare(ctx); err != nil || len(left) != 0 {
		t.Fatalf("still listed as waiting: %v %v", left, err)
	}

	// A meeting retired meanwhile is not shared, and stops waiting.
	add("IMP3", "user/dave")
	lifecycle := meetingLifecycle{Name: "IMP3.opus", FileID: 7, Path: ncRecordingsRoot + "/meetings/IMP3.opus", State: "active", CreatedAtUTC: "2026-11-01T10:00:00Z", RecordedAtUTC: "2026-11-01T10:00:00Z"}
	if err := setMeetingRetentionAge(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if err := store.adoptMeetingLifecycle(ctx, lifecycle); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE meeting_lifecycle SET state='retired' WHERE name='IMP3.opus'`); err != nil {
		t.Fatal(err)
	}
	f.posts = nil
	s.completeWaitingShares(ctx)
	if len(f.posts) != 0 || len(waitingOn("IMP3")) != 0 {
		t.Fatalf("retired meeting: posts %v, waiting %v", f.posts, waitingOn("IMP3"))
	}
}

// #484: an import whose shares the limit held back is done, and its row says
// who is still waiting and whom Nextcloud refused.
func TestAnImportHeldBackByTheLimitIsDoneAndSaysWhoIsWaiting(t *testing.T) {
	s := queueWorkerService(t, func(ctx context.Context, job ImportJob) (meetingImportResult, error) {
		return meetingImportResult{DeliveredName: job.ID + ".opus", FileID: 7, ImportedAtUTC: nowUTCString(), NotSharedWith: []skippedShare{
			{Type: "user", ID: "bob", ShareLimit: shareLimitDeferred},
			{Type: "user", ID: "eve", Reason: "Sharing is only allowed with group members"},
		}}, nil
	})
	// Removing the staged copy afterwards is best effort; it fails here.
	s.client = &http.Client{Timeout: time.Second}
	if err := s.rt.store.CreateImportJobs(context.Background(), []ImportJob{queueTestJob("a1", "alice")}); err != nil {
		t.Fatal(err)
	}
	s.dispatch()
	waitForImportState(t, s, "a1", importStateDone)
	job, _, err := s.rt.store.ImportJob(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if len(job.Sharing.Waiting) != 1 || job.Sharing.Waiting[0].ID != "bob" ||
		len(job.Sharing.NotShared) != 1 || job.Sharing.NotShared[0].ID != "eve" || job.Sharing.NotShared[0].Reason == "" {
		t.Fatalf("sharing: %+v", job.Sharing)
	}
}

// A completion that keeps failing for another reason than the limit backs
// off, and is given up after shareCompletionTries: the row then names those
// it could not reach instead of saying "waiting" for ever.
func TestACompletionThatKeepsFailingBacksOffAndGivesUp(t *testing.T) {
	f := &shareServer{broken: true, shares: map[string]bool{}}
	server, cfg := f.serve(t)
	store, err := OpenStore(filepath.Join(t.TempDir(), "jobs.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	cfg.lifecycle = store
	rt := &Runtime{store: store, ctx: ctx, logger: log.New(testWriter{t}, "", 0)}
	s := &importService{rt: rt, logger: rt.logger, importer: &meetingImporter{sink: &nextcloudFilesPublishSink{cfg: cfg, client: server.Client(), rt: rt}}}
	job := ImportJob{ID: "IMP", BatchID: "b", CreatedBy: "alice", SourceName: "a.opus", StagedPath: ".CassiniImports/a.opus",
		Sharing: importAudience{Share: true, Audience: []importPrincipal{{Type: "user", ID: "bob"}}}}
	if err := store.CreateImportJobs(ctx, []ImportJob{job}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetImportJobOutcome(ctx, "IMP", "IMP.opus", 7, nowUTCString()); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordImportShares(ctx, "IMP", []skippedShare{{Type: "user", ID: "bob", ShareLimit: shareLimitDeferred}}, nil); err != nil {
		t.Fatal(err)
	}
	// A failed try is not repeated before its backoff.
	s.completeWaitingShares(ctx)
	first := f.calls
	s.completeWaitingShares(ctx)
	if first == 0 || f.calls != first {
		t.Fatalf("calls %d then %d: a failed completion was retried at once", first, f.calls)
	}
	// Each try past its backoff, until the last.
	for try := 2; try <= shareCompletionTries; try++ {
		s.mu.Lock()
		retry := s.shareRetries["IMP"]
		retry.next = time.Time{}
		s.shareRetries["IMP"] = retry
		s.mu.Unlock()
		s.completeWaitingShares(ctx)
	}
	got, _, err := store.ImportJob(ctx, "IMP")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sharing.Waiting) != 0 || len(got.Sharing.NotShared) != 1 || got.Sharing.NotShared[0].ID != "bob" ||
		!strings.Contains(got.Sharing.NotShared[0].Reason, "Cassini could not share it") {
		t.Fatalf("after giving up: %+v", got.Sharing)
	}
	if left, _ := store.ImportsWaitingToShare(ctx); len(left) != 0 {
		t.Fatalf("still waiting: %v", left)
	}
}
