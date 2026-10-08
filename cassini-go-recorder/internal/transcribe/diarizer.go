package transcribe

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

// nemotronDiarizationRuntimeMarker is in the native version string of a
// sherpa-onnx build that runs the Nemotron v2 ONNX interface (PR #359). Stock
// libraries (macOS, Windows, Linux arm32) lack it and cannot diarize.
const nemotronDiarizationRuntimeMarker = ".nemotron-diarization-v2"

// Settings shared with cassini-android (Diarization.kt). Threshold 0.5 is the
// model's own activity threshold; the Go wrapper exposes no knob for it.
const (
	diarizationMinDurationOn  = 0.3
	diarizationMinDurationOff = 0.5
	diarizationThreads        = 2
)

// DiarizationModelEnv points at a decompressed Nemotron v2 ONNX file. This is
// the proof-of-concept seam until the model is a catalogue entry.
const DiarizationModelEnv = "CASSINI_DIARIZATION_MODEL"

// ErrDiarizationUnavailable means the stage cannot run here: no model, or a
// native runtime without Nemotron. Callers skip the split; they never fail a
// build for it.
var ErrDiarizationUnavailable = errors.New("speaker diarization is not available")

// DiarizationModel describes the model a split used, for provenance.
type DiarizationModel struct {
	Path   string
	Name   string
	SHA256 string
}

// HasDiarizationRuntime reports whether the linked sherpa-onnx can run Nemotron.
func HasDiarizationRuntime() bool {
	return strings.Contains(sherpa.GetVersion(), nemotronDiarizationRuntimeMarker)
}

// ResolveDiarizationModel finds the Nemotron model: $CASSINI_DIARIZATION_MODEL,
// else the int8 file under <cacheDir>/models/nemotron-3-diarization-int8/.
func ResolveDiarizationModel(cacheDir string) (DiarizationModel, error) {
	if !HasDiarizationRuntime() {
		return DiarizationModel{}, fmt.Errorf("%w: native runtime %q has no Nemotron support", ErrDiarizationUnavailable, sherpa.GetVersion())
	}
	path := strings.TrimSpace(os.Getenv(DiarizationModelEnv))
	if path == "" && cacheDir != "" {
		path = filepath.Join(cacheDir, "models", "nemotron-3-diarization-int8", "model.int8.onnx")
	}
	if path == "" {
		return DiarizationModel{}, fmt.Errorf("%w: set %s", ErrDiarizationUnavailable, DiarizationModelEnv)
	}
	if _, err := os.Stat(path); err != nil {
		return DiarizationModel{}, fmt.Errorf("%w: %v", ErrDiarizationUnavailable, err)
	}
	sum, err := fileSHA256(path)
	if err != nil {
		return DiarizationModel{}, fmt.Errorf("%w: hash model: %v", ErrDiarizationUnavailable, err)
	}
	name := "nvidia/Nemotron-3-Diarization"
	if strings.Contains(filepath.Base(path), "int8") {
		name += " INT8"
	}
	return DiarizationModel{Path: path, Name: name, SHA256: sum}, nil
}

// diarizeFn runs the diarizer over 16 kHz mono PCM on the meeting timeline and
// returns normalised turns. Tests replace it so no model is needed.
var diarizeFn = diarizeWithSherpa

func diarizeWithSherpa(model DiarizationModel, samples []float32, sampleRate int) ([]SpeakerTurn, error) {
	if sampleRate != 16000 {
		return nil, fmt.Errorf("diarization needs 16 kHz audio, got %d Hz", sampleRate)
	}
	durationMS := int64(len(samples)) * 1000 / int64(sampleRate)
	// Process indexes samples[0]; anything shorter than one Sortformer frame
	// has no speech to find anyway.
	if len(samples) < sampleRate/5 {
		return nil, nil
	}
	cfg := sherpa.OfflineSpeakerDiarizationConfig{
		MinDurationOn:  diarizationMinDurationOn,
		MinDurationOff: diarizationMinDurationOff,
	}
	cfg.Segmentation.Pyannote.Model = model.Path
	cfg.Segmentation.NumThreads = diarizationThreads
	// CUDA is untested for Nemotron; keep it on the CPU like the VAD.
	cfg.Segmentation.Provider = "cpu"
	sd := sherpa.NewOfflineSpeakerDiarization(&cfg)
	if sd == nil {
		return nil, fmt.Errorf("failed to create speaker diarization (model %s)", model.Path)
	}
	defer sherpa.DeleteOfflineSpeakerDiarization(sd)
	if got := sd.SampleRate(); got != sampleRate {
		return nil, fmt.Errorf("diarization model expects %d Hz, got %d Hz", got, sampleRate)
	}
	segs := sd.Process(samples)
	raw := make([]rawTurn, len(segs))
	for i, s := range segs {
		raw[i] = rawTurn{Start: float64(s.Start), End: float64(s.End), Speaker: s.Speaker}
	}
	return normalizeTurns(raw, durationMS), nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
