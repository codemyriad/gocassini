package modelstore

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

var timeZero = time.Time{}

func fixture(t *testing.T) (*Store, Model, map[string][]byte) {
	t.Helper()
	s := New(t.TempDir())
	s.Catalogue = Catalogue{SchemaVersion: 2}
	encoded := map[string][]byte{}
	for _, id := range []string{"test-model", "silero-vad"} {
		raw := bytes.Repeat([]byte(id+"-weights "), 1000)
		encoder, err := zstd.NewWriter(nil)
		if err != nil {
			t.Fatal(err)
		}
		data := encoder.EncodeAll(raw, nil)
		encoder.Close()
		hash := fmt.Sprintf("%x", sha256.Sum256(data))
		uh := fmt.Sprintf("%x", sha256.Sum256(raw))
		filename := id + ".onnx"
		a := Artifact{Key: "models/files/" + hash + "/" + filename + ".zst", URL: "https://dist.gocassini.com/models/files/" + hash + "/" + filename + ".zst", Size: int64(len(data)), SHA256: hash, UncompressedSize: int64(len(raw)), UncompressedSHA256: uh, Encoding: "zstd-seekable"}
		s.Catalogue.Artifacts = append(s.Catalogue.Artifacts, a)
		s.Catalogue.Models = append(s.Catalogue.Models, Model{ID: id, Revision: uh, Files: []File{{Path: filename, Artifact: a.Key, Required: true}}})
		encoded[a.Key] = data
	}
	s.Catalogue.Models[0].VADRevision = s.Catalogue.Models[1].Revision
	return s, s.Catalogue.Models[0], encoded
}
func serve(t *testing.T, s *Store, data map[string][]byte, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("ETag", `"fixture"`)
		http.ServeContent(w, r, "model", timeZero, bytes.NewReader(data[strings.TrimPrefix(r.URL.Path, "/")]))
	}))
	t.Cleanup(server.Close)
	s.URL = func(a Artifact) string { return server.URL + "/" + a.Key }
	return server
}
func TestShippedCatalogue(t *testing.T) {
	if err := Shipped().Validate(); err != nil {
		t.Fatal(err)
	}
}
func TestAcquirePackImportAndReuse(t *testing.T) {
	s, m, data := fixture(t)
	var calls atomic.Int32
	server := serve(t, s, data, &calls)
	ctx := context.Background()
	if err := s.Acquire(ctx, m); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("requests=%d", calls.Load())
	}
	s.NoDownload = true
	server.Close()
	if err := s.Acquire(ctx, m); err != nil {
		t.Fatal(err)
	}
	pack := filepath.Join(t.TempDir(), "offline.tar")
	if err := s.Pack(ctx, m, pack); err != nil {
		t.Fatal(err)
	}
	target := New(t.TempDir())
	target.Catalogue = s.Catalogue
	// NoDownload deliberately false: import must never consult its network seam.
	target.URL = func(Artifact) string { t.Fatal("offline import constructed network request"); return "" }
	if err := target.Import(ctx, m, ImportOptions{From: pack, Pack: true}); err != nil {
		t.Fatal(err)
	}
	if err := target.Complete(m); err != nil {
		t.Fatal(err)
	}
	if target.Ready(m, "cpu", "target-runtime") {
		t.Fatal("import trusted foreign readiness")
	}
	if err := target.MarkReady(m, "cpu", "target-runtime"); err != nil {
		t.Fatal(err)
	}
	if !target.Ready(m, "cpu", "target-runtime") || target.Ready(m, "cpu", "new-runtime") {
		t.Fatal("readiness does not track runtime")
	}
	if err := target.Import(ctx, m, ImportOptions{From: pack, Pack: true}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("reuse triggered a network request")
	}
	file := filepath.Join(target.Dir(m), m.Files[0].Path)
	if err := os.WriteFile(file, []byte("broken"), 0644); err != nil {
		t.Fatal(err)
	}
	if target.Complete(m) == nil || target.Ready(m, "cpu", "target-runtime") {
		t.Fatal("corrupt model accepted")
	}
}
func TestResumeAndServerIgnoringRange(t *testing.T) {
	for _, ignore := range []bool{false, true} {
		t.Run(fmt.Sprint(ignore), func(t *testing.T) {
			s, m, data := fixture(t)
			a, _ := s.Catalogue.Artifact(m.Files[0].Artifact)
			offset := len(data[a.Key]) / 2
			var ranged bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ranged = r.Header.Get("Range") == fmt.Sprintf("bytes=%d-", offset) && r.Header.Get("If-Range") == `"stable"`
				w.Header().Set("ETag", `"stable"`)
				if ignore {
					w.Write(data[a.Key])
					return
				}
				http.ServeContent(w, r, "model", timeZero, bytes.NewReader(data[a.Key]))
			}))
			defer server.Close()
			s.URL = func(Artifact) string { return server.URL }
			part := filepath.Join(s.Root, "partial")
			os.WriteFile(part, data[a.Key][:offset], 0600)
			writeJSON(part+".json", checkpoint{server.URL, a.SHA256, `"stable"`})
			if err := s.download(context.Background(), a, part, func(int64) {}); err != nil {
				t.Fatal(err)
			}
			if !ranged {
				t.Fatal("did not request validated resume")
			}
			if err := verifyFile(context.Background(), part, a.Size, a.SHA256); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestInvalidRangeNeverAppends(t *testing.T) {
	s, m, data := fixture(t)
	a, _ := s.Catalogue.Artifact(m.Files[0].Artifact)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-1/2")
		w.WriteHeader(206)
		w.Write([]byte("xx"))
	}))
	defer server.Close()
	s.URL = func(Artifact) string { return server.URL }
	part := filepath.Join(s.Root, "partial")
	os.WriteFile(part, data[a.Key][:3], 0600)
	writeJSON(part+".json", checkpoint{server.URL, a.SHA256, `"stable"`})
	if err := s.download(context.Background(), a, part, func(int64) {}); err == nil {
		t.Fatal("accepted wrong Content-Range")
	}
	st, _ := os.Stat(part)
	if st.Size() != 3 {
		t.Fatal("modified prefix on invalid response")
	}
}
func TestOfflineMissingDependencyAndTamperedPackage(t *testing.T) {
	s, m, data := fixture(t)
	var calls atomic.Int32
	serve(t, s, data, &calls)
	ctx := context.Background()
	if err := s.Acquire(ctx, m); err != nil {
		t.Fatal(err)
	}
	target := New(t.TempDir())
	target.Catalogue = s.Catalogue
	if err := target.Import(ctx, m, ImportOptions{From: s.Dir(m)}); err == nil || !strings.Contains(err.Error(), "VAD") {
		t.Fatalf("missing dependency: %v", err)
	}
	if target.Installed(m) == nil {
		t.Fatal("partial import was published")
	}
	pack := filepath.Join(t.TempDir(), "pack.tar")
	if err := s.Pack(ctx, m, pack); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(pack)
	if err != nil {
		t.Fatal(err)
	}
	// Change payload bytes without changing manifest identity.
	needle := []byte("test-model-weights")
	pos := bytes.Index(raw, needle)
	if pos < 0 {
		t.Fatal("payload missing")
	}
	raw[pos] = 'X'
	os.WriteFile(pack, raw, 0600)
	if err := target.Import(ctx, m, ImportOptions{From: pack, Pack: true}); err == nil {
		t.Fatal("accepted tampered package")
	}
}
func TestPackageTraversal(t *testing.T) {
	s, m, _ := fixture(t)
	v, _ := s.VAD(m)
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	manifest, _ := json.Marshal(PackManifest{Format: PackFormat, Model: m.ID, Revision: m.Revision, VADRevision: v.Revision})
	tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(manifest))})
	tw.Write(manifest)
	tw.WriteHeader(&tar.Header{Name: "../outside", Mode: 0600, Size: 1})
	tw.Write([]byte("x"))
	tw.Close()
	src := filepath.Join(t.TempDir(), "bad.tar")
	os.WriteFile(src, b.Bytes(), 0600)
	if err := s.Import(context.Background(), m, ImportOptions{From: src, Pack: true}); err == nil {
		t.Fatal("accepted traversal")
	}
}
func TestNoDownloadPolicy(t *testing.T) {
	s, m, _ := fixture(t)
	s.NoDownload = true
	s.URL = func(Artifact) string { t.Fatal("policy allowed network setup"); return "" }
	if s.Acquire(context.Background(), m) == nil {
		t.Fatal("missing files accepted")
	}
}
func TestCancellationRetainsPartial(t *testing.T) {
	s, m, data := fixture(t)
	a, _ := s.Catalogue.Artifact(m.Files[0].Artifact)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"stable"`)
		w.Header().Set("Content-Length", fmt.Sprint(a.Size))
		w.Write(data[a.Key][:10])
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	s.URL = func(Artifact) string { return server.URL }
	part := filepath.Join(s.Root, "partial")
	err := s.download(ctx, a, part, func(n int64) {
		if n > 0 {
			cancel()
		}
	})
	if err == nil {
		t.Fatal("cancel succeeded")
	}
	st, e := os.Stat(part)
	if e != nil || st.Size() != 10 {
		t.Fatalf("lost prefix %v %v", st, e)
	}
	cp, err := os.Open(part + ".json")
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	_, _ = io.Copy(io.Discard, cp)
}

func TestCompressedDirectoryImportIsLocalAndIdempotent(t *testing.T) {
	s, m, data := fixture(t)
	src := t.TempDir()
	for _, f := range m.Files {
		if err := os.WriteFile(filepath.Join(src, f.Path+".zst"), data[f.Artifact], 0644); err != nil {
			t.Fatal(err)
		}
	}
	v, _ := s.VAD(m)
	vf := v.Files[0]
	vad := filepath.Join(t.TempDir(), vf.Path+".zst")
	if err := os.WriteFile(vad, data[vf.Artifact], 0644); err != nil {
		t.Fatal(err)
	}
	s.URL = func(Artifact) string { t.Fatal("import tried network acquisition"); return "" }
	opts := ImportOptions{From: src, VAD: vad}
	if err := s.Import(context.Background(), m, opts); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(s.Dir(m), m.Files[0].Path))
	if err != nil {
		t.Fatal(err)
	}
	opts.VAD = "" // the exact dependency is already verified in the destination
	if err := s.Import(context.Background(), m, opts); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(filepath.Join(s.Dir(m), m.Files[0].Path))
	if !st.ModTime().Equal(after.ModTime()) {
		t.Fatal("repeat import replaced published files")
	}
}

func TestConcurrentAcquisitionSharesTheWriterAndBytes(t *testing.T) {
	s, m, data := fixture(t)
	var calls atomic.Int32
	serve(t, s, data, &calls)
	second := *s
	results := make(chan error, 2)
	go func() { results <- s.Acquire(context.Background(), m) }()
	go func() { results <- second.Acquire(context.Background(), m) }()
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("concurrent writers reacquired bytes: %d requests", calls.Load())
	}
}

func TestPinnedDependencySurvivesNewCatalogueDefault(t *testing.T) {
	s, m, data := fixture(t)
	var calls atomic.Int32
	serve(t, s, data, &calls)
	if err := s.Acquire(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	next, _ := s.VAD(m)
	next.Revision = strings.Repeat("f", 64)
	s.Catalogue.Models = append([]Model{next}, s.Catalogue.Models...)
	if err := s.Catalogue.Validate(); err != nil {
		t.Fatal(err)
	}
	s.NoDownload = true
	if err := s.Acquire(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("new dependency default changed active model")
	}
}
