package operator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestJobShareOutcomeIsReplacedByEachReconciliation(t *testing.T) {
	ctx := context.Background()
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	insertJob(t, rt.store.db, "J1", nowUTCString())
	if outcome, err := rt.store.JobShareOutcome(ctx, "J1"); err != nil || outcome != nil {
		t.Fatalf("unrecorded outcome = %+v, %v", outcome, err)
	}
	skipped := []skippedShare{{Type: "user", ID: "bob", Reason: "Sharing is only allowed with group members"}, {Type: "group", ID: "staff"}}
	if err := rt.store.RecordJobShareOutcome(ctx, "J1", shareOutcomePartial, skipped); err != nil {
		t.Fatal(err)
	}
	outcome, err := rt.store.JobShareOutcome(ctx, "J1")
	if err != nil || outcome.Status != shareOutcomePartial || !reflect.DeepEqual(outcome.Skipped, skipped) || outcome.UpdatedAt == "" {
		t.Fatalf("outcome = %+v, %v", outcome, err)
	}
	if counts, err := rt.store.partiallySharedJobs(ctx); err != nil || counts["J1"] != 2 {
		t.Fatalf("partially shared = %v, %v", counts, err)
	}
	// An administrator fixed the policy and re-shared: the whole truth again.
	if err := rt.store.RecordJobShareOutcome(ctx, "J1", shareOutcomeComplete, nil); err != nil {
		t.Fatal(err)
	}
	if outcome, _ := rt.store.JobShareOutcome(ctx, "J1"); outcome.Status != shareOutcomeComplete || len(outcome.Skipped) != 0 {
		t.Fatalf("replaced outcome = %+v", outcome)
	}
	if counts, _ := rt.store.partiallySharedJobs(ctx); len(counts) != 0 {
		t.Fatalf("a complete outcome is still flagged: %v", counts)
	}
}

func TestShareRefusalReasonIsNextcloudsOwnWordsBounded(t *testing.T) {
	if got := shareRefusalReason(&ncShareError{HTTPStatus: 403, OCSStatus: 403, Message: "Sharing is only allowed with group members"}); got != "Sharing is only allowed with group members" {
		t.Fatalf("reason = %q", got)
	}
	if got := shareRefusalReason(&ncShareError{HTTPStatus: 404, OCSStatus: 404}); got != "refused (OCS 404)" {
		t.Fatalf("reason without a message = %q", got)
	}
	if got := shareRefusalReason(&ncShareError{OCSStatus: 403, Message: strings.Repeat("x", 500)}); len(got) > maxShareRefusalReason+len("…") {
		t.Fatalf("reason not bounded: %d bytes", len(got))
	}
}

// directPublishFixture is a published-recording delivery against a Nextcloud
// that refuses shares to the principals named, with the group-members message.
type directPublishFixture struct {
	rt      *Runtime
	sink    *directSharesPublishSink
	attempt string
	mu      sync.Mutex
	shares  map[string]bool
	refuse  map[string]bool
	fail    map[string]bool
	// notes is the note each share was created with, by the same key.
	notes map[string]string
	// allow is how many more creates Nextcloud's limit on new shares lets
	// through, refused ones included; negative is no limit. posted is every
	// create asked for, in order, the ones the limit refused included.
	allow  int
	posted []string
}

const directPublishJob = "SHARE1"

func newDirectPublishFixture(t *testing.T, audience []sharePrincipal, refuse ...string) *directPublishFixture {
	t.Helper()
	ctx := context.Background()
	rt, cleanup := newBarePublishRuntime(t, log.New(io.Discard, "", 0))
	t.Cleanup(cleanup)
	insertJob(t, rt.store.db, directPublishJob, "2026-11-01T10:00:00Z")
	if err := rt.store.SetJobTalkBinding(ctx, directPublishJob, `{"backend_url":"https://talk.example","room_token":"room1","owner":"alice","room_public":false}`); err != nil {
		t.Fatal(err)
	}
	if err := rt.store.MergeJobRoomAudience(ctx, directPublishJob, audience, nowUTCString()); err != nil {
		t.Fatal(err)
	}
	f := &directPublishFixture{rt: rt, attempt: t.TempDir(), shares: map[string]bool{}, refuse: map[string]bool{}, fail: map[string]bool{}, notes: map[string]string{}, allow: -1}
	for _, id := range refuse {
		f.refuse[id] = true
	}
	if err := os.MkdirAll(filepath.Join(f.attempt, "meetings"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.attempt, "meetings", directPublishJob+".opus"), []byte("sealed bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.attempt, "catalog.json"), []byte(`{"version":"cassini.viewer.catalog.v1","meetings":[{"id":"SHARE1","title":"Standup","meetingPath":"./meetings/SHARE1.opus"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	files := newFakeNCFiles()
	filesServer := files.server(t)
	target, _ := url.Parse(filesServer.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/files_sharing/api/v1/shares") {
			proxy.ServeHTTP(w, r)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method == http.MethodPost {
			who := r.FormValue("shareWith")
			f.posted = append(f.posted, who)
			if f.allow == 0 {
				// Nextcloud's rate limiter answers with no body at all.
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			if f.allow > 0 {
				f.allow--
			}
			if f.fail[who] {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = io.WriteString(w, `{"ocs":{"meta":{"status":"failure","statuscode":996,"message":"bad gateway"},"data":[]}}`)
				return
			}
			if f.refuse[who] {
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `{"ocs":{"meta":{"status":"failure","statuscode":403,"message":"Sharing is only allowed with group members"},"data":[]}}`)
				return
			}
			f.shares[r.FormValue("shareType")+"/"+who] = true
			f.notes[r.FormValue("shareType")+"/"+who] = r.FormValue("note")
			_, _ = io.WriteString(w, `{"ocs":{"meta":{"statuscode":100},"data":{"id":"11"}}}`)
			return
		}
		items := []map[string]any{}
		for key := range f.shares {
			kind, who, _ := strings.Cut(key, "/")
			shareType := ncShareTypeUser
			if kind == "1" {
				shareType = ncShareTypeGroup
			}
			items = append(items, map[string]any{"id": "11", "share_type": shareType, "share_with": who, "permissions": 1})
		}
		body, _ := json.Marshal(map[string]any{"ocs": map[string]any{"meta": map[string]any{"statuscode": 100}, "data": items}})
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	cfg := ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppVersion: "1", AppSecret: "secret", PublishSink: publishSinkNextcloudFiles}
	f.sink = &directSharesPublishSink{&nextcloudFilesPublishSink{cfg: cfg, client: server.Client(), logger: log.New(io.Discard, "", 0), rt: rt}}
	ncAccessSubstrate.reset()
	ncAccessSubstrate.markApplicable()
	ncAccessSubstrate.beginRun()
	ncAccessSubstrate.succeed()
	t.Cleanup(ncAccessSubstrate.reset)
	return f
}

func (f *directPublishFixture) deliver() error {
	_, err := f.sink.Deliver(context.Background(), publishDelivery{AttemptSitePath: f.attempt, JobID: directPublishJob, RoomName: "Standup"})
	return err
}

func (f *directPublishFixture) setRefused(ids ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refuse = map[string]bool{}
	for _, id := range ids {
		f.refuse[id] = true
	}
}

func (f *directPublishFixture) setFailing(ids ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = map[string]bool{}
	for _, id := range ids {
		f.fail[id] = true
	}
}

func (f *directPublishFixture) shared(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.shares[key]
}

func (f *directPublishFixture) markPublished(t *testing.T) {
	t.Helper()
	if _, err := f.rt.store.db.Exec(`UPDATE job_attempts SET stage='done', state='succeeded' WHERE job_id=?`, directPublishJob); err != nil {
		t.Fatal(err)
	}
}

// sortedSkips orders skips by type then id: the audience's order is the
// store's, not the test's.
func sortedSkips(skips []skippedShare) []skippedShare {
	out := append([]skippedShare(nil), skips...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (f *directPublishFixture) outcome(t *testing.T) *jobShareOutcome {
	t.Helper()
	outcome, err := f.rt.store.JobShareOutcome(context.Background(), directPublishJob)
	if err != nil {
		t.Fatal(err)
	}
	return outcome
}

// The customer's report, the admin-starts half: the meeting publishes for
// whoever Nextcloud allows, and the run names everyone it does not — a group
// included — instead of the meeting silently not appearing for them.
func TestPublishIsBestEffortAndNamesWhoItCouldNotShareWith(t *testing.T) {
	f := newDirectPublishFixture(t,
		[]sharePrincipal{{Type: "user", ID: "bob"}, {Type: "user", ID: "carol"}, {Type: "group", ID: "marketing"}},
		"bob", "marketing")
	if err := f.deliver(); err != nil {
		t.Fatalf("publication failed on a refused participant: %v", err)
	}
	outcome := f.outcome(t)
	want := []skippedShare{
		{Type: "group", ID: "marketing", Reason: "Sharing is only allowed with group members"},
		{Type: "user", ID: "bob", Reason: "Sharing is only allowed with group members"},
	}
	if outcome == nil || outcome.Status != shareOutcomePartial || !reflect.DeepEqual(sortedSkips(outcome.Skipped), want) {
		t.Fatalf("outcome = %+v", outcome)
	}
	if !f.shares["0/alice"] || !f.shares["0/carol"] {
		t.Fatalf("shares = %v", f.shares)
	}

	// A rerun keeps the file and deliberately does not re-share, so the
	// outcome — and the people it names — must survive it.
	if _, err := f.rt.store.db.Exec(`UPDATE job_attempts SET stage='done', state='succeeded' WHERE job_id=?`, directPublishJob); err != nil {
		t.Fatal(err)
	}
	if err := f.deliver(); err != nil {
		t.Fatal(err)
	}
	if again := f.outcome(t); again == nil || again.Status != shareOutcomePartial || len(again.Skipped) != 2 {
		t.Fatalf("a rerun lost the share outcome: %+v", again)
	}
}

// The non-admin-starts half: a meeting its own starter cannot open is not
// published, and the run says it was the starter Nextcloud refused.
func TestPublishFailsWhenTheStarterIsRefusedAndSaysSo(t *testing.T) {
	f := newDirectPublishFixture(t, []sharePrincipal{{Type: "user", ID: "bob"}}, "alice")
	err := f.deliver()
	var refused *starterShareRefusedError
	if !errors.As(err, &refused) || refused.Starter != "alice" {
		t.Fatalf("starter refusal = %v", err)
	}
	outcome := f.outcome(t)
	if outcome == nil || outcome.Status != shareOutcomeStarterRefused || len(outcome.Skipped) != 1 || outcome.Skipped[0].ID != "alice" {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestPublishSharedWithEveryoneRecordsComplete(t *testing.T) {
	f := newDirectPublishFixture(t, []sharePrincipal{{Type: "user", ID: "bob"}})
	if err := f.deliver(); err != nil {
		t.Fatal(err)
	}
	if outcome := f.outcome(t); outcome == nil || outcome.Status != shareOutcomeComplete || len(outcome.Skipped) != 0 {
		t.Fatalf("outcome = %+v", outcome)
	}
}

// The run list flags it, the run detail names them, and an event carries both,
// so a run that has just published says so without a reload.
func TestShareOutcomeReachesTheRunListDetailAndEvents(t *testing.T) {
	ctx := context.Background()
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	insertJob(t, rt.store.db, "J1", nowUTCString())
	insertJob(t, rt.store.db, "J2", nowUTCString())
	insertJob(t, rt.store.db, "J3", nowUTCString())
	skipped := []skippedShare{{Type: "user", ID: "bob", Reason: "Sharing is only allowed with group members"}}
	if err := rt.store.RecordJobShareOutcome(ctx, "J1", shareOutcomePartial, skipped); err != nil {
		t.Fatal(err)
	}
	// J1's publish used its starter plus the captured room: alice (who is
	// also in the roster), bob and the staff group — three recipients.
	if err := rt.store.SetJobTalkBinding(ctx, "J1", `{"backend_url":"https://talk.example","room_token":"room1","owner":"alice","room_public":false}`); err != nil {
		t.Fatal(err)
	}
	if err := rt.store.MergeJobRoomAudience(ctx, "J1", []sharePrincipal{{Type: "user", ID: "alice"}, {Type: "user", ID: "bob"}, {Type: "group", ID: "staff"}}, nowUTCString()); err != nil {
		t.Fatal(err)
	}
	// J3 has an outcome but no captured audience: no count to report.
	if err := rt.store.RecordJobShareOutcome(ctx, "J3", shareOutcomeComplete, nil); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	rt.jobsHandler(rec, httptest.NewRequest(http.MethodGet, "/jobs", nil))
	var jobs []Job
	if err := json.Unmarshal(rec.Body.Bytes(), &jobs); err != nil {
		t.Fatalf("list: %v %s", err, rec.Body.String())
	}
	counts := map[string]int{}
	for _, job := range jobs {
		counts[job.ID] = job.UnsharedCount
	}
	if counts["J1"] != 1 || counts["J2"] != 0 {
		t.Fatalf("unshared counts = %v", counts)
	}

	rec = httptest.NewRecorder()
	rt.jobDetailHandler(rec, httptest.NewRequest(http.MethodGet, "/jobs/J1", nil))
	var detail jobDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("detail: %v %s", err, rec.Body.String())
	}
	if detail.Sharing == nil || detail.Sharing.Status != shareOutcomePartial || !reflect.DeepEqual(detail.Sharing.Skipped, skipped) || detail.Job.UnsharedCount != 1 || detail.Sharing.AudienceCount != 3 {
		t.Fatalf("detail sharing = %+v, job count %d", detail.Sharing, detail.Job.UnsharedCount)
	}
	rec = httptest.NewRecorder()
	rt.jobDetailHandler(rec, httptest.NewRequest(http.MethodGet, "/jobs/J2", nil))
	if strings.Contains(rec.Body.String(), `"sharing"`) {
		t.Fatalf("a job with no recorded outcome reports one: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	rt.jobDetailHandler(rec, httptest.NewRequest(http.MethodGet, "/jobs/J3", nil))
	if !strings.Contains(rec.Body.String(), `"sharing"`) || strings.Contains(rec.Body.String(), `"audience_count"`) {
		t.Fatalf("a job with no captured audience reports a count: %s", rec.Body.String())
	}

	hub := newEventHub()
	rt.events = hub
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	job, err := rt.store.GetJob(ctx, "J1")
	if err != nil {
		t.Fatal(err)
	}
	rt.publishStateChangeEvent(StateChangeEvent{Type: "job.updated", JobID: "J1", Job: job})
	select {
	case event := <-events:
		if event.Sharing == nil || event.Sharing.Status != shareOutcomePartial || event.Job.UnsharedCount != 1 || event.Sharing.AudienceCount != 3 {
			t.Fatalf("event sharing = %+v, job count %d", event.Sharing, event.Job.UnsharedCount)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event")
	}
}

// The policy was fixed and the rerun got past the starter, but failed on
// something else: the run must not go on blaming the policy.
func TestALaterReconciliationReplacesAStarterRefusal(t *testing.T) {
	f := newDirectPublishFixture(t, []sharePrincipal{{Type: "user", ID: "bob"}}, "alice")
	if err := f.deliver(); err == nil {
		t.Fatal("the starter's refusal did not fail publication")
	}
	if outcome := f.outcome(t); outcome == nil || outcome.Status != shareOutcomeStarterRefused {
		t.Fatalf("outcome = %+v", outcome)
	}
	f.setRefused()
	f.setFailing("bob")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := f.sink.Deliver(ctx, publishDelivery{AttemptSitePath: f.attempt, JobID: directPublishJob}); err == nil || !strings.Contains(err.Error(), "bob") {
		t.Fatalf("an outage did not fail publication: %v", err)
	}
	if outcome := f.outcome(t); outcome != nil {
		t.Fatalf("a stale outcome outlived the reconciliation that replaced it: %+v", outcome)
	}
}

// #472 review: attendee mode must not look the room up again at publish time.
// With no saved roster it shares the starter only and leaves a run warning.
func TestAttendeeModeWithoutCapturedRosterSharesOnlyTheStarter(t *testing.T) {
	f := newDirectPublishFixture(t, []sharePrincipal{{Type: "user", ID: "old-recipient"}})
	if _, err := f.rt.store.db.Exec(`UPDATE jobs SET room_audience = NULL, room_audience_at = NULL, audience_mode = 'attendees' WHERE id = ?`, directPublishJob); err != nil {
		t.Fatal(err)
	}
	called := false
	f.rt.fetchTalkParticipants = func(context.Context, string, string) ([]sharePrincipal, error) {
		called = true
		return nil, nil
	}
	if err := f.deliver(); err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	if called {
		t.Fatal("attendee mode looked up Today's room at publish time")
	}
	if len(f.posted) != 1 || !f.shared("0/alice") {
		t.Fatalf("posted=%v shares=%v, want the starter only", f.posted, f.shares)
	}
	if f.shared("0/old-recipient") {
		t.Fatal("a stale roster leaked into an attendee-mode publish with no capture")
	}
	job, err := f.rt.store.GetJob(context.Background(), directPublishJob)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(job.AudienceNotice, "only the starter") {
		t.Fatalf("audience notice = %q, want the starter-only warning", job.AudienceNotice)
	}
}

// A file already in place from an earlier publish makes the next delivery a
// republish, which does not re-share — except after a refused starter, which
// stopped the last reconciliation before anyone was shared with.
func TestARerunAfterARefusedStarterSharesEvenAsARepublish(t *testing.T) {
	f := newDirectPublishFixture(t, []sharePrincipal{{Type: "user", ID: "bob"}}, "alice")
	if err := f.deliver(); err == nil {
		t.Fatal("the starter's refusal did not fail publication")
	}
	// An earlier publish counts as a prior success (another sink, say).
	f.markPublished(t)
	f.setRefused()
	if err := f.deliver(); err != nil {
		t.Fatal(err)
	}
	if !f.shared("0/alice") || !f.shared("0/bob") {
		t.Fatalf("the rerun published without sharing: %v", f.shares)
	}
	if outcome := f.outcome(t); outcome == nil || outcome.Status != shareOutcomeComplete {
		t.Fatalf("outcome = %+v", outcome)
	}
}

// A refused starter explains a failure only while it is that failure.
func TestAStarterRefusalIsReportedOnlyWhileItIsTheFailure(t *testing.T) {
	ctx := context.Background()
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	insertJob(t, rt.store.db, "J1", nowUTCString())
	if err := rt.store.RecordJobShareOutcome(ctx, "J1", shareOutcomeStarterRefused, []skippedShare{{Type: "user", ID: "alice"}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		state, err string
		shown      bool
	}{
		{"failed", "publish: the recording " + starterRefusedMarker + ", alice: Nextcloud share API: HTTP 403", true},
		{"failed", "build: out of memory", false},
		{"succeeded", "", false},
	} {
		job := Job{ID: "J1", State: tc.state}
		if tc.err != "" {
			job.Error = &tc.err
		}
		if got := rt.jobSharing(ctx, &job); (got != nil) != tc.shown {
			t.Errorf("state=%s error=%q: sharing=%+v, want shown=%v", tc.state, tc.err, got, tc.shown)
		}
	}
}

// A deleted meeting has nobody left to share with: no flag, no notice.
func TestADeletedMeetingCarriesNoShareOutcome(t *testing.T) {
	ctx := context.Background()
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	insertJob(t, rt.store.db, "J1", nowUTCString())
	if err := rt.store.RecordJobShareOutcome(ctx, "J1", shareOutcomePartial, []skippedShare{{Type: "user", ID: "bob"}}); err != nil {
		t.Fatal(err)
	}
	job := Job{ID: "J1", State: "succeeded", MeetingDeleted: true}
	if got := rt.jobSharing(ctx, &job); got != nil || job.UnsharedCount != 0 {
		t.Fatalf("deleted meeting sharing = %+v, count %d", got, job.UnsharedCount)
	}
}

// The count a run reports is the list its publish shared with: the starter
// and the captured room, without the recordings owner and without repeats.
// No capture, no count: the live roster a publish fell back to is not kept.
func TestCapturedAudienceCountIsThePublishedList(t *testing.T) {
	binding := `{"backend_url":"https://talk.example","room_token":"room1","owner":"alice","room_public":false}`
	at := nowUTCString()
	audience := `[{"type":"user","id":"alice"},{"type":"user","id":"bob"},{"type":"group","id":"staff"},{"type":"user","id":"cassini"},{"type":"user","id":"bob"}]`
	if got := capturedAudienceCount(Job{TalkBinding: &binding, RoomAudience: &audience, RoomAudienceAt: &at}); got != 3 {
		t.Fatalf("count = %d, want alice, bob and staff", got)
	}
	empty := "[]"
	if got := capturedAudienceCount(Job{TalkBinding: &binding, RoomAudience: &empty, RoomAudienceAt: &at}); got != 1 {
		t.Fatalf("count with an empty room = %d, want the starter alone", got)
	}
	if got := capturedAudienceCount(Job{TalkBinding: &binding, RoomAudience: &audience}); got != 0 {
		t.Fatalf("count without a capture = %d, want none", got)
	}
}

// A refused group costs the caller the recording only when the group was
// their only way to it. Someone who was in the room holds their own share:
// with "Allow sharing with groups" off, every group in the room is refused,
// and that must not read as "could not be shared with you" to each person
// who can open it.
func TestARefusedGroupCountsOnlyWhenItWasTheCallersOnlyWay(t *testing.T) {
	binding := `{"backend_url":"https://talk.example","room_token":"room1","owner":"bob","room_public":false}`
	at := nowUTCString()
	withAlice := `[{"type":"user","id":"alice"},{"type":"group","id":"staff"}]`
	withoutAlice := `[{"type":"user","id":"carol"},{"type":"group","id":"staff"}]`
	staff := map[string]bool{"staff": true}
	groupRefused := []skippedShare{{Type: "group", ID: "staff"}}
	for name, tc := range map[string]struct {
		skipped []skippedShare
		job     Job
		want    bool
	}{
		"refused by name":                    {[]skippedShare{{Type: "user", ID: "alice"}}, Job{TalkBinding: &binding, RoomAudience: &withAlice, RoomAudienceAt: &at}, true},
		"reached through the group alone":    {groupRefused, Job{TalkBinding: &binding, RoomAudience: &withoutAlice, RoomAudienceAt: &at}, true},
		"in the room, own share succeeded":   {groupRefused, Job{TalkBinding: &binding, RoomAudience: &withAlice, RoomAudienceAt: &at}, false},
		"in the room, own share refused too": {append(groupRefused, skippedShare{Type: "user", ID: "alice"}), Job{TalkBinding: &binding, RoomAudience: &withAlice, RoomAudienceAt: &at}, true},
		"the starter, in a re-share":         {groupRefused, Job{TalkBinding: &binding, RoomAudience: &withoutAlice, RoomAudienceAt: &at}, true},
		"no audience captured":               {groupRefused, Job{TalkBinding: &binding, RoomAudience: &withAlice}, true},
		"a group the caller is not in":       {[]skippedShare{{Type: "group", ID: "sales"}}, Job{TalkBinding: &binding, RoomAudience: &withoutAlice, RoomAudienceAt: &at}, false},
		"somebody else, and a Team":          {[]skippedShare{{Type: "user", ID: "bob"}, {Type: "circle", ID: "team"}}, Job{TalkBinding: &binding, RoomAudience: &withoutAlice, RoomAudienceAt: &at}, false},
	} {
		t.Run(name, func(t *testing.T) {
			caller := "alice"
			if name == "the starter, in a re-share" {
				caller = "bob"
				tc.want = false // bob started it: the starter is in the list by name.
			}
			if got := refusedToCaller(tc.skipped, caller, staff, tc.job); got != tc.want {
				t.Fatalf("refusedToCaller = %v, want %v", got, tc.want)
			}
		})
	}
}
