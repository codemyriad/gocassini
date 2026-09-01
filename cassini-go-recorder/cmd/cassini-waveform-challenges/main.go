// Command cassini-waveform-challenges reruns Cassini's review-only waveform
// challenge miner against an existing recording and words transcript.
//
// It performs waveform analysis only: it neither loads nor runs a speech
// recognition model. Audio tracks are decoded sequentially by FFmpeg into
// fixed-size PCM frames, keeping memory use bounded for long meetings.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"gocassini/internal/transcribe"
)

const transcriptWordsVersion = "transcript.words.v1"

type config struct {
	MKVPath        string
	TranscriptPath string
	OutputPath     string
}

// transcriptDocument deliberately declares only the transcript fields used by
// the waveform miner. encoding/json streams past media, speakers, IDs, and any
// future top-level metadata without retaining them in memory.
type transcriptDocument struct {
	Version  string                   `json:"version"`
	Segments []transcriptSegmentEntry `json:"segments"`
}

type transcriptSegmentEntry struct {
	Speaker string                `json:"speaker"`
	StartMS int64                 `json:"startMs"`
	EndMS   int64                 `json:"endMs"`
	Words   []transcriptWordEntry `json:"words"`
}

type transcriptWordEntry struct {
	StartMS int64 `json:"startMs"`
	EndMS   int64 `json:"endMs"`
}

func main() {
	cfg, err := parseFlags(os.Args[1:], os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		exitErr(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg, os.Stdout); err != nil {
		exitErr(err)
	}
}

func parseFlags(args []string, stderr io.Writer) (config, error) {
	var cfg config
	fs := flag.NewFlagSet("cassini-waveform-challenges", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&cfg.MKVPath, "mkv", "", "source multi-track MKV recording")
	fs.StringVar(&cfg.TranscriptPath, "transcript", "", "source transcript.words.v1.json")
	fs.StringVar(&cfg.OutputPath, "output", "", "destination challenges.v1.json")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: %s --mkv meeting.mkv --transcript transcript.words.v1.json --output challenges.v1.json\n", fs.Name())
		fmt.Fprintln(stderr, "Waveform analysis only; no speech recognition model is loaded or run.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if fs.NArg() != 0 {
		return config{}, fmt.Errorf("unexpected positional argument %q", fs.Arg(0))
	}
	if strings.TrimSpace(cfg.MKVPath) == "" {
		return config{}, errors.New("missing --mkv")
	}
	if strings.TrimSpace(cfg.TranscriptPath) == "" {
		return config{}, errors.New("missing --transcript")
	}
	if strings.TrimSpace(cfg.OutputPath) == "" {
		return config{}, errors.New("missing --output")
	}
	return cfg, nil
}

func run(ctx context.Context, cfg config, stdout io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if samePath(cfg.OutputPath, cfg.MKVPath) {
		return errors.New("--output must differ from --mkv")
	}
	if samePath(cfg.OutputPath, cfg.TranscriptPath) {
		return errors.New("--output must differ from --transcript")
	}
	// A failed rerun must not leave a valid-looking result from an older pair
	// of inputs. Clear the destination before opening either source; the final
	// sidecar is installed atomically by the transcribe package.
	if err := clearStaleOutput(cfg.OutputPath); err != nil {
		return fmt.Errorf("clear stale output: %w", err)
	}

	transcriptFile, err := os.Open(cfg.TranscriptPath)
	if err != nil {
		return fmt.Errorf("open transcript: %w", err)
	}
	transcriptHash := sha256.New()
	segments, decodeErr := decodeTranscriptSegments(io.TeeReader(transcriptFile, transcriptHash))
	closeErr := transcriptFile.Close()
	if decodeErr != nil {
		return fmt.Errorf("decode transcript: %w", decodeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close transcript: %w", closeErr)
	}
	if err := transcribe.ValidateSegments(segments); err != nil {
		return fmt.Errorf("validate transcript: %w", err)
	}
	transcriptSHA256 := fmt.Sprintf("%x", transcriptHash.Sum(nil))
	audioSHA256, err := hashFileSHA256(ctx, cfg.MKVPath)
	if err != nil {
		return fmt.Errorf("hash MKV: %w", err)
	}

	streams, durationMS, err := transcribe.ProbeMKV(cfg.MKVPath)
	if err != nil {
		return fmt.Errorf("probe MKV: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "waveform-only analysis: %d audio stream(s), %d transcript segment(s), duration %d ms\n", len(streams), len(segments), durationMS)
	provenance := transcribe.WaveformChallengeProvenance{
		AudioSHA256:      audioSHA256,
		TranscriptPath:   cfg.TranscriptPath,
		TranscriptSHA256: transcriptSHA256,
	}
	if err := transcribe.WriteWaveformChallengesWithProvenance(ctx, cfg.OutputPath, cfg.MKVPath, streams, segments, durationMS, provenance); err != nil {
		return fmt.Errorf("mine waveform challenges: %w", err)
	}
	fmt.Fprintf(stdout, "wrote review-only waveform challenges to %s\n", cfg.OutputPath)
	return nil
}

func clearStaleOutput(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() || (!info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0) {
		return fmt.Errorf("refusing to remove non-file destination %s", path)
	}
	return os.Remove(path)
}

func hashFileSHA256(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	buffer := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			if _, err := hash.Write(buffer[:n]); err != nil {
				return "", err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func samePath(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr == nil && rightErr == nil && filepath.Clean(leftAbs) == filepath.Clean(rightAbs) {
		return true
	}
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}

func decodeTranscriptSegments(r io.Reader) ([]transcribe.Segment, error) {
	decoder := json.NewDecoder(r)
	var document transcriptDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	if document.Version != transcriptWordsVersion {
		return nil, fmt.Errorf("unsupported transcript version %q (want %q)", document.Version, transcriptWordsVersion)
	}
	// Reject a second JSON value while still permitting trailing whitespace.
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("unexpected data after transcript document")
		}
		return nil, fmt.Errorf("read trailing transcript data: %w", err)
	}

	segments := make([]transcribe.Segment, len(document.Segments))
	for segmentIndex, entry := range document.Segments {
		words := make([]transcribe.Word, len(entry.Words))
		for wordIndex, word := range entry.Words {
			words[wordIndex] = transcribe.Word{
				StartMS: word.StartMS,
				EndMS:   word.EndMS,
			}
		}
		segments[segmentIndex] = transcribe.Segment{
			SpeakerID: entry.Speaker,
			StartMS:   entry.StartMS,
			EndMS:     entry.EndMS,
			Words:     words,
		}
	}
	return segments, nil
}

func exitErr(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
