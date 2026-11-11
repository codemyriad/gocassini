package talk

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"gocassini/internal/config"
)

func TestSeenParticipantsIncludesLateJoinerAndSkipsGuests(t *testing.T) {
	dir := t.TempDir()
	r := &Recorder{
		cfg:              config.Config{OutputPath: filepath.Join(dir, "recording.mkv")},
		identityByRemote: make(map[string]participantIdentity),
		seenParticipants: make(map[string]participantIdentity),
	}
	r.rememberParticipantIdentity("s-alice", "Alice", "alice", "users", true)
	r.rememberParticipantIdentity("s-bob", "Bob", "bob", "users", true)
	r.rememberParticipantIdentity("s-guest", "Guest", "guest", "guests", true)
	// Room presence without an in-call bit is not attendance.
	r.rememberParticipantIdentity("s-roomonly", "Room only", "roomonly", "users", false)

	r.writeSeenParticipants()
	raw, err := os.ReadFile(filepath.Join(dir, seenParticipantsFilename))
	if err != nil {
		t.Fatalf("read seen participants: %v", err)
	}
	var got []seenParticipant
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode seen participants: %v", err)
	}
	if len(got) != 2 || got[0].ActorID != "alice" || got[1].ActorID != "bob" {
		t.Fatalf("seen participants = %+v, want alice and bob only", got)
	}

	// Leaving the call does not remove an account already seen: that is the
	// #479 gap the monotonic list closes.
	r.forgetParticipantIdentity("s-alice")
	r.writeSeenParticipants()
	raw, err = os.ReadFile(filepath.Join(dir, seenParticipantsFilename))
	if err != nil {
		t.Fatalf("read seen participants after leave: %v", err)
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode seen participants after leave: %v", err)
	}
	if len(got) != 2 || got[0].ActorID != "alice" {
		t.Fatalf("seen participants after leave = %+v, want alice still present", got)
	}
}
