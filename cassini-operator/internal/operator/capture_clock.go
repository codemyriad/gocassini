package operator

import (
	"fmt"
	"log"
	"math"
)

// Capture-time observations, not upload latency. Positive skew means client ahead.
type captureClockSample struct {
	ClientSendWallMS    int64   `json:"clientSendWallMs"`
	ClientReceiveWallMS int64   `json:"clientReceiveWallMs"`
	ServerReceiveWallMS int64   `json:"serverReceiveWallMs"`
	ServerSendWallMS    int64   `json:"serverSendWallMs"`
	ElapsedMS           float64 `json:"elapsedMs"`
}

const (
	// At 250 ms, a probe can include nearly 500 ms of network/proxy round trip.
	// This is an admission policy, not a claim of sub-word alignment accuracy.
	captureClockMaxUncertaintyMS = 250.0
	captureClockCoverageMS       = 90000.0
	// Identity probes precede capture startup; stop probes follow teardown. The
	// browser bounds each request to five seconds. Older sessions are irrelevant.
	captureClockProbeMarginMS = 5000
	// Compare variation among probes within 50 ms network RTT of the fastest.
	captureClockFastBoundSlackMS = 25.0
	captureClockMaxVariationMS   = 150.0
)

type captureClockFit struct {
	OffsetMS      int64
	UncertaintyMS float64 // path-asymmetry bound at the selected observation
	VariationMS   float64 // offset spread among the fastest observations
}

// The midpoint estimate assumes symmetric paths; half the network round trip
// bounds the asymmetry error. Two milliseconds allow timestamp quantization.
// Low scatter does not establish accuracy: a stable asymmetric path is biased.
func captureClockEstimate(samples []captureClockSample, start, end int64) (captureClockFit, error) {
	fit := captureClockFit{}
	if len(samples) == 0 || len(samples) > 128 {
		return fit, fmt.Errorf("missing or excessive samples")
	}
	bestError, bestOffset := math.Inf(1), 0.0
	startDistance, endDistance := math.Inf(1), math.Inf(1)
	offsets, bounds := []float64{}, []float64{}
	for _, s := range samples {
		for _, stamp := range []int64{s.ClientSendWallMS, s.ClientReceiveWallMS, s.ServerReceiveWallMS, s.ServerSendWallMS} {
			if stamp <= 0 || stamp > 1<<53-1 {
				return fit, fmt.Errorf("invalid timestamp")
			}
		}
		// Do not let an inherited identity sample select the correction or reject a
		// new session for movement that happened before it started. An exchange
		// spanning the boundary still belongs to the session and is checked below.
		if math.Max(float64(s.ClientSendWallMS), float64(s.ClientReceiveWallMS)) < float64(start)-captureClockProbeMarginMS ||
			math.Min(float64(s.ClientSendWallMS), float64(s.ClientReceiveWallMS)) > float64(end)+captureClockProbeMarginMS {
			continue
		}
		elapsed := s.ElapsedMS
		processing := float64(s.ServerSendWallMS - s.ServerReceiveWallMS)
		wallElapsed := float64(s.ClientReceiveWallMS - s.ClientSendWallMS)
		if math.IsNaN(elapsed) || math.IsInf(elapsed, 0) || elapsed < 0 || elapsed > 10000 || processing < 0 || processing > elapsed+2 || math.Abs(wallElapsed-elapsed) > 5 {
			return fit, fmt.Errorf("clock stepped during probe")
		}
		uncertainty := math.Max(0, elapsed-processing)/2 + 2
		offset := (float64(s.ClientSendWallMS-s.ServerReceiveWallMS) + float64(s.ClientReceiveWallMS-s.ServerSendWallMS)) / 2
		offsets, bounds = append(offsets, offset), append(bounds, uncertainty)
		if uncertainty <= captureClockMaxUncertaintyMS {
			startDistance = math.Min(startDistance, math.Abs(float64(s.ClientReceiveWallMS)-float64(start)))
			endDistance = math.Min(endDistance, math.Abs(float64(s.ClientReceiveWallMS)-float64(end)))
		}
		if uncertainty < bestError {
			bestError, bestOffset = uncertainty, offset
		}
	}
	if len(offsets) == 0 {
		return fit, fmt.Errorf("no clock observations for this session")
	}
	fit.OffsetMS = int64(math.Round(bestOffset))
	// Round upward to microsecond precision, including offset rounding error.
	fit.UncertaintyMS = math.Ceil((bestError+math.Abs(float64(fit.OffsetMS)-bestOffset))*1000) / 1000
	if bestError > captureClockMaxUncertaintyMS {
		return fit, fmt.Errorf("round trip uncertainty exceeds %.0f ms", captureClockMaxUncertaintyMS)
	}
	if startDistance > captureClockCoverageMS || endDistance > captureClockCoverageMS {
		return fit, fmt.Errorf("clock observations do not cover capture")
	}
	low, high := math.Inf(1), math.Inf(-1)
	consistent := true
	for i, offset := range offsets {
		if bounds[i] <= bestError+captureClockFastBoundSlackMS {
			low, high = math.Min(low, offset), math.Max(high, offset)
		}
		// Even a slow probe can establish a change outside its asymmetry bound.
		if math.Abs(offset-bestOffset) > bounds[i]+bestError+50 {
			consistent = false
		}
	}
	fit.VariationMS = high - low
	if !consistent {
		return fit, fmt.Errorf("clock offset changed during capture")
	}
	if fit.VariationMS > captureClockMaxVariationMS {
		return fit, fmt.Errorf("fast-probe offset spread exceeds %.0f ms", captureClockMaxVariationMS)
	}
	return fit, nil
}

// Normalize once at intake, after computing the immutable request digest.
// The samples stay raw; every wall timestamp consumed by placement is shifted
// together, preserving RTP rate fits, mute spans and decoded-duration checks.
func correctCaptureClock(s *captureSidecar, logger *log.Logger) {
	s.ClockStatus, s.ClockCorrectionMS, s.ClockUncertaintyMS, s.ClockVariationMS = "", 0, 0, 0
	if len(s.ClockSamples) == 0 {
		if logger != nil {
			logger.Printf("capture clock: owner=%q recording=%q session=%q status=unmeasured", s.OwnerUserID, s.RecordingID, s.SessionID)
		}
		return
	}
	fit, err := captureClockEstimate(s.ClockSamples, s.CallStartWallMS, s.CallEndWallMS)
	offset := fit.OffsetMS
	if err == nil && (s.CallStartWallMS-offset <= 0 || s.CallEndWallMS-offset <= 0) {
		err = fmt.Errorf("invalid corrected window")
	}
	if err != nil {
		s.ClockStatus = "unreliable"
		if logger != nil {
			logger.Printf("capture clock: owner=%q recording=%q session=%q status=unreliable client_ahead_ms=%d uncertainty_ms=%.3f variation_ms=%.3f action=retain_recorded_audio reason=%q", s.OwnerUserID, s.RecordingID, s.SessionID, offset, fit.UncertaintyMS, fit.VariationMS, err)
		}
		return
	}
	s.ClockStatus, s.ClockCorrectionMS, s.ClockUncertaintyMS, s.ClockVariationMS = "corrected", offset, fit.UncertaintyMS, fit.VariationMS
	s.CallStartWallMS -= offset
	s.CallEndWallMS -= offset
	for i := range s.Segments {
		segment := &s.Segments[i]
		segment.StartWallMS -= offset
		segment.StopWallMS -= offset
		for j := range segment.Anchors {
			segment.Anchors[j].WallMS -= offset
		}
		for j := range segment.MuteIntervals {
			segment.MuteIntervals[j][0] -= offset
			segment.MuteIntervals[j][1] -= offset
		}
	}
	if logger != nil {
		logger.Printf("capture clock: owner=%q recording=%q session=%q status=corrected client_ahead_ms=%d uncertainty_ms=%.3f variation_ms=%.3f samples=%d", s.OwnerUserID, s.RecordingID, s.SessionID, offset, fit.UncertaintyMS, fit.VariationMS, len(s.ClockSamples))
	}
}
