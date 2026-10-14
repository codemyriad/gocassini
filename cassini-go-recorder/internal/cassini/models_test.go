package cassini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gocassini/internal/modelstore"
	"gocassini/internal/topics"
	"gocassini/internal/transcribe"
)

// fakeInstallShipped publishes a shipped model's receipt over sparse files of
// the catalogue sizes, which is all Installed() checks.
func fakeInstallShipped(t *testing.T, root, id string) modelstore.Model {
	t.Helper()
	s := modelstore.New(root)
	m, err := s.Catalogue.Model(id, "")
	if err != nil {
		t.Fatal(err)
	}
	dir := s.Dir(m)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]map[string]int64{}
	for _, f := range m.Files {
		a, _ := s.Catalogue.Artifact(f.Artifact)
		path := filepath.Join(dir, f.Path)
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(path, a.UncompressedSize); err != nil {
			t.Fatal(err)
		}
		st, _ := os.Stat(path)
		files[f.Path] = map[string]int64{"size": st.Size(), "mtime_ns": st.ModTime().UnixNano()}
	}
	receipt, _ := json.Marshal(map[string]any{"model": m.ID, "revision": m.Revision, "files": files})
	if err := os.WriteFile(filepath.Join(dir, ".cassini-model.json"), receipt, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Installed(m); err != nil {
		t.Fatal(err)
	}
	return m
}

func stubModelRuntime(t *testing.T, diarization bool) (asrProbes, diarizerProbes *int) {
	t.Helper()
	asr, diar := 0, 0
	prevRuntime, prevASR, prevDiar := diarizationRuntimeFn, probeInstalledModelFn, probeInstalledDiarizer
	diarizationRuntimeFn = func() bool { return diarization }
	probeInstalledModelFn = func(context.Context, *modelstore.Store, modelstore.Model, string) error { asr++; return nil }
	probeInstalledDiarizer = func(context.Context, *modelstore.Store, modelstore.Model) error { diar++; return nil }
	t.Cleanup(func() {
		diarizationRuntimeFn, probeInstalledModelFn, probeInstalledDiarizer = prevRuntime, prevASR, prevDiar
	})
	stubEmbeddingRuntime(t, true)
	return &asr, &diar
}

// stubEmbeddingRuntime replaces the ONNX Runtime check and the embedder probe.
func stubEmbeddingRuntime(t *testing.T, supported bool) (probes *int) {
	t.Helper()
	n := 0
	prevRuntime, prevProbe := embeddingRuntimeFn, probeInstalledEmbedder
	embeddingRuntimeFn = func() bool { return supported }
	probeInstalledEmbedder = func(context.Context, *modelstore.Store, modelstore.Model, int) error { n++; return nil }
	t.Cleanup(func() { embeddingRuntimeFn, probeInstalledEmbedder = prevRuntime, prevProbe })
	return &n
}

func listModels(t *testing.T, root, device string) map[string]modelView {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := runModels(context.Background(), []string{"list", "--json", "--cache-root", root, "--device", device}, &stdout, &stderr); code != 0 {
		t.Fatalf("models list = %d: %s", code, stderr.String())
	}
	var rows []modelView
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		t.Fatalf("%v: %s", err, stdout.String())
	}
	out := map[string]modelView{}
	for _, r := range rows {
		out[r.ID] = r
	}
	return out
}

func TestModelsListShowsTheDiarizerAsItsOwnKind(t *testing.T) {
	stubModelRuntime(t, true)
	root := t.TempDir()

	views := listModels(t, root, "cuda")
	if _, ok := views["silero-vad"]; ok {
		t.Fatal("the VAD is listed as a model")
	}
	d, ok := views[transcribe.DefaultDiarizationModelID]
	if !ok {
		t.Fatalf("no diarizer in %v", views)
	}
	// Its own download only (no VAD), on the CPU whatever device was asked.
	if d.Kind != modelstore.KindDiarization || d.DownloadBytes != 64915668 || d.InstalledBytes != 103967426 || d.Device != "cpu" || d.Installed || d.Ready || d.RuntimeSupported == nil || !*d.RuntimeSupported {
		t.Fatalf("diarizer row = %+v", d)
	}
	if p := views["parakeet-tdt-ctc-110m-en-int8"]; p.Kind != modelstore.KindSpeech || p.RuntimeSupported != nil || p.Device != "cuda" {
		t.Fatalf("speech row = %+v", p)
	}

	fakeInstallShipped(t, root, transcribe.DefaultDiarizationModelID)
	if d = listModels(t, root, "cpu")[transcribe.DefaultDiarizationModelID]; !d.Installed || !d.Ready {
		t.Fatalf("installed diarizer = %+v, want ready without a probe receipt", d)
	}

	// A runtime without Nemotron: installed, never ready, and the text says why.
	stubModelRuntime(t, false)
	if d = listModels(t, root, "cpu")[transcribe.DefaultDiarizationModelID]; !d.Installed || d.Ready || *d.RuntimeSupported {
		t.Fatalf("unsupported runtime = %+v", d)
	}
	var stdout, stderr bytes.Buffer
	if code := runModels(context.Background(), []string{"list", "--cache-root", root}, &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	if !strings.Contains(stdout.String(), transcribe.DefaultDiarizationModelID+"  diarization  ") || !strings.Contains(stdout.String(), "this runtime cannot run it") {
		t.Fatalf("text listing:\n%s", stdout.String())
	}
}

func TestModelsProbeChecksADiarizerWithTheDiarizationRuntime(t *testing.T) {
	asr, diar := stubModelRuntime(t, true)
	root := t.TempDir()
	m := fakeInstallShipped(t, root, transcribe.DefaultDiarizationModelID)

	var stdout, stderr bytes.Buffer
	if code := runModels(context.Background(), []string{"probe", m.ID, "--cache-root", root, "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("probe = %d: %s", code, stderr.String())
	}
	if *asr != 0 || *diar != 1 {
		t.Fatalf("probes: asr=%d diarizer=%d, want the diarizer's only", *asr, *diar)
	}
	if !strings.Contains(stdout.String(), `"ready":true`) {
		t.Fatalf("result: %s", stdout.String())
	}

	stderr.Reset()
	if code := runModels(context.Background(), []string{"install", m.ID, "--cache-root", root, "--device", "cuda"}, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "CPU only") {
		t.Fatalf("cuda install = %d: %s", code, stderr.String())
	}
	if code := runModels(context.Background(), []string{"install", "silero-vad", "--cache-root", root}, &stdout, &stderr); code == 0 {
		t.Fatal("the VAD was installable on its own")
	}
}

// The topic suggestions model is listed as its own kind, on the CPU, and is
// ready once its files are installed, the runtime check passes and its probe
// passed. The probe's receipt names neither the boot nor the image, so a
// reboot or an image upgrade (a new runtime fingerprint) leaves it ready while
// a speech model needs a new probe.
func TestModelsListEmbeddingModelReadyAcrossARuntimeChange(t *testing.T) {
	stubModelRuntime(t, true)
	root := t.TempDir()
	views := listModels(t, root, "cuda")
	e, ok := views[DefaultEmbeddingModelID]
	if !ok {
		t.Fatalf("no embedding model in %v", views)
	}
	if e.Kind != modelstore.KindEmbedding || e.DownloadBytes != 288733465 || e.InstalledBytes != 318909106 || e.Device != "cpu" || e.Installed || e.Ready || e.RuntimeSupported == nil || !*e.RuntimeSupported {
		t.Fatalf("embedding row = %+v", e)
	}

	m := fakeInstallShipped(t, root, DefaultEmbeddingModelID)
	speech := fakeInstallShipped(t, root, "parakeet-tdt-ctc-110m-en-int8")
	fakeInstallShipped(t, root, "silero-vad")
	s := modelstore.New(root)
	if e = listModels(t, root, "cpu")[m.ID]; !e.Installed || e.Ready {
		t.Fatalf("installed but never probed = %+v, want not ready", e)
	}
	if err := s.MarkReady(speech, "cpu", transcribe.ModelRuntimeFingerprint()); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkReady(m, "cpu", topics.ReadyFingerprint); err != nil {
		t.Fatal(err)
	}
	views = listModels(t, root, "cpu")
	if e = views[m.ID]; !e.Installed || !e.Ready {
		t.Fatalf("probed embedding model = %+v, want ready", e)
	}
	if !views[speech.ID].Ready {
		t.Fatal("the speech model's receipt did not count")
	}

	// A new native library changes the runtime fingerprint, as a reboot (a
	// new boot_id) or an image upgrade does.
	lib := t.TempDir()
	if err := os.WriteFile(filepath.Join(lib, "libonnxruntime.so"), []byte("new runtime"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CASSINI_LIB_DIR", lib)
	views = listModels(t, root, "cpu")
	if views[speech.ID].Ready {
		t.Fatal("the fingerprint did not change; the test proves nothing")
	}
	if e = views[m.ID]; !e.Ready {
		t.Fatalf("after a runtime change the embedding model = %+v, want still ready", e)
	}

	// A runtime the binding cannot load: installed, never ready.
	stubEmbeddingRuntime(t, false)
	if e = listModels(t, root, "cpu")[m.ID]; !e.Installed || e.Ready || *e.RuntimeSupported {
		t.Fatalf("unsupported runtime = %+v", e)
	}
}

// The probe is the embedding model's quality gate: a failing probe makes a
// ready model not ready, and a probe that only had to wait changes nothing.
func TestModelsProbeFailureTakesTheEmbeddingModelOutOfReady(t *testing.T) {
	stubModelRuntime(t, true)
	stubEmbeddingRuntime(t, true)
	root := t.TempDir()
	m := fakeInstallShipped(t, root, DefaultEmbeddingModelID)
	probe := func(err error) int {
		probeInstalledEmbedder = func(context.Context, *modelstore.Store, modelstore.Model, int) error { return err }
		var stdout, stderr bytes.Buffer
		return runModels(context.Background(), []string{"probe", m.ID, "--cache-root", root}, &stdout, &stderr)
	}
	ready := func() bool { return listModels(t, root, "cpu")[m.ID].Ready }
	if code := probe(nil); code != 0 || !ready() {
		t.Fatalf("passing probe: exit %d, ready %v", code, ready())
	}
	if code := probe(transcribe.ErrRuntimeCheckDeferred); code != exitTempFail || !ready() {
		t.Fatalf("deferred probe: exit %d, ready %v; waiting must not change readiness", code, ready())
	}
	if code := probe(fmt.Errorf("%w: probe text 0 has cosine 0.9", topics.ErrModelFault)); code == 0 || ready() {
		t.Fatalf("failing probe: exit %d, ready %v; want a failure and not ready", code, ready())
	}
	if code := probe(nil); code != 0 || !ready() {
		t.Fatalf("passing again: exit %d, ready %v", code, ready())
	}
}

func TestModelsProbeChecksTheEmbeddingModelWithTheEmbedderProbe(t *testing.T) {
	asr, diar := stubModelRuntime(t, true)
	embed := stubEmbeddingRuntime(t, true)
	root := t.TempDir()
	m := fakeInstallShipped(t, root, DefaultEmbeddingModelID)

	var stdout, stderr bytes.Buffer
	if code := runModels(context.Background(), []string{"probe", m.ID, "--cache-root", root, "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("probe = %d: %s", code, stderr.String())
	}
	if *asr != 0 || *diar != 0 || *embed != 1 {
		t.Fatalf("probes: asr=%d diarizer=%d embedder=%d, want the embedder's only", *asr, *diar, *embed)
	}
	if !strings.Contains(stdout.String(), `"ready":true`) {
		t.Fatalf("result: %s", stdout.String())
	}
	for _, command := range []string{"install", "probe"} {
		stderr.Reset()
		if code := runModels(context.Background(), []string{command, m.ID, "--cache-root", root, "--device", "cuda"}, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "CPU only") {
			t.Fatalf("cuda %s = %d: %s", command, code, stderr.String())
		}
	}
	if *embed != 1 {
		t.Fatalf("a refused cuda request ran the probe (%d)", *embed)
	}
}
