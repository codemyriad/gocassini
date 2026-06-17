package talk

// Regression guards for D-365: subscribers must be created from
// participants-update inCall flag transitions, not from signaling-room
// join events. Per the standalone signaling API, room join/leave events
// indicate room presence only ("a WebRTC peerconnection should be
// established if a participant has the inCall flag set"); subscribing on
// join created dead peers for chat-window-only users — server-rejected
// requestoffer traffic ("not in same call") and a subscriberCount() that
// blocked the room-empty autostop indefinitely.

import (
	"testing"

	"gocassini/internal/signaling"
)

// newInCallSubscribeTestRecorder builds a recorder whose signaling client
// was never connected: Send degrades to a "not connected" error, which the
// subscriber-creation path logs and tolerates.
func newInCallSubscribeTestRecorder() *Recorder {
	r := newAutostopTestRecorder()
	r.signaling = signaling.NewClient("ws://127.0.0.1:1/spreed", true)
	return r
}

func TestRoomJoinDoesNotCreateSubscriber(t *testing.T) {
	r := newInCallSubscribeTestRecorder()

	err := r.handleRoomEvent(map[string]any{
		"target": "room",
		"type":   "join",
		"join": []any{
			map[string]any{
				"sessionid":   "alice-session",
				"displayName": "Alice",
				"userid":      "alice",
			},
		},
	})
	if err != nil {
		t.Fatalf("handleRoomEvent() error = %v", err)
	}

	if got := r.subscriberCount(); got != 0 {
		t.Fatalf("room join created %d subscriber(s); joins indicate room presence only, subscriptions must wait for the inCall flag", got)
	}
	identity := r.identityByRemote["alice-session"]
	if identity.DisplayName != "Alice" || identity.ParticipantID != "alice" {
		t.Fatalf("expected the join to remember the participant identity, got %+v", identity)
	}
}

func TestParticipantsUpdateCreatesSubscriberOnInCallTransition(t *testing.T) {
	r := newInCallSubscribeTestRecorder()

	err := r.handleParticipantsEvent(map[string]any{
		"changed": []any{
			map[string]any{
				"sessionId":   "alice-session",
				"displayName": "Alice",
				"userId":      "alice",
				"inCall":      float64(3), // in call + publishing audio
			},
		},
	})
	if err != nil {
		t.Fatalf("handleParticipantsEvent() error = %v", err)
	}
	t.Cleanup(func() { r.removeParticipantSessions([]any{"alice-session"}) })

	if got := r.subscriberCount(); got != 1 {
		t.Fatalf("expected the inCall transition to create one subscriber, got %d", got)
	}
	if _, ok := r.subscribers["alice-session"]; !ok {
		t.Fatalf("expected a subscriber for alice-session")
	}
}

func TestParticipantsUpdateWithoutInCallBitDoesNotSubscribe(t *testing.T) {
	r := newInCallSubscribeTestRecorder()

	err := r.handleParticipantsEvent(map[string]any{
		"changed": []any{
			// Room presence update without any inCall flags: must not
			// produce a subscriber.
			map[string]any{
				"sessionId":   "bob-session",
				"displayName": "Bob",
			},
		},
	})
	if err != nil {
		t.Fatalf("handleParticipantsEvent() error = %v", err)
	}

	if got := r.subscriberCount(); got != 0 {
		t.Fatalf("participants update without the in-call bit created %d subscriber(s)", got)
	}
}

func TestParseParticipantUpdateRequiresInCallBit(t *testing.T) {
	tests := []struct {
		name      string
		user      map[string]any
		wantState participantCallState
	}{
		{
			name:      "in-call bit set",
			user:      map[string]any{"sessionId": "s1", "inCall": float64(1)},
			wantState: callStateInCall,
		},
		{
			name:      "in-call with published media",
			user:      map[string]any{"sessionId": "s1", "inCall": float64(7)},
			wantState: callStateInCall,
		},
		{
			name:      "explicitly out of call",
			user:      map[string]any{"sessionId": "s1", "inCall": float64(0)},
			wantState: callStateNotInCall,
		},
		{
			// Missing inCall is neutral: it must not subscribe, and it
			// must not remove a live subscriber either (a hypothetical
			// partial update omitting the field is not an out-of-call
			// signal).
			name:      "inCall flags missing means room presence only",
			user:      map[string]any{"sessionId": "s1", "displayName": "Alice"},
			wantState: callStateUnknown,
		},
		{
			name:      "media bits without the in-call bit",
			user:      map[string]any{"sessionId": "s1", "inCall": float64(2)},
			wantState: callStateNotInCall,
		},
		{
			name:      "internal sessions never subscribe",
			user:      map[string]any{"sessionId": "s1", "internal": true, "inCall": float64(1)},
			wantState: callStateNotInCall,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sessionID, _, state := parseParticipantUpdate(tc.user)
			if sessionID != "s1" {
				t.Fatalf("unexpected session id: %q", sessionID)
			}
			if state != tc.wantState {
				t.Fatalf("call state mismatch: got=%d want=%d", state, tc.wantState)
			}
		})
	}
}

func TestParticipantsUpdateWithoutInCallFieldDoesNotRemoveSubscriber(t *testing.T) {
	r := newInCallSubscribeTestRecorder()

	if err := r.handleParticipantsEvent(map[string]any{
		"changed": []any{
			map[string]any{
				"sessionId":   "alice-session",
				"displayName": "Alice",
				"userId":      "alice",
				"inCall":      float64(1),
			},
		},
	}); err != nil {
		t.Fatalf("handleParticipantsEvent() error = %v", err)
	}
	t.Cleanup(func() { r.removeParticipantSessions([]any{"alice-session"}) })
	if got := r.subscriberCount(); got != 1 {
		t.Fatalf("expected the inCall transition to create one subscriber, got %d", got)
	}

	// A partial update omitting inCall says nothing about call
	// membership; the live subscriber must survive it.
	if err := r.handleParticipantsEvent(map[string]any{
		"changed": []any{
			map[string]any{
				"sessionId":   "alice-session",
				"displayName": "Alice Renamed",
			},
		},
	}); err != nil {
		t.Fatalf("handleParticipantsEvent() error = %v", err)
	}
	if got := r.subscriberCount(); got != 1 {
		t.Fatalf("participants update without an inCall field tore down the subscriber: got %d, want 1", got)
	}

	// Only an explicit inCall with the in-call bit clear removes.
	if err := r.handleParticipantsEvent(map[string]any{
		"changed": []any{
			map[string]any{
				"sessionId": "alice-session",
				"inCall":    float64(0),
			},
		},
	}); err != nil {
		t.Fatalf("handleParticipantsEvent() error = %v", err)
	}
	if got := r.subscriberCount(); got != 0 {
		t.Fatalf("explicit inCall=0 should remove the subscriber, got %d", got)
	}
}
