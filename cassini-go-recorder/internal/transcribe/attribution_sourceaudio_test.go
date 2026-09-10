package transcribe

// Attribution has to judge the audio the words were decoded from.
//
// The transcription pass honours stream.SourceAudioPath and
// stream.SuppressTranscription (audio.go, transcribe.go); the attribution stage
// used to honour neither, so it measured the recorded track for a speaker whose
// words came from their upload. Where the SFU delivered nothing, that scores a
// word against silence: a quiet owner under somebody else's speech, which is
// the crosstalk signature. The word is flagged, kept out of the summary, and
// under CASSINI_ATTRIBUTION_DROP deleted — while being plainly audible in the
// published mix, which carries the same splice.

import (
	"bytes"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

// spanAudio is one stretch of a synthetic track: seconds of sign noise at a
// fixed amplitude, which gives an exact RMS and so an exact dB level.
type spanAudio struct {
	seconds float64
	amp     float32
}

func spansToSamples(spans []spanAudio, sampleRate int, seed int64) []float32 {
	var out []float32
	for i, span := range spans {
		out = append(out, signNoise(int(span.seconds*float64(sampleRate)), span.amp, seed+int64(i))...)
	}
	return out
}

// splitTrackFixture builds the case in the doc comment above: alice speaks from
// 2 s to 4 s but her recorded track carries nothing there, and bob is speaking
// over the top of her. Her spliced render has the words. Returns the MKV, her
// render, and the two streams.
func splitTrackFixture(t *testing.T) (string, string, []AudioStream) {
	t.Helper()
	const sampleRate = 16000
	const quiet = 0.004 // ~-48 dBFS: a microphone's own floor
	const loud = 0.3    // ~-10 dBFS: somebody speaking
	dir := t.TempDir()

	// What the SFU delivered for alice: her floor over the stretch she was
	// actually talking, then two seconds where it did carry her voice — which
	// is what gives her a speech reference at all.
	alice := filepath.Join(dir, "alice.wav")
	writeFloorWAV(t, alice, spansToSamples([]spanAudio{
		{2, quiet}, {2, quiet}, {2, loud},
	}, sampleRate, 1), sampleRate)

	// Bob is speaking over exactly the stretch alice's track lost.
	bob := filepath.Join(dir, "bob.wav")
	writeFloorWAV(t, bob, spansToSamples([]spanAudio{
		{2, quiet}, {2, loud}, {2, quiet},
	}, sampleRate, 100), sampleRate)

	// Alice's render: her own capture, so her voice is there from 2 s.
	render := filepath.Join(dir, "source-alice.wav")
	writeFloorWAV(t, render, spansToSamples([]spanAudio{
		{2, quiet}, {2, loud}, {2, loud},
	}, sampleRate, 200), sampleRate)

	mkv := filepath.Join(dir, "recording.mkv")
	if err := runMediaCommand("ffmpeg", "-y", "-v", "error",
		"-i", alice, "-i", bob,
		"-map", "0:a", "-map", "1:a",
		"-c:a", "flac", mkv); err != nil {
		t.Fatalf("mux fixture: %v", err)
	}

	return mkv, render, []AudioStream{
		{Index: 0, SpeakerID: "alice", SpeakerLabel: "Alice", Channels: 1},
		{Index: 1, SpeakerID: "bob", SpeakerLabel: "Bob", Channels: 1},
	}
}

// The envelope must come from the file the recogniser was given, frame for
// frame. Reverting the SourceAudioPath branch in buildSpeakerEnvelopeStreaming
// makes this read the recorded track instead, which has no speech at 2-4 s.
func TestTheEnvelopeMeasuresTheAudioTheWordsCameFrom(t *testing.T) {
	requireFFMediaTools(t)
	const sampleRate = 16000
	mkv, render, streams := splitTrackFixture(t)
	streams[0].SourceAudioPath = render

	envelopes, err := BuildSpeakerEnvelopes(mkv, streams, sampleRate, nil)
	if err != nil {
		t.Fatalf("BuildSpeakerEnvelopes: %v", err)
	}
	if len(envelopes) != 2 {
		t.Fatalf("got %d envelopes, want one per speaker", len(envelopes))
	}
	if !envelopes[0].FromSourceAudio {
		t.Fatal("alice's envelope is not marked as measured on her render")
	}
	if envelopes[1].FromSourceAudio {
		t.Fatal("bob has no upload; his envelope must not be marked as one")
	}
	// The frame-for-frame claim below is about the DECODE, so the fixture is
	// built to give both tracks the same usable range and leave
	// capIngestedDynamicRange nothing to do. Assert that rather than rely on
	// it: if the fixture drifts, the clamp would silently become the thing
	// under test.
	aliceRange := envelopes[0].SpeechDB - envelopes[0].FloorDB
	bobRange := envelopes[1].SpeechDB - envelopes[1].FloorDB
	if aliceRange > bobRange+0.5 {
		t.Fatalf("the fixture no longer isolates the decode: alice's range is %.1f dB against bob's %.1f dB, so the ingested cap has clamped her frames",
			aliceRange, bobRange)
	}

	// The same audio, decoded the plain way and folded by the reference
	// implementation, must give the same frames.
	frame, hop := sampleRate*attributionFrameMS/1000, sampleRate*attributionHopMS/1000
	samples, err := ExtractMixedFloats(render)
	if err != nil {
		t.Fatalf("decode render: %v", err)
	}
	want := envelopeFromSamples("alice", samples, frame, hop, attributionHopMS)
	got := envelopes[0]
	if len(got.FrameDB) != len(want.FrameDB) {
		t.Fatalf("alice's envelope has %d frames, the render has %d — it was measured on something else",
			len(got.FrameDB), len(want.FrameDB))
	}
	for i := range want.FrameDB {
		if math.Abs(got.FrameDB[i]-want.FrameDB[i]) > 1e-6 {
			t.Fatalf("frame %d is %.3f dB, the render's is %.3f dB", i, got.FrameDB[i], want.FrameDB[i])
		}
	}
	// And the stretch the SFU lost has to read as speech, not as silence.
	level, ok := got.aboveFloor(2500, 3000)
	if !ok {
		t.Fatal("alice's envelope cannot measure the window her words came from")
	}
	if level < 25 {
		t.Errorf("alice reads %.1f dB above her own floor at 2.5 s; her render has her speaking there", level)
	}
}

// The consequence, at the decision the stage actually makes.
func TestAWordRecoveredFromAnUploadIsNotJudgedAgainstSilence(t *testing.T) {
	requireFFMediaTools(t)
	const sampleRate = 16000
	mkv, render, streams := splitTrackFixture(t)
	word := Word{Text: "recovered", StartMS: 2500, EndMS: 3000}

	// As the stage behaved before: alice measured on the track the SFU
	// delivered, which carries nothing where she was speaking.
	recorded, err := BuildSpeakerEnvelopes(mkv, streams, sampleRate, nil)
	if err != nil {
		t.Fatalf("BuildSpeakerEnvelopes (recorded): %v", err)
	}
	gapBefore, ok := AttributionGapDB(word, "alice", recorded)
	if !ok {
		t.Fatal("expected a measurable gap on the recorded tracks")
	}
	if !ownerQuietDuring(word, "alice", recorded) {
		t.Fatalf("the fixture is not exercising anything: measured on her recorded track, alice does not read as a quiet owner (gap %.1f dB)", gapBefore)
	}
	if gapBefore < 25 {
		t.Fatalf("the fixture is not exercising anything: measured on her recorded track, alice's word is contradicted by only %.1f dB", gapBefore)
	}

	// As it behaves now: alice measured on the render her words were decoded
	// from. She was speaking, so nobody contradicts her and the word is not
	// even flaggable.
	streams[0].SourceAudioPath = render
	spliced, err := BuildSpeakerEnvelopes(mkv, streams, sampleRate, nil)
	if err != nil {
		t.Fatalf("BuildSpeakerEnvelopes (spliced): %v", err)
	}
	gapAfter, ok := AttributionGapDB(word, "alice", spliced)
	if !ok {
		t.Fatal("expected a measurable gap on the spliced render")
	}
	t.Logf("alice's recovered word: %.1f dB against her recorded track, %.1f dB against the audio it came from", gapBefore, gapAfter)
	if gapAfter > 5 {
		t.Errorf("alice is still contradicted by %.1f dB while speaking on the audio her words came from", gapAfter)
	}
	if ownerQuietDuring(word, "alice", spliced) {
		t.Error("alice still reads as a quiet owner over a window where her own render has her speaking; the word stays flaggable and droppable")
	}
}

// One participant, one envelope. The splice renders the whole meeting timeline
// for a participant into a single file and suppresses their other streams; an
// envelope for a suppressed sibling would be a second helping of the same
// audio — a duplicate rival for every word and a second floor to pool.
func TestSuppressedSiblingStreamsGetNoSecondEnvelope(t *testing.T) {
	requireFFMediaTools(t)
	const sampleRate = 16000
	mkv, render, streams := splitTrackFixture(t)

	// Both tracks belong to alice: she rejoined, so remux emitted two. Her
	// render spans both, and the splice points the first at it.
	streams[0].SourceAudioPath = render
	streams[1].SpeakerID = "alice"
	streams[1].SpeakerLabel = "Alice"
	streams[1].SuppressTranscription = true

	envelopes, err := BuildSpeakerEnvelopes(mkv, streams, sampleRate, nil)
	if err != nil {
		t.Fatalf("BuildSpeakerEnvelopes: %v", err)
	}
	if len(envelopes) != 1 {
		t.Fatalf("got %d envelopes for one participant, want 1", len(envelopes))
	}
	if envelopes[0].SpeakerID != "alice" || !envelopes[0].FromSourceAudio {
		t.Fatalf("the surviving envelope is %+v, want alice's spliced one", envelopes[0])
	}

	// The suppression is what does it, not the shared speaker id: the same two
	// streams unsuppressed are two envelopes.
	streams[1].SuppressTranscription = false
	both, err := BuildSpeakerEnvelopes(mkv, streams, sampleRate, nil)
	if err != nil {
		t.Fatalf("BuildSpeakerEnvelopes (unsuppressed): %v", err)
	}
	if len(both) != 2 {
		t.Fatalf("got %d envelopes with nothing suppressed, want 2", len(both))
	}
}

// One person alone in a recording can own several streams: remux emits a fresh
// one on every rotation or rejoin. Attribution compares PARTICIPANTS, so that
// meeting must skip the stage rather than decode both tracks and then find
// there was never a rival to measure against.
func TestOneParticipantWithSeveralStreamsIsNotAMeeting(t *testing.T) {
	streams := []AudioStream{
		{Index: 0, SpeakerID: "spk_solo", SpeakerLabel: "Solo"},
		{Index: 1, SpeakerID: "spk_solo", SpeakerLabel: "Solo"}, // they rejoined
	}
	segments := []Segment{{SpeakerID: "spk_solo", StartMS: 0, EndMS: 800, Text: "hello there",
		Words: []Word{{Text: "hello", StartMS: 0, EndMS: 400}, {Text: "there", StartMS: 410, EndMS: 800}}}}

	var out bytes.Buffer
	report := &AttributionProvenance{Mode: "annotate"}
	// A path that cannot be decoded: if the stage tried anyway, its decode
	// warning would appear here.
	got := applyAttributionReported(filepath.Join(t.TempDir(), "does-not-exist.mkv"),
		streams, segments, 16000, BuildConfig{}, nil, &out, report)
	if report.Ran || report.Reason == "" {
		t.Errorf("a single-participant skip must be recorded, got ran=%v reason=%q", report.Ran, report.Reason)
	}
	if strings.Contains(out.String(), "measuring cross-track") || strings.Contains(out.String(), "warn:") {
		t.Errorf("attribution decoded a meeting with one participant in it: %q", out.String())
	}
	for _, seg := range got {
		for _, w := range seg.Words {
			if w.HasAttributionGap {
				t.Errorf("word %q carries evidence from a meeting with no rival track", w.Text)
			}
		}
	}
}

// The synthetic merged-fallback speaker has no track of its own, so it can
// never be decoded and must never become a rival.
func TestTheMergedFallbackSpeakerIsNotMeasured(t *testing.T) {
	kept := measurableStreams([]AudioStream{
		{Index: 0, SpeakerID: "alice"},
		{Index: -1, SpeakerID: "merged"},
		{Index: 1, SpeakerID: "bob", SuppressTranscription: true},
		{Index: 2, SpeakerID: "carol"},
	})
	if len(kept) != 2 || kept[0].SpeakerID != "alice" || kept[1].SpeakerID != "carol" {
		t.Fatalf("measurableStreams kept %+v, want alice and carol", kept)
	}
}
