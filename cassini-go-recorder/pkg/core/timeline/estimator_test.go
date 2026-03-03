package timeline

import "testing"

func TestSegmentEstimatorWraparoundMonotonic(t *testing.T) {
	e := NewSegmentEstimator()
	ssrc := uint32(0x12345678)

	recordPackets(t, e, ssrc, 1000, 4294967280)
	recordPackets(t, e, ssrc, 1001, 2)

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

	recordPackets(t, e, ssrc, 100, 1)
	if _, ok := e.PTS(ssrc, 1); !ok {
		t.Fatal("expected stream state")
	}

	e.CloseStream(ssrc)
	if _, ok := e.PTS(ssrc, 2); ok {
		t.Fatal("expected state removed")
	}
}

func recordPackets(t *testing.T, e *SegmentEstimator, ssrc uint32, recv uint64, ts uint32) {
	t.Helper()
	e.ObserveRTP(ssrc, uint64(recv), ts, false, false)
	_, ok := e.PTS(ssrc, ts)
	if !ok {
		t.Fatalf("expected PTS state for ssrc=%d ts=%d", ssrc, ts)
	}
}
