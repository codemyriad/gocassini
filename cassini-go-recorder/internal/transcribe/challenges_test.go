package transcribe

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMineChallengeEventsFindsKnownLeakageShape(t *testing.T) {
	const frames = 400
	chima := testEnvelope("spk_chima", frames, -70)
	ivan := testEnvelope("spk_ivan", frames, -70)

	// Chima owns the actual speech around 3.2-3.76s. Ivan's isolated track
	// contains only roughly -61 dBFS bleed there, but later contains normal
	// direct speech so its adaptive reference is not trained on the bleed.
	setFrameRange(chima.rmsDB, 80, 200, -24)
	setFrameRange(ivan.rmsDB, 100, 118, -61)
	setFrameRange(ivan.rmsDB, 250, 300, -18)
	calibrateEnvelope(chima)
	calibrateEnvelope(ivan)

	segments := []Segment{
		{
			SpeakerID: "spk_chima", StartMS: 0, EndMS: 8000,
			Words: []Word{
				{Text: "before", StartMS: 3000, EndMS: 3100},
				{Text: "after", StartMS: 3800, EndMS: 3900},
			},
		},
		{
			SpeakerID: "spk_ivan", StartMS: 3200, EndMS: 3760,
			Words: []Word{
				{Text: "I", StartMS: 3200, EndMS: 3280},
				{Text: "don't", StartMS: 3360, EndMS: 3520},
				{Text: "remember", StartMS: 3600, EndMS: 3760},
			},
		},
	}

	events := mineChallengeEvents([]*speakerEnvelope{chima, ivan}, segments)
	nested := findChallengeEvent(events, "short_nested_turn")
	if nested == nil || nested.SpeakerID != "spk_ivan" {
		t.Fatalf("missing nested Ivan turn; events=%#v", events)
	}
	mismatch := findChallengeEvent(events, "attribution_mismatch")
	if mismatch == nil {
		t.Fatalf("missing attribution mismatch; events=%#v", events)
	}
	if mismatch.CompetingSpeakerID != "spk_chima" {
		t.Fatalf("competing speaker = %q, want spk_chima", mismatch.CompetingSpeakerID)
	}
	if mismatch.LevelDeltaDB < 30 || mismatch.OwnerActiveRatio >= 0.25 || mismatch.OtherActiveRatio < 0.60 {
		t.Fatalf("mismatch evidence not conservative enough: %#v", mismatch)
	}
	if got := mismatch.LevelDeltaDB; math.Abs(got-37) > 1 {
		t.Fatalf("level delta = %.1f dB, want about 37 dB", got)
	}
}

func TestMineChallengeEventsFindsAcousticOverlapAndUntranscribedIsland(t *testing.T) {
	left := testEnvelope("spk_left", 100, -70)
	right := testEnvelope("spk_right", 100, -70)
	setFrameRange(left.rmsDB, 10, 30, -20)
	setFrameRange(right.rmsDB, 15, 28, -22)
	calibrateEnvelope(left)
	calibrateEnvelope(right)

	events := mineChallengeEvents([]*speakerEnvelope{left, right}, nil)
	overlap := findChallengeEvent(events, "acoustic_overlap")
	if overlap == nil {
		t.Fatalf("missing acoustic overlap; events=%#v", events)
	}
	if overlap.DurationMS < challengeOverlapFrames*challengeFrameMS {
		t.Fatalf("overlap duration = %dms, below %dms gate", overlap.DurationMS, challengeOverlapFrames*challengeFrameMS)
	}
	if missing := findChallengeEvent(events, "untranscribed_activity"); missing == nil {
		t.Fatalf("missing untranscribed activity island; events=%#v", events)
	}
}

func TestMergeEnvelopeFrameCollapsesRejoinStreams(t *testing.T) {
	envelope := testEnvelope("spk_alice", 0, challengeSilenceDB)
	for i := 0; i < 20; i++ {
		mergeEnvelopeFrame(envelope, i, -20)
	}
	// A second physical stream for Alice overlaps the first. Framewise max
	// preserves her energy, but there is still only one logical envelope and
	// therefore no possible self-overlap event.
	for i := 10; i < 30; i++ {
		mergeEnvelopeFrame(envelope, i, -25)
	}
	envelope.physicalStreams = 2
	calibrateEnvelope(envelope)
	for _, event := range mineChallengeEvents([]*speakerEnvelope{envelope}, nil) {
		if event.Kind == "acoustic_overlap" {
			t.Fatalf("rejoin streams produced self-overlap: %#v", event)
		}
	}
	if envelope.rmsDB[15] != -20 {
		t.Fatalf("merged frame = %.1f dB, want louder stream's -20 dB", envelope.rmsDB[15])
	}
}

func TestCalibrateEnvelopeRejectsFlatLowLevelNoise(t *testing.T) {
	noise := testEnvelope("spk_noise", 1000, -68)
	calibrateEnvelope(noise)
	for frame, active := range noise.active {
		if active {
			t.Fatalf("flat noise frame %d classified active", frame)
		}
	}
	if events := mineChallengeEvents([]*speakerEnvelope{noise}, nil); len(events) != 0 {
		t.Fatalf("flat noise produced challenges: %#v", events)
	}
}

func TestStreamSpeakerRMSPreservesDelayedTimeline(t *testing.T) {
	requireFFMediaTools(t)
	mkvPath := buildOffsetMeeting(t, t.TempDir(), 10.0)
	streams, _, err := ProbeMKV(mkvPath)
	if err != nil {
		t.Fatal(err)
	}
	var frames []float64
	if err := streamSpeakerRMS(context.Background(), mkvPath, streams[1], func(rmsDB float64) {
		frames = append(frames, rmsDB)
	}); err != nil {
		t.Fatalf("stream delayed track: %v", err)
	}
	firstSignal := -1
	for i, rmsDB := range frames {
		if rmsDB > -50 {
			firstSignal = i
			break
		}
	}
	if firstSignal < 0 {
		t.Fatal("no signal frame found")
	}
	firstSignalMS := firstSignal * challengeFrameMS
	if firstSignalMS < 9700 || firstSignalMS > 10300 {
		t.Fatalf("first signal = %dms, want about 10000ms", firstSignalMS)
	}
}

func TestWriteWaveformChallengesWritesReviewOnlySidecar(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	mkvPath := buildOffsetMeeting(t, tmp, 2.0)
	streams, durationMS, err := ProbeMKV(mkvPath)
	if err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(tmp, "challenges.v1.json")
	segments := []Segment{
		{SpeakerID: streams[0].SpeakerID, StartMS: 0, EndMS: 100, Words: []Word{{Text: "one", StartMS: 0, EndMS: 100}}},
		{SpeakerID: streams[1].SpeakerID, StartMS: 2000, EndMS: 2100, Words: []Word{{Text: "two", StartMS: 2000, EndMS: 2100}}},
	}
	provenance := WaveformChallengeProvenance{
		AudioSHA256:      strings.Repeat("a", 64),
		TranscriptPath:   "/audit/private/transcript.words.v1.json",
		TranscriptSHA256: strings.Repeat("b", 64),
	}
	if err := WriteWaveformChallengesWithProvenance(context.Background(), outPath, mkvPath, streams, segments, durationMS, provenance); err != nil {
		t.Fatalf("WriteWaveformChallenges: %v", err)
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	var got waveformChallengesFile
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode sidecar: %v", err)
	}
	if got.Version != "cassini.waveform-challenges.v1" || !got.ReviewOnly {
		t.Fatalf("sidecar contract = version %q reviewOnly=%v", got.Version, got.ReviewOnly)
	}
	if got.FrameMS != challengeFrameMS || len(got.Tracks) != 2 {
		t.Fatalf("sidecar frame/tracks = %d/%d, want %d/2", got.FrameMS, len(got.Tracks), challengeFrameMS)
	}
	if got.SourceAudioSHA256 != provenance.AudioSHA256 || got.SourceTranscriptSHA256 != provenance.TranscriptSHA256 {
		t.Fatalf("sidecar source hashes = %q/%q, want exact input hashes", got.SourceAudioSHA256, got.SourceTranscriptSHA256)
	}
	if got.SourceTranscript != "transcript.words.v1.json" {
		t.Fatalf("sidecar source transcript = %q, want basename only", got.SourceTranscript)
	}
}

func TestSummarizePCM16UsesFixedReadBuffer(t *testing.T) {
	const frameCount = 5000
	reader := &generatedPCMReader{remaining: int64(frameCount * challengeFrameSamples * 2)}
	gotFrames := 0
	if err := summarizePCM16(reader, challengeFrameSamples, func(float64) { gotFrames++ }); err != nil {
		t.Fatal(err)
	}
	if gotFrames != frameCount {
		t.Fatalf("frames = %d, want %d", gotFrames, frameCount)
	}
	if reader.maxRequest != challengeFrameSamples*2 {
		t.Fatalf("largest read = %d bytes, want one %d-byte frame", reader.maxRequest, challengeFrameSamples*2)
	}

	allocs := testing.AllocsPerRun(5, func() {
		r := &generatedPCMReader{remaining: int64(100 * challengeFrameSamples * 2)}
		if err := summarizePCM16(r, challengeFrameSamples, func(float64) {}); err != nil {
			panic(err)
		}
	})
	if allocs > 6 {
		t.Fatalf("summarizing allocations = %.1f, want <=6 independent of frame count", allocs)
	}
}

func TestBuildChallengeCandidatesCapsAndMergesEvidence(t *testing.T) {
	var events []challengeEvidence
	for i := 0; i < 30; i++ {
		start := int64(i * 30000)
		events = append(events, challengeEvidence{Kind: "acoustic_overlap", StartMS: start, EndMS: start + 300, DurationMS: 300, Score: float64(100 - i)})
	}
	// A distinct reason at the first event must merge into that candidate.
	events = append(events, challengeEvidence{Kind: "attribution_mismatch", StartMS: 100, EndMS: 500, DurationMS: 400, Score: 95})
	candidates := buildChallengeCandidates(events, 1_000_000, challengeMaxCandidates)
	if len(candidates) != challengeMaxCandidates {
		t.Fatalf("candidates = %d, want cap %d", len(candidates), challengeMaxCandidates)
	}
	if len(candidates[0].Reasons) != 2 {
		t.Fatalf("first reasons = %v, want merged overlap+mismatch", candidates[0].Reasons)
	}
	if candidates[0].EndMS-candidates[0].StartMS > challengeMaxWindowMS {
		t.Fatalf("merged candidate exceeds maximum window: %#v", candidates[0])
	}
}

func TestChallengeEvidencePreservesFirstSegmentIndex(t *testing.T) {
	raw, err := json.Marshal(challengeEvidence{
		Kind:         "short_nested_turn",
		SegmentIndex: challengeSegmentIndex(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"segmentIndex":0`)) {
		t.Fatalf("first segment index was omitted from review evidence: %s", raw)
	}
	withoutIndex, err := json.Marshal(challengeEvidence{Kind: "acoustic_overlap"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(withoutIndex, []byte(`"segmentIndex"`)) {
		t.Fatalf("non-segment evidence advertised a segment index: %s", withoutIndex)
	}
}

func TestBoundedChallengeEventsCapsPerKeyAndGlobalDeterministically(t *testing.T) {
	var input []challengeEvidence
	for pair := 0; pair < 40; pair++ {
		left := fmt.Sprintf("spk_%03d", pair)
		right := fmt.Sprintf("spk_%03d", pair+100)
		for eventIndex := 0; eventIndex < 80; eventIndex++ {
			kind := "acoustic_overlap"
			if (eventIndex/2)%2 == 1 {
				kind = "attribution_mismatch"
			}
			// Reverse some speaker directions. The acoustic-overlap key is an
			// unordered pair, so both directions must share one per-key budget;
			// the alternating kind proves each evidence class has its own budget.
			speakerID, competingID := left, right
			if eventIndex%2 == 1 {
				speakerID, competingID = competingID, speakerID
			}
			input = append(input, challengeEvidence{
				Kind:               kind,
				StartMS:            int64(pair*1_000_000 + eventIndex*10_000),
				EndMS:              int64(pair*1_000_000 + eventIndex*10_000 + 300 + eventIndex),
				SpeakerID:          speakerID,
				CompetingSpeakerID: competingID,
				DurationMS:         int64(300 + eventIndex),
				Score:              float64((eventIndex*37+pair)%101) + float64(eventIndex)/1000,
			})
		}
	}

	collect := func(reverse bool) (*boundedChallengeEvents, []challengeEvidence) {
		collector := newBoundedChallengeEvents(challengeMaxEventsPerKey, challengeMaxEvents)
		if reverse {
			for i := len(input) - 1; i >= 0; i-- {
				collector.Add(input[i])
			}
		} else {
			for _, event := range input {
				collector.Add(event)
			}
		}
		return collector, collector.Events()
	}

	forwardCollector, forward := collect(false)
	_, reverse := collect(true)
	if len(forward) != challengeMaxEvents {
		t.Fatalf("retained events = %d, want hard global cap %d", len(forward), challengeMaxEvents)
	}
	for key, bucket := range forwardCollector.byKey {
		if len(bucket) > challengeMaxEventsPerKey {
			t.Fatalf("bucket %q retained %d events, cap %d", key, len(bucket), challengeMaxEventsPerKey)
		}
	}
	classCollector := newBoundedChallengeEvents(challengeMaxEventsPerKey, challengeMaxEvents)
	for i := 0; i < 100; i++ {
		for _, kind := range []string{"acoustic_overlap", "attribution_mismatch"} {
			classCollector.Add(challengeEvidence{
				Kind: kind, SpeakerID: "spk_a", CompetingSpeakerID: "spk_b",
				StartMS: int64(i * 1000), EndMS: int64(i*1000 + 300), Score: float64(i),
			})
		}
	}
	if len(classCollector.byKey) != 2 {
		t.Fatalf("same pair across two classes produced %d buckets, want 2", len(classCollector.byKey))
	}
	for key, bucket := range classCollector.byKey {
		if len(bucket) != challengeMaxEventsPerKey {
			t.Fatalf("class bucket %q retained %d events, want cap %d", key, len(bucket), challengeMaxEventsPerKey)
		}
		for _, item := range bucket {
			if item.event.Score < 68 {
				t.Fatalf("class bucket %q retained score %.1f below top-%d cutoff", key, item.event.Score, challengeMaxEventsPerKey)
			}
		}
	}
	forwardJSON, err := json.Marshal(forward)
	if err != nil {
		t.Fatal(err)
	}
	reverseJSON, err := json.Marshal(reverse)
	if err != nil {
		t.Fatal(err)
	}
	if string(forwardJSON) != string(reverseJSON) {
		t.Fatal("bounded event selection changed when adversarial input order was reversed")
	}
}

func TestMineChallengeEventsBoundsAdversarialOverlapRuns(t *testing.T) {
	const (
		speakerCount = 8
		frameCount   = 40_000
	)
	envelopes := make([]*speakerEnvelope, 0, speakerCount)
	for speaker := 0; speaker < speakerCount; speaker++ {
		active := make([]bool, frameCount)
		for frame := range active {
			// Eight active frames followed by one inactive frame produces
			// thousands of independently qualifying 256 ms overlap runs.
			active[frame] = frame%9 < 8
		}
		envelopes = append(envelopes, &speakerEnvelope{
			speakerID: fmt.Sprintf("spk_%02d", speaker),
			active:    active,
		})
	}

	events, err := mineChallengeEventsContext(context.Background(), envelopes, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != challengeMaxEvents {
		t.Fatalf("adversarial miner retained %d events, want global cap %d", len(events), challengeMaxEvents)
	}
	perKey := make(map[string]int)
	for _, event := range events {
		key := challengeEventKey(event)
		perKey[key]++
		if perKey[key] > challengeMaxEventsPerKey {
			t.Fatalf("miner retained more than %d events for %q", challengeMaxEventsPerKey, key)
		}
	}
}

func TestBuildChallengeCandidatesBoundsAdversarialInput(t *testing.T) {
	events := make([]challengeEvidence, 25_000)
	for i := range events {
		events[i] = challengeEvidence{
			Kind:               "acoustic_overlap",
			StartMS:            int64(i * 30_000),
			EndMS:              int64(i*30_000 + 300),
			SpeakerID:          fmt.Sprintf("spk_%05d", i),
			CompetingSpeakerID: fmt.Sprintf("spk_%05d", i+30_000),
			DurationMS:         300,
			Score:              float64(i % 100),
		}
	}

	candidates, err := buildChallengeCandidatesContext(context.Background(), events, 1_000_000_000, challengeMaxCandidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != challengeMaxCandidates {
		t.Fatalf("candidate count = %d, want output cap %d", len(candidates), challengeMaxCandidates)
	}
	totalEvidence := 0
	for _, candidate := range candidates {
		totalEvidence += len(candidate.Evidence)
	}
	if totalEvidence > challengeMaxEvents {
		t.Fatalf("candidate evidence = %d, exceeded bounded input %d", totalEvidence, challengeMaxEvents)
	}
}

func TestMineChallengeEventsHonorsCancellationInsideLongRun(t *testing.T) {
	ctx := &cancelAfterChecksContext{Context: context.Background(), cancelAfter: 6}
	envelopes := []*speakerEnvelope{
		{speakerID: "spk_left", active: make([]bool, 1_000_000)},
		{speakerID: "spk_right", active: make([]bool, 1_000_000)},
	}
	for _, envelope := range envelopes {
		for i := range envelope.active {
			envelope.active[i] = true
		}
	}

	_, err := mineChallengeEventsContext(ctx, envelopes, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("mine error = %v, want context.Canceled", err)
	}
	if ctx.checks != ctx.cancelAfter {
		t.Fatalf("context checks = %d, want cancellation at check %d", ctx.checks, ctx.cancelAfter)
	}
}

func testEnvelope(speakerID string, frames int, baseline float64) *speakerEnvelope {
	values := make([]float64, frames)
	for i := range values {
		values[i] = baseline
	}
	return &speakerEnvelope{speakerID: speakerID, speakerLabel: speakerID, physicalStreams: 1, rmsDB: values}
}

func setFrameRange(values []float64, start, end int, value float64) {
	for i := start; i < end; i++ {
		values[i] = value
	}
}

func findChallengeEvent(events []challengeEvidence, kind string) *challengeEvidence {
	for i := range events {
		if events[i].Kind == kind {
			return &events[i]
		}
	}
	return nil
}

type generatedPCMReader struct {
	remaining  int64
	maxRequest int
	phase      int16
}

type cancelAfterChecksContext struct {
	context.Context
	checks      int
	cancelAfter int
}

func (c *cancelAfterChecksContext) Err() error {
	c.checks++
	if c.checks >= c.cancelAfter {
		return context.Canceled
	}
	return nil
}

func (r *generatedPCMReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	if len(p) > r.maxRequest {
		r.maxRequest = len(p)
	}
	n := len(p)
	if int64(n) > r.remaining {
		n = int(r.remaining)
	}
	n -= n % 2
	for off := 0; off < n; off += 2 {
		r.phase += 97
		binary.LittleEndian.PutUint16(p[off:off+2], uint16(r.phase))
	}
	r.remaining -= int64(n)
	return n, nil
}
