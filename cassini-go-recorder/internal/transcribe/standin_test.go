package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gocassini/internal/portable"
)

const (
	ultraInt8V1Revision = "18cb623004dd6c15999b93cb70b14d8a1f6e8f3a2a97ff733f104bfc3494f6a9"
	ultraInt8V2Revision = "50c15b94ea9cb1c2f8dbcdcbc0f15b450169154deeb206923cb5388f60892366"
	v3Int8Revision      = "5793d0fd397c5778d2cf2126994d58e9d56b1be7c04d13c7a15bb1b4eafb16bf"
	v3Fp32Revision      = "9125e6a7beb392821f19a3633b80bb2847599580e34ed42a4128732978ad759c"
)

// Parakeet Ultra int8 v1 and Parakeet v3 are kept only to stand in for the
// shipped Ultra model that replaced them: they run as the last version that
// shipped them ran them, they are never selected or downloaded, and a
// configuration that names one still moves to its replacement.
func TestRetiredModelSpecs(t *testing.T) {
	for _, id := range []ModelID{ModelParakeetUltraInt8V1, ModelParakeetV3Int8, ModelParakeetV3} {
		spec := knownModels[id]
		if !spec.Retired || spec.URL != "" || spec.ModelType != "nemo_transducer" || spec.SampleRate != 16000 || spec.FeatureDim != 128 {
			t.Fatalf("%s spec %+v", id, spec)
		}
		if !usesParakeetBoundaryPolicy(id) || CheckNativeRuntime(id, cassiniRuntimeVersion) != nil || CheckNativeRuntime(id, legacyCassiniRuntimeVersion) == nil {
			t.Fatalf("%s does not run on the Cassini 2.0.1 runtime", id)
		}
		if _, err := EnsureModel(t.TempDir(), id, io.Discard); err == nil || !strings.Contains(err.Error(), "has no archive download") {
			t.Fatalf("EnsureModel(%s) = %v, want no download", id, err)
		}
		if got, retired := ReplacementModelID(id); !retired || got == id {
			t.Fatalf("ReplacementModelID(%s) = %s %v", id, got, retired)
		}
	}
	// v3 carries NVIDIA's tdt url so any 2.0.1 runtime loads it; Ultra int8 v1
	// carries the plain Moondream url so it requires the TDT joiner marker.
	for _, id := range []ModelID{ModelParakeetV3Int8, ModelParakeetV3} {
		if len(knownModels[id].RuntimeMarkers) != 0 || CheckNativeRuntime(id, stockRuntimeVersion) != nil {
			t.Fatalf("%s markers %v", id, knownModels[id].RuntimeMarkers)
		}
	}
	if !reflect.DeepEqual(knownModels[ModelParakeetUltraInt8V1].RuntimeMarkers, []string{parakeetTDTJoinerRuntimeMarker}) || CheckNativeRuntime(ModelParakeetUltraInt8V1, stockRuntimeVersion) == nil {
		t.Fatalf("Ultra int8 v1 must require %s", parakeetTDTJoinerRuntimeMarker)
	}
	if !reflect.DeepEqual(RequiredModelFileNames(ModelParakeetV3), []string{"encoder.onnx", "decoder.onnx", "joiner.onnx", "tokens.txt", "encoder.weights"}) {
		t.Fatalf("v3 fp32 files %v", RequiredModelFileNames(ModelParakeetV3))
	}
	for _, id := range []ModelID{ModelParakeetUltraInt8V1, ModelParakeetV3Int8} {
		if !reflect.DeepEqual(RequiredModelFileNames(id), []string{"encoder.int8.onnx", "decoder.int8.onnx", "joiner.int8.onnx", "tokens.txt"}) {
			t.Fatalf("%s files %v", id, RequiredModelFileNames(id))
		}
		if !knownModels[id].GreedyWithoutBpeVocab {
			t.Fatalf("%s must decode greedily without bpe.vocab", id)
		}
	}
	if !SupportsCUDA(ModelParakeetV3) || SupportsCUDA(ModelParakeetV3Int8) || SupportsCUDA(ModelParakeetUltraInt8V1) {
		t.Fatal("only v3 fp32 runs on CUDA")
	}
	for id, want := range map[ModelID]int{ModelParakeetUltraInt8V1: 2816, ModelParakeetV3Int8: 2816, ModelParakeetV3: 4608} {
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
	for _, retired := range []string{"parakeet-ultra-0.6b-int8", "parakeet-tdt-0.6b-v3-int8"} {
		if got := ResolveModelID(retired, "balanced", "cpu"); got != ModelParakeetUltraInt8 {
			t.Fatalf("a %s pin resolves to %s", retired, got)
		}
	}
	// Only the pair the catalogue relates may stand in.
	for _, c := range []struct {
		retired, model ModelID
		want           bool
	}{
		{ModelParakeetUltraInt8V1, ModelParakeetUltraInt8, true},
		{ModelParakeetV3Int8, ModelParakeetUltraInt8, true},
		{ModelParakeetV3, ModelParakeetUltra, true},
		{ModelParakeetUltraInt8V1, ModelParakeetUltra, false},
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
	t.Setenv(envSTTStandInFor, "parakeet-ultra-0.6b-int8-v2")
	t.Setenv(envSTTStandInReason, portable.StandInReasonInstalling)
	t.Setenv(envSTTStandInDetail, "replaces retired parakeet-tdt-0.6b-v3-int8; being installed (downloading, 40%)")
	t.Setenv("CASSINI_STT_ADDITIONAL_MODELS", "parakeet-tdt-0.6b-v3")
	cfg := DefaultBuildConfig()
	want := &portable.TranscriptionStandIn{For: "parakeet-ultra-0.6b-int8-v2", Reason: "installing", Detail: "replaces retired parakeet-tdt-0.6b-v3-int8; being installed (downloading, 40%)"}
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

	t.Run("ultra int8 v1 stands in for v2", func(t *testing.T) {
		t.Setenv("CASSINI_STT_MODEL", "parakeet-ultra-0.6b-int8")
		t.Setenv("CASSINI_STT_REVISION", ultraInt8V1Revision)
		t.Setenv(envSTTStandInFor, "parakeet-ultra-0.6b-int8-v2")
		t.Setenv(envSTTStandInDetail, "replaces retired parakeet-ultra-0.6b-int8; being installed (downloading, 40%)")
		cfg := DefaultBuildConfig()
		want := &portable.TranscriptionStandIn{For: "parakeet-ultra-0.6b-int8-v2", Reason: "installing", Detail: "replaces retired parakeet-ultra-0.6b-int8; being installed (downloading, 40%)"}
		if cfg.ModelID != ModelParakeetUltraInt8V1 || cfg.ModelRevision != ultraInt8V1Revision || cfg.ReplacedModelID != "" || !reflect.DeepEqual(cfg.StandIn, want) {
			t.Fatalf("stand-in config %s %s replaced=%q %+v", cfg.ModelID, cfg.ModelRevision, cfg.ReplacedModelID, cfg.StandIn)
		}
	})

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
		t.Setenv("CASSINI_STT_MODEL", "parakeet-ultra-0.6b-int8-v2")
		t.Setenv("CASSINI_STT_REVISION", "")
		cfg := DefaultBuildConfig()
		if cfg.ModelID != ModelParakeetUltraInt8 || cfg.StandIn != nil || cfg.StandInIgnored != ModelParakeetUltraInt8 {
			t.Fatalf("config %s stand-in %+v ignored %q", cfg.ModelID, cfg.StandIn, cfg.StandInIgnored)
		}
	})
}

// standInBuildFor runs a build with the operator's stand-in environment for
// retiredModel@revision, a fake backend and the model resolution replaced by
// load, and returns its manifest, log and the models it resolved.
func standInBuildFor(t *testing.T, retiredModel ModelID, revision string, load func(ModelID, string) (ModelPaths, error)) (portable.Processing, string, []string) {
	t.Helper()
	requireFFMediaTools(t)
	t.Setenv("CASSINI_ATTRIBUTION_DISABLED", "1")
	t.Setenv("CASSINI_STT_MODEL", string(retiredModel))
	t.Setenv("CASSINI_STT_REVISION", revision)
	t.Setenv(envSTTStandInFor, "parakeet-ultra-0.6b-int8-v2")
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

func standInBuild(t *testing.T, load func(ModelID, string) (ModelPaths, error)) (portable.Processing, string, []string) {
	t.Helper()
	return standInBuildFor(t, ModelParakeetV3Int8, v3Int8Revision, load)
}

// A build standing in with a retired model (Ultra int8 v1 or v3 int8) for
// Ultra int8 v2 transcribes with the stand-in, and the transcript records
// which model stood in for which, and why.
func TestStandInBuildRecordsWhy(t *testing.T) {
	for _, c := range []struct {
		model    ModelID
		revision string
	}{
		{ModelParakeetUltraInt8V1, ultraInt8V1Revision},
		{ModelParakeetV3Int8, v3Int8Revision},
	} {
		t.Run(string(c.model), func(t *testing.T) {
			processing, log, resolved := standInBuildFor(t, c.model, c.revision, func(ModelID, string) (ModelPaths, error) {
				return ModelPaths{ModelType: "stub", SampleRate: 16000}, nil
			})
			if !reflect.DeepEqual(resolved, []string{string(c.model) + "@" + c.revision}) {
				t.Fatalf("resolved %v", resolved)
			}
			want := portable.TranscriptionStatus{Status: "completed", Model: string(c.model), Revision: c.revision,
				StandIn: &portable.TranscriptionStandIn{For: "parakeet-ultra-0.6b-int8-v2", Reason: "installing", Detail: "being installed (downloading, 40%)"}}
			if !reflect.DeepEqual(processing.Transcription, want) {
				t.Fatalf("transcription %+v standIn %+v", processing.Transcription, processing.Transcription.StandIn)
			}
			if !strings.Contains(log, "STT model "+string(c.model)+" stands in for parakeet-ultra-0.6b-int8-v2, which is not ready (being installed (downloading, 40%))") {
				t.Fatalf("log does not say why:\n%s", log)
			}
		})
	}
}

// stubModelReady makes ready the models that ready names, on the CPU.
func stubModelReady(t *testing.T, ready ...ModelID) {
	t.Helper()
	prev := modelReadyFn
	modelReadyFn = func(_ string, id ModelID, device string) bool {
		for _, r := range ready {
			if id == r && device == "cpu" {
				return true
			}
		}
		return false
	}
	t.Cleanup(func() { modelReadyFn = prev })
}

// A stand-in's files can go between the operator's admission and the build
// taking the inference lock, when its replacement passes a check run outside
// the operator. They go only once the replacement has passed it, so the build
// uses the replacement instead of keeping only the audio.
func TestStandInWhoseFilesAreGoneUsesItsReplacement(t *testing.T) {
	if ModelReady(t.TempDir(), ModelParakeetUltraInt8, "cpu") {
		t.Fatal("an empty store has a ready model")
	}
	for _, c := range []struct {
		model    ModelID
		revision string
	}{
		{ModelParakeetUltraInt8V1, ultraInt8V1Revision},
		{ModelParakeetV3Int8, v3Int8Revision},
	} {
		t.Run(string(c.model), func(t *testing.T) {
			if _, err := InstalledModel(t.TempDir(), c.model, c.revision, io.Discard); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("removed %s: %v", c.model, err)
			}
			stubModelReady(t, ModelParakeetUltraInt8)
			processing, log, resolved := standInBuildFor(t, c.model, c.revision, func(id ModelID, _ string) (ModelPaths, error) {
				if id == c.model {
					return ModelPaths{}, fmt.Errorf("model is not installed/verified: %w", fs.ErrNotExist)
				}
				return ModelPaths{ModelType: "stub", SampleRate: 16000}, nil
			})
			if !reflect.DeepEqual(resolved, []string{string(c.model) + "@" + c.revision, "parakeet-ultra-0.6b-int8-v2@" + ultraInt8V2Revision}) {
				t.Fatalf("resolved %v", resolved)
			}
			if got := processing.Transcription; got.Status != "completed" || got.Model != "parakeet-ultra-0.6b-int8-v2" || got.Revision != ultraInt8V2Revision || got.StandIn != nil {
				t.Fatalf("transcription %+v", got)
			}
			if !strings.Contains(log, "retired "+string(c.model)+" is gone") || !strings.Contains(log, "using parakeet-ultra-0.6b-int8-v2, which replaced it") {
				t.Fatalf("log:\n%s", log)
			}
		})
	}
}

// The build was admitted and sized for v3, so it switches to Ultra only when
// v3's files are gone and Ultra has passed its check here, which is what
// removing them requires. v3 files that are there but changed, or an Ultra
// that failed its check, keep the audio as any unavailable model does.
func TestStandInFallsBackOnlyToAReadyReplacement(t *testing.T) {
	cases := []struct {
		name  string
		v3Err error
		ready []ModelID
	}{
		{name: "v3 files changed", v3Err: errors.New("installed file changed: encoder.int8.onnx"), ready: []ModelID{ModelParakeetUltraInt8}},
		{name: "Ultra not ready", v3Err: fmt.Errorf("model is not installed/verified: %w", fs.ErrNotExist)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubModelReady(t, c.ready...)
			processing, log, resolved := standInBuild(t, func(id ModelID, _ string) (ModelPaths, error) {
				if id == ModelParakeetV3Int8 {
					return ModelPaths{}, c.v3Err
				}
				return ModelPaths{ModelType: "stub", SampleRate: 16000}, nil
			})
			if !reflect.DeepEqual(resolved, []string{"parakeet-tdt-0.6b-v3-int8@" + v3Int8Revision}) {
				t.Fatalf("resolved %v", resolved)
			}
			if got := processing.Transcription; got.Status != "skipped" || got.Reason != "model_unavailable" || got.StandIn != nil {
				t.Fatalf("transcription %+v", got)
			}
			if strings.Contains(log, "is gone") {
				t.Fatalf("log:\n%s", log)
			}
		})
	}
}
