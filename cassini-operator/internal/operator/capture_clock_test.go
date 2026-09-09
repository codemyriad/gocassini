package operator

import (
	"bytes"
	"encoding/json"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func clockSample(server, ahead int64, network, processing float64) captureClockSample {
	return captureClockSample{
		ClientSendWallMS:    server + ahead,
		ServerReceiveWallMS: server + int64(network/2),
		ServerSendWallMS:    server + int64(network/2+processing),
		ClientReceiveWallMS: server + ahead + int64(network+processing),
		ElapsedMS:           network + processing,
	}
}

func TestCaptureClockCorrection(t *testing.T) {
	for _, ahead := range []int64{-300000, 300000, 0} {
		s := validSidecar()
		start, end := s.CallStartWallMS, s.CallEndWallMS
		s.ClockSamples = []captureClockSample{clockSample(start-ahead, ahead, 20, 500), clockSample(end-ahead, ahead, 10, 0)}
		s.Segments[0].Anchors = []captureAnchor{{FrameIndex: 0, WallMS: start + 100}}
		s.Segments[0].MuteIntervals = [][2]int64{{start + 200, start + 300}}
		var logs bytes.Buffer
		correctCaptureClock(&s, log.New(&logs, "", 0))
		if s.ClockStatus != "corrected" || s.ClockCorrectionMS != ahead || s.CallStartWallMS != start-ahead || s.CallEndWallMS != end-ahead {
			t.Fatalf("ahead=%d correction: %+v", ahead, s)
		}
		if s.Segments[0].StartWallMS != start-ahead || s.Segments[0].Anchors[0].WallMS != start+100-ahead || s.Segments[0].MuteIntervals[0][0] != start+200-ahead {
			t.Fatalf("timestamps did not move together: %+v", s.Segments[0])
		}
		if !strings.Contains(logs.String(), "client_ahead_ms=") || !strings.Contains(logs.String(), "uncertainty_ms=") {
			t.Fatal(logs.String())
		}
		if s.ClockSamples[0].ClientSendWallMS != start {
			t.Fatal("raw observation was changed")
		}
		if err := validateSidecar(&s); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCaptureClockRejectsUnreliableMeasurements(t *testing.T) {
	const at int64 = 1700000000000
	tests := map[string][]captureClockSample{
		"slow":                {clockSample(at, 4000, 1000, 0)},
		"stepped-between":     {clockSample(at, 4000, 10, 0), clockSample(at+1000, 6000, 10, 0)},
		"missing":             nil,
		"stale":               {clockSample(at-300000, 4000, 10, 0)},
		"negative-processing": {{ClientSendWallMS: at, ClientReceiveWallMS: at + 10, ServerReceiveWallMS: at, ServerSendWallMS: at - 1, ElapsedMS: 10}},
		"step-during":         {{ClientSendWallMS: at, ClientReceiveWallMS: at + 2000, ServerReceiveWallMS: at, ServerSendWallMS: at, ElapsedMS: 10}},
		"nonfinite":           {{ClientSendWallMS: at, ClientReceiveWallMS: at + 10, ServerReceiveWallMS: at, ServerSendWallMS: at, ElapsedMS: math.NaN()}},
	}
	for name, samples := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := captureClockEstimate(samples, at+4000, at+5000); err == nil {
				t.Fatal("accepted unreliable clock")
			}
		})
	}
}

func TestCaptureClockExcludesUnreliableFromRebuild(t *testing.T) {
	root := t.TempDir()
	start := ms(t, "2026-09-02T10:00:00Z")
	dir := seedRebuildCapture(t, root, "room-a", "alice", start, start+60000, 64)
	raw, err := os.ReadFile(filepath.Join(dir, captureSidecarName))
	if err != nil {
		t.Fatal(err)
	}
	var s captureSidecar
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	s.ClockSamples = []captureClockSample{clockSample(start, 0, 2000, 0)}
	correctCaptureClock(&s, nil)
	if s.ClockStatus != "unreliable" || s.CallStartWallMS != start {
		t.Fatalf("%+v", s)
	}
	raw, _ = json.Marshal(s)
	if err := os.WriteFile(filepath.Join(dir, captureSidecarName), raw, 0600); err != nil {
		t.Fatal(err)
	}
	set, err := scanSourceCapturesForRecording(root, "room-a", captureRecordingWindow{StartMS: start, EndMS: start + 60000})
	if err != nil || set.Count != 0 {
		t.Fatalf("unreliable capture selected: %+v %v", set, err)
	}
}

func TestCaptureClockSessionCoverage(t *testing.T) {
	const start int64 = 1700000000000
	const end = start + 60000
	const ahead int64 = 4000
	fresh := []captureClockSample{clockSample(start-ahead, ahead, 200, 80), clockSample(start+30000-ahead, ahead, 200, 80), clockSample(end-ahead, ahead, 200, 80)}
	for _, oldAhead := range []int64{ahead, -10000} {
		// A faster inherited probe must neither win selection nor veto this session.
		samples := append([]captureClockSample{clockSample(start-600000-oldAhead, oldAhead, 10, 80)}, fresh...)
		fit, err := captureClockEstimate(samples, start, end)
		if err != nil || fit.OffsetMS != ahead {
			t.Fatalf("old ahead=%d: fit=%+v error=%v", oldAhead, fit, err)
		}
	}
	samples := append([]captureClockSample{clockSample(start-4000-ahead, ahead, 200, 80)}, fresh...)
	if _, err := captureClockEstimate(samples, start, end); err != nil {
		t.Fatal(err)
	}
	if _, err := captureClockEstimate(fresh[:1], start, end+120000); err == nil {
		t.Fatal("initial probe extrapolated across an unobserved tail")
	}
	if _, err := captureClockEstimate(fresh[2:], start-120000, end); err == nil {
		t.Fatal("tail probe extrapolated across an unobserved start")
	}
}

// Observations from the real AppAPI/HaRP seam run 33964161554. Only timing
// fields are retained; all timestamps are translated to an arbitrary epoch.
func TestCaptureClockMeasuredProxyWithRemoteLatency(t *testing.T) {
	raw, err := os.ReadFile("testdata/capture-clock-proxy.json")
	if err != nil {
		t.Fatal(err)
	}
	var sessions []struct {
		Start, End int64
		Samples    []captureClockSample
	}
	if err := json.Unmarshal(raw, &sessions); err != nil {
		t.Fatal(err)
	}
	for i, session := range sessions {
		baseline, err := captureClockEstimate(session.Samples, session.Start, session.End)
		if err != nil {
			t.Fatalf("session %d baseline: %v", i, err)
		}
		t.Logf("proxy session %d: offset=%d ms uncertainty=%.3f ms variation=%.1f ms", i+1, baseline.OffsetMS, baseline.UncertaintyMS, baseline.VariationMS)
		for _, extraRTT := range []int64{130, 300, 400} {
			for _, outbound := range []int64{0, extraRTT / 2, extraRTT} {
				samples := append([]captureClockSample(nil), session.Samples...)
				for j := range samples {
					samples[j].ServerReceiveWallMS += outbound
					samples[j].ServerSendWallMS += outbound
					samples[j].ClientReceiveWallMS += extraRTT
					samples[j].ElapsedMS += float64(extraRTT)
				}
				fit, err := captureClockEstimate(samples, session.Start, session.End)
				if err != nil {
					t.Fatalf("session %d RTT=%d outbound=%d: %v", i, extraRTT, outbound, err)
				}
				bias := float64(extraRTT)/2 - float64(outbound)
				if math.Abs(float64(fit.OffsetMS-baseline.OffsetMS)-bias) > 1 {
					t.Fatalf("unexpected asymmetric-path estimate: %+v baseline=%+v bias=%v", fit, baseline, bias)
				}
				if fit.UncertaintyMS < math.Abs(bias) || math.Abs(fit.UncertaintyMS-baseline.UncertaintyMS-float64(extraRTT)/2) > 0.0011 {
					t.Fatalf("stable scatter concealed asymmetry uncertainty: %+v baseline=%+v", fit, baseline)
				}
				if fit.VariationMS != baseline.VariationMS {
					t.Fatalf("round trip became variation: %+v baseline=%+v", fit, baseline)
				}
			}
		}
	}
}

func TestCaptureClockDistinguishesNetworkJitterAndClockMovement(t *testing.T) {
	const start int64 = 1700000000000
	const ahead int64 = 300000
	first := clockSample(start-ahead, ahead, 300, 80)
	steady := clockSample(start+30000-ahead, ahead, 310, 80)
	// A queued response changes the midpoint within its own asymmetry bound.
	queued := clockSample(start+60000-ahead, ahead, 300, 80)
	queued.ClientReceiveWallMS += 800
	queued.ElapsedMS += 800
	fit, err := captureClockEstimate([]captureClockSample{first, steady, queued}, start, start+60000)
	if err != nil || fit.OffsetMS != ahead || fit.VariationMS != 0 {
		t.Fatalf("queued response: %+v %v", fit, err)
	}
	// Broad network bounds overlap, but comparable fast probes expose the step.
	stepped := clockSample(start+30000-(ahead+200), ahead+200, 300, 80)
	if _, err := captureClockEstimate([]captureClockSample{first, stepped}, start, start+60000); err == nil || !strings.Contains(err.Error(), "spread") {
		t.Fatalf("fast-probe clock movement accepted: %v", err)
	}
	// A slow probe still rejects a change exceeding its delay bound.
	queued.ServerReceiveWallMS -= 2000
	queued.ServerSendWallMS -= 2000
	if _, err := captureClockEstimate([]captureClockSample{first, queued}, start, start+60000); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("slow-probe clock step accepted: %v", err)
	}
	// Broad uncertainty does not authorize uncorrected placement minutes late.
	s := validSidecar()
	s.ClockSamples = []captureClockSample{clockSample(s.CallStartWallMS-ahead, ahead, 800, 80)}
	before := s.CallStartWallMS
	var logs bytes.Buffer
	correctCaptureClock(&s, log.New(&logs, "", 0))
	if s.ClockStatus != "unreliable" || s.CallStartWallMS != before || !strings.Contains(logs.String(), "action=retain_recorded_audio") || !strings.Contains(logs.String(), "reason=") {
		t.Fatalf("unsafe fallback or missing operational reason: %+v %s", s, logs.String())
	}
}

func TestCaptureClockRoundsStoredUncertainty(t *testing.T) {
	s := validSidecar()
	sample := clockSample(s.CallStartWallMS-5000, 5000, 100, 80)
	sample.ElapsedMS = 180.79999999998836
	s.ClockSamples = []captureClockSample{sample}
	correctCaptureClock(&s, nil)
	if s.ClockStatus != "corrected" || s.ClockUncertaintyMS != 52.4 {
		t.Fatalf("unrounded uncertainty: %+v", s)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"clockUncertaintyMs":52.4`)) {
		t.Fatalf("stored uncertainty: %s", raw)
	}
}
