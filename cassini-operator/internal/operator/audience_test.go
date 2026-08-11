package operator

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingFetcher stands in for the Talk participants API, recording which
// identity asked and returning a scripted answer per identity.
type recordingFetcher struct {
	mu     sync.Mutex
	calls  []string
	answer map[string]func(n int) ([]aclMapping, error)
}

func (f *recordingFetcher) fetch(_ context.Context, owner, _ string) ([]aclMapping, error) {
	f.mu.Lock()
	f.calls = append(f.calls, owner)
	n := 0
	for _, c := range f.calls {
		if c == owner {
			n++
		}
	}
	fn := f.answer[owner]
	f.mu.Unlock()
	if fn == nil {
		return nil, errors.New("no scripted answer")
	}
	return fn(n)
}

func (f *recordingFetcher) callsFor(owner string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == owner {
			n++
		}
	}
	return n
}

func newAudienceRuntime(t *testing.T, f *recordingFetcher) *Runtime {
	t.Helper()
	rt := &Runtime{
		ctx:                   context.Background(),
		logger:                log.New(ioDiscard{}, "", 0),
		fetchTalkParticipants: f.fetch,
		talkAudienceRetryGap:  time.Millisecond,
	}
	return rt
}

func TestAudienceRetriesATransientLookupFailure(t *testing.T) {
	// The reason this exists: the sink fails the publish when the audience
	// cannot be resolved, so a single 5xx used to cost a recording.
	f := &recordingFetcher{answer: map[string]func(int) ([]aclMapping, error){
		"starter": func(n int) ([]aclMapping, error) {
			if n < 3 {
				return nil, errors.New("participants request returned 503")
			}
			return []aclMapping{{Type: "user", ID: "alice"}}, nil
		},
	}}
	rt := newAudienceRuntime(t, f)

	mappings, source, err := rt.resolveRecordingAudience(context.Background(), "job1", "starter", "room1")
	if err != nil {
		t.Fatalf("resolveRecordingAudience() error = %v, want the retry to succeed", err)
	}
	if len(mappings) != 1 || mappings[0].ID != "alice" {
		t.Fatalf("mappings = %#v, want alice", mappings)
	}
	if source != audienceSourceStarter {
		t.Fatalf("source = %q, want %q", source, audienceSourceStarter)
	}
	if got := f.callsFor("starter"); got != 3 {
		t.Fatalf("starter calls = %d, want 3", got)
	}
	if got := f.callsFor(ncRecordingsOwner); got != 0 {
		t.Fatalf("owner calls = %d, want 0 — tier 2 must not run when tier 1 succeeds", got)
	}
}

func TestAudienceFallsBackToTheRecordingsOwner(t *testing.T) {
	// The case retrying alone cannot fix: the starter left the room between
	// record and publish, so asking as them will never work.
	f := &recordingFetcher{answer: map[string]func(int) ([]aclMapping, error){
		"departed": func(int) ([]aclMapping, error) {
			return nil, errors.New("participants request returned 404")
		},
		ncRecordingsOwner: func(int) ([]aclMapping, error) {
			return []aclMapping{{Type: "user", ID: "bob"}}, nil
		},
	}}
	rt := newAudienceRuntime(t, f)

	mappings, source, err := rt.resolveRecordingAudience(context.Background(), "job1", "departed", "room1")
	if err != nil {
		t.Fatalf("resolveRecordingAudience() error = %v, want the owner fallback to succeed", err)
	}
	if len(mappings) != 1 || mappings[0].ID != "bob" {
		t.Fatalf("mappings = %#v, want bob", mappings)
	}
	if source != audienceSourceOwner {
		t.Fatalf("source = %q, want %q", source, audienceSourceOwner)
	}
	if got := f.callsFor("departed"); got != talkAudienceAttempts {
		t.Fatalf("starter calls = %d, want %d before falling back", got, talkAudienceAttempts)
	}
	if got := f.callsFor(ncRecordingsOwner); got != 1 {
		t.Fatalf("owner calls = %d, want exactly 1 — tier 2 is a different question, not a retry", got)
	}
}

func TestAudienceReportsFailureWhenEveryTierFails(t *testing.T) {
	f := &recordingFetcher{answer: map[string]func(int) ([]aclMapping, error){
		"starter":         func(int) ([]aclMapping, error) { return nil, errors.New("boom-starter") },
		ncRecordingsOwner: func(int) ([]aclMapping, error) { return nil, errors.New("boom-owner") },
	}}
	rt := newAudienceRuntime(t, f)

	_, _, err := rt.resolveRecordingAudience(context.Background(), "job1", "starter", "room1")
	if err == nil {
		t.Fatalf("expected an error when every tier fails")
	}
	// The joined error must name both tiers, or the operator cannot tell a
	// departed starter from an unreachable Nextcloud.
	if msg := err.Error(); !strings.Contains(msg, "boom-starter") || !strings.Contains(msg, "boom-owner") {
		t.Fatalf("error = %v, want it to carry both tiers' failures", err)
	}
}

func TestAudienceTreatsAnEmptyRoomAsAnAnswerNotAFailure(t *testing.T) {
	// A guests-only room genuinely has no local principal to grant. That must
	// not read as "we could not find out" — the sink fails the publish on the
	// latter and must not on the former.
	f := &recordingFetcher{answer: map[string]func(int) ([]aclMapping, error){
		"starter": func(int) ([]aclMapping, error) { return nil, nil },
	}}
	rt := newAudienceRuntime(t, f)

	mappings, _, err := rt.resolveRecordingAudience(context.Background(), "job1", "starter", "room1")
	if err != nil {
		t.Fatalf("resolveRecordingAudience() error = %v, want an empty answer", err)
	}
	if len(mappings) != 0 {
		t.Fatalf("mappings = %#v, want empty", mappings)
	}
	if got := f.callsFor("starter"); got != 1 {
		t.Fatalf("starter calls = %d, want 1 — an empty answer must not be retried", got)
	}
}

func TestAudienceSkipsTheOwnerTierWhenTheStarterIsTheOwner(t *testing.T) {
	f := &recordingFetcher{answer: map[string]func(int) ([]aclMapping, error){
		ncRecordingsOwner: func(int) ([]aclMapping, error) { return nil, errors.New("nope") },
	}}
	rt := newAudienceRuntime(t, f)

	if _, _, err := rt.resolveRecordingAudience(context.Background(), "job1", ncRecordingsOwner, "room1"); err == nil {
		t.Fatalf("expected an error")
	}
	// Asking the same identity a fourth time is not a fallback.
	if got := f.callsFor(ncRecordingsOwner); got != talkAudienceAttempts {
		t.Fatalf("owner calls = %d, want %d", got, talkAudienceAttempts)
	}
}

func TestAudienceAbortsOnContextCancellation(t *testing.T) {
	f := &recordingFetcher{answer: map[string]func(int) ([]aclMapping, error){
		"starter": func(int) ([]aclMapping, error) { return nil, errors.New("slow") },
	}}
	rt := newAudienceRuntime(t, f)
	rt.talkAudienceRetryGap = time.Hour // the gap must be interruptible

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = rt.resolveRecordingAudience(ctx, "job1", "starter", "room1")
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("resolveRecordingAudience did not abort on cancellation")
	}
}

// applyNCFilesAccessStrict is what the nextcloud-files sink calls, so which of
// its exits are fatal decides whether a recording publishes. It had no coverage
// at all before D-553; these pin every exit.

func TestApplyAccessStrictSkipsANonTalkJob(t *testing.T) {
	// A job with no room binding has no audience to derive. Non-fatal: there is
	// nothing to look up, so nothing failed.
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.applyNCFilesAccessFn = func(context.Context, string, []aclMapping, bool) error {
		t.Fatalf("applier must not run for a non-Talk job")
		return nil
	}

	if err := rt.applyNCFilesAccessStrict(context.Background(), "no-such-job"); err != nil {
		t.Fatalf("applyNCFilesAccessStrict() error = %v, want nil for a non-Talk job", err)
	}
}

func TestApplyAccessStrictFailsWhenTheAudienceCannotBeResolved(t *testing.T) {
	// The publish-failing case: every tier of the lookup failed, so we do not
	// know who this recording belongs to.
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	seedTalkJob(t, rt, "job-unresolved")
	rt.talkAudienceRetryGap = time.Millisecond
	rt.applyNCFilesAccessFn = func(context.Context, string, []aclMapping, bool) error { return nil }
	rt.fetchTalkParticipants = func(context.Context, string, string) ([]aclMapping, error) {
		return nil, errors.New("participants request returned 503")
	}

	err := rt.applyNCFilesAccessStrict(context.Background(), "job-unresolved")
	if err == nil {
		t.Fatalf("expected an unresolved audience to be fatal")
	}
	if !strings.Contains(err.Error(), "participants lookup") {
		t.Fatalf("error = %v, want it to name the lookup step", err)
	}
}

func TestApplyAccessStrictAcceptsARoomWithNoGrantablePrincipals(t *testing.T) {
	// Guests/email/federated only. Fail-closed (owner-only) but NOT fatal —
	// treating it as fatal would make every guest-heavy meeting unpublishable.
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	seedTalkJob(t, rt, "job-guests")
	applied := false
	rt.applyNCFilesAccessFn = func(context.Context, string, []aclMapping, bool) error {
		applied = true
		return nil
	}
	rt.fetchTalkParticipants = func(context.Context, string, string) ([]aclMapping, error) {
		return nil, nil
	}

	if err := rt.applyNCFilesAccessStrict(context.Background(), "job-guests"); err != nil {
		t.Fatalf("applyNCFilesAccessStrict() error = %v, want nil for a guests-only room", err)
	}
	if applied {
		t.Fatalf("the applier must not run with no principals to grant")
	}
}

func TestApplyAccessStrictFailsWhenTheACLWriteFails(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	seedTalkJob(t, rt, "job-aclfail")
	rt.fetchTalkParticipants = func(context.Context, string, string) ([]aclMapping, error) {
		return []aclMapping{{Type: "user", ID: "alice"}}, nil
	}
	rt.applyNCFilesAccessFn = func(context.Context, string, []aclMapping, bool) error {
		return errors.New("acl opus: PROPPATCH -> 500")
	}

	err := rt.applyNCFilesAccessStrict(context.Background(), "job-aclfail")
	if err == nil {
		t.Fatalf("expected a failed ACL write to be fatal")
	}
	if !strings.Contains(err.Error(), "acl opus") {
		t.Fatalf("error = %v, want the underlying ACL failure", err)
	}
}

func TestApplyAccessStrictGrantsTheResolvedAudience(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	seedTalkJob(t, rt, "job-ok")
	var granted []aclMapping
	rt.fetchTalkParticipants = func(context.Context, string, string) ([]aclMapping, error) {
		// A group invitee who never joined is still an attendee, which is the
		// distinctive half of "everyone with access to the room".
		return []aclMapping{{Type: "user", ID: "alice"}, {Type: "group", ID: "team"}}, nil
	}
	rt.applyNCFilesAccessFn = func(_ context.Context, jobID string, m []aclMapping, _ bool) error {
		if jobID != "job-ok" {
			t.Errorf("applier got jobID %q, want job-ok", jobID)
		}
		granted = m
		return nil
	}

	if err := rt.applyNCFilesAccessStrict(context.Background(), "job-ok"); err != nil {
		t.Fatalf("applyNCFilesAccessStrict() error = %v", err)
	}
	if len(granted) != 2 || granted[0].ID != "alice" || granted[1].ID != "team" {
		t.Fatalf("granted = %#v, want alice + team", granted)
	}
}
