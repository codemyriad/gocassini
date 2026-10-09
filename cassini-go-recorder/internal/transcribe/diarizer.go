package transcribe

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
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
)

// DiarizationThreadsEnv sets how many CPU threads the diarizer uses. The
// operator sets it from its own thread budget; it is 2 when unset, as on
// Android, and always between 1 and 16. The threads change only how fast the
// turns come, not which turns: each turn set records the number it ran with.
const DiarizationThreadsEnv = "CASSINI_DIARIZATION_THREADS"

const (
	DefaultDiarizationThreads = 2
	maxDiarizationThreads     = 16
)

// DiarizationThreads reads $CASSINI_DIARIZATION_THREADS: the default when it
// is unset, clamped to 1..16 when it is an integer. A value that is not an
// integer gives the default and an error saying so, for the caller to warn.
func DiarizationThreads() (int, error) {
	raw := strings.TrimSpace(os.Getenv(DiarizationThreadsEnv))
	if raw == "" {
		return DefaultDiarizationThreads, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return DefaultDiarizationThreads, fmt.Errorf("%s=%q is not an integer; using %d threads", DiarizationThreadsEnv, raw, DefaultDiarizationThreads)
	}
	return min(max(n, 1), maxDiarizationThreads), nil
}

// DiarizationModelEnv points at a decompressed Nemotron v2 ONNX file. It is a
// development override: installed systems use the model store.
const DiarizationModelEnv = "CASSINI_DIARIZATION_MODEL"

// DefaultDiarizationModelID is the catalogue diarizer Settings offers and
// Cassini for Android runs. The fp32 export is used only when it is the one
// installed.
const DefaultDiarizationModelID = "nemotron-3-diarization-int8"

var diarizationModelIDs = []string{DefaultDiarizationModelID, "nemotron-3-diarization"}

// DiarizationInstallHint says how an administrator gets the model. It ends
// every "not installed" error, and the operator shows it as is.
const DiarizationInstallHint = "an administrator can download it in Cassini's Settings (Voice separation), or run `cassini models install " + DefaultDiarizationModelID + "` (offline: `cassini models import`)"

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
// else the installed catalogue diarizer under the model store rooted at
// cacheDir (the int8 export first).
func ResolveDiarizationModel(cacheDir string) (DiarizationModel, error) {
	if !HasDiarizationRuntime() {
		return DiarizationModel{}, fmt.Errorf("%w: native runtime %q has no Nemotron support", ErrDiarizationUnavailable, sherpa.GetVersion())
	}
	if path := strings.TrimSpace(os.Getenv(DiarizationModelEnv)); path != "" {
		return LoadDiarizationModel(path)
	}
	if cacheDir == "" {
		return DiarizationModel{}, fmt.Errorf("%w: no model store; set %s or CASSINI_CACHE_ROOT", ErrDiarizationUnavailable, DiarizationModelEnv)
	}
	var first error
	for _, id := range diarizationModelIDs {
		model, err := InstalledDiarizer(cacheDir, id, "")
		if err == nil {
			return model, nil
		}
		if first == nil {
			first = err
		}
	}
	return DiarizationModel{}, first
}

// LoadDiarizationModel describes the Nemotron model at an explicit path, with
// the same runtime and file checks as ResolveDiarizationModel.
func LoadDiarizationModel(path string) (DiarizationModel, error) {
	if !HasDiarizationRuntime() {
		return DiarizationModel{}, fmt.Errorf("%w: native runtime %q has no Nemotron support", ErrDiarizationUnavailable, sherpa.GetVersion())
	}
	if _, err := os.Stat(path); err != nil {
		return DiarizationModel{}, fmt.Errorf("%w: %v", ErrDiarizationUnavailable, err)
	}
	sum, err := fileSHA256(path)
	if err != nil {
		return DiarizationModel{}, fmt.Errorf("%w: hash model: %v", ErrDiarizationUnavailable, err)
	}
	return DiarizationModel{Path: path, Name: diarizationModelName(path), SHA256: sum}, nil
}

func diarizationModelName(path string) string {
	name := "nvidia/Nemotron-3-Diarization"
	if strings.Contains(filepath.Base(path), "int8") {
		name += " INT8"
	}
	return name
}

// diarizeFn runs the diarizer with that many CPU threads over 16 kHz mono PCM
// on the meeting timeline and returns normalised turns. Tests replace it so no
// model is needed.
var diarizeFn = diarizeWithSherpa

func diarizeWithSherpa(model DiarizationModel, samples []float32, sampleRate, threads int) ([]SpeakerTurn, error) {
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
	cfg.Segmentation.NumThreads = max(threads, 1)
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
