package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
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
	if read(5) != 32767 || read(6) != -32768 {
		t.Fatalf("clamping failed: %d, %d", read(5), read(6))
	}
	// The scale is the decoder's, exactly. readPCM16LEFloatsBounded produces
	// s16/32768, and this has to put the same s16 back: the file is mostly the
	// participant's RECORDED track, and every sample of it outside an overlaid
	// window is supposed to be untouched. Scaling by 32767 quietly changed all
	// of them.
	if read(1) != 16384 || read(2) != -16384 {
		t.Fatalf("half scale round-tripped to %d and %d, want 16384 and -16384", read(1), read(2))
	}
}

// The boundary property has to hold in the FILE the transcription pass reads,
// not only in the slice the splice returned. An in-memory assertion missed a
// requantisation that changed every sample of the recorded floor on its way to
// disk.
func TestSplicedWAVLeavesTheRecordedFloorBitExact(t *testing.T) {
	const sampleRate = 16000
	const outSamples = sampleRate * 4

	// Values chosen to be exactly representable coming back out of a 16-bit
	// file, and to include the ones a 32767 scale destroys.
	recorded := make([]float32, outSamples)
	for i := range recorded {
		recorded[i] = float32(int16(1+(i%7)*2341)) / 32768
	}

	path := filepath.Join(t.TempDir(), "spliced.wav")
	if err := writeWAV16(path, recorded, sampleRate); err != nil {
		t.Fatalf("writeWAV16: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	back, err := readPCM16LEFloats(bytes.NewReader(raw[44:]), outSamples)
	if err != nil {
		t.Fatalf("readPCM16LEFloats: %v", err)
	}
	if len(back) != outSamples {
		t.Fatalf("read back %d samples, want %d", len(back), outSamples)
	}
	for i := range recorded {
		if math.Float32bits(back[i]) != math.Float32bits(recorded[i]) {
			t.Fatalf("sample %d came back as %v, want %v: the recorded floor is not bit-exact through the WAV",
				i, back[i], recorded[i])
		}
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

// The wall-clock deadline does not bound how much audio a segment decodes to.
// ffmpeg runs far faster than real time, so a small compressed file that
// expands to hours — or loops — emits gigabytes of PCM well inside any timeout,
// and the build dies on memory instead. The declared window is the only size
// the client committed to.
func TestMaxSourceSegmentSamplesBoundsDecodedAudio(t *testing.T) {
	const rate = 16000
	// expectedPCMSamples carries its own one-second allowance, so the bound is
	// expressed through it rather than restated here.
	floor := expectedPCMSamples(60_000, rate)

	if got := maxSourceSegmentSamples(0, rate, 0); got != floor {
		t.Fatalf("a segment declaring no length got %d samples, want the %d floor", got, floor)
	}
	if got := maxSourceSegmentSamples(-5000, rate, 0); got != floor {
		t.Fatalf("a negative declaration got %d samples, want the %d floor", got, floor)
	}
	// Ten declared minutes plus the one-minute slack.
	if got, want := maxSourceSegmentSamples(600_000, rate, 0), expectedPCMSamples(660_000, rate); got != want {
		t.Fatalf("a ten-minute segment got %d samples, want %d", got, want)
	}
	// The recording is the real bound: a segment cannot legitimately hold more
	// audio than the meeting it came from, whatever its sidecar claims.
	timeline := expectedPCMSamples(120_000, rate)
	got := maxSourceSegmentSamples(30*24*3_600_000, rate, timeline)
	if got > timeline+expectedPCMSamples(60_000, rate) {
		t.Fatalf("a segment claiming a month permits %d samples against a two-minute meeting", got)
	}
	// And a declaration inside the meeting is left alone.
	if got := maxSourceSegmentSamples(60_000, rate, timeline); got != expectedPCMSamples(120_000, rate) {
		t.Fatalf("a one-minute segment in a two-minute meeting got %d samples", got)
	}
}

// The reader must stop rather than keep buffering once the ceiling is passed.
func TestReadPCM16LEFloatsBoundedRefusesRunawayOutput(t *testing.T) {
	// 4000 samples of silence, against a 100-sample ceiling.
	raw := make([]byte, 4000*2)
	if _, err := readPCM16LEFloatsBounded(bytes.NewReader(raw), 0, 100); err == nil {
		t.Fatal("a decode past the ceiling was accepted; that is the OOM path")
	}

	// Comfortably inside the ceiling still returns every sample.
	samples, err := readPCM16LEFloatsBounded(bytes.NewReader(raw), 0, 8000)
	if err != nil {
		t.Fatalf("readPCM16LEFloatsBounded: %v", err)
	}
	if len(samples) != 4000 {
		t.Fatalf("got %d samples, want 4000", len(samples))
	}

	// A zero ceiling means unbounded, for inputs the recorder itself wrote.
	if _, err := readPCM16LEFloatsBounded(bytes.NewReader(raw), 0, 0); err != nil {
		t.Fatalf("an unbounded read failed: %v", err)
	}
}

// The ceiling has to be reachable from the ffmpeg path, not only from the
// reader. An earlier attempt wired the reader but not the command that calls
// it, and the reader-level test did not notice.
func TestDecodeSourceSegmentEnforcesTheCeiling(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "long.wav")
	// Ten seconds of tone, offered as a segment that declares nothing.
	cmd := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi",
		"-i", "sine=frequency=440:duration=10", "-ac", "1", "-ar", "16000", src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("could not synthesise input: %v: %s", err, out)
	}

	// A ceiling below the real length must be refused, through the real path.
	if _, err := decodeSourceSegment(context.Background(), src, 16000, 30*time.Second, 16000); err == nil {
		t.Fatal("decode past the ceiling was accepted through decodeSourceSegment")
	}
	// A ceiling above it decodes normally.
	samples, err := decodeSourceSegment(context.Background(), src, 16000, 30*time.Second, 16000*60)
	if err != nil {
		t.Fatalf("decodeSourceSegment: %v", err)
	}
	if len(samples) < 16000*9 {
		t.Fatalf("decoded %d samples, want about ten seconds", len(samples))
	}
}

// --- the splice --------------------------------------------------------------
//
// Ingestion lays a participant's own recording over their recorded track rather
// than replacing it. Everything below is about the one property that makes that
// safe to do without a coverage threshold in front of it: outside the windows an
// upload actually holds audio for, the recorded track has to come through
// untouched.

// recordedMarker is a track of one constant value, so any sample the splice
// changed is distinguishable from one it left alone by inspection.
func recordedMarker(n int, value float32) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = value
	}
	return out
}

// The boundary property, stated as narrowly as it can be: a segment overlaid in
// the middle of a track changes its own window and not one sample either side of
// it. Compared on the raw bits, because "close enough" is exactly the failure
// this is guarding against — a resampler that bleeds a sample past the edge, or
// a summed rather than overwritten placement.
func TestOverlayLeavesEverythingOutsideItsWindowByteIdentical(t *testing.T) {
	const sampleRate = 16000
	const outSamples = sampleRate * 10

	recorded := recordedMarker(outSamples, 0.25)
	original := append([]float32(nil), recorded...)

	// A distinct signal, so nothing inside the window can be mistaken for the
	// recorded value it replaced.
	src := make([]float32, sampleRate*2)
	for i := range src {
		src[i] = -0.75
	}

	from, to := overlayOntoTimeline(recorded, src, sampleRate, Placement{OffsetMS: 4000, Rate: 1})
	if from <= 0 || to <= from || to >= outSamples {
		t.Fatalf("overlay window [%d, %d) is not inside the %d-sample track", from, to, outSamples)
	}
	// The window is where the segment said it would be.
	if want := 4 * sampleRate; from != want {
		t.Fatalf("window opens at sample %d, want %d", from, want)
	}
	if want, got := 6*sampleRate, to; got < want-2 || got > want+2 {
		t.Fatalf("window closes at sample %d, want about %d", got, want)
	}

	for i := 0; i < outSamples; i++ {
		inside := i >= from && i < to
		changed := math.Float32bits(recorded[i]) != math.Float32bits(original[i])
		if !inside && changed {
			t.Fatalf("sample %d outside the overlay window changed from %v to %v; the recorded track must survive there",
				i, original[i], recorded[i])
		}
		if inside && !changed {
			t.Fatalf("sample %d inside the overlay window kept the recorded value %v; the upload was not applied",
				i, original[i])
		}
	}
}

// Overwrite, never sum. The recorded track is already in the destination, so
// adding would play the same words twice at once.
func TestOverlayReplacesRatherThanMixes(t *testing.T) {
	const sampleRate = 16000
	dst := recordedMarker(sampleRate, 0.5)
	src := recordedMarker(sampleRate, 0.5)
	from, to := overlayOntoTimeline(dst, src, sampleRate, Placement{OffsetMS: 0, Rate: 1})
	if to <= from {
		t.Fatal("nothing was overlaid")
	}
	for i := from; i < to; i++ {
		if dst[i] > 0.6 {
			t.Fatalf("sample %d is %v: the segment was summed onto the recorded track, not laid over it", i, dst[i])
		}
	}
}

// writeToneSegment synthesises one segment's audio file. The name is the
// sidecar's audioName — ffmpeg reads the container from the bytes, so a WAV
// under a .webm name decodes exactly as the real upload would.
func writeToneSegment(t *testing.T, dir, name string, seconds float64, frequency int) {
	t.Helper()
	cmd := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi",
		"-i", fmt.Sprintf("sine=frequency=%d:duration=%g", frequency, seconds),
		"-ac", "1", "-ar", "16000", "-f", "wav", filepath.Join(dir, name))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("could not synthesise segment audio: %v: %s", err, out)
	}
}

func writeSidecar(t *testing.T, dir string, sidecar SourceSidecar) {
	t.Helper()
	raw, err := json.Marshal(sidecar)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "capture.json"), raw, 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
}

// The reload case, end to end through the splice.
//
// A participant reloads mid-recording and rejoins. Their capture holds the audio
// from before the reload and the audio from after it, with a hole between the
// two where the page was loading and they were not in the call at all. The old
// whole-call coverage gate refused exactly this capture; there is nothing to
// refuse now, because the hole keeps whatever the recorder heard — which for a
// participant who was absent is what should be there.
func TestSpliceUsesBothSidesOfAReloadAndKeepsTheRecordedAudioInTheGap(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	const sampleRate = 16000
	const outSamples = sampleRate * 120

	dir := t.TempDir()
	// Twenty seconds from the start, then a forty-second reload gap, then
	// twenty seconds after the rejoin.
	before := syntheticSegmentDelayed(0, 20, 1000, 0, 0)
	before.AudioName = "segment-0.webm"
	after := syntheticSegmentDelayed(60_000, 20, 1000, 0, 0)
	after.Index = 1
	after.AudioName = "segment-1.webm"
	writeToneSegment(t, dir, before.AudioName, 20, 440)
	writeToneSegment(t, dir, after.AudioName, 20, 660)
	writeSidecar(t, dir, SourceSidecar{
		Format: SourceCaptureFormat, RoomToken: "room1", OwnerUserID: "alice",
		CallStartWallMS: before.StartWallMS, CallEndWallMS: after.StopWallMS,
		Segments: []SourceSegment{before, after},
	})

	recorded := recordedMarker(outSamples, 0.25)
	original := append([]float32(nil), recorded...)

	out, report, err := SpliceSourceTrack(context.Background(), recorded, []string{dir}, testBase(), sampleRate, outSamples)
	if err != nil {
		t.Fatalf("a reloader's capture was refused: %v", err)
	}
	if report.Placed != 2 || report.Skipped != 0 {
		t.Fatalf("placed %d segments and skipped %d, want 2 and 0", report.Placed, report.Skipped)
	}
	// Both stints reached the timeline, in their own places.
	for _, at := range []int{sampleRate * 5, sampleRate * 65} {
		if math.Float32bits(out[at]) == math.Float32bits(original[at]) {
			t.Fatalf("sample %d still holds the recorded value; that stint of the upload was not used", at)
		}
	}
	// The reload gap is the recorded track, sample for sample. The participant
	// was not in the call there, so this is silence the recorder already had.
	for i := sampleRate * 25; i < sampleRate*55; i++ {
		if math.Float32bits(out[i]) != math.Float32bits(original[i]) {
			t.Fatalf("sample %d inside the reload gap was overwritten; the recorded audio must stand there", i)
		}
	}
	// And so is everything after the participant stopped.
	for i := sampleRate * 85; i < outSamples; i++ {
		if math.Float32bits(out[i]) != math.Float32bits(original[i]) {
			t.Fatalf("sample %d after the capture ends was overwritten", i)
		}
	}
	if report.SplicedMS < 38_000 || report.SplicedMS > 42_000 {
		t.Fatalf("report claims %d ms spliced, want about 40000", report.SplicedMS)
	}
	// The caller's own slice is never mutated: it is the fallback if the WAV
	// write fails, and the evidence that a splice changed only what it says.
	for i := range recorded {
		if math.Float32bits(recorded[i]) != math.Float32bits(original[i]) {
			t.Fatalf("SpliceSourceTrack mutated the caller's recorded track at sample %d", i)
		}
	}
}

// A segment nothing can place is left out, and the recorded audio stays where it
// would have gone. Under whole-track substitution this failed the entire
// speaker, so one short fragment — a reload a few seconds in, a microphone
// change near the end — cost every other segment they uploaded.
func TestSpliceSkipsAnUnplaceableSegmentAndKeepsTheRest(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	const sampleRate = 16000
	const outSamples = sampleRate * 120

	dir := t.TempDir()
	// Three anchors: fewer than a placement needs.
	stub := syntheticSegmentDelayed(0, 3, 1000, 0, 0)
	stub.AudioName = "segment-0.webm"
	good := syntheticSegmentDelayed(60_000, 20, 1000, 0, 0)
	good.Index = 1
	good.AudioName = "segment-1.webm"
	writeToneSegment(t, dir, stub.AudioName, 3, 440)
	writeToneSegment(t, dir, good.AudioName, 20, 660)
	writeSidecar(t, dir, SourceSidecar{
		Format: SourceCaptureFormat, RoomToken: "room1", OwnerUserID: "alice",
		CallStartWallMS: stub.StartWallMS, CallEndWallMS: good.StopWallMS,
		Segments: []SourceSegment{stub, good},
	})

	recorded := recordedMarker(outSamples, 0.25)
	original := append([]float32(nil), recorded...)
	out, report, err := SpliceSourceTrack(context.Background(), recorded, []string{dir}, testBase(), sampleRate, outSamples)
	if err != nil {
		t.Fatalf("one unplaceable segment refused the whole speaker: %v", err)
	}
	if report.Placed != 1 || report.Skipped != 1 {
		t.Fatalf("placed %d and skipped %d, want 1 and 1", report.Placed, report.Skipped)
	}
	if len(report.Rejections) != 1 || !strings.Contains(report.Rejections[0], "anchors") {
		t.Fatalf("the skip does not say why: %v", report.Rejections)
	}
	for i := 0; i < sampleRate*3; i++ {
		if math.Float32bits(out[i]) != math.Float32bits(original[i]) {
			t.Fatalf("sample %d was overwritten by a segment that could not be placed", i)
		}
	}
	if math.Float32bits(out[sampleRate*65]) == math.Float32bits(original[sampleRate*65]) {
		t.Fatal("the placeable segment was dropped along with the unplaceable one")
	}
}

// A segment holding far less audio than it declares is left out too. Intake
// checks that a declared file arrived, never that it holds what was claimed, and
// a file that disagrees with its own manifest this badly is not one whose timing
// claims are worth believing either.
func TestSpliceSkipsASegmentWhoseAudioDoesNotMatchItsSidecar(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	const sampleRate = 16000
	const outSamples = sampleRate * 120

	dir := t.TempDir()
	// Declares sixty seconds, holds five.
	lying := syntheticSegmentDelayed(0, 60, 1000, 0, 0)
	lying.AudioName = "segment-0.webm"
	writeToneSegment(t, dir, lying.AudioName, 5, 440)
	writeSidecar(t, dir, SourceSidecar{
		Format: SourceCaptureFormat, RoomToken: "room1", OwnerUserID: "alice",
		CallStartWallMS: lying.StartWallMS, CallEndWallMS: lying.StopWallMS,
		Segments: []SourceSegment{lying},
	})

	recorded := recordedMarker(outSamples, 0.25)
	_, report, err := SpliceSourceTrack(context.Background(), recorded, []string{dir}, testBase(), sampleRate, outSamples)
	if err == nil {
		t.Fatal("a segment whose audio contradicts its sidecar was used")
	}
	if report.Skipped != 1 {
		t.Fatalf("skipped %d segments, want 1", report.Skipped)
	}
	if len(report.Rejections) != 1 || !strings.Contains(report.Rejections[0], "does not match the sidecar") {
		t.Fatalf("the skip does not say why: %v", report.Rejections)
	}
}

// A segment that holds far MORE audio than it declares must not replace the
// recorded track past the window it claimed. The decoded-versus-declared check
// only catches a file that is too short, and the decoder's own ceiling allows a
// generous overrun, so without a clamp a one-second window could overwrite a
// minute of recorded audio.
func TestSpliceOverlaysOnlyTheWindowASegmentDeclares(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	const sampleRate = 16000
	const outSamples = sampleRate * 120

	dir := t.TempDir()
	// Declares twenty seconds and holds sixty.
	overlong := syntheticSegmentDelayed(0, 20, 1000, 0, 0)
	overlong.AudioName = "segment-0.webm"
	writeToneSegment(t, dir, overlong.AudioName, 60, 440)
	writeSidecar(t, dir, SourceSidecar{
		Format: SourceCaptureFormat, RoomToken: "room1", OwnerUserID: "alice",
		CallStartWallMS: overlong.StartWallMS, CallEndWallMS: overlong.StopWallMS,
		Segments: []SourceSegment{overlong},
	})

	recorded := recordedMarker(outSamples, 0.25)
	original := append([]float32(nil), recorded...)
	out, report, err := SpliceSourceTrack(context.Background(), recorded, []string{dir}, testBase(), sampleRate, outSamples)
	if err != nil {
		t.Fatalf("SpliceSourceTrack: %v", err)
	}
	if report.Placed != 1 {
		t.Fatalf("placed %d segments, want 1", report.Placed)
	}
	// Twenty declared seconds plus the checkpoint slack, and not a sample of
	// the forty seconds beyond that.
	if report.SplicedMS > 32_000 {
		t.Fatalf("spliced %d ms over a segment declaring 20000 ms", report.SplicedMS)
	}
	for i := sampleRate * 45; i < outSamples; i++ {
		if math.Float32bits(out[i]) != math.Float32bits(original[i]) {
			t.Fatalf("sample %d was overwritten by audio outside the segment's declared window", i)
		}
	}
}

// A degenerate call is an error, not a division by zero.
func TestSpliceRefusesADegenerateTimeline(t *testing.T) {
	if _, _, err := SpliceSourceTrack(context.Background(), nil, nil, testBase(), 0, 100); err == nil {
		t.Fatal("a zero sample rate was accepted")
	}
	if _, _, err := SpliceSourceTrack(context.Background(), nil, nil, testBase(), 16000, 0); err == nil {
		t.Fatal("a zero-length timeline was accepted")
	}
}

// A capture that contributes nothing is an error rather than a WAV identical to
// the recorded track, so the caller leaves the stream alone.
func TestSpliceRefusesWhenNoSegmentCanBePlaced(t *testing.T) {
	const sampleRate = 16000
	const outSamples = sampleRate * 120

	dir := t.TempDir()
	stub := syntheticSegmentDelayed(0, 3, 1000, 0, 0)
	stub.AudioName = "segment-0.webm"
	writeSidecar(t, dir, SourceSidecar{
		Format: SourceCaptureFormat, RoomToken: "room1", OwnerUserID: "alice",
		CallStartWallMS: stub.StartWallMS, CallEndWallMS: stub.StopWallMS,
		Segments: []SourceSegment{stub},
	})

	recorded := recordedMarker(outSamples, 0.25)
	samples, report, err := SpliceSourceTrack(context.Background(), recorded, []string{dir}, testBase(), sampleRate, outSamples)
	if err == nil {
		t.Fatal("a capture with nothing placeable produced a track")
	}
	if samples != nil {
		t.Fatal("a refused splice still returned audio")
	}
	if report.Segments != 1 || report.Placed != 0 {
		t.Fatalf("report claims %d segments and %d placed", report.Segments, report.Placed)
	}
}

// A capture from a rejoin is a second directory, and both belong to this
// recording. Each is spliced onto the same timeline in its own place; neither
// can refuse the other, because there is no refusal left.
func TestSpliceUsesEveryCaptureDirectory(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	const sampleRate = 16000
	const outSamples = sampleRate * 120

	root := t.TempDir()
	write := func(name string, segment SourceSegment, seconds float64, frequency int) string {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		writeToneSegment(t, dir, segment.AudioName, seconds, frequency)
		writeSidecar(t, dir, SourceSidecar{
			Format: SourceCaptureFormat, RoomToken: "room1", OwnerUserID: "alice",
			CallStartWallMS: segment.StartWallMS, CallEndWallMS: segment.StopWallMS,
			Segments: []SourceSegment{segment},
		})
		return dir
	}
	first := syntheticSegmentDelayed(0, 20, 1000, 0, 0)
	first.AudioName = "segment-0.webm"
	second := syntheticSegmentDelayed(60_000, 20, 1000, 0, 0)
	second.AudioName = "segment-0.webm"

	recorded := recordedMarker(outSamples, 0.25)
	original := append([]float32(nil), recorded...)
	out, report, err := SpliceSourceTrack(context.Background(), recorded,
		[]string{write("session-1", first, 20, 440), write("session-2", second, 20, 660)},
		testBase(), sampleRate, outSamples)
	if err != nil {
		t.Fatalf("SpliceSourceTrack: %v", err)
	}
	if report.Placed != 2 {
		t.Fatalf("placed %d segments across two captures, want 2", report.Placed)
	}
	for _, at := range []int{sampleRate * 5, sampleRate * 65} {
		if math.Float32bits(out[at]) == math.Float32bits(original[at]) {
			t.Fatalf("sample %d still holds the recorded value; one capture was ignored", at)
		}
	}
}
