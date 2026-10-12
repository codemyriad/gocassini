package cassini

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gocassini/internal/modelstore"
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
	return &asr, &diar
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
