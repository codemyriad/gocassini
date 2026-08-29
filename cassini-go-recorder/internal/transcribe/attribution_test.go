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

// A participant who joins late has their pre-join span materialised as exact
// digital silence when the track is put on the shared meeting timeline. That
// padding is not their microphone's noise floor. Including it drove the 20th
// percentile to the log epsilon and made the track score ~238 dB above "its own
// floor" against ~52 dB for an identical continuous track — which would have won
// that participant every contested word in the meeting.
func TestLateJoinPaddingDoesNotPoisonTheFloor(t *testing.T) {
	const sr = 16000
	frame, hop := sr*attributionFrameMS/1000, sr*attributionHopMS/1000

	// Identical microphone dynamics: 1 s at the mic's noise floor, 1 s of speech.
	realAudio := append(tone(sr, 0.002), tone(sr, 0.8)...)
	continuous := envelopeFromSamples("cont", realAudio, frame, hop, attributionHopMS)
	// The late joiner is the same capture, preceded by 2 s of timeline padding.
	late := envelopeFromSamples("late",
		append(make([]float32, sr*2), realAudio...), frame, hop, attributionHopMS)

	if got := continuous.FloorDB - late.FloorDB; math.Abs(got) > 3 {
		t.Errorf("same mic should calibrate alike: continuous floor %.1f, late-join floor %.1f",
			continuous.FloorDB, late.FloorDB)
	}
	lc, okc := continuous.aboveFloor(1200, 1400)
	ll, okl := late.aboveFloor(3200, 3400) // same speech, shifted by the padding
	if !okc || !okl {
		t.Fatal("expected both windows to be measurable")
	}
	if math.Abs(ll-lc) > 3 {
		t.Errorf("identical speech must score alike: continuous %.1f dB, late-join %.1f dB", lc, ll)
	}
}

// Before a participant joins there is nothing of theirs to hear, so they must
// not compete for a word — and a word attributed to them there cannot be
// measured at all.
func TestAbsentTrackIsNotARival(t *testing.T) {
	const sr = 16000
	frame, hop := sr*attributionFrameMS/1000, sr*attributionHopMS/1000
	early := envelopeFromSamples("early", append(tone(sr, 0.002), tone(sr, 0.8)...), frame, hop, attributionHopMS)
	late := envelopeFromSamples("late", append(make([]float32, sr*2), tone(sr, 0.8)...), frame, hop, attributionHopMS)

	if _, ok := late.aboveFloor(1200, 1400); ok {
		t.Error("a track that had not joined yet must not report a level")
	}
	// A word on the early speaker during the late speaker's absence is
	// uncontested: the only measurable track is its own.
	gap, ok := AttributionGapDB(Word{Text: "hi", StartMS: 1200, EndMS: 1400}, "early",
		[]*SpeakerEnvelope{early, late})
	if ok {
		t.Errorf("with no rival present there is nothing to compare against, got gap %.1f", gap)
	}
}

func TestTrackWithNoCapturedAudioNeverClaimsAWord(t *testing.T) {
	const sr = 16000
	frame, hop := sr*attributionFrameMS/1000, sr*attributionHopMS/1000
	silent := envelopeFromSamples("never_spoke", make([]float32, sr*3), frame, hop, attributionHopMS)
	if _, ok := silent.aboveFloor(1000, 1100); ok {
		t.Error("an all-padding track must decline every window")
	}
}

// One participant can own several MKV streams: remux emits one per rotated or
// rejoined packet stream and they share the participant-derived speaker id.
// Scoring must not depend on which of those streams is iterated last.
func TestDuplicateSpeakerStreamsAreConsolidated(t *testing.T) {
	loud := make([]float64, 300)
	for i := range loud {
		loud[i] = 40
	}
	quiet := make([]float64, 300)
	present := make([]bool, 300)
	for i := range present {
		present[i] = true
	}
	active := &SpeakerEnvelope{SpeakerID: "spk_a", FrameDB: loud, Present: present, FloorDB: 0, HopMS: attributionHopMS}
	rotated := &SpeakerEnvelope{SpeakerID: "spk_a", FrameDB: quiet, Present: present, FloorDB: 0, HopMS: attributionHopMS}
	rival := &SpeakerEnvelope{SpeakerID: "spk_b", FrameDB: loud, Present: present, FloorDB: 0, HopMS: attributionHopMS}

	word := Word{Text: "hi", StartMS: 1000, EndMS: 1100}
	first, ok1 := AttributionGapDB(word, "spk_a", []*SpeakerEnvelope{active, rotated, rival})
	second, ok2 := AttributionGapDB(word, "spk_a", []*SpeakerEnvelope{rotated, active, rival})
	if !ok1 || !ok2 {
		t.Fatal("expected both orderings to produce a gap")
	}
	if first != second {
		t.Errorf("attribution must not depend on stream order: %.1f vs %.1f", first, second)
	}
	if first != 0 {
		t.Errorf("the speaker's loudest stream should tie the equally loud rival, got %.1f", first)
	}
}

// Words overlap, so a segment's envelope is the running min/max over the words
// that survive — not the first word's start and the last word's end. Getting
// that wrong reintroduces the invalid envelope fixed in #216.
func TestDropModeKeepsSegmentEnvelopeAroundOverlappingWords(t *testing.T) {
	frames := make([]float64, 400)
	for i := range frames {
		frames[i] = 30
	}
	present := make([]bool, 400)
	for i := range present {
		present[i] = true
	}
	envs := []*SpeakerEnvelope{
		{SpeakerID: "a", FrameDB: frames, Present: present, FloorDB: 0, HopMS: attributionHopMS},
		{SpeakerID: "b", FrameDB: make([]float64, 400), Present: present, FloorDB: 0, HopMS: attributionHopMS},
	}
	real := make([]Word, 60)
	for i := range real {
		s := int64(100 + i*10)
		real[i] = Word{Text: "real", StartMS: s, EndMS: s + 5}
	}
	// A long word that outlasts every word starting after it.
	real[0].EndMS = 5000
	bleed := make([]Word, 30)
	for i := range bleed {
		s := int64(100 + i*10)
		bleed[i] = Word{Text: "bleed", StartMS: s, EndMS: s + 5}
	}
	segments := []Segment{
		{SpeakerID: "a", StartMS: 100, EndMS: 5000, Text: "real", Words: real},
		{SpeakerID: "b", StartMS: 100, EndMS: 400, Text: "bleed", Words: bleed},
	}

	out, res := AnnotateAttribution(segments, envs, true)
	if !res.ThresholdFound || res.Dropped == 0 {
		t.Fatalf("fixture did not exercise drop mode: %+v", res)
	}
	if err := ValidateSegments(out); err != nil {
		t.Fatalf("drop mode produced an invalid transcript: %v", err)
	}
	for _, seg := range out {
		for _, w := range seg.Words {
			if w.StartMS < seg.StartMS || w.EndMS > seg.EndMS {
				t.Errorf("word %d-%d escapes segment %d-%d", w.StartMS, w.EndMS, seg.StartMS, seg.EndMS)
			}
		}
	}
}

func TestWithoutLowConfidenceWordsIsANoopWhenNothingIsFlagged(t *testing.T) {
	segments := []Segment{{SpeakerID: "spk_a", StartMS: 0, EndMS: 200, Text: "a b",
		Words: []Word{{Text: "a", StartMS: 0, EndMS: 100}, {Text: "b", StartMS: 100, EndMS: 200}}}}
	out, removed := WithoutLowConfidenceWords(segments)
	if removed != 0 || CountWords(out) != 2 {
		t.Errorf("unflagged transcript must pass through untouched, removed=%d words=%d",
			removed, CountWords(out))
	}
}

func TestWithoutLowConfidenceWordsRebuildsBoundsAndDropsEmptySegments(t *testing.T) {
	segments := []Segment{
		{SpeakerID: "spk_a", StartMS: 0, EndMS: 900, Text: "keep long drop",
			Words: []Word{
				{Text: "keep", StartMS: 0, EndMS: 100},
				{Text: "long", StartMS: 50, EndMS: 900}, // overlaps and outlasts
				{Text: "drop", StartMS: 800, EndMS: 850, LowConfidenceSpeaker: true},
			}},
		{SpeakerID: "spk_b", StartMS: 0, EndMS: 100, Text: "ghost",
			Words: []Word{{Text: "ghost", StartMS: 0, EndMS: 100, LowConfidenceSpeaker: true}}},
	}
	out, removed := WithoutLowConfidenceWords(segments)
	if removed != 2 {
		t.Fatalf("expected both flagged words removed, got %d", removed)
	}
	if len(out) != 1 {
		t.Fatalf("the all-flagged segment should be dropped, got %d segments", len(out))
	}
	if out[0].StartMS != 0 || out[0].EndMS != 900 {
		t.Errorf("bounds must span the retained overlapping word, got %d-%d",
			out[0].StartMS, out[0].EndMS)
	}
	if out[0].Text != "keep long" {
		t.Errorf("text should be rebuilt from retained words, got %q", out[0].Text)
	}
	if err := ValidateSegments(out); err != nil {
		t.Errorf("filtered transcript must stay valid: %v", err)
	}
}

// The canonical transcript must never lose a word to this: only the summary
// input is filtered.
func TestFilteringDoesNotMutateTheCanonicalSegments(t *testing.T) {
	segments := []Segment{{SpeakerID: "spk_a", StartMS: 0, EndMS: 200, Text: "a b",
		Words: []Word{
			{Text: "a", StartMS: 0, EndMS: 100},
			{Text: "b", StartMS: 100, EndMS: 200, LowConfidenceSpeaker: true},
		}}}
	before := CountWords(segments)
	if _, removed := WithoutLowConfidenceWords(segments); removed != 1 {
		t.Fatalf("expected one removal, got %d", removed)
	}
	if after := CountWords(segments); after != before {
		t.Errorf("canonical transcript was mutated: %d words became %d", before, after)
	}
}
