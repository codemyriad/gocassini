package transcribe

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

// SpeechRecognizer is the seam a speech decoder plugs into.
//
// Everything downstream of this interface — VAD-driven segmentation, segment
// assembly, attribution, artifact writing — is decoder-agnostic and works from
// timed words alone. Keeping that explicit is what lets a second engine be
// added without touching the pipeline: a backend has to produce words with
// millisecond bounds on the recording timeline, and nothing else.
//
// The bounds are not optional. Per-word timing is what every later stage keys
// off, including speaker attribution (see attribution.go), so a model that can
// only return a flat transcript cannot be plugged in here without a forced
// aligner in front of it.
type SpeechRecognizer interface {
	// Transcribe returns timed words for 16 kHz mono float32 samples in
	// [-1,1]. Timestamps are relative to the start of samples.
	Transcribe(samples []float32, sampleRate int, useVAD bool) ([]Word, error)
	// Close releases any native resources held by the backend.
	Close()
}

// The bundled sherpa-onnx recognizer is the reference implementation of the
// seam; this assertion fails the build if it ever drifts out of shape.
var _ SpeechRecognizer = (*Recognizer)(nil)

// RecognizerFactory constructs a recognizer for an already-resolved model and
// device. Resolution (quality tier -> model, auto -> cpu/cuda) stays in
// policy.go so every backend inherits the same host-adaptive behaviour.
type RecognizerFactory func(paths ModelPaths, vadModelPath, provider string, numThreads int) (SpeechRecognizer, error)

// SherpaOnnxBackend is the id of the bundled in-process decoder.
const SherpaOnnxBackend = "sherpa-onnx"

var (
	backendMu       sync.RWMutex
	backendRegistry = map[string]RecognizerFactory{
		SherpaOnnxBackend: func(paths ModelPaths, vadModelPath, provider string, numThreads int) (SpeechRecognizer, error) {
			return NewRecognizer(paths, vadModelPath, provider, numThreads)
		},
	}
)

// RegisterRecognizerBackend adds a decoder under the given id. It is safe to
// call from an init function. Re-registering an id replaces it, which is how a
// test double is installed.
func RegisterRecognizerBackend(id string, factory RecognizerFactory) error {
	id = strings.TrimSpace(strings.ToLower(id))
	if id == "" {
		return fmt.Errorf("recognizer backend id must not be empty")
	}
	if factory == nil {
		return fmt.Errorf("recognizer backend %q has no factory", id)
	}
	backendMu.Lock()
	defer backendMu.Unlock()
	backendRegistry[id] = factory
	return nil
}

// RecognizerBackends lists the registered ids, sorted, for diagnostics and for
// the error message on an unknown selection.
func RecognizerBackends() []string {
	backendMu.RLock()
	defer backendMu.RUnlock()
	ids := make([]string, 0, len(backendRegistry))
	for id := range backendRegistry {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ResolveRecognizerBackend returns the backend id to use: an explicit value
// wins, otherwise CASSINI_STT_BACKEND, otherwise the bundled decoder.
func ResolveRecognizerBackend(explicit string) string {
	if id := strings.TrimSpace(strings.ToLower(explicit)); id != "" {
		return id
	}
	if id := strings.TrimSpace(strings.ToLower(os.Getenv("CASSINI_STT_BACKEND"))); id != "" {
		return id
	}
	return SherpaOnnxBackend
}

// LookupRecognizerBackend resolves the backend id (explicit value, then
// CASSINI_STT_BACKEND, then the bundled decoder) and confirms it is actually
// registered. It is the cheap front-door check: BuildMeetingArtifact calls it
// before probing, mixing, hashing or downloading anything, so a misconfigured
// backend fails in milliseconds instead of after minutes of decode and
// download work that would be repeated on every retry.
func LookupRecognizerBackend(id string) (string, error) {
	id = ResolveRecognizerBackend(id)
	backendMu.RLock()
	_, ok := backendRegistry[id]
	backendMu.RUnlock()
	if !ok {
		return "", errUnknownBackend(id)
	}
	return id, nil
}

// errUnknownBackend is the loud refusal shared by every lookup path: silently
// falling back to a different engine than the operator asked for would make
// the resulting artifact's provenance a lie.
func errUnknownBackend(id string) error {
	return fmt.Errorf("unknown STT backend %q (available: %s)",
		id, strings.Join(RecognizerBackends(), ", "))
}

// NewRecognizerForBackend builds a recognizer from the named backend. An
// unknown id is an error naming what is available rather than a silent
// fallback.
func NewRecognizerForBackend(id string, paths ModelPaths, vadModelPath, provider string, numThreads int) (SpeechRecognizer, error) {
	id = ResolveRecognizerBackend(id)
	backendMu.RLock()
	factory, ok := backendRegistry[id]
	backendMu.RUnlock()
	if !ok {
		return nil, errUnknownBackend(id)
	}
	rec, err := factory(paths, vadModelPath, provider, numThreads)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, fmt.Errorf("STT backend %q returned no recognizer", id)
	}
	return rec, nil
}
