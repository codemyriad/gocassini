package talk

import "testing"

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
