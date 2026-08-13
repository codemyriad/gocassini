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

// signalingProbe makes absence checks event-based: it sends a marker through
// the same signaling client and returns every message that was written before
// it. The client serializes writes on one WebSocket and the capture server
// reads in order, so the probe arriving proves no earlier write is still in
// flight — no wall-clock quiet window needed.
func signalingProbe(t *testing.T, r *Recorder, messages <-chan map[string]any) []map[string]any {
	t.Helper()
	if err := r.sendPeerMessage("signaling-probe", "probe", nil, ""); err != nil {
		t.Fatalf("send probe: %v", err)
	}
	var before []map[string]any
	for {
		select {
		case data := <-messages:
			if asString(data["type"]) == "probe" {
				return before
			}
			before = append(before, data)
		case <-time.After(5 * time.Second):
			t.Fatalf("probe never delivered; messages before it: %v", before)
		}
	}
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

	if got := signalingProbe(t, r, messages); len(got) != 0 {
		t.Fatalf("ICE messages escaped before answer: %v", got)
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
//
// This is a smoke test: the synchronous answer write usually wins the race
// against real ICE gathering anyway, and there is no seam to delay it. The
// deterministic ordering coverage lives in TestLocalICESignalsWaitForAnswer
// and TestOfferResetsLocalICEGate.
func TestAnswerPrecedesTrickledICEMessages(t *testing.T) {
	r, messages := newPeerMessageCapture(t)

	// Repeat the native callback path to exercise creation/teardown and ensure
	// every negotiation gets an independent answer-first ordering gate. Each
	// iteration gets its own deadline: ~32 PeerConnection lifecycles under one
	// shared budget timed out spuriously on loaded runners.
	const iterations = 16
	for i := 0; i < iterations; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
		cancel()

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

// TestOfferResetsLocalICEGate pins the per-negotiation reset that protects a
// Janus SID rotation (D-454): a new offer must close the answer gate again and
// drop signals buffered for the superseded negotiation, so nothing stale can
// ride out around the new answer.
func TestOfferResetsLocalICEGate(t *testing.T) {
	r, messages := newPeerMessageCapture(t)
	peer := &subscriberPeer{
		owner:           r,
		remoteSessionID: "ivan",
		currentSID:      "negotiation-1",
	}

	// Negotiation 1 completed: the answer is out and the gate is open.
	peer.markAnswerSent()

	// The MCU rotates the subscriber: a fresh offer supersedes negotiation 1.
	// A callback firing before the new answer write must be buffered again,
	// not flushed through the still-open gate.
	peer.beginNegotiation("negotiation-2")
	peer.sendLocalICESignal("candidate", map[string]any{"candidate": map[string]any{"candidate": "candidate:pre-answer"}})
	expectNoSignalingMessage(t, r, messages)

	// A second rotation before negotiation 2's answer: its buffered candidate
	// is now stale and must be dropped, not replayed into negotiation 3.
	peer.beginNegotiation("negotiation-3")

	if err := r.sendPeerMessage("ivan", "answer", map[string]any{"type": "answer", "sdp": "test"}, "negotiation-3"); err != nil {
		t.Fatalf("send answer: %v", err)
	}
	peer.markAnswerSent()

	got := signalingProbe(t, r, messages)
	if len(got) != 1 {
		t.Fatalf("messages after negotiation-3 answer = %v, want only the answer (stale buffered candidate must not replay)", got)
	}
	if typ := asString(got[0]["type"]); typ != "answer" {
		t.Fatalf("first message = %q, want answer (data=%v)", typ, got[0])
	}
	if sid := asString(got[0]["sid"]); sid != "negotiation-3" {
		t.Fatalf("answer sid = %q, want negotiation-3 (data=%v)", sid, got[0])
	}
}

// TestFailedAnswerWriteLeavesRetriesEnabledAndGateClosed pins markAnswerSent's
// contract for the failure path: when the answer write fails, offer retries
// must stay enabled (offerReceived false, D-386) and the local ICE gate must
// stay closed with candidates buffered, so nothing is signaled ahead of the
// answer a later retry will send.
func TestFailedAnswerWriteLeavesRetriesEnabledAndGateClosed(t *testing.T) {
	// The signaling client is never connected, so the answer write fails.
	r := newInCallSubscribeTestRecorder()
	p, err := r.newSubscriberPeer("ivan")
	if err != nil {
		t.Fatalf("newSubscriberPeer: %v", err)
	}
	t.Cleanup(func() { go func() { _ = p.close() }() })

	err = p.handleMessage(context.Background(), map[string]any{
		"type":    "offer",
		"sid":     "1",
		"payload": map[string]any{"sdp": makeOfferSDP(t)},
	})
	if err == nil || !strings.Contains(err.Error(), "send answer") {
		t.Fatalf("expected the answer send to fail, got: %v", err)
	}

	if p.hasOffer() {
		t.Fatal("offerReceived set despite failed answer write; offer retries would be suppressed (D-386)")
	}

	p.sendLocalICESignal("candidate", map[string]any{"candidate": map[string]any{"candidate": "candidate:buffered"}})
	p.mu.Lock()
	answerSent := p.answerSent
	pending := len(p.pendingLocalICESignals)
	p.mu.Unlock()
	if answerSent {
		t.Fatal("answer gate open despite failed answer write")
	}
	if pending == 0 {
		t.Fatal("candidate not buffered behind the failed answer; it could overtake the retried answer")
	}
}
