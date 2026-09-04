package transcribe

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The published mix, in two phases.
//
// Mixing used to be one call: decode every recorded track to a temporary WAV,
// run one ffmpeg, throw the WAVs away. Splicing participant uploads into the
// published audio needs a seam in the middle of that — the decoded tracks are
// exactly the floor an upload is laid over, and they already exist, on disk, on
// the meeting timeline, at the rate the encoder wants. So the decode is now
// PrepareMix, the encode is Encode, and between them ApplySourceAudio may
// replace a participant's track(s) with a spliced render of the same span.
//
// Nothing about the encode itself changed. With no substitution, Inputs returns
// the same files in the same order and mixEncodeArgs returns the same argument
// list, so a build with no usable upload produces a byte-identical meeting.webm
// to the one the previous code produced. That is a property with a test on it.
type meetingMix struct {
	dir string
	// tracks[i] is the decoded WAV for streams[i], in stream order.
	tracks []string
	// replacement[i], when set, stands in for streams[i] in the encode: a
	// spliced render covering that participant's whole timeline.
	replacement []string
	// sibling[i] means streams[i]'s recorded audio is already inside another
	// stream's replacement. A participant who rejoined has several tracks; the
	// render sums them, so feeding them to amix as well would play their words
	// twice at double amplitude.
	sibling []bool
	// TimelineSamples is the longest decoded track, which is exactly what
	// amix=duration=longest produces. It is the timeline the splice places
	// against — measured from the recorded material rather than from the
	// encoded mix, which does not exist yet.
	TimelineSamples int
	// useAmix is decided on the ORIGINAL stream count, never on how many inputs
	// survive substitution. Today a single-stream meeting skips amix (and with
	// it the limiter) and a multi-stream one does not; a participant whose
	// several tracks collapse into one rendered input must not silently change
	// which of those two paths their meeting takes.
	useAmix bool
}

// PrepareMix decodes every recorded stream onto the meeting timeline and leaves
// the results on disk for the caller to encode, and for the splice to overlay.
func PrepareMix(mkv string, streams []AudioStream) (*meetingMix, error) {
	if len(streams) == 0 {
		return nil, fmt.Errorf("no streams to mix")
	}
	workDir, err := os.MkdirTemp("", "cassini-mix-*")
	if err != nil {
		return nil, fmt.Errorf("create mix work dir: %w", err)
	}
	mix := &meetingMix{
		dir:         workDir,
		tracks:      make([]string, len(streams)),
		replacement: make([]string, len(streams)),
		sibling:     make([]bool, len(streams)),
		useAmix:     len(streams) > 1,
	}
	for i, stream := range streams {
		trackPath := filepath.Join(workDir, fmt.Sprintf("track-%02d.wav", i+1))
		// Decode each participant track to a gap-preserving WAV first. Mixing
		// directly from sparse MKV tracks risks flattening timestamp gaps, which
		// turns turn-taking speech into artificial overlap in the final artifact.
		// We also re-apply the stream's global start offset so late joins stay on
		// the shared meeting timeline instead of snapping back to t=0.
		if err := decodeTrackWithSparseGaps(mkv, stream, 48000, trackPath); err != nil {
			mix.Close()
			return nil, fmt.Errorf("decode track %d for mix: %w", stream.Index, err)
		}
		mix.tracks[i] = trackPath
		wav, err := openWAV(trackPath)
		if err != nil {
			mix.Close()
			return nil, fmt.Errorf("read decoded track %d: %w", stream.Index, err)
		}
		if wav.samples > mix.TimelineSamples {
			mix.TimelineSamples = wav.samples
		}
		_ = wav.Close()
	}
	return mix, nil
}

// Close removes the decoded tracks and any render written beside them. Safe to
// call more than once.
func (m *meetingMix) Close() {
	if m == nil || m.dir == "" {
		return
	}
	_ = os.RemoveAll(m.dir)
	m.dir = ""
}

// RenderPath is where a spliced render for one speaker belongs: beside the
// decoded tracks, so it disappears with them. The bundle keeps a 16 kHz
// derivative of it for transcription; the 48 kHz original is a mix input and
// has no reason to outlive the mix.
func (m *meetingMix) RenderPath(name string) string {
	return filepath.Join(m.dir, "render-"+name+".wav")
}

// TrackPaths returns the decoded WAVs for the given stream indexes, in order.
func (m *meetingMix) TrackPaths(streamIdx []int) []string {
	paths := make([]string, 0, len(streamIdx))
	for _, idx := range streamIdx {
		if idx >= 0 && idx < len(m.tracks) {
			paths = append(paths, m.tracks[idx])
		}
	}
	return paths
}

// Substitute makes `path` stand in for every one of a participant's streams:
// it takes the place of the first, and the rest drop out of the mix because
// their audio is already summed into it.
func (m *meetingMix) Substitute(streamIdx []int, path string) {
	if len(streamIdx) == 0 {
		return
	}
	m.replacement[streamIdx[0]] = path
	for _, idx := range streamIdx[1:] {
		m.sibling[idx] = true
	}
}

// Substituted reports whether any participant's audio now comes from a render.
func (m *meetingMix) Substituted() bool {
	for _, path := range m.replacement {
		if path != "" {
			return true
		}
	}
	return false
}

// RevertSubstitutions puts the recorded tracks back. The renders are left on
// disk — the transcript still reads a derivative of them — but the published
// mix goes back to being exactly what it would have been without ingestion.
func (m *meetingMix) RevertSubstitutions() {
	for i := range m.replacement {
		m.replacement[i] = ""
		m.sibling[i] = false
	}
}

// Inputs lists the files the encoder mixes, in stream order.
func (m *meetingMix) Inputs() []string {
	inputs := make([]string, 0, len(m.tracks))
	for i, track := range m.tracks {
		if m.sibling[i] {
			continue
		}
		if m.replacement[i] != "" {
			inputs = append(inputs, m.replacement[i])
			continue
		}
		inputs = append(inputs, track)
	}
	return inputs
}

// Encode writes the published mix.
func (m *meetingMix) Encode(outPath string) error {
	inputs := m.Inputs()
	if len(inputs) == 0 {
		return fmt.Errorf("no inputs to mix")
	}
	return runFFmpegQuiet(mixEncodeArgs(inputs, m.useAmix, outPath)...)
}

// mixEncodeArgs is the whole encoder contract, in one place, so that "a build
// with no usable upload produces the same bytes as before" is a claim a test
// can check against a literal argument list rather than against prose.
//
// Plain sum (normalize=0) with a limiter after it, mono 48 kHz Opus at 64 kbit
// in voip mode. duration=longest is what makes TimelineSamples the timeline.
func mixEncodeArgs(inputs []string, useAmix bool, outPath string) []string {
	if !useAmix {
		return []string{
			"-y",
			"-v", "error",
			"-i", inputs[0],
			"-map", "0:a:0",
			"-ac", "1",
			"-ar", "48000",
			"-c:a", "libopus",
			"-b:a", "64k",
			"-vbr", "on",
			"-compression_level", "10",
			"-application", "voip",
			outPath,
		}
	}
	args := []string{"-y", "-v", "error"}
	var filterInputs strings.Builder
	for _, input := range inputs {
		args = append(args, "-i", input)
	}
	for i := range inputs {
		filterInputs.WriteString(fmt.Sprintf("[%d:a]", i))
	}
	filter := fmt.Sprintf("%samix=inputs=%d:duration=longest:normalize=0,alimiter=limit=0.95[out]", filterInputs.String(), len(inputs))
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
	return args
}

// MixDownToWebM mixes all audio streams from the MKV into a single-channel
// 48 kHz Opus WebM file, with no splice. PrepareMix plus Encode; kept as one
// call for the paths that only want the recorded mix.
func MixDownToWebM(mkv string, streams []AudioStream, outPath string) error {
	mix, err := PrepareMix(mkv, streams)
	if err != nil {
		return err
	}
	defer mix.Close()
	return mix.Encode(outPath)
}
