package transcribe

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testClockRate = 48000

func testBase() RTPTimeBase {
	return RTPTimeBase{
		FirstRTPTimestamp: 1_000_000,
		FirstTimelineNS:   2_000_000_000, // the speaker's track starts 2 s in
		ClockRate:         testClockRate,
		Known:             true,
	}
}

// syntheticSegment builds a segment whose anchors describe a recording that
// started `offsetMS` into the meeting timeline, sampled every `stepMS`.
// driftPPM skews the participant's wall clock against their sample clock, which
// is what real hardware does.
func syntheticSegment(offsetMS float64, count int, stepMS float64, driftPPM float64) SourceSegment {
	base := testBase()
	startWall := int64(1_700_000_000_000)
	segment := SourceSegment{Index: 0, AudioName: "segment-0.webm", StartWallMS: startWall}
	for i := 0; i < count; i++ {
		localMS := float64(i) * stepMS
		meetingMS := offsetMS + localMS*(1+driftPPM/1e6)
		rtp := base.FirstRTPTimestamp +
			int64(math.Round((meetingMS-float64(base.FirstTimelineNS)/1e6)*float64(testClockRate)/1000))
		segment.Anchors = append(segment.Anchors, SourceAnchor{
			FrameIndex:   int64(i) * 50,
			RTPTimestamp: rtp,
			SSRC:         7,
			WallMS:       startWall + int64(math.Round(localMS)),
		})
	}
	segment.StopWallMS = startWall + int64(float64(count)*stepMS)
	return segment
}

func TestRTPTimeBaseTimelineMS(t *testing.T) {
	base := testBase()
	if got := base.timelineMS(base.FirstRTPTimestamp); got != 2000 {
		t.Fatalf("base timestamp maps to %v ms, want 2000", got)
	}
	// One second of samples later on the sender's 48 kHz clock.
	if got := base.timelineMS(base.FirstRTPTimestamp + testClockRate); math.Abs(got-3000) > 1e-9 {
		t.Fatalf("one second later maps to %v ms, want 3000", got)
	}
}

func TestRTPTimeBaseUnwrapsAroundTheModulus(t *testing.T) {
	// A meeting that straddles the 32-bit RTP wrap: the base sits just below
	// the modulus and later timestamps have wrapped to small values.
	base := RTPTimeBase{
		FirstRTPTimestamp: (1 << 32) - testClockRate, // one second before wrapping
		FirstTimelineNS:   0,
		ClockRate:         testClockRate,
		Known:             true,
	}
	// One second after the base, the counter has wrapped to 0.
	if got := base.timelineMS(0); math.Abs(got-1000) > 1e-9 {
		t.Fatalf("wrapped timestamp maps to %v ms, want 1000", got)
	}
}

func TestFitPlacementRecoversOffsetAndDrift(t *testing.T) {
	segment := syntheticSegment(5000, 120, 1000, 80) // 80 ppm, 2 minutes
	placement, err := FitPlacement(segment, testBase())
	if err != nil {
		t.Fatalf("FitPlacement: %v", err)
	}
	if math.Abs(placement.OffsetMS-5000) > 2 {
		t.Fatalf("offset = %.3f ms, want ~5000", placement.OffsetMS)
	}
	if math.Abs(placement.RatePPMDeviation()-80) > 5 {
		t.Fatalf("rate = %.1f ppm, want ~80", placement.RatePPMDeviation())
	}
	if placement.ResidualMS > 1 {
		t.Fatalf("residual = %.3f ms on clean anchors", placement.ResidualMS)
	}
}

// The central claim of the design: the mapping comes from the sender's clock,
// so losing most of the packets changes nothing. The anchors the client reports
// are the frames it ENCODED; whether each one reached the server is irrelevant.
func TestFitPlacementIsUnaffectedByPacketLoss(t *testing.T) {
	full := syntheticSegment(5000, 200, 1000, 80)
	intact, err := FitPlacement(full, testBase())
	if err != nil {
		t.Fatalf("FitPlacement(intact): %v", err)
	}

	// Keep every seventh anchor: an ~86% loss rate.
	lossy := SourceSegment{
		Index:       full.Index,
		AudioName:   full.AudioName,
		StartWallMS: full.StartWallMS,
		StopWallMS:  full.StopWallMS,
	}
	for i, anchor := range full.Anchors {
		if i%7 == 0 {
			lossy.Anchors = append(lossy.Anchors, anchor)
		}
	}
	damaged, err := FitPlacement(lossy, testBase())
	if err != nil {
		t.Fatalf("FitPlacement(lossy): %v", err)
	}

	if math.Abs(damaged.OffsetMS-intact.OffsetMS) > 1 {
		t.Fatalf("offset moved by %.3f ms under 86%% loss (intact %.3f, lossy %.3f)",
			math.Abs(damaged.OffsetMS-intact.OffsetMS), intact.OffsetMS, damaged.OffsetMS)
	}
	if math.Abs(damaged.RatePPMDeviation()-intact.RatePPMDeviation()) > 5 {
		t.Fatalf("rate moved by %.1f ppm under 86%% loss", math.Abs(damaged.RatePPMDeviation()-intact.RatePPMDeviation()))
	}
}

func TestFitPlacementRejectsUntrustworthyFits(t *testing.T) {
	t.Run("too few anchors", func(t *testing.T) {
		if _, err := FitPlacement(syntheticSegment(1000, 3, 1000, 0), testBase()); err == nil {
			t.Fatal("expected rejection with 3 anchors")
		}
	})

	t.Run("no RTP base in the recording", func(t *testing.T) {
		if _, err := FitPlacement(syntheticSegment(1000, 50, 1000, 0), RTPTimeBase{}); err == nil {
			t.Fatal("expected rejection without a base")
		}
	})

	t.Run("anchors that disagree", func(t *testing.T) {
		// A spliced or fabricated sidecar: half the anchors describe a
		// different placement. The fit must refuse rather than average them.
		segment := syntheticSegment(1000, 100, 1000, 0)
		for i := range segment.Anchors {
			if i%2 == 0 {
				segment.Anchors[i].WallMS += 4000
			}
		}
		_, err := FitPlacement(segment, testBase())
		if err == nil {
			t.Fatal("expected rejection of contradictory anchors")
		}
		if !strings.Contains(err.Error(), "disagree") && !strings.Contains(err.Error(), "plausible") {
			t.Fatalf("unexpected rejection reason: %v", err)
		}
	})

	t.Run("implausible rate", func(t *testing.T) {
		// 5% "drift" is not a sound card, it is a broken fit.
		if _, err := FitPlacement(syntheticSegment(1000, 100, 1000, 50000), testBase()); err == nil {
			t.Fatal("expected rejection of a 5% rate")
		}
	})
}

func TestFitPlacementSurvivesAWallClockStep(t *testing.T) {
	// NTP steps the clock mid-call. One outlier must not move the placement.
	segment := syntheticSegment(5000, 100, 1000, 40)
	segment.Anchors[50].WallMS += 900

	placement, err := FitPlacement(segment, testBase())
	if err != nil {
		t.Fatalf("FitPlacement: %v", err)
	}
	if math.Abs(placement.OffsetMS-5000) > 5 {
		t.Fatalf("a single clock step moved the offset to %.3f ms", placement.OffsetMS)
	}
	if placement.Anchors >= len(segment.Anchors) {
		t.Fatalf("outlier was not rejected (%d of %d anchors kept)", placement.Anchors, len(segment.Anchors))
	}
}

func TestRenderOntoTimeline(t *testing.T) {
	const sampleRate = 16000
	// A one-second ramp, to be placed 500 ms into a two-second timeline.
	src := make([]float32, sampleRate)
	for i := range src {
		src[i] = float32(i) / float32(sampleRate)
	}
	out := RenderOntoTimeline(src, sampleRate, Placement{OffsetMS: 500, Rate: 1}, 2*sampleRate)

	if len(out) != 2*sampleRate {
		t.Fatalf("length = %d, want %d", len(out), 2*sampleRate)
	}
	// Before the placement: silence, which is how the pipeline represents a
	// participant who was not producing audio yet.
	for i := 0; i < sampleRate/2; i++ {
		if out[i] != 0 {
			t.Fatalf("sample %d before the segment is %v, want 0", i, out[i])
		}
	}
	// The ramp's midpoint should land 1000 ms in.
	mid := out[sampleRate]
	if math.Abs(float64(mid)-0.5) > 0.01 {
		t.Fatalf("midpoint = %v, want ~0.5", mid)
	}
	// After the segment ends: silence again.
	if out[len(out)-1] != 0 {
		t.Fatalf("tail sample = %v, want 0", out[len(out)-1])
	}
}

func TestRenderOntoTimelineAppliesRateCorrection(t *testing.T) {
	const sampleRate = 16000
	src := make([]float32, sampleRate)
	// A single impulse half a second into the local recording.
	src[sampleRate/2] = 1

	// Rate > 1 means the participant's clock ran slow relative to the meeting,
	// so the impulse should land LATER than its local time.
	out := RenderOntoTimeline(src, sampleRate, Placement{OffsetMS: 0, Rate: 1.02}, 2*sampleRate)

	peak, peakAt := float32(0), 0
	for i, v := range out {
		if v > peak {
			peak, peakAt = v, i
		}
	}
	wantAt := int(0.5 * 1.02 * sampleRate)
	if math.Abs(float64(peakAt-wantAt)) > 2 {
		t.Fatalf("impulse landed at sample %d, want ~%d", peakAt, wantAt)
	}
}

func TestRenderOntoTimelineHandlesDegenerateInput(t *testing.T) {
	if got := RenderOntoTimeline(nil, 16000, Placement{Rate: 1}, 10); len(got) != 10 {
		t.Fatalf("empty source should still produce a full-length silent track, got %d", len(got))
	}
	if got := RenderOntoTimeline([]float32{1, 2}, 16000, Placement{Rate: 0}, 10); len(got) != 10 {
		t.Fatal("a zero rate must not panic or truncate")
	}
}

func writeCapture(t *testing.T, root, room, owner string, sidecar SourceSidecar) string {
	t.Helper()
	dir := filepath.Join(root, room, owner, "1700")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	raw, err := json.Marshal(sidecar)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "capture.json"), raw, 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	return dir
}

func TestDiscoverSourceCaptures(t *testing.T) {
	root := t.TempDir()
	writeCapture(t, root, "room1", "alice", SourceSidecar{
		Format: SourceCaptureFormat, OwnerUserID: "alice", CallStartWallMS: 1000,
	})
	writeCapture(t, root, "room1", "bob", SourceSidecar{
		Format: SourceCaptureFormat, OwnerUserID: "bob", CallStartWallMS: 1000,
	})
	// A malformed upload must be skipped, not fail the scan: the meeting still
	// has to publish.
	broken := filepath.Join(root, "room1", "carol", "1700")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(broken, "capture.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	found, err := DiscoverSourceCaptures(root)
	if err != nil {
		t.Fatalf("DiscoverSourceCaptures: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("found %d captures, want 2 (alice, bob): %v", len(found), found)
	}
	if _, ok := found["carol"]; ok {
		t.Fatal("a malformed sidecar was accepted")
	}
}

func TestDiscoverSourceCapturesIgnoresUnknownFormat(t *testing.T) {
	root := t.TempDir()
	writeCapture(t, root, "room1", "alice", SourceSidecar{
		Format: "org.cassini.source-capture/99", OwnerUserID: "alice",
	})
	found, err := DiscoverSourceCaptures(root)
	if err != nil {
		t.Fatalf("DiscoverSourceCaptures: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("a future format was accepted: %v", found)
	}
}

func TestDiscoverSourceCapturesEmptyRoot(t *testing.T) {
	found, err := DiscoverSourceCaptures("")
	if err != nil || found != nil {
		t.Fatalf("empty root = %v, %v; want nil, nil", found, err)
	}
}

func TestRTPTimeBaseFromTags(t *testing.T) {
	base := rtpTimeBaseFromTags("12345", "678", "48000")
	if !base.Known || base.FirstRTPTimestamp != 12345 || base.FirstTimelineNS != 678 || base.ClockRate != 48000 {
		t.Fatalf("parsed base = %+v", base)
	}
	// A partial base cannot map anything, and treating a missing tag as zero
	// would place audio at a confidently wrong time.
	for _, tc := range [][3]string{
		{"", "678", "48000"},
		{"12345", "", "48000"},
		{"12345", "678", ""},
		{"12345", "678", "0"},
		{"nonsense", "678", "48000"},
	} {
		if got := rtpTimeBaseFromTags(tc[0], tc[1], tc[2]); got.Known {
			t.Fatalf("tags %v produced a usable base %+v", tc, got)
		}
	}
}

func TestWriteWAV16RoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.wav")
	samples := []float32{0, 0.5, -0.5, 1, -1, 2, -2} // last two clamp
	if err := writeWAV16(path, samples, 16000); err != nil {
		t.Fatalf("writeWAV16: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(raw) != 44+len(samples)*2 {
		t.Fatalf("file is %d bytes, want %d", len(raw), 44+len(samples)*2)
	}
	if string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		t.Fatalf("not a RIFF/WAVE file: %q", raw[:12])
	}
	read := func(i int) int16 { return int16(uint16(raw[44+i*2]) | uint16(raw[45+i*2])<<8) }
	if read(0) != 0 {
		t.Fatalf("sample 0 = %d", read(0))
	}
	if read(5) != 32767 || read(6) != -32767 {
		t.Fatalf("clamping failed: %d, %d", read(5), read(6))
	}
}
