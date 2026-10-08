package transcribe

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gocassini/internal/modelstore"
)

// fakeInstallShipped publishes a shipped catalogue model the way the store
// does, without its bytes: sparse files of the catalogue sizes and the
// installation receipt. Installed() checks sizes and the receipt; the hashes
// were checked at publication, which this skips.
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
	type stamp struct {
		Size  int64 `json:"size"`
		Mtime int64 `json:"mtime_ns"`
	}
	files := map[string]stamp{}
	for _, f := range m.Files {
		a, err := s.Catalogue.Artifact(f.Artifact)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, f.Path)
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(a.UncompressedSize); err != nil {
			t.Fatal(err)
		}
		file.Close()
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		files[f.Path] = stamp{st.Size(), st.ModTime().UnixNano()}
	}
	receipt, _ := json.Marshal(map[string]any{"model": m.ID, "revision": m.Revision, "files": files})
	if err := os.WriteFile(filepath.Join(dir, ".cassini-model.json"), receipt, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Installed(m); err != nil {
		t.Fatalf("fake install not accepted: %v", err)
	}
	return m
}

func TestInstalledDiarizerReadsTheModelStore(t *testing.T) {
	root := t.TempDir()
	_, err := InstalledDiarizer(root, DefaultDiarizationModelID, "")
	if !errors.Is(err, ErrDiarizationUnavailable) || !strings.Contains(err.Error(), "Settings") || !strings.Contains(err.Error(), "cassini models install "+DefaultDiarizationModelID) {
		t.Fatalf("nothing installed: err = %v, want unavailable with how to install it", err)
	}
	if _, err := InstalledDiarizer(root, string(ModelParakeet110M), ""); err == nil || errors.Is(err, ErrDiarizationUnavailable) {
		t.Fatalf("a speech model as diarizer: err = %v", err)
	}

	m := fakeInstallShipped(t, root, DefaultDiarizationModelID)
	got, err := InstalledDiarizer(root, DefaultDiarizationModelID, "")
	if err != nil {
		t.Fatal(err)
	}
	want := DiarizationModel{
		Path:   filepath.Join(root, "models", DefaultDiarizationModelID, m.Revision, "model.int8.onnx"),
		Name:   "nvidia/Nemotron-3-Diarization INT8",
		SHA256: "47c221ea9b4d4e7f6c108bd098e769bc706cdc986c29f6133c93cae335fe6779",
	}
	if got != want {
		t.Fatalf("InstalledDiarizer = %+v, want %+v", got, want)
	}

	// A changed file is not the verified model any more.
	if err := os.WriteFile(want.Path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstalledDiarizer(root, DefaultDiarizationModelID, ""); !errors.Is(err, ErrDiarizationUnavailable) {
		t.Fatalf("changed file: err = %v", err)
	}
}

func TestResolveDiarizationModelUsesTheStore(t *testing.T) {
	if !HasDiarizationRuntime() {
		t.Skip("native runtime without Nemotron diarization")
	}
	t.Setenv(DiarizationModelEnv, "")
	root := t.TempDir()

	// The proof of concept's loose file is not an installed model.
	legacy := filepath.Join(root, "models", DefaultDiarizationModelID, "model.int8.onnx")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("onnx"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveDiarizationModel(root); !errors.Is(err, ErrDiarizationUnavailable) || !strings.Contains(err.Error(), DefaultDiarizationModelID) {
		t.Fatalf("empty store: err = %v", err)
	}

	// Only the fp32 export installed: that is the one used.
	fp32 := fakeInstallShipped(t, root, "nemotron-3-diarization")
	got, err := ResolveDiarizationModel(root)
	if err != nil || filepath.Dir(got.Path) != filepath.Join(root, "models", fp32.ID, fp32.Revision) || got.Name != "nvidia/Nemotron-3-Diarization" {
		t.Fatalf("fp32 only: %+v, %v", got, err)
	}

	// With both, the int8 export Settings installs wins.
	fakeInstallShipped(t, root, DefaultDiarizationModelID)
	got, err = ResolveDiarizationModel(root)
	if err != nil || got.SHA256 != "47c221ea9b4d4e7f6c108bd098e769bc706cdc986c29f6133c93cae335fe6779" {
		t.Fatalf("int8 installed: %+v, %v", got, err)
	}

	// The development override still wins over the store.
	explicit := filepath.Join(t.TempDir(), "dev.onnx")
	if err := os.WriteFile(explicit, []byte("dev"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(DiarizationModelEnv, explicit)
	if got, err = ResolveDiarizationModel(root); err != nil || got.Path != explicit {
		t.Fatalf("override: %+v, %v", got, err)
	}
}
