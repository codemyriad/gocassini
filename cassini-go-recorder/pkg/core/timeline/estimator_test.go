package timeline

import (
	"math"
	"testing"

	"github.com/pion/rtcp"
)

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

func TestSegmentEstimatorSRCorrectionReducesDrift(t *testing.T) {
	ssrc := uint32(0x42)
	const seconds = 60
	const driftedTicksPerSecond = 90090

	withSR := NewSegmentEstimator()
	withoutSR := NewSegmentEstimator()

	withSR.ObserveRTP(ssrc, 0, 0, false, false, 90000)
	withoutSR.ObserveRTP(ssrc, 0, 0, false, false, 90000)

	var withSRPTS uint64
	var withoutSRPTS uint64
	for sec := 1; sec <= seconds; sec++ {
		recv := uint64(sec) * 1_000_000_000
		rtpTS := uint32(sec * driftedTicksPerSecond)

		withSR.ObserveRTP(ssrc, recv, rtpTS, false, false, 90000)
		withoutSR.ObserveRTP(ssrc, recv, rtpTS, false, false, 90000)
		if sec%5 == 0 {
			raw := mustSenderReport(t, ssrc, rtpTS)
			withSR.ObserveRTCP(ssrc, recv, raw)
		}

		var ok bool
		withSRPTS, ok = withSR.PTS(ssrc, rtpTS)
		if !ok {
			t.Fatalf("missing withSR pts for sec=%d", sec)
		}
		withoutSRPTS, ok = withoutSR.PTS(ssrc, rtpTS)
		if !ok {
			t.Fatalf("missing withoutSR pts for sec=%d", sec)
		}
	}

	expectedRecv := float64(seconds) * 1_000_000_000
	withDiff := math.Abs(float64(withSRPTS) - expectedRecv)
	withoutDiff := math.Abs(float64(withoutSRPTS) - expectedRecv)
	if withDiff >= withoutDiff {
		t.Fatalf("expected SR correction to reduce drift: with=%.0f without=%.0f", withDiff, withoutDiff)
	}
	if withDiff > 20_000_000 {
		t.Fatalf("expected corrected drift <=20ms, got %.0fns", withDiff)
	}
}

func TestSegmentEstimatorSRNeverMovesPTSBackwards(t *testing.T) {
	e := NewSegmentEstimator()
	ssrc := uint32(0x77)
	e.ObserveRTP(ssrc, 1_000_000_000, 0, false, false, 90000)

	last := uint64(0)
	for sec := 1; sec <= 10; sec++ {
		recv := uint64(1_000_000_000 + sec*1_000_000_000)
		rtpTS := uint32(sec * 90000)
		e.ObserveRTP(ssrc, recv, rtpTS, false, false, 90000)
		// Inject noisy sender reports that would normally pull timestamps around.
		noisyRTP := rtpTS
		if sec%2 == 0 {
			noisyRTP = rtpTS - 1000
		}
		e.ObserveRTCP(ssrc, recv, mustSenderReport(t, ssrc, noisyRTP))

		pts, ok := e.PTS(ssrc, rtpTS)
		if !ok {
			t.Fatalf("missing pts at sec=%d", sec)
		}
		if sec > 1 && pts <= last {
			t.Fatalf("pts went backwards at sec=%d: pts=%d last=%d", sec, pts, last)
		}
		last = pts
	}
}

func TestSegmentEstimatorIgnoreInvalidRTCP(t *testing.T) {
	e := NewSegmentEstimator()
	ssrc := uint32(0x99)
	e.ObserveRTP(ssrc, 1_000, 10, false, false, 90000)
	e.ObserveRTCP(ssrc, 2_000, []byte{0x01, 0x02, 0x03})
	pts, ok := e.PTS(ssrc, 20)
	if !ok {
		t.Fatal("expected pts after invalid rtcp")
	}
	if pts == 0 {
		t.Fatal("expected non-zero pts")
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

func mustSenderReport(t *testing.T, ssrc, rtpTS uint32) []byte {
	t.Helper()
	sr := &rtcp.SenderReport{
		SSRC:    ssrc,
		RTPTime: rtpTS,
	}
	raw, err := sr.Marshal()
	if err != nil {
		t.Fatalf("marshal sender report: %v", err)
	}
	return raw
}
