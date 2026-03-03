package timeline

import "testing"

func TestSegmentEstimatorWraparoundMonotonic(t *testing.T) {
	e := NewSegmentEstimator()
	ssrc := uint32(0x12345678)

	recordPackets(t, e, ssrc, 1000, 4294967280, 90000)
	recordPackets(t, e, ssrc, 1001, 2, 90000)

	p0, ok := e.PTS(ssrc, 4294967280)
	if !ok {
		t.Fatal("missing first state")
	}
	p1, ok := e.PTS(ssrc, 2)
	if !ok {
		t.Fatal("missing second state")
	}
	if p1 <= p0 {
		t.Fatalf("timestamp went backwards: %d <= %d", p1, p0)
	}
}

func TestSegmentEstimatorCloseStreamDropsState(t *testing.T) {
	e := NewSegmentEstimator()
	ssrc := uint32(1)

	recordPackets(t, e, ssrc, 100, 1, 90000)
	if _, ok := e.PTS(ssrc, 1); !ok {
		t.Fatal("expected stream state")
	}

	e.CloseStream(ssrc)
	if _, ok := e.PTS(ssrc, 2); ok {
		t.Fatal("expected state removed")
	}
}

func TestSegmentEstimatorMonotonicWithOutOfOrderPackets(t *testing.T) {
	e := NewSegmentEstimator()
	ssrc := uint32(0x87654321)

	e.ObserveRTP(ssrc, 1000, 100, false, false, 90000)
	e.ObserveRTP(ssrc, 1005, 90, false, false, 90000)
	e.ObserveRTP(ssrc, 1010, 200, false, false, 90000)

	p90, ok := e.PTS(ssrc, 90)
	if !ok {
		t.Fatal("expected pts for in-order packet")
	}
	p100, ok := e.PTS(ssrc, 100)
	if !ok {
		t.Fatal("expected pts for first packet")
	}
	p200, ok := e.PTS(ssrc, 200)
	if !ok {
		t.Fatal("expected pts for late packet")
	}
	if p100 <= p90 || p200 <= p100 {
		t.Fatalf("pts not monotonic: p90=%d p100=%d p200=%d", p90, p100, p200)
	}

	p10, ok := e.PTS(ssrc, 10)
	if !ok {
		t.Fatal("missing state for rewind")
	}
	if p10 < p200 {
		t.Fatalf("rewound packet did not remain monotonic: p10=%d p200=%d", p10, p200)
	}
}

func TestSegmentEstimatorCanWrapAndUnwrapLargeCycles(t *testing.T) {
	e := NewSegmentEstimator()
	ssrc := uint32(0x100)

	// Start near wrap boundary and then move forward across it.
	e.ObserveRTP(ssrc, 2000, 4294967290, false, false, 90000)
	e.ObserveRTP(ssrc, 2100, 4294967295, false, false, 90000)
	e.ObserveRTP(ssrc, 2200, 4, false, false, 90000)

	p1, ok := e.PTS(ssrc, 4294967290)
	if !ok {
		t.Fatal("expected base pts")
	}
	p2, ok := e.PTS(ssrc, 4)
	if !ok {
		t.Fatal("expected post-wrap pts")
	}
	if p2 <= p1 {
		t.Fatalf("wrap-around caused backward pts: %d <= %d", p2, p1)
	}
}

func TestSegmentEstimatorUsesAudioClockRate(t *testing.T) {
	e := NewSegmentEstimator()
	ssrc := uint32(0x99)

	e.ObserveRTP(ssrc, 1_000_000_000, 0, false, false, 48000)

	base, ok := e.PTS(ssrc, 0)
	if !ok {
		t.Fatal("expected pts for base packet")
	}
	e.ObserveRTP(ssrc, 2_000_000_000, 48000, false, false, 48000)
	p1, ok := e.PTS(ssrc, 48000)
	if !ok {
		t.Fatal("expected pts for one second-later packet")
	}
	if p1-base != 1_000_000_000 {
		t.Fatalf("expected 1s PTS delta at 48kHz, got %d", p1-base)
	}
}

func TestSegmentEstimatorCanSetClockRateAfterFirstPacket(t *testing.T) {
	e := NewSegmentEstimator()
	ssrc := uint32(0x55)

	e.ObserveRTP(ssrc, 1_000_000_000, 0, false, false, 0)
	e.SetClockRate(ssrc, 48000)
	e.ObserveRTP(ssrc, 2_000_000_000, 48000, false, false, 0)

	base, ok := e.PTS(ssrc, 0)
	if !ok {
		t.Fatal("expected base pts")
	}
	p1, ok := e.PTS(ssrc, 48000)
	if !ok {
		t.Fatal("expected second pts")
	}
	if p1-base != 1_000_000_000 {
		t.Fatalf("expected 1s PTS delta after late clock-rate set, got %d", p1-base)
	}
}

func recordPackets(t *testing.T, e *SegmentEstimator, ssrc uint32, recv uint64, ts uint32, clockRate uint32) {
	t.Helper()
	e.ObserveRTP(ssrc, recv, ts, false, false, clockRate)
	_, ok := e.PTS(ssrc, ts)
	if !ok {
		t.Fatalf("expected PTS state for ssrc=%d ts=%d", ssrc, ts)
	}
}
