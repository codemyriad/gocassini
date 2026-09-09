package transcribe

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The encoder contract, as a literal argument list.
//
// "A build with no usable upload publishes exactly the audio it published
// before" is the one promise the splice makes to every meeting that does not
// use it, and libopus keeps it only for identical samples through identical
// arguments. Prose cannot check that; this can. If a filter is added, an option
// reordered, or a rate changed, this test says so in the one place where the
// reason is legible, before the sample comparison below has to explain it.
func TestMixEncodeArgsAreTheOnesTheMixHasAlwaysUsed(t *testing.T) {
	single := mixEncodeArgs([]string{"track-01.wav"}, false, "out.webm")
	wantSingle := []string{
		"-y",
		"-v", "error",
		"-i", "track-01.wav",
		"-map", "0:a:0",
		"-ac", "1",
		"-ar", "48000",
		"-c:a", "libopus",
		"-b:a", "64k",
		"-vbr", "on",
		"-compression_level", "10",
		"-application", "voip",
		"out.webm",
	}
	if !reflect.DeepEqual(single, wantSingle) {
		t.Fatalf("single-track encode args changed:\n got %q\nwant %q", single, wantSingle)
	}

	multi := mixEncodeArgs([]string{"track-01.wav", "track-02.wav"}, true, "out.webm")
	wantMulti := []string{
		"-y",
		"-v", "error",
		"-i", "track-01.wav",
		"-i", "track-02.wav",
		"-filter_complex", "[0:a][1:a]amix=inputs=2:duration=longest:normalize=0,alimiter=limit=0.95[out]",
		"-map", "[out]",
		"-ac", "1",
		"-ar", "48000",
		"-c:a", "libopus",
		"-b:a", "64k",
		"-vbr", "on",
		"-compression_level", "10",
		"-application", "voip",
		"out.webm",
	}
	if !reflect.DeepEqual(multi, wantMulti) {
		t.Fatalf("multi-track encode args changed:\n got %q\nwant %q", multi, wantMulti)
	}
}

// mixAudioSHA256 identifies the AUDIO in a mix, which is what "unchanged" has
// to mean here.
//
// The file bytes cannot be compared: Matroska stamps a fresh random segment
// identifier into every WebM ffmpeg writes, so two encodes of the same samples
// have never produced the same file, before this change or after it. What is
// identical is the audio — and it is the audio that is hashed into every
// transcript as media.sha256, and whose Opus essence becomes the published
// meeting's identity.
func mixAudioSHA256(t *testing.T, path string) string {
	t.Helper()
	digest, _, err := PCMsha256FromWebM(path)
	if err != nil {
		t.Fatalf("hash %s: %v", path, err)
	}
	return digest
}

// What this proves, exactly: the encoder is deterministic, and TimelineSamples
// describes the mix that comes out of it.
//
// It used to claim more. It compared MixDownToWebM against PrepareMix plus
// Encode as if the two were the old code and the new one — but MixDownToWebM
// *is* PrepareMix plus Encode now, so both sides run the same functions and the
// comparison cannot fail however the split is broken. Determinism is still
// worth pinning, because it is what makes every "the audio did not change"
// assertion in this package mean anything; the claim about the split is checked
// against base's own code in
// TestTheTwoPhaseMixReproducesTheMixdownItReplaced.
func TestTheEncoderIsDeterministicAndTimelineSamplesDescribesIt(t *testing.T) {
	requireFFMediaTools(t)
	dir := t.TempDir()
	meeting := buildOffsetMeeting(t, dir, 1.5)
	streams, _, err := ProbeMKV(meeting)
	if err != nil {
		t.Fatalf("ProbeMKV: %v", err)
	}

	oneCall := filepath.Join(dir, "one-call.webm")
	if err := MixDownToWebM(meeting, streams, oneCall); err != nil {
		t.Fatalf("MixDownToWebM: %v", err)
	}

	mix, err := PrepareMix(meeting, streams)
	if err != nil {
		t.Fatalf("PrepareMix: %v", err)
	}
	defer mix.Close()
	twoPhase := filepath.Join(dir, "two-phase.webm")
	if err := mix.Encode(twoPhase); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if mixAudioSHA256(t, oneCall) != mixAudioSHA256(t, twoPhase) {
		t.Fatal("two encodes of the same decoded tracks produced different audio")
	}

	// The timeline the splice places against is the longest decoded track,
	// which is what amix=duration=longest renders. It has to agree with the
	// mix that was actually encoded, to within the encoder's frame padding.
	encodedMS, err := AudioDurationMS(twoPhase)
	if err != nil {
		t.Fatalf("AudioDurationMS: %v", err)
	}
	timelineMS := int64(mix.TimelineSamples) * 1000 / 48000
	if deltaMS(encodedMS, timelineMS) > 100 {
		t.Fatalf("TimelineSamples says %d ms, the encoded mix is %d ms", timelineMS, encodedMS)
	}
}

// baseMixDownToWebM is the mixdown as it stood BEFORE this branch: a verbatim
// copy of MixDownToWebM from internal/transcribe/audio.go at 8726367, the
// commit this branch starts from.
//
// It is here because the promise "a build with no usable upload publishes
// exactly the audio it published before" is a claim about code that no longer
// exists, and the only honest way to check a claim about old code is to run it.
// The alternative — a PCM digest checked in beside a fixture MKV — would pin
// the libopus build that produced the digest as firmly as it pins this package,
// so it would fail on any machine whose ffmpeg differs by a patch release while
// saying nothing about whether the split preserved anything.
//
// Do not tidy it towards the current code, and do not call it from anything but
// the test below. Its entire value is that it is the old implementation.
func baseMixDownToWebM(mkv string, streams []AudioStream, outPath string) error {
	if len(streams) == 0 {
		return fmt.Errorf("no streams to mix")
	}

	workDir, err := os.MkdirTemp("", "cassini-mix-*")
	if err != nil {
		return fmt.Errorf("create mix work dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	trackPaths := make([]string, len(streams))
	for i, stream := range streams {
		trackPath := filepath.Join(workDir, fmt.Sprintf("track-%02d.wav", i+1))
		if err := decodeTrackWithSparseGaps(mkv, stream, 48000, trackPath); err != nil {
			return fmt.Errorf("decode track %d for mix: %w", stream.Index, err)
		}
		trackPaths[i] = trackPath
	}

	if len(trackPaths) == 1 {
		return runFFmpegQuiet(
			"-y",
			"-v", "error",
			"-i", trackPaths[0],
			"-map", "0:a:0",
			"-ac", "1",
			"-ar", "48000",
			"-c:a", "libopus",
			"-b:a", "64k",
			"-vbr", "on",
			"-compression_level", "10",
			"-application", "voip",
			outPath,
		)
	}

	args := []string{"-y", "-v", "error"}
	var filterInputs strings.Builder
	for _, trackPath := range trackPaths {
		args = append(args, "-i", trackPath)
	}
	for i := range trackPaths {
		filterInputs.WriteString(fmt.Sprintf("[%d:a]", i))
	}
	filter := fmt.Sprintf("%samix=inputs=%d:duration=longest:normalize=0,alimiter=limit=0.95[out]", filterInputs.String(), len(trackPaths))

	args = append(args,
		"-filter_complex", filter,
		"-map", "[out]",
		"-ac", "1",
		"-ar", "48000",
		"-c:a", "libopus",
		"-b:a", "64k",
		"-vbr", "on",
		"-compression_level", "10",
		"-application", "voip",
		outPath,
	)
	return runFFmpegQuiet(args...)
}

// The claim this branch makes to every meeting that uses none of it: the audio
// is what the old mixdown published, sample for sample.
//
// Checked against the old mixdown itself, running in this process against this
// machine's ffmpeg, over the whole path a real build takes — PrepareMix, then
// ApplySourceAudio with an empty capture root, which is what ingestion does for
// every meeting nobody uploaded for, then Encode. Both the amix path and the
// single-track path, because they are different argument lists and only one of
// them is exercised by a two-speaker fixture.
func TestTheTwoPhaseMixReproducesTheMixdownItReplaced(t *testing.T) {
	requireFFMediaTools(t)
	dir := t.TempDir()
	meeting := buildOffsetMeeting(t, dir, 1.5)
	probed, _, err := ProbeMKV(meeting)
	if err != nil {
		t.Fatalf("ProbeMKV: %v", err)
	}
	if len(probed) < 2 {
		t.Fatalf("the fixture has %d streams; the amix path needs two", len(probed))
	}

	for _, tc := range []struct {
		name    string
		streams []AudioStream
	}{
		{name: "two tracks, through amix", streams: probed},
		{name: "one track, without amix", streams: probed[:1]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caseDir := t.TempDir()
			before := filepath.Join(caseDir, "before-the-split.webm")
			if err := baseMixDownToWebM(meeting, tc.streams, before); err != nil {
				t.Fatalf("the mixdown this branch replaced: %v", err)
			}

			mix, err := PrepareMix(meeting, tc.streams)
			if err != nil {
				t.Fatalf("PrepareMix: %v", err)
			}
			defer mix.Close()
			spliced := append([]AudioStream(nil), tc.streams...)
			var log bytes.Buffer
			if reports := ApplySourceAudio(context.Background(), mix, spliced, t.TempDir(), "room1",
				t.TempDir(), &log); reports != nil {
				t.Fatalf("an empty capture root reported %v", reports)
			}
			for i := range spliced {
				if spliced[i].SourceAudioPath != "" || spliced[i].SuppressTranscription {
					t.Fatalf("stream %d was rerouted although nobody uploaded", i)
				}
			}
			after := filepath.Join(caseDir, "after-the-split.webm")
			if err := mix.Encode(after); err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if mixAudioSHA256(t, before) != mixAudioSHA256(t, after) {
				t.Fatal("the split mixdown published different audio from the one it replaced")
			}
		})
	}
}

// The render replaces every one of a participant's streams at once: it takes
// the place of the first, and their siblings drop out because the render
// already contains them. Feeding a sibling to amix as well would play that
// participant's words twice, at double amplitude.
func TestMixInputsCollapseARejoinedParticipant(t *testing.T) {
	mix := &meetingMix{
		tracks:      []string{"track-01.wav", "track-02.wav", "track-03.wav"},
		replacement: make([]string, 3),
		sibling:     make([]bool, 3),
		removed:     make([]bool, 3),
		useAmix:     true,
	}
	if got, want := mix.Inputs(), []string{"track-01.wav", "track-02.wav", "track-03.wav"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("with nothing substituted the inputs are %q, want %q", got, want)
	}
	if mix.Substituted() {
		t.Fatal("an untouched mix claims a substitution")
	}

	// Streams 0 and 2 are the same participant, rejoined.
	mix.Substitute([]int{0, 2}, "render-alice.wav")
	if got, want := mix.Inputs(), []string{"render-alice.wav", "track-02.wav"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after substitution the inputs are %q, want %q", got, want)
	}
	if !mix.Substituted() {
		t.Fatal("a substituted mix does not say so")
	}

	// Reverting the substitution alone (the decoded tracks come back with it;
	// TestTheSpliceNeverWritesIntoTheMixsRecordedTracks checks that against a
	// real recording).
	if err := mix.RevertSubstitutions(); err != nil {
		t.Fatalf("RevertSubstitutions: %v", err)
	}
	if got, want := mix.Inputs(), []string{"track-01.wav", "track-02.wav", "track-03.wav"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reverting left the inputs as %q, want %q", got, want)
	}
}

// The single-input encode path has no limiter and the amix path does, so which
// one a meeting takes must not depend on how many inputs survive substitution.
// A two-speaker meeting where one speaker's two tracks collapse into one render
// still goes through amix, exactly as it does today.
func TestMixKeepsItsEncodePathWhenSubstitutionCollapsesInputs(t *testing.T) {
	mix := &meetingMix{
		tracks:      []string{"track-01.wav", "track-02.wav"},
		replacement: make([]string, 2),
		sibling:     make([]bool, 2),
		removed:     make([]bool, 2),
		useAmix:     true,
	}
	mix.Substitute([]int{0, 1}, "render-alice.wav")
	args := mixEncodeArgs(mix.Inputs(), mix.useAmix, "out.webm")
	found := false
	for _, arg := range args {
		if arg == "[0:a]amix=inputs=1:duration=longest:normalize=0,alimiter=limit=0.95[out]" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a collapsed multi-stream meeting left the amix path: %q", args)
	}
}

// ffmpeg's WAV muxer writes a LIST/INFO chunk between "fmt " and "data", so a
// reader that assumes a 44-byte header is off by the length of that chunk —
// which would place every spliced window at the wrong offset, silently.
func TestOpenWAVFindsTheDataChunkPastALISTChunk(t *testing.T) {
	requireFFMediaTools(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "tone.wav")
	if err := runMediaCommand("ffmpeg", "-y", "-v", "error", "-f", "lavfi",
		"-i", "sine=frequency=440:duration=0.5:sample_rate=48000",
		"-ac", "1", "-ar", "48000", "-c:a", "pcm_s16le", path); err != nil {
		t.Fatalf("synthesise: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	wav, err := openWAV(path)
	if err != nil {
		t.Fatalf("openWAV: %v", err)
	}
	defer wav.Close()
	if wav.sampleRate != 48000 {
		t.Fatalf("sample rate read as %d", wav.sampleRate)
	}
	if want := 24000; wav.samples < want-500 || wav.samples > want+500 {
		t.Fatalf("half a second at 48 kHz read as %d samples", wav.samples)
	}
	// The first sample of a sine starting at phase zero is zero, and the second
	// is not: a header offset that is wrong by a whole chunk lands in the middle
	// of the LIST text instead, which is very much not silence.
	if int(wav.dataOffset) == 44 && len(raw) > 44 && string(raw[36:40]) != "data" {
		t.Fatal("the data chunk was assumed at 44 bytes although the file has a chunk in between")
	}
	got := make([]float32, 2)
	if err := wav.readSamples(0, got); err != nil {
		t.Fatalf("readSamples: %v", err)
	}
	if got[0] != 0 {
		t.Fatalf("the first sample of the tone read as %v, want 0: the data offset is wrong", got[0])
	}
}

// Anything the reader does not fully understand is refused, so a participant
// keeps their recorded audio rather than getting a splice at a wrong offset.
func TestOpenWAVRefusesWhatItCannotPlaceSamplesIn(t *testing.T) {
	requireFFMediaTools(t)
	dir := t.TempDir()

	stereo := filepath.Join(dir, "stereo.wav")
	if err := runMediaCommand("ffmpeg", "-y", "-v", "error", "-f", "lavfi",
		"-i", "sine=frequency=440:duration=0.2:sample_rate=48000",
		"-ac", "2", "-ar", "48000", "-c:a", "pcm_s16le", stereo); err != nil {
		t.Fatalf("synthesise: %v", err)
	}
	if _, err := openWAV(stereo); err == nil {
		t.Fatal("a stereo WAV was accepted as a mono floor")
	}

	float := filepath.Join(dir, "float.wav")
	if err := runMediaCommand("ffmpeg", "-y", "-v", "error", "-f", "lavfi",
		"-i", "sine=frequency=440:duration=0.2:sample_rate=48000",
		"-ac", "1", "-ar", "48000", "-c:a", "pcm_f32le", float); err != nil {
		t.Fatalf("synthesise: %v", err)
	}
	if _, err := openWAV(float); err == nil {
		t.Fatal("a 32-bit float WAV was accepted as a 16-bit floor")
	}

	rf64 := filepath.Join(dir, "huge.wav")
	header := make([]byte, 44)
	copy(header[0:4], "RF64")
	binary.LittleEndian.PutUint32(header[4:8], 0xFFFFFFFF)
	copy(header[8:12], "WAVE")
	if err := os.WriteFile(rf64, header, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := openWAV(rf64); err == nil {
		t.Fatal("an RF64 file was accepted")
	}
}

// A truncated file must read as what it holds, not as what its header promises.
// Reading past a header that outlived its writer would return zeros as if they
// were recorded silence.
func TestOpenWAVTrustsTheFileOverItsHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "truncated.wav")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := writeWAVHeader(f, 1000, 48000); err != nil {
		t.Fatalf("header: %v", err)
	}
	if _, err := f.Write(make([]byte, 200)); err != nil { // 100 samples, not 1000
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	wav, err := openWAV(path)
	if err != nil {
		t.Fatalf("openWAV: %v", err)
	}
	defer wav.Close()
	if wav.samples != 100 {
		t.Fatalf("a header claiming 1000 samples over 100 written read as %d", wav.samples)
	}
}

// A short read must come back as silence, never as the previous chunk.
//
// readSamples converts through one scratch buffer that is reused call after
// call, so bytes ReadAt did not fill still hold the samples of the chunk before
// them. Converting the whole buffer would put that audio into the timeline a
// second time, at a place it never occupied — a repeat, not silence, and one
// that no assertion about the recorded floor would catch. openWAV's clamp keeps
// production off this path today; the buffer reuse is what makes it worth
// nailing down anyway.
func TestReadSamplesNeverDecodesTheScratchItDidNotFill(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "short.wav")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := writeWAVHeader(f, 4, 48000); err != nil {
		t.Fatalf("header: %v", err)
	}
	raw := make([]byte, 8)
	for i, sample := range []int16{9000, -9000, 9000, -9000} {
		binary.LittleEndian.PutUint16(raw[i*2:], uint16(sample))
	}
	if _, err := f.Write(raw); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	wav, err := openWAV(path)
	if err != nil {
		t.Fatalf("openWAV: %v", err)
	}
	defer wav.Close()

	// The one thing that puts a caller past the data chunk: a sample count that
	// says more than the file holds. openWAV computes it from the file, so this
	// is set by hand — which is the point, since the caller is what stands
	// between this reader and the bug today.
	wav.samples = 8

	first := make([]float32, 4)
	if err := wav.readSamples(0, first); err != nil {
		t.Fatalf("readSamples: %v", err)
	}
	if first[0] == 0 {
		t.Fatal("the first chunk read as silence; the fixture is wrong")
	}
	second := make([]float32, 4)
	if err := wav.readSamples(4, second); err != nil {
		t.Fatalf("readSamples: %v", err)
	}
	for i, sample := range second {
		if sample != 0 {
			t.Fatalf("sample %d past the end of the data read as %v, which is the previous chunk's audio, not silence",
				i, sample)
		}
	}
}
