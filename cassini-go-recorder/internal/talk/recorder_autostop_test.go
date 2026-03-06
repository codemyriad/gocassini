package talk

import "testing"

func newAutostopTestRecorder() *Recorder {
	return &Recorder{
		subscribers:       map[string]*subscriberPeer{},
		subscriberUpdates: make(chan struct{}, 16),
		sessionsByRemote:  map[string]*sessionCapture{},
		identityByRemote:  map[string]participantIdentity{},
	}
}

func TestNextRoomEmptyTimerAction(t *testing.T) {
	tests := []struct {
		name            string
		hasSeenRemote   bool
		timerArmed      bool
		subscriberCount int
		wantSeenRemote  bool
		wantAction      roomEmptyTimerAction
	}{
		{
			name:            "idle room before first participant",
			hasSeenRemote:   false,
			timerArmed:      false,
			subscriberCount: 0,
			wantSeenRemote:  false,
			wantAction:      roomEmptyTimerNoop,
		},
		{
			name:            "first participant joins",
			hasSeenRemote:   false,
			timerArmed:      false,
			subscriberCount: 1,
			wantSeenRemote:  true,
			wantAction:      roomEmptyTimerNoop,
		},
		{
			name:            "room empties after participants were seen",
			hasSeenRemote:   true,
			timerArmed:      false,
			subscriberCount: 0,
			wantSeenRemote:  true,
			wantAction:      roomEmptyTimerArm,
		},
		{
			name:            "room stays empty while timer already armed",
			hasSeenRemote:   true,
			timerArmed:      true,
			subscriberCount: 0,
			wantSeenRemote:  true,
			wantAction:      roomEmptyTimerArm,
		},
		{
			name:            "participant rejoins while timer armed",
			hasSeenRemote:   true,
			timerArmed:      true,
			subscriberCount: 2,
			wantSeenRemote:  true,
			wantAction:      roomEmptyTimerDisarm,
		},
		{
			name:            "safety disarm if timer armed before any participant",
			hasSeenRemote:   false,
			timerArmed:      true,
			subscriberCount: 0,
			wantSeenRemote:  false,
			wantAction:      roomEmptyTimerDisarm,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			gotSeenRemote, gotAction := nextRoomEmptyTimerAction(tt.hasSeenRemote, tt.timerArmed, tt.subscriberCount)
			if gotSeenRemote != tt.wantSeenRemote {
				t.Fatalf("seen remote mismatch: got=%t want=%t", gotSeenRemote, tt.wantSeenRemote)
			}
			if gotAction != tt.wantAction {
				t.Fatalf("action mismatch: got=%d want=%d", gotAction, tt.wantAction)
			}
		})
	}
}

func TestHandleParticipantsEventClearsSubscribersWhenRoomIsEmpty(t *testing.T) {
	r := newAutostopTestRecorder()
	r.subscribers["alice"] = nil
	r.subscribers["bob"] = nil
	r.identityByRemote["alice"] = participantIdentity{DisplayName: "Alice", ParticipantID: "user-a"}
	r.identityByRemote["bob"] = participantIdentity{DisplayName: "Bob", ParticipantID: "user-b"}

	err := r.handleParticipantsEvent(map[string]any{
		"all":    true,
		"incall": float64(0),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := r.subscriberCount(); got != 0 {
		t.Fatalf("expected all subscribers removed, got=%d", got)
	}
	if len(r.identityByRemote) != 0 {
		t.Fatalf("expected participant identities cleared, got=%d", len(r.identityByRemote))
	}
}

func TestHandleParticipantsEventSyncsAuthoritativeSnapshot(t *testing.T) {
	r := newAutostopTestRecorder()
	r.subscribers["alice"] = &subscriberPeer{}
	r.subscribers["bob"] = nil
	r.identityByRemote["alice"] = participantIdentity{DisplayName: "Alice", ParticipantID: "user-a"}
	r.identityByRemote["bob"] = participantIdentity{DisplayName: "Bob", ParticipantID: "user-b"}

	err := r.handleParticipantsEvent(map[string]any{
		"all": true,
		"users": []any{
			map[string]any{
				"sessionId":   "alice",
				"displayName": "Alice Updated",
				"userId":      "user-a",
				"inCall":      float64(1),
			},
			map[string]any{
				"sessionId": "internal-observer",
				"internal":  true,
				"inCall":    float64(1),
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := r.subscriberCount(); got != 1 {
		t.Fatalf("expected stale subscriber removed, got=%d", got)
	}
	if _, ok := r.subscribers["alice"]; !ok {
		t.Fatalf("expected alice subscriber to remain")
	}
	if _, ok := r.subscribers["bob"]; ok {
		t.Fatalf("expected bob subscriber to be removed")
	}
	if got := r.identityByRemote["alice"].DisplayName; got != "Alice Updated" {
		t.Fatalf("unexpected alice display name: %q", got)
	}
	if _, ok := r.identityByRemote["bob"]; ok {
		t.Fatalf("expected bob identity to be removed")
	}
}

func TestHandleParticipantsEventRemovesUserMarkedOutOfCall(t *testing.T) {
	r := newAutostopTestRecorder()
	r.subscribers["alice"] = &subscriberPeer{}
	r.subscribers["bob"] = nil
	r.identityByRemote["alice"] = participantIdentity{DisplayName: "Alice", ParticipantID: "user-a"}
	r.identityByRemote["bob"] = participantIdentity{DisplayName: "Bob", ParticipantID: "user-b"}

	err := r.handleParticipantsEvent(map[string]any{
		"changed": []any{
			map[string]any{
				"sessionId": "bob",
				"inCall":    float64(0),
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := r.subscriberCount(); got != 1 {
		t.Fatalf("expected one subscriber left, got=%d", got)
	}
	if _, ok := r.subscribers["bob"]; ok {
		t.Fatalf("expected bob subscriber removed")
	}
	if _, ok := r.identityByRemote["bob"]; ok {
		t.Fatalf("expected bob identity removed")
	}
}
