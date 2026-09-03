package remux

import (
	"gocassini/pkg/core/session"
	"strings"
	"testing"
)

func entryValue(t *testing.T, entries []string, key string) (string, bool) {
	t.Helper()
	for _, e := range entries {
		if k, v, ok := strings.Cut(e, "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

// The wall-clock anchor has to reach the MKV, because the transcription side
// reads it with the ffprobe call it already makes rather than reopening the
// session artifact.
func TestStreamMetadataEntriesEmitsWallClockAnchor(t *testing.T) {
	entries := streamMetadataEntries(StreamPlan{
		ClockRate:         48000,
		FirstRTPTimestamp: 12345,
		FirstRTPSet:       true,
		FirstPacketWallMS: 1756800000000,
		FirstTimelineNS:   1500000000,
	})

	for key, want := range map[string]string{
		"clock_rate":           "48000",
		"first_rtp_timestamp":  "12345",
		"first_packet_wall_ms": "1756800000000",
		"first_timeline_ns":    "1500000000",
	} {
		got, ok := entryValue(t, entries, key)
		if !ok {
			t.Fatalf("%s missing from %v", key, entries)
		}
		if got != want {
			t.Fatalf("%s = %s, want %s", key, got, want)
		}
	}
}

// Zero is a legal RTP timestamp, so "no packet was seen" and "the first packet
// carried timestamp 0" must stay distinguishable.
func TestStreamMetadataEntriesDistinguishesUnsetRTPBaseFromZero(t *testing.T) {
	unset := streamMetadataEntries(StreamPlan{ClockRate: 48000})
	if _, ok := entryValue(t, unset, "first_rtp_timestamp"); ok {
		t.Fatalf("no packet observed, so no first_rtp_timestamp tag: %v", unset)
	}

	zero := streamMetadataEntries(StreamPlan{ClockRate: 48000, FirstRTPSet: true})
	got, ok := entryValue(t, zero, "first_rtp_timestamp")
	if !ok || got != "0" {
		t.Fatalf("a first packet with timestamp 0 must be emitted, got %v", zero)
	}
}

// An absent wall anchor must leave both tags off rather than emitting the
// epoch, which downstream would read as a real instant in 1970.
func TestStreamMetadataEntriesOmitsAnchorWhenUnknown(t *testing.T) {
	entries := streamMetadataEntries(StreamPlan{ClockRate: 48000, FirstTimelineNS: 1500000000})
	if _, ok := entryValue(t, entries, "first_packet_wall_ms"); ok {
		t.Fatalf("unknown anchor must not be emitted: %v", entries)
	}
	if _, ok := entryValue(t, entries, "first_timeline_ns"); ok {
		t.Fatalf("timeline position is meaningless without its wall anchor: %v", entries)
	}
}

// first_timeline_ns is a position on the output timeline: the earliest stream
// sits at 0 and a later one at its distance from it, not at the receive clock.
func TestBuildStreamPlansWritesTimelinePositionNotReceiveClock(t *testing.T) {
	const base = uint64(1_788_464_999_367_062_628) // a real receive clock reading
	segments := []segmentArtifact{
		{Stream: session.PacketStream{StreamID: "s_1", LTID: "p:a:audio:janus"}, FirstNS: base, FirstTimelineNS: int64(base)},
		{Stream: session.PacketStream{StreamID: "s_2", LTID: "p:a:audio:janus"}, FirstNS: base + 28_869_900_000, FirstTimelineNS: int64(base + 28_869_900_000)},
	}
	inputs := []StreamInput{
		{StreamID: "s_1", LTID: "p:a:audio:janus", Kind: "audio", FirstRecvNS: base, FirstTimelineNS: int64(base)},
		{StreamID: "s_2", LTID: "p:a:audio:janus", Kind: "audio", FirstRecvNS: base + 28_869_900_000, FirstTimelineNS: int64(base + 28_869_900_000)},
	}
	plans := buildStreamPlans(session.Session{}, segments, PlanMerge(inputs))
	if len(plans) != 2 {
		t.Fatalf("plans = %d, want 2", len(plans))
	}
	if plans[0].FirstTimelineNS != 0 || plans[1].FirstTimelineNS != 28_869_900_000 {
		t.Fatalf("timeline positions = %d, %d; want 0 and 28869900000", plans[0].FirstTimelineNS, plans[1].FirstTimelineNS)
	}
	if plans[1].FirstRecvNS != base+28_869_900_000 {
		t.Fatalf("receive clock must stay absolute, got %d", plans[1].FirstRecvNS)
	}
}
