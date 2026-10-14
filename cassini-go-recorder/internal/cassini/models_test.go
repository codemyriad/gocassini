package cassini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"gocassini/internal/modelstore"
	"gocassini/internal/topics"
	"gocassini/internal/transcribe"
)

// fakeInstallShipped publishes a shipped model's receipt over sparse files of
// the catalogue sizes, which is all Installed() checks.
func fakeInstallShipped(t *testing.T, root, id string) modelstore.Model {
	t.Helper()
	m, err := modelstore.Shipped().Model(id, "")
	if err != nil {
		t.Fatal(err)
	}
	return fakeInstall(t, root, m)
}

// fakeInstallRetired does the same for a retired model, as an install upgraded
// from a version that shipped it still has it.
func fakeInstallRetired(t *testing.T, root, id string) modelstore.Model {
	t.Helper()
	m, err := modelstore.Shipped().RetiredModel(id, "")
	if err != nil {
		t.Fatal(err)
	}
	return fakeInstall(t, root, m)
}

func fakeInstall(t *testing.T, root string, m modelstore.Model) modelstore.Model {
	t.Helper()
	s := modelstore.New(root)
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

// Parakeet Ultra replaces v3. Its files go only after Ultra passes its runtime
// check: an upgrade whose new model fails to run must not also lose the old
// files, and one that succeeds reclaims them (about 2.5 GB for v3 fp32).
func TestModelsProbeRemovesTheReplacedModelOnlyAfterItRuns(t *testing.T) {
	stubModelRuntime(t, true)
	root := t.TempDir()
	for _, id := range []string{"silero-vad", "parakeet-ultra-0.6b-int8"} {
		fakeInstallShipped(t, root, id)
	}
	retired := filepath.Join(root, "models", "parakeet-tdt-0.6b-v3-int8")
	if err := os.MkdirAll(filepath.Join(retired, strings.Repeat("a", 64)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(retired, "encoder.int8.onnx"), []byte("old archive layout"), 0o644); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(root, "models", "parakeet-tdt-0.6b-v3")
	if err := os.MkdirAll(unrelated, 0o755); err != nil {
		t.Fatal(err)
	}

	if row := listModels(t, root, "cpu")["parakeet-ultra-0.6b-int8"]; len(row.Replaces) != 1 || row.Replaces[0] != "parakeet-tdt-0.6b-v3-int8" {
		t.Fatalf("listed replaces = %v", row.Replaces)
	}

	failing := probeInstalledModelFn
	probeInstalledModelFn = func(context.Context, *modelstore.Store, modelstore.Model, string) error {
		return os.ErrInvalid
	}
	var stdout, stderr bytes.Buffer
	if code := runModels(context.Background(), []string{"probe", "parakeet-ultra-0.6b-int8", "--cache-root", root}, &stdout, &stderr); code == 0 {
		t.Fatal("failing probe succeeded")
	}
	if _, err := os.Stat(retired); err != nil {
		t.Fatalf("a failed runtime check removed the replaced model: %v", err)
	}

	probeInstalledModelFn = failing
	stderr.Reset()
	if code := runModels(context.Background(), []string{"probe", "parakeet-ultra-0.6b-int8", "--cache-root", root}, &stdout, &stderr); code != 0 {
		t.Fatalf("probe = %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(retired); !os.IsNotExist(err) {
		t.Fatalf("replaced model still present: %v", err)
	}
	if !strings.Contains(stderr.String(), "removed parakeet-tdt-0.6b-v3-int8") {
		t.Fatalf("stderr: %s", stderr.String())
	}
	// fp32 v3 is replaced by fp32 Ultra, not by this model.
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("removed a model the probed one does not replace: %v", err)
	}
}

// An install upgraded from a version that shipped Parakeet v3 still has its
// files until Ultra passes its runtime check. The inventory lists such a
// model as its own row, marked with the model that replaced it, so the
// operator can stand in with it; an install without those files lists
// exactly what it did before.
func TestModelsListShowsAnInstalledRetiredModel(t *testing.T) {
	stubModelRuntime(t, true)
	root := t.TempDir()
	fakeInstallShipped(t, root, "silero-vad")
	fresh := listModels(t, root, "cpu")
	for id := range fresh {
		if strings.HasPrefix(id, "parakeet-tdt-0.6b-v3") {
			t.Fatalf("an install without v3 lists %s", id)
		}
	}

	fakeInstallRetired(t, root, "parakeet-tdt-0.6b-v3-int8")
	fakeInstallRetired(t, root, "parakeet-tdt-0.6b-v3")
	views := listModels(t, root, "cpu")
	if len(views) != len(fresh)+2 {
		t.Fatalf("rows %d, want %d", len(views), len(fresh)+2)
	}
	v3 := views["parakeet-tdt-0.6b-v3-int8"]
	if v3.ReplacedBy != "parakeet-ultra-0.6b-int8" || !v3.Installed || v3.Ready || v3.Revision != "5793d0fd397c5778d2cf2126994d58e9d56b1be7c04d13c7a15bb1b4eafb16bf" || v3.Kind != modelstore.KindSpeech {
		t.Fatalf("retired row = %+v", v3)
	}
	if views["parakeet-ultra-0.6b-int8"].ReplacedBy != "" {
		t.Fatal("a shipped model is marked as replaced")
	}

	// Ready once it has passed its runtime check here.
	probeInstalledModelFn = func(_ context.Context, s *modelstore.Store, m modelstore.Model, device string) error {
		return s.MarkReady(m, device, transcribe.ModelRuntimeFingerprint())
	}
	var stdout, stderr bytes.Buffer
	if code := runModels(context.Background(), []string{"probe", "parakeet-tdt-0.6b-v3-int8", "--cache-root", root}, &stdout, &stderr); code != 0 {
		t.Fatalf("probe = %d: %s", code, stderr.String())
	}
	if v3 = listModels(t, root, "cpu")["parakeet-tdt-0.6b-v3-int8"]; !v3.Ready {
		t.Fatalf("checked retired row = %+v", v3)
	}

	// CUDA runs only fp32, so only fp32 v3 can stand in there.
	cuda := listModels(t, root, "cuda")
	if _, ok := cuda["parakeet-tdt-0.6b-v3-int8"]; ok {
		t.Fatal("v3 int8 is listed for CUDA")
	}
	if cuda["parakeet-tdt-0.6b-v3"].ReplacedBy != "parakeet-ultra-0.6b" {
		t.Fatalf("v3 fp32 on CUDA = %+v", cuda["parakeet-tdt-0.6b-v3"])
	}

	stdout.Reset()
	if code := runModels(context.Background(), []string{"list", "--cache-root", root}, &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	if !strings.Contains(stdout.String(), "(retired; stands in for parakeet-ultra-0.6b-int8 until it is ready)") {
		t.Fatalf("text listing:\n%s", stdout.String())
	}
}

// A retired model's files can be checked, so a stand-in runs only where it
// works, but nothing installs, imports or packs one: no v3 download ever
// starts.
func TestModelsProbeChecksARetiredModelButNeverInstallsIt(t *testing.T) {
	asr, _ := stubModelRuntime(t, true)
	root := t.TempDir()
	fakeInstallShipped(t, root, "silero-vad")
	fakeInstallRetired(t, root, "parakeet-tdt-0.6b-v3-int8")

	var stdout, stderr bytes.Buffer
	if code := runModels(context.Background(), []string{"probe", "parakeet-tdt-0.6b-v3-int8", "--cache-root", root}, &stdout, &stderr); code != 0 || *asr != 1 {
		t.Fatalf("probe = %d (%d checks): %s", code, *asr, stderr.String())
	}
	for _, args := range [][]string{
		{"install", "parakeet-tdt-0.6b-v3-int8"},
		{"install", "parakeet-tdt-0.6b-v3", "--device", "cuda"},
		{"import", "parakeet-tdt-0.6b-v3-int8", "--from", root, "--revision", "5793d0fd397c5778d2cf2126994d58e9d56b1be7c04d13c7a15bb1b4eafb16bf"},
		{"pack", "parakeet-tdt-0.6b-v3-int8", "--out", filepath.Join(t.TempDir(), "v3.pack")},
	} {
		stderr.Reset()
		if code := runModels(context.Background(), append(args, "--cache-root", root), &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "is retired and is never installed") {
			t.Fatalf("%v = %d: %s", args, code, stderr.String())
		}
	}
	if _, err := os.Stat(filepath.Join(root, "downloads")); !os.IsNotExist(err) {
		t.Fatalf("a download started: %v", err)
	}
}

// The operator checks v3 before it stands in while Ultra downloads, and the
// download holds the store lock until it is done. v3 replaces nothing, so its
// check returns at once instead of waiting for that lock.
func TestRetiredModelProbeDoesNotWaitForADownload(t *testing.T) {
	stubModelRuntime(t, true)
	root := t.TempDir()
	fakeInstallShipped(t, root, "silero-vad")
	fakeInstallRetired(t, root, "parakeet-tdt-0.6b-v3-int8")

	lock, err := os.OpenFile(filepath.Join(root, ".models.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	var stdout, stderr bytes.Buffer
	if code := runModels(ctx, []string{"probe", "parakeet-tdt-0.6b-v3-int8", "--cache-root", root}, &stdout, &stderr); code != 0 || strings.Contains(stderr.String(), "warning") {
		t.Fatalf("probe while a download held the store lock = %d: %s", code, stderr.String())
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("probe waited %s for the store lock", took)
	}
}

// A build holds the inference lock from resolving its model until its last
// recognizer is closed, and it may be standing in with the model Ultra
// replaces. The probe that checks Ultra removes that model's files only once
// it holds the lock itself, so it never pulls them from under such a build.
func TestReplacedModelIsRemovedOnlyUnderTheInferenceLock(t *testing.T) {
	stubModelRuntime(t, true)
	root := t.TempDir()
	fakeInstallShipped(t, root, "parakeet-ultra-0.6b-int8")
	fakeInstallShipped(t, root, "silero-vad")
	v3 := fakeInstallRetired(t, root, "parakeet-tdt-0.6b-v3-int8")

	unlock, err := transcribe.LockModelRuntime(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	var stderr bytes.Buffer
	go func() {
		var stdout bytes.Buffer
		done <- runModels(context.Background(), []string{"probe", "parakeet-ultra-0.6b-int8", "--cache-root", root}, &stdout, &stderr)
	}()
	select {
	case code := <-done:
		t.Fatalf("probe finished (%d) while a build held the inference lock: %s", code, stderr.String())
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := os.Stat(modelstore.New(root).Dir(v3)); err != nil {
		t.Fatalf("v3 removed while a build held the inference lock: %v", err)
	}
	unlock()
	if code := <-done; code != 0 {
		t.Fatalf("probe = %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, "models", v3.ID)); !os.IsNotExist(err) {
		t.Fatalf("v3 still installed: %v", err)
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
