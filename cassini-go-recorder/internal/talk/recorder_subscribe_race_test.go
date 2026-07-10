package talk

// These tests guard D-454's subscriber-generation race. A participants update
// reports desired in-call state, not a transition edge; sending another empty-
// SID requestoffer for an existing peer can make the MCU replace its Janus
// subscriber while the first offer is being answered. Initial and recovery
// requests must therefore be single and repeated snapshots idempotent.

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

func expectSingleRequestOffer(t *testing.T, r *Recorder, messages <-chan map[string]any) {
	t.Helper()
	got := signalingProbe(t, r, messages)
	if len(got) != 1 {
		t.Fatalf("signaling messages before probe = %v, want exactly one requestoffer", got)
	}
	if typ := asString(got[0]["type"]); typ != "requestoffer" {
		t.Fatalf("signaling message type = %q, want requestoffer (data=%v)", typ, got[0])
	}
	if to := asString(got[0]["to"]); to != "alice-session" {
		t.Fatalf("requestoffer recipient = %q, want alice-session (data=%v)", to, got[0])
	}
}

func peerRequestOfferAttempts(peer *subscriberPeer) int {
	peer.mu.Lock()
	defer peer.mu.Unlock()
	return peer.requestOfferAttempts
}

func expectNoSignalingMessage(t *testing.T, r *Recorder, messages <-chan map[string]any) {
	t.Helper()
	if got := signalingProbe(t, r, messages); len(got) != 0 {
		t.Fatalf("unexpected signaling messages: %v", got)
	}
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

			expectSingleRequestOffer(t, r, messages)

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

func TestRepeatedParticipantUpdateKeepsExistingRequestThrottled(t *testing.T) {
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
			expectSingleRequestOffer(t, r, messages)

			peer := r.subscribers["alice-session"]
			if peer == nil {
				t.Fatal("new in-call participant has no subscriber")
			}
			if got := peerRequestOfferAttempts(peer); got != 1 {
				t.Fatalf("initial requestOfferAttempts = %d, want 1", got)
			}

			// A repeated full/partial snapshot is not a transition edge. Keep the
			// valid response throttle instead of starting a second Janus join.
			if err := r.handleParticipantsEvent(tc.update()); err != nil {
				t.Fatalf("repeat participant update: %v", err)
			}
			expectNoSignalingMessage(t, r, messages)
			if got := peerRequestOfferAttempts(peer); got != 1 {
				t.Fatalf("existing subscriber requestOfferAttempts = %d, want 1", got)
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
			expectSingleRequestOffer(t, r, messages)

			peer := r.subscribers["alice-session"]
			if peer == nil {
				t.Fatal("new in-call participant has no subscriber")
			}
			peer.mu.Lock()
			peer.requestOfferAttempts = 8
			peer.awaitingOfferSince = time.Now()
			peer.offerExhaustedLogged = true
			peer.mu.Unlock()

			// Participant-state handling owns recovery from exhaustion: it resets
			// the throttle and issues one request, without an additional join.
			if err := r.handleParticipantsEvent(tc.update()); err != nil {
				t.Fatalf("recover exhausted subscriber: %v", err)
			}
			expectSingleRequestOffer(t, r, messages)
			if got := peerRequestOfferAttempts(peer); got != 1 {
				t.Fatalf("recovered subscriber requestOfferAttempts = %d, want 1", got)
			}
		})
	}
}

func TestEnsureSubscriberDoesNotRecoverExhaustedPeerFromInboundLookup(t *testing.T) {
	r, messages := newPeerMessageCapture(t)
	if err := r.handleParticipantsEvent(partialInCallUpdate()); err != nil {
		t.Fatalf("create subscriber: %v", err)
	}
	t.Cleanup(func() { r.removeParticipantSessions([]any{"alice-session"}) })
	expectSingleRequestOffer(t, r, messages)

	peer := r.subscribers["alice-session"]
	if peer == nil {
		t.Fatal("new in-call participant has no subscriber")
	}
	peer.mu.Lock()
	peer.requestOfferAttempts = 8
	peer.awaitingOfferSince = time.Now()
	peer.offerExhaustedLogged = true
	peer.mu.Unlock()

	// handleSignalingData uses ensureSubscriber before processing an inbound
	// offer/candidate. That lookup must not reset recovery state and issue a new
	// empty-SID request in front of the valid inbound negotiation message.
	got, err := r.ensureSubscriber("alice-session")
	if err != nil {
		t.Fatalf("ensure existing subscriber: %v", err)
	}
	if got != peer {
		t.Fatalf("ensureSubscriber returned peer %p, want existing %p", got, peer)
	}
	expectNoSignalingMessage(t, r, messages)

	peer.mu.Lock()
	attempts := peer.requestOfferAttempts
	exhausted := peer.offerExhaustedLogged
	peer.mu.Unlock()
	if attempts != 8 || !exhausted {
		t.Fatalf("inbound lookup changed exhausted state: attempts=%d exhausted=%t", attempts, exhausted)
	}
}

func TestResetIfExhaustedClearsCounterAndThrottle(t *testing.T) {
	// A later participant-state update may explicitly recover a peer whose
	// normal request burst was exhausted; reset all gating state together.
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
