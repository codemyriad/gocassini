package operator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
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

// Imports still fail on the limit, as on any error that names no recipient,
// but say what it was and do not spend the limit again on a retry.
func TestAnImportAtTheShareLimitSaysSo(t *testing.T) {
	ncAccessSubstrate.reset()
	t.Cleanup(ncAccessSubstrate.reset)
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"ocs":{"meta":{"statuscode":100},"data":[]}}`))
	}))
	defer server.Close()
	cfg := ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppVersion: "1", AppSecret: "secret"}
	audience := []sharePrincipal{{Type: "user", ID: "alice"}, {Type: "user", ID: "bob"}}
	_, err := cfg.reconcileRecordingShares(context.Background(), server.Client(), ncRecordingsRoot+"/meetings/a.opus", audience, true, shareImport, "")
	if !shareRateLimited(err) || strings.Contains(err.Error(), "decode") {
		t.Fatalf("import at the limit = %v", err)
	}
	if posts != 1 {
		t.Fatalf("the import asked %d times after the limit answered", posts)
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
