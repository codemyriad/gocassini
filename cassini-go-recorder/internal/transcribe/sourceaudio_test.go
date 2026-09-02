package transcribe

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testClockRate = 48000

const testFirstPacketWallMS = int64(1_700_000_000_000)

func testBase() SourceTimeBase {
	return SourceTimeBase{
		FirstPacketWallMS: testFirstPacketWallMS,
		FirstTimelineNS:   2_000_000_000, // the speaker's track starts 2 s in
		ClockRate:         testClockRate,
		Known:             true,
	}
}

// syntheticSegment builds a segment whose anchors describe a recording that
// starts `offsetMS` into the meeting timeline, sampled every `stepMS` of the
// participant's wall clock.
//
// driftPPM is the participant's AUDIO sample clock running fast against their
// wall clock, which is what real sound cards do and the dominant drift this
// design corrects. The anchors therefore pair a wall instant with an RTP
// timestamp that advances at (1+drift) times nominal.
func syntheticSegment(offsetMS float64, count int, stepMS float64, driftPPM float64) SourceSegment {
	return syntheticSegmentDelayed(offsetMS, count, stepMS, driftPPM, 0)
}

// syntheticSegmentDelayed additionally models the gap between MediaRecorder
// starting and the first sampled anchor arriving — the encoder spinning up, and
// one sampling interval of fifty frames.
func syntheticSegmentDelayed(offsetMS float64, count int, stepMS float64, driftPPM float64, firstAnchorDelayMS float64) SourceSegment {
	base := testBase()
	// A segment that starts offsetMS into the meeting timeline began at this
	// wall instant, since timelineMS is wall-anchored.
	startWall := testFirstPacketWallMS + int64(offsetMS-float64(base.FirstTimelineNS)/1e6)
	baseRTP := int64(1_000_000)
	segment := SourceSegment{Index: 0, AudioName: "segment-0.webm", StartWallMS: startWall}
	for i := 0; i < count; i++ {
		wallElapsedMS := firstAnchorDelayMS + float64(i)*stepMS
		audioElapsedMS := wallElapsedMS * (1 + driftPPM/1e6)
		segment.Anchors = append(segment.Anchors, SourceAnchor{
			FrameIndex:   int64(i) * 50,
			RTPTimestamp: baseRTP + int64(math.Round(audioElapsedMS*float64(testClockRate)/1000)),
			SSRC:         7,
			WallMS:       startWall + int64(math.Round(wallElapsedMS)),
		})
	}
	segment.StopWallMS = startWall + int64(firstAnchorDelayMS+float64(count)*stepMS)
	return segment
}

func TestSourceTimeBaseTimelineMS(t *testing.T) {
	base := testBase()
	if got := base.timelineMS(base.FirstPacketWallMS); got != 2000 {
		t.Fatalf("the base instant maps to %v ms, want 2000 (where the track starts)", got)
	}
	if got := base.timelineMS(base.FirstPacketWallMS + 1000); math.Abs(got-3000) > 1e-9 {
		t.Fatalf("one second later maps to %v ms, want 3000", got)
	}
}

func TestMediaMSUnwrapsAroundTheModulus(t *testing.T) {
	// A long call straddling the 32-bit RTP wrap: the segment's base sits just
	// below the modulus and later anchors have wrapped to small values.
	baseRTP := int64(1<<32) - testClockRate // one second before wrapping
	if got := mediaMS(0, baseRTP, testClockRate); math.Abs(got-1000) > 1e-9 {
		t.Fatalf("wrapped anchor reads as %v ms, want 1000", got)
	}
	if got := mediaMS(baseRTP, baseRTP, testClockRate); got != 0 {
		t.Fatalf("the base anchor reads as %v ms, want 0", got)
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
	// The audio clock ran 80 ppm FAST, so a millisecond of recorded audio
	// covers slightly less than a millisecond of meeting: the fitted rate is
	// below 1 by the same amount.
	if math.Abs(placement.RatePPMDeviation()+80) > 5 {
		t.Fatalf("rate = %.1f ppm, want ~-80", placement.RatePPMDeviation())
	}
	if placement.ResidualMS > 1 {
		t.Fatalf("residual = %.3f ms on clean anchors", placement.ResidualMS)
	}
}

// Regression: local media time zero is the instant MediaRecorder started, not
// the first sampled anchor. Anchors arrive one per fifty encoded frames and the
// first comes after the encoder spins up, so anchoring on it shifted every
// speaker's audio late by up to a second — inside a word, and enough to attach
// the wrong speaker's name to it in a fast exchange.
func TestFitPlacementIgnoresWhenTheFirstAnchorArrives(t *testing.T) {
	prompt := syntheticSegmentDelayed(5000, 100, 1000, 60, 0)
	late := syntheticSegmentDelayed(5000, 100, 1000, 60, 900)

	promptPlacement, err := FitPlacement(prompt, testBase())
	if err != nil {
		t.Fatalf("FitPlacement(prompt): %v", err)
	}
	latePlacement, err := FitPlacement(late, testBase())
	if err != nil {
		t.Fatalf("FitPlacement(late): %v", err)
	}

	if math.Abs(latePlacement.OffsetMS-promptPlacement.OffsetMS) > 1 {
		t.Fatalf("a 900 ms delay before the first anchor moved the placement by %.1f ms (prompt %.1f, late %.1f)",
			math.Abs(latePlacement.OffsetMS-promptPlacement.OffsetMS),
			promptPlacement.OffsetMS, latePlacement.OffsetMS)
	}
	if math.Abs(latePlacement.OffsetMS-5000) > 1 {
		t.Fatalf("offset = %.1f ms, want 5000 — the segment starts where MediaRecorder started", latePlacement.OffsetMS)
	}
	// And the rate is still recovered from the anchors.
	if math.Abs(latePlacement.RatePPMDeviation()+60) > 5 {
		t.Fatalf("rate = %.1f ppm, want ~-60", latePlacement.RatePPMDeviation())
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

	t.Run("no wall-clock base in the recording", func(t *testing.T) {
		if _, err := FitPlacement(syntheticSegment(1000, 50, 1000, 0), SourceTimeBase{}); err == nil {
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

// A recording covering this wall-clock span. Captures are selected against it.
const (
	testWindowStartMS = int64(1_700_000_000_000)
	testWindowEndMS   = testWindowStartMS + 600_000
)

func inWindowSidecar(owner string, startMS int64) SourceSidecar {
	return SourceSidecar{
		Format:          SourceCaptureFormat,
		RoomToken:       "room1",
		OwnerUserID:     owner,
		CallStartWallMS: startMS,
		CallEndWallMS:   startMS + 300_000,
	}
}

func TestDiscoverSourceCaptures(t *testing.T) {
	root := t.TempDir()
	writeCapture(t, root, "room1", "alice", inWindowSidecar("alice", testWindowStartMS))
	writeCapture(t, root, "room1", "bob", inWindowSidecar("bob", testWindowStartMS))
	// A malformed upload must be skipped, not fail the scan: the meeting still
	// has to publish.
	broken := filepath.Join(root, "room1", "carol", "1700")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(broken, "capture.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	found, err := DiscoverSourceCaptures(root, "room1", testWindowStartMS, testWindowEndMS)
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

// Selecting on participant id alone put one meeting's speech into another's
// transcript two ways: a later unrelated capture hid the correct older one, and
// two nearby calls both looked plausible.
func TestDiscoverSourceCapturesIsScopedToThisRecording(t *testing.T) {
	root := t.TempDir()
	// The right capture for this recording.
	writeCapture(t, root, "room1", "alice", inWindowSidecar("alice", testWindowStartMS))
	// The same person, same room, a call three hours later — out of window.
	later := inWindowSidecar("alice", testWindowStartMS+3*3_600_000)
	dir := filepath.Join(root, "room1", "alice", "later")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	raw, _ := json.Marshal(later)
	if err := os.WriteFile(filepath.Join(dir, "capture.json"), raw, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// The same person in a DIFFERENT room, overlapping in time.
	other := inWindowSidecar("alice", testWindowStartMS)
	other.RoomToken = "room2"
	writeCapture(t, root, "room2", "alice", other)

	found, err := DiscoverSourceCaptures(root, "room1", testWindowStartMS, testWindowEndMS)
	if err != nil {
		t.Fatalf("DiscoverSourceCaptures: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("found %d captures, want exactly the in-window room1 one: %v", len(found), found)
	}
	if len(found["alice"]) != 1 || !strings.Contains(found["alice"][0], filepath.Join("room1", "alice", "1700")) {
		t.Fatalf("selected the wrong capture: %v", found["alice"])
	}
}

func TestDiscoverSourceCapturesWithoutARoomTokenStillFiltersByWindow(t *testing.T) {
	root := t.TempDir()
	writeCapture(t, root, "room1", "alice", inWindowSidecar("alice", testWindowStartMS))
	far := inWindowSidecar("bob", testWindowStartMS+24*3_600_000)
	far.RoomToken = "room9"
	writeCapture(t, root, "room9", "bob", far)

	found, err := DiscoverSourceCaptures(root, "", testWindowStartMS, testWindowEndMS)
	if err != nil {
		t.Fatalf("DiscoverSourceCaptures: %v", err)
	}
	if _, ok := found["bob"]; ok {
		t.Fatal("a capture a day away was accepted when no room token was given")
	}
	if _, ok := found["alice"]; !ok {
		t.Fatal("the in-window capture was rejected")
	}
}

func TestWindowsOverlap(t *testing.T) {
	const start, end = int64(1000), int64(2000)
	cases := []struct {
		name         string
		aStart, aEnd int64
		want         bool
	}{
		{"identical", start, end, true},
		{"contained", 1200, 1800, true},
		{"straddling the start", 500, 1200, true},
		{"straddling the end", 1800, 5000, true},
		{"just outside, inside the slack", end + 30_000, end + 60_000, true},
		{"well after", end + 600_000, end + 900_000, false},
		{"well before", start - 900_000, start - 600_000, false},
		{"unset", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := windowsOverlap(tc.aStart, tc.aEnd, start, end); got != tc.want {
				t.Fatalf("windowsOverlap(%d,%d,%d,%d) = %v, want %v", tc.aStart, tc.aEnd, start, end, got, tc.want)
			}
		})
	}
}

func TestTranscribableStreamsDropsSuppressed(t *testing.T) {
	streams := []AudioStream{
		{SpeakerID: "alice", SourceAudioPath: "/tmp/alice.wav"},
		{SpeakerID: "alice", SuppressTranscription: true},
		{SpeakerID: "bob"},
	}
	kept := transcribableStreams(streams)
	if len(kept) != 2 {
		t.Fatalf("kept %d streams, want 2", len(kept))
	}
	for _, stream := range kept {
		if stream.SuppressTranscription {
			t.Fatal("a suppressed stream survived")
		}
	}
	// Unchanged when nothing is suppressed, which is every build with no
	// uploads.
	plain := []AudioStream{{SpeakerID: "a"}, {SpeakerID: "b"}}
	if len(transcribableStreams(plain)) != 2 {
		t.Fatal("plain streams were filtered")
	}
}

func TestSourceTimeBaseFromTags(t *testing.T) {
	base := sourceTimeBaseFromTags("12345", "678", "48000")
	if !base.Known || base.FirstPacketWallMS != 12345 || base.FirstTimelineNS != 678 || base.ClockRate != 48000 {
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
		{"0", "678", "48000"},
	} {
		if got := sourceTimeBaseFromTags(tc[0], tc[1], tc[2]); got.Known {
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

func TestPlausibleOffset(t *testing.T) {
	const timelineMS = 600_000 // a ten-minute meeting

	if !PlausibleOffset(Placement{OffsetMS: 0}, timelineMS) {
		t.Fatal("a segment starting with the meeting is plausible")
	}
	if !PlausibleOffset(Placement{OffsetMS: 300_000}, timelineMS) {
		t.Fatal("a segment starting mid-meeting is plausible")
	}
	// A little negative or a little past the end is tolerated: the guard exists
	// to catch a clock that is wrong by hours, and rejecting wrongly only costs
	// us the fallback to the recorded track.
	if !PlausibleOffset(Placement{OffsetMS: -10_000}, timelineMS) {
		t.Fatal("a small negative offset should be tolerated")
	}

	// A client whose clock is wrong by a day scatters its audio somewhere the
	// meeting never was. That must be refused rather than transcribed.
	if PlausibleOffset(Placement{OffsetMS: 86_400_000}, timelineMS) {
		t.Fatal("an offset a day into the future was accepted")
	}
	if PlausibleOffset(Placement{OffsetMS: -86_400_000}, timelineMS) {
		t.Fatal("an offset a day in the past was accepted")
	}

	// A very short recording still gets a floor of slack, so a 5 s clip is not
	// judged against a 2.5 s window.
	if !PlausibleOffset(Placement{OffsetMS: -20_000}, 5_000) {
		t.Fatal("the minimum slack floor is not being applied")
	}
}

// A participant who leaves and rejoins uploads one capture per session, and
// both belong to this recording. Keeping only the newest while suppressing
// their recorded streams silently dropped the first half of what they said.
func TestDiscoverSourceCapturesKeepsEveryMatchingCapture(t *testing.T) {
	root := t.TempDir()
	first := inWindowSidecar("alice", testWindowStartMS)
	writeCapture(t, root, "room1", "alice", first)

	second := inWindowSidecar("alice", testWindowStartMS+120_000)
	dir := filepath.Join(root, "room1", "alice", "rejoin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	raw, _ := json.Marshal(second)
	if err := os.WriteFile(filepath.Join(dir, "capture.json"), raw, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	found, err := DiscoverSourceCaptures(root, "room1", testWindowStartMS, testWindowEndMS)
	if err != nil {
		t.Fatalf("DiscoverSourceCaptures: %v", err)
	}
	if len(found["alice"]) != 2 {
		t.Fatalf("kept %d of alice's captures, want both sessions: %v", len(found["alice"]), found["alice"])
	}
}

// Skipping an unplaceable segment and keeping the rest looked conservative and
// was the opposite: the caller substitutes the render for the speaker AND drops
// their recorded streams, so a skipped segment became silence where words had
// been. Any segment that cannot be placed must fail the whole speaker back to
// the recorded audio.
func TestRenderSourceTrackRefusesAPartialCapture(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "capture")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Too few anchors to place: a short segment, e.g. after a device change
	// near the end of a call. Placed FIRST so the refusal happens at the fit,
	// before any decoding, which keeps the test free of fixture media.
	bad := syntheticSegment(1000, 3, 1000, 0)
	bad.AudioName = "segment-0.webm"
	good := syntheticSegment(70_000, 60, 1000, 0)
	good.Index = 1
	good.AudioName = "segment-1.webm"

	sidecar := SourceSidecar{
		Format:          SourceCaptureFormat,
		RoomToken:       "room1",
		OwnerUserID:     "alice",
		CallStartWallMS: testWindowStartMS,
		CallEndWallMS:   testWindowEndMS,
		Segments:        []SourceSegment{bad, good},
	}
	raw, _ := json.Marshal(sidecar)
	if err := os.WriteFile(filepath.Join(dir, "capture.json"), raw, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, report, err := RenderSourceTrack(context.Background(), []string{dir}, testBase(), 16000, 16000*120)
	if err == nil {
		t.Fatal("a capture with an unplaceable segment was accepted; that silently deletes speech")
	}
	if !strings.Contains(err.Error(), "anchors") {
		t.Fatalf("refused for the wrong reason: %v", err)
	}
	if report.Placed != 0 {
		t.Fatalf("report claims %d segments placed by a refused capture", report.Placed)
	}
}

func TestRecordingWallWindowDerivesTimelineZero(t *testing.T) {
	const zero = int64(1_700_000_000_000)
	// Nobody spoke for the first thirty seconds, so the earliest track's first
	// packet is thirty seconds AFTER the recording began. Taking that instant
	// as timeline zero shifted the whole matching window later, which could
	// then select a later call in the same room.
	streams := []AudioStream{
		{TimeBase: SourceTimeBase{FirstPacketWallMS: zero + 30_000, FirstTimelineNS: 30_000_000_000, Known: true}},
		{TimeBase: SourceTimeBase{FirstPacketWallMS: zero + 45_000, FirstTimelineNS: 45_000_000_000, Known: true}},
	}
	start, end := recordingWallWindow(streams, 600_000)
	if start != zero {
		t.Fatalf("window starts at %d, want %d (the recording's own zero)", start, zero)
	}
	if end != zero+600_000 {
		t.Fatalf("window ends at %d, want %d", end, zero+600_000)
	}

	// No usable base at all: no window, so nothing is selected.
	if s, e := recordingWallWindow([]AudioStream{{}}, 600_000); s != 0 || e != 0 {
		t.Fatalf("unknown base produced a window: %d..%d", s, e)
	}
}

// A re-upload is promoted by renaming the previous capture aside. That name
// lives at the same depth as a real capture, so discovery has to reject it by
// name: finding both would render the same speech twice onto one timeline,
// summed, while the recorded track is already suppressed.
func TestDiscoverSourceCapturesIgnoresSupersededDirectories(t *testing.T) {
	root := t.TempDir()
	live := writeCapture(t, root, "room1", "alice", inWindowSidecar("alice", testWindowStartMS))

	aside := live + supersededSuffix
	if err := os.MkdirAll(aside, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	raw, err := json.Marshal(inWindowSidecar("alice", testWindowStartMS))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(aside, "capture.json"), raw, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	found, err := DiscoverSourceCaptures(root, "room1", testWindowStartMS, testWindowEndMS)
	if err != nil {
		t.Fatalf("DiscoverSourceCaptures: %v", err)
	}
	dirs := found["alice"]
	if len(dirs) != 1 {
		t.Fatalf("found %d captures for alice, want only the live one: %v", len(dirs), dirs)
	}
	if dirs[0] != live {
		t.Fatalf("discovered %s, want the live capture %s", dirs[0], live)
	}
}

// The decode budget has to be generous enough for a real segment on a loaded
// host and tight enough that a file whose declared length is a lie cannot hold
// the only build worker.
func TestSourceDecodeTimeoutIsBounded(t *testing.T) {
	cases := []struct {
		name      string
		segmentMS int64
		want      time.Duration
	}{
		{"a zero-length claim still gets the floor", 0, 60 * time.Second},
		{"a negative claim cannot shrink it", -100_000, 60 * time.Second},
		{"a two-minute segment stays on the floor", 120_000, 60 * time.Second},
		{"a one-hour segment scales up", 3_600_000, 6 * time.Minute},
		{"a claimed month is capped", 30 * 24 * 3_600_000, 10 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sourceDecodeTimeout(tc.segmentMS); got != tc.want {
				t.Fatalf("sourceDecodeTimeout(%d) = %s, want %s", tc.segmentMS, got, tc.want)
			}
		})
	}
}

// A WAV that cannot be written must say so. Reporting success on a truncated
// file hands it to the transcription pass as if it were whole, and that
// failure is fatal to a build that would otherwise have published.
func TestWriteWAV16ReportsAFailedWrite(t *testing.T) {
	dir := t.TempDir()
	// A directory is not a file: Create fails, and the error must surface.
	if err := writeWAV16(dir, []float32{0.1, -0.1}, 16000); err == nil {
		t.Fatal("writing over a directory reported success")
	}

	good := filepath.Join(dir, "ok.wav")
	if err := writeWAV16(good, []float32{0.5, -0.5, 0}, 16000); err != nil {
		t.Fatalf("writeWAV16: %v", err)
	}
	info, err := os.Stat(good)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if want := int64(44 + 3*2); info.Size() != want {
		t.Fatalf("wrote %d bytes, want %d (44-byte header plus three samples)", info.Size(), want)
	}
}
