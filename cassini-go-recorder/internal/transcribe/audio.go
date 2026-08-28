package transcribe

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// AudioStream represents one audio track in the source MKV.
type AudioStream struct {
	Index         int
	ParticipantID string
	SpeakerID     string
	SpeakerLabel  string
	Channels      int
	StartTimeMS   int64
	// TimelineDurationMS is the duration of the containing recording. It is
	// carried from the initial probe so PCM extraction can reserve the one
	// unavoidable []float32 result exactly once, without retaining a second
	// full-duration []byte buffer or re-probing the file for every stream.
	TimelineDurationMS int64
}

type ffprobeOutput struct {
	Streams []struct {
		Index     int    `json:"index"`
		CodecType string `json:"codec_type"`
		Channels  int    `json:"channels"`
		StartTime string `json:"start_time"`
		Tags      struct {
			Title           string `json:"title"`
			ParticipantID   string `json:"PARTICIPANT_ID"`
			ParticipantName string `json:"PARTICIPANT_NAME"`
		} `json:"tags"`
	} `json:"streams"`
	Format struct {
		DurationStr string `json:"duration"`
	} `json:"format"`
}

// ProbeMKV returns all audio streams and the recording duration in ms.
func ProbeMKV(mkv string) ([]AudioStream, int64, error) {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-show_entries", "stream=index,codec_type,channels,start_time:stream_tags=title,participant_id,participant_name:format=duration",
		"-of", "json",
		mkv,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, 0, fmt.Errorf("ffprobe: %w", err)
	}
	var probe ffprobeOutput
	if err := json.Unmarshal(out, &probe); err != nil {
		return nil, 0, fmt.Errorf("parse ffprobe output: %w", err)
	}

	durSec, _ := strconv.ParseFloat(strings.TrimSpace(probe.Format.DurationStr), 64)
	durationMs := int64(math.Round(durSec * 1000))

	audioIdx := 0
	var streams []AudioStream
	for _, s := range probe.Streams {
		if s.CodecType != "audio" {
			continue
		}
		participantID := strings.TrimSpace(s.Tags.ParticipantID)
		label := strings.TrimSpace(s.Tags.ParticipantName)
		if label == "" {
			label = strings.TrimSpace(s.Tags.Title)
		}
		if label == "" {
			label = fmt.Sprintf("Speaker %d", audioIdx+1)
		}
		speakerIdentity := participantID
		if speakerIdentity == "" {
			// Legacy MKVs predate participant tags and use the stream title as
			// their only stable speaker identity.
			speakerIdentity = label
		}
		streams = append(streams, AudioStream{
			Index:              s.Index,
			ParticipantID:      participantID,
			SpeakerID:          speakerIDFromLabel(speakerIdentity),
			SpeakerLabel:       label,
			Channels:           s.Channels,
			StartTimeMS:        maxInt64(0, durationStringToMS(s.StartTime)),
			TimelineDurationMS: durationMs,
		})
		audioIdx++
	}
	if len(streams) == 0 {
		return nil, 0, fmt.Errorf("no audio streams found in %s", mkv)
	}
	return streams, durationMs, nil
}

// ExtractSpeakerFloats extracts one audio stream as []float32 (16 kHz mono,
// normalised to [-1, 1]) by streaming raw PCM from ffmpeg. The return type
// necessarily owns four bytes per sample; decoding incrementally avoids the
// former additional two-byte-per-sample, full-duration raw buffer.
func ExtractSpeakerFloats(mkv string, stream AudioStream) ([]float32, error) {
	durationMS := stream.TimelineDurationMS
	if durationMS <= 0 {
		// Preserve the memory bound for direct callers that construct AudioStream
		// themselves instead of using ProbeMKV. Normal builds avoid this extra
		// probe because the duration is carried on each discovered stream.
		durationMS, _ = AudioDurationMS(mkv)
	}
	cmd := exec.Command("ffmpeg",
		"-v", "error",
		"-y",
		"-i", mkv,
		"-map", fmt.Sprintf("0:%d", stream.Index),
		"-vn",
		"-sn",
		"-dn",
		"-af", sparseTimelineAudioFilter(),
		"-ac", "1",
		"-ar", "16000",
		"-f", "s16le",
		"pipe:1",
	)
	samples, err := runPCM16LECommand(cmd, expectedPCMSamples(durationMS, 16000))
	if err != nil {
		return nil, fmt.Errorf("ffmpeg extract speaker %d: %w", stream.Index, err)
	}
	return samples, nil
}

// ExtractMixedFloats decodes the mixed meeting.webm (or any single-stream
// audio file) as []float32 (16 kHz mono, normalised to [-1, 1]). Used by
// the merged-fallback transcription path.
func ExtractMixedFloats(audioPath string) ([]float32, error) {
	durationMS, _ := AudioDurationMS(audioPath)
	cmd := exec.Command("ffmpeg",
		"-v", "error",
		"-y",
		"-i", audioPath,
		"-vn",
		"-sn",
		"-dn",
		"-ac", "1",
		"-ar", "16000",
		"-f", "s16le",
		"pipe:1",
	)
	samples, err := runPCM16LECommand(cmd, expectedPCMSamples(durationMS, 16000))
	if err != nil {
		return nil, fmt.Errorf("ffmpeg extract mixed audio: %w", err)
	}
	return samples, nil
}

const pcmReadChunkBytes = 64 * 1024

// runPCM16LECommand starts an ffmpeg command whose stdout is signed 16-bit
// little-endian PCM and converts it incrementally. Both pipes stay bounded:
// stdout is consumed in fixed chunks and repeated decoder diagnostics cannot
// grow stderr without limit on malformed media.
func runPCM16LECommand(cmd *exec.Cmd, expectedSamples int) ([]float32, error) {
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

	samples, readErr := readPCM16LEFloats(stdout, expectedSamples)
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
	return samples, nil
}

// boundedBuffer implements io.Writer while retaining at most limit bytes.
// Write still reports the full input length so a noisy child process cannot
// block on stderr merely because the diagnostic prefix is complete.
type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	originalLen := len(p)
	if remaining := b.limit - b.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return originalLen, nil
}

// readPCM16LEFloats converts fixed-size chunks and tolerates arbitrarily short
// reads. expectedSamples is a capacity hint only; callers still get every
// decoded sample if container metadata under-reports the duration.
func readPCM16LEFloats(r io.Reader, expectedSamples int) ([]float32, error) {
	if expectedSamples < 0 {
		expectedSamples = 0
	}
	samples := make([]float32, 0, expectedSamples)
	raw := make([]byte, pcmReadChunkBytes)
	for {
		n, err := io.ReadFull(r, raw)
		if n%2 != 0 {
			return nil, fmt.Errorf("decoded PCM has odd byte count")
		}
		if n > 0 {
			oldLen := len(samples)
			sampleCount := n / 2
			newLen := oldLen + sampleCount
			if newLen <= cap(samples) {
				samples = samples[:newLen]
			} else {
				// The duration hint can be absent or slightly low. Growth is only a
				// fallback; normal probed recordings stay within the exact initial
				// allocation plus the one-second allowance below.
				samples = append(samples, make([]float32, sampleCount)...)
			}
			for i := 0; i < sampleCount; i++ {
				lo := raw[i*2]
				hi := raw[i*2+1]
				s16 := int16(uint16(lo) | uint16(hi)<<8)
				samples[oldLen+i] = float32(s16) / 32768.0
			}
		}

		switch err {
		case nil:
			continue
		case io.EOF, io.ErrUnexpectedEOF:
			return samples, nil
		default:
			return nil, fmt.Errorf("read decoded PCM: %w", err)
		}
	}
}

func expectedPCMSamples(durationMS int64, sampleRate int) int {
	if durationMS <= 0 || sampleRate <= 0 {
		return 0
	}
	duration := uint64(durationMS)
	rate := uint64(sampleRate)
	if duration > (^uint64(0)-999)/rate {
		return 0
	}
	samples := (duration*rate + 999) / 1000
	// Allow a second for codec/resampler tail rounding. This is 64 KiB at
	// 16 kHz float32 and prevents a whole-slice growth for tiny metadata skew.
	if samples > ^uint64(0)-rate {
		return 0
	}
	samples += rate
	if samples > uint64(^uint(0)>>1) {
		return 0
	}
	return int(samples)
}

// MixDownToWebM mixes all audio streams from the MKV into a single-channel
// 48 kHz Opus WebM file.
func MixDownToWebM(mkv string, streams []AudioStream, outPath string) error {
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
		// Decode each participant track to a gap-preserving WAV first. Mixing
		// directly from sparse MKV tracks risks flattening timestamp gaps, which
		// turns turn-taking speech into artificial overlap in the final artifact.
		// We also re-apply the stream's global start offset so late joins stay on
		// the shared meeting timeline instead of snapping back to t=0.
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

// PCMsha256FromWebM decodes the WebM audio to 48 kHz mono s16le PCM and
// returns its SHA-256 hex digest. Used for portable meeting integrity.
func PCMsha256FromWebM(webmPath string) (string, int64, error) {
	cmd := exec.Command("ffmpeg",
		"-i", webmPath,
		"-f", "s16le",
		"-ac", "1",
		"-ar", "48000",
		"pipe:1",
	)
	pr, pw, err := os.Pipe()
	if err != nil {
		return "", 0, err
	}
	cmd.Stdout = pw
	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		return "", 0, err
	}
	pw.Close()

	h := sha256.New()
	n, copyErr := io.Copy(h, pr)
	pr.Close()
	waitErr := cmd.Wait()
	if copyErr != nil {
		return "", 0, fmt.Errorf("read decoded pcm: %w", copyErr)
	}
	if waitErr != nil {
		return "", 0, fmt.Errorf("ffmpeg pcm decode failed: %w", waitErr)
	}

	return fmt.Sprintf("%x", h.Sum(nil)), n / 2, nil // n bytes / 2 bytes per s16 sample
}

// AudioDurationMS returns the duration of an audio file in milliseconds.
func AudioDurationMS(path string) (int64, error) {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe duration: %w", err)
	}
	durSec, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0, fmt.Errorf("parse duration %q: %w", strings.TrimSpace(string(out)), err)
	}
	return int64(math.Round(durSec * 1000)), nil
}

func durationStringToMS(value string) int64 {
	if strings.TrimSpace(value) == "" || value == "N/A" {
		return 0
	}
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return int64(math.Round(seconds * 1000.0))
}

func runFFmpegQuiet(args ...string) error {
	cmd := exec.Command("ffmpeg", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg: %w\n%s", err, truncate(string(out), 800))
	}
	return nil
}

// decodeTrackWithSparseGaps turns one source stream into a PCM WAV whose sample
// timeline matches the meeting timeline. This is the critical anti-regression
// step for sparse tracks: without async resampling, FFmpeg emits only packets
// that exist and silently drops the long gaps between speaking turns. Packet
// PTS is authoritative here: aresample materializes both the initial offset and
// later gaps, including for rotated tracks whose stream start_time metadata is
// zero despite a much later first packet.
func decodeTrackWithSparseGaps(mkv string, stream AudioStream, sampleRate int, outPath string) error {
	args := []string{
		"-y",
		"-v", "error",
		"-i", mkv,
		"-map", fmt.Sprintf("0:%d", stream.Index),
		"-vn",
		"-sn",
		"-dn",
		"-af", sparseTimelineAudioFilter(),
		"-ac", "1",
		"-ar", strconv.Itoa(sampleRate),
		"-c:a", "pcm_s16le",
		outPath,
	}
	return runFFmpegQuiet(args...)
}

func sparseTimelineAudioFilter() string {
	// Sparse meeting tracks can have long timestamp gaps during mute / silence.
	// We must materialize those holes as actual silence before piping raw PCM,
	// otherwise separate speaking turns collapse together in both STT and
	// mixdown. Do not add stream.start_time as a separate delay: the input packet
	// PTS already contains it, so doing both shifts late streams twice.
	return "aresample=async=1:first_pts=0"
}

func speakerIDFromLabel(label string) string {
	const maxSlugBytes = 48
	var slug strings.Builder
	lastWasSeparator := false
	for _, r := range strings.ToLower(label) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if slug.Len() < maxSlugBytes {
				slug.WriteRune(r)
			}
			lastWasSeparator = false
			continue
		}
		if slug.Len() > 0 && slug.Len() < maxSlugBytes && !lastWasSeparator {
			slug.WriteByte('_')
		}
		lastWasSeparator = true
	}
	clean := strings.Trim(slug.String(), "_")
	if clean == "" {
		clean = "unknown"
	}
	// Sanitisation is intentionally lossy (slashes, punctuation, case and
	// non-ASCII runes can collapse to the same slug). Bind the readable slug to
	// the exact identity bytes with a 96-bit SHA-256 suffix so distinct
	// participant IDs cannot silently merge.
	digest := sha256.Sum256([]byte(label))
	return fmt.Sprintf("spk_%s_%x", clean, digest[:12])
}

// WorkPath returns a path inside the bundle's _work subdirectory,
// creating the directory if needed.
func WorkPath(bundleDir, name string) (string, error) {
	dir := filepath.Join(bundleDir, "_work")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return "..." + s[len(s)-max:]
}

func maxInt64(a int64, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
