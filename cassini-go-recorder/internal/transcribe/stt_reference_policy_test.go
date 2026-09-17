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

func TestReferenceDecoderReportsUnappliedVocabulary(t *testing.T) {
	for _, id := range []ModelID{ModelParakeet06BV3, ModelParakeet06BV3Int8} {
		dir := t.TempDir()
		paths := transducerPaths(t)
		paths.ModelID = id
		vocabulary := decoderVocabulary{Terms: []string{"Librocco", "Chris"}, ParticipantTermCount: 1}
		decoder, provenance, err := resolveDecoderVocabulary(dir, vocabulary, paths)
		if err != nil {
			t.Fatal(err)
		}
		if decoder.Method != decodingGreedySearch || decoder.Biased() || decoder.MaxActivePaths != 0 {
			t.Fatalf("unexpected decoder %+v", decoder)
		}
		if provenance == nil || provenance.Applied || provenance.TermCount != 2 || provenance.ParticipantTermCount != 1 || provenance.DecodingMethod != decodingGreedySearch || !strings.Contains(provenance.Reason, "not applied") {
			t.Fatalf("misleading provenance: %+v", provenance)
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("wrote unused hints: %v, %v", entries, err)
		}
		_, provenance, err = resolveDecoderVocabulary(dir, decoderVocabulary{}, paths)
		if err != nil || provenance != nil {
			t.Fatalf("empty vocabulary: %+v, %v", provenance, err)
		}
	}
}
