package transcribe

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// Production-shaped fixtures.
//
// The attribution stage shipped with a bug that would have fired on two thirds
// of production streams, and none of my fixtures could have caught it, because
// every fixture track started at t=0, was fully dense, and had a unique speaker
// id. A survey of the 44 multitrack meetings in the archive (199 audio streams)
// says production looks nothing like that:
//
//	streams starting >= 1 s late     67.8%   median 36.8 s, p90 737 s, max 4308 s
//	duplicate speaker ids            17/44 meetings, 46 extra streams
//	packet density (DTX sparsity)    p10 5.4%, p25 22%, p50 71%
//	streams under 50% dense          37.7%
//
// A participant joins after the call starts, mutes, drops and rejoins under a
// second stream carrying the same participant id, and speaks for 40 seconds of
// a 50-minute meeting. Every one of those is normal, and each one broke an
// assumption baked into the fixtures.
//
// The 6-second fixture below compresses that shape: a punctual speaker with a
// genuine noise floor under their speech, a late joiner, a DTX-shaped sparse
// participant whose packet density is derived from the survey's p10, and a
// speech-only rejoin stream sharing the punctual speaker's participant id at a
// different level than its parent. Join offsets are hard-coded (the survey's
// absolute offsets do not scale to a 6 s meeting); the density is derived from
// the constant so the sparse track cannot quietly drift back to dense.
const (
	// prodSparseDensityP10 is the 10th-percentile packet density observed in
	// production. A participant who barely speaks contributes almost no audio
	// to their own track; the fixture's DTX burst spacing is derived from this.
	prodSparseDensityP10 = 0.054
)

// buildProdShapedMeeting writes a multitrack MKV with production's shape.
//
// Every dense track carries a real quiet/loud contrast — a low-level noise
// floor under gated speech, at different levels per track — so each track's
// own calibration is observable: a bug that ignores it shows up as a wrong
// floor or a wrong winner, not as noise. The sparse track is DTX-shaped: many
// isolated 20 ms packets spread over the whole meeting, exact digital silence
// between them (it is encoded FLAC so the inter-packet silence survives the
// codec losslessly). The rejoin stream carries ONLY ongoing speech, at a
// different level than its parent stream, so calibrated alone its floor IS
// speech — the exact rotation failure calibrateByLogicalSpeaker exists for.
func buildProdShapedMeeting(t *testing.T, dir string) string {
	t.Helper()
	outPath := filepath.Join(dir, "prod-shaped.mkv")

	const total = 6.0
	// One 20 ms packet per period, spaced so the packet density matches the
	// production p10.
	const burstSeconds = 0.02
	burstPeriod := burstSeconds / prodSparseDensityP10

	// A hot mic present from the start: room noise throughout, with speech at
	// -14 dBFS peak between 2 s and 3.6 s.
	punctual := fmt.Sprintf(
		"sine=frequency=300:sample_rate=48000:duration=%.1f,volume=0.2,"+
			"volume=0:enable='not(between(t,2,3.6))'[s];"+
			"anoisesrc=colour=pink:sample_rate=48000:amplitude=0.002:seed=7:duration=%.1f[n];"+
			"[s][n]amix=inputs=2:duration=first:normalize=0", total, total)
	// A quiet rig that joins 2 s late: low-level noise from the join onward,
	// speech at -26 dBFS peak from 4.5 s to 5.3 s (after the punctual
	// passage), leaving 5.35-5.95 s as a quiet-on-every-track tail.
	late := fmt.Sprintf(
		"sine=frequency=700:sample_rate=48000:duration=%.1f,volume=0.05,"+
			"volume=0:enable='not(between(t,2.5,3.3))'[s];"+
			"anoisesrc=colour=pink:sample_rate=48000:amplitude=0.0005:seed=9:duration=%.1f[n];"+
			"[s][n]amix=inputs=2:duration=first:normalize=0", total-2.0, total-2.0)
	// The DTX-shaped participant: 20 ms packets of a -30 dBFS tone (bleed and
	// comfort noise) on the survey-derived spacing, exact zeros in between —
	// one packet lands inside the 2500-2700 ms window the tests probe — plus
	// ONE real utterance at 3.8-4.4 s, well above the packets. The utterance
	// is what establishes this speaker's own speech reference: a word riding a
	// bleed-level packet then carries the true ghost signature (owner far
	// below their own speech level) and stays flaggable.
	sparse := fmt.Sprintf(
		"sine=frequency=1100:sample_rate=48000:duration=%.1f,volume=0.03,"+
			"asetnsamples=n=960,volume=0:enable='gte(mod(t,%.4f),%.2f)'[b];"+
			"sine=frequency=900:sample_rate=48000:duration=%.1f,volume=0.35,"+
			"volume=0:enable='not(between(t,3.8,4.4))'[sp];"+
			"[b][sp]amix=inputs=2:duration=first:normalize=0",
		total, burstPeriod, burstSeconds, total)
	// The rejoin: a second stream for the punctual speaker, same participant
	// id, carrying ONLY ongoing speech — at -20 dBFS, deliberately NOT the
	// parent's level, so a floor calibrated on this stream alone is visibly
	// different from the parent's. It overlaps the probed window.
	rejoin := "sine=frequency=300:sample_rate=48000:duration=1.2,volume=0.1"

	args := []string{
		"-y", "-v", "error",
		"-f", "lavfi", "-i", punctual,
		"-itsoffset", "2.0", "-f", "lavfi", "-i", late,
		"-f", "lavfi", "-i", sparse,
		"-itsoffset", "2.2", "-f", "lavfi", "-i", rejoin,
		"-map", "0:a:0", "-map", "1:a:0", "-map", "2:a:0", "-map", "3:a:0",
		"-metadata:s:a:0", "title=Punctual",
		"-metadata:s:a:0", "participant_id=user-punctual",
		"-metadata:s:a:1", "title=LateJoiner",
		"-metadata:s:a:1", "participant_id=user-late",
		"-metadata:s:a:2", "title=BarelySpoke",
		"-metadata:s:a:2", "participant_id=user-sparse",
		// Same participant id as stream 0: this is the rotated/rejoined stream.
		"-metadata:s:a:3", "title=Punctual",
		"-metadata:s:a:3", "participant_id=user-punctual",
		"-c:a", "libopus",
		// Lossless for the DTX track: opus is not guaranteed to reproduce the
		// exact-zero inter-packet silence that makes a track DTX-shaped on the
		// decoded timeline.
		"-c:a:2", "flac",
		outPath,
	}
	if err := runMediaCommand("ffmpeg", args...); err != nil {
		t.Fatalf("create prod-shaped meeting: %v", err)
	}
	return outPath
}

// prodStreamByLabel returns the first probed stream with the given title.
func prodStreamByLabel(t *testing.T, streams []AudioStream, label string) AudioStream {
	t.Helper()
	for _, s := range streams {
		if s.SpeakerLabel == label {
			return s
		}
	}
	t.Fatalf("no stream labelled %q in the fixture", label)
	return AudioStream{}
}

// The fixture must actually have the shape it claims, or the tests built on it
// prove nothing. This is the guard against the fixture quietly drifting back
// towards the tidy version.
func TestProdShapedFixtureHasProductionShape(t *testing.T) {
	requireFFMediaTools(t)
	dir := t.TempDir()
	mkv := buildProdShapedMeeting(t, dir)

	streams, _, err := ProbeMKV(mkv)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(streams) != 4 {
		t.Fatalf("expected 4 audio streams, got %d", len(streams))
	}

	byID := map[string]int{}
	for _, s := range streams {
		byID[s.SpeakerID]++
	}
	var shared int
	for _, n := range byID {
		if n > 1 {
			shared += n - 1
		}
	}
	if shared == 0 {
		t.Error("fixture must contain a rejoined stream sharing a participant id: " +
			"39% of production meetings do")
	}

	const sr = 16000
	frame, hop := sr*attributionFrameMS/1000, sr*attributionHopMS/1000

	var late int
	for _, s := range streams {
		samples, err := ExtractSpeakerFloats(mkv, s)
		if err != nil {
			t.Fatalf("decode %s: %v", s.SpeakerLabel, err)
		}
		// A late joiner's decoded track begins with timeline padding.
		if len(samples) > 0 && samples[0] == 0 {
			var lead int
			for lead < len(samples) && samples[lead] == 0 {
				lead++
			}
			if float64(lead)/16000.0 > 0.5 {
				late++
			}
		}
	}
	if late == 0 {
		t.Error("fixture must contain a late-joining track: 67.8% of production streams are")
	}

	// The sparse track must be DTX-shaped: many short isolated packets, not
	// one contiguous burst and not a dense stream.
	sparseStream := prodStreamByLabel(t, streams, "BarelySpoke")
	samples, err := ExtractSpeakerFloats(mkv, sparseStream)
	if err != nil {
		t.Fatalf("decode sparse track: %v", err)
	}
	sparse := envelopeFromSamples(sparseStream.SpeakerID, samples, frame, hop, attributionHopMS)
	var present, runs int
	prev := false
	for _, ok := range sparse.Present {
		if ok {
			present++
			if !prev {
				runs++
			}
		}
		prev = ok
	}
	if runs < 8 {
		t.Errorf("sparse track has %d packet bursts; DTX shape needs many short separated packets", runs)
	}
	if density := float64(present) / float64(len(sparse.Present)); density < 0.02 || density > 0.30 {
		t.Errorf("sparse track frame density %.2f is not production-sparse", density)
	}

	// Every stream — the sparse one included — must be measurable in the window
	// the attribution tests probe, or the ghost-word and rotation cases are
	// never exercised.
	envelopes, err := BuildSpeakerEnvelopes(mkv, streams, 16000, nil)
	if err != nil {
		t.Fatalf("envelopes: %v", err)
	}
	var covering int
	for _, env := range envelopes {
		if _, ok := env.aboveFloor(2500, 2700); ok {
			covering++
		}
	}
	if covering != len(envelopes) {
		t.Errorf("only %d of %d streams carry audio in the probed window; every track "+
			"must overlap it or its case is never exercised", covering, len(envelopes))
	}
}

// The rejoined stream carries only speech at its own level, so calibrated on
// its own its floor IS that speech. Pooling by logical speaker is what gives it
// the punctual speaker's established noise floor, and this asserts it on a real
// decoded fixture rather than on synthesised envelopes: the rejoin must land on
// the PARENT's floor, not merely near its own.
func TestProdShapeRotationSharesOneFloorPerSpeaker(t *testing.T) {
	requireFFMediaTools(t)
	dir := t.TempDir()
	mkv := buildProdShapedMeeting(t, dir)

	streams, _, err := ProbeMKV(mkv)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	const sr = 16000
	frame, hop := sr*attributionFrameMS/1000, sr*attributionHopMS/1000

	var dupID string
	count := map[string]int{}
	for _, s := range streams {
		count[s.SpeakerID]++
	}
	for id, n := range count {
		if n > 1 {
			dupID = id
		}
	}
	if dupID == "" {
		t.Fatal("fixture no longer contains a speaker with two streams")
	}

	// Uncalibrated per-stream floors for the duplicated speaker.
	var uncal []float64
	for _, s := range streams {
		if s.SpeakerID != dupID {
			continue
		}
		samples, err := ExtractSpeakerFloats(mkv, s)
		if err != nil {
			t.Fatalf("decode %s: %v", s.SpeakerLabel, err)
		}
		env := envelopeFromSamples(s.SpeakerID, samples, frame, hop, attributionHopMS)
		uncal = append(uncal, env.FloorDB)
	}
	if len(uncal) != 2 {
		t.Fatalf("expected exactly 2 streams for the duplicated speaker, got %d", len(uncal))
	}
	parentFloor, rejoinFloor := uncal[0], uncal[1]
	if parentFloor > rejoinFloor {
		parentFloor, rejoinFloor = rejoinFloor, parentFloor
	}
	// The fixture only exercises the rotation failure while the speech-only
	// rejoin calibrates far away from the parent on its own.
	if rejoinFloor-parentFloor < 15 {
		t.Fatalf("fixture stopped exercising the rotation failure: uncalibrated floors "+
			"%.1f vs %.1f dB", parentFloor, rejoinFloor)
	}

	envelopes, err := BuildSpeakerEnvelopes(mkv, streams, 16000, nil)
	if err != nil {
		t.Fatalf("envelopes: %v", err)
	}
	var floors []float64
	for _, env := range envelopes {
		if env.SpeakerID == dupID {
			floors = append(floors, env.FloorDB)
		}
	}
	if len(floors) != 2 {
		t.Fatalf("expected 2 calibrated envelopes for the duplicated speaker, got %d", len(floors))
	}
	for _, v := range floors {
		if math.Abs(v-floors[0]) > 1 {
			t.Errorf("%s has streams calibrated to different floors: %.1f vs %.1f",
				dupID, floors[0], v)
		}
		// Sharing a floor is not enough — it must be the parent's noise floor.
		// Without per-speaker calibration the rejoin keeps its own speech-level
		// floor, tens of dB above this.
		if v > parentFloor+1 {
			t.Errorf("stream floor %.1f dB did not inherit the parent's %.1f dB noise floor",
				v, parentFloor)
		}
	}
}

// The regression the review caught, at the level it actually bites: a late
// joiner whose pre-join padding was measured as their noise floor scored
// hundreds of dB above it and would have won every contested word — and a word
// on the barely-present track during somebody else's speech must be
// contradicted by the evidence, unconditionally.
func TestAttributionOnProductionShapeDoesNotFavourLateJoiners(t *testing.T) {
	requireFFMediaTools(t)
	dir := t.TempDir()
	mkv := buildProdShapedMeeting(t, dir)

	streams, _, err := ProbeMKV(mkv)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	envelopes, err := BuildSpeakerEnvelopes(mkv, streams, 16000, nil)
	if err != nil {
		t.Fatalf("envelopes: %v", err)
	}

	// No track may report an implausible level above its own floor. Padding
	// measured as floor produced ~238 dB; real microphone dynamics are tens.
	for _, env := range envelopes {
		for i := range env.FrameDB {
			if len(env.Present) == len(env.FrameDB) && !env.Present[i] {
				continue
			}
			if got := env.FrameDB[i] - env.FloorDB; got > 120 {
				t.Fatalf("%s reports %.1f dB above its own floor: padding is being "+
					"measured as the noise floor", env.SpeakerID, got)
			}
		}
	}

	// SpeakerID is derived and hashed from the participant id, so an assertion
	// written against a guessed literal silently never runs. Look the real one
	// up from the probed streams.
	speakerID := func(label string) string {
		return prodStreamByLabel(t, streams, label).SpeakerID
	}

	// The punctual speaker is genuinely talking here, so their own word must not
	// be contradicted.
	word := Word{Text: "test", StartMS: 2500, EndMS: 2700}
	gap, ok := AttributionGapDB(word, speakerID("Punctual"), envelopes)
	if !ok {
		t.Fatal("expected the real speaker's word to be measurable")
	}
	if gap > 6 {
		t.Errorf("the actual speaker was contradicted by %.1f dB", gap)
	}

	// A word attributed to the participant who has barely any audio, during the
	// punctual speaker's loud passage, is the crosstalk shape and must be
	// contradicted. The sparse track has a DTX packet inside this window, so
	// the measurement must succeed — a fixture where it cannot be measured is
	// not testing anything.
	ghost, ok := AttributionGapDB(word, speakerID("BarelySpoke"), envelopes)
	if !ok {
		t.Fatal("the sparse track must be measurable during the probed word; " +
			"the fixture no longer covers the ghost-word case")
	}
	if ghost <= gap+20 {
		t.Errorf("a word on the near-silent track scored %.1f dB, not clearly worse than "+
			"the real speaker's %.1f dB", ghost, gap)
	}
	if ghost < 25 {
		t.Errorf("a ghost word during the punctual speaker's speech should be contradicted "+
			"by tens of dB, got %.1f", ghost)
	}
}

// prodShapeSegments builds a transcript over the fixture with the populations
// attribution needs to make a real decision. The punctual speaker's own words
// sit inside their 2-3.6 s passage (owner at reference — never flaggable).
// The ghost populations ride the QUIET tracks: hot ghosts on the late joiner's
// noise while the punctual speaker talks (large gap, owner quiet), cold words
// on the same track during the 5.4-5.95 s quiet tail (near-zero gap, owner
// quiet — the estimator's lower cluster), and a handful of ghosts on the
// DTX-shaped track's bleed packets in the probed window.
func prodShapeSegments(streams []AudioStream) []Segment {
	id := map[string]string{}
	for _, s := range streams {
		id[s.SpeakerLabel] = s.SpeakerID
	}
	var real, ghostHot []Word
	for i := 0; i < 50; i++ {
		start := int64(2050 + i*30)
		real = append(real, Word{Text: "real", StartMS: start, EndMS: start + 25})
		ghostHot = append(ghostHot, Word{Text: "ghost", StartMS: start, EndMS: start + 25})
	}
	var ghostCold []Word
	for i := 0; i < 20; i++ {
		start := int64(5400 + i*25)
		ghostCold = append(ghostCold, Word{Text: "cold", StartMS: start, EndMS: start + 20})
	}
	var sparseGhost []Word
	for i := 0; i < 8; i++ {
		start := int64(2500 + i*30)
		sparseGhost = append(sparseGhost, Word{Text: "sghost", StartMS: start, EndMS: start + 25})
	}
	return []Segment{
		{SpeakerID: id["Punctual"], StartMS: real[0].StartMS, EndMS: real[len(real)-1].EndMS,
			Text: "real", Words: real},
		{SpeakerID: id["LateJoiner"], StartMS: ghostHot[0].StartMS, EndMS: ghostHot[len(ghostHot)-1].EndMS,
			Text: "ghost", Words: ghostHot},
		{SpeakerID: id["LateJoiner"], StartMS: ghostCold[0].StartMS, EndMS: ghostCold[len(ghostCold)-1].EndMS,
			Text: "cold", Words: ghostCold},
		{SpeakerID: id["BarelySpoke"], StartMS: sparseGhost[0].StartMS, EndMS: sparseGhost[len(sparseGhost)-1].EndMS,
			Text: "sghost", Words: sparseGhost},
	}
}

// Attribution must survive a meeting whose tracks are late, sparse and
// duplicated, without failing the build or corrupting the transcript — and its
// verdicts must be real: the fixture produces a genuine crosstalk population,
// so the threshold must be found, the ghost words flagged, the real speaker's
// words untouched, and word counts must change exactly when drop mode says so.
func TestAttributionSurvivesProductionShapeEndToEnd(t *testing.T) {
	requireFFMediaTools(t)
	dir := t.TempDir()
	mkv := buildProdShapedMeeting(t, dir)

	streams, _, err := ProbeMKV(mkv)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	segments := prodShapeSegments(streams)
	before := CountWords(segments)

	out := applyAttribution(mkv, streams, segments, 16000, BuildConfig{}, os.Stdout)
	if CountWords(out) != before {
		t.Errorf("the default path must not delete words: %d became %d", before, CountWords(out))
	}
	if err := ValidateSegments(out); err != nil {
		t.Fatalf("attribution produced an invalid transcript on production shape: %v", err)
	}

	// Whatever the verdict, it must be reproducible: the same input twice gives
	// the same answer, which a stream-order-dependent bug would break.
	again := applyAttribution(mkv, streams, prodShapeSegments(streams), 16000, BuildConfig{}, os.Stdout)
	if len(again) != len(out) {
		t.Fatalf("attribution is not deterministic: %d vs %d segments", len(again), len(out))
	}
	for i := range out {
		for j := range out[i].Words {
			if out[i].Words[j].AttributionGapDB != again[i].Words[j].AttributionGapDB {
				t.Errorf("segment %d word %d gap changed between runs: %.3f vs %.3f",
					i, j, out[i].Words[j].AttributionGapDB, again[i].Words[j].AttributionGapDB)
			}
		}
	}

	// The verdict itself, at the annotation level.
	envelopes, err := BuildSpeakerEnvelopes(mkv, streams, 16000, nil)
	if err != nil {
		t.Fatalf("envelopes: %v", err)
	}
	annotated, res := AnnotateAttribution(prodShapeSegments(streams), envelopes, false)
	if res.WordsMeasured < 50 {
		t.Fatalf("only %d words measured; the fixture no longer produces a usable gap population",
			res.WordsMeasured)
	}
	if !res.ThresholdFound {
		t.Fatal("this fixture carries a genuine crosstalk population; the estimator must find " +
			"its threshold")
	}
	var flaggedReal, flaggedHot, flaggedCold, flaggedSparse int
	for _, seg := range annotated {
		for _, w := range seg.Words {
			if !w.LowConfidenceSpeaker {
				continue
			}
			switch w.Text {
			case "real":
				flaggedReal++
			case "ghost":
				flaggedHot++
			case "cold":
				flaggedCold++
			case "sghost":
				flaggedSparse++
			}
		}
	}
	if flaggedReal != 0 {
		t.Errorf("the actual speaker's words must never be flagged, got %d", flaggedReal)
	}
	if flaggedHot < 40 {
		t.Errorf("expected the ghost words on the quiet late-join track to be flagged, got %d of 50",
			flaggedHot)
	}
	if flaggedCold != 0 {
		t.Errorf("owner-quiet words with near-zero gaps must not be flagged, got %d", flaggedCold)
	}
	if flaggedSparse == 0 {
		t.Error("the ghost riding the DTX track's bleed packet in the probed window should be flagged")
	}

	// Drop mode removes exactly the flagged words and keeps every real one.
	dropped, resDrop := AnnotateAttribution(prodShapeSegments(streams), envelopes, true)
	if resDrop.Dropped < 40 {
		t.Fatalf("drop mode removed %d words; the crosstalk population was not dropped", resDrop.Dropped)
	}
	if CountWords(dropped) != before-resDrop.Dropped {
		t.Errorf("word count %d does not match %d - %d dropped",
			CountWords(dropped), before, resDrop.Dropped)
	}
	var keptReal, keptCold int
	for _, seg := range dropped {
		for _, w := range seg.Words {
			switch w.Text {
			case "real":
				keptReal++
			case "cold":
				keptCold++
			}
		}
	}
	if keptReal != 50 {
		t.Errorf("drop mode must keep every one of the real speaker's 50 words, kept %d", keptReal)
	}
	if keptCold != 20 {
		t.Errorf("drop mode must keep the 20 owner-quiet words with near-zero gaps, kept %d", keptCold)
	}
	if err := ValidateSegments(dropped); err != nil {
		t.Fatalf("drop mode produced an invalid transcript: %v", err)
	}
}
