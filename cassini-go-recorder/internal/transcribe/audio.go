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
}

type ffprobeOutput struct {
	Streams []struct {
		Index     int    `json:"index"`
		CodecType string `json:"codec_type"`
		Channels  int    `json:"channels"`
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
		"-show_entries", "stream=index,codec_type,channels:stream_tags=title:format=duration",
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
func ExtractSpeakerFloats(mkv string, streamIndex int) ([]float32, error) {
	cmd := exec.Command("ffmpeg",
		"-y",
		"-i", mkv,
		"-map", fmt.Sprintf("0:%d", streamIndex),
		"-ac", "1",
		"-ar", "16000",
		"-f", "s16le",
		"pipe:1",
	)
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg extract speaker %d: %w", streamIndex, err)
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
func MixDownToWebM(mkv string, numStreams int, outPath string) error {
	if numStreams == 0 {
		return fmt.Errorf("no streams to mix")
	}
	args := []string{"-y", "-i", mkv}

	var filterInputs string
	for i := 0; i < numStreams; i++ {
		filterInputs += fmt.Sprintf("[0:a:%d]", i)
	}
	filter := fmt.Sprintf("%samix=inputs=%d:duration=longest:normalize=1[out]", filterInputs, numStreams)

	args = append(args,
		"-filter_complex", filter,
		"-map", "[out]",
		"-ac", "1",
		"-ar", "48000",
		"-c:a", "libopus",
		"-b:a", "64k",
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

func runFFmpegQuiet(args ...string) error {
	cmd := exec.Command("ffmpeg", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg: %w\n%s", err, truncate(string(out), 800))
	}
	return nil
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
