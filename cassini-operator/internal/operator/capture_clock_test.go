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
			if _, _, err := captureClockEstimate(samples, at+4000, at+5000); err == nil {
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
