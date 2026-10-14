package transcribe

import (
	"reflect"
	"strings"
	"testing"
)

const (
	cassini6RuntimeVersion = "1.13.7+cassini-parakeet-v3-reference-v1.nemotron-diarization-v2"
	stockRuntimeVersion    = "1.13.7"
)

// Parakeet Ultra is v3 post-trained: same files, tokenizer and features. It must
// get exactly v3 fp32's treatment from every gate, which used to key on the v3
// ids and gave any other id the default policy.
func TestParakeetUltraSharesV3Fp32Spec(t *testing.T) {
	ultra, v3 := knownModels[ModelParakeetUltra], knownModels[ModelParakeet06BV3]
	if ultra.URL != "" {
		t.Fatalf("Ultra has an archive URL %q; it installs from dist.gocassini.com only", ultra.URL)
	}
	if !reflect.DeepEqual(RequiredModelFileNames(ModelParakeetUltra), RequiredModelFileNames(ModelParakeet06BV3)) {
		t.Fatalf("Ultra files %v, v3 fp32 files %v", RequiredModelFileNames(ModelParakeetUltra), RequiredModelFileNames(ModelParakeet06BV3))
	}
	if ultra.BpeVocabFile != v3.BpeVocabFile || ultra.ModelType != v3.ModelType || ultra.SampleRate != v3.SampleRate || ultra.FeatureDim != v3.FeatureDim {
		t.Fatalf("Ultra spec %+v differs from v3 fp32 %+v", ultra, v3)
	}
	if !usesParakeetV3ReferencePolicy(ModelParakeetUltra) {
		t.Fatal("Ultra does not get the Parakeet reference boundary policy")
	}
	if vadDecodePolicyForModel(ModelParakeetUltra) != vadDecodePolicyForModel(ModelParakeet06BV3) {
		t.Fatal("Ultra's boundary policy differs from v3's")
	}
	if !SupportsCUDA(ModelParakeetUltra) || !SupportsCUDA(ModelParakeet06BV3) {
		t.Fatal("both fp32 models must install and probe on CUDA")
	}
	for _, id := range []ModelID{ModelParakeet06BV3Int8, ModelParakeet110M, ModelParakeet06B, "unknown"} {
		if SupportsCUDA(id) {
			t.Fatalf("%s must not run on CUDA", id)
		}
	}
	for _, device := range []string{"cpu", "cuda"} {
		if got, want := probeMemoryFloor(ModelParakeetUltra, device), probeMemoryFloor(ModelParakeet06BV3, device); got != want {
			t.Fatalf("%s probe floor: Ultra %d, v3 fp32 %d", device, got, want)
		}
	}
	// The table moved into the spec; the values must not move with it.
	for id, want := range map[ModelID]int{ModelParakeet110M: 1536, ModelParakeet06BV3Int8: 2816, ModelParakeet06BV3: 4608, "nemotron-3-diarization-int8": 1024, "nemotron-3-diarization": 1536, "unknown": 4608} {
		if got := probeMemoryFloor(id, "cpu"); got != want {
			t.Fatalf("probeMemoryFloor(%s, cpu) = %d, want %d", id, got, want)
		}
	}
}

// v3 keeps its marker; Ultra needs the Ultra selection too. A runtime without
// it would run an honest-URL Ultra export on the legacy frontend (or exit while
// loading it), so Ultra is refused before sherpa sees it, and v3 is not.
func TestParakeetUltraNeedsUltraRuntimeMarker(t *testing.T) {
	cases := []struct {
		id      ModelID
		version string
		isRef   bool
		refuse  bool
	}{
		{ModelParakeet06BV3, cassini6RuntimeVersion, true, false},
		{ModelParakeet06BV3, cassiniRuntimeVersion, true, false},
		{ModelParakeet06BV3, stockRuntimeVersion, false, false},
		{ModelParakeetUltra, cassiniRuntimeVersion, true, false},
		{ModelParakeetUltra, cassini6RuntimeVersion, false, true},
		{ModelParakeetUltra, stockRuntimeVersion, false, true},
		{ModelParakeet110M, stockRuntimeVersion, false, false},
	}
	for _, c := range cases {
		isRef, warn := checkReferenceRuntime(c.id, c.version)
		if isRef != c.isRef {
			t.Errorf("%s on %q: reference=%v, want %v", c.id, c.version, isRef, c.isRef)
		}
		if usesParakeetV3ReferencePolicy(c.id) && !isRef && !strings.Contains(warn, string(c.id)) {
			t.Errorf("%s on %q: warning %q does not name the model", c.id, c.version, warn)
		}
		policy, gotRef, _ := effectiveVADPolicy(c.id, c.version, true)
		if gotRef != c.isRef || (c.isRef && policy != vadDecodePolicyForModel(c.id)) {
			t.Errorf("%s on %q: effective policy ref=%v %+v", c.id, c.version, gotRef, policy)
		}
		err := refuseWithoutReferenceRuntime(c.id, c.version)
		if (err != nil) != c.refuse {
			t.Errorf("%s on %q: refuse=%v (%v), want %v", c.id, c.version, err != nil, err, c.refuse)
		}
		if err != nil && !strings.Contains(err.Error(), parakeetUltraRuntimeMarker) {
			t.Errorf("refusal %q does not name the missing marker", err)
		}
	}
}

// Ultra ships bpe.vocab like v3 fp32, so it beam-searches with hints exactly as
// v3 fp32 does, and a vocabulary-less Ultra bundle behaves like a
// vocabulary-less v3 fp32 one. Only the v3 int8 archive keeps greedy for lack
// of bpe.vocab.
func TestParakeetUltraDecoderMatchesV3Fp32(t *testing.T) {
	t.Setenv(envHintsDisabled, "")
	for _, withVocab := range []bool{true, false} {
		methods := map[ModelID]string{}
		for _, id := range []ModelID{ModelParakeet06BV3, ModelParakeetUltra, ModelParakeet06BV3Int8} {
			paths := transducerPaths(t)
			paths.ModelID = id
			if !withVocab {
				paths.BpeVocabFile = ""
			}
			dec, _, err := resolveDecoderVocabulary(t.TempDir(), decoderVocabulary{}, paths)
			if err != nil {
				t.Fatalf("%s vocab=%v: %v", id, withVocab, err)
			}
			methods[id] = dec.Method
		}
		if methods[ModelParakeetUltra] != methods[ModelParakeet06BV3] {
			t.Fatalf("vocab=%v: Ultra decodes with %s, v3 fp32 with %s", withVocab, methods[ModelParakeetUltra], methods[ModelParakeet06BV3])
		}
		if withVocab && methods[ModelParakeetUltra] != decodingModifiedBeamSearch {
			t.Fatalf("Ultra with bpe.vocab decodes with %s", methods[ModelParakeetUltra])
		}
		if !withVocab && methods[ModelParakeet06BV3Int8] != decodingGreedySearch {
			t.Fatalf("v3 int8 without bpe.vocab decodes with %s", methods[ModelParakeet06BV3Int8])
		}
	}
}
