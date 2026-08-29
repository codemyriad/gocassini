package transcribe

import (
	"fmt"
	"io"
	"math"
	"sort"
)

// Speaker attribution as a stage over the shared timeline.
//
// A word's speaker is currently decided by which decoder pass produced it:
// transcribePass runs one recognizer per track and AssembleSegments stamps that
// pass's SpeakerID onto every word it emitted. That is structurally unable to
// notice crosstalk. A participant's microphone also picks up everyone else
// through their speakers, ~30 dB down; the decoder hears faint speech on that
// track and transcribes it, and the result ships as that participant talking.
//
// This file adds the missing evidence. For every word it measures how far the
// owning track sat above its OWN noise floor, against how far the loudest other
// track sat above ITS own floor. Everything is per-track relative, so a
// participant recording 30 dB hotter than another cannot win on absolute
// loudness alone.
//
// Default behaviour is to annotate, never to delete. Real crosstalk is diffuse —
// on a 51-minute production meeting it was ~1.2% of words spread over 6–64 dB,
// overlapping the range genuine simultaneous speech occupies — so no threshold
// separates the two populations cleanly, and silently dropping canonical words
// on one would be wrong. The gap travels with each word as provenance so the
// viewer, the summariser, or a human can act on it. Dropping is available but
// opt-in, and only fires where the meeting's own distribution shows an
// unambiguous crosstalk mode.

const (
	// attributionFrameMS / attributionHopMS define the log-RMS envelope. 32 ms
	// frames at a 16 ms hop is short enough to bracket a one-syllable
	// interjection and long enough not to track individual pitch periods.
	attributionFrameMS = 32
	attributionHopMS   = 16

	// attributionPadMS widens a word's window slightly so a short word is not
	// judged on one or two frames of its own onset.
	attributionPadMS = 40

	// attributionFloorPercentile is where a track's own noise floor is read
	// from its level distribution, and attributionSpeechPercentile is its
	// speech level. Percentiles rather than a fixed dB value: participants
	// arrive with wildly different gains.
	attributionFloorPercentile  = 20.0
	attributionSpeechPercentile = 97.0
)

// SpeakerEnvelope is one track's level over the meeting timeline, expressed
// relative to that track's own quiet baseline.
type SpeakerEnvelope struct {
	SpeakerID string
	// FrameDB is the log-RMS of each frame in dBFS.
	FrameDB []float64
	// Present marks frames that carry real captured audio. Decoding puts every
	// track on the shared meeting timeline, which means a participant who joined
	// late — or was muted, or whose stream was rotated — has their absent span
	// materialised as exact digital silence. That padding is not this
	// microphone's noise floor and must not be measured as if it were: including
	// it drags the 20th percentile down to the log epsilon and makes the track
	// score hundreds of dB above "its own floor", which would win every word.
	Present []bool
	// FloorDB and SpeechDB are this track's own quiet and loud levels, computed
	// over present frames only.
	FloorDB  float64
	SpeechDB float64
	HopMS    int64
}

// aboveFloor reports how far the loudest part of [startMS,endMS] sits above
// this track's own floor. Returns false when the window falls outside the
// envelope entirely.
func (e *SpeakerEnvelope) aboveFloor(startMS, endMS int64) (float64, bool) {
	if len(e.FrameDB) == 0 || e.HopMS <= 0 {
		return 0, false
	}
	lo := (startMS - attributionPadMS) / e.HopMS
	hi := (endMS+attributionPadMS)/e.HopMS + 1
	if lo < 0 {
		lo = 0
	}
	if hi > int64(len(e.FrameDB)) {
		hi = int64(len(e.FrameDB))
	}
	if hi <= lo {
		return 0, false
	}
	// Only present frames count. A track that was not in the call at this
	// instant is not a rival for the word, and has no floor to be measured
	// against.
	window := make([]float64, 0, hi-lo)
	for i := lo; i < hi; i++ {
		if len(e.Present) == len(e.FrameDB) && !e.Present[i] {
			continue
		}
		window = append(window, e.FrameDB[i])
	}
	if len(window) == 0 {
		return 0, false
	}
	// Mean of the loudest half: robust to a word window that includes a pause,
	// without being swayed by one outlying frame the way a max would be.
	sorted := append([]float64(nil), window...)
	sort.Float64s(sorted)
	k := len(sorted) / 2
	if k < 1 {
		k = 1
	}
	var sum float64
	for _, v := range sorted[len(sorted)-k:] {
		sum += v
	}
	return sum/float64(k) - e.FloorDB, true
}

// BuildSpeakerEnvelopes decodes each participant track once and reduces it to a
// level envelope on the shared timeline. This is cheap next to ASR: it is the
// same ffmpeg decode the transcription pass already performs, plus arithmetic.
func BuildSpeakerEnvelopes(mkvPath string, streams []AudioStream, sampleRate int, progress io.Writer) ([]*SpeakerEnvelope, error) {
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	frame := sampleRate * attributionFrameMS / 1000
	hop := sampleRate * attributionHopMS / 1000
	if frame <= 0 || hop <= 0 {
		return nil, fmt.Errorf("attribution: bad frame/hop for sample rate %d", sampleRate)
	}

	envelopes := make([]*SpeakerEnvelope, 0, len(streams))
	for _, stream := range streams {
		samples, err := ExtractSpeakerFloats(mkvPath, stream)
		if err != nil {
			return nil, fmt.Errorf("attribution: decode %s: %w", stream.SpeakerLabel, err)
		}
		env := envelopeFromSamples(stream.SpeakerID, samples, frame, hop, int64(attributionHopMS))
		envelopes = append(envelopes, env)
		if progress != nil {
			fmt.Fprintf(progress, "    envelope %s: floor %.1f dB, speech %.1f dB\n",
				stream.SpeakerLabel, env.FloorDB, env.SpeechDB)
		}
	}
	return envelopes, nil
}

// envelopeFromSamples is the pure core of BuildSpeakerEnvelopes, split out so
// the framing and calibration can be tested without ffmpeg or a fixture.
func envelopeFromSamples(speakerID string, samples []float32, frame, hop int, hopMS int64) *SpeakerEnvelope {
	env := &SpeakerEnvelope{SpeakerID: speakerID, HopMS: hopMS}
	if len(samples) < frame || frame <= 0 || hop <= 0 {
		return env
	}
	n := 1 + (len(samples)-frame)/hop
	env.FrameDB = make([]float64, n)
	env.Present = make([]bool, n)
	present := make([]float64, 0, n)
	for i := 0; i < n; i++ {
		var sum float64
		nonZero := false
		for _, s := range samples[i*hop : i*hop+frame] {
			if s != 0 {
				nonZero = true
			}
			sum += float64(s) * float64(s)
		}
		rms := math.Sqrt(sum / float64(frame))
		env.FrameDB[i] = 20 * math.Log10(rms+1e-12)
		// A frame of exact zeros is timeline padding, not captured silence:
		// real capture always carries at least dither. Anything genuinely
		// recorded, however quiet, has a non-zero sample somewhere in 32 ms.
		env.Present[i] = nonZero
		if nonZero {
			present = append(present, env.FrameDB[i])
		}
	}
	if len(present) == 0 {
		// The track carries no captured audio at all. Leave the levels at zero
		// and the mask all-false; aboveFloor will decline for every window, so
		// this track is never a rival and never claims a word.
		return env
	}
	env.FloorDB = percentileOf(present, attributionFloorPercentile)
	env.SpeechDB = percentileOf(present, attributionSpeechPercentile)
	return env
}

func percentileOf(values []float64, pct float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	idx := int(pct / 100 * float64(len(sorted)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// AttributionGapDB is how far the loudest other track sat above the track a
// word was attributed to, both measured against their own floors. A gap near
// zero means the owner really was the loudest voice; a large gap means somebody
// else was, and the word is a crosstalk candidate.
func AttributionGapDB(word Word, speakerID string, envelopes []*SpeakerEnvelope) (float64, bool) {
	own := math.Inf(-1)
	var haveOwn bool
	best := math.Inf(-1)
	for _, env := range envelopes {
		level, ok := env.aboveFloor(word.StartMS, word.EndMS)
		if !ok {
			continue
		}
		if env.SpeakerID == speakerID {
			// One participant can own several streams: remux emits one per
			// rotated or rejoined packet stream, and they share the
			// participant-derived speaker id. Take the loudest, so the answer
			// does not depend on which stream happens to come last.
			if level > own {
				own = level
			}
			haveOwn = true
			continue
		}
		if level > best {
			best = level
		}
	}
	if !haveOwn || math.IsInf(best, -1) {
		return 0, false
	}
	return best - own, true
}

// EstimateCrosstalkThresholdDB reads this meeting's crosstalk threshold off its
// own gap distribution instead of using a constant.
//
// Where crosstalk lands is a property of the room and the gain staging, not of
// speech, so a value tuned on one corpus mis-fires on the next deployment. Two
// clusters are fitted and the split is accepted only when the upper one looks
// like a genuine crosstalk mode: enough mass, well separated, and tight. The
// tightness test is what stops it firing on cleanly isolated tracks, where the
// distribution is a monotone tail rather than a second mode — splitting that
// tail deletes real speech during overlap and measurably makes things worse.
//
// ok is false for "this meeting shows no crosstalk population", which is a
// correct and common answer, and means nothing should be dropped.
func EstimateCrosstalkThresholdDB(gaps []float64) (threshold float64, ok bool) {
	const (
		minDB             = 8.0
		minMass           = 0.005
		maxUpperSpreadDB  = 5.0
		minSeparationDB   = 12.0
		minUpperCount     = 5
		minSampleForSplit = 50
		// valleyHalfWidthDB / valleyMaxShare define "the cut lands in an empty
		// region": at most a tenth of the upper cluster's mass may sit within
		// this many dB either side of the threshold.
		valleyHalfWidthDB = 3.0
		valleyMaxShare    = 0.10
	)
	if len(gaps) < minSampleForSplit {
		return 0, false
	}
	var high []float64
	for _, g := range gaps {
		if g >= minDB {
			high = append(high, g)
		}
	}
	if len(high) < minUpperCount || float64(len(high))/float64(len(gaps)) < minMass {
		return 0, false
	}

	// Seed the two centres from the extremes of the WHOLE distribution, not of
	// the candidate subset: when every crosstalk word sits at the same level the
	// subset has zero range, and seeding from it collapses both centres onto the
	// same point so nothing is ever found.
	lo, hi := gaps[0], gaps[0]
	for _, g := range gaps {
		lo = math.Min(lo, g)
		hi = math.Max(hi, g)
	}
	if hi-lo < 1e-6 {
		return 0, false
	}
	centres := [2]float64{lo, hi}
	for iter := 0; iter < 50; iter++ {
		var sums, counts [2]float64
		for _, g := range gaps {
			j := 0
			if math.Abs(g-centres[1]) < math.Abs(g-centres[0]) {
				j = 1
			}
			sums[j] += g
			counts[j]++
		}
		for j := 0; j < 2; j++ {
			if counts[j] > 0 {
				centres[j] = sums[j] / counts[j]
			}
		}
		if centres[0] > centres[1] {
			centres[0], centres[1] = centres[1], centres[0]
		}
	}

	var upper []float64
	var lowerCount int
	for _, g := range gaps {
		if math.Abs(g-centres[1]) < math.Abs(g-centres[0]) {
			upper = append(upper, g)
		} else {
			lowerCount++
		}
	}
	if len(upper) < minUpperCount || lowerCount == 0 {
		return 0, false
	}
	if float64(len(upper))/float64(len(gaps)) < minMass {
		return 0, false
	}
	if centres[1]-centres[0] < minSeparationDB {
		return 0, false
	}
	if stddev(upper) > maxUpperSpreadDB {
		// a tail, not a mode
		return 0, false
	}
	threshold = math.Max((centres[0]+centres[1])/2, minDB)

	// A real bimodal distribution has an empty valley between the modes; a
	// monotone tail does not, and k-means will happily cut one anyway. Require
	// the neighbourhood of the cut to be nearly empty, or decline. Without this
	// the estimator fires on cleanly isolated tracks and deletes real speech
	// during genuine overlap.
	var inValley int
	for _, g := range gaps {
		if math.Abs(g-threshold) <= valleyHalfWidthDB {
			inValley++
		}
	}
	if float64(inValley) > valleyMaxShare*float64(len(upper)) {
		return 0, false
	}
	count := 0
	for _, g := range gaps {
		if g >= threshold {
			count++
		}
	}
	if count < minUpperCount {
		return 0, false
	}
	return threshold, true
}

func stddev(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	var mean float64
	for _, v := range values {
		mean += v
	}
	mean /= float64(len(values))
	var sum float64
	for _, v := range values {
		sum += (v - mean) * (v - mean)
	}
	return math.Sqrt(sum / float64(len(values)))
}

// AttributionResult reports what the stage measured, for provenance and logs.
type AttributionResult struct {
	// WordsMeasured is how many words got a gap.
	WordsMeasured int
	// ThresholdDB is the estimated crosstalk threshold, or 0 when this meeting
	// shows no crosstalk population.
	ThresholdDB float64
	// ThresholdFound distinguishes "estimated 0 dB" from "no estimate".
	ThresholdFound bool
	// Flagged is how many words sit at or above the threshold.
	Flagged int
	// Dropped is how many words were removed (0 unless dropping is enabled).
	Dropped int
}

// AnnotateAttribution measures every word against the envelopes and records the
// result on the word. When drop is true and this meeting shows an unambiguous
// crosstalk mode, flagged words are removed instead of merely marked.
//
// Segments whose words are all removed are dropped; surviving segments have
// their bounds and text rebuilt so the artifact stays internally consistent.
func AnnotateAttribution(segments []Segment, envelopes []*SpeakerEnvelope, drop bool) ([]Segment, AttributionResult) {
	var res AttributionResult
	if len(segments) == 0 || len(envelopes) == 0 {
		return segments, res
	}

	gaps := make([]float64, 0, 512)
	for si := range segments {
		for wi := range segments[si].Words {
			gap, ok := AttributionGapDB(segments[si].Words[wi], segments[si].SpeakerID, envelopes)
			if !ok {
				continue
			}
			segments[si].Words[wi].AttributionGapDB = gap
			segments[si].Words[wi].HasAttributionGap = true
			gaps = append(gaps, gap)
			res.WordsMeasured++
		}
	}
	if res.WordsMeasured == 0 {
		return segments, res
	}

	threshold, found := EstimateCrosstalkThresholdDB(gaps)
	res.ThresholdDB, res.ThresholdFound = threshold, found
	if !found {
		return segments, res
	}

	out := make([]Segment, 0, len(segments))
	for _, seg := range segments {
		kept := make([]Word, 0, len(seg.Words))
		for _, w := range seg.Words {
			if w.HasAttributionGap && w.AttributionGapDB >= threshold {
				res.Flagged++
				w.LowConfidenceSpeaker = true
				if drop {
					res.Dropped++
					continue
				}
			}
			kept = append(kept, w)
		}
		if len(kept) == 0 {
			continue
		}
		seg.Words = kept
		if drop {
			// Words can overlap, so the envelope is the running min/max over the
			// retained words — not the first word's start and the last word's
			// end. Taking the last word's end reintroduces the invalid-envelope
			// bug fixed in #216, and ValidateSegments rejects the result.
			start, end := kept[0].StartMS, kept[0].EndMS
			for _, w := range kept[1:] {
				if w.StartMS < start {
					start = w.StartMS
				}
				if w.EndMS > end {
					end = w.EndMS
				}
			}
			seg.StartMS, seg.EndMS = start, end
			seg.Text = joinWordText(kept)
		}
		out = append(out, seg)
	}
	return out, res
}

func joinWordText(words []Word) string {
	var b []byte
	for i, w := range words {
		if i > 0 {
			b = append(b, ' ')
		}
		b = append(b, w.Text...)
	}
	return string(b)
}

// WithoutLowConfidenceWords returns segments with words the attribution stage
// flagged removed, leaving the canonical transcript untouched.
//
// This is what makes the measurement pay off on the default path. The canonical
// transcript keeps every word — a human reading it can see, and overrule,
// anything marked — but a generated summary has no reader to overrule it. A
// fabricated interjection that reached the summariser becomes a decision
// somebody supposedly made, which is a far more expensive error than a greyed
// word in a viewer. Flags exist only where the meeting's own level distribution
// showed an unambiguous crosstalk population, so this removes nothing when there
// is nothing to remove.
//
// Segments left with no words are dropped; survivors get their bounds rebuilt
// from the words that remain.
func WithoutLowConfidenceWords(segments []Segment) ([]Segment, int) {
	var removed int
	out := make([]Segment, 0, len(segments))
	for _, seg := range segments {
		kept := make([]Word, 0, len(seg.Words))
		for _, w := range seg.Words {
			if w.LowConfidenceSpeaker {
				removed++
				continue
			}
			kept = append(kept, w)
		}
		if len(kept) == 0 {
			continue
		}
		if len(kept) != len(seg.Words) {
			start, end := kept[0].StartMS, kept[0].EndMS
			for _, w := range kept[1:] {
				if w.StartMS < start {
					start = w.StartMS
				}
				if w.EndMS > end {
					end = w.EndMS
				}
			}
			seg.StartMS, seg.EndMS = start, end
			seg.Text = joinWordText(kept)
		}
		seg.Words = kept
		out = append(out, seg)
	}
	return out, removed
}
