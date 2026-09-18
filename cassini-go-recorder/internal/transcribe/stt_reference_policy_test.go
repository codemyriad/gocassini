package transcribe

import (
	"os"
	"strings"
	"testing"
)

func TestReferencePolicyPreservesUtterancesOnlyForV3(t *testing.T) {
	for _, id := range []ModelID{ModelParakeet06BV3, ModelParakeet06BV3Int8} {
		p := vadDecodePolicyForModel(id)
		if !p.preserveVADSpan || p.contextMS != 30 || p.tailPaddingMS != 0 || p.headPaddingMS != 0 || !p.disableSyntheticPadding {
			t.Fatalf("%s policy: %+v", id, p)
		}
		bounds := vadSegmentWindowBoundsWithPolicy(16000*24, 16000, p)
		if len(bounds) != 1 {
			t.Fatalf("%s split a VAD utterance: %v", id, bounds)
		}
	}
	for _, id := range []ModelID{"", "parakeet-tdt-0.6b-v2", "other"} {
		if got := vadDecodePolicyForModel(id); got != defaultVADDecodePolicy() {
			t.Fatalf("changed unrelated %s: %+v", id, got)
		}
	}
}

func TestReferenceRuntimeRequirement(t *testing.T) {
	for _, id := range []ModelID{ModelParakeet06BV3, ModelParakeet06BV3Int8} {
		if err := validateReferenceRuntime(id, "1.13.7"); err == nil || !strings.Contains(err.Error(), "build-cassini-bin.sh") {
			t.Fatalf("missing actionable rejection: %v", err)
		}
		if err := validateReferenceRuntime(id, "1.13.7"+parakeetReferenceRuntimeMarker); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateReferenceRuntime("other", "1.13.7"); err != nil {
		t.Fatal(err)
	}
}

func TestReferenceFrontendRetainsVocabularyHints(t *testing.T) {
	for _, id := range []ModelID{ModelParakeet06BV3, ModelParakeet06BV3Int8} {
		dir := t.TempDir()
		paths := transducerPaths(t)
		paths.ModelID = id
		vocabulary := decoderVocabulary{Terms: []string{"Librocco", "Chris"}, ParticipantTermCount: 1}
		decoder, provenance, err := resolveDecoderVocabulary(dir, vocabulary, paths)
		if err != nil {
			t.Fatal(err)
		}
		if decoder.Method != decodingModifiedBeamSearch || !decoder.Biased() || decoder.MaxActivePaths != hotwordsMaxActivePaths {
			t.Fatalf("lost vocabulary decoder: %+v", decoder)
		}
		if provenance == nil || !provenance.Applied || provenance.TermCount != 2 || provenance.ParticipantTermCount != 1 {
			t.Fatalf("incorrect hint provenance: %+v", provenance)
		}
		data, err := os.ReadFile(decoder.HotwordsFile)
		if err != nil || !strings.Contains(string(data), "Librocco") || !strings.Contains(string(data), "Chris :0.5") {
			t.Fatalf("missing scored vocabulary: %q, %v", data, err)
		}
		decoder, provenance, err = resolveDecoderVocabulary(dir, decoderVocabulary{}, paths)
		if err != nil || provenance != nil || decoder.Method != decodingModifiedBeamSearch {
			t.Fatalf("empty vocabulary changed decoder: %+v, %+v, %v", decoder, provenance, err)
		}
	}
}

// No native recognizer is needed: a short reference chunk must return before
// constructing a stream. This covers the former zero-frame native abort.
func TestReferenceSkipsChunksWithoutTwoFeatureFrames(t *testing.T) {
	for _, id := range []ModelID{ModelParakeet06BV3, ModelParakeet06BV3Int8} {
		policy := vadDecodePolicyForModel(id)
		for _, n := range []int{0, 1, 159, 160, 319} {
			for _, vadSegment := range []bool{false, true} {
				rec := &Recognizer{}
				words, err := rec.transcribeSegmentWithPolicy(make([]float32, n), 16000, 1000, vadSegment, policy)
				if err != nil || len(words) != 0 {
					t.Fatalf("model=%s samples=%d vad=%v: words=%v err=%v", id, n, vadSegment, words, err)
				}
			}
		}
	}
}

func TestReferenceINT8WithoutHintTokenizerUsesGreedy(t *testing.T) {
	paths := transducerPaths(t)
	paths.ModelID = ModelParakeet06BV3Int8
	paths.BpeVocabFile = ""
	for _, terms := range [][]string{nil, {"Librocco"}} {
		decoder, provenance, err := resolveDecoder(t.TempDir(), terms, paths)
		if err != nil || decoder.Method != decodingGreedySearch || decoder.Biased() || decoder.MaxActivePaths != 0 {
			t.Fatalf("unsupported INT8 hint bundle: %+v, %v", decoder, err)
		}
		if len(terms) == 0 {
			if provenance != nil {
				t.Fatalf("unexpected empty-hint provenance: %+v", provenance)
			}
		} else if provenance == nil || provenance.Applied || provenance.DecodingMethod != decodingGreedySearch || !strings.Contains(provenance.Reason, "bpe.vocab") {
			t.Fatalf("missing unsupported-hint explanation: %+v", provenance)
		}
	}
}
