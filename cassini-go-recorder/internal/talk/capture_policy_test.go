package talk

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"gocassini/internal/config"
)

func TestAudioOnlyArtifactRejectsVideoBeforeSideEffects(t *testing.T) {
	a, err := newSessionCaptureArtifact(filepath.Join(t.TempDir(), "recording.mkv"), "", "room", "recorder", false)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	for _, kind := range []string{"video", "unknown", ""} {
		_, err := a.openStream("remote", "participant", "name", trackDescriptor{kind: kind}, 1, 96, time.Now())
		if !errors.Is(err, errCaptureKindDenied) {
			t.Fatalf("kind %q: %v", kind, err)
		}
	}
	entries, err := os.ReadDir(a.streamsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 || len(a.sessionMeta.Participants) != 0 || len(a.sessionMeta.LogicalTracks) != 0 {
		t.Fatal("denied stream created capture state")
	}
	// Simulate a caller bypassing both OnTrack and openStream. Nil packets make
	// serialization panic if the artifact guard fails to run first.
	a.streams["bypass"] = &sessionCaptureStream{kind: "video"}
	if err := a.writeRTP("bypass", (*rtp.Packet)(nil), time.Now()); !errors.Is(err, errCaptureKindDenied) {
		t.Fatal(err)
	}
	if err := a.writeRTCP("bypass", []rtcp.Packet{nil}, time.Now()); !errors.Is(err, errCaptureKindDenied) {
		t.Fatal(err)
	}
	if a.captureFailure() != nil {
		t.Fatal("policy rejection marked capture damaged")
	}
	delete(a.streams, "bypass")
	id, err := a.openStream("remote", "participant", "name", trackDescriptor{kind: "audio", codec: "audio/opus", clockRate: 48000}, 1, 111, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if a.hasAudio() {
		t.Fatal("empty stream counted as audio")
	}
	if err := a.writeRTP(id, &rtp.Packet{Header: rtp.Header{Version: 2}, Payload: []byte{0xf8, 0xff, 0xfe}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !a.hasAudio() {
		t.Fatal("valid silence not counted as audio")
	}
}

func TestCaptureOfferPolicy(t *testing.T) {
	r := &Recorder{}
	payload := r.captureOfferPayload()
	if payload["audio"] != true || payload["video"] != false {
		t.Fatal(payload)
	}
	r.cfg = config.Config{RetainVideo: true}
	if r.captureOfferPayload() != nil {
		t.Fatal("opt-in changed existing subscription")
	}
}

func TestAudioOnlyReconcileSilenceAndWaiting(t *testing.T) {
	now := time.Now()
	state := peerReconcileState{audioOnly: true, createdAt: now.Add(-time.Hour), answeredAt: now.Add(-time.Hour), offerReceived: true, iceConnected: true}
	if got := reconcileActionFor(now, state, 0); got != actionNone {
		t.Fatalf("connected silence: %v", got)
	}
	state.noEligibleAudio = true
	if got := reconcileActionFor(now, state, 0); got != actionRequestOffer {
		t.Fatalf("waiting for audio: %v", got)
	}
	state.hasPendingAnswer = true
	state.pendingAnswerSince = now.Add(-time.Second)
	if got := reconcileActionFor(now, state, 0); got != actionRetryAnswer {
		t.Fatalf("pending answer: %v", got)
	}
	state.hasPendingAnswer = false
	state.noEligibleAudio = false
	state.iceConnected = false
	if got := reconcileActionFor(now, state, 0); got != actionRebuild {
		t.Fatalf("broken transport: %v", got)
	}
	state.audioOnly = false
	state.iceConnected = true
	if got := reconcileActionFor(now, state, 0); got != actionRebuild {
		t.Fatalf("opt-in legacy recovery: %v", got)
	}
}

func TestAudioOnlyRefreshUsesExistingSubscriberSID(t *testing.T) {
	r, messages := newPeerMessageCapture(t)
	p := &subscriberPeer{owner: r, remoteSessionID: "silent", offerReceived: true, noEligibleAudio: true, currentSID: "existing-subscription"}
	if err := p.requestOffer(); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-messages:
		if msg["type"] != "requestoffer" || msg["sid"] != "existing-subscription" {
			t.Fatal(msg)
		}
		payload := asMap(msg["payload"])
		if payload["audio"] != true || payload["video"] != false {
			t.Fatal(payload)
		}
	case <-time.After(time.Second):
		t.Fatal("no subscription refresh")
	}
	if err := p.requestOffer(); err != nil {
		t.Fatal(err)
	}
	if got := signalingProbe(t, r, messages); len(got) != 0 {
		t.Fatalf("refresh was not throttled: %v", got)
	}
}

func TestOfferedAudioAvailability(t *testing.T) {
	for _, tc := range []struct {
		media string
		want  bool
	}{
		{"m=audio 9 UDP/TLS/RTP/SAVPF 111\r\na=sendonly\r\n", true},
		{"m=audio 9 UDP/TLS/RTP/SAVPF 111\r\na=inactive\r\n", false},
		{"m=audio 0 UDP/TLS/RTP/SAVPF 111\r\n", false},
		{"m=video 9 UDP/TLS/RTP/SAVPF 96\r\n", false},
	} {
		offer := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\n" + tc.media}
		got, err := offerHasEligibleAudio(offer)
		if err != nil || got != tc.want {
			t.Fatalf("%q: got=%v err=%v", tc.media, got, err)
		}
	}
}
