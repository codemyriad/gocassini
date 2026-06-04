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
	// Stream rotates to a new SSRC before the grace period elapses: the
	// old sequence space (and its gaps) must not produce a PLI.
	now = now.Add(pliNACKGracePeriod * 2)
	if tr.observe(2, 5000, now) {
		t.Fatal("PLI requested across SSRC change")
	}
	feedInOrder(tr, 2, 5001, 50, now.Add(20*time.Millisecond))
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
