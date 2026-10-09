package talk

import (
	"context"
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
	artifact, err := newSessionCaptureArtifact(artifactPath, "https://example.test/call/room", "room-token", "recorder", true)
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

	// 3. Participants update arrives from Nextcloud Talk with roomSessionID ("2NAEY4lj..."), guestActorID, and "Lisa".
	err = r.handleParticipantsEvent(map[string]any{
		"users": []any{
			map[string]any{
				"sessionId":   nextcloudRoomSessionID,
				"actorId":     guestActorID,
				"displayName": "Lisa",
				"inCall":      float64(1),
			},
		},
	})
	if err != nil {
		t.Fatalf("handleParticipantsEvent: %v", err)
	}

	// 4. Verify recorder's in-memory session capture was updated to "Lisa".
	r.sessionMu.Lock()
	updatedSession := r.sessionsByRemote[signalingSessionID]
	r.sessionMu.Unlock()
	if updatedSession == nil {
		t.Fatalf("expected sessionCapture for %s", signalingSessionID)
	}
	if updatedSession.ParticipantName != "Lisa" {
		t.Fatalf("expected session ParticipantName = %q, got %q", "Lisa", updatedSession.ParticipantName)
	}

	// 5. Verify session artifact in-memory metadata was updated to "Lisa".
	artifact.mu.Lock()
	participants := append([]session.Participant(nil), artifact.sessionMeta.Participants...)
	artifact.mu.Unlock()
	if len(participants) != 1 {
		t.Fatalf("expected 1 participant, got %d", len(participants))
	}
	if participants[0].Display != "Lisa" {
		t.Fatalf("expected participant display %q, got %q", "Lisa", participants[0].Display)
	}

	// 6. Verify session.json file on disk was persisted with "Lisa".
	raw, err := os.ReadFile(artifact.sessionPath)
	if err != nil {
		t.Fatalf("read session.json: %v", err)
	}
	if !strings.Contains(string(raw), `"display": "Lisa"`) {
		t.Fatalf("expected session.json to contain Lisa: %s", string(raw))
	}
}

func TestParticipantsUpdateCallStateUnknownPreservesIdentity(t *testing.T) {
	tmp := t.TempDir()
	artifactPath := filepath.Join(tmp, "presence-identity.mkv")
	artifact, err := newSessionCaptureArtifact(artifactPath, "https://example.test/call/room", "room-token", "recorder", true)
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
				"displayName": "Mark",
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

	if ident.DisplayName != "Mark" {
		t.Fatalf("expected identity DisplayName = %q, got %q", "Mark", ident.DisplayName)
	}
	if sess.ParticipantName != "Mark" {
		t.Fatalf("expected session ParticipantName = %q, got %q", "Mark", sess.ParticipantName)
	}

	artifact.mu.Lock()
	display := artifact.sessionMeta.Participants[0].Display
	artifact.mu.Unlock()
	if display != "Mark" {
		t.Fatalf("expected artifact display = %q, got %q", "Mark", display)
	}
}

func TestResolveRemoteSessionIDAndForgetParticipantIdentity(t *testing.T) {
	r := &Recorder{
		sessionsByRemote:    make(map[string]*sessionCapture),
		identityByRemote:    make(map[string]participantIdentity),
		remoteByRoomSession: make(map[string]string),
	}

	remoteSessionID := "remote-session-1"
	roomSessionID := "room-session-1"
	participantID := "actor-guest-1"

	r.sessionMu.Lock()
	r.mapRemoteSessionLocked(remoteSessionID, roomSessionID, participantID)
	r.sessionMu.Unlock()
	r.rememberParticipantIdentity(remoteSessionID, "Alice", participantID)

	// 1. Direct match by remote signaling session ID
	if got := r.resolveRemoteSessionID(remoteSessionID, "", ""); got != remoteSessionID {
		t.Fatalf("expected %q, got %q", remoteSessionID, got)
	}

	// 2. Match via room session ID
	if got := r.resolveRemoteSessionID("", roomSessionID, ""); got != remoteSessionID {
		t.Fatalf("expected %q, got %q", remoteSessionID, got)
	}

	// 3. Fallback when sessionID and roomSessionID are unknown
	if got := r.resolveRemoteSessionID("unknown-session-2", "", participantID); got != "unknown-session-2" {
		t.Fatalf("expected unknown session to retain its own session ID without aliasing, got %q", got)
	}

	// 4. forgetParticipantIdentity prunes identity and index maps
	r.forgetParticipantIdentity(remoteSessionID)

	if got := r.resolveRemoteSessionID("", roomSessionID, ""); got != roomSessionID {
		t.Fatalf("expected roomSessionID to return itself after forget, got %q", got)
	}
}

func TestPresenceUpdateBeforeSignalingJoinResolvesCorrectly(t *testing.T) {
	tmp := t.TempDir()
	artifactPath := filepath.Join(tmp, "presence-first.mkv")
	artifact, err := newSessionCaptureArtifact(artifactPath, "https://example.test/call/room", "room-token", "recorder", true)
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
	}

	signalingSessionID := "signaling-sess-123"
	ncRoomSessionID := "nc-room-sess-456"
	guestActorID := "actor-guest-789"

	// 1. Presence update arrives FIRST (no inCall flag)
	err = r.handleParticipantsEvent(map[string]any{
		"users": []any{
			map[string]any{
				"sessionId":   ncRoomSessionID,
				"actorId":     guestActorID,
				"displayName": "Lisa",
			},
		},
	})
	if err != nil {
		t.Fatalf("handleParticipantsEvent: %v", err)
	}

	// 2. Signaling room join arrives SECOND
	err = r.handleRoomEvent(map[string]any{
		"target": "room",
		"type":   "join",
		"join": []any{
			map[string]any{
				"sessionid":     signalingSessionID,
				"roomsessionid": ncRoomSessionID,
				"userid":        guestActorID,
			},
		},
	})
	if err != nil {
		t.Fatalf("handleRoomEvent(join): %v", err)
	}

	// 3. Track arrives for signalingSessionID
	sessionCap, err := r.ensureSessionCapture(signalingSessionID)
	if err != nil {
		t.Fatalf("ensureSessionCapture: %v", err)
	}
	if sessionCap.ParticipantName != "Lisa" {
		t.Fatalf("expected ensureSessionCapture to have Lisa, got %q", sessionCap.ParticipantName)
	}

	// 4. resolveRemoteSessionID for ncRoomSessionID must resolve to signalingSessionID
	if resolved := r.resolveRemoteSessionID(ncRoomSessionID, "", guestActorID); resolved != signalingSessionID {
		t.Fatalf("expected ncRoomSessionID to resolve to %q, got %q", signalingSessionID, resolved)
	}
}

// newIdentityTestRecorder builds a recorder with a live session artifact and
// no signaling connection, enough to drive identity handling directly.
func newIdentityTestRecorder(t *testing.T) (*Recorder, *sessionCaptureArtifact) {
	t.Helper()
	artifact, err := newSessionCaptureArtifact(filepath.Join(t.TempDir(), "nick.mkv"), "https://example.test/call/room", "room-token", "recorder", true)
	if err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	t.Cleanup(func() { _ = artifact.close() })
	r := &Recorder{
		sessionArtifact:     artifact,
		sessionPath:         artifact.sessionPath,
		signalingSessionID:  "recorder-internal-session",
		subscribers:         make(map[string]*subscriberPeer),
		inCallEver:          make(map[string]struct{}),
		sessionsByRemote:    make(map[string]*sessionCapture),
		identityByRemote:    make(map[string]participantIdentity),
		remoteByRoomSession: make(map[string]string),
	}
	return r, artifact
}

// nickChangedMessage is the frame the signaling server delivers when a Talk
// client calls sendTo(recorder, 'nickChanged', {name}).
func nickChangedMessage(senderSessionID string, payload any) map[string]any {
	return map[string]any{
		"sender": map[string]any{"type": "session", "sessionid": senderSessionID},
		"data": map[string]any{
			"to":       "recorder-internal-session",
			"roomType": "video",
			"type":     "nickChanged",
			"payload":  payload,
		},
	}
}

// A guest who sets their name before their signaling session connects never
// appears with a displayName in any participants update the recorder sees:
// Nextcloud announces the name while the session is unknown to the signaling
// server, which drops the entry. Seen in the harness as a guest named Ana
// Silva recorded as participant-6mHDQx8p. The guest's client then announces
// the name with nickChanged, and that must replace the placeholder on the
// stream that is already open, in memory and in session.json.
func TestNickChangedNamesGuestWhoseNameUpdateWasDropped(t *testing.T) {
	r, artifact := newIdentityTestRecorder(t)
	ctx := context.Background()

	signalingSessionID := "6mHDQx8pIpqJ8HRhcG1vYwYaA5eq-IL91aAQEAg"
	roomSessionID := "57MlUbXLg72X2GDF45JMH9Fq"
	guestActorID := "86f96b8607af9e941d9c1568384952066b2218f7"

	// The order the harness logs show: the signaling join (no name),
	// then the in-call update (actor id, no name), then media.
	if err := r.handleRoomEvent(map[string]any{
		"target": "room",
		"type":   "join",
		"join": []any{map[string]any{
			"sessionid":     signalingSessionID,
			"roomsessionid": roomSessionID,
		}},
	}); err != nil {
		t.Fatalf("handleRoomEvent(join): %v", err)
	}
	if err := r.handleParticipantsEvent(map[string]any{
		"changed": []any{map[string]any{
			"sessionId":          signalingSessionID,
			"nextcloudSessionId": roomSessionID,
			"actorType":          "guests",
			"actorId":            guestActorID,
			"inCall":             float64(7),
		}},
	}); err != nil {
		t.Fatalf("handleParticipantsEvent(incall): %v", err)
	}
	t.Cleanup(func() { _ = r.removeSubscriber(signalingSessionID) })

	sessionCap, err := r.ensureSessionCapture(signalingSessionID)
	if err != nil {
		t.Fatalf("ensureSessionCapture: %v", err)
	}
	if sessionCap.ParticipantName != "participant-6mHDQx8p" {
		t.Fatalf("precondition: expected placeholder participant-6mHDQx8p, got %q", sessionCap.ParticipantName)
	}
	desc := trackDescriptor{kind: "audio", codec: "audio/opus", mid: "janus", clockRate: 48000}
	if _, err := artifact.openStream(signalingSessionID, sessionCap.ParticipantID, sessionCap.ParticipantName, desc, 3365858225, 111, time.Now()); err != nil {
		t.Fatalf("openStream: %v", err)
	}

	if err := r.handleSignalingMessage(ctx, nickChangedMessage(signalingSessionID, map[string]any{"name": "Ana Silva"})); err != nil {
		t.Fatalf("handleSignalingMessage(nickChanged): %v", err)
	}

	if _, name := r.sessionIdentity(signalingSessionID); name != "Ana Silva" {
		t.Fatalf("expected session name Ana Silva, got %q", name)
	}
	artifact.mu.Lock()
	participants := append([]session.Participant(nil), artifact.sessionMeta.Participants...)
	artifact.mu.Unlock()
	if len(participants) != 1 || participants[0].Display != "Ana Silva" || participants[0].PID != guestActorID {
		t.Fatalf("expected one participant %s displayed as Ana Silva, got %+v", guestActorID, participants)
	}
	raw, err := os.ReadFile(artifact.sessionPath)
	if err != nil {
		t.Fatalf("read session.json: %v", err)
	}
	if !strings.Contains(string(raw), `"display": "Ana Silva"`) {
		t.Fatalf("expected session.json to name Ana Silva: %s", raw)
	}
}

// Talk clients announce the name as soon as they see the recorder in the
// call, which is usually before the first media packet. The name must then
// be used when the stream opens, and the older bare-string payload must work
// as well as the {"name": ...} object.
func TestNickChangedBeforeMediaNamesTheStream(t *testing.T) {
	r, _ := newIdentityTestRecorder(t)
	ctx := context.Background()

	if err := r.handleSignalingMessage(ctx, nickChangedMessage("guest-session-1", "Noah Brandt")); err != nil {
		t.Fatalf("handleSignalingMessage(nickChanged): %v", err)
	}
	sessionCap, err := r.ensureSessionCapture("guest-session-1")
	if err != nil {
		t.Fatalf("ensureSessionCapture: %v", err)
	}
	if sessionCap.ParticipantName != "Noah Brandt" {
		t.Fatalf("expected stream named Noah Brandt, got %q", sessionCap.ParticipantName)
	}
	if count := r.subscriberCount(); count != 0 {
		t.Fatalf("nickChanged must not create subscribers, got %d", count)
	}
}

// The name applies to the session the signaling server says sent it. A "from"
// written into the payload by the client is ignored, so one participant cannot
// rename another.
func TestNickChangedIgnoresClientWrittenFrom(t *testing.T) {
	r, _ := newIdentityTestRecorder(t)
	ctx := context.Background()

	message := nickChangedMessage("mallory-session", map[string]any{"name": "Mallory"})
	asMap(message["data"])["from"] = "victim-session"
	if err := r.handleSignalingMessage(ctx, message); err != nil {
		t.Fatalf("handleSignalingMessage(nickChanged): %v", err)
	}

	victim, err := r.ensureSessionCapture("victim-session")
	if err != nil {
		t.Fatalf("ensureSessionCapture(victim): %v", err)
	}
	if victim.ParticipantName != "participant-victim-s" {
		t.Fatalf("victim must keep its placeholder, got %q", victim.ParticipantName)
	}
	sender, err := r.ensureSessionCapture("mallory-session")
	if err != nil {
		t.Fatalf("ensureSessionCapture(sender): %v", err)
	}
	if sender.ParticipantName != "Mallory" {
		t.Fatalf("expected the sender to be named Mallory, got %q", sender.ParticipantName)
	}
}
