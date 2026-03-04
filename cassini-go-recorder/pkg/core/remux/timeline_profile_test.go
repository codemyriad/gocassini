package remux

import (
	"path/filepath"
	"testing"

	"gocassini/pkg/core/store"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

func TestAnalyzeTimelineWithSRProducesStartAdjustment(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "stream.rtplog")

	const (
		ssrc      = uint32(77)
		clockRate = uint32(90000)
	)
	if err := writeDriftedTimelineLog(logPath, ssrc, clockRate, 90090, true); err != nil {
		t.Fatalf("write timeline log: %v", err)
	}

	profile, err := analyzeTimeline(logPath, ssrc, clockRate)
	if err != nil {
		t.Fatalf("analyze timeline: %v", err)
	}
	if profile.Samples < minTimelineSamplesForAdjustment {
		t.Fatalf("insufficient samples: %d", profile.Samples)
	}
	if profile.RawDurationNS <= profile.CorrectedDurationNS {
		t.Fatalf("expected raw duration > corrected duration, got raw=%d corrected=%d", profile.RawDurationNS, profile.CorrectedDurationNS)
	}
	adjust := recommendedTimelineStartAdjustmentNS(profile)
	if adjust >= 0 {
		t.Fatalf("expected negative start adjustment, got %d", adjust)
	}
	if adjust < -maxTimelineStartAdjustmentNS {
		t.Fatalf("adjustment should be clamped, got %d", adjust)
	}
}

func TestAnalyzeTimelineWithoutSRKeepsAdjustmentAtZero(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "stream-no-sr.rtplog")

	const (
		ssrc      = uint32(78)
		clockRate = uint32(90000)
	)
	if err := writeDriftedTimelineLog(logPath, ssrc, clockRate, 90090, false); err != nil {
		t.Fatalf("write timeline log: %v", err)
	}

	profile, err := analyzeTimeline(logPath, ssrc, clockRate)
	if err != nil {
		t.Fatalf("analyze timeline: %v", err)
	}
	adjust := recommendedTimelineStartAdjustmentNS(profile)
	if adjust != 0 {
		t.Fatalf("expected zero adjustment without SR correction, got %d", adjust)
	}
}

func TestRecommendedTimelineStartAdjustmentClamp(t *testing.T) {
	adjust := recommendedTimelineStartAdjustmentNS(timelineProfile{
		Samples:             minTimelineSamplesForAdjustment + 10,
		RawDurationNS:       10_000_000_000,
		CorrectedDurationNS: 100_000_000,
	})
	if adjust != -maxTimelineStartAdjustmentNS {
		t.Fatalf("expected clamp to -%d, got %d", maxTimelineStartAdjustmentNS, adjust)
	}
}

func writeDriftedTimelineLog(
	path string,
	ssrc uint32,
	clockRate uint32,
	driftedTicksPerSecond uint32,
	includeSR bool,
) error {
	writer, err := store.NewWriter(path, store.StreamHeader{
		StreamID:    "s_000001",
		Codec:       "video/vp8",
		ClockRate:   clockRate,
		Direction:   "recvonly",
		StartMonoNS: 1_000_000_000,
		PT:          96,
	})
	if err != nil {
		return err
	}
	defer func() {
		_ = writer.Close()
	}()

	seq := uint16(1)
	for sec := 0; sec <= 60; sec++ {
		recvNS := uint64(1_000_000_000 + sec*1_000_000_000)
		ts := uint32(sec) * driftedTicksPerSecond
		packet := rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: seq,
				Timestamp:      ts,
				SSRC:           ssrc,
				Marker:         true,
			},
			Payload: []byte{0x10, 0x00, 0x00, 0x00, 0x9d, 0x01, 0x2a},
		}
		seq++
		raw, err := packet.Marshal()
		if err != nil {
			return err
		}
		if err := writer.Write(store.Record{
			RecvMonoNS: recvNS,
			Kind:       store.KindRTP,
			WireBytes:  raw,
		}); err != nil {
			return err
		}
		if includeSR && sec%5 == 0 {
			sr := &rtcp.SenderReport{SSRC: ssrc, RTPTime: ts}
			srRaw, err := sr.Marshal()
			if err != nil {
				return err
			}
			if err := writer.Write(store.Record{
				RecvMonoNS: recvNS,
				Kind:       store.KindRTCP,
				WireBytes:  srRaw,
			}); err != nil {
				return err
			}
		}
	}

	return writer.Close()
}
