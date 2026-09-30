package talk

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestCapturePolicyRecoveryMatrix(t *testing.T) {
	now := time.Now()
	for _, audioOnly := range []bool{false, true} {
		for _, offered := range []bool{false, true} {
			for _, connected := range []bool{false, true} {
				for _, pending := range []bool{false, true} {
					for _, packets := range []int{0, 1} {
						name := fmt.Sprintf("audioOnly=%t/offered=%t/connected=%t/pending=%t/packets=%d", audioOnly, offered, connected, pending, packets)
						t.Run(name, func(t *testing.T) {
							state := peerReconcileState{audioOnly: audioOnly, noEligibleAudio: !offered, createdAt: now.Add(-time.Hour), offerReceived: true, answeredAt: now.Add(-time.Hour), iceConnected: connected, hasPendingAnswer: pending, pendingAnswerSince: now.Add(-time.Second)}
							want := actionRebuild
							switch {
							case pending:
								want = actionRetryAnswer
							case audioOnly && !offered:
								want = actionRequestOffer
							case packets > 0 || audioOnly && connected:
								want = actionNone
							}
							if got := reconcileActionFor(now, state, packets); got != want {
								t.Fatalf("got %s want %s", got, want)
							}
							state.rebuildCount = maxCaptureRebuilds
							state.answerRetransmits = maxAnswerRetransmits
							if got := reconcileActionFor(now, state, packets); got == actionRebuild || got == actionRetryAnswer {
								t.Fatalf("exceeded recovery budget: %s", got)
							}
						})
					}
				}
			}
		}
	}
}

func TestNoAudioFailsBeforePublishing(t *testing.T) {
	output := filepath.Join(t.TempDir(), "empty.mkv")
	artifact, err := newSessionCaptureArtifact(output, "", "room", "recorder", false)
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.close()
	r := &Recorder{sessionArtifact: artifact, sessionPath: artifact.sessionPath, finalOutputPath: output}
	err = r.composeFinalOutputFromSessionArtifact()
	if err == nil || !strings.Contains(err.Error(), "no captured audio") {
		t.Fatalf("unclear no-audio result: %v", err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("empty recording published: %v", err)
	}
}

func TestAudioOnlyGapsDoNotClaimSilenceIsFailure(t *testing.T) {
	r := &Recorder{inCallEver: map[string]struct{}{"quiet": {}}, identityByRemote: map[string]participantIdentity{}}
	gaps := r.detectCaptureGaps(nil)
	if len(gaps) != 1 || gaps[0].Reason != "no-audio-observed" || !strings.Contains(gaps[0].warning(), "may be silent") {
		t.Fatalf("misleading silence diagnostic: %+v", gaps)
	}
}

func TestValidSilentOpusComposesAudioOnlyRecording(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg required")
	}
	output := filepath.Join(t.TempDir(), "silence.mkv")
	a, err := newSessionCaptureArtifact(output, "", "room", "recorder", false)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	start := time.Now()
	id, err := a.openStream("sid", "pid", "Silent participant", trackDescriptor{kind: "audio", codec: "audio/opus", clockRate: 48000}, 1, 111, start)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if err := a.writeRTP(id, &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 111, SSRC: 1, SequenceNumber: uint16(i), Timestamp: uint32(i * 960)}, Payload: []byte{0xf8, 0xff, 0xfe}}, start.Add(time.Duration(i)*20*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.close(); err != nil {
		t.Fatal(err)
	}
	r := &Recorder{sessionArtifact: a, sessionPath: a.sessionPath, finalOutputPath: output, segmentsDir: t.TempDir()}
	if err := r.composeFinalOutputFromSessionArtifact(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(output); err != nil || info.Size() == 0 {
		t.Fatalf("silent audio failed: %v", err)
	}
}
