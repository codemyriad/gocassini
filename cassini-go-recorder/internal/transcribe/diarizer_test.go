package transcribe

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// An explicit model path (`cassini speakers diarize --model`) gets the same
// checks and the same provenance as the resolved one.
func TestLoadDiarizationModelDescribesAnExplicitFile(t *testing.T) {
	if !HasDiarizationRuntime() {
		t.Skip("native runtime without Nemotron diarization")
	}
	dir := t.TempDir()
	if _, err := LoadDiarizationModel(filepath.Join(dir, "missing.onnx")); !errors.Is(err, ErrDiarizationUnavailable) {
		t.Fatalf("missing file: err = %v, want ErrDiarizationUnavailable", err)
	}
	path := filepath.Join(dir, "model.int8.onnx")
	if err := os.WriteFile(path, []byte("not really a model"), 0o644); err != nil {
		t.Fatal(err)
	}
	model, err := LoadDiarizationModel(path)
	if err != nil {
		t.Fatalf("LoadDiarizationModel: %v", err)
	}
	sum := sha256.Sum256([]byte("not really a model"))
	if model.Path != path || model.Name != "nvidia/Nemotron-3-Diarization INT8" || model.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("model = %+v", model)
	}
	t.Setenv(DiarizationModelEnv, path)
	resolved, err := ResolveDiarizationModel("")
	if err != nil || resolved != model {
		t.Errorf("ResolveDiarizationModel = %+v, %v; want the same description %+v", resolved, err, model)
	}
}
