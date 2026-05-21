package transcribe

import (
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
	Index        int
	SpeakerID    string
	SpeakerLabel string
	Channels     int
	StartTimeMS  int64
}

type ffprobeOutput struct {
	Streams []struct {
		Index     int    `json:"index"`
		CodecType string `json:"codec_type"`
		Channels  int    `json:"channels"`
		StartTime string `json:"start_time"`
		Tags      struct {
			Title string `json:"title"`
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
		"-show_entries", "stream=index,codec_type,channels,start_time:stream_tags=title:format=duration",
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
		label := strings.TrimSpace(s.Tags.Title)
		if label == "" {
			label = fmt.Sprintf("Speaker %d", audioIdx+1)
		}
		id := speakerIDFromLabel(label)
		streams = append(streams, AudioStream{
			Index:        s.Index,
			SpeakerID:    id,
			SpeakerLabel: label,
			Channels:     s.Channels,
			StartTimeMS:  maxInt64(0, durationStringToMS(s.StartTime)),
		})
		audioIdx++
	}
	if len(streams) == 0 {
		return nil, 0, fmt.Errorf("no audio streams found in %s", mkv)
	}
	return streams, durationMs, nil
}

// ExtractSpeakerFloats extracts one audio stream as []float32 (16 kHz mono,
// normalised to [-1, 1]) by piping raw PCM from ffmpeg.
func ExtractSpeakerFloats(mkv string, stream AudioStream) ([]float32, error) {
	cmd := exec.Command("ffmpeg",
		"-v", "error",
		"-y",
		"-i", mkv,
		"-map", fmt.Sprintf("0:%d", stream.Index),
		"-vn",
		"-sn",
		"-dn",
		"-af", sparseTimelineAudioFilter(stream.StartTimeMS),
		"-ac", "1",
		"-ar", "16000",
		"-f", "s16le",
		"pipe:1",
	)
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg extract speaker %d: %w", stream.Index, err)
	}
	samples := make([]float32, len(raw)/2)
	for i := range samples {
		lo := raw[i*2]
		hi := raw[i*2+1]
		s16 := int16(uint16(lo) | uint16(hi)<<8)
		samples[i] = float32(s16) / 32768.0
	}
	return samples, nil
}

// ExtractMixedFloats decodes the mixed meeting.webm (or any single-stream
// audio file) as []float32 (16 kHz mono, normalised to [-1, 1]). Used by
// the merged-fallback transcription path.
func ExtractMixedFloats(audioPath string) ([]float32, error) {
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
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg extract mixed audio: %w", err)
	}
	samples := make([]float32, len(raw)/2)
	for i := range samples {
		lo := raw[i*2]
		hi := raw[i*2+1]
		s16 := int16(uint16(lo) | uint16(hi)<<8)
		samples[i] = float32(s16) / 32768.0
	}
	return samples, nil
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
	n, _ := io.Copy(h, pr)
	pr.Close()
	_ = cmd.Wait()

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
// that exist and silently drops the long gaps between speaking turns. The
// explicit delay restores the stream's container start offset so late joins and
// rejoin tracks remain aligned with the rest of the meeting.
func decodeTrackWithSparseGaps(mkv string, stream AudioStream, sampleRate int, outPath string) error {
	args := []string{
		"-y",
		"-v", "error",
		"-i", mkv,
		"-map", fmt.Sprintf("0:%d", stream.Index),
		"-vn",
		"-sn",
		"-dn",
		"-af", sparseTimelineAudioFilter(stream.StartTimeMS),
		"-ac", "1",
		"-ar", strconv.Itoa(sampleRate),
		"-c:a", "pcm_s16le",
		outPath,
	}
	return runFFmpegQuiet(args...)
}

func sparseTimelineAudioFilter(startTimeMS int64) string {
	// Sparse meeting tracks can have long timestamp gaps during mute / silence.
	// We must materialize those holes as actual silence before piping raw PCM,
	// otherwise separate speaking turns collapse together in both STT and mixdown.
	filter := "aresample=async=1:first_pts=0"
	if startTimeMS > 0 {
		filter += fmt.Sprintf(",adelay=%d:all=1", startTimeMS)
	}
	return filter
}

func speakerIDFromLabel(label string) string {
	clean := strings.ToLower(label)
	clean = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, clean)
	clean = strings.Trim(clean, "_")
	if clean == "" {
		clean = "unknown"
	}
	return "spk_" + clean
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
