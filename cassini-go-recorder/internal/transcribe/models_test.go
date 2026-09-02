package transcribe

import (
	"archive/tar"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnsureModelPrefersTheBundledRoot(t *testing.T) {
	// The image bakes the model of its default tier. EnsureModel must read it
	// from there and never write to that directory, so the writable cache on
	// the persistent volume only receives tiers the image does not carry
	// (D-704).
	bundledRoot := t.TempDir()
	cacheRoot := t.TempDir()
	t.Setenv(envBundledModelRoot, bundledRoot)
	t.Setenv(envBundledModels, string(ModelParakeet06BV3Int8))

	dir := filepath.Join(bundledRoot, "models", string(ModelParakeet06BV3Int8))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range RequiredModelFileNames(ModelParakeet06BV3Int8) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	paths, err := EnsureModel(cacheRoot, ModelParakeet06BV3Int8, io.Discard)
	if err != nil {
		t.Fatalf("EnsureModel() error = %v", err)
	}
	if !strings.HasPrefix(paths.EncoderFile, bundledRoot) {
		t.Errorf("encoder path = %q, want it under the bundled root %q", paths.EncoderFile, bundledRoot)
	}
	if entries, err := os.ReadDir(cacheRoot); err != nil || len(entries) != 0 {
		t.Errorf("writable cache = %v (err %v), want it untouched", entries, err)
	}
}

func TestEnsureModelRefusesToRepairADeclaredBundledModel(t *testing.T) {
	// A model the image claims to bake, but does not have, is a broken image.
	// Downloading over the top would hide that, so EnsureModel fails and says
	// to rebuild.
	bundledRoot := t.TempDir()
	t.Setenv(envBundledModelRoot, bundledRoot)
	t.Setenv(envBundledModels, string(ModelParakeet06BV3Int8))

	_, err := EnsureModel(t.TempDir(), ModelParakeet06BV3Int8, io.Discard)
	if err == nil {
		t.Fatal("EnsureModel() error = nil, want a broken-image error")
	}
	if !strings.Contains(err.Error(), "rebuild the image") {
		t.Errorf("error = %v, want advice to rebuild the image", err)
	}
}

func TestSafeExtractPathRejectsEscapes(t *testing.T) {
	// A tar entry names its own path. The container runs as root, so an entry
	// that climbs out of the cache would write anywhere on the host.
	dest := t.TempDir()
	for _, name := range []string{
		"root/../../etc/cron.d/evil",
		"root/../../../outside",
		"/absolute/path",
		"./root/../../escape",
	} {
		if _, err := safeExtractPath(dest, name); err == nil {
			t.Errorf("safeExtractPath(%q) = nil error, want a refusal", name)
		}
	}

	// The ordinary shapes still work, with the archive's top directory stripped.
	for name, want := range map[string]string{
		"sherpa-onnx-model/encoder.onnx": "encoder.onnx",
		"./sherpa-onnx-model/tokens.txt": "tokens.txt",
		"model/test_wavs/sample.wav":     filepath.Join("test_wavs", "sample.wav"),
	} {
		got, err := safeExtractPath(dest, name)
		if err != nil {
			t.Errorf("safeExtractPath(%q) error = %v", name, err)
			continue
		}
		if got != filepath.Join(dest, want) {
			t.Errorf("safeExtractPath(%q) = %q, want %q", name, got, filepath.Join(dest, want))
		}
	}
}

func TestEnsureModelIgnoresAnIncompleteCacheDirectory(t *testing.T) {
	// An interrupted download leaves files that exist. The cache is persistent,
	// so accepting them would break every later build on that host. Without the
	// completion marker the directory does not count, and EnsureModel goes on
	// to download (which fails here, with no network in the test).
	cacheRoot := t.TempDir()
	dir := filepath.Join(cacheRoot, "models", string(ModelParakeet06BV3Int8))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range RequiredModelFileNames(ModelParakeet06BV3Int8) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("truncated"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CASSINI_DISALLOW_MODEL_DOWNLOAD", "1")

	if _, err := EnsureModel(cacheRoot, ModelParakeet06BV3Int8, io.Discard); err == nil {
		t.Fatal("EnsureModel() accepted a directory with no completion marker")
	}

	// With the marker, and non-empty files, the same directory is usable.
	if err := os.WriteFile(filepath.Join(dir, completionMarker), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureModel(cacheRoot, ModelParakeet06BV3Int8, io.Discard); err != nil {
		t.Fatalf("EnsureModel() error = %v, want the completed cache to be used", err)
	}
}

func TestEnsureModelRejectsAnEmptyCachedFile(t *testing.T) {
	// A zero-length file is what a download that died mid-write leaves behind.
	cacheRoot := t.TempDir()
	dir := filepath.Join(cacheRoot, "models", string(ModelParakeet06BV3Int8))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range RequiredModelFileNames(ModelParakeet06BV3Int8) {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, completionMarker), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CASSINI_DISALLOW_MODEL_DOWNLOAD", "1")

	if _, err := EnsureModel(cacheRoot, ModelParakeet06BV3Int8, io.Discard); err == nil {
		t.Fatal("EnsureModel() accepted empty model files")
	}
}

// serveModelArchive builds a .tar.bz2 with one top-level directory and the
// given files, and serves it. It needs the bzip2 command because the standard
// library only decompresses.
// serveCountingModelArchive is serveModelArchive plus a request counter and a
// gate that holds every request until the test releases it. Counting is what
// proves serialization: without it a concurrency test passes even when every
// caller downloads its own copy.
func serveCountingModelArchive(t *testing.T, files map[string]string, gate <-chan struct{}) (url string, requests *atomic.Int32) {
	t.Helper()
	requests = &atomic.Int32{}
	base := serveModelArchive(t, files)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		<-gate
		resp, err := http.Get(base)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Length", resp.Header.Get("Content-Length"))
		io.Copy(w, resp.Body)
	}))
	t.Cleanup(proxy.Close)
	return proxy.URL + "/model.tar.bz2", requests
}

func serveModelArchive(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("bzip2"); err != nil {
		t.Skip("bzip2 is not installed")
	}
	var plain bytes.Buffer
	tw := tar.NewWriter(&plain)
	for name, body := range files {
		hdr := &tar.Header{Name: "model-top-dir/" + name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bzip2", "-c")
	cmd.Stdin = &plain
	compressed, err := cmd.Output()
	if err != nil {
		t.Fatalf("bzip2: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(compressed)))
		w.Write(compressed)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/model.tar.bz2"
}

func TestEnsureModelReplacesTheRemainsOfAnInterruptedDownload(t *testing.T) {
	// Rename refuses a destination that exists and is not empty. Without
	// clearing it, one interrupted download would block that tier on that host
	// forever, and the cache is persistent.
	const id = ModelParakeet110M
	required := RequiredModelFileNames(id)
	files := map[string]string{}
	for _, name := range required {
		files[name] = "real content for " + name
	}

	spec := knownModels[id]
	original := spec
	spec.URL = serveModelArchive(t, files)
	knownModels[id] = spec
	t.Cleanup(func() { knownModels[id] = original })

	cacheRoot := t.TempDir()
	stale := filepath.Join(cacheRoot, "models", string(id))
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	// The shape an interrupted download leaves: the files exist, one is
	// truncated, and no marker was ever written.
	for _, name := range required {
		if err := os.WriteFile(filepath.Join(stale, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	paths, err := EnsureModel(cacheRoot, id, io.Discard)
	if err != nil {
		t.Fatalf("EnsureModel() error = %v, want the stale directory replaced", err)
	}
	if !strings.HasPrefix(paths.ModelFile, stale) {
		t.Fatalf("model file = %q, want it inside %q", paths.ModelFile, stale)
	}
	if !modelDirComplete(stale) {
		t.Error("the promoted directory has no completion marker")
	}
	body, err := os.ReadFile(paths.ModelFile)
	if err != nil || len(body) == 0 {
		t.Fatalf("promoted model file is empty or unreadable: %v", err)
	}

	// No staging directory survives a success.
	entries, err := os.ReadDir(filepath.Join(cacheRoot, "models"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".staging-") {
			t.Errorf("staging directory %s was left behind", e.Name())
		}
	}
}

func TestEnsureModelRejectsAnArchiveThatEscapesTheCache(t *testing.T) {
	// The extractor must refuse a hostile entry, and must leave nothing behind.
	const id = ModelParakeet110M
	spec := knownModels[id]
	original := spec
	spec.URL = serveModelArchive(t, map[string]string{"../../escaped.onnx": "x"})
	knownModels[id] = spec
	t.Cleanup(func() { knownModels[id] = original })

	cacheRoot := t.TempDir()
	_, err := EnsureModel(cacheRoot, id, io.Discard)
	if err == nil {
		t.Fatal("EnsureModel() accepted an archive that writes outside the cache")
	}
	// Assert the refusal, not merely any error: "required file missing" would
	// also fail here, and would hide a containment hole.
	if !strings.Contains(err.Error(), "escapes the destination") {
		t.Fatalf("error = %v, want the extractor to refuse the entry", err)
	}
	// The entry is ../../escaped.onnx relative to <cache>/models/<id>, so the
	// vulnerable output is <cache>/escaped.onnx.
	if _, err := os.Stat(filepath.Join(cacheRoot, "escaped.onnx")); err == nil {
		t.Fatal("the archive wrote outside the model directory")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(cacheRoot), "escaped.onnx")); err == nil {
		t.Fatal("the archive wrote outside the cache root")
	}
}

func TestEnsureModelClearsStagingLeftByAKilledDownload(t *testing.T) {
	// SIGKILL skips every deferred cleanup, so a staging directory of several
	// gigabytes can outlive the process on a persistent volume. The next holder
	// of the model lock removes it.
	const id = ModelParakeet110M
	required := RequiredModelFileNames(id)
	files := map[string]string{}
	for _, name := range required {
		files[name] = "content of " + name
	}
	spec := knownModels[id]
	original := spec
	spec.URL = serveModelArchive(t, files)
	knownModels[id] = spec
	t.Cleanup(func() { knownModels[id] = original })

	cacheRoot := t.TempDir()
	modelsDir := filepath.Join(cacheRoot, "models")
	orphan := filepath.Join(modelsDir, ".staging-"+string(id)+"-abandoned")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "half-written.onnx"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureModel(cacheRoot, id, io.Discard); err != nil {
		t.Fatalf("EnsureModel() error = %v", err)
	}
	if _, err := os.Stat(orphan); err == nil {
		t.Error("the abandoned staging directory survived the next download")
	}
}

func TestEnsureModelSerializesConcurrentDownloads(t *testing.T) {
	// Two writers must not both judge the destination invalid: the loser would
	// remove the directory the winner promoted, while a reader loads from it.
	// The server counts requests and holds the first, so this proves the other
	// callers waited for the lock and then used the finished model, rather than
	// each fetching their own copy.
	const id = ModelParakeet110M
	required := RequiredModelFileNames(id)
	files := map[string]string{}
	for _, name := range required {
		files[name] = "content of " + name
	}
	gate := make(chan struct{})
	url, requests := serveCountingModelArchive(t, files, gate)

	spec := knownModels[id]
	original := spec
	spec.URL = url
	knownModels[id] = spec
	t.Cleanup(func() { knownModels[id] = original })

	cacheRoot := t.TempDir()
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			_, errs[slot] = EnsureModel(cacheRoot, id, io.Discard)
		}(i)
	}

	// Give every caller time to reach the lock, then let the winner finish.
	time.Sleep(500 * time.Millisecond)
	close(gate)
	wg.Wait()

	for slot, err := range errs {
		if err != nil {
			t.Errorf("concurrent EnsureModel %d error = %v", slot, err)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("archive was requested %d times, want exactly 1", got)
	}
	if !modelDirValid(filepath.Join(cacheRoot, "models", string(id)), required) {
		t.Fatal("the model directory is not valid after concurrent downloads")
	}
}
func TestEnsureModelRejectsADirectoryNamedLikeTheModelFile(t *testing.T) {
	// An archive entry of "model.int8.onnx/child" makes the extractor create a
	// directory with the name of the required file. A size test alone accepts
	// it, and the cache is persistent, so it would poison that tier for good.
	const id = ModelParakeet110M
	spec := knownModels[id]
	original := spec
	spec.URL = serveModelArchive(t, map[string]string{
		"model.int8.onnx/child": "not the model",
		"tokens.txt":            "tokens",
	})
	knownModels[id] = spec
	t.Cleanup(func() { knownModels[id] = original })

	cacheRoot := t.TempDir()
	_, err := EnsureModel(cacheRoot, id, io.Discard)
	if err == nil {
		t.Fatal("EnsureModel() accepted a directory in place of the model file")
	}
	if !strings.Contains(err.Error(), "not a file") {
		t.Fatalf("error = %v, want a refusal that names the wrong file type", err)
	}
	if modelDirComplete(filepath.Join(cacheRoot, "models", string(id))) {
		t.Error("a rejected download was promoted anyway")
	}
}
