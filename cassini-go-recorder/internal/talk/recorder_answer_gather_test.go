package talk

// Reproduction guard for D-454: the offer->answer path must not block on
// ICE gathering. #106 replaced the old 4s answer timer with a wait on
// GatheringCompletePromise, which resolves only after EVERY configured
// STUN/TURN server has been gathered or timed out. A single slow or
// unreachable ICE server (the "marginal TURN/ICE path" the issue points at)
// therefore stalls the SDP answer by the whole gather timeout
// (pion's stunGatherTimeout default is 5s). The delayed answer is what shows
// up in production as a subscriber that takes ~24s to reach ICE `connected`,
// misses the call window, and finalizes with "no remuxable streams" / an
// empty transcript.
//
// The recorder already trickles its own candidates via OnICECandidate, so the
// answer never needed the gathered candidates inline: it must go out
// immediately (trickle ICE) while candidates follow on their own.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"gocassini/internal/nextcloud"
)

// makeOfferSDP builds a valid SDP offer from a throwaway peer connection so
// the subscriber's SetRemoteDescription / CreateAnswer path can run for real.
func makeOfferSDP(t *testing.T) string {
	t.Helper()
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("new offerer peer: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })

	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio); err != nil {
		t.Fatalf("add audio transceiver: %v", err)
	}
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatalf("create offer: %v", err)
	}
	// The SDP carries ufrag/pwd/fingerprint immediately; we intentionally do
	// NOT wait for gathering — a trickle offer is exactly what Janus sends.
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatalf("set local offer: %v", err)
	}
	return offer.SDP
}

// TestAnswerNotBlockedByUnreachableICEServer is the D-454 regression guard: an
// unreachable STUN server must not delay the answer. With the pre-fix code
// this blocks ~5s (pion's stunGatherTimeout) on GatheringCompletePromise;
// with trickle ICE restored the answer path returns well under that.
func TestAnswerNotBlockedByUnreachableICEServer(t *testing.T) {
	r := newInCallSubscribeTestRecorder()
	// 192.0.2.0/24 is TEST-NET-1 (RFC 5737): a routable-looking but
	// black-holed address, so the STUN binding request never gets a reply and
	// gathering runs the full stunGatherTimeout before giving up.
	r.settings = &nextcloud.SignalingSettings{
		StunServers: []nextcloud.SettingICEServer{
			{URLs: "stun:192.0.2.1:3478"},
		},
	}

	p, err := r.newSubscriberPeer("ivan")
	if err != nil {
		t.Fatalf("newSubscriberPeer: %v", err)
	}
	// pc.Close() can park until pion gives up on the black-holed STUN gather
	// (up to stunGatherTimeout). Close in the background with a bounded wait so
	// teardown is deterministic; the gather goroutine self-terminates at the
	// timeout and is reaped at process exit. We never depend on it here.
	t.Cleanup(func() {
		done := make(chan struct{})
		go func() { _ = p.close(); close(done) }()
		select {
		case <-done:
		case <-time.After(200 * time.Millisecond):
		}
	})

	offerSDP := makeOfferSDP(t)

	start := time.Now()
	// The signaling client in the test recorder is not connected, so the
	// answer "send" returns an error; we only care that the path REACHES the
	// send quickly rather than parking behind ICE gathering.
	err = p.handleMessage(context.Background(), map[string]any{
		"type":    "offer",
		"sid":     "1",
		"payload": map[string]any{"sdp": offerSDP},
	})
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "send answer") {
		t.Fatalf("offer path did not reach the expected answer send: %v", err)
	}

	// stunGatherTimeout is 5s; the answer must not wait on it. 2s leaves a
	// wide margin above the sub-millisecond SDP work while still failing hard
	// on the ~5s gather block the pre-fix code imposed.
	if elapsed > 2*time.Second {
		t.Fatalf("offer->answer took %v; the answer must not block on ICE gathering (D-454). "+
			"An unreachable ICE server stalled the answer, which is what produces the ~24s ICE "+
			"connect and empty captures in production.", elapsed)
	}
}
