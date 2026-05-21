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
