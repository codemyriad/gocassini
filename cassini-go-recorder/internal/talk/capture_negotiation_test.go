package talk

import (
	"strings"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"sync/atomic"
	"testing"
	"time"
)

func receiver(t *testing.T, video bool) *webrtc.PeerConnection {
	t.Helper()
	p, e := webrtc.NewPeerConnection(webrtc.Configuration{})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { p.Close() })
	return p
}
func track(t *testing.T, p *webrtc.PeerConnection, video bool) *webrtc.TrackLocalStaticRTP {
	t.Helper()
	kind, mime, rate := "audio", webrtc.MimeTypeOpus, uint32(48000)
	if video {
		kind, mime, rate = "video", webrtc.MimeTypeVP8, 90000
	}
	tr, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: mime, ClockRate: rate}, kind, kind)
	if err != nil {
		t.Fatal(err)
	}
	sender, err := p.AddTrack(tr)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		buf := make([]byte, 1500)
		for {
			if _, _, err := sender.Read(buf); err != nil {
				return
			}
		}
	}()
	return tr
}
func negotiate(t *testing.T, p, r *webrtc.PeerConnection, audioOnly bool) string {
	t.Helper()
	offer, err := p.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	pg := webrtc.GatheringCompletePromise(p)
	if err = p.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-pg
	if err = r.SetRemoteDescription(*p.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	if err := applyCapturePolicy(r, !audioOnly); err != nil {
		t.Fatal(err)
	}

	answer, err := r.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	rg := webrtc.GatheringCompletePromise(r)
	if err = r.SetLocalDescription(answer); err != nil {
		t.Fatal(err)
	}
	<-rg
	if err = p.SetRemoteDescription(*r.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	return r.LocalDescription().SDP
}
func TestAudioOnlyMixedOfferAndRenegotiation(t *testing.T) {
	for _, optin := range []bool{false, true} {
		t.Run(map[bool]string{false: "audio-only", true: "video-opt-in"}[optin], func(t *testing.T) {
			p := receiver(t, true)
			r := receiver(t, optin)
			a := track(t, p, false)
			v := track(t, p, true)
			var ac, vc atomic.Int32
			r.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
				for {
					_, _, err := tr.ReadRTP()
					if err != nil {
						return
					}
					if tr.Kind() == webrtc.RTPCodecTypeAudio {
						ac.Add(1)
					} else {
						vc.Add(1)
					}
				}
			})
			for round := 0; round < 2; round++ {

				sdp := negotiate(t, p, r, !optin)
				rejected := strings.Contains(sdp[strings.Index(sdp, "m=video"):], "a=inactive")
				t.Logf("round=%d video_rejected=%t", round, rejected)
				if rejected == optin {
					t.Fatal("wrong video negotiation")
				}
				before := ac.Load()
				deadline := time.Now().Add(3 * time.Second)
				for time.Now().Before(deadline) {
					n := uint16(ac.Load() + vc.Load() + 1)
					a.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: n, Timestamp: uint32(n) * 960}, Payload: []byte{0xf8, 0xff, 0xfe}})
					v.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: n, Timestamp: uint32(n) * 3000, Marker: true}, Payload: []byte{0x10, 0, 0, 0, 0x9d, 1, 0x2a}})
					time.Sleep(20 * time.Millisecond)
					if ac.Load() > before+10 && (!optin || vc.Load() > 0) {
						break
					}
				}
				if ac.Load() <= before {
					t.Fatal("audio did not flow")
				}
				if !optin && vc.Load() != 0 {
					t.Fatal("video received")
				}
			}
			t.Logf("audio packets=%d video packets=%d", ac.Load(), vc.Load())
		})
	}
}
func TestVideoOnlyOfferThenAudioAdded(t *testing.T) {
	p := receiver(t, true)
	r := receiver(t, false)
	track(t, p, true)
	s := negotiate(t, p, r, true)
	if !strings.Contains(s, "a=inactive") {
		t.Fatal("video not inactive")
	}
	track(t, p, false)
	s = negotiate(t, p, r, true)
	if !strings.Contains(s, "m=audio ") || !strings.Contains(s, "a=recvonly") {
		t.Fatal("audio not accepted")
	}
	t.Log("video-only inactive offer answered; later audio accepted")
}
func TestAudioFirstThenCameraEnabled(t *testing.T) {
	p := receiver(t, true)
	r := receiver(t, false)
	track(t, p, false)
	negotiate(t, p, r, true)
	track(t, p, true)
	s := negotiate(t, p, r, true)
	if !strings.Contains(s[strings.Index(s, "m=video"):], "a=inactive") {
		t.Fatal("late camera not disabled")
	}
	t.Log("audio-first negotiation accepts audio; added camera remains inactive")
}
