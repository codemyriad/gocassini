package talk

// Regression guards for D-509 gap 1 (the sub-captureRebuildGrace dead zone):
// a peer whose handshake completed but whose media never flowed used to match
// no guard at all until it aged past the 45s createdAt grace — so a ~30s call
// finalized with "no remuxable streams" while the reconcile loop watched it.
//
// reconcileActionFor is a pure function of a peer snapshot precisely so this
// whole decision surface can be pinned here without an MCU, real ICE, or a
// single wall-clock sleep.

import (
	"testing"
	"time"
)

// TestReconcileActionForCoversEveryPeerState is the decision table: one row per
// reachable peer state, including the two dead ends D-509 closes and the states
// that must keep behaving exactly as they did before it.
func TestReconcileActionForCoversEveryPeerState(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	aged := now.Add(-(captureRebuildGrace + time.Second))
	young := now.Add(-(captureRebuildGrace / 2))

	cases := []struct {
		name     string
		state    peerReconcileState
		captured int
		want     reconcileAction
	}{
		{
			name:  "fresh peer with no offer is requestoffer'd",
			state: peerReconcileState{createdAt: now.Add(-time.Second)},
			want:  actionRequestOffer,
		},
		{
			name:  "never-offered peer past the 45s floor is rebuilt",
			state: peerReconcileState{createdAt: aged},
			want:  actionRebuild,
		},
		{
			name:  "never-offered peer inside the floor keeps asking",
			state: peerReconcileState{createdAt: young},
			want:  actionRequestOffer,
		},
		{
			// D-509 gap 1: the dead zone. Before this change every guard
			// missed and the peer sat idle until 45s after CREATION.
			name: "answered, no media, past firstMediaGrace, ICE down -> rebuild",
			state: peerReconcileState{
				createdAt:     young,
				offerReceived: true,
				answeredAt:    now.Add(-(firstMediaGrace + time.Second)),
			},
			want: actionRebuild,
		},
		{
			name: "answered, no media, inside firstMediaGrace -> wait",
			state: peerReconcileState{
				createdAt:     young,
				offerReceived: true,
				answeredAt:    now.Add(-(firstMediaGrace - time.Second)),
			},
			want: actionNone,
		},
		{
			// The DEFECT-4 guard. The answer is sent before ICE gathering
			// finishes and stunGatherTimeout is 5s PER server, so a healthy
			// peer can legitimately first-media well past 12s. If its
			// transport is up, tearing it down would be strictly worse than
			// waiting; the 45s floor still owns it.
			name: "answered, no media, past the grace, but ICE connected -> left alone",
			state: peerReconcileState{
				createdAt:     young,
				offerReceived: true,
				answeredAt:    now.Add(-(firstMediaGrace + 10*time.Second)),
				iceConnected:  true,
			},
			want: actionNone,
		},
		{
			name: "answered, no media, at the rebuild cap -> parked",
			state: peerReconcileState{
				createdAt:     young,
				offerReceived: true,
				answeredAt:    now.Add(-(firstMediaGrace + time.Second)),
				rebuildCount:  maxCaptureRebuilds,
			},
			want: actionNone,
		},
		{
			// A hand-built peer that never went through markAnswerSent has a
			// zero answeredAt. It must fall through to the 45s floor, not be
			// rebuilt on a zero-time anchor.
			name: "answered with a zero answeredAt falls back to the floor",
			state: peerReconcileState{
				createdAt:     young,
				offerReceived: true,
			},
			want: actionNone,
		},
		{
			name: "zero answeredAt past the floor is still rebuilt by the floor",
			state: peerReconcileState{
				createdAt:     aged,
				offerReceived: true,
			},
			want: actionRebuild,
		},
		{
			name:     "healthy capturing peer is never touched",
			state:    peerReconcileState{createdAt: aged, offerReceived: true},
			captured: 5000,
			want:     actionNone,
		},
		{
			name: "capturing peer past firstMediaGrace is still never touched",
			state: peerReconcileState{
				createdAt:     aged,
				offerReceived: true,
				answeredAt:    now.Add(-(firstMediaGrace + time.Minute)),
			},
			captured: 1,
			want:     actionNone,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reconcileActionFor(now, tc.state, tc.captured); got != tc.want {
				t.Fatalf("reconcileActionFor(%+v, captured=%d) = %s, want %s", tc.state, tc.captured, got, tc.want)
			}
		})
	}
}

// TestReconcileRebuildsAnsweredPeerWithNoMediaBeforeTheFloor is gap 1 end to
// end through the real loop: a peer answered 13s ago with nothing captured is
// rebuilt now, not in 45s. It is deliberately YOUNG (well inside
// captureRebuildGrace) so only the new answeredAt anchor can explain the
// rebuild.
func TestReconcileRebuildsAnsweredPeerWithNoMediaBeforeTheFloor(t *testing.T) {
	r := newInCallSubscribeTestRecorder()
	old, err := r.newSubscriberPeer("ivan")
	if err != nil {
		t.Fatalf("newSubscriberPeer: %v", err)
	}
	now := time.Now()
	old.createdAt = now.Add(-20 * time.Second) // far inside the 45s floor
	old.offerReceived = true
	old.answeredAt = now.Add(-(firstMediaGrace + time.Second))
	r.subscribers["ivan"] = old
	r.sessionsByRemote["ivan"] = &sessionCapture{RemoteSessionID: "ivan"} // 0 packets
	t.Cleanup(func() {
		if p := r.subscribers["ivan"]; p != nil {
			_ = p.close()
		}
	})

	r.reconcileSubscribers(now)

	got := r.subscribers["ivan"]
	if got == nil || got == old {
		t.Fatalf("an answered peer with no media past firstMediaGrace must be rebuilt inside the 45s floor (D-509 gap 1); got=%p old=%p", got, old)
	}
	if got.rebuildCount != 1 {
		t.Fatalf("rebuilt peer rebuildCount = %d, want 1", got.rebuildCount)
	}
}

// TestReconcileLeavesRecentlyAnsweredPeerAlone is the anti-test: the same peer
// inside the grace must not be rebuilt, or a healthy handshake gets torn down
// while its media is still on the way.
func TestReconcileLeavesRecentlyAnsweredPeerAlone(t *testing.T) {
	r := newInCallSubscribeTestRecorder()
	now := time.Now()
	old := &subscriberPeer{
		owner:           r,
		remoteSessionID: "ivan",
		createdAt:       now.Add(-20 * time.Second),
		offerReceived:   true,
		answeredAt:      now.Add(-(firstMediaGrace - 2*time.Second)),
	}
	r.subscribers["ivan"] = old
	r.sessionsByRemote["ivan"] = &sessionCapture{RemoteSessionID: "ivan"}

	r.reconcileSubscribers(now)

	if r.subscribers["ivan"] != old {
		t.Fatal("a peer answered less than firstMediaGrace ago must not be rebuilt")
	}
}

// TestMarkAnswerSentStampsAnsweredAt pins the anchor itself: without this
// write, gap 1's whole branch is unreachable (a zero answeredAt disables it).
func TestMarkAnswerSentStampsAnsweredAt(t *testing.T) {
	r := newInCallSubscribeTestRecorder()
	p := &subscriberPeer{owner: r, remoteSessionID: "ivan", currentSID: "negotiation-1"}

	before := time.Now()
	p.markAnswerSent()

	p.mu.Lock()
	answeredAt := p.answeredAt
	p.mu.Unlock()

	if answeredAt.IsZero() {
		t.Fatal("markAnswerSent must stamp answeredAt; a zero anchor disables the firstMediaGrace rebuild entirely")
	}
	if answeredAt.Before(before) {
		t.Fatalf("answeredAt = %v, want >= %v", answeredAt, before)
	}
}

// TestBeginNegotiationKeepsAnsweredAt pins the deliberate non-clear: answeredAt
// means "last successful answer at", which stays true across a renegotiation. A
// peer that has been answered since T and still has no media is still exactly
// that while a new offer is in flight.
func TestBeginNegotiationKeepsAnsweredAt(t *testing.T) {
	r := newInCallSubscribeTestRecorder()
	p := &subscriberPeer{owner: r, remoteSessionID: "ivan", currentSID: "negotiation-1"}
	p.markAnswerSent()

	p.mu.Lock()
	first := p.answeredAt
	p.mu.Unlock()

	p.beginNegotiation("negotiation-2")

	p.mu.Lock()
	after := p.answeredAt
	p.mu.Unlock()

	if !after.Equal(first) {
		t.Fatalf("beginNegotiation must not clear answeredAt: was %v, now %v", first, after)
	}
}

// TestReconcileStateNilGuardsPeerConnection pins that the snapshot tolerates a
// peer with no pc. Several reconcile tests build bare &subscriberPeer{} values;
// sampling ICE state must not panic on them.
func TestReconcileStateNilGuardsPeerConnection(t *testing.T) {
	r := newInCallSubscribeTestRecorder()
	p := &subscriberPeer{owner: r, remoteSessionID: "ivan"}

	state := p.reconcileState()

	if state.iceConnected {
		t.Fatal("a peer with no peer connection must not report ICE connected")
	}
}
