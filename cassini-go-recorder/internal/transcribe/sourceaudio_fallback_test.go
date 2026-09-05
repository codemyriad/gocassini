package transcribe

// The manifest must not claim a per-speaker splice the transcript did not use.
//
// The splice and the transcript can come apart. When the per-participant pass
// comes back thin, ensureMergedFallback re-decodes the whole meeting from the
// mixed track under the synthetic "merged" speaker and can replace the
// transcript outright. Every word then belongs to "merged"; no word carries any
// participant's id. The splice still happened — it is in the published audio,
// and in the mix that was decoded — but provenance.sourceAudio was still handed
// to WriteManifest unconditionally, so it read as "Alice's words came from
// Alice's upload" about a transcript containing none of Alice's words.
//
// Observed on the demo: job 01M1MD49R8QVHCA0YBGH8XQ1VR spliced 52,542 ms,
// logged "per-participant transcription thin (0 words); transcribing mixed
// track as fallback...", recovered five words from the mix, and named the
// upload as the source of those five words.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheFallbackRevisesEverySpliceThatFedThePerParticipantPass(t *testing.T) {
	reports := []SourceRenderReport{
		{SpeakerID: "spk_alice", Owner: "alice", Placed: 2, SplicedMS: 52_542, MixSpliced: true,
			TranscriptSource: transcriptSourcePerParticipant},
		{SpeakerID: "spk_dave", Owner: "dave", Segments: 1, Skipped: 1,
			Rejections: []string{"segment 0 not spliced: nothing decoded"}},
	}
	streams := []AudioStream{
		{Index: 0, SpeakerID: "spk_alice", SpeakerLabel: "Alice"},
		{Index: 1, SpeakerID: "spk_dave", SpeakerLabel: "Dave"},
	}
	var log bytes.Buffer
	noteMergedFallbackTranscript(reports, streams, &log)

	if reports[0].TranscriptSource != transcriptSourceMergedMix {
		t.Errorf("alice's splice still says %q; the transcript came from the mix", reports[0].TranscriptSource)
	}
	// The audio facts are untouched: the mix that was decoded carries this
	// splice, and so does what people play back.
	if reports[0].SplicedMS != 52_542 || !reports[0].MixSpliced || reports[0].Placed != 2 {
		t.Errorf("the splice's own record was rewritten: %+v", reports[0])
	}
	// A splice that went unused was answerable for no pass to begin with.
	if reports[1].TranscriptSource != "" {
		t.Errorf("dave's refused upload picked up a transcript source: %q", reports[1].TranscriptSource)
	}

	line := log.String()
	if !strings.Contains(line, "Alice") {
		t.Errorf("the build log does not name the speaker:\n%s", line)
	}
	// The log has to say what the manifest says, in the manifest's own words,
	// or a reader diagnosing one has to translate to reach the other.
	if !strings.Contains(line, "transcript_source="+transcriptSourceMergedMix) {
		t.Errorf("the build log does not carry the manifest's value:\n%s", line)
	}
	if strings.Contains(line, "Dave") {
		t.Errorf("the build log revised a splice that was never used:\n%s", line)
	}
}

// Reverting ingestion for the whole build takes the transcript claim with it:
// the recorded tracks were transcribed, so no upload is answerable for any word.
func TestRevertingIngestionClearsTheTranscriptSource(t *testing.T) {
	reports := []SourceRenderReport{
		{SpeakerID: "spk_alice", Owner: "alice", Placed: 1, SplicedMS: 4000, MixSpliced: true,
			TranscriptSource: transcriptSourcePerParticipant},
	}
	streams := []AudioStream{{Index: 0, SpeakerID: "spk_alice", SourceAudioPath: "unused"}}
	revertSourceAudio(streams, reports, "", "the spliced mix would not encode")
	if reports[0].TranscriptSource != "" {
		t.Errorf("a reverted splice still claims %q", reports[0].TranscriptSource)
	}
	if reports[0].Placed != 0 || reports[0].SplicedMS != 0 {
		t.Errorf("a reverted splice still claims placed audio: %+v", reports[0])
	}
}

// varyingRecognizer answers the two passes differently: the per-participant
// pass runs with the VAD, the merged-mix fallback deliberately without it (see
// ensureMergedFallback). That is what lets one build exercise a thin
// participant pass and a rich mixed one with no STT model anywhere near it.
type varyingRecognizer struct{ vadWords, mixWords []Word }

func (r *varyingRecognizer) Transcribe(samples []float32, sampleRate int, useVAD bool) ([]Word, error) {
	if useVAD {
		return append([]Word(nil), r.vadWords...), nil
	}
	return append([]Word(nil), r.mixWords...), nil
}

func (r *varyingRecognizer) Close() {}

func registerVaryingBackend(t *testing.T, id string, vadWords, mixWords []Word) {
	t.Helper()
	if err := RegisterRecognizerBackend(id, func(ModelPaths, string, string, int, *DecoderConfig) (SpeechRecognizer, error) {
		return &varyingRecognizer{vadWords: vadWords, mixWords: mixWords}, nil
	}); err != nil {
		t.Fatalf("register %s: %v", id, err)
	}
	t.Cleanup(func() {
		backendMu.Lock()
		delete(backendRegistry, id)
		backendMu.Unlock()
	})
}

// buildSplicedBundle runs the real BuildMeetingArtifact over a two-participant
// recording with one upload for alice, and returns the manifest's source-audio
// provenance plus the build log.
func buildSplicedBundle(t *testing.T, backendID string, vadWords, mixWords []Word) ([]SourceRenderReport, string) {
	t.Helper()
	requireFFMediaTools(t)
	stubModelEnsurers(t)
	registerVaryingBackend(t, backendID, vadWords, mixWords)

	mkv, _ := spliceFixture(t, twoSpeakerSpecs())
	root := t.TempDir()
	segment := syntheticSegmentDelayed(10_000, 10, 1000, 0, 0)
	segment.AudioName = "segment-0.webm"
	writeCaptureAt(t, root, "room1", "alice", segment, 10, 1100)

	outDir := t.TempDir()
	var log bytes.Buffer
	if err := BuildMeetingArtifact(context.Background(), mkv, outDir, BuildConfig{
		Backend:         backendID,
		Device:          "cpu",
		CacheDir:        t.TempDir(),
		SourceAudioDir:  root,
		SourceAudioRoom: "room1",
	}, &log); err != nil {
		t.Fatalf("BuildMeetingArtifact: %v\n%s", err, log.String())
	}

	raw, err := os.ReadFile(filepath.Join(outDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest struct {
		Provenance struct {
			SourceAudio []SourceRenderReport `json:"sourceAudio"`
		} `json:"provenance"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse manifest: %v\n%s", err, raw)
	}
	if len(manifest.Provenance.SourceAudio) != 1 {
		t.Fatalf("got %d source-audio reports, want alice's one: %s", len(manifest.Provenance.SourceAudio), raw)
	}
	return manifest.Provenance.SourceAudio, log.String()
}

// The demo failure, end to end: a thin participant pass, a mixed pass that
// replaces the transcript, and a manifest that must not read as if alice's
// words came from alice's upload — because the transcript has none of alice's
// words at all.
func TestTheManifestDoesNotClaimAPerSpeakerSpliceTheTranscriptDidNotUse(t *testing.T) {
	reports, log := buildSplicedBundle(t, "fallback-wins-stub",
		fixedTimedWords(2),  // per-participant: thin, as on the demo
		fixedTimedWords(20)) // mixed track: enough to clear the margin

	if !strings.Contains(log, "per-participant transcription thin") {
		t.Fatalf("the fixture did not fire the merged fallback:\n%s", log)
	}
	report := reports[0]
	if report.SplicedMS <= 0 || !report.MixSpliced {
		t.Fatalf("the fixture did not splice anything: %+v", report)
	}
	if report.TranscriptSource != transcriptSourceMergedMix {
		t.Errorf("provenance.sourceAudio says transcript_source=%q after the merged fallback replaced the transcript; no word in it carries alice's id",
			report.TranscriptSource)
	}
	if !strings.Contains(log, "transcript_source="+transcriptSourceMergedMix) {
		t.Errorf("the build log does not say what the manifest says:\n%s", log)
	}
}

// The ordinary build, where the participant pass stands: the splice IS
// answerable for the words carrying that speaker's id, and must say so.
func TestAHealthyParticipantPassKeepsThePerSpeakerClaim(t *testing.T) {
	reports, log := buildSplicedBundle(t, "participant-pass-stub",
		fixedTimedWords(12), // per-participant: healthy, no fallback
		fixedTimedWords(20))

	if strings.Contains(log, "per-participant transcription thin") {
		t.Fatalf("the merged fallback fired on a healthy pass:\n%s", log)
	}
	if got := reports[0].TranscriptSource; got != transcriptSourcePerParticipant {
		t.Errorf("provenance.sourceAudio says transcript_source=%q on a build whose participant pass stood", got)
	}
	if strings.Contains(log, transcriptSourceMergedMix) {
		t.Errorf("the build log mentions the merged mix on a build that did not use it:\n%s", log)
	}
}

// The merged fallback appends a synthetic speaker with Index -1, standing for
// the whole mix rather than for a track. An additional model configured on the
// same build was then handed it and asked ffmpeg for "-map 0:-1", which took
// the build down with "Invalid argument" after the primary transcript had
// already been written.
func TestAnAdditionalModelSurvivesTheMergedFallback(t *testing.T) {
	requireFFMediaTools(t)
	stubModelEnsurers(t)
	registerVaryingBackend(t, "additional-after-fallback-stub", fixedTimedWords(2), fixedTimedWords(20))

	mkv, _ := spliceFixture(t, twoSpeakerSpecs())
	outDir := t.TempDir()
	var log bytes.Buffer
	if err := BuildMeetingArtifact(context.Background(), mkv, outDir, BuildConfig{
		Backend:          "additional-after-fallback-stub",
		Device:           "cpu",
		CacheDir:         t.TempDir(),
		AdditionalModels: []ModelID{"second-model"},
	}, &log); err != nil {
		t.Fatalf("BuildMeetingArtifact: %v\n%s", err, log.String())
	}
	if !strings.Contains(log.String(), "per-participant transcription thin") {
		t.Fatalf("the fixture did not fire the merged fallback:\n%s", log.String())
	}
	// And the sibling transcript is there, not merely un-crashed.
	if _, err := os.Stat(filepath.Join(outDir, "transcript-second-model.words.v1.json")); err != nil {
		t.Fatalf("the additional transcript was not written: %v", err)
	}
}

// The mix-splice off switch leaves a build where the upload fed the transcript
// and not the published audio. If the fallback then replaces the transcript,
// the render reached neither, and neither the log nor the manifest may say it
// is in the published mix.
func TestTheFallbackLogDoesNotClaimAPublishedMixThatKeptTheRecordedTrack(t *testing.T) {
	reports := []SourceRenderReport{
		{SpeakerID: "spk_alice", Owner: "alice", Placed: 1, SplicedMS: 4000,
			MixSpliced: false, MixSkipReason: "disabled by configuration (CASSINI_SOURCE_AUDIO_MIX=0)",
			TranscriptSource: transcriptSourcePerParticipant},
	}
	var log bytes.Buffer
	noteMergedFallbackTranscript(reports, []AudioStream{{SpeakerID: "spk_alice", SpeakerLabel: "Alice"}}, &log)

	line := log.String()
	if strings.Contains(line, "describes the published audio") {
		t.Errorf("the log claims the published audio carries a splice the mix refused:\n%s", line)
	}
	if !strings.Contains(line, "reached neither the published mix") {
		t.Errorf("the log does not say where the splice ended up:\n%s", line)
	}
	// The transcript question still has its own answer: the words came from the
	// mixed pass, and mix_spliced / mix_skip_reason answer the audio question.
	if reports[0].TranscriptSource != transcriptSourceMergedMix {
		t.Errorf("transcript_source = %q, want %q", reports[0].TranscriptSource, transcriptSourceMergedMix)
	}
	if reports[0].MixSpliced {
		t.Error("the mix-splice record was rewritten")
	}
}
