package talk

// Regression guards for D-386 (recovery half): an in-call participant the
// recorder captured nothing from must not be silently abandoned. Two
// mechanisms back each other up:
//   - requestOffer never permanently parks: after the fast burst it keeps
//     retrying at the slow steady interval (requestOfferRetryWindow).
//   - reconcileSubscribers rebuilds a peer that has captured no media after a
//     grace period, covering an offer that was answered but whose media never
//     flowed.
// Plus: offerReceived is only set once the answer is actually sent, so a failed
// answer path no longer suppresses retries forever.

import (
	"context"
	"testing"
	"time"
)

func TestRequestOfferRetryWindow(t *testing.T) {
	cases := []struct {
		name        string
		attempts    int
		maxAttempts int
		want        time.Duration
	}{
		{"first attempt is fast", 0, 8, requestOfferResponseTimeout},
		{"mid burst is fast", 7, 8, requestOfferResponseTimeout},
		{"at the cap goes slow", 8, 8, slowRequestOfferInterval},
		{"past the cap stays slow", 50, 8, slowRequestOfferInterval},
		{"no cap is always fast", 100, 0, requestOfferResponseTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := requestOfferRetryWindow(tc.attempts, tc.maxAttempts); got != tc.want {
				t.Fatalf("requestOfferRetryWindow(%d, %d) = %v, want %v", tc.attempts, tc.maxAttempts, got, tc.want)
			}
		})
	}
}

func TestRequestOfferSlowRetriesAfterFastBudgetInsteadOfParking(t *testing.T) {
	r := newInCallSubscribeTestRecorder()
	r.cfg.MaxRequestOfferAttempts = 2
	// Signaling is not connected, so sendRequestOffer returns an error; we
	// assert on the peer's throttle state, not the send result.
	p := &subscriberPeer{owner: r, remoteSessionID: "ivan"}

	// Fast burst: a fresh peer sends immediately.
	_ = p.requestOffer()
	if p.requestOfferAttempts != 1 {
		t.Fatalf("first requestOffer did not send: attempts=%d", p.requestOfferAttempts)
	}
	// Within the fast window it is throttled.
	p.awaitingOfferSince = time.Now().Add(-1 * time.Second)
	_ = p.requestOffer()
	if p.requestOfferAttempts != 1 {
		t.Fatalf("send within fast window: attempts=%d, want 1", p.requestOfferAttempts)
	}
	// After the fast window the second burst attempt goes out.
	p.awaitingOfferSince = time.Now().Add(-(requestOfferResponseTimeout + time.Second))
	_ = p.requestOffer()
	if p.requestOfferAttempts != 2 {
		t.Fatalf("send after fast window: attempts=%d, want 2", p.requestOfferAttempts)
	}

	// Budget now spent (attempts == maxAttempts). The old code parked here
	// forever. The fast window no longer applies; the slow one does.
	p.awaitingOfferSince = time.Now().Add(-(requestOfferResponseTimeout + time.Second))
	_ = p.requestOffer()
	if p.requestOfferAttempts != 2 {
		t.Fatalf("slow window should still throttle a fast-window-aged wait: attempts=%d, want 2", p.requestOfferAttempts)
	}
	// Past the slow window, it keeps retrying (never parked).
	p.awaitingOfferSince = time.Now().Add(-(slowRequestOfferInterval + time.Second))
	_ = p.requestOffer()
	if p.requestOfferAttempts != 3 {
		t.Fatalf("expected a slow retry past the budget (never park): attempts=%d, want 3", p.requestOfferAttempts)
	}
	if !p.offerExhaustedLogged {
		t.Fatalf("expected the slow-mode transition to be logged once")
	}
}

func TestReconcileShouldRebuild(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	old := now.Add(-(captureRebuildGrace + time.Second))
	young := now.Add(-(captureRebuildGrace / 2))
	cases := []struct {
		name      string
		createdAt time.Time
		count     int
		want      bool
	}{
		{"older than grace, under cap", old, 0, true},
		{"older than grace, near cap", old, maxCaptureRebuilds - 1, true},
		{"at the cap", old, maxCaptureRebuilds, false},
		{"within grace", young, 0, false},
		{"zero createdAt", time.Time{}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reconcileShouldRebuild(now, tc.createdAt, tc.count); got != tc.want {
				t.Fatalf("reconcileShouldRebuild(createdAt=%v, count=%d) = %v, want %v", tc.createdAt, tc.count, got, tc.want)
			}
		})
	}
}

func TestReconcileRebuildsStuckPeerWithNoMedia(t *testing.T) {
	r := newInCallSubscribeTestRecorder()
	old, err := r.newSubscriberPeer("ivan")
	if err != nil {
		t.Fatalf("newSubscriberPeer: %v", err)
	}
	old.createdAt = time.Now().Add(-(captureRebuildGrace + 5*time.Second))
	r.subscribers["ivan"] = old
	r.sessionsByRemote["ivan"] = &sessionCapture{RemoteSessionID: "ivan"} // 0 packets
	t.Cleanup(func() {
		if p := r.subscribers["ivan"]; p != nil {
			_ = p.close()
		}
	})

	r.reconcileSubscribers(time.Now())

	got := r.subscribers["ivan"]
	if got == nil || got == old {
		t.Fatalf("expected the stuck peer to be rebuilt with a fresh peer, got=%p old=%p", got, old)
	}
	if got.rebuildCount != 1 {
		t.Fatalf("rebuilt peer rebuildCount = %d, want 1", got.rebuildCount)
	}
}

// TestReconcileLeavesHealthyCapturingPeerAlone pins the invariant that protects
// a working recording: a peer capturing media is not torn down, however old it
// is. Note "healthy" — the peer has no answer outstanding. D-509 narrowed this
// invariant, deliberately and visibly: a peer that is capturing AND wedged by a
// failed renegotiation answer is now retried rather than skipped forever (see
// TestFailedRenegotiationAnswerIsRetriedNotSkipped in
// recorder_answer_retry_test.go). The narrowing keys on the pending answer, not
// on the packet count, so this assertion is unchanged.
func TestReconcileLeavesHealthyCapturingPeerAlone(t *testing.T) {
	r := newInCallSubscribeTestRecorder()
	old := &subscriberPeer{owner: r, remoteSessionID: "speaker", createdAt: time.Now().Add(-(captureRebuildGrace + 5*time.Second))}
	r.subscribers["speaker"] = old
	r.sessionsByRemote["speaker"] = &sessionCapture{RemoteSessionID: "speaker", AudioPackets: 5000}

	r.reconcileSubscribers(time.Now())

	if r.subscribers["speaker"] != old {
		t.Fatalf("a capturing peer must not be rebuilt")
	}
}

func TestReconcileDoesNotRebuildWithinGrace(t *testing.T) {
	r := newInCallSubscribeTestRecorder()
	old := &subscriberPeer{owner: r, remoteSessionID: "fresh", createdAt: time.Now().Add(-(captureRebuildGrace / 2))}
	r.subscribers["fresh"] = old
	r.sessionsByRemote["fresh"] = &sessionCapture{RemoteSessionID: "fresh"} // 0 packets

	r.reconcileSubscribers(time.Now())

	if r.subscribers["fresh"] != old {
		t.Fatalf("a peer younger than the grace must not be rebuilt")
	}
}

func TestReconcileDoesNotRebuildAtCap(t *testing.T) {
	r := newInCallSubscribeTestRecorder()
	old := &subscriberPeer{
		owner:           r,
		remoteSessionID: "exhausted",
		createdAt:       time.Now().Add(-(captureRebuildGrace + 5*time.Second)),
		rebuildCount:    maxCaptureRebuilds,
		offerReceived:   true, // answered but silent: requestOffer is also skipped
	}
	r.subscribers["exhausted"] = old
	r.sessionsByRemote["exhausted"] = &sessionCapture{RemoteSessionID: "exhausted"} // 0 packets

	r.reconcileSubscribers(time.Now())

	if r.subscribers["exhausted"] != old {
		t.Fatalf("a peer at the rebuild cap must not be rebuilt again")
	}
}

func TestReconcileNoOpWhenStopping(t *testing.T) {
	r := newInCallSubscribeTestRecorder()
	old := &subscriberPeer{owner: r, remoteSessionID: "ivan", createdAt: time.Now().Add(-(captureRebuildGrace + 5*time.Second))}
	r.subscribers["ivan"] = old
	r.sessionsByRemote["ivan"] = &sessionCapture{RemoteSessionID: "ivan"}
	r.stopping = true

	r.reconcileSubscribers(time.Now())

	if r.subscribers["ivan"] != old {
		t.Fatalf("reconcile must not rebuild while stopping")
	}
}

func TestOfferReceivedOnlySetAfterSuccessfulAnswer(t *testing.T) {
	r := newInCallSubscribeTestRecorder()
	p, err := r.newSubscriberPeer("ivan")
	if err != nil {
		t.Fatalf("newSubscriberPeer: %v", err)
	}
	t.Cleanup(func() { _ = p.close() })

	// An offer whose SDP cannot be set fails before the answer is sent. The
	// handshake did not complete, so retries must stay enabled.
	err = p.handleMessage(context.Background(), map[string]any{
		"type":    "offer",
		"sid":     "1",
		"payload": map[string]any{"sdp": "not-a-valid-sdp"},
	})
	if err == nil {
		t.Fatalf("expected the malformed offer to fail")
	}
	if p.hasOffer() {
		t.Fatalf("offerReceived must stay false when the answer path failed, or retries are suppressed forever")
	}
}
