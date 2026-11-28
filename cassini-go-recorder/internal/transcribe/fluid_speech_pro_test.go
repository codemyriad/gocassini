package transcribe

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gocassini/internal/modelstore"
)

const fluidSpeechProInt8Revision = "366116b549382a16e31cfa571c2e7e442814e950541187b9ab9ab3e6236b4f5b"

// Fluid Speech Pro is Parakeet Ultra fine-tuned: same tokenizer and features,
// so it runs as Ultra int8 v2 did (boundary policy, greedy search without
// bpe.vocab, the same memory floor). It is the balanced CPU tier and the
// default (D-1092); CUDA stays on Ultra fp32. Its encoder url names
// parakeet-tdt, so unlike the Ultra exports it loads on any 2.0.1 runtime.
func TestFluidSpeechProSpecs(t *testing.T) {
	id := ModelFluidSpeechProInt8
	spec := knownModels[id]
	if spec.Retired || spec.URL != "" || spec.ModelType != "nemo_transducer" || spec.SampleRate != 16000 || spec.FeatureDim != 128 {
		t.Fatalf("%s spec %+v", id, spec)
	}
	if DefaultModelID() != id || ModelForQuality(QualityBalanced, "cpu") != id || ModelForQuality("", "cpu") != id {
		t.Fatalf("default %s, balanced CPU %s", DefaultModelID(), ModelForQuality(QualityBalanced, "cpu"))
	}
	for _, q := range []STTQuality{QualityFast, QualityBalanced, QualityBest} {
		if ModelForQuality(q, "cuda") != ModelParakeetUltra {
			t.Fatalf("%s on CUDA selects %s", q, ModelForQuality(q, "cuda"))
		}
	}
	if ModelForQuality(QualityBest, "cpu") != ModelParakeetUltra || ModelForQuality(QualityFast, "cpu") != ModelParakeet110M {
		t.Fatal("Fluid Speech Pro changed a tier other than balanced on CPU")
	}
	if SupportsCUDA(id) {
		t.Fatalf("%s must not run on CUDA", id)
	}
	if len(spec.RuntimeMarkers) != 0 {
		t.Fatalf("%s markers %v", id, spec.RuntimeMarkers)
	}
	for _, version := range []string{cassiniRuntimeVersion, stockRuntimeVersion} {
		if err := CheckNativeRuntime(id, version); err != nil {
			t.Fatalf("%s on %q: %v", id, version, err)
		}
	}
	if CheckNativeRuntime(id, legacyCassiniRuntimeVersion) == nil {
		t.Fatalf("%s loads on a runtime older than the 2.0.1 C API", id)
	}
	if !usesParakeetBoundaryPolicy(id) || vadDecodePolicyForModel(id) != vadDecodePolicyForModel(ModelParakeetUltraInt8) {
		t.Fatalf("%s does not get the boundary policy Ultra int8 v2 ran with", id)
	}
	if !reflect.DeepEqual(RequiredModelFileNames(id), RequiredModelFileNames(ModelParakeetUltraInt8)) ||
		!reflect.DeepEqual(RequiredModelFileNames(id), []string{"encoder.int8.onnx", "decoder.int8.onnx", "joiner.int8.onnx", "tokens.txt"}) {
		t.Fatalf("%s files %v", id, RequiredModelFileNames(id))
	}
	if got := probeMemoryFloor(id, "cpu"); got != 2816 || got != probeMemoryFloor(ModelParakeetUltraInt8, "cpu") {
		t.Fatalf("probeMemoryFloor(%s) = %d, want Ultra int8 v2's 2816", id, got)
	}
}

// Published without bpe.vocab, it decodes greedily, with hints on or off; a
// bpe.vocab that ends up in its directory cannot switch it to beam search.
func TestFluidSpeechProDecodesGreedily(t *testing.T) {
	dir := t.TempDir()
	for _, name := range append(RequiredModelFileNames(ModelFluidSpeechProInt8), "bpe.vocab") {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	paths := resolveModelPaths(dir, knownModels[ModelFluidSpeechProInt8], ModelFluidSpeechProInt8)
	if paths.BpeVocabFile != "" {
		t.Fatalf("Fluid Speech Pro resolved a vocabulary: %s", paths.BpeVocabFile)
	}
	for _, hints := range []string{"", "0"} {
		t.Setenv(envHintsDisabled, hints)
		dec, prov, err := resolveDecoderVocabulary(t.TempDir(), decoderVocabulary{Terms: []string{"Cassini"}}, paths)
		if err != nil || dec.Method != decodingGreedySearch || dec.Biased() {
			t.Fatalf("%s=%q: decodes with %+v, %v", envHintsDisabled, hints, dec, err)
		}
		if prov == nil || prov.Applied {
			t.Fatalf("%s=%q: claims hints it cannot apply: %+v", envHintsDisabled, hints, prov)
		}
	}
}

// The catalogue installs exactly the files its spec loads, under the revision
// dist.gocassini.com publishes (the SHA-256 of the sorted `sha256sum` listing).
// tokens.txt is the same object as Parakeet Ultra's. It replaces every earlier
// int8 model, newest first, which is the order an upgraded install picks a
// stand-in in. The NOTICE carries the attribution FluidVoice asks for.
func TestFluidSpeechProCatalogueEntry(t *testing.T) {
	c := modelstore.Shipped()
	m, err := c.Model(string(ModelFluidSpeechProInt8), "")
	if err != nil {
		t.Fatal(err)
	}
	if m.Revision != fluidSpeechProInt8Revision || m.Kind != modelstore.KindSpeech || m.License != "CC-BY-4.0" || m.SourceURL != "https://huggingface.co/FluidVoice-Labs/fluid-speech-pro" {
		t.Fatalf("catalogue entry %+v", m)
	}
	if !reflect.DeepEqual(m.Replaces, []string{string(ModelParakeetUltraInt8), string(ModelParakeetUltraInt8V1), string(ModelParakeetV3Int8)}) {
		t.Fatalf("replaces %v", m.Replaces)
	}
	ultra, err := c.Model(string(ModelParakeetUltra), "")
	if err != nil {
		t.Fatal(err)
	}
	ultraTokens := ""
	for _, f := range ultra.Files {
		if f.Path == "tokens.txt" {
			ultraTokens = f.Artifact
		}
	}
	listing := map[string]string{}
	required := map[string]bool{}
	for _, f := range m.Files {
		a, err := c.Artifact(f.Artifact)
		if err != nil {
			t.Fatal(err)
		}
		listing[f.Path] = a.UncompressedSHA256
		required[f.Path] = f.Required
		if f.Path == "tokens.txt" && f.Artifact != ultraTokens {
			t.Fatalf("tokens.txt %s, Ultra's %s", f.Artifact, ultraTokens)
		}
	}
	if _, ok := listing["bpe.vocab"]; ok {
		t.Fatal("Fluid Speech Pro installs a bpe.vocab its spec never loads")
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
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(sums.String()))); got != m.Revision {
		t.Fatalf("revision %s, but the files' sha256sum listing hashes to %s", m.Revision, got)
	}
	if len(m.Files) != len(RequiredModelFileNames(ModelFluidSpeechProInt8)) {
		t.Fatalf("catalogue files %+v", m.Files)
	}
	for _, name := range RequiredModelFileNames(ModelFluidSpeechProInt8) {
		if !required[name] {
			t.Fatalf("spec requires %s, the catalogue does not install it as required", name)
		}
	}
	for _, want := range []string{
		"Fluid Speech Pro by FluidVoice. Built on NVIDIA Parakeet TDT 0.6B v3 and\nMoondream Parakeet Ultra.",
		"https://huggingface.co/FluidVoice-Labs/fluid-speech-pro",
		"https://huggingface.co/FluidVoice-Labs/fluid-speech-pro-onnx",
		"int8 variant by Cassini",
		"No endorsement by FluidVoice, Moondream, NVIDIA or sherpa-onnx",
	} {
		if !strings.Contains(modelstore.Notices, want) {
			t.Fatalf("NOTICE lacks %q", want)
		}
	}
}
