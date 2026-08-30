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
	if err := RegisterRecognizerBackend("test-stub", func(ModelPaths, string, string, int) (SpeechRecognizer, error) {
		return stub, nil
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() {
		backendMu.Lock()
		delete(backendRegistry, "test-stub")
		backendMu.Unlock()
	})

	rec, err := NewRecognizerForBackend("test-stub", ModelPaths{}, "", "cpu", 1)
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
	_, err := NewRecognizerForBackend("does-not-exist", ModelPaths{}, "", "cpu", 1)
	if err == nil {
		t.Fatal("expected an error for an unknown backend")
	}
	if !strings.Contains(err.Error(), SherpaOnnxBackend) {
		t.Errorf("error should list what is available, got %q", err)
	}
}

func TestRegisterRejectsIncompleteBackends(t *testing.T) {
	if err := RegisterRecognizerBackend("", func(ModelPaths, string, string, int) (SpeechRecognizer, error) {
		return &stubRecognizer{}, nil
	}); err == nil {
		t.Error("an empty id must be rejected")
	}
	if err := RegisterRecognizerBackend("no-factory", nil); err == nil {
		t.Error("a nil factory must be rejected")
	}
}

func TestBackendReturningNilRecognizerIsAnError(t *testing.T) {
	if err := RegisterRecognizerBackend("nil-rec", func(ModelPaths, string, string, int) (SpeechRecognizer, error) {
		return nil, nil
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() {
		backendMu.Lock()
		delete(backendRegistry, "nil-rec")
		backendMu.Unlock()
	})
	if _, err := NewRecognizerForBackend("nil-rec", ModelPaths{}, "", "cpu", 1); err == nil {
		t.Error("a backend that returns no recognizer must be an error, not a nil deref later")
	}
}
