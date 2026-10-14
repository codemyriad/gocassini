package transcribe

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gocassini/internal/modelstore"
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

// The catalogue entry installs exactly the files the spec loads, laid out like
// v3 fp32, sharing v3's tokenizer files (Ultra keeps v3's tokenizer), and the
// shipped NOTICE credits both Moondream and NVIDIA.
func TestParakeetUltraCatalogueEntry(t *testing.T) {
	c := modelstore.Shipped()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	ultra, err := c.Model(string(ModelParakeetUltra), "")
	if err != nil {
		t.Fatal(err)
	}
	v3, err := c.Model(string(ModelParakeet06BV3), "")
	if err != nil {
		t.Fatal(err)
	}
	if ultra.Kind != modelstore.KindSpeech || ultra.License != "CC-BY-4.0" || ultra.VADRevision != v3.VADRevision {
		t.Fatalf("catalogue entry %+v", ultra)
	}
	// The revision is Cassini's export, as dist.gocassini.com publishes it:
	// the SHA-256 of the `sha256sum` listing of the six files sorted by name,
	// not Moondream's model.safetensors (c9608f36…, credited in the NOTICE).
	if ultra.Revision != "5030173982bf58c7975b4b26bd47cc4612f54c5c93846756f5c27cbff27f94f6" {
		t.Fatalf("revision %s is not the published Cassini export", ultra.Revision)
	}
	listing := map[string]string{}
	for _, f := range ultra.Files {
		a, err := c.Artifact(f.Artifact)
		if err != nil {
			t.Fatal(err)
		}
		listing[f.Path] = a.UncompressedSHA256
	}
	names := make([]string, 0, len(listing))
	for name := range listing {
		names = append(names, name)
	}
	sort.Strings(names)
	var sums strings.Builder
	for _, name := range names {
		fmt.Fprintf(&sums, "%s  %s\n", listing[name], name)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(sums.String()))); got != ultra.Revision {
		t.Fatalf("revision %s, but the files' sha256sum listing hashes to %s", ultra.Revision, got)
	}
	if !strings.Contains(modelstore.Notices, "c9608f36d0ab956c14bfcc525479b0746b3b42a56f6949ec85c14eb7466717dc") {
		t.Fatal("NOTICE lacks Moondream's model.safetensors sha256")
	}
	if len(ultra.Files) != len(v3.Files) {
		t.Fatalf("Ultra has %d files, v3 fp32 %d", len(ultra.Files), len(v3.Files))
	}
	for i, f := range ultra.Files {
		g := v3.Files[i]
		if f.Path != g.Path || f.Required != g.Required {
			t.Fatalf("file %d: Ultra %+v, v3 %+v", i, f, g)
		}
		shared := f.Path == "tokens.txt" || f.Path == "bpe.vocab"
		if shared != (f.Artifact == g.Artifact) {
			t.Fatalf("%s: Ultra artifact %s, v3 %s", f.Path, f.Artifact, g.Artifact)
		}
	}
	required := map[string]bool{}
	for _, f := range ultra.Files {
		if f.Required {
			required[f.Path] = true
		}
	}
	for _, name := range RequiredModelFileNames(ModelParakeetUltra) {
		if !required[name] {
			t.Fatalf("spec requires %s, the catalogue does not install it as required", name)
		}
	}
	for _, want := range []string{"https://huggingface.co/moondream/parakeet-ultra", "https://huggingface.co/nvidia/parakeet-tdt-0.6b-v3", "Post-trained by Moondream", "No endorsement by Moondream"} {
		if !strings.Contains(modelstore.Notices, want) {
			t.Fatalf("NOTICE lacks %q", want)
		}
	}
}
