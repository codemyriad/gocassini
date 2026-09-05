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

// The midpoint estimate assumes symmetric paths; half the network round trip
// bounds the asymmetry error. Two milliseconds allow timestamp quantization.
func captureClockEstimate(samples []captureClockSample, start, end int64) (int64, float64, error) {
	if len(samples) == 0 || len(samples) > 128 {
		return 0, 0, fmt.Errorf("missing or excessive samples")
	}
	bestError, bestOffset := math.Inf(1), 0.0
	first, last := math.Inf(1), math.Inf(-1)
	offsets, bounds := []float64{}, []float64{}
	for _, s := range samples {
		// Bound timestamps before subtracting, including malformed imported data.
		for _, stamp := range []int64{s.ClientSendWallMS, s.ClientReceiveWallMS, s.ServerReceiveWallMS, s.ServerSendWallMS} {
			if stamp <= 0 || stamp > 1<<53-1 {
				return 0, 0, fmt.Errorf("invalid timestamp")
			}
		}
		elapsed := s.ElapsedMS
		processing := float64(s.ServerSendWallMS - s.ServerReceiveWallMS)
		wallElapsed := float64(s.ClientReceiveWallMS - s.ClientSendWallMS)
		if math.IsNaN(elapsed) || math.IsInf(elapsed, 0) || elapsed < 0 || elapsed > 10000 || processing < 0 || processing > elapsed+2 || math.Abs(wallElapsed-elapsed) > 5 {
			return 0, 0, fmt.Errorf("clock stepped during probe")
		}
		uncertainty := math.Max(0, elapsed-processing)/2 + 2
		offset := (float64(s.ClientSendWallMS-s.ServerReceiveWallMS) + float64(s.ClientReceiveWallMS-s.ServerSendWallMS)) / 2
		offsets, bounds = append(offsets, offset), append(bounds, uncertainty)
		if uncertainty <= 100 {
			first = math.Min(first, float64(s.ClientReceiveWallMS))
			last = math.Max(last, float64(s.ClientReceiveWallMS))
		}
		if uncertainty < bestError {
			bestError, bestOffset = uncertainty, offset
		}
	}
	if bestError > 100 {
		return 0, bestError, fmt.Errorf("round trip uncertainty exceeds 100 ms")
	}
	// Never extrapolate an initial probe across an unobserved long call.
	if math.Abs(first-float64(start)) > 90000 || math.Abs(last-float64(end)) > 90000 {
		return 0, bestError, fmt.Errorf("clock observations do not cover capture")
	}
	observedError := bestError
	for i, offset := range offsets {
		if bounds[i] <= 100 {
			observedError = math.Max(observedError, math.Abs(offset-bestOffset)+bounds[i])
		}
		if math.Abs(offset-bestOffset) > bounds[i]+bestError+50 {
			return 0, bestError, fmt.Errorf("clock offset changed during capture")
		}
	}
	if observedError > 150 {
		return 0, observedError, fmt.Errorf("offset variation uncertainty exceeds 150 ms")
	}
	return int64(math.Round(bestOffset)), observedError, nil
}

// Normalize once at intake, after computing the immutable request digest.
// The samples stay raw; every wall timestamp consumed by placement is shifted
// together, preserving RTP rate fits, mute spans and decoded-duration checks.
func correctCaptureClock(s *captureSidecar, logger *log.Logger) {
	s.ClockStatus, s.ClockCorrectionMS, s.ClockUncertaintyMS = "", 0, 0
	if len(s.ClockSamples) == 0 {
		if logger != nil {
			logger.Printf("capture clock: owner=%q recording=%q session=%q status=unmeasured", s.OwnerUserID, s.RecordingID, s.SessionID)
		}
		return
	}
	offset, uncertainty, err := captureClockEstimate(s.ClockSamples, s.CallStartWallMS, s.CallEndWallMS)
	if err == nil && (s.CallStartWallMS-offset <= 0 || s.CallEndWallMS-offset <= 0) {
		err = fmt.Errorf("invalid corrected window")
	}
	if err != nil {
		s.ClockStatus = "unreliable"
		if logger != nil {
			logger.Printf("capture clock: owner=%q recording=%q session=%q status=unreliable reason=%q", s.OwnerUserID, s.RecordingID, s.SessionID, err)
		}
		return
	}
	s.ClockStatus, s.ClockCorrectionMS, s.ClockUncertaintyMS = "corrected", offset, uncertainty
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
		logger.Printf("capture clock: owner=%q recording=%q session=%q status=corrected client_ahead_ms=%d uncertainty_ms=%.3f samples=%d", s.OwnerUserID, s.RecordingID, s.SessionID, offset, uncertainty, len(s.ClockSamples))
	}
}
