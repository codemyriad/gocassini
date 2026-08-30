package transcribe

// The waveform challenge miner is deliberately independent of speech
// recognition. It decodes one physical participant track at a time, reduces
// each 32 ms PCM window to one RMS value, and keeps only those summaries. This
// makes its memory use proportional to meeting duration and logical speaker
// count rather than decoded PCM size. Its output is review evidence only: it
// never changes canonical words, timestamps, or speaker attribution.

import (
	"bytes"
	"container/heap"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	challengeSampleRate       = 16000
	challengeFrameSamples     = 512
	challengeFrameMS          = challengeFrameSamples * 1000 / challengeSampleRate // 32 ms
	challengeSilenceDB        = -120.0
	challengeMinIslandFrames  = 4 // 128 ms: reject isolated clicks
	challengeCloseGapFrames   = 3 // bridge <=96 ms within an utterance
	challengeOverlapFrames    = 7 // 224 ms
	challengeMaxCandidates    = 20
	challengeContextMS        = 4000
	challengeMinWindowMS      = 10000
	challengeMaxWindowMS      = 20000
	challengeSurroundingGapMS = 750
	// Keep evidence bounded before candidate construction. The per-key limit
	// prevents one noisy speaker pair from consuming the review budget, while
	// the global limit also bounds meetings with very many participants.
	challengeMaxEventsPerKey = 32
	challengeMaxEvents       = 512
	challengeContextStride   = 256
)

type challengeTrackStats struct {
	SpeakerID         string  `json:"speakerId"`
	SpeakerLabel      string  `json:"speakerLabel"`
	PhysicalStreams   int     `json:"physicalStreams"`
	FrameCount        int     `json:"frameCount"`
	SpeechReferenceDB float64 `json:"speechReferenceDb"`
	NoiseFloorDB      float64 `json:"noiseFloorDb"`
	ActiveThresholdDB float64 `json:"activeThresholdDb"`
}

type challengeEvidence struct {
	Kind               string  `json:"kind"`
	StartMS            int64   `json:"startMs"`
	EndMS              int64   `json:"endMs"`
	SpeakerID          string  `json:"speakerId,omitempty"`
	CompetingSpeakerID string  `json:"competingSpeakerId,omitempty"`
	SegmentIndex       *int    `json:"segmentIndex,omitempty"`
	OwnerRMSDB         float64 `json:"ownerRmsDb,omitempty"`
	CompetingRMSDB     float64 `json:"competingRmsDb,omitempty"`
	LevelDeltaDB       float64 `json:"levelDeltaDb,omitempty"`
	OwnerActiveRatio   float64 `json:"ownerActiveRatio,omitempty"`
	OtherActiveRatio   float64 `json:"competingActiveRatio,omitempty"`
	DurationMS         int64   `json:"durationMs"`
	Score              float64 `json:"score"`
}

type challengeCandidate struct {
	ID       string              `json:"id"`
	StartMS  int64               `json:"startMs"`
	EndMS    int64               `json:"endMs"`
	Score    float64             `json:"score"`
	Reasons  []string            `json:"reasons"`
	Evidence []challengeEvidence `json:"evidence"`
}

type waveformChallengesFile struct {
	Version                string                `json:"version"`
	GeneratedAt            string                `json:"generatedAt"`
	ReviewOnly             bool                  `json:"reviewOnly"`
	SourceAudio            string                `json:"sourceAudio"`
	SourceAudioSHA256      string                `json:"sourceAudioSha256,omitempty"`
	SourceTranscript       string                `json:"sourceTranscript,omitempty"`
	SourceTranscriptSHA256 string                `json:"sourceTranscriptSha256,omitempty"`
	FrameMS                int                   `json:"frameMs"`
	MaxCandidates          int                   `json:"maxCandidates"`
	Tracks                 []challengeTrackStats `json:"tracks"`
	Candidates             []challengeCandidate  `json:"candidates"`
}

// WaveformChallengeProvenance binds a standalone miner result to the exact
// input bytes it reviewed. The normal build path can omit it because its
// sidecar and transcript are produced together inside one artifact build.
type WaveformChallengeProvenance struct {
	AudioSHA256      string
	TranscriptPath   string
	TranscriptSHA256 string
}

type speakerEnvelope struct {
	speakerID       string
	speakerLabel    string
	physicalStreams int
	rmsDB           []float64
	speechRefDB     float64
	noiseFloorDB    float64
	thresholdDB     float64
	active          []bool
}

type boundedChallengeItem struct {
	event challengeEvidence
	key   string
	index int
}

// boundedChallengeHeap keeps the least useful retained event at index zero.
// That makes admission O(log N) once the fixed-size global budget is full.
type boundedChallengeHeap []*boundedChallengeItem

func (h boundedChallengeHeap) Len() int { return len(h) }
func (h boundedChallengeHeap) Less(i, j int) bool {
	return challengeEventBetter(h[j].event, h[i].event)
}
func (h boundedChallengeHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}
func (h *boundedChallengeHeap) Push(value any) {
	item := value.(*boundedChallengeItem)
	item.index = len(*h)
	*h = append(*h, item)
}
func (h *boundedChallengeHeap) Pop() any {
	old := *h
	last := len(old) - 1
	item := old[last]
	old[last] = nil
	item.index = -1
	*h = old[:last]
	return item
}

type boundedChallengeEvents struct {
	items  boundedChallengeHeap
	byKey  map[string][]*boundedChallengeItem
	perKey int
	limit  int
}

func newBoundedChallengeEvents(perKey, limit int) *boundedChallengeEvents {
	return &boundedChallengeEvents{
		byKey:  make(map[string][]*boundedChallengeItem),
		perKey: perKey,
		limit:  limit,
	}
}

func (c *boundedChallengeEvents) Add(event challengeEvidence) {
	if c.perKey <= 0 || c.limit <= 0 {
		return
	}
	key := challengeEventKey(event)
	bucket := c.byKey[key]
	if len(bucket) >= c.perKey {
		worst := bucket[0]
		for _, item := range bucket[1:] {
			if challengeEventBetter(worst.event, item.event) {
				worst = item
			}
		}
		if !challengeEventBetter(event, worst.event) {
			return
		}
		worst.event = event
		heap.Fix(&c.items, worst.index)
		return
	}

	if len(c.items) < c.limit {
		item := &boundedChallengeItem{event: event, key: key}
		heap.Push(&c.items, item)
		c.byKey[key] = append(bucket, item)
		return
	}

	worst := c.items[0]
	if !challengeEventBetter(event, worst.event) {
		return
	}
	c.removeFromBucket(worst)
	worst.event = event
	worst.key = key
	c.byKey[key] = append(c.byKey[key], worst)
	heap.Fix(&c.items, worst.index)
}

func (c *boundedChallengeEvents) removeFromBucket(item *boundedChallengeItem) {
	bucket := c.byKey[item.key]
	for i, candidate := range bucket {
		if candidate != item {
			continue
		}
		bucket[i] = bucket[len(bucket)-1]
		bucket = bucket[:len(bucket)-1]
		break
	}
	if len(bucket) == 0 {
		delete(c.byKey, item.key)
		return
	}
	c.byKey[item.key] = bucket
}

func (c *boundedChallengeEvents) Events() []challengeEvidence {
	events := make([]challengeEvidence, len(c.items))
	for i, item := range c.items {
		events[i] = item.event
	}
	sort.Slice(events, func(i, j int) bool {
		return challengeEventBetter(events[i], events[j])
	})
	return events
}

func challengeEventKey(event challengeEvidence) string {
	left, right := event.SpeakerID, event.CompetingSpeakerID
	if right != "" && right < left {
		left, right = right, left
	}
	return event.Kind + "\x00" + left + "\x00" + right
}

// challengeEventBetter is a total deterministic preference order. High-score
// evidence wins; ties prefer earlier, shorter windows and then stable metadata.
func challengeEventBetter(left, right challengeEvidence) bool {
	if left.Score != right.Score {
		return left.Score > right.Score
	}
	if left.StartMS != right.StartMS {
		return left.StartMS < right.StartMS
	}
	if left.EndMS != right.EndMS {
		return left.EndMS < right.EndMS
	}
	if left.Kind != right.Kind {
		return left.Kind < right.Kind
	}
	if left.SpeakerID != right.SpeakerID {
		return left.SpeakerID < right.SpeakerID
	}
	if left.CompetingSpeakerID != right.CompetingSpeakerID {
		return left.CompetingSpeakerID < right.CompetingSpeakerID
	}
	leftSegment, rightSegment := challengeSegmentIndexValue(left), challengeSegmentIndexValue(right)
	if leftSegment != rightSegment {
		return leftSegment < rightSegment
	}
	if left.DurationMS != right.DurationMS {
		return left.DurationMS < right.DurationMS
	}
	if left.LevelDeltaDB != right.LevelDeltaDB {
		return left.LevelDeltaDB > right.LevelDeltaDB
	}
	if left.OwnerRMSDB != right.OwnerRMSDB {
		return left.OwnerRMSDB < right.OwnerRMSDB
	}
	if left.CompetingRMSDB != right.CompetingRMSDB {
		return left.CompetingRMSDB > right.CompetingRMSDB
	}
	if left.OwnerActiveRatio != right.OwnerActiveRatio {
		return left.OwnerActiveRatio < right.OwnerActiveRatio
	}
	return left.OtherActiveRatio > right.OtherActiveRatio
}

func challengeSegmentIndexValue(event challengeEvidence) int {
	if event.SegmentIndex == nil {
		return -1
	}
	return *event.SegmentIndex
}

func challengeSegmentIndex(value int) *int { return &value }

func checkChallengeContext(ctx context.Context, iteration int) error {
	if iteration%challengeContextStride != 0 {
		return nil
	}
	return ctx.Err()
}

// WriteWaveformChallenges mines suspicious waveform/transcript intersections
// and writes a review-only sidecar. Physical streams sharing a stable logical
// SpeakerID (for example after a participant rejoins) are merged before any
// overlap calculation, so a rejoin cannot be mistaken for two speakers.
func WriteWaveformChallenges(ctx context.Context, path, mkvPath string, streams []AudioStream, segments []Segment, durationMS int64) error {
	return WriteWaveformChallengesWithProvenance(ctx, path, mkvPath, streams, segments, durationMS, WaveformChallengeProvenance{})
}

// WriteWaveformChallengesWithProvenance is WriteWaveformChallenges plus exact
// source hashes for independently rerunnable audits.
func WriteWaveformChallengesWithProvenance(ctx context.Context, path, mkvPath string, streams []AudioStream, segments []Segment, durationMS int64, provenance WaveformChallengeProvenance) error {
	envelopes := make(map[string]*speakerEnvelope)
	for streamIndex, stream := range streams {
		if err := checkChallengeContext(ctx, streamIndex); err != nil {
			return err
		}
		// Synthetic fallback streams are not present in the source MKV.
		if stream.Index < 0 || strings.TrimSpace(stream.SpeakerID) == "" {
			continue
		}
		envelope := envelopes[stream.SpeakerID]
		if envelope == nil {
			envelope = &speakerEnvelope{
				speakerID:    stream.SpeakerID,
				speakerLabel: stream.SpeakerLabel,
			}
			envelopes[stream.SpeakerID] = envelope
		}
		envelope.physicalStreams++
		frameIndex := 0
		err := streamSpeakerRMS(ctx, mkvPath, stream, func(rmsDB float64) {
			mergeEnvelopeFrame(envelope, frameIndex, rmsDB)
			frameIndex++
		})
		if err != nil {
			return fmt.Errorf("summarize %s (stream %d): %w", stream.SpeakerLabel, stream.Index, err)
		}
	}

	ordered := make([]*speakerEnvelope, 0, len(envelopes))
	calibrationIndex := 0
	for _, envelope := range envelopes {
		if err := calibrateEnvelopeContext(ctx, envelope); err != nil {
			return err
		}
		ordered = append(ordered, envelope)
		calibrationIndex++
		if err := checkChallengeContext(ctx, calibrationIndex); err != nil {
			return err
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].speakerID < ordered[j].speakerID })

	events, err := mineChallengeEventsContext(ctx, ordered, segments)
	if err != nil {
		return err
	}
	candidates, err := buildChallengeCandidatesContext(ctx, events, durationMS, challengeMaxCandidates)
	if err != nil {
		return err
	}
	trackStats := make([]challengeTrackStats, 0, len(ordered))
	for envelopeIndex, envelope := range ordered {
		if err := checkChallengeContext(ctx, envelopeIndex); err != nil {
			return err
		}
		trackStats = append(trackStats, challengeTrackStats{
			SpeakerID:         envelope.speakerID,
			SpeakerLabel:      envelope.speakerLabel,
			PhysicalStreams:   envelope.physicalStreams,
			FrameCount:        len(envelope.rmsDB),
			SpeechReferenceDB: roundDB(envelope.speechRefDB),
			NoiseFloorDB:      roundDB(envelope.noiseFloorDB),
			ActiveThresholdDB: roundDB(envelope.thresholdDB),
		})
	}
	if trackStats == nil {
		trackStats = []challengeTrackStats{}
	}
	if candidates == nil {
		candidates = []challengeCandidate{}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	sourceTranscript := ""
	if strings.TrimSpace(provenance.TranscriptPath) != "" {
		sourceTranscript = filepath.Base(provenance.TranscriptPath)
	}
	return writeJSON(path, waveformChallengesFile{
		Version:                "cassini.waveform-challenges.v1",
		GeneratedAt:            time.Now().UTC().Format(time.RFC3339),
		ReviewOnly:             true,
		SourceAudio:            filepath.Base(mkvPath),
		SourceAudioSHA256:      strings.TrimSpace(provenance.AudioSHA256),
		SourceTranscript:       sourceTranscript,
		SourceTranscriptSHA256: strings.TrimSpace(provenance.TranscriptSHA256),
		FrameMS:                challengeFrameMS,
		MaxCandidates:          challengeMaxCandidates,
		Tracks:                 trackStats,
		Candidates:             candidates,
	})
}

func mergeEnvelopeFrame(envelope *speakerEnvelope, frameIndex int, rmsDB float64) {
	if frameIndex >= len(envelope.rmsDB) {
		oldLen := len(envelope.rmsDB)
		envelope.rmsDB = append(envelope.rmsDB, make([]float64, frameIndex-oldLen+1)...)
		for i := oldLen; i < len(envelope.rmsDB); i++ {
			envelope.rmsDB[i] = challengeSilenceDB
		}
	}
	if rmsDB > envelope.rmsDB[frameIndex] {
		envelope.rmsDB[frameIndex] = rmsDB
	}
}

// streamSpeakerRMS uses a fixed-size pipe buffer and invokes visit once per
// 32 ms frame. In particular, it does not use exec.Cmd.Output (whole decoded
// byte stream) or construct a meeting-length []float32.
func streamSpeakerRMS(ctx context.Context, mkvPath string, stream AudioStream, visit func(float64)) error {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-v", "error",
		"-i", mkvPath,
		"-map", fmt.Sprintf("0:%d", stream.Index),
		"-vn", "-sn", "-dn",
		"-af", sparseTimelineAudioFilter(),
		"-ac", "1",
		"-ar", fmt.Sprint(challengeSampleRate),
		"-f", "s16le",
		"pipe:1",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr limitedBuffer
	stderr.limit = 8192
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	readErr := summarizePCM16(stdout, challengeFrameSamples, visit)
	waitErr := cmd.Wait()
	if readErr != nil {
		return readErr
	}
	if waitErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("ffmpeg PCM decode: %w: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// summarizePCM16 consumes little-endian mono s16 PCM using one reusable frame
// buffer. It is split from the FFmpeg process wrapper so bounded allocation can
// be pinned without generating a large audio fixture.
func summarizePCM16(r io.Reader, frameSamples int, visit func(float64)) error {
	if frameSamples <= 0 {
		return fmt.Errorf("invalid PCM frame size %d", frameSamples)
	}
	frame := make([]byte, frameSamples*2)
	for {
		n, err := io.ReadFull(r, frame)
		if n > 0 {
			sampleCount := n / 2
			if sampleCount > 0 {
				var sumSquares float64
				for off := 0; off+1 < n; off += 2 {
					sample := int16(binary.LittleEndian.Uint16(frame[off : off+2]))
					normalized := float64(sample) / 32768.0
					sumSquares += normalized * normalized
				}
				meanSquare := sumSquares / float64(sampleCount)
				rmsDB := challengeSilenceDB
				if meanSquare > 0 {
					rmsDB = 10 * math.Log10(meanSquare)
					if rmsDB < challengeSilenceDB {
						rmsDB = challengeSilenceDB
					}
				}
				visit(rmsDB)
			}
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	originalLen := len(p)
	if remaining := b.limit - b.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return originalLen, nil
}

func calibrateEnvelope(envelope *speakerEnvelope) {
	_ = calibrateEnvelopeContext(context.Background(), envelope)
}

func calibrateEnvelopeContext(ctx context.Context, envelope *speakerEnvelope) error {
	values := make([]float64, 0, len(envelope.rmsDB))
	for i, value := range envelope.rmsDB {
		if err := checkChallengeContext(ctx, i); err != nil {
			return err
		}
		if value > -90 { // discard materialized digital silence only
			values = append(values, value)
		}
	}
	if len(values) == 0 {
		envelope.speechRefDB = -60
		envelope.noiseFloorDB = -90
		envelope.thresholdDB = -55
		envelope.active = make([]bool, len(envelope.rmsDB))
		return nil
	}
	sort.Float64s(values)
	if err := ctx.Err(); err != nil {
		return err
	}
	envelope.speechRefDB = percentileSorted(values, 0.90)
	envelope.noiseFloorDB = percentileSorted(values, 0.20)
	// A flat low-level track has no defensible speech/noise separation. Mark it
	// inactive rather than turning codec hiss into meeting-long overlap. Direct
	// speech normally has substantially more than 6 dB of frame-level range.
	if envelope.speechRefDB-envelope.noiseFloorDB < 6 {
		envelope.thresholdDB = envelope.speechRefDB + 1
		envelope.active = make([]bool, len(envelope.rmsDB))
		return nil
	}
	// The absolute -55 dBFS floor prevents tiny codec noise from becoming an
	// activity island. Relative terms retain legitimately quiet microphones.
	envelope.thresholdDB = maxFloat(envelope.noiseFloorDB+8, envelope.speechRefDB-30, -55)
	// A DTX track may contain almost nothing but direct speech, making its P20
	// a poor noise estimate. Always leave at least 6 dB below the speech P90.
	if ceiling := envelope.speechRefDB - 6; envelope.thresholdDB > ceiling {
		envelope.thresholdDB = ceiling
	}
	raw := make([]bool, len(envelope.rmsDB))
	for i, value := range envelope.rmsDB {
		if err := checkChallengeContext(ctx, i); err != nil {
			return err
		}
		raw[i] = value >= envelope.thresholdDB
	}
	if err := closeActivityGapsContext(ctx, raw, challengeCloseGapFrames); err != nil {
		return err
	}
	if err := removeShortActivityContext(ctx, raw, challengeMinIslandFrames); err != nil {
		return err
	}
	envelope.active = raw
	return nil
}

func percentileSorted(values []float64, quantile float64) float64 {
	if len(values) == 0 {
		return challengeSilenceDB
	}
	if quantile <= 0 {
		return values[0]
	}
	if quantile >= 1 {
		return values[len(values)-1]
	}
	position := quantile * float64(len(values)-1)
	lo := int(math.Floor(position))
	hi := int(math.Ceil(position))
	if lo == hi {
		return values[lo]
	}
	fraction := position - float64(lo)
	return values[lo]*(1-fraction) + values[hi]*fraction
}

func closeActivityGaps(active []bool, maxGap int) {
	_ = closeActivityGapsContext(context.Background(), active, maxGap)
}

func closeActivityGapsContext(ctx context.Context, active []bool, maxGap int) error {
	for i := 0; i < len(active); {
		if err := checkChallengeContext(ctx, i); err != nil {
			return err
		}
		if active[i] {
			i++
			continue
		}
		start := i
		for i < len(active) && !active[i] {
			if err := checkChallengeContext(ctx, i); err != nil {
				return err
			}
			i++
		}
		if start > 0 && i < len(active) && i-start <= maxGap {
			for j := start; j < i; j++ {
				active[j] = true
			}
		}
	}
	return nil
}

func removeShortActivity(active []bool, minFrames int) {
	_ = removeShortActivityContext(context.Background(), active, minFrames)
}

func removeShortActivityContext(ctx context.Context, active []bool, minFrames int) error {
	for i := 0; i < len(active); {
		if err := checkChallengeContext(ctx, i); err != nil {
			return err
		}
		if !active[i] {
			i++
			continue
		}
		start := i
		for i < len(active) && active[i] {
			if err := checkChallengeContext(ctx, i); err != nil {
				return err
			}
			i++
		}
		if i-start < minFrames {
			for j := start; j < i; j++ {
				active[j] = false
			}
		}
	}
	return nil
}

func mineChallengeEvents(envelopes []*speakerEnvelope, segments []Segment) []challengeEvidence {
	events, _ := mineChallengeEventsContext(context.Background(), envelopes, segments)
	return events
}

func mineChallengeEventsContext(ctx context.Context, envelopes []*speakerEnvelope, segments []Segment) ([]challengeEvidence, error) {
	bySpeaker := make(map[string]*speakerEnvelope, len(envelopes))
	for i, envelope := range envelopes {
		if err := checkChallengeContext(ctx, i); err != nil {
			return nil, err
		}
		bySpeaker[envelope.speakerID] = envelope
	}
	events := newBoundedChallengeEvents(challengeMaxEventsPerKey, challengeMaxEvents)

	// Transcript-derived short turns. Surrounding words make this work for
	// both legacy containing segments and the newer chronological A/B/A split.
	for segmentIndex, segment := range segments {
		if err := checkChallengeContext(ctx, segmentIndex); err != nil {
			return nil, err
		}
		durationMS := segment.EndMS - segment.StartMS
		if durationMS < 200 || durationMS > 2500 || segment.SpeakerID == "" {
			continue
		}
		nested, err := isNestedShortTurnContext(ctx, segmentIndex, segments)
		if err != nil {
			return nil, err
		}
		if nested {
			score := 55 + minFloat(20, float64(2500-durationMS)/115)
			events.Add(challengeEvidence{
				Kind:         "short_nested_turn",
				StartMS:      segment.StartMS,
				EndMS:        segment.EndMS,
				SpeakerID:    segment.SpeakerID,
				SegmentIndex: challengeSegmentIndex(segmentIndex),
				DurationMS:   durationMS,
				Score:        score,
			})
		}

		owner := bySpeaker[segment.SpeakerID]
		if owner == nil {
			continue
		}
		ownerRMS, ownerActive := envelopeIntervalStats(owner, segment.StartMS, segment.EndMS)
		var competitor *speakerEnvelope
		competitorRMS := challengeSilenceDB
		competitorActive := 0.0
		for otherIndex, other := range envelopes {
			if err := checkChallengeContext(ctx, otherIndex); err != nil {
				return nil, err
			}
			if other.speakerID == owner.speakerID {
				continue
			}
			rms, ratio := envelopeIntervalStats(other, segment.StartMS, segment.EndMS)
			if ratio > competitorActive || (ratio == competitorActive && rms > competitorRMS) {
				competitor = other
				competitorRMS = rms
				competitorActive = ratio
			}
		}
		delta := competitorRMS - ownerRMS
		// Conservative by design. The known leakage examples clear this gate by
		// 34-38 dB; a 12 dB requirement avoids judging ordinary gain imbalance.
		if competitor != nil && ownerActive < 0.25 && competitorActive >= 0.60 && delta >= 12 {
			score := 60 + minFloat(25, (delta-12)*1.25)
			events.Add(challengeEvidence{
				Kind:               "attribution_mismatch",
				StartMS:            segment.StartMS,
				EndMS:              segment.EndMS,
				SpeakerID:          segment.SpeakerID,
				CompetingSpeakerID: competitor.speakerID,
				SegmentIndex:       challengeSegmentIndex(segmentIndex),
				OwnerRMSDB:         roundDB(ownerRMS),
				CompetingRMSDB:     roundDB(competitorRMS),
				LevelDeltaDB:       roundDB(delta),
				OwnerActiveRatio:   roundRatio(ownerActive),
				OtherActiveRatio:   roundRatio(competitorActive),
				DurationMS:         durationMS,
				Score:              score,
			})
		}
	}

	// Energy overlap is evaluated only after rejoin streams have been merged by
	// logical speaker ID.
	for i := 0; i < len(envelopes); i++ {
		if err := checkChallengeContext(ctx, i); err != nil {
			return nil, err
		}
		for j := i + 1; j < len(envelopes); j++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			left, right := envelopes[i], envelopes[j]
			limit := minInt(len(left.active), len(right.active))
			for frame := 0; frame < limit; {
				if err := checkChallengeContext(ctx, frame); err != nil {
					return nil, err
				}
				if !left.active[frame] || !right.active[frame] {
					frame++
					continue
				}
				start := frame
				for frame < limit && left.active[frame] && right.active[frame] {
					if err := checkChallengeContext(ctx, frame); err != nil {
						return nil, err
					}
					frame++
				}
				if frame-start < challengeOverlapFrames {
					continue
				}
				startMS := int64(start * challengeFrameMS)
				endMS := int64(frame * challengeFrameMS)
				durationMS := endMS - startMS
				events.Add(challengeEvidence{
					Kind:               "acoustic_overlap",
					StartMS:            startMS,
					EndMS:              endMS,
					SpeakerID:          left.speakerID,
					CompetingSpeakerID: right.speakerID,
					DurationMS:         durationMS,
					Score:              45 + minFloat(25, float64(durationMS-224)/100),
				})
			}
		}
	}

	// Short activity without same-speaker words is useful for finding ASR
	// omissions. Long always-on noise is explicitly outside the 0.3-3 s gate.
	wordsBySpeaker, err := wordIntervalsBySpeakerContext(ctx, segments)
	if err != nil {
		return nil, err
	}
	for envelopeIndex, envelope := range envelopes {
		if err := checkChallengeContext(ctx, envelopeIndex); err != nil {
			return nil, err
		}
		for frame := 0; frame < len(envelope.active); {
			if err := checkChallengeContext(ctx, frame); err != nil {
				return nil, err
			}
			if !envelope.active[frame] {
				frame++
				continue
			}
			start := frame
			for frame < len(envelope.active) && envelope.active[frame] {
				if err := checkChallengeContext(ctx, frame); err != nil {
					return nil, err
				}
				frame++
			}
			startMS := int64(start * challengeFrameMS)
			endMS := int64(frame * challengeFrameMS)
			durationMS := endMS - startMS
			if durationMS < 300 || durationMS > 3000 {
				continue
			}
			hasWord, err := intervalHasWordContext(ctx, wordsBySpeaker[envelope.speakerID], startMS-200, endMS+200)
			if err != nil {
				return nil, err
			}
			if hasWord {
				continue
			}
			events.Add(challengeEvidence{
				Kind:       "untranscribed_activity",
				StartMS:    startMS,
				EndMS:      endMS,
				SpeakerID:  envelope.speakerID,
				DurationMS: durationMS,
				Score:      50 + minFloat(20, float64(durationMS)/150),
			})
		}
	}
	return events.Events(), nil
}

func isNestedShortTurn(index int, segments []Segment) bool {
	nested, _ := isNestedShortTurnContext(context.Background(), index, segments)
	return nested
}

func isNestedShortTurnContext(ctx context.Context, index int, segments []Segment) (bool, error) {
	turn := segments[index]
	before := make(map[string]bool)
	after := make(map[string]bool)
	for otherIndex, other := range segments {
		if err := checkChallengeContext(ctx, otherIndex); err != nil {
			return false, err
		}
		if otherIndex == index || other.SpeakerID == "" || other.SpeakerID == turn.SpeakerID {
			continue
		}
		if other.StartMS < turn.StartMS && other.EndMS > turn.EndMS {
			return true, nil
		}
		// Only nearby split turns can surround or overlap this short turn. This
		// avoids rescanning every word in a long meeting for each candidate.
		if other.EndMS < turn.StartMS-challengeSurroundingGapMS || other.StartMS > turn.EndMS+challengeSurroundingGapMS {
			continue
		}
		for wordIndex, word := range other.Words {
			if err := checkChallengeContext(ctx, wordIndex); err != nil {
				return false, err
			}
			if word.EndMS <= turn.StartMS && turn.StartMS-word.EndMS <= challengeSurroundingGapMS {
				before[other.SpeakerID] = true
			}
			if word.StartMS >= turn.EndMS && word.StartMS-turn.EndMS <= challengeSurroundingGapMS {
				after[other.SpeakerID] = true
			}
			if word.StartMS < turn.EndMS && word.EndMS > turn.StartMS {
				return true, nil
			}
		}
	}
	for speakerID := range before {
		if after[speakerID] {
			return true, nil
		}
	}
	return false, nil
}

func envelopeIntervalStats(envelope *speakerEnvelope, startMS, endMS int64) (float64, float64) {
	start, end := intervalFrameBounds(startMS, endMS, len(envelope.rmsDB))
	if end <= start {
		return challengeSilenceDB, 0
	}
	values := append([]float64(nil), envelope.rmsDB[start:end]...)
	sort.Float64s(values)
	activeCount := 0
	for _, on := range envelope.active[start:end] {
		if on {
			activeCount++
		}
	}
	return percentileSorted(values, 0.50), float64(activeCount) / float64(end-start)
}

func intervalFrameBounds(startMS, endMS int64, frameCount int) (int, int) {
	start := int(maxInt64(0, startMS) / challengeFrameMS)
	end := int((maxInt64(0, endMS) + challengeFrameMS - 1) / challengeFrameMS)
	if start > frameCount {
		start = frameCount
	}
	if end > frameCount {
		end = frameCount
	}
	return start, end
}

type millisecondInterval struct{ start, end int64 }

func wordIntervalsBySpeaker(segments []Segment) map[string][]millisecondInterval {
	result, _ := wordIntervalsBySpeakerContext(context.Background(), segments)
	return result
}

func wordIntervalsBySpeakerContext(ctx context.Context, segments []Segment) (map[string][]millisecondInterval, error) {
	result := make(map[string][]millisecondInterval)
	wordCount := 0
	for segmentIndex, segment := range segments {
		if err := checkChallengeContext(ctx, segmentIndex); err != nil {
			return nil, err
		}
		for _, word := range segment.Words {
			if err := checkChallengeContext(ctx, wordCount); err != nil {
				return nil, err
			}
			result[segment.SpeakerID] = append(result[segment.SpeakerID], millisecondInterval{word.StartMS, word.EndMS})
			wordCount++
		}
	}
	return result, nil
}

func intervalHasWord(words []millisecondInterval, startMS, endMS int64) bool {
	hasWord, _ := intervalHasWordContext(context.Background(), words, startMS, endMS)
	return hasWord
}

func intervalHasWordContext(ctx context.Context, words []millisecondInterval, startMS, endMS int64) (bool, error) {
	for i, word := range words {
		if err := checkChallengeContext(ctx, i); err != nil {
			return false, err
		}
		if word.start < endMS && word.end > startMS {
			return true, nil
		}
	}
	return false, nil
}

func buildChallengeCandidates(events []challengeEvidence, durationMS int64, limit int) []challengeCandidate {
	candidates, _ := buildChallengeCandidatesContext(context.Background(), events, durationMS, limit)
	return candidates
}

func buildChallengeCandidatesContext(ctx context.Context, events []challengeEvidence, durationMS int64, limit int) ([]challengeCandidate, error) {
	// Defend this stage independently of the miner: callers and future evidence
	// sources cannot accidentally reintroduce an unbounded O(E^2) merge.
	bounded := newBoundedChallengeEvents(challengeMaxEventsPerKey, challengeMaxEvents)
	for i, event := range events {
		if err := checkChallengeContext(ctx, i); err != nil {
			return nil, err
		}
		bounded.Add(event)
	}
	events = bounded.Events()
	var candidates []challengeCandidate
	for eventIndex, event := range events {
		if err := checkChallengeContext(ctx, eventIndex); err != nil {
			return nil, err
		}
		startMS, endMS := challengeWindow(event.StartMS, event.EndMS, durationMS)
		merged := false
		for i := range candidates {
			if err := checkChallengeContext(ctx, i); err != nil {
				return nil, err
			}
			unionStart := minInt64(startMS, candidates[i].StartMS)
			unionEnd := maxInt64(endMS, candidates[i].EndMS)
			if unionEnd-unionStart > challengeMaxWindowMS || overlapRatio(startMS, endMS, candidates[i].StartMS, candidates[i].EndMS) < 0.50 {
				continue
			}
			candidates[i].StartMS = unionStart
			candidates[i].EndMS = unionEnd
			candidates[i].Evidence = append(candidates[i].Evidence, event)
			candidates[i].Reasons = appendUnique(candidates[i].Reasons, event.Kind)
			candidates[i].Score = candidateScore(candidates[i].Evidence, candidates[i].Reasons)
			merged = true
			break
		}
		if !merged {
			candidates = append(candidates, challengeCandidate{
				StartMS:  startMS,
				EndMS:    endMS,
				Score:    event.Score,
				Reasons:  []string{event.Kind},
				Evidence: []challengeEvidence{event},
			})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score == candidates[j].Score {
			return candidates[i].StartMS < candidates[j].StartMS
		}
		return candidates[i].Score > candidates[j].Score
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	for i := range candidates {
		candidates[i].ID = fmt.Sprintf("challenge_%04d", i+1)
		candidates[i].Score = math.Round(candidates[i].Score*10) / 10
	}
	return candidates, nil
}

func challengeWindow(eventStartMS, eventEndMS, durationMS int64) (int64, int64) {
	startMS := maxInt64(0, eventStartMS-challengeContextMS)
	endMS := eventEndMS + challengeContextMS
	if durationMS > 0 && endMS > durationMS {
		endMS = durationMS
	}
	if endMS-startMS < challengeMinWindowMS {
		missing := challengeMinWindowMS - (endMS - startMS)
		startMS = maxInt64(0, startMS-missing/2)
		endMS += missing - missing/2
		if durationMS > 0 && endMS > durationMS {
			startMS = maxInt64(0, startMS-(endMS-durationMS))
			endMS = durationMS
		}
	}
	if endMS-startMS > challengeMaxWindowMS {
		center := eventStartMS + (eventEndMS-eventStartMS)/2
		startMS = maxInt64(0, center-challengeMaxWindowMS/2)
		endMS = startMS + challengeMaxWindowMS
		if durationMS > 0 && endMS > durationMS {
			endMS = durationMS
			startMS = maxInt64(0, endMS-challengeMaxWindowMS)
		}
	}
	return startMS, endMS
}

func overlapRatio(aStart, aEnd, bStart, bEnd int64) float64 {
	overlap := minInt64(aEnd, bEnd) - maxInt64(aStart, bStart)
	if overlap <= 0 {
		return 0
	}
	shorter := minInt64(aEnd-aStart, bEnd-bStart)
	if shorter <= 0 {
		return 0
	}
	return float64(overlap) / float64(shorter)
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func candidateScore(evidence []challengeEvidence, reasons []string) float64 {
	base := 0.0
	for _, item := range evidence {
		base = maxFloat(base, item.Score)
	}
	return base + minFloat(20, float64(len(reasons)-1)*5)
}

func roundDB(value float64) float64    { return math.Round(value*10) / 10 }
func roundRatio(value float64) float64 { return math.Round(value*1000) / 1000 }

func minFloat(values ...float64) float64 {
	result := values[0]
	for _, value := range values[1:] {
		if value < result {
			result = value
		}
	}
	return result
}

func maxFloat(values ...float64) float64 {
	result := values[0]
	for _, value := range values[1:] {
		if value > result {
			result = value
		}
	}
	return result
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
