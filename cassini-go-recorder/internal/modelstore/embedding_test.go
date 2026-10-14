package modelstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// fixtureEmbedder adds a three-file embedding model to the fixture catalogue.
func fixtureEmbedder(t *testing.T, s *Store, data map[string][]byte) Model {
	t.Helper()
	m := Model{ID: "test-embedder", Kind: KindEmbedding}
	for _, name := range []string{"model_quantized.onnx", "model_quantized.onnx_data", EmbeddingTokenizerFile} {
		raw := bytes.Repeat([]byte(name+"-bytes "), 500)
		encoder, err := zstd.NewWriter(nil)
		if err != nil {
			t.Fatal(err)
		}
		encoded := encoder.EncodeAll(raw, nil)
		encoder.Close()
		hash := fmt.Sprintf("%x", sha256.Sum256(encoded))
		key := "models/files/" + hash + "/" + name + ".zst"
		s.Catalogue.Artifacts = append(s.Catalogue.Artifacts, Artifact{Key: key, URL: "https://dist.gocassini.com/" + key, Size: int64(len(encoded)), SHA256: hash, UncompressedSize: int64(len(raw)), UncompressedSHA256: fmt.Sprintf("%x", sha256.Sum256(raw)), Encoding: "zstd-seekable"})
		m.Files = append(m.Files, File{Path: name, Artifact: key, Required: true})
		data[key] = encoded
	}
	m.Revision = fmt.Sprintf("%x", sha256.Sum256([]byte(m.ID)))
	s.Catalogue.Models = append(s.Catalogue.Models, m)
	return m
}

// The shipped entry: three files with the names the graph and the tokenizer
// loader expect, and a revision any maintainer can recompute from the
// decompressed files with `sha256sum <files in this order> | sha256sum`.
func TestShippedEmbeddingModel(t *testing.T) {
	c := Shipped()
	m, err := c.Model("embeddinggemma-2-text-int8", "")
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != KindEmbedding || m.License != "Apache-2.0" || m.VADRevision != "" {
		t.Fatalf("model = %+v", m)
	}
	files, err := embeddingFiles(m)
	if err != nil || files != (EmbeddingFiles{"model_quantized.onnx", "model_quantized.onnx_data", "tokenizer.model"}) {
		t.Fatalf("files = %+v, %v", files, err)
	}
	var sums strings.Builder
	for _, f := range m.Files {
		a, err := c.Artifact(f.Artifact)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&sums, "%s  %s\n", a.UncompressedSHA256, f.Path)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(sums.String()))); got != m.Revision {
		t.Fatalf("revision %s, the files' checksum list hashes to %s", m.Revision, got)
	}
	s := &Store{Catalogue: c}
	if got := s.Components(m); len(got) != 1 || got[0].ID != m.ID {
		t.Fatalf("components = %+v, want the model alone", got)
	}
	if !strings.Contains(Notices, "EmbeddingGemma 2") || !strings.Contains(Notices, "daa72c51243991dfcaf9f9137d2c573d8f7790c0") || !strings.Contains(Notices, "914f7f89142e33e77833254d9c9b90c3cef7303b") {
		t.Fatal("NOTICE.txt does not name the EmbeddingGemma 2 sources")
	}
}

func TestValidateEmbeddingModels(t *testing.T) {
	cases := map[string]func(m *Model, c *Catalogue){
		"pins a VAD":          func(m *Model, c *Catalogue) { m.VADRevision = c.Models[1].Revision },
		"no data file":        func(m *Model, c *Catalogue) { m.Files = m.Files[:1:1] },
		"no tokenizer":        func(m *Model, c *Catalogue) { m.Files = m.Files[:2:2] },
		"renamed data file":   func(m *Model, c *Catalogue) { m.Files[1].Path = "weights.bin" },
		"optional file":       func(m *Model, c *Catalogue) { m.Files[2].Required = false },
		"nested name":         func(m *Model, c *Catalogue) { m.Files[2].Path = "sub/tokenizer.model" },
		"foreign origin":      func(m *Model, c *Catalogue) { c.Artifacts[len(c.Artifacts)-1].URL = "https://example.com/tokenizer.model.zst" },
		"plain zstd encoding": func(m *Model, c *Catalogue) { c.Artifacts[len(c.Artifacts)-1].Encoding = "zstd" },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			s, _, data := fixture(t)
			fixtureEmbedder(t, s, data)
			if err := s.Catalogue.Validate(); err != nil {
				t.Fatalf("fixture invalid: %v", err)
			}
			last := &s.Catalogue.Models[len(s.Catalogue.Models)-1]
			last.Files = append([]File(nil), last.Files...)
			breakIt(last, &s.Catalogue)
			if err := s.Catalogue.Validate(); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// An embedding model installs, packs and imports on its own, like a diarizer,
// and InstalledEmbedder hands back its three files.
func TestEmbedderInstallPackImport(t *testing.T) {
	s, _, data := fixture(t)
	m := fixtureEmbedder(t, s, data)
	var calls atomic.Int32
	server := serve(t, s, data, &calls)
	ctx := context.Background()
	if _, err := s.InstalledEmbedder(m); err == nil {
		t.Fatal("an absent model was reported installed")
	}
	if err := s.Acquire(ctx, m); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("requests=%d, want the three files", calls.Load())
	}
	files, err := s.InstalledEmbedder(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{files.Graph, files.Data, files.Tokenizer} {
		if filepath.Dir(path) != s.Dir(m) {
			t.Fatalf("%s is outside %s", path, s.Dir(m))
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
	if filepath.Base(files.Data) != filepath.Base(files.Graph)+"_data" {
		t.Fatalf("files = %+v", files)
	}
	server.Close()

	pack := filepath.Join(t.TempDir(), "embedder.tar")
	if err := s.Pack(ctx, m, pack); err != nil {
		t.Fatal(err)
	}
	target := New(t.TempDir())
	target.Catalogue = s.Catalogue
	target.URL = func(Artifact) string { t.Fatal("offline import constructed network request"); return "" }
	if err := target.Import(ctx, m, ImportOptions{From: pack, Pack: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := target.InstalledEmbedder(m); err != nil {
		t.Fatal(err)
	}
	// A directory of the three copied files imports too.
	fromDir := New(t.TempDir())
	fromDir.Catalogue = s.Catalogue
	if err := fromDir.Import(ctx, m, ImportOptions{From: s.Dir(m)}); err != nil {
		t.Fatal(err)
	}
	if _, err := fromDir.InstalledEmbedder(m); err != nil {
		t.Fatal(err)
	}
	// A changed file is no longer an installed model.
	if err := os.WriteFile(files.Tokenizer, []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InstalledEmbedder(m); err == nil {
		t.Fatal("a changed tokenizer was accepted")
	}
	speech, _ := s.Catalogue.Model("test-model", "")
	if _, err := s.InstalledEmbedder(speech); err == nil {
		t.Fatal("a speech model was accepted as an embedder")
	}
}
