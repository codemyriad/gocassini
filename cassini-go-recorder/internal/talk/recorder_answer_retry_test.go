package talk

// Regression guards for D-509 gap 2: a capturing peer wedged by a failed
// RENEGOTIATION answer.
//
// The shape of the wedge: a participant turns their camera on, the MCU sends a
// fresh offer, the recorder builds the answer (SetLocalDescription succeeds) —
// and the signaling WRITE fails, typically because the client is inside its
// resume window (clearConn has dropped the conn and Send returns
// ErrNotConnected while the SAME session is redialed and resumed). markAnswerSent
// never runs, so the ICE gate stays shut and candidates buffer forever, while
// negotiation 1's audio keeps flowing.
//
// That peer used to be unreachable for the rest of the call: reconcile's
// captured>0 skip runs before every other guard, requestOffer is unreachable
// (hasOffer() is true) and resetIfExhausted short-circuits on offerReceived.
// offerReceived is a ONE-WAY LATCH — the only thing that clears it is a brand
// new peer struct — so on a renegotiation it is already true and nothing
// re-opens it. Audio survived; the added video was permanently lost.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gocassini/internal/signaling"
)

// wedgedRenegotiationPeer drives a real peer to the exact wedge: answer #1
// sent (offerReceived latched true, non-trivially), then offer #2 whose answer
// write fails against a never-connected signaling client.
func wedgedRenegotiationPeer(t *testing.T) (*Recorder, *subscriberPeer) {
	t.Helper()

	// The signaling client is never connected, so every write fails.
	r := newInCallSubscribeTestRecorder()
	p, err := r.newSubscriberPeer("ivan")
	if err != nil {
		t.Fatalf("newSubscriberPeer: %v", err)
	}
	t.Cleanup(func() { go func() { _ = p.close() }() })

	// Negotiation 1 completed: this is what latches offerReceived. Reaching the
	// wedge through markAnswerSent (rather than setting the field) is the point
	// — it is the latch that makes gap 2 unreachable.
	p.markAnswerSent()
	if !p.hasOffer() {
		t.Fatal("setup: negotiation 1 must latch offerReceived")
	}

	// Negotiation 2 (camera on): a new SID, and the answer write fails.
	err = p.handleMessage(context.Background(), map[string]any{
		"type":    "offer",
		"sid":     "negotiation-2",
		"payload": map[string]any{"sdp": makeOfferSDP(t)},
	})
	if err == nil || !strings.Contains(err.Error(), "send answer") {
		t.Fatalf("setup: expected the answer send to fail, got: %v", err)
	}
	return r, p
}

// TestFailedRenegotiationAnswerIsRetriedNotSkipped is the D-509 gap 2 guard,
// and the renegotiation-path counterpart the ticket asks for: the existing
// TestFailedAnswerWriteLeavesRetriesEnabledAndGateClosed covers only the FIRST
// offer, where the invariant it asserts is already true on entry.
//
// Here the peer is capturing audio (captured>0) AND wedged. Before D-509 the
// captured>0 skip made it unreachable forever. It must now be seen and retried.
func TestFailedRenegotiationAnswerIsRetriedNotSkipped(t *testing.T) {
	r, p := wedgedRenegotiationPeer(t)

	p.mu.Lock()
	pending := p.pendingAnswer
	pendingSID := p.pendingAnswerSID
	answerSent := p.answerSent
	p.mu.Unlock()

	if pending == nil {
		t.Fatal("a failed renegotiation answer must be recorded, or reconcile cannot see the wedge at all")
	}
	if pendingSID != "negotiation-2" {
		t.Fatalf("pendingAnswerSID = %q, want negotiation-2 (the retransmit must be stamped with the negotiation it answers)", pendingSID)
	}
	if answerSent {
		t.Fatal("the ICE gate must stay shut while the answer has not landed")
	}

	// The peer is capturing negotiation 1's audio: exactly the state that used
	// to make it invisible to the loop.
	r.sessionsByRemote["ivan"] = &sessionCapture{RemoteSessionID: "ivan", AudioPackets: 5000}

	got := reconcileActionFor(time.Now(), p.reconcileState(), 5000)
	if got != actionRetryAnswer {
		t.Fatalf("reconcileActionFor for a wedged CAPTURING peer = %s, want retryAnswer; the captured>0 skip must not hide it (D-509 gap 2)", got)
	}
}

// TestRetryPendingAnswerSendsSIDStampedAnswerThenFlushesCandidates is the
// recovery, end to end on a live capture server: the retransmit lands as a
// SID-stamped `answer` — never a requestoffer, which against a peer with a live
// Janus subscriber is the D-454 ALREADY_JOINED mechanism #127 removed — and the
// buffered candidates drain AFTER it, never around it.
func TestRetryPendingAnswerSendsSIDStampedAnswerThenFlushesCandidates(t *testing.T) {
	r, messages := newPeerMessageCapture(t)
	p := &subscriberPeer{owner: r, remoteSessionID: "ivan"}

	// Reach the wedge directly: negotiation 2's answer is built and recorded,
	// its gate shut, and a candidate is buffered behind it.
	p.markAnswerSent()
	p.beginNegotiation("negotiation-2")
	p.recordPendingAnswer(map[string]any{"type": "answer", "sdp": "answer-2"}, "negotiation-2")
	p.sendLocalICESignal("candidate", map[string]any{"candidate": map[string]any{"candidate": "candidate:buffered"}})

	if got := signalingProbe(t, r, messages); len(got) != 0 {
		t.Fatalf("messages escaped while the answer was still pending: %v", got)
	}

	p.retryPendingAnswer()

	want := []string{"answer", "candidate"}
	for i, wantType := range want {
		select {
		case data := <-messages:
			if gotType := asString(data["type"]); gotType != wantType {
				t.Fatalf("message %d type = %q, want %q (data=%v)", i, gotType, wantType, data)
			}
			if gotSID := asString(data["sid"]); gotSID != "negotiation-2" {
				t.Fatalf("message %d sid = %q, want negotiation-2 (data=%v)", i, gotSID, data)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for message %d (%s)", i, wantType)
		}
	}

	p.mu.Lock()
	answerSent := p.answerSent
	pending := p.pendingAnswer
	p.mu.Unlock()
	if !answerSent {
		t.Fatal("a landed retransmit must open the ICE gate, or the candidates stay stuck forever")
	}
	if pending != nil {
		t.Fatal("a landed retransmit must clear the pending answer, or it would be sent again")
	}
}

// TestRetryPendingAnswerBailsOnSupersededNegotiation is the race guard, and the
// reason retryPendingAnswer is compare-and-act rather than snapshot-and-send.
//
// reconcile is snapshot -> decide -> act, so a new offer can land between the
// two. If the retransmit sent the OLD sid's answer anyway and then called
// markAnswerSent, it would open the gate and flush the NEW negotiation's
// buffered candidates around an answer that was never sent for it — the exact
// D-454 harm (#127), re-entered through a brand new door. It must bail instead.
func TestRetryPendingAnswerBailsOnSupersededNegotiation(t *testing.T) {
	r, messages := newPeerMessageCapture(t)
	p := &subscriberPeer{owner: r, remoteSessionID: "ivan"}

	// Wedged on negotiation 2.
	p.markAnswerSent()
	p.beginNegotiation("negotiation-2")
	p.recordPendingAnswer(map[string]any{"type": "answer", "sdp": "answer-2"}, "negotiation-2")

	// The offer that lands between reconcile's snapshot and its act.
	p.beginNegotiation("negotiation-3")
	p.sendLocalICESignal("candidate", map[string]any{"candidate": map[string]any{"candidate": "candidate:negotiation-3"}})

	// Reconcile acts on its now-stale snapshot.
	p.retryPendingAnswer()

	if got := signalingProbe(t, r, messages); len(got) != 0 {
		t.Fatalf("a superseded answer was retransmitted: %v; it would strand negotiation-3 and flush its candidates around an answer the remote never got", got)
	}

	p.mu.Lock()
	answerSent := p.answerSent
	p.mu.Unlock()
	if answerSent {
		t.Fatal("retryPendingAnswer opened the ICE gate for negotiation-3, whose answer was never sent (D-454 class regression)")
	}
}

// TestCompleteAnswerRetransmitBailsWhenSupersededMidWrite is the sharp end of
// the same race, and the one a pre-write check alone does NOT cover.
//
// The retransmit write happens with p.mu released — a network write must not
// hold the peer lock — so a new offer can land while it is in flight:
// beginNegotiation drops the pending answer, rotates currentSID to sid-3 and
// shuts the gate. If the write's completion then called markAnswerSent
// unconditionally, it would re-open the gate and flush negotiation-3's buffered
// candidates around an answer that was only ever sent for negotiation-2. The
// remote cannot apply candidates for an answer it never received: that is the
// D-454 failure PR #127 fixed, arriving from a new site.
//
// This drives the completion directly, because that window cannot be entered by
// timing a real socket write deterministically.
func TestCompleteAnswerRetransmitBailsWhenSupersededMidWrite(t *testing.T) {
	r, messages := newPeerMessageCapture(t)
	p := &subscriberPeer{owner: r, remoteSessionID: "ivan"}

	// Wedged on negotiation 2; the retransmit for it is "in flight".
	p.markAnswerSent()
	p.beginNegotiation("negotiation-2")
	p.recordPendingAnswer(map[string]any{"type": "answer", "sdp": "answer-2"}, "negotiation-2")

	// Negotiation 3 supersedes it while that write is still in flight, and
	// buffers a candidate behind its own not-yet-sent answer.
	p.beginNegotiation("negotiation-3")
	p.sendLocalICESignal("candidate", map[string]any{"candidate": map[string]any{"candidate": "candidate:negotiation-3"}})

	// Negotiation 2's write now reports success. It is too late to matter.
	p.completeAnswerRetransmit("negotiation-2", nil)

	p.mu.Lock()
	answerSent := p.answerSent
	p.mu.Unlock()
	if answerSent {
		t.Fatal("a superseded retransmit opened the ICE gate for negotiation-3, whose answer was never sent (D-454 class regression)")
	}
	if got := signalingProbe(t, r, messages); len(got) != 0 {
		t.Fatalf("negotiation-3's candidates escaped around an answer that was never sent for them: %v", got)
	}
}

// TestRecordPendingAnswerIgnoresSupersededNegotiation closes the same race at
// the other end: an offer handler can be superseded while its own write is in
// flight, and must not then install a wedge for a negotiation the MCU has
// already abandoned.
func TestRecordPendingAnswerIgnoresSupersededNegotiation(t *testing.T) {
	r := newInCallSubscribeTestRecorder()
	p := &subscriberPeer{owner: r, remoteSessionID: "ivan"}

	p.beginNegotiation("negotiation-3")
	p.recordPendingAnswer(map[string]any{"type": "answer", "sdp": "answer-2"}, "negotiation-2")

	p.mu.Lock()
	pending := p.pendingAnswer
	p.mu.Unlock()
	if pending != nil {
		t.Fatal("an answer for a superseded negotiation must not be recorded; the retransmit would chase a dead SID")
	}
}

// TestBeginNegotiationClearsPendingAnswer pins beginNegotiation as the single
// point that invalidates superseded per-negotiation state — now the pending
// answer as well as the buffered ICE signals.
func TestBeginNegotiationClearsPendingAnswer(t *testing.T) {
	r := newInCallSubscribeTestRecorder()
	p := &subscriberPeer{owner: r, remoteSessionID: "ivan"}

	p.beginNegotiation("negotiation-2")
	p.recordPendingAnswer(map[string]any{"type": "answer", "sdp": "answer-2"}, "negotiation-2")
	p.answerRetransmits = 3

	p.beginNegotiation("negotiation-3")

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pendingAnswer != nil || p.pendingAnswerSID != "" {
		t.Fatal("a new offer must drop the superseded pending answer")
	}
	if !p.pendingAnswerSince.IsZero() {
		t.Fatal("a new offer must reset the retransmit budget anchor")
	}
	if p.answerRetransmits != 0 {
		t.Fatalf("answerRetransmits = %d, want 0: a new negotiation starts with a fresh budget", p.answerRetransmits)
	}
}

// TestReconcileShouldRetryAnswerBounds pins both budgets and the scope. The
// budgets are not decoration: they are what stop an unbounded retransmit on a
// permanently broken peer, and what stop the mechanism giving up while the
// wedge is curing itself.
func TestReconcileShouldRetryAnswerBounds(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	wedged := func(mut func(*peerReconcileState)) peerReconcileState {
		s := peerReconcileState{
			offerReceived:      true,
			hasPendingAnswer:   true,
			pendingAnswerSince: now.Add(-4 * time.Second),
		}
		if mut != nil {
			mut(&s)
		}
		return s
	}

	cases := []struct {
		name  string
		state peerReconcileState
		want  bool
	}{
		{"a fresh renegotiation wedge is retried", wedged(nil), true},
		{
			// DEFECT 3: the first-offer path keeps D-386's requestOffer
			// recovery; only a renegotiation wedge is structurally unreachable.
			name:  "a first-offer wedge is NOT retried (GUARD 3 still recovers it)",
			state: wedged(func(s *peerReconcileState) { s.offerReceived = false }),
			want:  false,
		},
		{"no pending answer, nothing to retry", wedged(func(s *peerReconcileState) { s.hasPendingAnswer = false }), false},
		{
			name:  "under the write cap",
			state: wedged(func(s *peerReconcileState) { s.answerRetransmits = maxAnswerRetransmits - 1 }),
			want:  true,
		},
		{
			name:  "at the write cap, bounded",
			state: wedged(func(s *peerReconcileState) { s.answerRetransmits = maxAnswerRetransmits }),
			want:  false,
		},
		{
			// DEFECT 2: a resume that SUCCEEDS can take ~15-25s
			// (defaultResumeAttempts=4, 1s backoff, 5s dial + 5s hello). The
			// budget must outlive it or the mechanism defeats itself.
			name:  "still retried at 25s, outliving a slow but SUCCESSFUL resume",
			state: wedged(func(s *peerReconcileState) { s.pendingAnswerSince = now.Add(-25 * time.Second) }),
			want:  true,
		},
		{
			name:  "past the wall-clock budget, bounded",
			state: wedged(func(s *peerReconcileState) { s.pendingAnswerSince = now.Add(-(answerRetransmitBudget + time.Second)) }),
			want:  false,
		},
		{"zero pendingAnswerSince is not retried", wedged(func(s *peerReconcileState) { s.pendingAnswerSince = time.Time{} }), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reconcileShouldRetryAnswer(now, tc.state); got != tc.want {
				t.Fatalf("reconcileShouldRetryAnswer(%+v) = %v, want %v", tc.state, got, tc.want)
			}
		})
	}
}

// TestAnswerRetransmitBudgetOutlivesASuccessfulResume pins the sizing itself
// against the resume schedule it exists to survive. If someone shrinks the
// budget below what a successful resume costs, the retransmit gives up exactly
// when the socket is about to come back — the wedge becomes permanent and
// silent, which is the bug this whole mechanism exists to fix.
func TestAnswerRetransmitBudgetOutlivesASuccessfulResume(t *testing.T) {
	// From internal/signaling: 4 attempts of (1s backoff + 5s dial + 5s hello).
	const worstCaseSuccessfulResume = 4 * (time.Second + 5*time.Second + 5*time.Second)

	if answerRetransmitBudget <= worstCaseSuccessfulResume/2 {
		t.Fatalf("answerRetransmitBudget = %s is under-budgeted: a resume that SUCCEEDS can take ~%s, and giving up first re-wedges the peer permanently", answerRetransmitBudget, worstCaseSuccessfulResume)
	}
}

// TestRetryPendingAnswerDoesNotBurnBudgetWhileDisconnected pins the other half
// of the budget fix: the resume window is the EXPECTED cause of the wedge, not
// evidence the peer is broken. If a "not connected" tick burned a retransmit,
// the 2s reconcile tick would spend the whole 5-write budget inside a ~10s
// resume and the peer would be dead before the socket even returned.
func TestRetryPendingAnswerDoesNotBurnBudgetWhileDisconnected(t *testing.T) {
	// Never connected: every send fails with ErrNotConnected.
	r := newInCallSubscribeTestRecorder()
	p := &subscriberPeer{owner: r, remoteSessionID: "ivan"}

	p.beginNegotiation("negotiation-2")
	p.recordPendingAnswer(map[string]any{"type": "answer", "sdp": "answer-2"}, "negotiation-2")

	for i := 0; i < maxAnswerRetransmits*3; i++ {
		p.retryPendingAnswer()
	}

	p.mu.Lock()
	retransmits := p.answerRetransmits
	pending := p.pendingAnswer
	p.mu.Unlock()

	if retransmits != 0 {
		t.Fatalf("answerRetransmits = %d after %d disconnected attempts, want 0: a resume window must not burn the budget", retransmits, maxAnswerRetransmits*3)
	}
	if pending == nil {
		t.Fatal("the pending answer must survive a disconnected retry, or the peer is re-wedged the moment the socket returns")
	}
}

// TestSendReportsErrNotConnected pins the sentinel the budget classification
// depends on. It was string-matched before; an errors.Is contract is what makes
// "the socket is momentarily away" distinguishable from a real send failure.
func TestSendReportsErrNotConnected(t *testing.T) {
	c := signaling.NewClient("ws://127.0.0.1:1/spreed", true)
	err := c.Send(map[string]any{"type": "message"})
	if err == nil {
		t.Fatal("expected a send on an unconnected client to fail")
	}
	if !errors.Is(err, signaling.ErrNotConnected) {
		t.Fatalf("Send on an unconnected client = %v, want signaling.ErrNotConnected", err)
	}
}
