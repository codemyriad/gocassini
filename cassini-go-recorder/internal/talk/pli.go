package talk

import (
	"time"
)

// PLI (Picture Loss Indication) policy for recorded video tracks.
//
// pion's default interceptors already NACK lost packets, and senders
// retransmit them — so the common case heals by itself and the recording
// keeps every packet (see PR #48 for the remux side of that story). What
// NACK cannot heal is loss past the sender's retransmission buffer (long
// bursts, sender restarts). Senders in this deployment emit keyframes
// roughly once per 100 s, so a single unhealed gap used to tear the
// recorded picture for up to that long (Linear D-305).
//
// pliGapTracker watches one video track's RTP sequence numbers as packets
// arrive (in arrival order, retransmissions included). When a gap survives
// longer than pliNACKGracePeriod — i.e. retransmission evidently isn't
// coming — it asks the caller to send a PLI, forcing the sender to emit a
// fresh keyframe and bound the damage to roughly the grace period plus an
// RTT, instead of the ~100 s keyframe cadence. Discontinuities (forward
// jumps too wide to enumerate) and SSRC rotations also warrant a keyframe
// request: the new sequence space is mid-GOP from our point of view. If
// the throttle suppresses one of those requests, the debt is recorded in
// pliDue and retried on subsequent packets until granted — otherwise the
// request would be lost with no gap entries left to re-arm it.
const (
	// pliNACKGracePeriod is how long a sequence gap may stay unfilled
	// before we conclude NACK recovery failed. Observed retransmissions
	// land within tens of milliseconds; 400 ms is comfortably past any
	// sane RTT while still ~250× tighter than the ~100 s keyframe cadence.
	pliNACKGracePeriod = 400 * time.Millisecond

	// pliMinInterval throttles keyframe requests per track. A PLI storm
	// would make the sender spend its whole bitrate on keyframes.
	pliMinInterval = 2 * time.Second

	// pliMaxTrackedGap bounds the missing-sequence bookkeeping. A forward
	// jump wider than this is not packet loss we can enumerate — it is a
	// discontinuity (sender restart, long stall) — so we reset and request
	// a keyframe outright.
	pliMaxTrackedGap = 64

	// pliMissingExpiry drops missing entries that are clearly never going
	// to be filled, so the map cannot grow without bound on lossy links.
	pliMissingExpiry = 5 * time.Second
)

type pliGapTracker struct {
	initialized bool
	ssrc        uint32
	highest     uint16
	missing     map[uint16]time.Time
	lastPLI     time.Time
	// pliDue, when non-zero, records a keyframe request that the throttle
	// suppressed (discontinuity or SSRC rotation). Unlike enumerated gaps,
	// those paths clear the missing map, so nothing else would retry.
	pliDue time.Time
}

func newPLIGapTracker() *pliGapTracker {
	return &pliGapTracker{missing: map[uint16]time.Time{}}
}

// observe ingests one received packet (in arrival order) and reports
// whether a PLI should be sent now. now is the packet arrival time.
func (t *pliGapTracker) observe(ssrc uint32, seq uint16, now time.Time) bool {
	if !t.initialized {
		// First packet; onRemoteTrack already requested a keyframe at
		// capture start, so just take the baseline.
		t.initialized = true
		t.ssrc = ssrc
		t.highest = seq
		return false
	}
	if ssrc != t.ssrc {
		// The simulcast/rejoin machinery rotated the stream — new sequence
		// space, old gaps are meaningless. The new stream is mid-GOP from
		// our point of view just like at capture start, so ask for a
		// keyframe (deferred past the throttle if necessary).
		t.ssrc = ssrc
		t.highest = seq
		clear(t.missing)
		return t.deferOrRequestPLI(now)
	}

	switch diff := int16(seq - t.highest); {
	case diff == 1:
		t.highest = seq
	case diff > 1:
		if int(diff) > pliMaxTrackedGap {
			// Discontinuity, not enumerable loss: resync and ask for a
			// keyframe (deferred past the throttle if necessary).
			t.highest = seq
			clear(t.missing)
			return t.deferOrRequestPLI(now)
		}
		for s := t.highest + 1; s != seq; s++ {
			t.missing[s] = now
		}
		t.highest = seq
	default:
		// Reordered packet or NACK retransmission: the gap entry, if we
		// were tracking one, is now healed. Fall through to the sweep —
		// another gap may have gone stale, or a deferred request may be
		// owed, and neither should wait for the next in-order packet.
		delete(t.missing, seq)
	}

	stale := false
	for s, seen := range t.missing {
		age := now.Sub(seen)
		if age > pliMissingExpiry {
			delete(t.missing, s)
			continue
		}
		if age > pliNACKGracePeriod {
			stale = true
		}
	}
	if !stale && t.pliDue.IsZero() {
		return false
	}
	return t.requestPLI(now)
}

// deferOrRequestPLI requests a PLI now if the throttle allows; otherwise it
// records the debt so subsequent packets retry until the request is granted.
func (t *pliGapTracker) deferOrRequestPLI(now time.Time) bool {
	if t.requestPLI(now) {
		return true
	}
	t.pliDue = now
	return false
}

func (t *pliGapTracker) requestPLI(now time.Time) bool {
	if !t.lastPLI.IsZero() && now.Sub(t.lastPLI) < pliMinInterval {
		return false
	}
	t.lastPLI = now
	t.pliDue = time.Time{}
	return true
}
