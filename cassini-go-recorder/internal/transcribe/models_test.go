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
