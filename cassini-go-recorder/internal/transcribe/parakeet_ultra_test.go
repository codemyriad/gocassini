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

// Parakeet Ultra is v3 post-trained: same architecture, tokenizer and
// features. Both precisions get the reference boundary policy; only fp32 runs
// on CUDA. The gates read these spec fields, never the ids.
func TestParakeetUltraSpecs(t *testing.T) {
	for _, id := range []ModelID{ModelParakeetUltra, ModelParakeetUltraInt8} {
		spec := knownModels[id]
		if spec.URL != "" {
			t.Fatalf("%s has an archive URL %q; it installs from dist.gocassini.com only", id, spec.URL)
		}
		if spec.ModelType != "nemo_transducer" || spec.SampleRate != 16000 || spec.FeatureDim != 128 {
			t.Fatalf("%s spec %+v", id, spec)
		}
		if !usesParakeetBoundaryPolicy(id) || !reflect.DeepEqual(spec.RuntimeMarkers, []string{parakeetTDTJoinerRuntimeMarker}) {
			t.Fatalf("%s does not get the Parakeet boundary policy, or loads without the TDT joiner detection", id)
		}
	}
	if vadDecodePolicyForModel(ModelParakeetUltra) != vadDecodePolicyForModel(ModelParakeetUltraInt8) {
		t.Fatal("the two Ultra precisions get different boundary policies")
	}
	if !SupportsCUDA(ModelParakeetUltra) {
		t.Fatal("Ultra fp32 must install and probe on CUDA")
	}
	for _, id := range []ModelID{ModelParakeetUltraInt8, ModelParakeet110M, "parakeet-tdt-0.6b-v3", "unknown"} {
		if SupportsCUDA(id) {
			t.Fatalf("%s must not run on CUDA", id)
		}
	}
	if !reflect.DeepEqual(RequiredModelFileNames(ModelParakeetUltraInt8), []string{"encoder.int8.onnx", "decoder.int8.onnx", "joiner.int8.onnx", "tokens.txt"}) {
		t.Fatalf("Ultra int8 files %v", RequiredModelFileNames(ModelParakeetUltraInt8))
	}
	// The int8 peak measured 1,893 MiB over 19 real builds; its check is
	// charged what v3 int8's was. An unknown id gets the largest floor.
	for id, want := range map[ModelID]int{ModelParakeet110M: 1536, ModelParakeetUltraInt8: 2816, ModelParakeetUltra: 4608, "nemotron-3-diarization-int8": 1024, "nemotron-3-diarization": 1536, "unknown": 4608} {
		if got := probeMemoryFloor(id, "cpu"); got != want {
			t.Fatalf("probeMemoryFloor(%s, cpu) = %d, want %d", id, got, want)
		}
	}
}

// Both Ultra precisions decode greedily by default, without hints (D-867).
// With biasing turned back on, Ultra fp32 ships bpe.vocab and beam-searches
// with hints, while Ultra int8 still decodes greedily, as v3 int8 did: on it
// greedy search omitted less speech than the unbiased beam decoder, at 22%
// less CPU and half the peak memory. Its spec names no vocabulary, so even a
// bpe.vocab that ends up in its directory cannot switch it to beam search.
func TestParakeetUltraDecoders(t *testing.T) {
	t.Setenv(envHintsDisabled, "")
	fp32 := transducerPaths(t)
	fp32.ModelID = ModelParakeetUltra
	dec, hints, err := resolveDecoderVocabulary(t.TempDir(), decoderVocabulary{Terms: []string{"Cassini"}}, fp32)
	if err != nil || dec.Method != decodingGreedySearch || dec.Biased() || hints == nil || hints.Applied {
		t.Fatalf("Ultra fp32 by default decodes with %+v, hints %+v, %v", dec, hints, err)
	}

	enableHints(t)
	dec, _, err = resolveDecoderVocabulary(t.TempDir(), decoderVocabulary{}, fp32)
	if err != nil || dec.Method != decodingModifiedBeamSearch {
		t.Fatalf("Ultra fp32 with bpe.vocab and hints on decodes with %+v, %v", dec, err)
	}

	dir := t.TempDir()
	for _, name := range append(RequiredModelFileNames(ModelParakeetUltraInt8), "bpe.vocab") {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	int8Paths := resolveModelPaths(dir, knownModels[ModelParakeetUltraInt8], ModelParakeetUltraInt8)
	if int8Paths.BpeVocabFile != "" {
		t.Fatalf("Ultra int8 resolved a vocabulary: %s", int8Paths.BpeVocabFile)
	}
	dec, hints, err = resolveDecoderVocabulary(t.TempDir(), decoderVocabulary{Terms: []string{"Cassini"}}, int8Paths)
	if err != nil || dec.Method != decodingGreedySearch {
		t.Fatalf("Ultra int8 decodes with %+v, %v", dec, err)
	}
	if hints == nil || hints.Applied {
		t.Fatalf("Ultra int8 claims hints it cannot apply: %+v", hints)
	}
}

// Each catalogue entry installs exactly the files its spec loads, under the
// revision dist.gocassini.com publishes (the SHA-256 of the `sha256sum`
// listing of the files sorted by name, not Moondream's model.safetensors,
// credited in the NOTICE). Both share v3's tokenizer file.
func TestParakeetUltraCatalogueEntries(t *testing.T) {
	c := modelstore.Shipped()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	tokens := ""
	for id, revision := range map[ModelID]string{
		ModelParakeetUltra:     "5030173982bf58c7975b4b26bd47cc4612f54c5c93846756f5c27cbff27f94f6",
		ModelParakeetUltraInt8: "18cb623004dd6c15999b93cb70b14d8a1f6e8f3a2a97ff733f104bfc3494f6a9",
	} {
		m, err := c.Model(string(id), "")
		if err != nil {
			t.Fatal(err)
		}
		if m.Kind != modelstore.KindSpeech || m.License != "CC-BY-4.0" || m.SourceURL != "https://huggingface.co/moondream/parakeet-ultra" {
			t.Fatalf("catalogue entry %+v", m)
		}
		if m.Revision != revision {
			t.Fatalf("%s revision %s is not the published Cassini export %s", id, m.Revision, revision)
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
			if f.Path == "tokens.txt" {
				if tokens != "" && tokens != f.Artifact {
					t.Fatalf("the Ultra models ship different tokens.txt objects")
				}
				tokens = f.Artifact
			}
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
			t.Fatalf("%s revision %s, but the files' sha256sum listing hashes to %s", id, m.Revision, got)
		}
		for _, name := range RequiredModelFileNames(id) {
			if !required[name] {
				t.Fatalf("%s spec requires %s, the catalogue does not install it as required", id, name)
			}
		}
		if spec := knownModels[id]; spec.BpeVocabFile != "" {
			if _, ok := listing[spec.BpeVocabFile]; !ok || required[spec.BpeVocabFile] {
				t.Fatalf("%s: bpe.vocab must be an optional catalogue file", id)
			}
		} else if _, ok := listing["bpe.vocab"]; ok {
			t.Fatalf("%s installs a bpe.vocab its spec never loads", id)
		}
	}
	for _, want := range []string{"c9608f36d0ab956c14bfcc525479b0746b3b42a56f6949ec85c14eb7466717dc", "https://huggingface.co/moondream/parakeet-ultra", "https://huggingface.co/nvidia/parakeet-tdt-0.6b-v3", "Post-trained by Moondream", "int8 variant by Cassini", "No endorsement by Moondream"} {
		if !strings.Contains(modelstore.Notices, want) {
			t.Fatalf("NOTICE lacks %q", want)
		}
	}
}

// Parakeet v3 is retired: a configuration that still names it resolves to the
// Ultra model of the same precision, drops the revision it pinned (that was
// v3's), and says so. An id no catalogue ever shipped is left alone, so the
// build reports it unavailable rather than transcribing with another model.
func TestRetiredModelResolvesToItsReplacement(t *testing.T) {
	cases := []struct {
		explicit, revision, device string
		want                       ResolvedModel
	}{
		{"parakeet-tdt-0.6b-v3", "9125e6a7beb392821f19a3633b80bb2847599580e34ed42a4128732978ad759c", "cuda",
			ResolvedModel{ID: ModelParakeetUltra, Replaces: "parakeet-tdt-0.6b-v3"}},
		{"parakeet-tdt-0.6b-v3-int8", "5793d0fd397c5778d2cf2126994d58e9d56b1be7c04d13c7a15bb1b4eafb16bf", "cpu",
			ResolvedModel{ID: ModelParakeetUltraInt8, Replaces: "parakeet-tdt-0.6b-v3-int8"}},
		{"parakeet-ultra-0.6b-int8", "18cb623004dd6c15999b93cb70b14d8a1f6e8f3a2a97ff733f104bfc3494f6a9", "cpu",
			ResolvedModel{ID: ModelParakeetUltraInt8, Revision: "18cb623004dd6c15999b93cb70b14d8a1f6e8f3a2a97ff733f104bfc3494f6a9"}},
		{"no-such-model", "abc", "cpu", ResolvedModel{ID: "no-such-model", Revision: "abc"}},
	}
	for _, c := range cases {
		if got := ResolveModel(c.explicit, c.revision, "balanced", c.device); got != c.want {
			t.Errorf("ResolveModel(%s) = %+v, want %+v", c.explicit, got, c.want)
		}
	}
}

// The environment the operator, an image or an administrator sets is where a
// v3 pin survives an upgrade. The build moves to Ultra and its log says why.
func TestBuildConfigMovesARetiredEnvPinToUltra(t *testing.T) {
	t.Setenv("CASSINI_STT_MODEL", "parakeet-tdt-0.6b-v3-int8")
	t.Setenv("CASSINI_STT_REVISION", "5793d0fd397c5778d2cf2126994d58e9d56b1be7c04d13c7a15bb1b4eafb16bf")
	t.Setenv("CASSINI_STT_ADDITIONAL_MODELS", "parakeet-tdt-0.6b-v3, parakeet-tdt-ctc-110m-en-int8")
	cfg := DefaultBuildConfig()
	if cfg.ModelID != ModelParakeetUltraInt8 || cfg.ModelRevision != "" || cfg.ReplacedModelID != "parakeet-tdt-0.6b-v3-int8" {
		t.Fatalf("model %s revision %q replaced %q", cfg.ModelID, cfg.ModelRevision, cfg.ReplacedModelID)
	}
	if !reflect.DeepEqual(cfg.AdditionalModels, []ModelID{ModelParakeetUltra, ModelParakeet110M}) {
		t.Fatalf("additional models %v", cfg.AdditionalModels)
	}

	t.Setenv("CASSINI_STT_MODEL", "no-such-model")
	t.Setenv("CASSINI_STT_REVISION", "")
	if cfg := DefaultBuildConfig(); cfg.ModelID != "no-such-model" || cfg.ReplacedModelID != "" {
		t.Fatalf("an unknown id became %s (replaced %q)", cfg.ModelID, cfg.ReplacedModelID)
	}
}
