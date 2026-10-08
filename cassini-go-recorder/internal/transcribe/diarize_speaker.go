package transcribe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SpeakerTurnsFormat identifies a stored diarizer result for one participant.
const SpeakerTurnsFormat = "cassini.speaker-turns.v1"

// SpeakerTurnSet is the frozen diarizer output for one participant's audio.
// It holds times and arbitrary integer voice labels only: no audio and no
// voice embedding, so keeping it does not keep biometric data. Splits are
// always re-applied from a stored set, never by re-running the model, so the
// voice ids a user has named cannot move.
type SpeakerTurnSet struct {
	Format    string `json:"format"`
	SpeakerID string `json:"speakerId"`
	Streams   []int  `json:"streams"`
	// Source binds the turns to the exact recording they were measured on,
	// so they are never replayed against different audio.
	Source     TurnSetSource      `json:"source"`
	Assignment string             `json:"assignment"`
	DurationMS int64              `json:"durationMs"`
	Model      TurnSetModel       `json:"model"`
	Params     TurnSetParams      `json:"params"`
	ElapsedMS  int64              `json:"elapsedMs"`
	Turns      []SpeakerTurnEntry `json:"turns"`
}

type TurnSetSource struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}

type TurnSetModel struct {
	Name    string `json:"name"`
	SHA256  string `json:"sha256"`
	Runtime string `json:"runtime,omitempty"`
}

type TurnSetParams struct {
	MinDurationOn  float64 `json:"minDurationOn"`
	MinDurationOff float64 `json:"minDurationOff"`
	Threads        int     `json:"threads"`
	Provider       string  `json:"provider"`
}

type SpeakerTurnEntry struct {
	StartMS int64 `json:"startMs"`
	EndMS   int64 `json:"endMs"`
	Speaker int   `json:"speaker"`
}

// ErrSpeakerNotFound means the recording has no stream for that participant.
var ErrSpeakerNotFound = errors.New("no audio stream for that speaker")

// SpeakerTurns returns the turns of a stored set.
func (s SpeakerTurnSet) SpeakerTurns() []SpeakerTurn {
	turns := make([]SpeakerTurn, len(s.Turns))
	for i, t := range s.Turns {
		turns[i] = SpeakerTurn{StartMS: t.StartMS, EndMS: t.EndMS, Speaker: t.Speaker}
	}
	return turns
}

// ReadSpeakerTurnSet loads and checks a stored set.
func ReadSpeakerTurnSet(path string) (SpeakerTurnSet, error) {
	var set SpeakerTurnSet
	b, err := os.ReadFile(path)
	if err != nil {
		return set, err
	}
	if err := json.Unmarshal(b, &set); err != nil {
		return set, fmt.Errorf("%s: %w", path, err)
	}
	if set.Format != SpeakerTurnsFormat {
		return set, fmt.Errorf("%s: unsupported format %q", path, set.Format)
	}
	if set.SpeakerID == "" {
		return set, fmt.Errorf("%s: missing speakerId", path)
	}
	return set, nil
}

// Check refuses a set that cannot be applied faithfully: one that does not
// say which recording and which model it came from, or whose turns are not
// spans of time with a voice number.
func (s SpeakerTurnSet) Check() error {
	if !isHexSHA256(s.Source.SHA256) {
		return fmt.Errorf("turns of %s do not name the recording they were measured on", s.SpeakerID)
	}
	if strings.TrimSpace(s.Model.SHA256) == "" {
		return fmt.Errorf("turns of %s do not name the model that measured them", s.SpeakerID)
	}
	for i, t := range s.Turns {
		if t.StartMS < 0 || t.EndMS <= t.StartMS || t.Speaker < 0 {
			return fmt.Errorf("turns of %s: turn %d (%d-%d ms, voice %d) is not a span with a voice", s.SpeakerID, i, t.StartMS, t.EndMS, t.Speaker)
		}
	}
	return nil
}

func isHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

// WriteSpeakerTurnSet writes a set atomically.
func WriteSpeakerTurnSet(path string, set SpeakerTurnSet) error {
	return writeJSON(path, set)
}

// extractSpeakerFloatsFn decodes one stream; tests replace it.
var extractSpeakerFloatsFn = ExtractSpeakerFloats

// DiarizeSpeaker diarizes one participant's own audio in a recording. A
// participant who rejoined has several streams; they are summed on the
// meeting timeline (each decode is already timeline-aligned), so the voices
// keep one numbering across the whole meeting.
func DiarizeSpeaker(ctx context.Context, mkvPath, speakerID string, model DiarizationModel) (SpeakerTurnSet, error) {
	streams, _, err := ProbeMKV(mkvPath)
	if err != nil {
		return SpeakerTurnSet{}, fmt.Errorf("probe recording: %w", err)
	}
	if d, err := AudioDurationMS(mkvPath); err == nil {
		setPCMCapacityDurationHints(streams, d)
	}
	var mix []float32
	var used []int
	for _, s := range streams {
		if s.SpeakerID != speakerID {
			continue
		}
		if err := ctx.Err(); err != nil {
			return SpeakerTurnSet{}, err
		}
		samples, err := extractSpeakerFloatsFn(mkvPath, s)
		if err != nil {
			return SpeakerTurnSet{}, err
		}
		used = append(used, s.Index)
		if mix == nil {
			// The usual case, one stream: diarize its own buffer. A copy would
			// hold the whole decoded track twice while the model runs, since
			// the native call allocates nothing the Go collector sees.
			mix = samples
			continue
		}
		if len(samples) > len(mix) {
			mix = append(mix, make([]float32, len(samples)-len(mix))...)
		}
		for i, v := range samples {
			mix[i] += v
		}
	}
	if len(used) == 0 {
		return SpeakerTurnSet{}, fmt.Errorf("%w: %s", ErrSpeakerNotFound, speakerID)
	}
	if err := ctx.Err(); err != nil {
		return SpeakerTurnSet{}, err
	}
	sourceSHA, err := fileSHA256(mkvPath)
	if err != nil {
		return SpeakerTurnSet{}, fmt.Errorf("hash recording: %w", err)
	}
	start := time.Now()
	turns, err := diarizeFn(model, mix, 16000)
	if err != nil {
		return SpeakerTurnSet{}, err
	}
	set := SpeakerTurnSet{
		Format:     SpeakerTurnsFormat,
		SpeakerID:  speakerID,
		Streams:    used,
		Source:     TurnSetSource{File: filepath.Base(mkvPath), SHA256: sourceSHA},
		Assignment: DiarizationAssignment,
		DurationMS: int64(len(mix)) * 1000 / 16000,
		Model:      TurnSetModel{Name: model.Name, SHA256: model.SHA256, Runtime: RuntimeVersion()},
		Params: TurnSetParams{
			MinDurationOn:  diarizationMinDurationOn,
			MinDurationOff: diarizationMinDurationOff,
			Threads:        diarizationThreads,
			Provider:       "cpu",
		},
		ElapsedMS: time.Since(start).Milliseconds(),
		Turns:     make([]SpeakerTurnEntry, len(turns)),
	}
	for i, t := range turns {
		set.Turns[i] = SpeakerTurnEntry{StartMS: t.StartMS, EndMS: t.EndMS, Speaker: t.Speaker}
	}
	return set, nil
}
