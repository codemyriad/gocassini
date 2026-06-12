package talk

// Regression guard for the shutdown data races fixed under D-355:
// cleanup() used to nil r.signaling and r.sessionArtifact while the
// event loop (and pion callbacks) could still dereference them — a
// Go-memory-model race with a real nil-pointer panic window that
// killed the process before compose. A join event delivered during
// cleanup could also resurrect a fresh subscriber into the
// just-drained map. This test delivers a room join concurrently with
// cleanup(); pre-fix it fails under -race (and can panic inside
// sendPeerMessage when the nil-ed signaling client is observed).

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gocassini/internal/config"
	"gocassini/internal/signaling"
)

func TestCleanupConcurrentWithJoinEventHasNoRaces(t *testing.T) {
	finalOutput := filepath.Join(t.TempDir(), "recording.mkv")
	artifact, err := newSessionCaptureArtifact(finalOutput, "https://cloud.example/call/tok", "tok", "Recorder Bot")
	if err != nil {
		t.Fatalf("newSessionCaptureArtifact() error = %v", err)
	}

	r := &Recorder{
		cfg:               config.Config{},
		subscribers:       make(map[string]*subscriberPeer),
		sessionsByRemote:  make(map[string]*sessionCapture),
		identityByRemote:  make(map[string]participantIdentity),
		subscriberUpdates: make(chan struct{}, 1),
		startedAt:         time.Now().UTC(),
		// Never connected: Send degrades to a "not connected" error,
		// which is exactly how a closed client behaves at shutdown.
		signaling:       signaling.NewClient("ws://127.0.0.1:1/spreed", true),
		sessionArtifact: artifact,
		finalOutputPath: finalOutput,
		segmentsDir:     t.TempDir(),
		// sessionPath stays empty so composeFinalOutput fails fast
		// instead of invoking the remux pipeline.
	}

	// Pre-seed a capture session so the join event takes the
	// shouldUpdateArtifact path in rememberParticipantIdentity, which
	// dereferences r.sessionArtifact.
	session := &sessionCapture{
		RemoteSessionID: "remote-session-1",
		ParticipantName: "participant-remote-s",
		Index:           1,
		StartedAt:       time.Now().UTC(),
	}
	r.sessionsByRemote[session.RemoteSessionID] = session
	r.sessionOrder = append(r.sessionOrder, session)

	joinEvent := map[string]any{
		"target": "room",
		"type":   "join",
		"join": []any{
			map[string]any{
				"sessionid":   "remote-session-1",
				"displayName": "Alice",
				"userid":      "alice",
			},
		},
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := r.handleRoomEvent(joinEvent); err != nil {
			t.Errorf("handleRoomEvent() error = %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		// Compose fails (empty session path); the point is that cleanup
		// must not race or panic against the concurrent join.
		_ = r.cleanup(context.Background())
	}()
	wg.Wait()

	r.mu.Lock()
	leaked := len(r.subscribers)
	r.mu.Unlock()
	if leaked != 0 {
		t.Errorf("subscribers map has %d entries after cleanup; a join during shutdown must not resurrect peers", leaked)
	}
}
