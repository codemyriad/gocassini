package talk

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"gocassini/internal/signaling"

	"github.com/gorilla/websocket"
)

func newPeerMessageCapture(t *testing.T) (*Recorder, <-chan map[string]any) {
	t.Helper()

	messages := make(chan map[string]any, 64)
	upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var wrapper map[string]any
			if err := conn.ReadJSON(&wrapper); err != nil {
				return
			}
			data := asMap(asMap(wrapper["message"])["data"])
			if len(data) != 0 {
				messages <- data
			}
		}
	}))
	t.Cleanup(server.Close)

	r := newInCallSubscribeTestRecorder()
	r.signaling = signaling.NewClient("ws"+strings.TrimPrefix(server.URL, "http"), false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	if err := r.signaling.Connect(ctx); err != nil {
		t.Fatalf("connect signaling client: %v", err)
	}
	t.Cleanup(func() { _ = r.signaling.Close() })
	return r, messages
}

// TestLocalICESignalsWaitForAnswer deterministically covers the ordering gate:
// callbacks that arrive before the answer write completes stay buffered, then
// drain in candidate/EOC order once markAnswerSent opens the gate.
func TestLocalICESignalsWaitForAnswer(t *testing.T) {
	r, messages := newPeerMessageCapture(t)
	peer := &subscriberPeer{
		owner:           r,
		remoteSessionID: "ivan",
		currentSID:      "negotiation-1",
	}

	peer.sendLocalICESignal("candidate", map[string]any{"candidate": map[string]any{"candidate": "candidate:first"}})
	peer.sendLocalICESignal("candidate", map[string]any{"candidate": map[string]any{"candidate": "candidate:second"}})
	peer.sendEndOfCandidates("test-gathering-complete")

	select {
	case got := <-messages:
		t.Fatalf("ICE message escaped before answer: %v", got)
	default:
	}

	if err := r.sendPeerMessage("ivan", "answer", map[string]any{"type": "answer", "sdp": "test"}, "negotiation-1"); err != nil {
		t.Fatalf("send answer: %v", err)
	}
	peer.markAnswerSent()

	want := []string{"answer", "candidate", "candidate", "endOfCandidates"}
	for i, wantType := range want {
		select {
		case data := <-messages:
			if gotType := asString(data["type"]); gotType != wantType {
				t.Fatalf("message %d type = %q, want %q (data=%v)", i, gotType, wantType, data)
			}
			if gotSID := asString(data["sid"]); gotSID != "negotiation-1" {
				t.Fatalf("message %d sid = %q, want negotiation-1 (data=%v)", i, gotSID, data)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for message %d (%s)", i, wantType)
		}
	}
}

// TestAnswerPrecedesTrickledICEMessages exercises the real Pion callback and
// signaling WebSocket. SetLocalDescription starts ICE gathering asynchronously,
// so OnICECandidate can run concurrently with the offer handler. The remote
// peer cannot apply candidates until it has installed our SDP answer; therefore
// the answer must always be the first message for its negotiation SID.
func TestAnswerPrecedesTrickledICEMessages(t *testing.T) {
	r, messages := newPeerMessageCapture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	// Repeat the native callback path to exercise creation/teardown and ensure
	// every negotiation gets an independent answer-first ordering gate.
	const iterations = 16
	for i := 0; i < iterations; i++ {
		peer, err := r.newSubscriberPeer("ivan")
		if err != nil {
			t.Fatalf("iteration %d: new subscriber peer: %v", i, err)
		}

		sid := "negotiation-" + strconv.Itoa(i)
		err = peer.handleMessage(ctx, map[string]any{
			"type":    "offer",
			"sid":     sid,
			"payload": map[string]any{"sdp": makeOfferSDP(t)},
		})
		if err != nil {
			_ = peer.close()
			t.Fatalf("iteration %d: handle offer: %v", i, err)
		}

		var gotTypes []string
		for len(gotTypes) == 0 || gotTypes[len(gotTypes)-1] != "endOfCandidates" {
			select {
			case data := <-messages:
				if gotSID := asString(data["sid"]); gotSID != sid {
					_ = peer.close()
					t.Fatalf("iteration %d: message sid = %q, want %q (data=%v)", i, gotSID, sid, data)
				}
				gotTypes = append(gotTypes, asString(data["type"]))
			case <-ctx.Done():
				_ = peer.close()
				t.Fatalf("iteration %d: timed out waiting for signaling sequence; got %v", i, gotTypes)
			}
		}
		_ = peer.close()

		if gotTypes[0] != "answer" {
			t.Fatalf("iteration %d: first signaling message = %q, want answer; sequence=%v", i, gotTypes[0], gotTypes)
		}
		if gotTypes[len(gotTypes)-1] != "endOfCandidates" {
			t.Fatalf("iteration %d: final signaling message = %q, want endOfCandidates; sequence=%v", i, gotTypes[len(gotTypes)-1], gotTypes)
		}
		for j, msgType := range gotTypes[:len(gotTypes)-1] {
			if msgType == "endOfCandidates" {
				t.Fatalf("iteration %d: endOfCandidates preceded later ICE messages at index %d; sequence=%v", i, j, gotTypes)
			}
		}
	}
}
