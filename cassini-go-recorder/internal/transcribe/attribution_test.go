package transcribe

import (
	"math"
	"math/rand"
	"testing"
)

// tone builds a constant-amplitude buffer; amplitude is what the envelope
// measures, so a square-ish signal is enough and keeps the expectations exact.
func tone(n int, amp float32) []float32 {
	out := make([]float32, n)
	for i := range out {
		if i%2 == 0 {
			out[i] = amp
		} else {
			out[i] = -amp
		}
	}
	return out
}

func TestEnvelopeCalibratesToItsOwnFloor(t *testing.T) {
	const sr = 16000
	frame, hop := sr*attributionFrameMS/1000, sr*attributionHopMS/1000

	// 1 s of near-silence, then 1 s of speech-level signal.
	samples := append(tone(sr, 0.0005), tone(sr, 0.2)...)
	env := envelopeFromSamples("spk_a", samples, frame, hop, attributionHopMS)

	if len(env.FrameDB) == 0 {
		t.Fatal("expected frames")
	}
	if env.FloorDB >= env.SpeechDB {
		t.Fatalf("floor %.1f should sit below speech %.1f", env.FloorDB, env.SpeechDB)
	}
	// -0.0005 is about -66 dBFS, 0.2 about -14 dBFS.
	if env.FloorDB < -80 || env.FloorDB > -50 {
		t.Errorf("floor %.1f dB is not the quiet half", env.FloorDB)
	}
	if env.SpeechDB < -20 || env.SpeechDB > -8 {
		t.Errorf("speech %.1f dB is not the loud half", env.SpeechDB)
	}

	// A window inside the loud half must read well above this track's floor.
	level, ok := env.aboveFloor(1200, 1400)
	if !ok {
		t.Fatal("expected a measurable window")
	}
	if level < 30 {
		t.Errorf("loud window only %.1f dB above floor", level)
	}
}

// A hot mic and a quiet mic saying the same thing must score the same, because
// each is judged against its own floor. This is the property that stops a
// participant with high gain winning every word by default.
func TestAttributionIsRelativeToEachTracksOwnFloor(t *testing.T) {
	const sr = 16000
	frame, hop := sr*attributionFrameMS/1000, sr*attributionHopMS/1000

	loudRig := append(tone(sr, 0.002), tone(sr, 0.8)...)    // hot mic
	quietRig := append(tone(sr, 0.0002), tone(sr, 0.08)...) // 20 dB quieter throughout

	a := envelopeFromSamples("spk_loud", loudRig, frame, hop, attributionHopMS)
	b := envelopeFromSamples("spk_quiet", quietRig, frame, hop, attributionHopMS)

	la, _ := a.aboveFloor(1200, 1400)
	lb, _ := b.aboveFloor(1200, 1400)
	if math.Abs(la-lb) > 3 {
		t.Errorf("same relative level should score alike: loud rig %.1f, quiet rig %.1f", la, lb)
	}

	word := Word{Text: "hello", StartMS: 1200, EndMS: 1400}
	gap, ok := AttributionGapDB(word, "spk_quiet", []*SpeakerEnvelope{a, b})
	if !ok {
		t.Fatal("expected a gap")
	}
	if math.Abs(gap) > 3 {
		t.Errorf("neither track dominates; gap should be ~0, got %.1f dB", gap)
	}
}

func TestAttributionGapFindsTheLouderTrack(t *testing.T) {
	const sr = 16000
	frame, hop := sr*attributionFrameMS/1000, sr*attributionHopMS/1000

	// spk_a talks; spk_b is at its own floor the whole time (pure bleed case).
	talker := envelopeFromSamples("spk_a", append(tone(sr, 0.001), tone(sr, 0.5)...), frame, hop, attributionHopMS)
	silent := envelopeFromSamples("spk_b", append(tone(sr, 0.001), tone(sr, 0.001)...), frame, hop, attributionHopMS)

	word := Word{Text: "okay", StartMS: 1200, EndMS: 1400}
	gap, ok := AttributionGapDB(word, "spk_b", []*SpeakerEnvelope{talker, silent})
	if !ok {
		t.Fatal("expected a gap")
	}
	if gap < 20 {
		t.Errorf("a word on the silent track should show a large gap, got %.1f dB", gap)
	}

	// The same word attributed to the speaker who was actually talking must not.
	gap, ok = AttributionGapDB(word, "spk_a", []*SpeakerEnvelope{talker, silent})
	if !ok {
		t.Fatal("expected a gap")
	}
	if gap > 5 {
		t.Errorf("the real speaker should not be contradicted, got %.1f dB", gap)
	}
}

func TestEstimateCrosstalkThresholdDeclinesWithoutASecondMode(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	// Cleanly isolated tracks: gaps decay away from 0 as a tail, with a few
	// large-ish values from genuine overlap. There is no crosstalk mode, and
	// splitting the tail would delete real speech.
	gaps := make([]float64, 0, 650)
	for i := 0; i < 640; i++ {
		gaps = append(gaps, math.Abs(rng.NormFloat64()*2.0))
	}
	for i := 0; i < 14; i++ {
		gaps = append(gaps, 6+rng.Float64()*24)
	}
	if thr, ok := EstimateCrosstalkThresholdDB(gaps); ok {
		t.Errorf("expected no crosstalk population, got threshold %.1f dB", thr)
	}
}

func TestEstimateCrosstalkThresholdTracksTheRoom(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	for _, level := range []float64{34, 28, 22} {
		gaps := make([]float64, 0, 726)
		for i := 0; i < 586; i++ {
			gaps = append(gaps, math.Abs(rng.NormFloat64()*2.0))
		}
		for i := 0; i < 140; i++ {
			gaps = append(gaps, level+rng.NormFloat64())
		}
		thr, ok := EstimateCrosstalkThresholdDB(gaps)
		if !ok {
			t.Fatalf("level %.0f dB: expected a threshold", level)
		}
		// The cut belongs between the two modes, not inside either.
		if thr <= 6 || thr >= level-3 {
			t.Errorf("level %.0f dB: threshold %.1f dB is not between the modes", level, thr)
		}
	}
}

func TestEstimateCrosstalkThresholdIgnoresTinySamples(t *testing.T) {
	if _, ok := EstimateCrosstalkThresholdDB([]float64{1, 2, 40, 41}); ok {
		t.Error("four words is not a distribution")
	}
}

// twoSpeakerScene builds a 4-second pair of tracks with a real quiet/loud
// contrast, so each track's own floor is meaningful: both are near-silent for
// the first two seconds, then spk_a talks while spk_b stays at its floor. Words
// are placed inside spk_a's speaking half, so a word attributed to spk_b there
// is exactly the crosstalk case.
func twoSpeakerScene(t *testing.T) ([]*SpeakerEnvelope, []Segment) {
	t.Helper()
	const sr = 16000
	frame, hop := sr*attributionFrameMS/1000, sr*attributionHopMS/1000

	quietHalf := tone(sr*2, 0.0008)
	talker := append(append([]float32(nil), quietHalf...), tone(sr*2, 0.4)...)
	silent := append(append([]float32(nil), quietHalf...), tone(sr*2, 0.0008)...)

	envs := []*SpeakerEnvelope{
		envelopeFromSamples("spk_a", talker, frame, hop, attributionHopMS),
		envelopeFromSamples("spk_b", silent, frame, hop, attributionHopMS),
	}

	var aWords, bWords []Word
	for i := 0; i < 60; i++ {
		start := int64(2100 + i*30)
		aWords = append(aWords, Word{Text: "real", StartMS: start, EndMS: start + 25})
	}
	for i := 0; i < 30; i++ {
		start := int64(2100 + i*30)
		bWords = append(bWords, Word{Text: "ghost", StartMS: start, EndMS: start + 25})
	}
	segments := []Segment{
		{SpeakerID: "spk_a", StartMS: aWords[0].StartMS, EndMS: aWords[len(aWords)-1].EndMS,
			Text: "real", Words: aWords},
		{SpeakerID: "spk_b", StartMS: bWords[0].StartMS, EndMS: bWords[len(bWords)-1].EndMS,
			Text: "ghost", Words: bWords},
	}
	return envs, segments
}

func TestAnnotateAttributionMarksButKeepsByDefault(t *testing.T) {
	envs, segments := twoSpeakerScene(t)

	annotated, res := AnnotateAttribution(segments, envs, false)
	if res.WordsMeasured != 90 {
		t.Fatalf("expected all 90 words measured, got %d", res.WordsMeasured)
	}
	if !res.ThresholdFound {
		t.Fatal("a clean two-mode distribution should yield a threshold")
	}
	if res.Dropped != 0 {
		t.Errorf("annotate-only must not drop, dropped %d", res.Dropped)
	}
	if CountWords(annotated) != 90 {
		t.Errorf("annotate-only must keep every word, got %d", CountWords(annotated))
	}
	if res.Flagged == 0 {
		t.Error("expected the bleed words to be flagged")
	}
	var flaggedOnGhost, flaggedOnReal int
	for _, seg := range annotated {
		for _, w := range seg.Words {
			if !w.HasAttributionGap {
				t.Fatalf("word %q carries no gap", w.Text)
			}
			if w.LowConfidenceSpeaker {
				if seg.SpeakerID == "spk_b" {
					flaggedOnGhost++
				} else {
					flaggedOnReal++
				}
			}
		}
	}
	if flaggedOnGhost == 0 {
		t.Error("the bleed track's words should be flagged")
	}
	if flaggedOnReal != 0 {
		t.Errorf("the real speaker's words must not be flagged, got %d", flaggedOnReal)
	}
}

func TestAnnotateAttributionDropsOnlyWhenAsked(t *testing.T) {
	envs, segments := twoSpeakerScene(t)

	annotated, res := AnnotateAttribution(segments, envs, true)
	if res.Dropped == 0 {
		t.Fatal("dropping was requested but nothing was dropped")
	}
	if CountWords(annotated) != 90-res.Dropped {
		t.Errorf("word count %d does not match 90 - %d dropped", CountWords(annotated), res.Dropped)
	}
	for _, seg := range annotated {
		if len(seg.Words) == 0 {
			t.Error("an emptied segment should have been removed")
			continue
		}
		if seg.StartMS != seg.Words[0].StartMS || seg.EndMS != seg.Words[len(seg.Words)-1].EndMS {
			t.Errorf("segment bounds not rebuilt after dropping: %d-%d vs words %d-%d",
				seg.StartMS, seg.EndMS, seg.Words[0].StartMS, seg.Words[len(seg.Words)-1].EndMS)
		}
	}
	if err := ValidateSegments(annotated); err != nil {
		t.Errorf("dropped output must still be a valid transcript: %v", err)
	}
	// The words that survive must be the real speaker's, not the bleed.
	for _, seg := range annotated {
		if seg.SpeakerID == "spk_b" && len(seg.Words) > 0 {
			t.Errorf("bleed segment survived with %d words", len(seg.Words))
		}
	}
}

func TestAnnotateAttributionIsANoopWithoutEnvelopes(t *testing.T) {
	segments := []Segment{{SpeakerID: "spk_a", StartMS: 0, EndMS: 100, Text: "hi",
		Words: []Word{{Text: "hi", StartMS: 0, EndMS: 100}}}}
	out, res := AnnotateAttribution(segments, nil, false)
	if len(out) != 1 || CountWords(out) != 1 {
		t.Error("no envelopes must leave the transcript untouched")
	}
	if res.WordsMeasured != 0 || res.ThresholdFound {
		t.Error("nothing should be measured without envelopes")
	}
}
