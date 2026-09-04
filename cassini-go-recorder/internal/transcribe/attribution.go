package transcribe

import (
	"fmt"
	"io"
	"math"
	"os/exec"
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
	// FrameDB is the log-RMS of each frame in dBFS, except where
	// capIngestedDynamicRange has raised the quietest frames of an ingested
	// speaker to the baseline the recorded tracks establish — below that, one
	// microphone's silence is not distinguishable from another's, and this
	// envelope exists to be compared with theirs.
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
	// over present frames only. SpeechDB is the speaker's speech reference: the
	// 97th percentile of present-frame level, pooled per logical speaker by
	// calibrateByLogicalSpeaker as the loudest any of their streams established
	// — a level this microphone is known to reach when its owner actually
	// speaks. Flaggability depends on it: a word may only be flagged when the
	// owner's own level sits well below this reference (see ownerQuietDuring).
	FloorDB  float64
	SpeechDB float64
	HopMS    int64
	// FromSourceAudio marks an envelope measured on a participant's spliced
	// render rather than on the track the SFU delivered. The two are not
	// interchangeable as evidence: see capIngestedDynamicRange.
	FromSourceAudio bool
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

// measurableStreams keeps the streams attribution can measure as independent
// evidence, and drops the two kinds it cannot.
//
// The merged-fallback speaker (Index < 0) is a synthetic mix of everyone: it
// has no track of its own to compare against and must never become a rival.
//
// A stream suppressed by source-audio ingestion is already inside another
// stream's envelope. The splice renders one file per participant spanning the
// whole meeting timeline — every one of their recorded streams, with their
// upload laid over it — and points ONE of their streams at that render while
// suppressing the siblings (sourceaudio.go). Measuring a suppressed sibling as
// well would give that participant two envelopes over the same audio: a
// duplicate rival for every word, and a second floor for
// calibrateByLogicalSpeaker to pool as if it were independent evidence.
func measurableStreams(streams []AudioStream) []AudioStream {
	keep := make([]AudioStream, 0, len(streams))
	for _, s := range streams {
		if s.Index < 0 || s.SuppressTranscription {
			continue
		}
		keep = append(keep, s)
	}
	return keep
}

// BuildSpeakerEnvelopes decodes each participant track once and reduces it to a
// level envelope on the shared timeline. This is cheap next to ASR: it is the
// same ffmpeg decode the transcription pass already performs, plus arithmetic.
//
// The decode is streamed: PCM is consumed from the ffmpeg pipe in small chunks
// and folded into frames as it arrives, so peak memory is O(frames) — a track
// never exists in memory as a whole PCM slice here. A 2-hour track is ~460 MB
// of float32 if materialised; its envelope is ~4 MB.
func BuildSpeakerEnvelopes(mkvPath string, streams []AudioStream, sampleRate int, progress io.Writer) ([]*SpeakerEnvelope, error) {
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	frame := sampleRate * attributionFrameMS / 1000
	hop := sampleRate * attributionHopMS / 1000
	if frame <= 0 || hop <= 0 {
		return nil, fmt.Errorf("attribution: bad frame/hop for sample rate %d", sampleRate)
	}

	streams = measurableStreams(streams)
	envelopes := make([]*SpeakerEnvelope, 0, len(streams))
	for _, stream := range streams {
		env, err := buildSpeakerEnvelopeStreaming(mkvPath, stream, frame, hop)
		if err != nil {
			return nil, fmt.Errorf("attribution: decode %s: %w", stream.SpeakerLabel, err)
		}
		env.FromSourceAudio = stream.SourceAudioPath != ""
		envelopes = append(envelopes, env)
	}
	calibrateByLogicalSpeaker(envelopes)
	capIngestedDynamicRange(envelopes)
	if progress != nil {
		for _, env := range envelopes {
			fmt.Fprintf(progress, "    envelope %s: floor %.1f dB, speech %.1f dB\n",
				env.SpeakerID, env.FloorDB, env.SpeechDB)
		}
	}
	return envelopes, nil
}

// calibrateByLogicalSpeaker gives every stream belonging to one participant
// that participant's floor, taken as the quietest floor any of their streams
// established.
//
// A noise floor is a property of somebody's microphone and room, not of a
// packet stream. Remux emits a fresh stream on every rotation or rejoin, and a
// short one can contain nothing but ongoing speech — calibrated alone, its own
// 20th percentile IS speech and its floor lands tens of dB too high. The
// speaker then measures as barely above "their own floor" while a quiet track
// carrying only their bleed measures well above its genuinely quiet one, and
// the bleed wins.
//
// Pooling every frame and taking one percentile does not survive this, because
// it is duration-weighted: five seconds of speech-only rotation swamps one
// second of the stream that established the real floor, and the pooled
// percentile lands in speech again. The quietest per-stream floor is the
// duration-independent answer — whichever stream actually saw this microphone
// idle is the one that knows where its floor is.
//
// Streams too short to characterise anything are ignored, so a fragment of a
// stream cannot drag a speaker's floor down.
func calibrateByLogicalSpeaker(envelopes []*SpeakerEnvelope) {
	type speakerLevels struct {
		floorDB  float64
		speechDB float64
		found    bool
	}
	levels := map[string]*speakerLevels{}
	for _, env := range envelopes {
		if env.presentFrames() < minCalibrationFrames {
			continue
		}
		agg, ok := levels[env.SpeakerID]
		if !ok {
			agg = &speakerLevels{floorDB: env.FloorDB, speechDB: env.SpeechDB, found: true}
			levels[env.SpeakerID] = agg
			continue
		}
		if env.FloorDB < agg.floorDB {
			agg.floorDB = env.FloorDB
		}
		if env.SpeechDB > agg.speechDB {
			agg.speechDB = env.SpeechDB
		}
	}
	for _, env := range envelopes {
		if agg, ok := levels[env.SpeakerID]; ok && agg.found {
			env.FloorDB = agg.floorDB
			env.SpeechDB = agg.speechDB
		}
	}
}

// minCalibrationFrames is ~160 ms at the standard hop: enough for a floor
// estimate to mean something, short enough that a brief rejoin still counts.
const minCalibrationFrames = 10

// presentFrames counts the frames that carry captured audio.
func (e *SpeakerEnvelope) presentFrames() int {
	if len(e.Present) != len(e.FrameDB) {
		return 0
	}
	var n int
	for _, ok := range e.Present {
		if ok {
			n++
		}
	}
	return n
}

// capIngestedDynamicRange stops a participant's own recording from making them
// everybody else's rival.
//
// Crosstalk is a relative judgement: a word is a bleed candidate because some
// OTHER track sat further above its own floor than the owning track sat above
// its own. That comparison assumes every track's floor was read off the same
// kind of silence. Ingestion breaks the assumption for one speaker at a time.
// The SFU track only exists where packets arrived, so its 20th percentile is
// read off whatever the network delivered — comfort noise, bleed, the quiet
// end of speech. The spliced render also holds the participant's own capture
// across the stretches the SFU sent nothing for, and that audio is continuous
// and undamaged, so its 20th percentile lands on the microphone's real noise
// floor, tens of dB lower. Every level on that track then measures that much
// further "above its own floor" than the same voice on a recorded track would,
// and the excess is not evidence about who was speaking.
//
// Measured on a synthetic three-speaker meeting where one speaker is spliced
// (attribution_bias_test.go): with the ingested floor 10 dB below the others'
// the crosstalk mode smears until EstimateCrosstalkThresholdDB declines
// altogether — 120 flagged crosstalk words become 0, silently, for the whole
// meeting. At 25 dB the same. So this is not a rounding difference; left alone
// it decides whether the stage works at all.
//
// The correction is the narrowest one that removes it: an ingested speaker may
// not be credited with a deeper usable range than the recorded tracks in the
// same meeting establish. Their levels still come from the audio the words were
// decoded from — that is the whole point of measuring the splice — but the
// baseline those levels are expressed against is the one everybody else is
// measured against too.
//
// Deliberately a no-op unless the meeting mixes the two kinds of evidence. A
// build with no uploads has no ingested envelope; a build where everybody
// uploaded has no recorded reference and no asymmetry to correct either.
func capIngestedDynamicRange(envelopes []*SpeakerEnvelope) {
	recorded := math.Inf(-1)
	for _, env := range envelopes {
		if env.FromSourceAudio || env.presentFrames() < minCalibrationFrames {
			continue
		}
		if snr := env.SpeechDB - env.FloorDB; snr > recorded {
			recorded = snr
		}
	}
	// No usable recorded reference, or one too shallow to be used as a
	// yardstick. Either way, leave the envelopes as measured.
	//
	// The lower bound is not arbitrary and it is not a tidiness check: it is
	// the range ownerQuietDuring needs to exist. Clamping this envelope's
	// frames up to SpeechDB-recorded bounds how far below their own speech
	// reference this speaker can ever measure — at recorded dB, exactly. A word
	// is only FLAGGABLE when its owner sits at least quietOwnerShortfallDB
	// below that reference, so a reference narrower than that would make every
	// word on this track unflaggable for the whole meeting: their ghosts would
	// become undetectable, quietly, as the price of correcting everybody
	// else's. That is a worse failure than the bias, so below this the
	// correction declines and the envelope stands as measured.
	//
	// A meeting where no recorded track shows even this much between its quiet
	// and its loud levels is also one where the recorded tracks are not a
	// usable yardstick for anything.
	if math.IsInf(recorded, -1) || recorded < quietOwnerShortfallDB {
		return
	}
	for _, env := range envelopes {
		if !env.FromSourceAudio {
			continue
		}
		floor := env.SpeechDB - recorded
		if floor <= env.FloorDB {
			continue
		}
		env.FloorDB = floor
		// The frames go up with it. Raising the floor alone would leave this
		// track's quiet stretches sitting BELOW their own floor, which reads as
		// "everybody else was louder than the owner" on exactly the windows
		// where nobody was speaking at all — a third population of gaps that
		// EstimateCrosstalkThresholdDB has to fit, and in the measurement it
		// either smeared the crosstalk mode away entirely (delta 15 dB: 120
		// flagged became 0) or joined it (delta 20-30 dB: 120 became 180).
		// Below this baseline one microphone's silence is not distinguishable
		// from another's, so it is not recorded as if it were.
		for i, db := range env.FrameDB {
			if db < floor {
				env.FrameDB[i] = floor
			}
		}
	}
}

// frameStats reduces one frame of samples to its dB level and presence.
//
// The RMS is computed over the samples that actually carry audio (non-zero),
// not over the whole frame. Timeline padding is exact digital silence, and a
// frame can straddle a padding boundary: a 20 ms DTX packet inside a 32 ms
// frame would otherwise be diluted by up to 20*log10(frame/covered) dB, which
// on a DTX-shaped track puts every present frame during silence several dB
// below the microphone's real floor — and min-pooling in
// calibrateByLogicalSpeaker then spreads that too-low floor to the whole
// speaker.
func frameStats(samples []float32) (db float64, present bool) {
	var sum float64
	nonZero := 0
	for _, s := range samples {
		if s != 0 {
			nonZero++
			sum += float64(s) * float64(s)
		}
	}
	var rms float64
	if nonZero > 0 {
		rms = math.Sqrt(sum / float64(nonZero))
	}
	// A frame of exact zeros is timeline padding, not captured silence:
	// real capture always carries at least dither. Anything genuinely
	// recorded, however quiet, has a non-zero sample somewhere in 32 ms.
	return 20 * math.Log10(rms+1e-12), nonZero > 0
}

// envelopeFromSamples is the pure core of the envelope computation, split out
// so the framing and calibration can be tested without ffmpeg or a fixture.
// The streaming path must produce exactly this, frame for frame.
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
		db, ok := frameStats(samples[i*hop : i*hop+frame])
		env.FrameDB[i] = db
		env.Present[i] = ok
		if ok {
			present = append(present, db)
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

// attributionReadChunkBytes is how much decoded PCM the streaming envelope
// reads from the ffmpeg pipe at a time. Small enough that the streaming path's
// total allocation stays far below the track's sample count, large enough to
// keep pipe overhead negligible.
const attributionReadChunkBytes = 16 * 1024

// buildSpeakerEnvelopeStreaming decodes one participant with the same ffmpeg
// invocation ExtractSpeakerFloats uses and folds the PCM into an envelope as it
// arrives. Peak allocation is O(frames) plus fixed chunk buffers — the audio is
// never materialised as a whole.
//
// The same invocation, deliberately: attribution must judge the audio the words
// were decoded FROM, or it is answering a different question than the one it
// reports on. A speaker whose upload was spliced in is transcribed from
// stream.SourceAudioPath, so measuring their recorded track instead scores a
// word recovered from the upload against whatever the SFU delivered there —
// nothing, where the track was silent. That reads as a quiet owner under
// somebody else's speech, which is exactly the crosstalk signature: the word is
// flagged, kept out of the summary, and under CASSINI_ATTRIBUTION_DROP deleted,
// while being plainly audible in the published mix that carries the same
// splice.
//
// The spliced file is already mono, 16 kHz and on the meeting timeline (it is
// the render the mix encodes, resampled), so it is read as a plain file and
// none of the sparse-timeline machinery applies — the same simplification
// ExtractSpeakerFloats makes. ExtractStreamFloatsAt keeps its own contract, a
// caller asking for a recorded track gets the recorded track: the splice's own
// floor is a different need from this one.
func buildSpeakerEnvelopeStreaming(mkvPath string, stream AudioStream, frame, hop int) (*SpeakerEnvelope, error) {
	args := []string{
		"-v", "error",
		"-y",
	}
	if stream.SourceAudioPath != "" {
		args = append(args, "-i", stream.SourceAudioPath)
	} else {
		args = append(args, "-i", mkvPath)
		args = append(args, sparseTimelineDecodeArgs(stream, 16000)...)
	}
	args = append(args,
		"-vn",
		"-sn",
		"-dn",
		"-ac", "1",
		"-ar", "16000",
		"-f", "s16le",
		"pipe:1",
	)
	cmd := exec.Command("ffmpeg", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open PCM pipe: %w", err)
	}
	var stderr boundedBuffer
	stderr.limit = 8192
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}

	builder := newEnvelopeBuilder(stream.SpeakerID, frame, hop, int64(attributionHopMS))
	chunkSamples := attributionReadChunkBytes / 2
	raw := make([]byte, attributionReadChunkBytes)
	floats := make([]float32, chunkSamples)
	var readErr error
	for {
		n, err := io.ReadFull(stdout, raw)
		if n%2 != 0 {
			readErr = fmt.Errorf("decoded PCM has odd byte count")
			break
		}
		if n > 0 {
			count := n / 2
			for i := 0; i < count; i++ {
				lo := raw[i*2]
				hi := raw[i*2+1]
				s16 := int16(uint16(lo) | uint16(hi)<<8)
				floats[i] = float32(s16) / 32768.0
			}
			builder.push(floats[:count])
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			readErr = fmt.Errorf("read decoded PCM: %w", err)
			break
		}
	}
	if readErr != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if readErr != nil {
		return nil, readErr
	}
	if waitErr != nil {
		return nil, fmt.Errorf("ffmpeg: %w\n%s", waitErr, truncate(stderr.String(), 800))
	}
	return builder.finish(), nil
}

// envelopeBuilder folds streamed PCM into overlapping frames incrementally,
// producing exactly what envelopeFromSamples produces on the concatenation of
// every pushed chunk. It retains at most frame-1 carried samples plus one
// chunk, never the track.
type envelopeBuilder struct {
	frame, hop int
	env        *SpeakerEnvelope
	presentDB  []float64
	work       []float32
}

func newEnvelopeBuilder(speakerID string, frame, hop int, hopMS int64) *envelopeBuilder {
	return &envelopeBuilder{
		frame: frame,
		hop:   hop,
		env:   &SpeakerEnvelope{SpeakerID: speakerID, HopMS: hopMS},
		work:  make([]float32, 0, frame+attributionReadChunkBytes/2),
	}
}

func (b *envelopeBuilder) push(chunk []float32) {
	b.work = append(b.work, chunk...)
	off := 0
	for off+b.frame <= len(b.work) {
		db, ok := frameStats(b.work[off : off+b.frame])
		b.env.FrameDB = append(b.env.FrameDB, db)
		b.env.Present = append(b.env.Present, ok)
		if ok {
			b.presentDB = append(b.presentDB, db)
		}
		off += b.hop
	}
	// Keep the tail that has not yet completed a frame (always < frame
	// samples), moved to the front of the fixed-capacity buffer so no append
	// ever reallocates.
	rem := copy(b.work, b.work[off:])
	b.work = b.work[:rem]
}

func (b *envelopeBuilder) finish() *SpeakerEnvelope {
	// The final partial window (fewer than frame samples) is discarded, the
	// same as envelopeFromSamples' 1+(len-frame)/hop framing.
	if len(b.presentDB) == 0 {
		// No captured audio at all: levels stay zero and the mask all-false,
		// so aboveFloor declines every window and the track never claims a
		// word. Matches envelopeFromSamples.
		return b.env
	}
	b.env.FloorDB = percentileOf(b.presentDB, attributionFloorPercentile)
	b.env.SpeechDB = percentileOf(b.presentDB, attributionSpeechPercentile)
	return b.env
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
		// outlierTrimPercentile: gaps above this percentile are excluded from
		// the estimate (not from flagging — the threshold still applies to
		// every word). A handful of words measured while a rival was shouting
		// sit far above the genuine crosstalk mode; left in, they capture the
		// upper k-means centre, the mode is absorbed into the lower cluster,
		// and a populated, tight, well-separated crosstalk mode either gets a
		// wrong threshold above it or is declined for looking too spread out.
		outlierTrimPercentile = 99.0
	)
	if len(gaps) > 0 {
		cut := percentileOf(gaps, outlierTrimPercentile)
		trimmed := make([]float64, 0, len(gaps))
		for _, g := range gaps {
			if g <= cut {
				trimmed = append(trimmed, g)
			}
		}
		gaps = trimmed
	}
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

	// Seed the two centres from the extremes of the outlier-trimmed
	// distribution, not of the candidate subset: when every crosstalk word
	// sits at the same level the subset has zero range, and seeding from it
	// collapses both centres onto the same point so nothing is ever found.
	// Trimming above is what makes the max a usable seed — untrimmed, five
	// far outliers own the upper centre from iteration one.
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

// quietOwnerShortfallDB is how far below the owner's own speech reference
// (SpeechDB) a word's own-track level must sit for the word to be FLAGGABLE.
//
// The gap alone cannot separate ghosts from genuine double-talk: with
// asymmetric microphones (a close mic at ~38 dB SNR against a laptop mic at
// ~27 dB) the interrupted speaker's REAL words measure +10..+15 dB — the same
// band real hallucinated runs occupy. What does separate them is the owner's
// own activity: a ghost word rides bleed on a track whose owner was NOT
// speaking, and bleed typically sits 15-20 dB or more below the owner's own
// speech level, while genuine speech — measured as the mean of the loudest
// half of a padded word window — stays within ~10 dB of the p97 reference
// even for soft words. 15 dB sits above the one and at the bottom of the
// other, and errs toward keeping real words unflaggable: the expensive
// mistake is flagging (and, in drop mode, deleting) a real word, not leaving
// a ghost merely annotated.
const quietOwnerShortfallDB = 15.0

// ownerQuietDuring reports whether the word's owner was quiet over the word
// window: their own level, on the loudest of their measurable streams, sits at
// least quietOwnerShortfallDB below their pooled speech reference. This is the
// flaggability precondition — the gap stays annotated on every measured word
// as ranking evidence, but only words carrying the true ghost signature
// (owner not speaking) may ever be flagged or dropped.
//
// A speaker whose streams never establish a reference above their bleed level
// (they never spoke anywhere in the meeting) can never satisfy the shortfall,
// so their words are never flaggable. That error goes in the safe direction:
// missed ghosts stay annotated; real words are never deleted.
func ownerQuietDuring(word Word, speakerID string, envelopes []*SpeakerEnvelope) bool {
	quiet := false
	for _, env := range envelopes {
		if env.SpeakerID != speakerID {
			continue
		}
		above, ok := env.aboveFloor(word.StartMS, word.EndMS)
		if !ok {
			continue
		}
		// Absolute own level over the window is above+FloorDB; the shortfall
		// against the pooled reference is independent of the floor.
		if env.SpeechDB-(env.FloorDB+above) < quietOwnerShortfallDB {
			// One of the owner's streams shows them at or near speech level:
			// they were talking, whatever any rival was doing.
			return false
		}
		quiet = true
	}
	return quiet
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
// Every measured word keeps its gap as ranking evidence, but only words whose
// owner was quiet (ownerQuietDuring) are FLAGGABLE, and the threshold is
// estimated on that subset's gaps alone. Genuine double-talk under asymmetric
// microphone SNR produces real words at +10..+15 dB — inside the ghost band —
// and mixing them into the estimate either got them flagged or smeared the
// upper cluster until the tightness gate declined and nothing was ever
// flagged on real speech. The owner's own activity is the discriminator the
// gap lacks.
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
			res.WordsMeasured++
			// Only flaggable words feed the estimator: gaps measured while the
			// owner was audibly speaking are double-talk evidence, not
			// crosstalk candidates, and they contaminate the fit.
			if ownerQuietDuring(segments[si].Words[wi], segments[si].SpeakerID, envelopes) {
				gaps = append(gaps, gap)
			}
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
		if len(seg.Words) == 0 {
			out = append(out, seg)
			continue
		}
		kept := make([]Word, 0, len(seg.Words))
		for _, w := range seg.Words {
			if w.HasAttributionGap && w.AttributionGapDB >= threshold &&
				ownerQuietDuring(w, seg.SpeakerID, envelopes) {
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
		if len(seg.Words) == 0 {
			// A legacy segment carries text but no word list. There is nothing
			// to filter and nothing to judge it on, so it passes through: it
			// must not disappear because some other segment was flagged.
			out = append(out, seg)
			continue
		}
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

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
