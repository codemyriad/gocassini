package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gocassini/internal/portable"
)

const (
	v3Int8Revision = "5793d0fd397c5778d2cf2126994d58e9d56b1be7c04d13c7a15bb1b4eafb16bf"
	v3Fp32Revision = "9125e6a7beb392821f19a3633b80bb2847599580e34ed42a4128732978ad759c"
)

// Parakeet v3 is kept only to stand in for the Ultra model that replaced it:
// it runs as the last version that shipped it ran it, it is never selected or
// downloaded, and a configuration that names it still moves to Ultra.
func TestRetiredV3Specs(t *testing.T) {
	for _, id := range []ModelID{ModelParakeetV3Int8, ModelParakeetV3} {
		spec := knownModels[id]
		if !spec.Retired || spec.URL != "" || spec.ModelType != "nemo_transducer" || spec.SampleRate != 16000 || spec.FeatureDim != 128 {
			t.Fatalf("%s spec %+v", id, spec)
		}
		// The v3 reference frontend, and the standard profile on a runtime
		// without it rather than a refusal.
		if !reflect.DeepEqual(spec.ReferenceRuntimeMarkers, []string{parakeetReferenceRuntimeMarker}) || spec.RefuseWithoutReferenceRuntime {
			t.Fatalf("%s markers %v refuse=%v", id, spec.ReferenceRuntimeMarkers, spec.RefuseWithoutReferenceRuntime)
		}
		if !usesParakeetV3ReferencePolicy(id) || !ReferenceFrontendActive(id, cassini6RuntimeVersion) || refuseWithoutReferenceRuntime(id, stockRuntimeVersion) != nil {
			t.Fatalf("%s does not run as v3 did", id)
		}
		if _, err := EnsureModel(t.TempDir(), id, io.Discard); err == nil || !strings.Contains(err.Error(), "has no archive download") {
			t.Fatalf("EnsureModel(%s) = %v, want no download", id, err)
		}
		if got, retired := ReplacementModelID(id); !retired || got == id {
			t.Fatalf("ReplacementModelID(%s) = %s %v", id, got, retired)
		}
	}
	if !reflect.DeepEqual(RequiredModelFileNames(ModelParakeetV3), []string{"encoder.onnx", "decoder.onnx", "joiner.onnx", "tokens.txt", "encoder.weights"}) {
		t.Fatalf("v3 fp32 files %v", RequiredModelFileNames(ModelParakeetV3))
	}
	if !reflect.DeepEqual(RequiredModelFileNames(ModelParakeetV3Int8), []string{"encoder.int8.onnx", "decoder.int8.onnx", "joiner.int8.onnx", "tokens.txt"}) {
		t.Fatalf("v3 int8 files %v", RequiredModelFileNames(ModelParakeetV3Int8))
	}
	if !SupportsCUDA(ModelParakeetV3) || SupportsCUDA(ModelParakeetV3Int8) {
		t.Fatal("only v3 fp32 runs on CUDA")
	}
	if !knownModels[ModelParakeetV3Int8].GreedyWithoutBpeVocab {
		t.Fatal("v3 int8 must decode greedily without bpe.vocab")
	}
	for id, want := range map[ModelID]int{ModelParakeetV3Int8: 2816, ModelParakeetV3: 4608} {
		if got := probeMemoryFloor(id, "cpu"); got != want {
			t.Fatalf("probeMemoryFloor(%s) = %d, want %d", id, got, want)
		}
	}
	for _, q := range []STTQuality{QualityFast, QualityBalanced, QualityBest} {
		for _, device := range []string{"cpu", "cuda"} {
			if knownModels[ModelForQuality(q, device)].Retired {
				t.Fatalf("%s on %s selects a retired model", q, device)
			}
		}
	}
	if got := ResolveModelID("parakeet-tdt-0.6b-v3-int8", "balanced", "cpu"); got != ModelParakeetUltraInt8 {
		t.Fatalf("a v3 int8 pin resolves to %s", got)
	}
	// Only the pair the catalogue relates may stand in.
	for _, c := range []struct {
		retired, model ModelID
		want           bool
	}{
		{ModelParakeetV3Int8, ModelParakeetUltraInt8, true},
		{ModelParakeetV3, ModelParakeetUltra, true},
		{ModelParakeetV3Int8, ModelParakeetUltra, false},
		{ModelParakeetV3, ModelParakeetUltraInt8, false},
		{ModelParakeet110M, ModelParakeetUltraInt8, false},
		{"no-such-model", ModelParakeetUltraInt8, false},
	} {
		if got := StandInFor(c.retired, c.model); got != c.want {
			t.Errorf("StandInFor(%s, %s) = %v", c.retired, c.model, got)
		}
	}
}

// The operator asks for a stand-in for one build. Only a retired model with
// the replacement the catalogue names keeps its id and revision; any other
// request is ignored, and the retired id moves to Ultra as before.
func TestDefaultBuildConfigStandIn(t *testing.T) {
	t.Setenv("CASSINI_STT_MODEL", "parakeet-tdt-0.6b-v3-int8")
	t.Setenv("CASSINI_STT_REVISION", v3Int8Revision)
	t.Setenv(envSTTStandInFor, "parakeet-ultra-0.6b-int8")
	t.Setenv(envSTTStandInReason, portable.StandInReasonInstalling)
	t.Setenv(envSTTStandInDetail, "replaces retired parakeet-tdt-0.6b-v3-int8; being installed (downloading, 40%)")
	t.Setenv("CASSINI_STT_ADDITIONAL_MODELS", "parakeet-tdt-0.6b-v3")
	cfg := DefaultBuildConfig()
	want := &portable.TranscriptionStandIn{For: "parakeet-ultra-0.6b-int8", Reason: "installing", Detail: "replaces retired parakeet-tdt-0.6b-v3-int8; being installed (downloading, 40%)"}
	if cfg.ModelID != ModelParakeetV3Int8 || cfg.ModelRevision != v3Int8Revision || cfg.ReplacedModelID != "" || !reflect.DeepEqual(cfg.StandIn, want) {
		t.Fatalf("stand-in config %s %s replaced=%q %+v", cfg.ModelID, cfg.ModelRevision, cfg.ReplacedModelID, cfg.StandIn)
	}
	if !reflect.DeepEqual(cfg.AdditionalModels, []ModelID{ModelParakeetUltra}) {
		t.Fatalf("additional models %v", cfg.AdditionalModels)
	}
	// The detail changes as the download progresses; a resumed build must not
	// read that as a different policy.
	raw, _ := json.Marshal(cfg)
	if strings.Contains(string(raw), "downloading") || strings.Contains(string(raw), "StandIn") {
		t.Fatalf("the stand-in reaches the resume fingerprint: %s", raw)
	}

	for name, standInFor := range map[string]string{"mismatched precision": "parakeet-ultra-0.6b", "no request": ""} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(envSTTStandInFor, standInFor)
			cfg := DefaultBuildConfig()
			if cfg.ModelID != ModelParakeetUltraInt8 || cfg.ModelRevision != "" || cfg.ReplacedModelID != ModelParakeetV3Int8 || cfg.StandIn != nil || string(cfg.StandInIgnored) != standInFor {
				t.Fatalf("config %s %q replaced=%q stand-in %+v ignored %q", cfg.ModelID, cfg.ModelRevision, cfg.ReplacedModelID, cfg.StandIn, cfg.StandInIgnored)
			}
		})
	}
	t.Run("not retired", func(t *testing.T) {
		t.Setenv("CASSINI_STT_MODEL", "parakeet-ultra-0.6b-int8")
		t.Setenv("CASSINI_STT_REVISION", "")
		cfg := DefaultBuildConfig()
		if cfg.ModelID != ModelParakeetUltraInt8 || cfg.StandIn != nil || cfg.StandInIgnored != ModelParakeetUltraInt8 {
			t.Fatalf("config %s stand-in %+v ignored %q", cfg.ModelID, cfg.StandIn, cfg.StandInIgnored)
		}
	})
}

// standInBuild runs a build with the operator's stand-in environment, a fake
// backend and the model resolution replaced by load, and returns its
// manifest, log and the models it resolved.
func standInBuild(t *testing.T, load func(ModelID, string) (ModelPaths, error)) (portable.Processing, string, []string) {
	t.Helper()
	requireFFMediaTools(t)
	t.Setenv("CASSINI_ATTRIBUTION_DISABLED", "1")
	t.Setenv("CASSINI_STT_MODEL", "parakeet-tdt-0.6b-v3-int8")
	t.Setenv("CASSINI_STT_REVISION", v3Int8Revision)
	t.Setenv(envSTTStandInFor, "parakeet-ultra-0.6b-int8")
	t.Setenv(envSTTStandInReason, portable.StandInReasonInstalling)
	t.Setenv(envSTTStandInDetail, "being installed (downloading, 40%)")
	registerFixedBackend(t, "fake-stand-in", fixedTimedWords(12))
	dir := t.TempDir()
	mkv := buildTwoTrackMeetingFromSmoke(t, dir)
	var resolved []string
	prevModel, prevVAD := ensureModelFn, ensureVADFn
	ensureModelFn = func(_ string, id ModelID, revision string, _ io.Writer) (ModelPaths, error) {
		resolved = append(resolved, string(id)+"@"+revision)
		return load(id, revision)
	}
	ensureVADFn = func(string, ModelID, string, io.Writer) (string, error) { return "", nil }
	t.Cleanup(func() { ensureModelFn, ensureVADFn = prevModel, prevVAD })

	cfg := DefaultBuildConfig()
	cfg.TranscriptionMode, cfg.Device, cfg.Backend, cfg.CacheDir, cfg.NumThreads, cfg.SkipAttribution = "on", "cpu", "fake-stand-in", t.TempDir(), 1, true
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := BuildMeetingArtifact(context.Background(), mkv, out, cfg, &stdout); err != nil {
		t.Fatalf("BuildMeetingArtifact: %v\n%s", err, stdout.String())
	}
	raw, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest artifactManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Processing == nil {
		t.Fatalf("no processing: %s", raw)
	}
	return *manifest.Processing, stdout.String(), resolved
}

// A build standing in with v3 for Ultra transcribes with v3, and the
// transcript records which model stood in for which, and why.
func TestStandInBuildRecordsWhy(t *testing.T) {
	processing, log, resolved := standInBuild(t, func(ModelID, string) (ModelPaths, error) {
		return ModelPaths{ModelType: "stub", SampleRate: 16000}, nil
	})
	if !reflect.DeepEqual(resolved, []string{"parakeet-tdt-0.6b-v3-int8@" + v3Int8Revision}) {
		t.Fatalf("resolved %v", resolved)
	}
	want := portable.TranscriptionStatus{Status: "completed", Model: "parakeet-tdt-0.6b-v3-int8", Revision: v3Int8Revision,
		StandIn: &portable.TranscriptionStandIn{For: "parakeet-ultra-0.6b-int8", Reason: "installing", Detail: "being installed (downloading, 40%)"}}
	if !reflect.DeepEqual(processing.Transcription, want) {
		t.Fatalf("transcription %+v standIn %+v", processing.Transcription, processing.Transcription.StandIn)
	}
	if !strings.Contains(log, "STT model parakeet-tdt-0.6b-v3-int8 stands in for parakeet-ultra-0.6b-int8, which is not ready (being installed (downloading, 40%))") {
		t.Fatalf("log does not say why:\n%s", log)
	}
}

// The v3 files can go between the operator's admission and the build taking
// the inference lock, when Ultra passes a check run outside the operator.
// They go only once Ultra has passed it, so the build uses Ultra instead of
// keeping only the audio.
func TestStandInWhoseFilesAreGoneUsesItsReplacement(t *testing.T) {
	processing, log, resolved := standInBuild(t, func(id ModelID, _ string) (ModelPaths, error) {
		if id == ModelParakeetV3Int8 {
			return ModelPaths{}, errors.New("model is not installed/verified")
		}
		return ModelPaths{ModelType: "stub", SampleRate: 16000}, nil
	})
	if !reflect.DeepEqual(resolved, []string{"parakeet-tdt-0.6b-v3-int8@" + v3Int8Revision, "parakeet-ultra-0.6b-int8@18cb623004dd6c15999b93cb70b14d8a1f6e8f3a2a97ff733f104bfc3494f6a9"}) {
		t.Fatalf("resolved %v", resolved)
	}
	if got := processing.Transcription; got.Status != "completed" || got.Model != "parakeet-ultra-0.6b-int8" || got.Revision != "18cb623004dd6c15999b93cb70b14d8a1f6e8f3a2a97ff733f104bfc3494f6a9" || got.StandIn != nil {
		t.Fatalf("transcription %+v", got)
	}
	if !strings.Contains(log, "retired parakeet-tdt-0.6b-v3-int8 is gone") || !strings.Contains(log, "using parakeet-ultra-0.6b-int8, which replaced it") {
		t.Fatalf("log:\n%s", log)
	}
}
