package transcribe

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
