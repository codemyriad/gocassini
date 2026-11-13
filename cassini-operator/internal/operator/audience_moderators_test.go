package operator

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// "Only the room's moderators" (#480): the owner and moderators receive a
// recording whether or not they were in the call, and nobody else does.

func TestParticipantMappingsModeratorsKeepsOwnerAndModerators(t *testing.T) {
	rows := []participantRow{
		{ActorType: "users", ActorID: "owner", ParticipantType: 1},
		{ActorType: "users", ActorID: "moderator", ParticipantType: 2}, // not in the call
		{ActorType: "users", ActorID: "member", ParticipantType: 3, InCall: 1},
		{ActorType: "users", ActorID: "self-joined", ParticipantType: 5, InCall: 1},
		{ActorType: "guests", ActorID: "guest-moderator", ParticipantType: 6},
		{ActorType: "groups", ActorID: "team-eng", ParticipantType: 3},
		{ActorType: "federated_users", ActorID: "remote@other", ParticipantType: 2},
	}
	got := participantMappings(rows, shareAudienceModerators)
	want := []sharePrincipal{{Type: "user", ID: "owner"}, {Type: "user", ID: "moderator"}}
	if len(got) != len(want) {
		t.Fatalf("participantMappings(moderators) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("mapping[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestCaptureRoomAudienceUsesModeratorsWhenConfigured(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	seedTalkJob(t, rt, "job-moderators")
	if _, err := rt.store.db.Exec(`UPDATE jobs SET audience_mode = 'moderators' WHERE id = 'job-moderators'`); err != nil {
		t.Fatal(err)
	}
	rt.talkAudienceRetryGap = time.Millisecond
	rt.fetchTalkParticipants = func(context.Context, string, string) ([]sharePrincipal, error) {
		t.Error("room fetcher called in moderator mode")
		return nil, nil
	}
	rt.fetchTalkAttendees = func(context.Context, string, string) ([]sharePrincipal, error) {
		t.Error("attendee fetcher called in moderator mode")
		return nil, nil
	}
	rt.fetchTalkModerators = func(context.Context, string, string) ([]sharePrincipal, error) {
		return []sharePrincipal{{Type: "user", ID: "alice"}, {Type: "user", ID: "olga"}}, nil
	}

	rt.captureRoomAudience("job-moderators", "alice", "tok123", roomAudiencePhaseStart)

	mappings, captured, err := rt.store.JobRoomAudience(context.Background(), "job-moderators")
	if err != nil || !captured {
		t.Fatalf("JobRoomAudience() captured=%v err=%v", captured, err)
	}
	if len(mappings) != 2 || mappings[0].ID != "alice" || mappings[1].ID != "olga" {
		t.Fatalf("mappings = %+v, want alice and olga", mappings)
	}
}

// The recorder's list of people seen in the call must not reach a
// moderator-mode roster: it would hand the recording to every attendee.
func TestSeenParticipantsStayOutOfAModeratorAudience(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	id := "job-seen"
	seedTalkJob(t, rt, id)
	for _, mode := range []string{shareAudienceRoom, shareAudienceAttendees, shareAudienceModerators} {
		if _, err := rt.store.db.Exec(`UPDATE jobs SET audience_mode = ? WHERE id = ?`, mode, id); err != nil {
			t.Fatal(err)
		}
		if got, want := rt.jobAudienceIncludesAttendees(id), mode != shareAudienceModerators; got != want {
			t.Errorf("jobAudienceIncludesAttendees(%s) = %v, want %v", mode, got, want)
		}
	}
	if rt.jobAudienceIncludesAttendees("no-such-job") {
		t.Error("an unreadable job must not take the in-call list")
	}
}

// No list of moderators was saved during the call: publication shares with the
// starter only, without asking Talk, and leaves a notice.
func TestModeratorModeWithoutCapturedRosterSharesOnlyTheStarter(t *testing.T) {
	f := newDirectPublishFixture(t, []sharePrincipal{{Type: "user", ID: "old-recipient"}})
	if _, err := f.rt.store.db.Exec(`UPDATE jobs SET room_audience = NULL, room_audience_at = NULL, audience_mode = 'moderators' WHERE id = ?`, directPublishJob); err != nil {
		t.Fatal(err)
	}
	lookup := func(context.Context, string, string) ([]sharePrincipal, error) {
		t.Error("moderator mode asked Talk at publish time")
		return nil, nil
	}
	f.rt.fetchTalkParticipants, f.rt.fetchTalkModerators = lookup, lookup
	if err := f.deliver(); err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	if len(f.posted) != 1 || !f.shared("0/alice") {
		t.Fatalf("posted=%v shares=%v, want the starter only", f.posted, f.shares)
	}
	job, err := f.rt.store.GetJob(context.Background(), directPublishJob)
	if err != nil {
		t.Fatal(err)
	}
	if job.AudienceNotice != jobAudienceNoticeNoModerators {
		t.Fatalf("audience notice = %q, want %q", job.AudienceNotice, jobAudienceNoticeNoModerators)
	}
}

// The way back from that notice: an administrator's re-share reads the room's
// moderators then, shares with them and clears the notice.
func TestReshareReadsModeratorsWhenNoneWereCaptured(t *testing.T) {
	f := newDirectPublishFixture(t, nil)
	if _, err := f.rt.store.db.Exec(`UPDATE jobs SET room_audience = NULL, room_audience_at = NULL, audience_mode = 'moderators' WHERE id = ?`, directPublishJob); err != nil {
		t.Fatal(err)
	}
	if err := f.deliver(); err != nil {
		t.Fatal(err)
	}
	f.markJobSucceeded(t)
	if f.shared("0/olga") {
		t.Fatal("publication shared with a moderator it never read")
	}
	f.rt.talkAudienceRetryGap = time.Millisecond
	f.rt.fetchTalkModerators = func(context.Context, string, string) ([]sharePrincipal, error) {
		return []sharePrincipal{{Type: "user", ID: "olga"}}, nil
	}
	if rec, _ := f.reshare(t); rec.Code != http.StatusOK {
		t.Fatalf("reshare = %d %s", rec.Code, rec.Body.String())
	}
	if !f.shared("0/olga") {
		t.Fatalf("shares = %v, want the moderator read at re-share", f.shares)
	}
	job, err := f.rt.store.GetJob(context.Background(), directPublishJob)
	if err != nil {
		t.Fatal(err)
	}
	if job.AudienceNotice != "" {
		t.Fatalf("audience notice = %q, want it cleared", job.AudienceNotice)
	}
}

func TestSavedModeratorAudienceIsRecognised(t *testing.T) {
	if got := normalizeSavedAudience("moderators"); got != shareAudienceModerators {
		t.Fatalf("normalizeSavedAudience(moderators) = %q", got)
	}
}

// Recipients of a public room's recording may pass it on, except in moderator
// mode, where the room is exactly who must not receive it.
func TestModeratorSharesAreReadOnlyInAPublicRoom(t *testing.T) {
	f := newDirectPublishFixture(t, []sharePrincipal{{Type: "user", ID: "olga"}})
	ctx := context.Background()
	if err := f.rt.store.SetJobTalkBinding(ctx, directPublishJob, `{"backend_url":"https://talk.example","room_token":"room1","owner":"alice","room_public":true}`); err != nil {
		t.Fatal(err)
	}
	if _, public, err := f.sink.audienceForJob(ctx, directPublishJob); err != nil || !public {
		t.Fatalf("room mode: public=%v err=%v, want the public room's reshare", public, err)
	}
	if _, err := f.rt.store.db.Exec(`UPDATE jobs SET audience_mode = 'moderators' WHERE id = ?`, directPublishJob); err != nil {
		t.Fatal(err)
	}
	audience, public, err := f.sink.audienceForJob(ctx, directPublishJob)
	if err != nil || public {
		t.Fatalf("moderator mode: public=%v err=%v, want read-only shares", public, err)
	}
	if len(audience) != 2 || audience[0].ID != "alice" || audience[1].ID != "olga" {
		t.Fatalf("audience = %+v, want the starter and the saved moderator", audience)
	}
}

// Talk would not say who moderates the room: nothing is shared, nothing is
// saved, and the next click asks again.
func TestReshareSavesNothingWhenModeratorsCannotBeRead(t *testing.T) {
	f := newDirectPublishFixture(t, nil)
	if _, err := f.rt.store.db.Exec(`UPDATE jobs SET room_audience = NULL, room_audience_at = NULL, audience_mode = 'moderators' WHERE id = ?`, directPublishJob); err != nil {
		t.Fatal(err)
	}
	if err := f.deliver(); err != nil {
		t.Fatal(err)
	}
	f.markJobSucceeded(t)
	f.rt.talkAudienceRetryGap = time.Millisecond
	f.rt.fetchTalkModerators = func(context.Context, string, string) ([]sharePrincipal, error) {
		return nil, errors.New("room is gone")
	}
	rec, _ := f.reshare(t)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "moderators") {
		t.Fatalf("reshare = %d %s, want the moderators lookup named", rec.Code, rec.Body.String())
	}
	if _, captured, err := f.rt.store.JobRoomAudience(context.Background(), directPublishJob); err != nil || captured {
		t.Fatalf("captured=%v err=%v, want no roster saved after a failed read", captured, err)
	}
	job, err := f.rt.store.GetJob(context.Background(), directPublishJob)
	if err != nil {
		t.Fatal(err)
	}
	if job.AudienceNotice != jobAudienceNoticeNoModerators {
		t.Fatalf("audience notice = %q, want it kept", job.AudienceNotice)
	}
}

// Only moderator mode reads the room afterwards. Who was in a call cannot be
// asked later, so attendee mode keeps refusing.
func TestReshareStillRefusesAnUncapturedAttendeeRecording(t *testing.T) {
	f := newDirectPublishFixture(t, nil)
	if _, err := f.rt.store.db.Exec(`UPDATE jobs SET room_audience = NULL, room_audience_at = NULL, audience_mode = 'attendees' WHERE id = ?`, directPublishJob); err != nil {
		t.Fatal(err)
	}
	if err := f.deliver(); err != nil {
		t.Fatal(err)
	}
	f.markJobSucceeded(t)
	lookup := func(context.Context, string, string) ([]sharePrincipal, error) {
		t.Error("an attendee-mode re-share asked Talk")
		return nil, nil
	}
	f.rt.fetchTalkModerators, f.rt.fetchTalkAttendees, f.rt.fetchTalkParticipants = lookup, lookup, lookup
	if rec, _ := f.reshare(t); rec.Code != http.StatusConflict {
		t.Fatalf("reshare = %d %s, want 409", rec.Code, rec.Body.String())
	}
}

func TestTalkNotificationSkippedInModeratorMode(t *testing.T) {
	var logs bytes.Buffer
	rt, cleanup := newBarePublishRuntime(t, log.New(&logs, "", 0))
	defer cleanup()
	seedTalkNotificationJob(t, rt.store, "one")
	if _, err := rt.store.db.Exec(`UPDATE jobs SET audience_mode = 'moderators' WHERE id = 'one'`); err != nil {
		t.Fatal(err)
	}
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	client := newTalkNotificationClient(ExAppConfig{NextcloudURL: server.URL, AppID: "gocassini", AppSecret: "test-secret", AppVersion: "1"})
	rt.publishSink = &notifyingTestSink{okPublishSink: okPublishSink{location: "remote"}, client: client}
	rt.publishJobFn = func(context.Context, publishTask) (string, error) {
		return filepath.Join(t.TempDir(), "attempt.site"), nil
	}
	if err := rt.store.MarkPublishQueued(rt.ctx, "one", "meeting", "meeting", nowUTCString()); err != nil {
		t.Fatal(err)
	}
	rt.runPublishJob(publishTask{JobID: "one", AttemptNumber: 1})
	if posts != 0 {
		t.Fatalf("posts = %d, want no room chat in moderator mode", posts)
	}
	if job := mustGetJob(t, rt.store, "one"); job.State != "succeeded" {
		t.Fatalf("state = %q, want succeeded", job.State)
	}
}
