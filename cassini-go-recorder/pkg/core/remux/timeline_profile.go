package remux

import (
	"errors"
	"fmt"
	"io"
	"math"

	"gocassini/pkg/core/store"
	"gocassini/pkg/core/timeline"

	"github.com/pion/rtp"
)

const (
	minTimelineSamplesForAdjustment = 30
	minTimelineDriftForAdjustmentNS = int64(5_000_000)   // 5ms
	maxTimelineStartAdjustmentNS    = int64(250_000_000) // 250ms
)

type timelineProfile struct {
	Samples             int
	FirstRecvNS         uint64
	LastRecvNS          uint64
	FirstCorrectedPTSNS uint64
	LastCorrectedPTSNS  uint64
	RawDurationNS       int64
	CorrectedDurationNS int64
}

func analyzeTimeline(logPath string, primarySSRC uint32, clockRate uint32) (timelineProfile, error) {
	reader, err := store.OpenReader(logPath)
	if err != nil {
		return timelineProfile{}, fmt.Errorf("open rtplog: %w", err)
	}
	defer func() {
		_ = reader.Close()
	}()

	estimator := timeline.NewSegmentEstimator()
	effectiveClockRate := clockRate
	if effectiveClockRate == 0 {
		effectiveClockRate = 90000
	}

	profile := timelineProfile{}
	firstRawSet := false
	firstRaw := int64(0)
	lastRaw := int64(0)
	lastRTPSsrc := primarySSRC

	for {
		rec, err := reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return timelineProfile{}, fmt.Errorf("read rtplog: %w", err)
		}

		switch rec.Kind {
		case store.KindRTP:
			var packet rtp.Packet
			if err := packet.Unmarshal(rec.WireBytes); err != nil {
				return timelineProfile{}, fmt.Errorf("unmarshal rtp: %w", err)
			}
			lastRTPSsrc = packet.SSRC
			estimator.ObserveRTP(packet.SSRC, rec.RecvMonoNS, packet.Timestamp, packet.Marker, false, effectiveClockRate)
			ptsNS, ok := estimator.PTS(packet.SSRC, packet.Timestamp)
			if !ok {
				continue
			}

			raw := int64(packet.Timestamp)
			if firstRawSet {
				raw = unwrapRTP32(lastRaw, packet.Timestamp)
			} else {
				firstRaw = raw
				firstRawSet = true
			}
			lastRaw = raw

			if profile.Samples == 0 {
				profile.FirstRecvNS = rec.RecvMonoNS
				profile.FirstCorrectedPTSNS = ptsNS
			}
			profile.LastRecvNS = rec.RecvMonoNS
			profile.LastCorrectedPTSNS = ptsNS
			profile.Samples++
		case store.KindRTCP:
			if lastRTPSsrc != 0 {
				estimator.ObserveRTCP(lastRTPSsrc, rec.RecvMonoNS, rec.WireBytes)
			}
		}
	}

	if profile.Samples >= 2 && firstRawSet && lastRaw > firstRaw {
		profile.RawDurationNS = int64(float64(lastRaw-firstRaw) * 1e9 / float64(effectiveClockRate))
		if profile.LastCorrectedPTSNS > profile.FirstCorrectedPTSNS {
			profile.CorrectedDurationNS = int64(profile.LastCorrectedPTSNS - profile.FirstCorrectedPTSNS)
		}
	}

	return profile, nil
}

func recommendedTimelineStartAdjustmentNS(profile timelineProfile) int64 {
	if profile.Samples < minTimelineSamplesForAdjustment {
		return 0
	}
	if profile.RawDurationNS <= 0 || profile.CorrectedDurationNS <= 0 {
		return 0
	}
	driftNS := profile.RawDurationNS - profile.CorrectedDurationNS
	if absInt64(driftNS) < minTimelineDriftForAdjustmentNS {
		return 0
	}
	adjust := -driftNS / 2
	return clampInt64(adjust, -maxTimelineStartAdjustmentNS, maxTimelineStartAdjustmentNS)
}

func unwrapRTP32(last int64, raw uint32) int64 {
	raw64 := int64(raw)
	delta := raw64 - (last & 0xffffffff)
	for delta > (1 << 31) {
		delta -= 1 << 32
	}
	for delta < -(1 << 31) {
		delta += 1 << 32
	}
	return last + delta
}

func clampInt64(value, min, max int64) int64 {
	return int64(math.Max(float64(min), math.Min(float64(max), float64(value))))
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}
