package talk

// These tests guard the subscribe-race fix introduced for the Talk
// e2e harness: when spreed-signaling-server emits a "participants"
// event marking a remote session as inCall=1 AFTER the recorder's
// first requestoffer to that session was silently rejected at
// hub.go:2948 ("not in the same call"), the recorder must drop the
// 8-second response throttle on that subscriber and send a fresh
// requestoffer immediately. Without this, the only retry happens
// 64 seconds later (8 attempts × 8s throttle), which exceeds any
// reasonable recording window and explains why pre-fix runs only
// subscribed to bots that joined the call before the recorder did.

import (
	"testing"
	"time"
)

func partialInCallUpdate() map[string]any {
	return map[string]any{
		"changed": []any{
			map[string]any{
				"sessionId":   "alice-session",
				"displayName": "Alice",
				"userId":      "alice",
				"inCall":      float64(3),
			},
		},
	}
}

func syncAllInCallUpdate() map[string]any {
	return map[string]any{
		"all": true,
		"users": []any{
			map[string]any{
				"sessionId":   "alice-session",
				"displayName": "Alice",
				"userId":      "alice",
				"inCall":      float64(3),
			},
		},
	}
}

func expectSingleRequestOffer(t *testing.T, messages <-chan map[string]any) {
	t.Helper()

	select {
	case data := <-messages:
		if got := asString(data["type"]); got != "requestoffer" {
			t.Fatalf("signaling message type = %q, want requestoffer (data=%v)", got, data)
		}
		if got := asString(data["to"]); got != "alice-session" {
			t.Fatalf("requestoffer recipient = %q, want alice-session (data=%v)", got, data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for requestoffer")
	}

	// WebSocket delivery is asynchronous. Leave a short quiet window after
	// the expected message so an accidental immediate duplicate is observed.
	select {
	case data := <-messages:
		t.Fatalf("unexpected second signaling message after requestoffer: %v", data)
	case <-time.After(200 * time.Millisecond):
	}
}

func peerRequestOfferAttempts(peer *subscriberPeer) int {
	peer.mu.Lock()
	defer peer.mu.Unlock()
	return peer.requestOfferAttempts
}

func TestParticipantUpdateSendsOneInitialRequestOffer(t *testing.T) {
	tests := []struct {
		name   string
		update func() map[string]any
	}{
		{name: "partial update", update: partialInCallUpdate},
		{name: "sync-all update", update: syncAllInCallUpdate},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, messages := newPeerMessageCapture(t)
			if err := r.handleParticipantsEvent(tc.update()); err != nil {
				t.Fatalf("handleParticipantsEvent: %v", err)
			}
			t.Cleanup(func() { r.removeParticipantSessions([]any{"alice-session"}) })

			expectSingleRequestOffer(t, messages)

			peer := r.subscribers["alice-session"]
			if peer == nil {
				t.Fatal("new in-call participant has no subscriber")
			}
			if got := peerRequestOfferAttempts(peer); got != 1 {
				t.Fatalf("new subscriber requestOfferAttempts = %d, want 1", got)
			}
		})
	}
}

func TestParticipantUpdateRetriesPreExistingThrottledSubscriber(t *testing.T) {
	tests := []struct {
		name   string
		update func() map[string]any
	}{
		{name: "partial update", update: partialInCallUpdate},
		{name: "sync-all update", update: syncAllInCallUpdate},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, messages := newPeerMessageCapture(t)
			if err := r.handleParticipantsEvent(tc.update()); err != nil {
				t.Fatalf("create subscriber: %v", err)
			}
			t.Cleanup(func() { r.removeParticipantSessions([]any{"alice-session"}) })
			expectSingleRequestOffer(t, messages)

			peer := r.subscribers["alice-session"]
			if peer == nil {
				t.Fatal("new in-call participant has no subscriber")
			}
			if got := peerRequestOfferAttempts(peer); got != 1 {
				t.Fatalf("initial requestOfferAttempts = %d, want 1", got)
			}

			// The peer now predates this participants update and is still
			// throttled waiting for an offer. This is the call-transition race
			// the eager retry exists to recover.
			if err := r.handleParticipantsEvent(tc.update()); err != nil {
				t.Fatalf("retry existing subscriber: %v", err)
			}
			expectSingleRequestOffer(t, messages)
			if got := peerRequestOfferAttempts(peer); got != 2 {
				t.Fatalf("existing subscriber requestOfferAttempts = %d, want 2 after transition retry", got)
			}
		})
	}
}

func TestParticipantUpdateSendsOneExhaustedRecoveryRequest(t *testing.T) {
	tests := []struct {
		name   string
		update func() map[string]any
	}{
		{name: "partial update", update: partialInCallUpdate},
		{name: "sync-all update", update: syncAllInCallUpdate},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, messages := newPeerMessageCapture(t)
			if err := r.handleParticipantsEvent(tc.update()); err != nil {
				t.Fatalf("create subscriber: %v", err)
			}
			t.Cleanup(func() { r.removeParticipantSessions([]any{"alice-session"}) })
			expectSingleRequestOffer(t, messages)

			peer := r.subscribers["alice-session"]
			if peer == nil {
				t.Fatal("new in-call participant has no subscriber")
			}
			peer.mu.Lock()
			peer.requestOfferAttempts = 8
			peer.awaitingOfferSince = time.Now()
			peer.offerExhaustedLogged = true
			peer.mu.Unlock()

			// ensureSubscriber owns recovery from exhaustion: it resets the
			// throttle and issues one request. The transition hook must not add
			// a second empty-SID request on top of it.
			if err := r.handleParticipantsEvent(tc.update()); err != nil {
				t.Fatalf("recover exhausted subscriber: %v", err)
			}
			expectSingleRequestOffer(t, messages)
			if got := peerRequestOfferAttempts(peer); got != 1 {
				t.Fatalf("recovered subscriber requestOfferAttempts = %d, want 1", got)
			}
		})
	}
}

func TestClearOfferThrottleForCallTransitionResetsThrottle(t *testing.T) {
	// Simulate the state right after a single requestoffer that got
	// silently rejected: awaitingOfferSince is set to "now", one
	// attempt has been counted, no offer received yet, not yet
	// exhausted. clearOfferThrottleForCallTransition must reset
	// awaitingOfferSince so the next requestOffer call goes out
	// immediately instead of being throttle-skipped.
	peer := &subscriberPeer{
		requestOfferAttempts: 1,
		awaitingOfferSince:   time.Now(),
	}

	changed := peer.clearOfferThrottleForCallTransition()

	if !changed {
		t.Fatalf("clearOfferThrottleForCallTransition returned false; expected true so the caller knows to retry")
	}
	if !peer.awaitingOfferSince.IsZero() {
		t.Errorf("awaitingOfferSince = %v, want zero (throttle should be cleared)", peer.awaitingOfferSince)
	}
}

func TestClearOfferThrottleForCallTransitionNoOpWhenOfferReceived(t *testing.T) {
	// If the offer already arrived between the first requestoffer and
	// the participants update we observed, we must NOT clear the
	// throttle — otherwise the requestOfferLoop ticker would spam the
	// peer with fresh requestoffers it doesn't need.
	original := time.Now().Add(-3 * time.Second)
	peer := &subscriberPeer{
		offerReceived:        true,
		requestOfferAttempts: 1,
		awaitingOfferSince:   original,
	}

	changed := peer.clearOfferThrottleForCallTransition()

	if changed {
		t.Errorf("clearOfferThrottleForCallTransition returned true with offer already received; expected false")
	}
	if !peer.awaitingOfferSince.Equal(original) {
		t.Errorf("awaitingOfferSince mutated to %v despite offer already received; want %v", peer.awaitingOfferSince, original)
	}
}

func TestClearOfferThrottleForCallTransitionNoOpBeforeAnyAttempt(t *testing.T) {
	// On a freshly-created subscriberPeer the creation path will send
	// its own initial requestoffer immediately (see ensureSubscriber).
	// The transition hook must stay out of the way in that window so
	// we don't double-send.
	peer := &subscriberPeer{}

	changed := peer.clearOfferThrottleForCallTransition()

	if changed {
		t.Errorf("clearOfferThrottleForCallTransition returned true before any attempt; expected false (initial create path sends its own requestoffer)")
	}
}

func TestClearOfferThrottleForCallTransitionDefersToExhaustedReset(t *testing.T) {
	// Once max attempts are reached the peer is in the "backing off"
	// state that resetIfExhausted is responsible for unwinding. The
	// transition hook must NOT short-circuit that path by clearing
	// the throttle without also clearing the attempt counter and
	// the exhausted-logged flag — otherwise we'd send a requestoffer
	// that gets immediately rejected again by the max-attempts gate.
	peer := &subscriberPeer{
		requestOfferAttempts: 8,
		awaitingOfferSince:   time.Now(),
		offerExhaustedLogged: true,
	}

	changed := peer.clearOfferThrottleForCallTransition()

	if changed {
		t.Errorf("clearOfferThrottleForCallTransition returned true with exhausted-logged peer; expected false (resetIfExhausted owns this transition)")
	}
}

func TestResetIfExhaustedClearsCounterAndThrottle(t *testing.T) {
	// resetIfExhausted is the OTHER half of the subscribe-race fix:
	// when a participant transitions back into the call after we
	// gave up on them, we need to clear *all* gating state so the
	// next requestoffer goes out. This existed before the bug we
	// just shipped a fix for, but the bug exposed a regression risk
	// that's worth pinning.
	peer := &subscriberPeer{
		requestOfferAttempts: 8,
		offerExhaustedLogged: true,
		awaitingOfferSince:   time.Now(),
	}

	if !peer.resetIfExhausted() {
		t.Fatalf("resetIfExhausted returned false on an exhausted peer; expected true")
	}
	if peer.requestOfferAttempts != 0 {
		t.Errorf("requestOfferAttempts = %d, want 0", peer.requestOfferAttempts)
	}
	if peer.offerExhaustedLogged {
		t.Errorf("offerExhaustedLogged still true after reset")
	}
	if !peer.awaitingOfferSince.IsZero() {
		t.Errorf("awaitingOfferSince = %v, want zero", peer.awaitingOfferSince)
	}
}

func TestResetIfExhaustedNoOpWhenNotExhausted(t *testing.T) {
	// Peers that are still within their attempt budget must NOT be
	// reset by this method — the throttle is what's keeping the
	// requestOfferLoop from hammering the signaling server.
	peer := &subscriberPeer{
		requestOfferAttempts: 1,
		awaitingOfferSince:   time.Now(),
	}

	if peer.resetIfExhausted() {
		t.Errorf("resetIfExhausted returned true on a peer with attempts=1, exhaustedLogged=false; expected false")
	}
}
