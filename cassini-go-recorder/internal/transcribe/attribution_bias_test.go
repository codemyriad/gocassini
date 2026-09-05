package transcribe

// Does measuring one speaker on their spliced render move everybody else's
// crosstalk verdict?
//
// It is a fair question to ask of attribution.go's change, because crosstalk is
// a RELATIVE judgement: a word is a bleed candidate because some other track
// sat further above its own floor than the owning track sat above its own. Once
// one speaker's envelope comes from their own continuous capture while the rest
// come from what the SFU delivered, that speaker's floor is read off a
// different kind of silence, and every level on their track measures further
// above it.
//
// These tests answer the question with numbers rather than with an argument.
// The scene below is a three-speaker meeting on a synthetic level timeline —
// turns, bleed between them, and pauses where nobody speaks — with words of
// three known kinds: words their owner really said, ghosts riding somebody
// else's voice, and hallucinations on the noise between turns. One speaker's
// floor is then pushed down by a swept offset, which is exactly what filling
// the SFU's silences with the participant's own microphone does, and the
// meeting's whole crosstalk decision is re-read at each step.
//
// The measurement (see the sweep in the tests): uncorrected, an offset of 10 dB
// is enough to smear the crosstalk mode until EstimateCrosstalkThresholdDB
// declines — 120 flagged crosstalk words become 0, for every speaker, silently.
// With capIngestedDynamicRange the verdict is identical at every offset from 0
// to 30 dB: threshold 15.0 dB, the same 120 words flagged, the same 0 false
// positives.

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"testing"
)

const (
	biasCycles = 20
	// biasTurnFrames / biasGapFrames: each speaker holds the floor for 640 ms,
	// then 320 ms where nobody does.
	biasTurnFrames = 40
	biasGapFrames  = 20
	// biasBleedBelowDB is how far below its owner's voice a microphone hears
	// the other people in the call, through their speakers.
	biasBleedBelowDB = 30.0
	// biasQuietBelowDB is where a recorded track's quiet frames sit. The
	// ingested speaker's spliced track is this plus the swept offset.
	biasQuietBelowDB = 34.0
	// biasJitterDB dithers every frame so no level in the scene is a constant.
	biasJitterDB = 2.0
	// biasGapToleranceDB is how far a gap may move before the sweep calls it a
	// change. Dithered levels do not reconstruct exactly under the correction —
	// frames below the recorded baseline are raised TO it, which is the point —
	// so the invariant asserted is the decision plus a gap that has not moved
	// by more than the dither could explain.
	biasGapToleranceDB = 2 * biasJitterDB
)

var biasSpeakers = []string{"alice", "bob", "carol"}

// Three different microphone gains, so nothing here can pass by everybody
// happening to record at the same level.
var biasSpeechDBFS = map[string]float64{"alice": -20, "bob": -26, "carol": -32}

func biasCycleFrames() int { return len(biasSpeakers) * (biasTurnFrames + biasGapFrames) }

// biasActive returns the index of the speaker holding the floor at frame i, or
// -1 in the pause after a turn.
func biasActive(i int) int {
	c := i % biasCycleFrames()
	slot := c / (biasTurnFrames + biasGapFrames)
	if c%(biasTurnFrames+biasGapFrames) < biasTurnFrames {
		return slot
	}
	return -1
}

// biasEnvelope builds one speaker's level timeline: their own voice while they
// hold the floor, everyone else's bleed while somebody else does, and
// quietBelow dB below their own voice in the pauses. Floor and speech
// references are read off it exactly as envelopeFromSamples reads them off real
// audio, so the calibration under test is the production one.
func biasEnvelope(id string, quietBelowDB float64) *SpeakerEnvelope {
	n := biasCycles * biasCycleFrames()
	env := &SpeakerEnvelope{SpeakerID: id, HopMS: attributionHopMS}
	env.FrameDB = make([]float64, n)
	env.Present = make([]bool, n)
	speech := biasSpeechDBFS[id]
	// Real speech, bleed and room tone are not flat, and a scene of perfect
	// steps would let the correction pass by reconstructing one constant from
	// another. Every frame is dithered by a deterministic +/-biasJitterDB, from
	// a sequence that depends only on the speaker, so the same frame carries
	// the same dither at every offset in the sweep and the comparison stays
	// like for like.
	dither := rand.New(rand.NewSource(int64(biasDitherSeed(id))))
	present := make([]float64, 0, n)
	for i := 0; i < n; i++ {
		active := biasActive(i)
		var level float64
		switch {
		case active >= 0 && biasSpeakers[active] == id:
			level = speech
		case active >= 0:
			level = speech - biasBleedBelowDB
		default:
			level = speech - quietBelowDB
		}
		level += (dither.Float64()*2 - 1) * biasJitterDB
		env.FrameDB[i] = level
		env.Present[i] = true
		present = append(present, level)
	}
	env.FloorDB = percentileOf(present, attributionFloorPercentile)
	env.SpeechDB = percentileOf(present, attributionSpeechPercentile)
	return env
}

// biasDitherSeed is FNV-1a over the speaker id. Seeding from the id's LENGTH
// gave alice and carol the same sequence, which is exactly the correlation the
// dither exists to avoid.
func biasDitherSeed(id string) uint32 {
	const offset32, prime32 = 2166136261, 16777619
	h := uint32(offset32)
	for i := 0; i < len(id); i++ {
		h ^= uint32(id[i])
		h *= prime32
	}
	return h
}

// biasWord is one word with the ground truth of how it came to exist.
type biasWord struct {
	speaker string
	// kind is "genuine" (its owner said it), "ghost" (the decoder heard
	// somebody else's voice bleeding onto this track) or "hallucination" (the
	// decoder produced it over the noise between turns).
	kind    string
	startMS int64
	endMS   int64
}

func biasWords() []biasWord {
	var out []biasWord
	hop := int64(attributionHopMS)
	total := biasCycles * biasCycleFrames()
	for i := 0; i < total; i += biasTurnFrames + biasGapFrames {
		active := biasActive(i)
		if active < 0 {
			continue
		}
		talker := biasSpeakers[active]
		for _, off := range []int{5, 15, 25} {
			out = append(out, biasWord{talker, "genuine", int64(i+off) * hop, int64(i+off+3) * hop})
		}
		for _, other := range biasSpeakers {
			if other == talker {
				continue
			}
			out = append(out, biasWord{other, "ghost", int64(i+12) * hop, int64(i+15) * hop})
		}
		pause := i + biasTurnFrames
		for _, any := range biasSpeakers {
			out = append(out, biasWord{any, "hallucination", int64(pause+5) * hop, int64(pause+8) * hop})
		}
	}
	return out
}

// biasVerdict is everything the attribution stage decided about one meeting.
type biasVerdict struct {
	thresholdDB    float64
	thresholdFound bool
	flagged        int
	// flaggedByKind counts flags against the ground truth: flags on "ghost" are
	// the stage working, flags on "genuine" are it deleting real speech.
	flaggedByKind map[string]int
	// medianGapByKind is the evidence the flags were read off, and
	// spreadGapByKind its standard deviation. The spread is the number that
	// exposes this bias: an ingested speaker does not move every ghost gap the
	// same way, it moves the ghosts OWNED by them down and the ghosts riding
	// their voice up, so the mode widens while its median stays put.
	medianGapByKind map[string]float64
	spreadGapByKind map[string]float64
	// ingestedFloorDB is the floor the ingested speaker was measured against.
	ingestedFloorDB float64
}

func (v biasVerdict) String() string {
	return fmt.Sprintf("threshold=%.2f found=%v flagged=%d (ghost %d, genuine %d, hallucination %d) ingestedFloor=%.1f gaps: ghost %.1f, genuine %.1f, hallucination %.1f",
		v.thresholdDB, v.thresholdFound, v.flagged,
		v.flaggedByKind["ghost"], v.flaggedByKind["genuine"], v.flaggedByKind["hallucination"],
		v.ingestedFloorDB,
		v.medianGapByKind["ghost"], v.medianGapByKind["genuine"], v.medianGapByKind["hallucination"]) +
		fmt.Sprintf(" ghostSpread=%.1f", v.spreadGapByKind["ghost"])
}

// runBiasScene reads the whole meeting's crosstalk verdict with alice's quiet
// frames offsetDB below where a recorded track's would sit. correct selects
// whether the normalisation under test runs.
func runBiasScene(offsetDB float64, correct bool) biasVerdict {
	envelopes := []*SpeakerEnvelope{
		biasEnvelope("alice", biasQuietBelowDB+offsetDB),
		biasEnvelope("bob", biasQuietBelowDB),
		biasEnvelope("carol", biasQuietBelowDB),
	}
	// Alice is the ingested speaker: her envelope came from the render her
	// words were decoded from, the other two from the tracks the SFU delivered.
	envelopes[0].FromSourceAudio = true
	if correct {
		capIngestedDynamicRange(envelopes)
	}

	words := biasWords()
	kind := make(map[string]string, len(words))
	bySpeaker := map[string][]Word{}
	for _, w := range words {
		kind[fmt.Sprintf("%s@%d", w.speaker, w.startMS)] = w.kind
		bySpeaker[w.speaker] = append(bySpeaker[w.speaker], Word{Text: "w", StartMS: w.startMS, EndMS: w.endMS})
	}
	segments := make([]Segment, 0, len(biasSpeakers))
	for _, id := range biasSpeakers {
		ws := bySpeaker[id]
		sort.Slice(ws, func(i, j int) bool { return ws[i].StartMS < ws[j].StartMS })
		segments = append(segments, Segment{SpeakerID: id, StartMS: ws[0].StartMS, EndMS: ws[len(ws)-1].EndMS, Words: ws})
	}

	out, res := AnnotateAttribution(segments, envelopes, false)
	verdict := biasVerdict{
		thresholdDB:     res.ThresholdDB,
		thresholdFound:  res.ThresholdFound,
		flagged:         res.Flagged,
		flaggedByKind:   map[string]int{},
		medianGapByKind: map[string]float64{},
		spreadGapByKind: map[string]float64{},
		ingestedFloorDB: envelopes[0].FloorDB,
	}
	gaps := map[string][]float64{}
	for _, seg := range out {
		for _, w := range seg.Words {
			k := kind[fmt.Sprintf("%s@%d", seg.SpeakerID, w.StartMS)]
			if w.HasAttributionGap {
				gaps[k] = append(gaps[k], w.AttributionGapDB)
			}
			if w.LowConfidenceSpeaker {
				verdict.flaggedByKind[k]++
			}
		}
	}
	for k, v := range gaps {
		sort.Float64s(v)
		verdict.medianGapByKind[k] = v[len(v)/2]
		verdict.spreadGapByKind[k] = stddev(v)
	}
	return verdict
}

// biasOffsetsDB is the sweep. 0 dB is the homogeneous meeting nobody ingested;
// the upper end is what filling an SFU track's silences with an undamaged
// microphone recording can plausibly reach.
var biasOffsetsDB = []float64{0, 2, 5, 10, 15, 20, 25, 30}

// The measurement that had to be made before the envelope change could ship:
// one ingested speaker must not move anybody's crosstalk verdict.
//
// Reverting capIngestedDynamicRange to a no-op fails this from 10 dB up.
func TestOneIngestedSpeakerDoesNotMoveEveryonesCrosstalkVerdict(t *testing.T) {
	baseline := runBiasScene(0, true)
	t.Logf("baseline (no floor offset): %v", baseline)
	if !baseline.thresholdFound {
		t.Fatalf("the fixture is not exercising anything: the baseline meeting shows no crosstalk population (%v)", baseline)
	}
	if baseline.flaggedByKind["ghost"] == 0 {
		t.Fatalf("the fixture is not exercising anything: no ghost was flagged in the baseline (%v)", baseline)
	}
	if baseline.flaggedByKind["genuine"] != 0 {
		t.Fatalf("the fixture starts out deleting real speech (%v)", baseline)
	}

	for _, offset := range biasOffsetsDB {
		got := runBiasScene(offset, true)
		t.Logf("ingested floor %2.0f dB lower: %v", offset, got)
		if got.thresholdFound != baseline.thresholdFound ||
			math.Abs(got.thresholdDB-baseline.thresholdDB) > biasGapToleranceDB {
			t.Errorf("a %.0f dB floor offset on one ingested speaker moved the meeting's crosstalk threshold: %v, baseline %v",
				offset, got, baseline)
		}
		if got.flagged != baseline.flagged {
			t.Errorf("a %.0f dB floor offset on one ingested speaker changed how many words the meeting flags: %v, baseline %v",
				offset, got, baseline)
		}
		for _, kind := range []string{"ghost", "genuine", "hallucination"} {
			if got.flaggedByKind[kind] != baseline.flaggedByKind[kind] {
				t.Errorf("a %.0f dB floor offset changed the flags on %s words: %d, baseline %d",
					offset, kind, got.flaggedByKind[kind], baseline.flaggedByKind[kind])
			}
			if math.Abs(got.medianGapByKind[kind]-baseline.medianGapByKind[kind]) > biasGapToleranceDB {
				t.Errorf("a %.0f dB floor offset moved the median gap on %s words to %.1f dB, baseline %.1f dB",
					offset, kind, got.medianGapByKind[kind], baseline.medianGapByKind[kind])
			}
			if math.Abs(got.spreadGapByKind[kind]-baseline.spreadGapByKind[kind]) > biasGapToleranceDB {
				t.Errorf("a %.0f dB floor offset spread the gaps on %s words to %.1f dB, baseline %.1f dB",
					offset, kind, got.spreadGapByKind[kind], baseline.spreadGapByKind[kind])
			}
		}
	}
}

// The bias itself, pinned so the correction above cannot be quietly removed as
// unnecessary: without it, one ingested speaker turns crosstalk detection off
// for the whole meeting, and says nothing about having done so.
func TestWithoutTheCorrectionAnIngestedSpeakerSilencesCrosstalkDetection(t *testing.T) {
	baseline := runBiasScene(0, false)
	if !baseline.thresholdFound || baseline.flaggedByKind["ghost"] == 0 {
		t.Fatalf("the fixture is not exercising anything: %v", baseline)
	}
	t.Logf("uncorrected baseline: %v", baseline)

	var broke []float64
	for _, offset := range biasOffsetsDB {
		got := runBiasScene(offset, false)
		t.Logf("uncorrected, ingested floor %2.0f dB lower: %v", offset, got)
		if !got.thresholdFound {
			broke = append(broke, offset)
		}
	}
	if len(broke) == 0 {
		t.Fatalf("the uncorrected scene never breaks, so capIngestedDynamicRange is measuring a bias that is not there; re-derive it before keeping it")
	}
	t.Logf("uncorrected, the meeting stops flagging any crosstalk at all from a %.0f dB floor offset up", broke[0])

	// The headline number in the PR: at 25 dB the mode is gone entirely.
	worst := runBiasScene(25, false)
	if worst.thresholdFound || worst.flagged != 0 {
		t.Fatalf("expected the 25 dB scene to lose its crosstalk mode altogether, got %v", worst)
	}
	// The mechanism, not just the outcome: the ghost gaps did not move as a
	// body — the ones riding alice's voice went up and the ones on her own
	// track went down — so the mode widened from a spike to a 20 dB smear and
	// the tightness gate in EstimateCrosstalkThresholdDB declined it.
	if worst.spreadGapByKind["ghost"] < 15 {
		t.Fatalf("expected the ghost gaps to smear without the correction, spread %.1f dB (baseline %.1f dB)",
			worst.spreadGapByKind["ghost"], baseline.spreadGapByKind["ghost"])
	}
	if baseline.spreadGapByKind["ghost"] > biasGapToleranceDB {
		t.Fatalf("the baseline ghost mode is already %.1f dB wide, so the smear above is not the ingested floor's doing",
			baseline.spreadGapByKind["ghost"])
	}
}

// The correction must be a no-op on every meeting that does not mix the two
// kinds of evidence, because there is nothing to correct there and a silent
// change to an already-tuned detector is not free.
func TestTheCorrectionDoesNothingWithoutIngestion(t *testing.T) {
	for _, offset := range biasOffsetsDB {
		envelopes := []*SpeakerEnvelope{
			biasEnvelope("alice", biasQuietBelowDB+offset),
			biasEnvelope("bob", biasQuietBelowDB),
			biasEnvelope("carol", biasQuietBelowDB),
		}
		before := make([]float64, len(envelopes))
		for i, env := range envelopes {
			before[i] = env.FloorDB
		}
		// Nobody uploaded: alice is simply on a quieter rig.
		capIngestedDynamicRange(envelopes)
		for i, env := range envelopes {
			if env.FloorDB != before[i] {
				t.Fatalf("offset %.0f dB: %s's floor moved from %.1f to %.1f with no ingestion in the meeting",
					offset, env.SpeakerID, before[i], env.FloorDB)
			}
		}
	}

	// A recorded reference too shallow to be a yardstick must be declined
	// rather than honoured. Honouring it would clamp every frame of the
	// ingested track to within `recorded` dB of its own speech reference, and
	// ownerQuietDuring needs quietOwnerShortfallDB of room below that reference
	// to fire at all — so the speaker's own ghosts would stop being flaggable
	// for the whole meeting as the price of correcting everybody else's.
	shallow := &SpeakerEnvelope{SpeakerID: "bob", HopMS: attributionHopMS,
		FrameDB:  []float64{-40, -36, -34, -33, -32, -31, -30, -30, -30, -30, -30, -30},
		SpeechDB: -30, FloorDB: -36}
	shallow.Present = make([]bool, len(shallow.FrameDB))
	for i := range shallow.Present {
		shallow.Present[i] = true
	}
	if snr := shallow.SpeechDB - shallow.FloorDB; snr >= quietOwnerShortfallDB {
		t.Fatalf("the fixture is not shallow: %.1f dB", snr)
	}
	ingested := biasEnvelope("alice", biasQuietBelowDB+25)
	ingested.FromSourceAudio = true
	floorBefore, framesBefore := ingested.FloorDB, append([]float64(nil), ingested.FrameDB...)
	capIngestedDynamicRange([]*SpeakerEnvelope{ingested, shallow})
	if ingested.FloorDB != floorBefore {
		t.Errorf("a %.1f dB recorded reference moved the ingested floor from %.1f to %.1f",
			shallow.SpeechDB-shallow.FloorDB, floorBefore, ingested.FloorDB)
	}
	for i := range framesBefore {
		if ingested.FrameDB[i] != framesBefore[i] {
			t.Fatalf("frame %d was clamped against a reference too shallow to use", i)
		}
	}
	// The property the guard is protecting, checked directly: the ingested
	// speaker's quiet stretches still read as a quiet owner.
	quietWord := Word{Text: "ghost", StartMS: int64(biasTurnFrames+5) * attributionHopMS,
		EndMS: int64(biasTurnFrames+8) * attributionHopMS}
	if !ownerQuietDuring(quietWord, "alice", []*SpeakerEnvelope{ingested, shallow}) {
		t.Error("the ingested speaker can no longer read as a quiet owner, so none of their words could ever be flagged")
	}

	// And when EVERY speaker was ingested there is no recorded reference and no
	// asymmetry either, so it must leave those alone too.
	all := []*SpeakerEnvelope{
		biasEnvelope("alice", biasQuietBelowDB+25),
		biasEnvelope("bob", biasQuietBelowDB+25),
	}
	for _, env := range all {
		env.FromSourceAudio = true
	}
	floors := []float64{all[0].FloorDB, all[1].FloorDB}
	capIngestedDynamicRange(all)
	for i, env := range all {
		if env.FloorDB != floors[i] {
			t.Fatalf("with every speaker ingested, %s's floor moved from %.1f to %.1f", env.SpeakerID, floors[i], env.FloorDB)
		}
	}
}
