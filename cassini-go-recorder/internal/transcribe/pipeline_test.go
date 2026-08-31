package transcribe

// Pipeline wiring tests: the real BuildMeetingArtifact driven end to end with
// a SpeechRecognizer backend registered through the public registry, on a
// two-track fixture built from the public harness/media/parakeet-smoke.mkv.
// No STT model is downloaded — the model/VAD ensure steps are stubbed — which
// is exactly what lets the wiring itself (backend validation order, the
// attribution stage, the summary crosstalk filter, manifest provenance) be
// asserted where only ffmpeg is available.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fixedWordsRecognizer struct{ words []Word }

func (r *fixedWordsRecognizer) Transcribe(samples []float32, sampleRate int, useVAD bool) ([]Word, error) {
	return append([]Word(nil), r.words...), nil
}

func (r *fixedWordsRecognizer) Close() {}

// registerFixedBackend installs a backend that returns the given words for
// every Transcribe call, registered exactly like a real second engine.
func registerFixedBackend(t *testing.T, id string, words []Word) {
	t.Helper()
	if err := RegisterRecognizerBackend(id, func(ModelPaths, string, string, int) (SpeechRecognizer, error) {
		return &fixedWordsRecognizer{words: words}, nil
	}); err != nil {
		t.Fatalf("register %s: %v", id, err)
	}
	t.Cleanup(func() {
		backendMu.Lock()
		delete(backendRegistry, id)
		backendMu.Unlock()
	})
}

// stubModelEnsurers replaces the model/VAD download steps for the duration of
// a test: a fake backend needs no sherpa model on disk.
func stubModelEnsurers(t *testing.T) {
	t.Helper()
	prevModel, prevVAD := ensureModelFn, ensureVADFn
	ensureModelFn = func(cacheDir string, id ModelID, progress io.Writer) (ModelPaths, error) {
		return ModelPaths{ModelType: "stub", SampleRate: 16000}, nil
	}
	ensureVADFn = func(cacheDir string, progress io.Writer) (string, error) {
		return "", nil
	}
	t.Cleanup(func() { ensureModelFn, ensureVADFn = prevModel, prevVAD })
}

// fixedTimedWords returns n words on the meeting timeline, one per second,
// each 400 ms long, starting at 500 ms. Enough words that the merged-mix
// fallback never fires (its threshold is 10 words across all streams).
func fixedTimedWords(n int) []Word {
	words := make([]Word, 0, n)
	for i := 0; i < n; i++ {
		start := int64(500 + i*1000)
		words = append(words, Word{Text: fmt.Sprintf("word%02d", i), StartMS: start, EndMS: start + 400})
	}
	return words
}

// buildTwoTrackMeetingFromSmoke duplicates the public 14 s smoke recording
// into a two-participant MKV so attribution has a rival track to measure
// against. Identical audio on both tracks makes every word's gap exactly
// 0 dB: measured (so attributionGapDb is written), never flagged.
func buildTwoTrackMeetingFromSmoke(t *testing.T, dir string) string {
	t.Helper()
	src := filepath.Join("..", "..", "..", "harness", "media", "parakeet-smoke.mkv")
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("public smoke fixture missing: %v", err)
	}
	out := filepath.Join(dir, "two-track.mkv")
	if err := runMediaCommand("ffmpeg",
		"-y", "-v", "error",
		"-i", src, "-i", src,
		"-map", "0:a:0", "-map", "1:a:0",
		"-metadata:s:a:0", "title=SpeakerOne",
		"-metadata:s:a:0", "participant_id=user-one",
		"-metadata:s:a:1", "title=SpeakerTwo",
		"-metadata:s:a:1", "participant_id=user-two",
		"-c:a", "flac",
		out,
	); err != nil {
		t.Fatalf("create two-track fixture: %v", err)
	}
	return out
}

// An unknown backend must be refused before any probe, mixdown, hashing or
// model download: the misconfiguration lives in the environment, so a late
// failure would repeat all of that work on every operator retry.
func TestBuildMeetingArtifactRejectsUnknownBackendBeforeAnyWork(t *testing.T) {
	t.Setenv("CASSINI_STT_BACKEND", "no-such-engine")
	outDir := t.TempDir()
	// The MKV deliberately does not exist: if validation ran after the probe,
	// the error would be a probe failure, not the backend refusal.
	missing := filepath.Join(t.TempDir(), "missing.mkv")

	var stdout bytes.Buffer
	err := BuildMeetingArtifact(context.Background(), missing, outDir,
		BuildConfig{Device: "cpu", NumThreads: 1}, &stdout)
	if err == nil {
		t.Fatal("expected an error for an unknown backend")
	}
	if !strings.Contains(err.Error(), `unknown STT backend "no-such-engine"`) {
		t.Fatalf("expected the loud unknown-backend error before any pipeline work, got: %v", err)
	}
	if !strings.Contains(err.Error(), SherpaOnnxBackend) {
		t.Errorf("the error should name what is available, got: %v", err)
	}
	entries, readErr := os.ReadDir(outDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Errorf("no output file may exist before backend validation, found %v", entries)
	}
}

// The one test of the full wiring: attribution runs and its evidence reaches
// the written transcript, a flagged word is excluded from the summary input
// while staying in the canonical transcript (CASSINI_ATTRIBUTION_DROP unset),
// and the manifest provenance names the engine actually used.
func TestBuildMeetingArtifactPipelineWiring(t *testing.T) {
	requireFFMediaTools(t)
	stubModelEnsurers(t)
	t.Setenv("CASSINI_STT_BACKEND", "fake-fixed")
	t.Setenv("CASSINI_ATTRIBUTION_DROP", "")
	t.Setenv("CASSINI_ATTRIBUTION_DISABLED", "")
	t.Setenv("CASSINI_STT_STREAM_CONCURRENCY", "")

	words := fixedTimedWords(12)
	// The injected crosstalk word, flagged the way AnnotateAttribution flags
	// one when a meeting shows a crosstalk population.
	words[5].Text = "ghostword"
	words[5].LowConfidenceSpeaker = true
	registerFixedBackend(t, "fake-fixed", words)

	var summaryInput []Segment
	prevSummary := buildMeetingSummaryFn
	buildMeetingSummaryFn = func(_ LLMConfig, _ []AudioStream, segs []Segment) (string, error) {
		summaryInput = segs
		return "# Summary\n", nil
	}
	t.Cleanup(func() { buildMeetingSummaryFn = prevSummary })

	dir := t.TempDir()
	mkv := buildTwoTrackMeetingFromSmoke(t, dir)
	outDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := BuildConfig{
		Device:     "cpu",
		ModelID:    ModelID("stub-model"),
		CacheDir:   t.TempDir(),
		NumThreads: 1,
		SummaryLLM: LLMConfig{APIKey: "test-key", BaseURL: "https://example.test/api/v1", Model: "summary-model"},
	}
	var stdout bytes.Buffer
	if err := BuildMeetingArtifact(context.Background(), mkv, outDir, cfg, &stdout); err != nil {
		t.Fatalf("BuildMeetingArtifact: %v\noutput:\n%s", err, stdout.String())
	}

	// --- the written transcript carries the attribution evidence ---
	raw, err := os.ReadFile(filepath.Join(outDir, "transcript.words.v1.json"))
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	var tx struct {
		Segments []struct {
			Speaker string           `json:"speaker"`
			Words   []map[string]any `json:"words"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(raw, &tx); err != nil {
		t.Fatalf("parse transcript: %v", err)
	}
	var total, measured, ghostFlagged int
	for _, seg := range tx.Segments {
		for _, w := range seg.Words {
			total++
			if gap, ok := w["attributionGapDb"]; ok {
				if _, isNum := gap.(float64); !isNum {
					t.Errorf("attributionGapDb must serialise as a number, got %T", gap)
				}
				measured++
			}
			if w["text"] == "ghostword" {
				if w["lowConfidenceSpeaker"] != true {
					t.Error("the flagged word lost lowConfidenceSpeaker in the canonical transcript")
				}
				ghostFlagged++
			}
		}
	}
	if total != 24 {
		t.Fatalf("expected 24 words (12 per track), got %d", total)
	}
	if measured != total {
		t.Errorf("expected every word measured against the rival track (attributionGapDb present), got %d of %d — applyAttribution wiring broken", measured, total)
	}
	if ghostFlagged != 2 {
		t.Errorf("the canonical transcript must keep the flagged word from both tracks, got %d", ghostFlagged)
	}

	// --- the summary input excludes the flagged words, keeps the rest ---
	if summaryInput == nil {
		t.Fatal("the summary stub was never invoked")
	}
	for _, seg := range summaryInput {
		if strings.Contains(seg.Text, "ghostword") {
			t.Errorf("flagged word reached the summary input text: %q", seg.Text)
		}
		for _, w := range seg.Words {
			if w.Text == "ghostword" {
				t.Error("flagged word reached the summary input; the WithoutLowConfidenceWords filter is not wired")
			}
		}
	}
	if got := CountWords(summaryInput); got != 22 {
		t.Errorf("summary input should keep the 22 unflagged words, got %d", got)
	}
	if !strings.Contains(stdout.String(), "excluding 2 low-confidence words from the summary input") {
		t.Errorf("expected the exclusion log line, got:\n%s", stdout.String())
	}

	// --- manifest provenance names the engine actually used ---
	rawManifest, err := os.ReadFile(filepath.Join(outDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest struct {
		Provenance struct {
			SpeechToText struct {
				Backend string `json:"backend"`
				Model   string `json:"model"`
			} `json:"speechToText"`
			Attribution *AttributionProvenance `json:"attribution"`
		} `json:"provenance"`
	}
	if err := json.Unmarshal(rawManifest, &manifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if manifest.Provenance.SpeechToText.Backend != "fake-fixed" {
		t.Errorf("provenance.speechToText.backend = %q, want the engine actually used (fake-fixed)",
			manifest.Provenance.SpeechToText.Backend)
	}
	if manifest.Provenance.SpeechToText.Model != "stub-model" {
		t.Errorf("provenance.speechToText.model = %q, want stub-model", manifest.Provenance.SpeechToText.Model)
	}

	// --- the attribution stage left its record ---
	attr := manifest.Provenance.Attribution
	if attr == nil {
		t.Fatal("manifest lost provenance.attribution")
	}
	if !attr.Ran || attr.Mode != "annotate" {
		t.Errorf("attribution provenance ran=%v mode=%q, want ran=true mode=annotate", attr.Ran, attr.Mode)
	}
	if attr.WordsMeasured != 24 || attr.WordsDropped != 0 {
		t.Errorf("attribution provenance measured=%d dropped=%d, want 24 measured and 0 dropped",
			attr.WordsMeasured, attr.WordsDropped)
	}
	// The stage itself flagged nothing: identical tracks show no crosstalk
	// population (the ghost word was flagged upstream by the fake decoder).
	if attr.WordsFlagged != 0 {
		t.Errorf("attribution provenance flagged=%d, want 0", attr.WordsFlagged)
	}
	if attr.ThresholdDB != nil {
		t.Errorf("thresholdDb should be absent with no crosstalk population, got %v", *attr.ThresholdDB)
	}
}

// The envelopes depend only on the audio, so a build with an additional model
// must decode each track for attribution once, not once per transcription
// pass — and the additional transcript still carries the evidence and the
// right provenance.
func TestAttributionEnvelopesAreBuiltOncePerBuild(t *testing.T) {
	requireFFMediaTools(t)
	stubModelEnsurers(t)
	t.Setenv("CASSINI_STT_STREAM_CONCURRENCY", "")
	t.Setenv("CASSINI_ATTRIBUTION_DISABLED", "")
	t.Setenv("CASSINI_ATTRIBUTION_DROP", "")

	registerFixedBackend(t, "fake-envelope-count", fixedTimedWords(12))

	var envelopeBuilds int
	prevBuild := buildSpeakerEnvelopesFn
	buildSpeakerEnvelopesFn = func(mkvPath string, streams []AudioStream, sampleRate int, progress io.Writer) ([]*SpeakerEnvelope, error) {
		envelopeBuilds++
		return prevBuild(mkvPath, streams, sampleRate, progress)
	}
	t.Cleanup(func() { buildSpeakerEnvelopesFn = prevBuild })

	dir := t.TempDir()
	mkv := buildTwoTrackMeetingFromSmoke(t, dir)
	outDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := BuildConfig{
		Device:           "cpu",
		Backend:          "fake-envelope-count",
		ModelID:          ModelID("stub-model"),
		AdditionalModels: []ModelID{"stub-extra-model"},
		CacheDir:         t.TempDir(),
		NumThreads:       1,
	}
	var stdout bytes.Buffer
	if err := BuildMeetingArtifact(context.Background(), mkv, outDir, cfg, &stdout); err != nil {
		t.Fatalf("BuildMeetingArtifact: %v\noutput:\n%s", err, stdout.String())
	}

	if envelopeBuilds != 1 {
		t.Errorf("envelopes were built %d times for one build; the primary and additional pass must share one decode", envelopeBuilds)
	}

	extraRaw, err := os.ReadFile(filepath.Join(outDir, "transcript-stub-extra-model.words.v1.json"))
	if err != nil {
		t.Fatalf("additional transcript missing: %v", err)
	}
	if !strings.Contains(string(extraRaw), `"attributionGapDb"`) {
		t.Error("the additional transcript lost the attribution evidence; the cached envelopes were not applied to it")
	}

	rawManifest, err := os.ReadFile(filepath.Join(outDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest struct {
		Files struct {
			Transcripts []struct {
				ID         string `json:"id"`
				Provenance *struct {
					Backend string `json:"backend"`
				} `json:"provenance"`
			} `json:"transcripts"`
		} `json:"files"`
	}
	if err := json.Unmarshal(rawManifest, &manifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if len(manifest.Files.Transcripts) != 2 {
		t.Fatalf("expected primary + additional transcript entries, got %d", len(manifest.Files.Transcripts))
	}
	for _, tr := range manifest.Files.Transcripts {
		if tr.Provenance == nil || tr.Provenance.Backend != "fake-envelope-count" {
			t.Errorf("transcript %q provenance backend = %#v, want fake-envelope-count", tr.ID, tr.Provenance)
		}
	}
}

// With a single participant there is no rival track: the documented behaviour
// is a pure pass-through — no decode, no measurement, no flags.
func TestApplyAttributionSingleTrackIsAPassthrough(t *testing.T) {
	streams := []AudioStream{{Index: 0, SpeakerID: "spk_solo", SpeakerLabel: "Solo"}}
	segments := []Segment{{SpeakerID: "spk_solo", StartMS: 0, EndMS: 800, Text: "hello there",
		Words: []Word{{Text: "hello", StartMS: 0, EndMS: 400}, {Text: "there", StartMS: 410, EndMS: 800}}}}

	var out bytes.Buffer
	report := &AttributionProvenance{Mode: "annotate"}
	// A path that cannot be decoded: if attribution tried anyway, its decode
	// warning would appear and this test would fail.
	got := applyAttributionReported(filepath.Join(t.TempDir(), "does-not-exist.mkv"), streams, segments, 16000, BuildConfig{}, nil, &out, report)
	if report.Ran || report.Reason == "" {
		t.Errorf("a single-track skip must be recorded in the provenance report, got ran=%v reason=%q",
			report.Ran, report.Reason)
	}
	if strings.Contains(out.String(), "warn:") || strings.Contains(out.String(), "measuring cross-track") {
		t.Errorf("single-track attribution attempted to measure: %q", out.String())
	}
	for _, seg := range got {
		for _, w := range seg.Words {
			if w.HasAttributionGap || w.LowConfidenceSpeaker {
				t.Errorf("single-track word %q must carry no attribution evidence: %+v", w.Text, w)
			}
		}
	}
}

// After the merged fallback replaced the transcript, no segment belongs to a
// participant track and nothing can be measured: attribution must skip
// without decoding a single track.
func TestApplyAttributionSkipsDecodingWhenOnlyTheMergedSpeakerRemains(t *testing.T) {
	streams := []AudioStream{
		{Index: 0, SpeakerID: "spk_one", SpeakerLabel: "One"},
		{Index: 1, SpeakerID: "spk_two", SpeakerLabel: "Two"},
		{Index: -1, SpeakerID: "merged", SpeakerLabel: "Everyone"},
	}
	segments := []Segment{{SpeakerID: "merged", StartMS: 0, EndMS: 800, Text: "hello there",
		Words: []Word{{Text: "hello", StartMS: 0, EndMS: 400}, {Text: "there", StartMS: 410, EndMS: 800}}}}

	var out bytes.Buffer
	report := &AttributionProvenance{Mode: "annotate"}
	// A path that cannot be decoded: if attribution tried to build envelopes
	// anyway, it would emit its decode warning and this test would fail.
	got := applyAttributionReported(filepath.Join(t.TempDir(), "does-not-exist.mkv"), streams, segments, 16000, BuildConfig{}, nil, &out, report)

	if report.Ran || !strings.Contains(report.Reason, "participant track") {
		t.Errorf("the merged-fallback skip must be recorded in the provenance report, got ran=%v reason=%q",
			report.Ran, report.Reason)
	}
	if !strings.Contains(out.String(), "attribution skipped: no transcript words belong to a participant track") {
		t.Errorf("expected the one-line skip log, got %q", out.String())
	}
	if strings.Contains(out.String(), "warn:") || strings.Contains(out.String(), "measuring cross-track") {
		t.Errorf("attribution attempted to decode the tracks: %q", out.String())
	}
	if len(got) != 1 || got[0].Text != "hello there" || len(got[0].Words) != 2 {
		t.Errorf("segments must pass through unchanged, got %#v", got)
	}
}

// A build with attribution disabled must say so in the manifest: a disabled
// stage and one that ran and measured nothing must be distinguishable, or
// re-running with different env yields different transcripts with identical
// provenance.
func TestBuildMeetingArtifactRecordsSkippedAttributionProvenance(t *testing.T) {
	requireFFMediaTools(t)
	stubModelEnsurers(t)
	t.Setenv("CASSINI_STT_STREAM_CONCURRENCY", "")
	registerFixedBackend(t, "fake-skip-attr", fixedTimedWords(12))

	dir := t.TempDir()
	mkv := buildTwoTrackMeetingFromSmoke(t, dir)
	outDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := BuildConfig{
		Device:          "cpu",
		Backend:         "fake-skip-attr",
		ModelID:         ModelID("stub-model"),
		CacheDir:        t.TempDir(),
		NumThreads:      1,
		SkipAttribution: true,
	}
	var stdout bytes.Buffer
	if err := BuildMeetingArtifact(context.Background(), mkv, outDir, cfg, &stdout); err != nil {
		t.Fatalf("BuildMeetingArtifact: %v\noutput:\n%s", err, stdout.String())
	}

	raw, err := os.ReadFile(filepath.Join(outDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest struct {
		Provenance struct {
			Attribution *AttributionProvenance `json:"attribution"`
		} `json:"provenance"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	attr := manifest.Provenance.Attribution
	if attr == nil {
		t.Fatal("a skipped attribution stage left no provenance trace")
	}
	if attr.Ran {
		t.Error("ran must be false for a disabled stage")
	}
	if attr.Mode != "disabled" {
		t.Errorf("mode = %q, want disabled", attr.Mode)
	}
	if attr.Reason == "" {
		t.Error("a skipped stage must record why it did not run")
	}
	if attr.WordsMeasured != 0 || attr.WordsFlagged != 0 || attr.WordsDropped != 0 {
		t.Errorf("a stage that never ran must count nothing, got measured=%d flagged=%d dropped=%d",
			attr.WordsMeasured, attr.WordsFlagged, attr.WordsDropped)
	}
}

// boundedWordsRecognizer is a fake decoder that DOES declare the
// AudioBoundedWordEnds guarantee. It measures nothing — it is the declaration
// itself that is under test, since the declaration is the only thing the
// pipeline can see.
type boundedWordsRecognizer struct{ fixedWordsRecognizer }

func (r *boundedWordsRecognizer) WordEndsAreBoundedByAudio() bool { return true }

// registerBoundedBackend installs a backend whose recognizer declares the
// audio-bounded word-end guarantee, registered exactly like a real engine.
func registerBoundedBackend(t *testing.T, id string, words []Word) {
	t.Helper()
	if err := RegisterRecognizerBackend(id, func(ModelPaths, string, string, int) (SpeechRecognizer, error) {
		return &boundedWordsRecognizer{fixedWordsRecognizer{words: words}}, nil
	}); err != nil {
		t.Fatalf("register %s: %v", id, err)
	}
	t.Cleanup(func() {
		backendMu.Lock()
		delete(backendRegistry, id)
		backendMu.Unlock()
	})
}

// readManifestWordTimings returns the manifest's provenance.wordTimings as raw
// JSON — key level, so an absent object and a false field stay distinguishable
// — together with the word count, so a test can prove the build actually
// produced words to make a claim about.
func readManifestWordTimings(t *testing.T, outDir string) (map[string]any, int, string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(outDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest struct {
		WordCount  int `json:"wordCount"`
		Provenance struct {
			WordTimings map[string]any `json:"wordTimings"`
		} `json:"provenance"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	return manifest.Provenance.WordTimings, manifest.WordCount, string(raw)
}

// buildWithBackendForWordTimings runs the real BuildMeetingArtifact over the
// two-track fixture with the named backend and returns the output directory.
func buildWithBackendForWordTimings(t *testing.T, backendID string) string {
	t.Helper()
	requireFFMediaTools(t)
	stubModelEnsurers(t)
	t.Setenv("CASSINI_STT_BACKEND", backendID)
	t.Setenv("CASSINI_ATTRIBUTION_DISABLED", "")
	t.Setenv("CASSINI_ATTRIBUTION_DROP", "")
	t.Setenv("CASSINI_STT_STREAM_CONCURRENCY", "")

	dir := t.TempDir()
	mkv := buildTwoTrackMeetingFromSmoke(t, dir)
	outDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := BuildConfig{Device: "cpu", ModelID: ModelID("stub-model"), CacheDir: t.TempDir(), NumThreads: 1}
	var stdout bytes.Buffer
	if err := BuildMeetingArtifact(context.Background(), mkv, outDir, cfg, &stdout); err != nil {
		t.Fatalf("BuildMeetingArtifact: %v\noutput:\n%s", err, stdout.String())
	}
	return outDir
}

// The guarantee must be earned, not inherited. This registers a second engine
// through the public registry — the whole integration surface a Voxtral or
// custom backend uses — returning perfectly well-formed timed words that never
// went near the energy gate, and drives it through the real
// BuildMeetingArtifact. The published manifest must carry no wordTimings key
// at all, so the viewer keeps running its legacy repair over ends this backend
// never measured. Before the capability existed, the producer wrote
// endsBoundedByAudio:true here regardless of who decoded.
func TestBuildMeetingArtifactMakesNoWordTimingClaimForABackendThatDoesNotDeclareOne(t *testing.T) {
	registerFixedBackend(t, "fake-unmeasured-ends", fixedTimedWords(12))
	outDir := buildWithBackendForWordTimings(t, "fake-unmeasured-ends")

	timings, wordCount, raw := readManifestWordTimings(t, outDir)
	if wordCount == 0 {
		t.Fatalf("the build produced no words, so this test asserts nothing:\n%s", raw)
	}
	if timings != nil {
		t.Errorf("a backend that declares nothing got provenance.wordTimings = %v", timings)
	}
	// Absence of the whole object is the signal, not endsBoundedByAudio:false.
	if strings.Contains(raw, "wordTimings") {
		t.Errorf("the manifest mentions wordTimings for an undeclared backend:\n%s", raw)
	}
}

// The other half: a backend that DOES declare the guarantee gets it published.
// Without this, deleting the claim entirely would pass the test above, and the
// viewer would clip correctly measured word ends on every real build.
func TestBuildMeetingArtifactPublishesTheWordTimingClaimADeclaringBackendMakes(t *testing.T) {
	registerBoundedBackend(t, "fake-measured-ends", fixedTimedWords(12))
	outDir := buildWithBackendForWordTimings(t, "fake-measured-ends")

	timings, wordCount, raw := readManifestWordTimings(t, outDir)
	if wordCount == 0 {
		t.Fatalf("the build produced no words, so this test asserts nothing:\n%s", raw)
	}
	if timings == nil {
		t.Fatalf("a declaring backend got no provenance.wordTimings:\n%s", raw)
	}
	if timings["endsBoundedByAudio"] != true {
		t.Errorf("endsBoundedByAudio = %v, want true", timings["endsBoundedByAudio"])
	}
}
