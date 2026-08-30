package transcribe

import (
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
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

// twoSpeakerScene builds a 6-second pair of tracks with a real quiet/loud
// contrast on BOTH: each is near-silent outside its own speech, spk_a talks in
// seconds 2-4, and spk_b talks in seconds 4-6 — which establishes spk_b's own
// speech reference, without which none of their words would be flaggable.
// Three word populations cover the cases: spk_a's real words during their
// speech (owner at reference, never flaggable), ghost words on spk_b during
// spk_a's speech (owner quiet, rival loud — the crosstalk shape), and murmur
// words on spk_b during the opening silence (owner quiet, rival quiet — the
// flaggable lower cluster the estimator needs to see a bimodal distribution).
func twoSpeakerScene(t *testing.T) ([]*SpeakerEnvelope, []Segment) {
	t.Helper()
	const sr = 16000
	frame, hop := sr*attributionFrameMS/1000, sr*attributionHopMS/1000

	quiet := func(n int) []float32 { return tone(n, 0.0008) }
	talker := append(append(append([]float32(nil), quiet(sr*2)...), tone(sr*2, 0.4)...), quiet(sr*2)...)
	other := append(append([]float32(nil), quiet(sr*4)...), tone(sr*2, 0.3)...)

	envs := []*SpeakerEnvelope{
		envelopeFromSamples("spk_a", talker, frame, hop, attributionHopMS),
		envelopeFromSamples("spk_b", other, frame, hop, attributionHopMS),
	}

	var aWords, ghost, murmur []Word
	for i := 0; i < 60; i++ {
		start := int64(2100 + i*30)
		aWords = append(aWords, Word{Text: "real", StartMS: start, EndMS: start + 25})
		ghost = append(ghost, Word{Text: "ghost", StartMS: start, EndMS: start + 25})
		early := int64(100 + i*30)
		murmur = append(murmur, Word{Text: "murmur", StartMS: early, EndMS: early + 25})
	}
	segments := []Segment{
		{SpeakerID: "spk_a", StartMS: aWords[0].StartMS, EndMS: aWords[len(aWords)-1].EndMS,
			Text: "real", Words: aWords},
		{SpeakerID: "spk_b", StartMS: ghost[0].StartMS, EndMS: ghost[len(ghost)-1].EndMS,
			Text: "ghost", Words: ghost},
		{SpeakerID: "spk_b", StartMS: murmur[0].StartMS, EndMS: murmur[len(murmur)-1].EndMS,
			Text: "murmur", Words: murmur},
	}
	return envs, segments
}

func TestAnnotateAttributionMarksButKeepsByDefault(t *testing.T) {
	envs, segments := twoSpeakerScene(t)

	annotated, res := AnnotateAttribution(segments, envs, false)
	if res.WordsMeasured != 180 {
		t.Fatalf("expected all 180 words measured, got %d", res.WordsMeasured)
	}
	if !res.ThresholdFound {
		t.Fatal("a clean two-mode distribution should yield a threshold")
	}
	if res.Dropped != 0 {
		t.Errorf("annotate-only must not drop, dropped %d", res.Dropped)
	}
	if CountWords(annotated) != 180 {
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
	if flaggedOnGhost != 60 {
		t.Errorf("exactly the 60 ghost words during spk_a's speech should be flagged "+
			"(the murmur words carry no gap to speak of), got %d", flaggedOnGhost)
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
	if CountWords(annotated) != 180-res.Dropped {
		t.Errorf("word count %d does not match 180 - %d dropped", CountWords(annotated), res.Dropped)
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
	// Exactly the ghost words must be gone: spk_b's murmur words carry a
	// near-zero gap and must survive drop mode untouched.
	var ghostLeft, murmurLeft int
	for _, seg := range annotated {
		for _, w := range seg.Words {
			switch w.Text {
			case "ghost":
				ghostLeft++
			case "murmur":
				murmurLeft++
			}
		}
	}
	if ghostLeft != 0 {
		t.Errorf("%d ghost words survived drop mode", ghostLeft)
	}
	if murmurLeft != 60 {
		t.Errorf("spk_b's own quiet words must survive drop mode, kept %d of 60", murmurLeft)
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
	// Speaker a talks through the first half (30 dB above floor) and is quiet
	// after; b's track is at its floor throughout but b's reference says they
	// do speak at 30 somewhere, so b's words are flaggable: bleed words during
	// a's speech carry a 30 dB gap, hush words in the quiet tail carry ~0 —
	// the bimodal population drop mode needs.
	frames := make([]float64, 400)
	for i := range frames {
		if i < 200 {
			frames[i] = 30
		}
	}
	present := make([]bool, 400)
	for i := range present {
		present[i] = true
	}
	envs := []*SpeakerEnvelope{
		{SpeakerID: "a", FrameDB: frames, Present: present, FloorDB: 0, SpeechDB: 30, HopMS: attributionHopMS},
		{SpeakerID: "b", FrameDB: make([]float64, 400), Present: present, FloorDB: 0, SpeechDB: 30, HopMS: attributionHopMS},
	}
	real := make([]Word, 60)
	for i := range real {
		s := int64(100 + i*10)
		real[i] = Word{Text: "real", StartMS: s, EndMS: s + 5}
	}
	// A long word that outlasts every word starting after it.
	real[0].EndMS = 5000
	bleed := make([]Word, 60)
	for i := range bleed {
		s := int64(100 + i*10)
		bleed[i] = Word{Text: "bleed", StartMS: s, EndMS: s + 5}
	}
	hush := make([]Word, 60)
	for i := range hush {
		s := int64(3500 + i*10)
		hush[i] = Word{Text: "hush", StartMS: s, EndMS: s + 5}
	}
	segments := []Segment{
		{SpeakerID: "a", StartMS: 100, EndMS: 5000, Text: "real", Words: real},
		{SpeakerID: "b", StartMS: 100, EndMS: 700, Text: "bleed", Words: bleed},
		{SpeakerID: "b", StartMS: 3500, EndMS: 4105, Text: "hush", Words: hush},
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

// A noise floor belongs to somebody's microphone, not to a packet stream. Remux
// emits a fresh stream on every rotation or rejoin, and a short one can contain
// nothing but ongoing speech — calibrated alone its own 20th percentile IS
// speech, so its floor lands tens of dB too high, the real speaker measures as
// barely above it, and a quiet track carrying only their bleed wins. Measured
// before pooling: a -40 dBFS bleed beat the real 0.8-amplitude owner by 20 dB.
func TestRotatedStreamInheritsTheSpeakersEstablishedFloor(t *testing.T) {
	const sr = 16000
	frame, hop := sr*attributionFrameMS/1000, sr*attributionHopMS/1000

	// The first stream establishes speaker A's microphone floor.
	established := envelopeFromSamples("a", tone(sr, 0.002), frame, hop, attributionHopMS)
	// The rotation produces a short stream carrying only speech.
	rotated := envelopeFromSamples("a",
		append(make([]float32, sr), tone(sr, 0.8)...), frame, hop, attributionHopMS)
	// Speaker B carries only bleed during that word, above its own quiet floor.
	bleed := envelopeFromSamples("b",
		append(tone(sr, 0.001), tone(sr, 0.01)...), frame, hop, attributionHopMS)

	envelopes := []*SpeakerEnvelope{established, rotated, bleed}
	if rotated.FloorDB-established.FloorDB < 20 {
		t.Fatalf("fixture is not exercising the failure: rotated floor %.1f vs established %.1f",
			rotated.FloorDB, established.FloorDB)
	}

	calibrateByLogicalSpeaker(envelopes)

	if math.Abs(rotated.FloorDB-established.FloorDB) > 1 {
		t.Errorf("both of A's streams must share one floor: %.1f vs %.1f",
			rotated.FloorDB, established.FloorDB)
	}
	gap, ok := AttributionGapDB(Word{Text: "real", StartMS: 1200, EndMS: 1400}, "a", envelopes)
	if !ok {
		t.Fatal("expected attribution evidence")
	}
	if gap > 0 {
		t.Errorf("quiet bleed beat the real owner by %.1f dB across a rotation", gap)
	}
}

// Readable cleanup rewrites text onto interpolated slots. If the flag does not
// survive that rewrite, the summary filter silently becomes a no-op — and since
// readable cleanup and summarisation normally share one configured LLM, the
// no-op is the normal path, not an edge case.
func TestAttributionSurvivesReadableCleanup(t *testing.T) {
	original := []Segment{{SpeakerID: "spk_a", StartMS: 0, EndMS: 1000, Text: "one two",
		Words: []Word{
			{Text: "one", StartMS: 0, EndMS: 500},
			{Text: "two", StartMS: 500, EndMS: 1000,
				LowConfidenceSpeaker: true, HasAttributionGap: true, AttributionGapDB: 31.7},
		}}}
	readable := []Segment{{SpeakerID: "spk_a", StartMS: 0, EndMS: 1000, Text: "One, two."}}

	applied := ApplyReadableText(original, readable)
	var flagged int
	var gap float64
	for _, seg := range applied {
		for _, w := range seg.Words {
			if w.LowConfidenceSpeaker {
				flagged++
				gap = w.AttributionGapDB
			}
		}
	}
	if flagged == 0 {
		t.Fatal("readable cleanup dropped the flag; summary filtering would be a no-op")
	}
	if gap != 31.7 {
		t.Errorf("the measured gap should travel with the flag, got %.1f", gap)
	}
	if _, removed := WithoutLowConfidenceWords(applied); removed == 0 {
		t.Error("the summary filter must still find something to remove after cleanup")
	}
}

// A word the evidence did not contradict must not pick up a flag from a
// neighbour just because the text was rewritten around it.
func TestReadableCleanupDoesNotSpreadTheFlag(t *testing.T) {
	original := []Segment{{SpeakerID: "spk_a", StartMS: 0, EndMS: 1000, Text: "a b c d",
		Words: []Word{
			{Text: "a", StartMS: 0, EndMS: 250},
			{Text: "b", StartMS: 250, EndMS: 500},
			{Text: "c", StartMS: 500, EndMS: 750},
			{Text: "d", StartMS: 750, EndMS: 1000, LowConfidenceSpeaker: true},
		}}}
	readable := []Segment{{SpeakerID: "spk_a", StartMS: 0, EndMS: 1000, Text: "a b c d"}}
	applied := ApplyReadableText(original, readable)
	var flagged int
	for _, w := range applied[0].Words {
		if w.LowConfidenceSpeaker {
			flagged++
		}
	}
	if flagged != 1 {
		t.Errorf("exactly the overlapping word should be flagged, got %d of %d",
			flagged, len(applied[0].Words))
	}
}

// Older transcripts carry segment text with no word list. There is nothing to
// filter and nothing to judge them on, so they must not vanish because some
// other segment was flagged.
func TestFilteringKeepsWordlessLegacySegments(t *testing.T) {
	segments := []Segment{
		{SpeakerID: "spk_a", StartMS: 0, EndMS: 100, Text: "legacy text, no word list"},
		{SpeakerID: "spk_b", StartMS: 0, EndMS: 100, Text: "ghost",
			Words: []Word{{Text: "ghost", StartMS: 0, EndMS: 100, LowConfidenceSpeaker: true}}},
	}
	out, removed := WithoutLowConfidenceWords(segments)
	if removed != 1 {
		t.Fatalf("expected the flagged word removed, got %d", removed)
	}
	var keptLegacy bool
	for _, seg := range out {
		if seg.SpeakerID == "spk_a" && seg.Text == "legacy text, no word list" {
			keptLegacy = true
		}
	}
	if !keptLegacy {
		t.Error("the wordless legacy segment was dropped")
	}
}

// Pooling every frame and taking one percentile is duration-weighted: five
// seconds of speech-only rotation swamps one second of the stream that
// established the real floor, and the pooled percentile lands in speech again.
// The equal-duration case passes either way, which is why it has to be tested
// unequal.
func TestCalibrationSurvivesALongSpeechOnlyRotation(t *testing.T) {
	const sr = 16000
	frame, hop := sr*attributionFrameMS/1000, sr*attributionHopMS/1000

	// One second establishes the microphone's floor; five seconds of rotation
	// carry nothing but speech.
	established := envelopeFromSamples("a", tone(sr, 0.002), frame, hop, attributionHopMS)
	rotated := envelopeFromSamples("a", tone(sr*5, 0.8), frame, hop, attributionHopMS)
	bleed := envelopeFromSamples("b",
		append(tone(sr, 0.001), tone(sr*5, 0.01)...), frame, hop, attributionHopMS)

	envelopes := []*SpeakerEnvelope{established, rotated, bleed}
	calibrateByLogicalSpeaker(envelopes)

	if math.Abs(established.FloorDB-rotated.FloorDB) > 1 {
		t.Errorf("both of A's streams must share one floor: %.1f vs %.1f",
			established.FloorDB, rotated.FloorDB)
	}
	if established.FloorDB > -40 {
		t.Errorf("the speaker's floor was dragged into speech: %.1f dB", established.FloorDB)
	}
	gap, ok := AttributionGapDB(Word{Text: "real", StartMS: 2000, EndMS: 2200}, "a", envelopes)
	if !ok {
		t.Fatal("expected attribution evidence")
	}
	if gap > 0 {
		t.Errorf("bleed beat the real owner by %.1f dB across a long rotation", gap)
	}
}

// A stream too short to characterise anything must not drag a speaker's floor
// down just because it happened to catch a quiet instant.
func TestCalibrationIgnoresStreamsTooShortToCharacterise(t *testing.T) {
	const sr = 16000
	frame, hop := sr*attributionFrameMS/1000, sr*attributionHopMS/1000
	real := envelopeFromSamples("a", append(tone(sr, 0.02), tone(sr, 0.8)...), frame, hop, attributionHopMS)
	before := real.FloorDB
	// A few milliseconds of near-silence: fewer frames than the guard allows.
	sliver := envelopeFromSamples("a", tone(600, 0.000001), frame, hop, attributionHopMS)

	calibrateByLogicalSpeaker([]*SpeakerEnvelope{real, sliver})
	if math.Abs(real.FloorDB-before) > 1 {
		t.Errorf("a sliver of a stream moved the speaker's floor from %.1f to %.1f",
			before, real.FloorDB)
	}
}

// Readable cleanup routinely changes the word count. Mapping a cleaned word to
// ANY overlapping source word lets one contradicted word flag a legitimate
// neighbour, deleting text from the summary that the evidence never questioned.
//
// The property to hold is not a word count — cleanup that expands one word into
// two may legitimately flag both — but containment: a flag must never reach a
// stretch of time no source word was contradicted in.
func TestReadableCleanupDoesNotSpreadBeyondTheContradictedSpan(t *testing.T) {
	original := []Segment{{SpeakerID: "spk_a", StartMS: 0, EndMS: 1000, Text: "a b c d",
		Words: []Word{
			{Text: "a", StartMS: 0, EndMS: 250},
			{Text: "b", StartMS: 250, EndMS: 500},
			{Text: "c", StartMS: 500, EndMS: 750},
			{Text: "d", StartMS: 750, EndMS: 1000, LowConfidenceSpeaker: true},
		}}}

	for _, cleaned := range []string{
		"a b c extra d", // a word inserted before the contradicted one
		"a b c d e f g", // substantial growth
		"a d",           // shrink
		"a b c d",       // unchanged
	} {
		readable := []Segment{{SpeakerID: "spk_a", StartMS: 0, EndMS: 1000, Text: cleaned}}
		applied := ApplyReadableText(original, readable)
		for _, w := range applied[0].Words {
			if !w.LowConfidenceSpeaker {
				continue
			}
			// The flagged source word is "d", 750-1000 ms. A cleaned word may
			// only inherit the flag if it genuinely overlaps that span.
			if w.EndMS <= 750 || w.StartMS >= 1000 {
				t.Errorf("cleanup %q: flagged %q at %d-%d, outside the contradicted 750-1000 ms",
					cleaned, w.Text, w.StartMS, w.EndMS)
			}
		}
	}
}

// The case the review reported: one inserted word must not cost a legitimate
// neighbour its place in the summary.
func TestReadableCleanupInsertionFlagsExactlyTheContradictedWord(t *testing.T) {
	original := []Segment{{SpeakerID: "spk_a", StartMS: 0, EndMS: 1000, Text: "a b c d",
		Words: []Word{
			{Text: "a", StartMS: 0, EndMS: 250},
			{Text: "b", StartMS: 250, EndMS: 500},
			{Text: "c", StartMS: 500, EndMS: 750},
			{Text: "d", StartMS: 750, EndMS: 1000, LowConfidenceSpeaker: true},
		}}}
	readable := []Segment{{SpeakerID: "spk_a", StartMS: 0, EndMS: 1000, Text: "a b c extra d"}}
	applied := ApplyReadableText(original, readable)
	var flagged int
	for _, w := range applied[0].Words {
		if w.LowConfidenceSpeaker {
			flagged++
		}
	}
	if flagged != 1 {
		t.Errorf("one contradicted source word flagged %d cleaned words", flagged)
	}
}

// signNoise builds a constant-magnitude, random-sign buffer. Its RMS over ANY
// non-empty subset of samples is exactly amp, which makes floor expectations
// exact: a correct partial-frame RMS gives identical floors however the audio
// is sliced, while whole-frame averaging dilutes partially covered frames.
func signNoise(n int, amp float32, seed int64) []float32 {
	rng := rand.New(rand.NewSource(seed))
	out := make([]float32, n)
	for i := range out {
		if rng.Intn(2) == 0 {
			out[i] = amp
		} else {
			out[i] = -amp
		}
	}
	return out
}

// A DTX-shaped track materialises as short packets of real audio inside long
// runs of exact-zero timeline padding, and a packet routinely starts and ends
// mid-frame. A frame's RMS must be computed over the samples that are actually
// present: averaged over the whole frame, a 20 ms packet inside a 32 ms frame
// reads up to ~6 dB quiet, every present frame during silence is partial, and
// the track's 20th-percentile floor lands well below the microphone's real
// floor — which min-pooling then spreads to the whole speaker.
func TestPartialFramesAreNotDilutedByPadding(t *testing.T) {
	const sr = 16000
	frame, hop := sr*attributionFrameMS/1000, sr*attributionHopMS/1000
	const amp = 0.0006

	// The same microphone noise, continuous vs DTX-shaped: one 20 ms packet
	// (320 samples) every 400 ms, each starting mid-frame (offset 137 is not a
	// multiple of the 256-sample hop, so every packet boundary cuts frames).
	continuous := envelopeFromSamples("cont", signNoise(sr*4, amp, 1), frame, hop, attributionHopMS)
	dtx := make([]float32, sr*12)
	for start := 137; start+320 <= len(dtx); start += 6400 {
		copy(dtx[start:start+320], signNoise(320, amp, int64(start)))
	}
	sparse := envelopeFromSamples("dtx", dtx, frame, hop, attributionHopMS)

	var present int
	for _, ok := range sparse.Present {
		if ok {
			present++
		}
	}
	if present < 10 {
		t.Fatalf("fixture broke: only %d present frames on the DTX track", present)
	}
	if got := math.Abs(continuous.FloorDB - sparse.FloorDB); got > 1 {
		t.Errorf("identical noise must calibrate alike however it is packetised: "+
			"continuous floor %.1f dB, DTX floor %.1f dB (Δ %.1f dB)",
			continuous.FloorDB, sparse.FloorDB, got)
	}
}

// A handful of words measured while a rival was shouting sit far above the
// genuine crosstalk mode. Seeded at the global max, the upper k-means centre
// belonged to those outliers, the mode was absorbed into the lower cluster,
// and a populated, tight, well-separated crosstalk mode was either cut above
// (flagging only the outliers) or declined as too spread out. The estimate
// must land in the valley under the mode regardless.
func TestEstimateCrosstalkThresholdSurvivesFarOutliers(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	gaps := make([]float64, 0, 732)
	for i := 0; i < 586; i++ {
		gaps = append(gaps, math.Abs(rng.NormFloat64()*2.0))
	}
	for i := 0; i < 140; i++ {
		gaps = append(gaps, 22+rng.NormFloat64())
	}
	for i := 0; i < 6; i++ {
		gaps = append(gaps, 55+rng.NormFloat64())
	}
	thr, ok := EstimateCrosstalkThresholdDB(gaps)
	if !ok {
		t.Fatal("a tight, populated, well-separated crosstalk mode must be found " +
			"even with a few far outliers above it")
	}
	// The valley sits between the lower mode (~0) and the crosstalk mode (22);
	// a threshold at or above the mode means the outliers captured the fit.
	if thr <= 6 || thr >= 19 {
		t.Errorf("threshold %.1f dB is not in the valley below the 22 dB mode", thr)
	}
}

// The tightness gate is what keeps the estimator honest when the "upper
// cluster" is two genuine sub-modes (say overlap plus crosstalk from different
// rooms): the valley between them is empty and they are far from the lower
// mode, so only stddev(upper) says "this is not one crosstalk population".
// This distribution is accepted the moment that gate is removed.
func TestEstimateCrosstalkThresholdRejectsABimodalUpperCluster(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	gaps := make([]float64, 0, 640)
	for i := 0; i < 600; i++ {
		gaps = append(gaps, math.Abs(rng.NormFloat64()*1.5))
	}
	for i := 0; i < 20; i++ {
		gaps = append(gaps, 20+rng.NormFloat64()*0.5)
	}
	for i := 0; i < 20; i++ {
		gaps = append(gaps, 35+rng.NormFloat64()*0.5)
	}
	if thr, ok := EstimateCrosstalkThresholdDB(gaps); ok {
		t.Errorf("a bimodal upper cluster is not one crosstalk mode; got threshold %.1f dB", thr)
	}
}

// The separation gate is what declines a tight mode that sits too close to the
// lower mode for the cut to be trustworthy: here the valley near the forced
// 8 dB minimum is essentially empty and the upper mode is tight, so only the
// centre separation says no. This distribution is accepted the moment that
// gate is removed.
func TestEstimateCrosstalkThresholdRequiresSeparation(t *testing.T) {
	rng := rand.New(rand.NewSource(9))
	gaps := make([]float64, 0, 740)
	for i := 0; i < 600; i++ {
		gaps = append(gaps, math.Abs(rng.NormFloat64()*1.5))
	}
	for i := 0; i < 140; i++ {
		gaps = append(gaps, 12+rng.NormFloat64()*0.5)
	}
	if thr, ok := EstimateCrosstalkThresholdDB(gaps); ok {
		t.Errorf("a mode only ~10 dB above the noise cluster is too close to cut; got threshold %.1f dB", thr)
	}
}

// The envelope stage must not materialise a track's PCM: the streaming decode
// has to produce, frame for frame, exactly what the batch decode produces,
// while allocating O(frames), not O(samples).
func TestStreamingEnvelopeMatchesBatchDecode(t *testing.T) {
	requireFFMediaTools(t)
	mkv := filepath.Join("..", "..", "..", "harness", "media", "parakeet-smoke.mkv")
	if _, err := os.Stat(mkv); err != nil {
		t.Skipf("public smoke fixture not present: %v", err)
	}
	const sr = 16000
	frame, hop := sr*attributionFrameMS/1000, sr*attributionHopMS/1000
	stream := AudioStream{Index: 0, SpeakerID: "smoke", SpeakerLabel: "smoke"}

	samples, err := ExtractSpeakerFloats(mkv, stream)
	if err != nil {
		t.Fatalf("batch decode: %v", err)
	}
	want := envelopeFromSamples("smoke", samples, frame, hop, attributionHopMS)
	if len(want.FrameDB) == 0 {
		t.Fatal("fixture decoded to no frames")
	}

	envs, err := BuildSpeakerEnvelopes(mkv, []AudioStream{stream}, sr, nil)
	if err != nil {
		t.Fatalf("streaming envelope: %v", err)
	}
	got := envs[0]
	if len(got.FrameDB) != len(want.FrameDB) {
		t.Fatalf("frame count differs: streaming %d, batch %d", len(got.FrameDB), len(want.FrameDB))
	}
	for i := range got.FrameDB {
		if math.Abs(got.FrameDB[i]-want.FrameDB[i]) > 1e-6 {
			t.Fatalf("frame %d differs: streaming %.9f dB, batch %.9f dB", i, got.FrameDB[i], want.FrameDB[i])
		}
		if got.Present[i] != want.Present[i] {
			t.Fatalf("frame %d presence differs", i)
		}
	}
	if math.Abs(got.FloorDB-want.FloorDB) > 1e-6 || math.Abs(got.SpeechDB-want.SpeechDB) > 1e-6 {
		t.Fatalf("levels differ: streaming floor %.6f speech %.6f, batch floor %.6f speech %.6f",
			got.FloorDB, got.SpeechDB, want.FloorDB, want.SpeechDB)
	}

	// The first call above warmed every code path; now measure. Materialising
	// the track costs at least len(samples)*4 bytes in one slice; the
	// streaming path's whole allocation budget must sit far below that.
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if _, err := BuildSpeakerEnvelopes(mkv, []AudioStream{stream}, sr, nil); err != nil {
		t.Fatalf("streaming envelope: %v", err)
	}
	runtime.ReadMemStats(&after)
	delta := after.TotalAlloc - before.TotalAlloc
	sampleBytes := uint64(len(samples)) * 4
	if delta > sampleBytes/2 {
		t.Errorf("streaming envelope allocated %d bytes for a track whose PCM is %d bytes; "+
			"the track is being materialised", delta, sampleBytes)
	}
}

// asymmetricScene builds the case the completeness critic measured end-to-end:
// genuine double-talk under microphone-SNR asymmetry. The interrupted victim
// speaks at their own reference (27 dB above their floor — a laptop mic's SNR)
// while the interrupter's close mic runs at 38, so the victim's REAL words
// measure a positive gap in the same +10..+15 dB band real ghost runs occupy.
// A third quiet participant carries the actual ghosts: bleed words while the
// hot rig is at a moderate level (region B), and quiet-quiet words (region C)
// that give the estimator its lower cluster. hotB sets the hot rig's region-B
// level so callers can spread the ghost band.
func asymmetricScene(hotA func(i int) float64, hotB func(i int) float64) ([]*SpeakerEnvelope, []Segment) {
	const n = 400
	mk := func(id string, speech float64, level func(i int) float64) *SpeakerEnvelope {
		fr := make([]float64, n)
		pr := make([]bool, n)
		for i := range fr {
			fr[i] = level(i)
			pr[i] = true
		}
		return &SpeakerEnvelope{SpeakerID: id, FrameDB: fr, Present: pr,
			FloorDB: 0, SpeechDB: speech, HopMS: attributionHopMS}
	}
	victim := mk("victim", 27, func(i int) float64 {
		if i < 100 {
			return 27
		}
		return 0
	})
	hot := mk("hot", 38, func(i int) float64 {
		if i < 100 {
			return hotA(i)
		}
		if i < 200 {
			return hotB(i)
		}
		return 0
	})
	quietguy := mk("quietguy", 30, func(i int) float64 { return 0 })

	words := func(fromMS int64, text string) []Word {
		out := make([]Word, 60)
		for i := range out {
			s := fromMS + int64(i)*25
			out[i] = Word{Text: text, StartMS: s, EndMS: s + 20}
		}
		return out
	}
	vWords := words(40, "real")    // region A: frames 0-99 (0-1600 ms)
	gWords := words(1660, "ghost") // region B: frames 100-199
	cWords := words(3260, "hush")  // region C: everyone quiet
	segments := []Segment{
		{SpeakerID: "victim", StartMS: vWords[0].StartMS, EndMS: vWords[len(vWords)-1].EndMS,
			Text: "real", Words: vWords},
		{SpeakerID: "quietguy", StartMS: gWords[0].StartMS, EndMS: gWords[len(gWords)-1].EndMS,
			Text: "ghost", Words: gWords},
		{SpeakerID: "quietguy", StartMS: cWords[0].StartMS, EndMS: cWords[len(cWords)-1].EndMS,
			Text: "hush", Words: cWords},
	}
	return []*SpeakerEnvelope{victim, hot, quietguy}, segments
}

// Genuine double-talk words under SNR asymmetry sit in the ghost band by gap
// alone; the quiet-owner gate is what keeps them unflaggable — the victim was
// speaking at their own reference level. Removing the gate makes this fail one
// of two ways: the victim's words get flagged (flag filter), or the
// double-talk contamination lands in the estimator's valley and the true
// ghosts go unflagged (estimator input).
func TestDoubleTalkWithAsymmetricMicsIsNotFlaggable(t *testing.T) {
	flatA := func(int) float64 { return 38 }
	flatB := func(int) float64 { return 14 }

	envs, segments := asymmetricScene(flatA, flatB)
	annotated, res := AnnotateAttribution(segments, envs, false)
	if !res.ThresholdFound {
		t.Fatal("the quiet-owner subset is cleanly bimodal; the estimator must find a threshold")
	}
	var victimFlagged, ghostFlagged int
	victimGap := math.Inf(-1)
	for _, seg := range annotated {
		for _, w := range seg.Words {
			switch w.Text {
			case "real":
				if !w.HasAttributionGap {
					t.Fatal("the victim's words must stay measured: the gap is ranking evidence")
				}
				victimGap = w.AttributionGapDB
				if w.LowConfidenceSpeaker {
					victimFlagged++
				}
			case "ghost":
				if w.LowConfidenceSpeaker {
					ghostFlagged++
				}
			}
		}
	}
	// The fixture must keep the victim in the dangerous band, above the found
	// threshold — otherwise the gate is not what protects them.
	if victimGap < 10 || victimGap > 15 {
		t.Fatalf("fixture drifted: double-talk gap should sit at +10..+15 dB, got %.1f", victimGap)
	}
	if victimGap < res.ThresholdDB {
		t.Fatalf("fixture drifted: victim gap %.1f dB is below the threshold %.1f dB, "+
			"so the gate is not being exercised", victimGap, res.ThresholdDB)
	}
	if victimFlagged != 0 {
		t.Errorf("genuine double-talk words were flagged (%d of 60): a speaker at their own "+
			"reference level must never be", victimFlagged)
	}
	if ghostFlagged < 50 {
		t.Errorf("the true ghosts should still be flagged, got %d of 60", ghostFlagged)
	}

	// Drop mode must keep every one of the victim's words.
	envs2, segments2 := asymmetricScene(flatA, flatB)
	dropped, resDrop := AnnotateAttribution(segments2, envs2, true)
	if resDrop.Dropped == 0 {
		t.Fatal("drop mode should have removed the ghosts")
	}
	var victimKept int
	for _, seg := range dropped {
		for _, w := range seg.Words {
			if w.Text == "real" {
				victimKept++
			}
		}
	}
	if victimKept != 60 {
		t.Errorf("drop mode deleted real double-talk words: kept %d of 60", victimKept)
	}
}

// On real speech the all-words positive-gap population is intrinsically broad:
// double-talk words track voice dynamics and smear from ~+9 to ~+17 dB, the
// upper cluster fails the tightness/valley gates, and the estimator declines —
// which is why flagging only ever fired on synthetic constant-gap fixtures.
// Restricted to quiet-owner words the double-talk contamination is gone and
// the same meeting yields a clean bimodal split.
func TestQuietOwnerSubsetRescuesABroadGapPopulation(t *testing.T) {
	// The hot rig's level varies with speech dynamics during the double-talk,
	// spreading the victim's gaps over +9..+17 dB; the ghosts sit at +30.
	dynamicA := func(i int) float64 { return 36 + float64((i/12)%9) }
	flatB := func(int) float64 { return 30 }

	envs, segments := asymmetricScene(dynamicA, flatB)
	annotated, res := AnnotateAttribution(segments, envs, false)

	// The unrestricted population — what the estimator used to be fed — must
	// decline: that is finding 2, nothing was ever flagged on real speech.
	var all []float64
	for _, seg := range annotated {
		for _, w := range seg.Words {
			if w.HasAttributionGap {
				all = append(all, w.AttributionGapDB)
			}
		}
	}
	if thr, ok := EstimateCrosstalkThresholdDB(all); ok {
		t.Errorf("the unrestricted gap population is broad and must decline, got %.1f dB — "+
			"the quiet-owner subset would be no rescue", thr)
	}
	if !res.ThresholdFound {
		t.Fatal("restricted to quiet-owner words the population separates cleanly; " +
			"the estimator must find the mode")
	}
	var victimFlagged, ghostFlagged int
	for _, seg := range annotated {
		for _, w := range seg.Words {
			if !w.LowConfidenceSpeaker {
				continue
			}
			switch w.Text {
			case "real":
				victimFlagged++
			case "ghost":
				ghostFlagged++
			}
		}
	}
	if victimFlagged != 0 {
		t.Errorf("broad double-talk words were flagged (%d)", victimFlagged)
	}
	if ghostFlagged < 50 {
		t.Errorf("the ghost mode should be flagged once rescued, got %d of 60", ghostFlagged)
	}
}
