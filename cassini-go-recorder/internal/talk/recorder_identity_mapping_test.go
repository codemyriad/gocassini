package talk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gocassini/pkg/core/session"
)

func TestGuestParticipantDisplayNameDecoupledSessionID(t *testing.T) {
	tmp := t.TempDir()
	artifactPath := filepath.Join(tmp, "guest-identity.mkv")
	artifact, err := newSessionCaptureArtifact(artifactPath, "https://example.test/call/room", "room-token", "recorder")
	if err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	defer func() {
		_ = artifact.close()
	}()

	r := &Recorder{
		sessionArtifact:     artifact,
		sessionPath:         artifact.sessionPath,
		subscribers:         make(map[string]*subscriberPeer),
		inCallEver:          make(map[string]struct{}),
		sessionsByRemote:    make(map[string]*sessionCapture),
		identityByRemote:    make(map[string]participantIdentity),
		remoteByRoomSession: make(map[string]string),
		remoteByParticipant: make(map[string]string),
	}

	signalingSessionID := "sXHFMabV7NOPjrF_VqGx8qzQ_jEnMozJrLpHrYliUxY1h1-HAaptquQgfCW78K3IXg"
	nextcloudRoomSessionID := "2NAEY4ljG7l1ZQqnwcNR8LyN"
	guestActorID := "9de480c78effa50443d6aca4944d6997d7722d79"

	// 1. Room join event arrives: guest has no display name yet, but has sessionid, roomsessionid, and userid.
	err = r.handleRoomEvent(map[string]any{
		"target": "room",
		"type":   "join",
		"join": []any{
			map[string]any{
				"sessionid":     signalingSessionID,
				"roomsessionid": nextcloudRoomSessionID,
				"userid":        guestActorID,
			},
		},
	})
	if err != nil {
		t.Fatalf("handleRoomEvent(join): %v", err)
	}

	// 2. Track arrives from signalingSessionID before display name update arrives.
	sessionCap, err := r.ensureSessionCapture(signalingSessionID)
	if err != nil {
		t.Fatalf("ensureSessionCapture: %v", err)
	}
	if sessionCap.ParticipantName != "participant-sXHFMabV" {
		t.Fatalf("expected synthetic name participant-sXHFMabV, got %q", sessionCap.ParticipantName)
	}

	desc := trackDescriptor{
		kind:      "audio",
		codec:     "audio/opus",
		mid:       "janus",
		clockRate: 48000,
	}
	streamID, err := artifact.openStream(signalingSessionID, sessionCap.ParticipantID, sessionCap.ParticipantName, desc, 12345, 111, time.Now())
	if err != nil {
		t.Fatalf("openStream: %v", err)
	}
	if streamID == "" {
		t.Fatalf("expected non-empty streamID")
	}

	// 3. Participants update arrives from Nextcloud Talk with roomSessionID ("2NAEY4lj..."), guestActorID, and "Phone".
	err = r.handleParticipantsEvent(map[string]any{
		"users": []any{
			map[string]any{
				"sessionId":   nextcloudRoomSessionID,
				"actorId":     guestActorID,
				"displayName": "Phone",
				"inCall":      float64(1),
			},
		},
	})
	if err != nil {
		t.Fatalf("handleParticipantsEvent: %v", err)
	}

	// 4. Verify recorder's in-memory session capture was updated to "Phone".
	r.sessionMu.Lock()
	updatedSession := r.sessionsByRemote[signalingSessionID]
	r.sessionMu.Unlock()
	if updatedSession == nil {
		t.Fatalf("expected sessionCapture for %s", signalingSessionID)
	}
	if updatedSession.ParticipantName != "Phone" {
		t.Fatalf("expected session ParticipantName = %q, got %q", "Phone", updatedSession.ParticipantName)
	}

	// 5. Verify session artifact in-memory metadata was updated to "Phone".
	artifact.mu.Lock()
	participants := append([]session.Participant(nil), artifact.sessionMeta.Participants...)
	artifact.mu.Unlock()
	if len(participants) != 1 {
		t.Fatalf("expected 1 participant, got %d", len(participants))
	}
	if participants[0].Display != "Phone" {
		t.Fatalf("expected participant display %q, got %q", "Phone", participants[0].Display)
	}

	// 6. Verify session.json file on disk was persisted with "Phone".
	raw, err := os.ReadFile(artifact.sessionPath)
	if err != nil {
		t.Fatalf("read session.json: %v", err)
	}
	if !strings.Contains(string(raw), `"display": "Phone"`) {
		t.Fatalf("expected session.json to contain Phone: %s", string(raw))
	}
}

func TestParticipantsUpdateCallStateUnknownPreservesIdentity(t *testing.T) {
	tmp := t.TempDir()
	artifactPath := filepath.Join(tmp, "presence-identity.mkv")
	artifact, err := newSessionCaptureArtifact(artifactPath, "https://example.test/call/room", "room-token", "recorder")
	if err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	defer func() {
		_ = artifact.close()
	}()

	r := &Recorder{
		sessionArtifact:     artifact,
		sessionPath:         artifact.sessionPath,
		subscribers:         make(map[string]*subscriberPeer),
		inCallEver:          make(map[string]struct{}),
		sessionsByRemote:    make(map[string]*sessionCapture),
		identityByRemote:    make(map[string]participantIdentity),
		remoteByRoomSession: make(map[string]string),
		remoteByParticipant: make(map[string]string),
	}

	signalingSessionID := "remote-user-xyz"
	actorID := "actor-guest-123"

	// Join with empty display name
	err = r.handleRoomEvent(map[string]any{
		"target": "room",
		"type":   "join",
		"join": []any{
			map[string]any{
				"sessionid": signalingSessionID,
				"actorid":   actorID,
			},
		},
	})
	if err != nil {
		t.Fatalf("handleRoomEvent: %v", err)
	}

	// Track opens with placeholder name
	sessionCap, err := r.ensureSessionCapture(signalingSessionID)
	if err != nil {
		t.Fatalf("ensureSessionCapture: %v", err)
	}
	desc := trackDescriptor{
		kind:      "audio",
		codec:     "audio/opus",
		mid:       "janus",
		clockRate: 48000,
	}
	if _, err := artifact.openStream(signalingSessionID, sessionCap.ParticipantID, sessionCap.ParticipantName, desc, 12345, 111, time.Now()); err != nil {
		t.Fatalf("openStream: %v", err)
	}

	// Update arrives without inCall field (presence/attendee display name update)
	err = r.handleParticipantsEvent(map[string]any{
		"users": []any{
			map[string]any{
				"sessionId":   signalingSessionID,
				"actorId":     actorID,
				"displayName": "Desktop computer",
			},
		},
	})
	if err != nil {
		t.Fatalf("handleParticipantsEvent: %v", err)
	}

	// Presence update must NOT create a subscriberpeer because inCall is missing
	if count := r.subscriberCount(); count != 0 {
		t.Fatalf("expected 0 subscribers from callStateUnknown update, got %d", count)
	}

	// But identity and artifact display MUST be updated
	r.sessionMu.Lock()
	ident := r.identityByRemote[signalingSessionID]
	sess := r.sessionsByRemote[signalingSessionID]
	r.sessionMu.Unlock()

	if ident.DisplayName != "Desktop computer" {
		t.Fatalf("expected identity DisplayName = %q, got %q", "Desktop computer", ident.DisplayName)
	}
	if sess.ParticipantName != "Desktop computer" {
		t.Fatalf("expected session ParticipantName = %q, got %q", "Desktop computer", sess.ParticipantName)
	}

	artifact.mu.Lock()
	display := artifact.sessionMeta.Participants[0].Display
	artifact.mu.Unlock()
	if display != "Desktop computer" {
		t.Fatalf("expected artifact display = %q, got %q", "Desktop computer", display)
	}
}
