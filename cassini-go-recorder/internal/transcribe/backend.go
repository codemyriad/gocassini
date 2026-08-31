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
//
// What the interface does NOT promise is where those bounds came from. A
// backend that measures each word's end against the audio can say so by also
// implementing AudioBoundedWordEnds below; one that does not stays here and
// publishes no such claim.
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

// AudioBoundedWordEnds is the optional guarantee a recognizer may declare on
// top of SpeechRecognizer, and the only way an artifact earns
// provenance.wordTimings.endsBoundedByAudio.
//
// SpeechRecognizer promises timed words and says nothing about where those
// times came from. A word's end can be its last token's timestamp — for
// Parakeet a trailing punctuation mark, stamped at the NEXT acoustic onset, so
// a sentence-final word runs seconds long over silence — or it can be measured
// against the speaker's own audio. Nothing in the timings tells the two apart,
// and consumers repair the first kind by clipping a long word back towards the
// meeting's median, which destroys the second kind. Only a decoder knows which
// rule produced its ends, so only a decoder can make the claim.
//
// Declaring it is opt-in, and that is the point. A backend registered tomorrow
// that returns the decoder's raw timestamps simply does not implement this
// interface; the manifest then omits provenance.wordTimings entirely and every
// consumer keeps its legacy repair, which is the correct behaviour for that
// backend. A default-on claim would have been silently wrong for it.
type AudioBoundedWordEnds interface {
	SpeechRecognizer
	// WordEndsAreBoundedByAudio reports whether every word this recognizer
	// returns from Transcribe had its end measured against the audio it was
	// decoded from, rather than left at its last token's timestamp.
	WordEndsAreBoundedByAudio() bool
}

// The bundled decoder is the one backend that makes the promise today. The
// assertion fails the build if the declaration is dropped, so losing the
// marker cannot be silent.
var _ AudioBoundedWordEnds = (*Recognizer)(nil)

// declaresAudioBoundedWordEnds reports whether rec makes the guarantee. Not
// implementing the interface is answered exactly like declaring false: no
// claim.
func declaresAudioBoundedWordEnds(rec SpeechRecognizer) bool {
	decl, ok := rec.(AudioBoundedWordEnds)
	return ok && decl.WordEndsAreBoundedByAudio()
}

// wordEndGuarantee accumulates that answer across every transcription pass of
// one build. The manifest record describes the whole artifact, so it survives
// only if EVERY recognizer that decoded for it declared the guarantee: the
// merged-mix fallback and each additional-model pass build their own
// recognizers, and a single pass that did not measure makes the record a lie.
// The zero value means nothing has been decoded yet, and claims nothing.
type wordEndGuarantee struct {
	mu       sync.Mutex
	observed bool
	allBound bool
}

// observe records what one recognizer declares. Called at construction, before
// it decodes anything, and safe to call from the parallel stream workers,
// which each build their own recognizer.
func (g *wordEndGuarantee) observe(rec SpeechRecognizer) {
	bounded := declaresAudioBoundedWordEnds(rec)
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.observed {
		g.observed, g.allBound = true, bounded
		return
	}
	g.allBound = g.allBound && bounded
}

// provenance returns the record the manifest may publish, or nil when this
// build did not earn it.
//
// nil is not a neutral default. An omitted provenance.wordTimings is precisely
// the signal a consumer keys off to run its own timing repair, so it is what
// every build that cannot prove its word ends were measured has to publish —
// including a build whose backend never told us either way.
func (g *wordEndGuarantee) provenance() *WordTimingProvenance {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.observed || !g.allBound {
		return nil
	}
	return &WordTimingProvenance{EndsBoundedByAudio: true}
}

// newRecognizerForPass builds a recognizer for one transcription pass and
// records its word-end guarantee in the same step, so a new construction site
// cannot pick up the recognizer while forgetting what it promises.
func newRecognizerForPass(id string, paths ModelPaths, vadModelPath, provider string, numThreads int, guarantee *wordEndGuarantee) (SpeechRecognizer, error) {
	rec, err := NewRecognizerForBackend(id, paths, vadModelPath, provider, numThreads)
	if err != nil {
		return nil, err
	}
	guarantee.observe(rec)
	return rec, nil
}

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
