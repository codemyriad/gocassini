package transcribe

import (
	"strings"
	"testing"
)

type stubRecognizer struct {
	words  []Word
	closed bool
}

func (s *stubRecognizer) Transcribe(samples []float32, sampleRate int, useVAD bool) ([]Word, error) {
	return s.words, nil
}

func (s *stubRecognizer) Close() { s.closed = true }

func TestSherpaBackendIsRegisteredByDefault(t *testing.T) {
	ids := RecognizerBackends()
	var found bool
	for _, id := range ids {
		if id == SherpaOnnxBackend {
			found = true
		}
	}
	if !found {
		t.Fatalf("bundled decoder missing from %v", ids)
	}
}

func TestResolveRecognizerBackendPrefersExplicitThenEnv(t *testing.T) {
	t.Setenv("CASSINI_STT_BACKEND", "from-env")
	if got := ResolveRecognizerBackend("Explicit"); got != "explicit" {
		t.Errorf("explicit selection should win and normalise, got %q", got)
	}
	if got := ResolveRecognizerBackend(""); got != "from-env" {
		t.Errorf("env should be used when nothing is explicit, got %q", got)
	}
	t.Setenv("CASSINI_STT_BACKEND", "")
	if got := ResolveRecognizerBackend(""); got != SherpaOnnxBackend {
		t.Errorf("expected the bundled decoder as the default, got %q", got)
	}
}

// A second engine must be addable without touching the pipeline: registering a
// factory is the whole integration surface.
func TestRegisteredBackendIsUsedForConstruction(t *testing.T) {
	stub := &stubRecognizer{words: []Word{{Text: "hello", StartMS: 0, EndMS: 100}}}
	if err := RegisterRecognizerBackend("test-stub", func(ModelPaths, string, string, int, *DecoderConfig) (SpeechRecognizer, error) {
		return stub, nil
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() {
		backendMu.Lock()
		delete(backendRegistry, "test-stub")
		backendMu.Unlock()
	})

	rec, err := NewRecognizerForBackend("test-stub", ModelPaths{}, "", "cpu", 1, nil)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	words, err := rec.Transcribe(nil, 16000, true)
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if len(words) != 1 || words[0].Text != "hello" {
		t.Errorf("registered backend was not used: %+v", words)
	}
	rec.Close()
	if !stub.closed {
		t.Error("Close must reach the backend so native resources are released")
	}
}

// Silently falling back to a different engine would make the artifact's
// provenance a lie, so an unknown id has to fail loudly.
func TestUnknownBackendIsAnErrorNamingWhatExists(t *testing.T) {
	_, err := NewRecognizerForBackend("does-not-exist", ModelPaths{}, "", "cpu", 1, nil)
	if err == nil {
		t.Fatal("expected an error for an unknown backend")
	}
	if !strings.Contains(err.Error(), SherpaOnnxBackend) {
		t.Errorf("error should list what is available, got %q", err)
	}
}

func TestRegisterRejectsIncompleteBackends(t *testing.T) {
	if err := RegisterRecognizerBackend("", func(ModelPaths, string, string, int, *DecoderConfig) (SpeechRecognizer, error) {
		return &stubRecognizer{}, nil
	}); err == nil {
		t.Error("an empty id must be rejected")
	}
	if err := RegisterRecognizerBackend("no-factory", nil); err == nil {
		t.Error("a nil factory must be rejected")
	}
}

func TestBackendReturningNilRecognizerIsAnError(t *testing.T) {
	if err := RegisterRecognizerBackend("nil-rec", func(ModelPaths, string, string, int, *DecoderConfig) (SpeechRecognizer, error) {
		return nil, nil
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() {
		backendMu.Lock()
		delete(backendRegistry, "nil-rec")
		backendMu.Unlock()
	})
	if _, err := NewRecognizerForBackend("nil-rec", ModelPaths{}, "", "cpu", 1, nil); err == nil {
		t.Error("a backend that returns no recognizer must be an error, not a nil deref later")
	}
}

// declaringRecognizer is a backend that makes the audio-bounded word-end
// promise; stubRecognizer above makes none, which is what every engine that
// has not thought about the question looks like.
type declaringRecognizer struct{ stubRecognizer }

func (d *declaringRecognizer) WordEndsAreBoundedByAudio() bool { return true }

// The bundled decoder is the reference implementation of the guarantee, and
// the manifest marker exists for it. Constructing a real Recognizer needs a
// sherpa model on disk, so this asserts the declaration itself — the only part
// of it the pipeline can read.
func TestBundledDecoderDeclaresAudioBoundedWordEnds(t *testing.T) {
	var rec SpeechRecognizer = &Recognizer{}
	if !declaresAudioBoundedWordEnds(rec) {
		t.Error("the bundled sherpa-onnx recognizer no longer declares AudioBoundedWordEnds, so every artifact it builds silently loses provenance.wordTimings")
	}
}

// A backend registered through the public registry promises timed words and
// nothing else. It must not pick up the audio-bounded guarantee by being
// registered: the viewer skips its own timing repair on that claim, so
// inheriting it would corrupt the timings of every meeting the new engine
// decodes.
func TestARegisteredBackendDoesNotInheritTheAudioBoundedClaim(t *testing.T) {
	if declaresAudioBoundedWordEnds(&stubRecognizer{}) {
		t.Error("a plain SpeechRecognizer claims audio-bounded word ends without implementing the capability")
	}
	if !declaresAudioBoundedWordEnds(&declaringRecognizer{}) {
		t.Error("a backend that explicitly declares the guarantee was not believed")
	}
}

// The manifest record covers every word in the artifact, and a build can run
// several passes (the merged-mix fallback, one per additional model). One pass
// that did not measure is enough to make the record a lie, so the claim is an
// AND across passes — and a build that decoded nothing claims nothing.
func TestWordEndGuaranteeNeedsEveryPassToDeclareIt(t *testing.T) {
	var nothingDecoded wordEndGuarantee
	if got := nothingDecoded.provenance(); got != nil {
		t.Errorf("a build with no decode claims %+v; want no record", got)
	}

	var allDeclared wordEndGuarantee
	allDeclared.observe(&declaringRecognizer{})
	allDeclared.observe(&declaringRecognizer{})
	got := allDeclared.provenance()
	if got == nil || !got.EndsBoundedByAudio {
		t.Errorf("every pass declared the guarantee, got %+v", got)
	}

	var mixed wordEndGuarantee
	mixed.observe(&declaringRecognizer{})
	mixed.observe(&stubRecognizer{})
	if got := mixed.provenance(); got != nil {
		t.Errorf("one pass that measured nothing still yielded %+v; want no record", got)
	}

	// Order must not decide it: the merged-mix fallback runs after the
	// participant pass, and an additional model after both.
	var mixedReversed wordEndGuarantee
	mixedReversed.observe(&stubRecognizer{})
	mixedReversed.observe(&declaringRecognizer{})
	if got := mixedReversed.provenance(); got != nil {
		t.Errorf("a later declaring pass reinstated the claim: %+v", got)
	}
}
