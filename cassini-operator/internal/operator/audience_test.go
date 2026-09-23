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
