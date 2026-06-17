package talk

import (
	"testing"
	"time"
)

func feedInOrder(t *pliGapTracker, ssrc uint32, from, count uint16, at time.Time) time.Time {
	for i := uint16(0); i < count; i++ {
		if t.observe(ssrc, from+i, at) {
			panic("unexpected PLI during in-order feed")
		}
		at = at.Add(20 * time.Millisecond)
	}
	return at
}

func TestPLIGapTrackerInOrderNoPLI(t *testing.T) {
	tr := newPLIGapTracker()
	feedInOrder(tr, 1, 100, 200, time.Unix(0, 0))
}

func TestPLIGapTrackerGapHealedByRetransmissionNoPLI(t *testing.T) {
	tr := newPLIGapTracker()
	now := feedInOrder(tr, 1, 100, 10, time.Unix(0, 0))

	// Packet 110 lost: 111 arrives next.
	if tr.observe(1, 111, now) {
		t.Fatal("PLI requested before grace period")
	}
	// NACK retransmission lands well inside the grace period.
	now = now.Add(80 * time.Millisecond)
	if tr.observe(1, 110, now) {
		t.Fatal("PLI requested for a healed gap")
	}
	// Stream continues; nothing stale remains.
	now = now.Add(pliNACKGracePeriod * 2)
	if tr.observe(1, 112, now) {
		t.Fatal("PLI requested after gap was healed")
	}
}

func TestPLIGapTrackerUnhealedGapTriggersPLIOnce(t *testing.T) {
	tr := newPLIGapTracker()
	now := feedInOrder(tr, 1, 100, 10, time.Unix(0, 0))

	if tr.observe(1, 111, now) { // 110 lost
		t.Fatal("PLI requested before grace period")
	}
	now = now.Add(pliNACKGracePeriod + 50*time.Millisecond)
	if !tr.observe(1, 112, now) {
		t.Fatal("expected PLI after grace period with unhealed gap")
	}
	// Still unhealed, but within the throttle window: no second PLI.
	now = now.Add(100 * time.Millisecond)
	if tr.observe(1, 113, now) {
		t.Fatal("PLI not throttled")
	}
	// Past the throttle window with the gap still open: PLI again.
	now = now.Add(pliMinInterval)
	if !tr.observe(1, 114, now) {
		t.Fatal("expected follow-up PLI after throttle window")
	}
}

func TestPLIGapTrackerSequenceWraparound(t *testing.T) {
	tr := newPLIGapTracker()
	now := feedInOrder(tr, 1, 65530, 5, time.Unix(0, 0)) // ends at 65534

	// 65535 lost; 0 arrives (wrapped). Gap of one.
	if tr.observe(1, 0, now) {
		t.Fatal("PLI requested before grace period across wraparound")
	}
	now = now.Add(pliNACKGracePeriod + 50*time.Millisecond)
	if !tr.observe(1, 1, now) {
		t.Fatal("expected PLI for unhealed gap across wraparound")
	}
}

func TestPLIGapTrackerSSRCChangeResets(t *testing.T) {
	tr := newPLIGapTracker()
	now := feedInOrder(tr, 1, 100, 10, time.Unix(0, 0))

	if tr.observe(1, 120, now) { // gap 110..119 opens
		t.Fatal("PLI requested before grace period")
	}
	// Stream rotates to a new SSRC: the new stream is mid-GOP from our
	// point of view, so a keyframe request is due for it...
	now = now.Add(pliNACKGracePeriod * 2)
	if !tr.observe(2, 5000, now) {
		t.Fatal("expected PLI on SSRC rotation")
	}
	// ...and the old sequence space (with its open gap) must not produce
	// further PLIs. 150 packets × 20 ms spans well past pliMinInterval,
	// so leftover gap entries would fire here if the reset missed them.
	feedInOrder(tr, 2, 5001, 150, now.Add(20*time.Millisecond))
}

func TestPLIGapTrackerRotationPLIDeferredPastThrottle(t *testing.T) {
	tr := newPLIGapTracker()
	now := feedInOrder(tr, 1, 100, 10, time.Unix(0, 0))

	// A normal unhealed gap fires a PLI, arming the throttle.
	if tr.observe(1, 111, now) { // 110 lost
		t.Fatal("PLI requested before grace period")
	}
	now = now.Add(pliNACKGracePeriod + 50*time.Millisecond)
	if !tr.observe(1, 112, now) {
		t.Fatal("expected PLI for unhealed gap")
	}
	// Rotation lands inside the throttle window: deferred, not granted.
	now = now.Add(500 * time.Millisecond)
	if tr.observe(2, 5000, now) {
		t.Fatal("expected rotation PLI to be throttled")
	}
	// In-order packets continue; once the throttle clears, the owed PLI
	// fires instead of being silently dropped.
	fired := false
	for i := uint16(1); i <= 200 && !fired; i++ {
		now = now.Add(20 * time.Millisecond)
		fired = tr.observe(2, 5000+i, now)
	}
	if !fired {
		t.Fatal("rotation PLI dropped: never retried after throttle")
	}
}

func TestPLIGapTrackerDiscontinuityPLIDeferredPastThrottle(t *testing.T) {
	tr := newPLIGapTracker()
	now := feedInOrder(tr, 1, 100, 10, time.Unix(0, 0))

	if tr.observe(1, 111, now) { // 110 lost
		t.Fatal("PLI requested before grace period")
	}
	now = now.Add(pliNACKGracePeriod + 50*time.Millisecond)
	if !tr.observe(1, 112, now) {
		t.Fatal("expected PLI for unhealed gap")
	}
	// Discontinuity inside the throttle window: deferred, not dropped.
	now = now.Add(500 * time.Millisecond)
	jump := uint16(112 + pliMaxTrackedGap + 10)
	if tr.observe(1, jump, now) {
		t.Fatal("expected discontinuity PLI to be throttled")
	}
	fired := false
	seq := jump
	for i := 0; i < 200 && !fired; i++ {
		seq++
		now = now.Add(20 * time.Millisecond)
		fired = tr.observe(1, seq, now)
	}
	if !fired {
		t.Fatal("discontinuity PLI dropped: never retried after throttle")
	}
}

func TestPLIGapTrackerOldPacketStillTriggersDuePLI(t *testing.T) {
	tr := newPLIGapTracker()
	now := feedInOrder(tr, 1, 100, 10, time.Unix(0, 0))

	if tr.observe(1, 111, now) { // 110 lost
		t.Fatal("PLI requested before grace period")
	}
	// An unrelated old duplicate is the first arrival after the grace
	// period: the stale gap must still fire, not wait for a forward packet.
	now = now.Add(pliNACKGracePeriod + 50*time.Millisecond)
	if !tr.observe(1, 50, now) {
		t.Fatal("expected stale-gap PLI despite old/duplicate packet arrival")
	}
}

func TestPLIGapTrackerDiscontinuityRequestsImmediatePLI(t *testing.T) {
	tr := newPLIGapTracker()
	now := feedInOrder(tr, 1, 100, 10, time.Unix(0, 0))

	if !tr.observe(1, 100+10+uint16(pliMaxTrackedGap)+5, now) {
		t.Fatal("expected immediate PLI on discontinuity jump")
	}
	if len(tr.missing) != 0 {
		t.Fatalf("expected missing map cleared on discontinuity, have %d", len(tr.missing))
	}
}

func TestPLIGapTrackerExpiredEntriesStopTriggering(t *testing.T) {
	tr := newPLIGapTracker()
	now := feedInOrder(tr, 1, 100, 10, time.Unix(0, 0))

	if tr.observe(1, 111, now) { // 110 lost, never healed
		t.Fatal("PLI requested before grace period")
	}
	now = now.Add(pliNACKGracePeriod + 50*time.Millisecond)
	if !tr.observe(1, 112, now) {
		t.Fatal("expected PLI for unhealed gap")
	}
	// Long after expiry, the stale entry must be gone: no more PLIs for it.
	now = now.Add(pliMissingExpiry + time.Second)
	if tr.observe(1, 113, now) {
		t.Fatal("expired missing entry still triggering PLIs")
	}
	if len(tr.missing) != 0 {
		t.Fatalf("expected expired entries pruned, have %d", len(tr.missing))
	}
}
