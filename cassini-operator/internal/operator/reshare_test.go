package operator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// markJobSucceeded is a finished, published run: its attempt and the job.
func (f *directPublishFixture) markJobSucceeded(t *testing.T) {
	t.Helper()
	f.markPublished(t)
	f.setJobState(t, "done", "succeeded")
}

func (f *directPublishFixture) setJobState(t *testing.T, stage, state string) {
	t.Helper()
	if _, err := f.rt.store.db.Exec(`UPDATE jobs SET stage=?, state=? WHERE id=?`, stage, state, directPublishJob); err != nil {
		t.Fatal(err)
	}
}

func (f *directPublishFixture) reshare(t *testing.T) (*httptest.ResponseRecorder, reshareResponse) {
	t.Helper()
	f.rt.publishSink = f.sink
	rec := httptest.NewRecorder()
	f.rt.jobDetailHandler(rec, httptest.NewRequest(http.MethodPost, "/jobs/"+directPublishJob+"/reshare", nil))
	var body reshareResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode reshare response: %v %s", err, rec.Body.String())
		}
	}
	return rec, body
}

// The way back for a meeting published while a policy refused some of its
// audience: once the policy is fixed, re-sharing reaches them.
func TestReshareReachesWhoPublicationCouldNot(t *testing.T) {
	f := newDirectPublishFixture(t, []sharePrincipal{{Type: "user", ID: "bob"}, {Type: "group", ID: "marketing"}}, "bob", "marketing")
	if err := f.deliver(); err != nil {
		t.Fatal(err)
	}
	f.markJobSucceeded(t)

	// Still refused: nothing changes, and the run keeps naming them.
	rec, body := f.reshare(t)
	if rec.Code != http.StatusOK || body.Sharing == nil || body.Sharing.Status != shareOutcomePartial || len(body.Sharing.Skipped) != 2 {
		t.Fatalf("reshare while refused = %d %+v", rec.Code, body.Sharing)
	}

	// The policy is fixed in Nextcloud.
	f.setRefused()
	rec, body = f.reshare(t)
	if rec.Code != http.StatusOK || body.Sharing == nil || body.Sharing.Status != shareOutcomeComplete || len(body.Sharing.Skipped) != 0 {
		t.Fatalf("reshare after the fix = %d %+v", rec.Code, body.Sharing)
	}
	// Out of how many: the starter and the captured room.
	if body.Sharing.AudienceCount != 3 {
		t.Fatalf("reshare reports %d recipients, want alice, bob and marketing", body.Sharing.AudienceCount)
	}
	if !f.shared("0/bob") || !f.shared("1/marketing") {
		t.Fatalf("re-share did not reach them: %v", f.shares)
	}
	if outcome := f.outcome(t); outcome == nil || outcome.Status != shareOutcomeComplete {
		t.Fatalf("stored outcome = %+v", outcome)
	}
}

// The meeting is published already, so even the starter's refusal cannot
// unpublish it: it is reported like anyone else's.
func TestReshareReportsARefusedStarterInsteadOfFailing(t *testing.T) {
	f := newDirectPublishFixture(t, []sharePrincipal{{Type: "user", ID: "bob"}})
	if err := f.deliver(); err != nil {
		t.Fatal(err)
	}
	f.markJobSucceeded(t)
	// Her share was removed in Nextcloud, and the policy now refuses her.
	f.mu.Lock()
	delete(f.shares, "0/alice")
	f.mu.Unlock()
	f.setRefused("alice")
	rec, body := f.reshare(t)
	if rec.Code != http.StatusOK || body.Sharing == nil || body.Sharing.Status != shareOutcomePartial ||
		len(body.Sharing.Skipped) != 1 || body.Sharing.Skipped[0].ID != "alice" {
		t.Fatalf("reshare with a refused starter = %d %+v", rec.Code, body.Sharing)
	}
}

func TestReshareOnlyAppliesToAPublishedNextcloudMeeting(t *testing.T) {
	f := newDirectPublishFixture(t, []sharePrincipal{{Type: "user", ID: "bob"}})
	// Not published yet.
	if rec, _ := f.reshare(t); rec.Code != http.StatusConflict {
		t.Fatalf("reshare of an unpublished run = %d %s", rec.Code, rec.Body.String())
	}
	// A run that does not exist.
	rec := httptest.NewRecorder()
	f.rt.jobDetailHandler(rec, httptest.NewRequest(http.MethodPost, "/jobs/NOPE/reshare", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("reshare of a missing run = %d", rec.Code)
	}
	// Only POST.
	rec = httptest.NewRecorder()
	f.rt.jobDetailHandler(rec, httptest.NewRequest(http.MethodGet, "/jobs/"+directPublishJob+"/reshare", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET reshare = %d", rec.Code)
	}
	// A deployment that keeps recordings on this machine shares nothing.
	f.markJobSucceeded(t)
	f.rt.publishSink = &localPublishSink{siteRoot: t.TempDir(), logger: f.rt.logger}
	rec = httptest.NewRecorder()
	f.rt.jobDetailHandler(rec, httptest.NewRequest(http.MethodPost, "/jobs/"+directPublishJob+"/reshare", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("reshare on the local sink = %d", rec.Code)
	}
}

// Through Nextcloud the operator is reached only on routes info.xml declares,
// so an action missing there is a button that answers 404 in production.
func TestReshareRouteIsDeclaredForAdministrators(t *testing.T) {
	raw, err := os.ReadFile("../../../appinfo/info.xml")
	if err != nil {
		t.Fatal(err)
	}
	routes := regexp.MustCompile(`(?s)<route>\s*<url>([^<]+)</url>\s*<verb>([^<]+)</verb>\s*<access_level>([^<]+)</access_level>`).FindAllStringSubmatch(string(raw), -1)
	for _, route := range routes {
		pattern, err := regexp.Compile(route[1])
		if err != nil {
			continue
		}
		if pattern.MatchString("operator/jobs/01ABC/reshare") && strings.Contains(route[2], "POST") {
			if route[3] != "ADMIN" {
				t.Fatalf("reshare is declared with access %q, want ADMIN", route[3])
			}
			return
		}
	}
	t.Fatal("no info.xml route lets POST operator/jobs/<id>/reshare through AppAPI")
}

// Published is a fact about the meeting, not about the latest attempt: a
// rerun or summary that failed afterwards leaves it published and shareable.
// While an attempt is under way, it waits.
func TestReshareFollowsPublicationNotTheLatestAttempt(t *testing.T) {
	f := newDirectPublishFixture(t, []sharePrincipal{{Type: "user", ID: "bob"}}, "bob")
	if err := f.deliver(); err != nil {
		t.Fatal(err)
	}
	f.markJobSucceeded(t)
	f.setRefused()

	f.setJobState(t, "build", "running")
	if rec, _ := f.reshare(t); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "processed again") {
		t.Fatalf("reshare during a rerun = %d %s", rec.Code, rec.Body.String())
	}
	f.setJobState(t, "done", "failed")
	rec, body := f.reshare(t)
	if rec.Code != http.StatusOK || body.Sharing == nil || body.Sharing.Status != shareOutcomeComplete || !f.shared("0/bob") {
		t.Fatalf("reshare after a failed rerun = %d %+v", rec.Code, body.Sharing)
	}
}

// A refused starter is a failed publication, which a rerun repairs; re-share
// must not paper over it as "published, partly shared".
func TestReshareLeavesAStarterRefusalToTheRerun(t *testing.T) {
	f := newDirectPublishFixture(t, []sharePrincipal{{Type: "user", ID: "bob"}})
	if err := f.deliver(); err != nil {
		t.Fatal(err)
	}
	f.markJobSucceeded(t)
	if err := f.rt.store.RecordJobShareOutcome(t.Context(), directPublishJob, shareOutcomeStarterRefused, []skippedShare{{Type: "user", ID: "alice"}}); err != nil {
		t.Fatal(err)
	}
	if rec, _ := f.reshare(t); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "rerun") {
		t.Fatalf("reshare over a starter refusal = %d %s", rec.Code, rec.Body.String())
	}
}

// With no roster captured for the meeting, the room as it is now could
// include people who were never in it: refuse rather than guess.
func TestReshareRefusesWithoutACapturedAudience(t *testing.T) {
	f := newDirectPublishFixture(t, []sharePrincipal{{Type: "user", ID: "bob"}}, "bob")
	if err := f.deliver(); err != nil {
		t.Fatal(err)
	}
	f.markJobSucceeded(t)
	if _, err := f.rt.store.db.Exec(`UPDATE jobs SET room_audience_at=NULL WHERE id=?`, directPublishJob); err != nil {
		t.Fatal(err)
	}
	f.setRefused()
	if rec, _ := f.reshare(t); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "capture") {
		t.Fatalf("reshare without a captured audience = %d %s", rec.Code, rec.Body.String())
	}
	if f.shared("0/bob") {
		t.Fatal("re-shared with a guessed audience")
	}
}

// An expired meeting is gone for everyone: no re-share, and no notice asking
// for one.
func TestReshareRefusesAnExpiredMeetingAndItsNoticeGoes(t *testing.T) {
	f := newDirectPublishFixture(t, []sharePrincipal{{Type: "user", ID: "bob"}}, "bob")
	if err := f.deliver(); err != nil {
		t.Fatal(err)
	}
	f.markJobSucceeded(t)
	f.sink.cfg.lifecycle = f.rt.store
	if _, err := f.rt.store.db.Exec(`INSERT INTO meeting_lifecycle(name,file_id,document_path,state,age_anchor,anchor_source,created_at_utc,recorded_at_local,reason)
VALUES(?,42,?,'retired','2026-01-01T00:00:00Z','test','','',?)`, directPublishJob+".opus", ncRecordingsRoot+"/meetings/"+directPublishJob+".opus", meetingRetireReasonExpired); err != nil {
		t.Fatal(err)
	}
	f.setRefused()
	if rec, _ := f.reshare(t); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "expired") {
		t.Fatalf("reshare of an expired meeting = %d %s", rec.Code, rec.Body.String())
	}
	job, err := f.rt.store.GetJob(t.Context(), directPublishJob)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.rt.jobSharing(t.Context(), &job); got != nil || job.UnsharedCount != 0 {
		t.Fatalf("an expired meeting still reports its sharing: %+v (count %d)", got, job.UnsharedCount)
	}
}

// Other open panels hear of it.
func TestReshareAnnouncesTheNewOutcome(t *testing.T) {
	f := newDirectPublishFixture(t, []sharePrincipal{{Type: "user", ID: "bob"}}, "bob")
	if err := f.deliver(); err != nil {
		t.Fatal(err)
	}
	f.markJobSucceeded(t)
	f.setRefused()
	hub := newEventHub()
	f.rt.events = hub
	f.rt.store.SetStateChangePublisher(f.rt.publishStateChangeEvent)
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	if rec, _ := f.reshare(t); rec.Code != http.StatusOK {
		t.Fatalf("reshare = %d %s", rec.Code, rec.Body.String())
	}
	select {
	case event := <-events:
		if event.JobID != directPublishJob || event.Sharing == nil || event.Sharing.Status != shareOutcomeComplete {
			t.Fatalf("event = %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event after a re-share")
	}
}

// A receiver hears of a re-share that changed who has the meeting, with the
// payload publication sends, and not of one that changed nothing.
func TestReshareEmitsSharesCompletedOnlyWhenTheOutcomeChanged(t *testing.T) {
	f := newDirectPublishFixture(t, []sharePrincipal{{Type: "user", ID: "bob"}, {Type: "group", ID: "marketing"}}, "bob", "marketing")
	if err := f.deliver(); err != nil {
		t.Fatal(err)
	}
	f.markJobSucceeded(t)
	attachEventWebhookForTest(t, f.rt, eventSharesCompleted)

	// Still refused: the outcome is what it was.
	if rec, _ := f.reshare(t); rec.Code != http.StatusOK {
		t.Fatalf("reshare = %d %s", rec.Code, rec.Body.String())
	}
	if types := eventOutboxTypes(t, f.rt.store); len(types) != 0 {
		t.Fatalf("events after a re-share that changed nothing = %v", types)
	}

	f.setRefused()
	if rec, _ := f.reshare(t); rec.Code != http.StatusOK {
		t.Fatalf("reshare = %d %s", rec.Code, rec.Body.String())
	}
	var raw string
	if err := f.rt.store.db.QueryRow(`SELECT payload FROM event_outbox`).Scan(&raw); err != nil {
		t.Fatalf("no shares.completed after the re-share reached them: %v", err)
	}
	var payload struct {
		Data struct {
			AttemptNumber int             `json:"attempt_number"`
			Reconciled    bool            `json:"reconciled"`
			Sharing       jobShareOutcome `json:"sharing"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Data.Reconciled || payload.Data.Sharing.Status != shareOutcomeComplete || payload.Data.Sharing.AudienceCount != 3 || payload.Data.AttemptNumber == 0 {
		t.Fatalf("payload = %s, want the complete outcome, its audience count and the job's attempt", raw)
	}
}
