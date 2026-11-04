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
	"sort"
	"strings"
	"sync"
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
	kinds := map[string]string{"test-model": KindSpeech, "silero-vad": KindVAD, "test-diarizer": KindDiarization}
	for _, id := range []string{"test-model", "silero-vad", "test-diarizer"} {
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
		s.Catalogue.Models = append(s.Catalogue.Models, Model{ID: id, Kind: kinds[id], Revision: uh, Files: []File{{Path: filename, Artifact: a.Key, Required: true}}})
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

// fixtureDiarizer is the fixture's diarization model: one file, no VAD.
func fixtureDiarizer(t *testing.T, s *Store) Model {
	t.Helper()
	m, err := s.Catalogue.Model("test-diarizer", "")
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func TestShippedCatalogue(t *testing.T) {
	c := Shipped()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, m := range c.Models {
		kinds[m.ID] = m.Kind
	}
	want := map[string]string{
		"parakeet-tdt-ctc-110m-en-int8": KindSpeech,
		"parakeet-ultra-0.6b-int8":      KindSpeech,
		"parakeet-ultra-0.6b":           KindSpeech,
		"silero-vad":                    KindVAD,
		"nemotron-3-diarization-int8":   KindDiarization,
		"nemotron-3-diarization":        KindDiarization,
	}
	for id, kind := range want {
		if kinds[id] != kind {
			t.Errorf("%s kind = %q, want %q", id, kinds[id], kind)
		}
	}
	// The diarizer Cassini runs by default: the version-2 INT8 export the
	// Cassini .6 runtime was reviewed with, not the version-1 one the CDN
	// still serves.
	m, err := c.Model("nemotron-3-diarization-int8", "")
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{Catalogue: c}
	if got := s.Components(m); len(got) != 1 || got[0].ID != m.ID {
		t.Fatalf("diarizer components = %+v, want the model alone", got)
	}
	a, err := c.Artifact(m.Files[0].Artifact)
	if err != nil {
		t.Fatal(err)
	}
	if m.Files[0].Path != "model.int8.onnx" || a.UncompressedSHA256 != "47c221ea9b4d4e7f6c108bd098e769bc706cdc986c29f6133c93cae335fe6779" || a.UncompressedSize != 103967426 || a.Size != 64915668 {
		t.Fatalf("diarizer artifact = %+v", a)
	}
	// An install is (id, revision), so each export needs its own revision: a
	// re-export under the old one would look like a damaged install. The
	// Nemotron exports share NVIDIA's weights, so their revision is the
	// exported file's own hash, not the weights'.
	owner := map[string]string{}
	for _, m := range c.Models {
		if other, ok := owner[m.Revision]; ok {
			t.Errorf("%s and %s share revision %s", other, m.ID, m.Revision)
		}
		owner[m.Revision] = m.ID
		if m.Kind == KindDiarization {
			f, err := c.Artifact(m.Files[0].Artifact)
			if err != nil || len(m.Files) != 1 || m.Revision != f.UncompressedSHA256 {
				t.Errorf("%s revision %s is not its exported file's sha256", m.ID, m.Revision)
			}
		}
	}
	// Every installed model directory gets the embedded notice; each licence
	// in the catalogue must be in it.
	for licence, needle := range map[string]string{"CC-BY-4.0": "CC BY 4.0", "MIT": "MIT License", "OpenMDW-1.1": "OpenMDW License Agreement, version 1.1"} {
		for _, m := range c.Models {
			if m.License == licence && !strings.Contains(Notices, needle) {
				t.Errorf("NOTICE.txt lacks the %s licence of %s", licence, m.ID)
			}
		}
	}
	for _, m := range c.Models {
		switch m.License {
		case "CC-BY-4.0", "MIT", "OpenMDW-1.1":
		default:
			t.Errorf("%s has licence %q, which this test does not know", m.ID, m.License)
		}
	}
	for _, a := range c.Artifacts {
		used := false
		for _, m := range append(append([]Model{}, c.Models...), c.Retired...) {
			for _, f := range m.Files {
				used = used || f.Artifact == a.Key
			}
		}
		if !used {
			t.Errorf("artifact %s belongs to no model", a.Key)
		}
	}
}

func TestValidateChecksEachKindsDependencies(t *testing.T) {
	cases := map[string]func(c *Catalogue){
		"speech model without VAD": func(c *Catalogue) { c.Models[0].VADRevision = "" },
		"diarizer pinning a VAD":   func(c *Catalogue) { c.Models[2].VADRevision = c.Models[1].Revision },
		"model without a kind":     func(c *Catalogue) { c.Models[2].Kind = "" },
		"unknown kind":             func(c *Catalogue) { c.Models[2].Kind = "embedding" },
		"VAD under another id":     func(c *Catalogue) { c.Models[2].Kind = KindVAD },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			s, _, _ := fixture(t)
			if err := s.Catalogue.Validate(); err != nil {
				t.Fatalf("fixture invalid: %v", err)
			}
			breakIt(&s.Catalogue)
			if err := s.Catalogue.Validate(); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// A diarizer is installed, packed and imported on its own: no VAD is fetched,
// packed or required on the target.
func TestDiarizerNeedsNoVAD(t *testing.T) {
	s, _, data := fixture(t)
	m := fixtureDiarizer(t, s)
	var calls atomic.Int32
	server := serve(t, s, data, &calls)
	ctx := context.Background()
	if err := s.Acquire(ctx, m); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("requests=%d, want the model file only", calls.Load())
	}
	if _, err := os.Stat(filepath.Join(s.Root, "vad")); !os.IsNotExist(err) {
		t.Fatalf("a VAD was installed for a diarizer: %v", err)
	}
	if err := s.Complete(m); err != nil {
		t.Fatal(err)
	}
	server.Close()

	pack := filepath.Join(t.TempDir(), "diarizer.tar")
	if err := s.Pack(ctx, m, pack); err != nil {
		t.Fatal(err)
	}
	identity, err := PackIdentity(pack)
	if err != nil || identity.Model != m.ID || identity.VADRevision != "" {
		t.Fatalf("identity = %+v, %v", identity, err)
	}
	if raw, _ := os.ReadFile(pack); bytes.Contains(raw, []byte("vad/")) {
		t.Fatal("pack carries a VAD")
	}
	target := New(t.TempDir())
	target.Catalogue = s.Catalogue
	target.URL = func(Artifact) string { t.Fatal("offline import constructed network request"); return "" }
	if err := target.Import(ctx, m, ImportOptions{From: pack, Pack: true}); err != nil {
		t.Fatal(err)
	}
	if err := target.Complete(m); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target.Root, "vad")); !os.IsNotExist(err) {
		t.Fatalf("import published a VAD: %v", err)
	}
	if err := target.Import(ctx, m, ImportOptions{From: s.Dir(m), VAD: "/dev/null"}); err == nil || !strings.Contains(err.Error(), "omit --vad") {
		t.Fatalf("--vad for a diarizer: %v", err)
	}
}

// A single-file model imports from the one file an administrator copied,
// compressed or not, and refuses a different file under the right name.
func TestSingleFileModelImportsFromTheFile(t *testing.T) {
	s, _, data := fixture(t)
	m := fixtureDiarizer(t, s)
	f := m.Files[0]
	s.URL = func(Artifact) string { t.Fatal("import tried network acquisition"); return "" }
	ctx := context.Background()

	zst := filepath.Join(t.TempDir(), f.Path+".zst")
	if err := os.WriteFile(zst, data[f.Artifact], 0644); err != nil {
		t.Fatal(err)
	}
	if err := s.Import(ctx, m, ImportOptions{From: zst}); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(m); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(s.Dir(m), f.Path))
	if err != nil {
		t.Fatal(err)
	}
	target := New(t.TempDir())
	target.Catalogue = s.Catalogue
	plain := filepath.Join(t.TempDir(), f.Path)
	if err := os.WriteFile(plain, raw, 0644); err != nil {
		t.Fatal(err)
	}
	if err := target.Import(ctx, m, ImportOptions{From: plain}); err != nil {
		t.Fatal(err)
	}
	if err := target.Complete(m); err != nil {
		t.Fatal(err)
	}

	// Saved under another name, as a browser does with a second download:
	// still the model's file, compressed or not.
	for _, name := range []string{"model.int8.onnx(1).zst", "nemo.onnx"} {
		renamed := New(t.TempDir())
		renamed.Catalogue = s.Catalogue
		body := data[f.Artifact]
		if !strings.HasSuffix(name, ".zst") {
			body = raw
		}
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, body, 0644); err != nil {
			t.Fatal(err)
		}
		if err := renamed.Import(ctx, m, ImportOptions{From: path}); err != nil {
			t.Fatalf("import %s: %v", name, err)
		}
		if err := renamed.Complete(m); err != nil {
			t.Fatalf("import %s: %v", name, err)
		}
	}

	other := New(t.TempDir())
	other.Catalogue = s.Catalogue
	wrong := filepath.Join(t.TempDir(), f.Path)
	if err := os.WriteFile(wrong, append(raw[:len(raw)-1:len(raw)-1], 'X'), 0644); err != nil {
		t.Fatal(err)
	}
	if err := other.Import(ctx, m, ImportOptions{From: wrong}); err == nil || other.Installed(m) == nil {
		t.Fatalf("a changed file was imported: %v", err)
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

// A download killed while unpacking leaves a temporary file in staging. It must
// not be installed with the revision.
func TestPublishDropsTempFilesFromKilledAttempts(t *testing.T) {
	s, m, data := fixture(t)
	var calls atomic.Int32
	serve(t, s, data, &calls)
	stage := filepath.Join(s.Root, "downloads", m.Revision, "files")
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".file-123", ".metadata-456"} {
		if err := os.WriteFile(filepath.Join(stage, name), []byte("partial"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Acquire(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".file-123", ".metadata-456"} {
		if _, err := os.Stat(filepath.Join(s.Dir(m), name)); !os.IsNotExist(err) {
			t.Fatalf("%s was installed with the revision (stat err = %v)", name, err)
		}
	}
	if err := s.Complete(m); err != nil {
		t.Fatal(err)
	}
}

// fastDownloadRetries makes the automatic resume loop test-sized.
func fastDownloadRetries(t *testing.T, stall time.Duration) {
	t.Helper()
	prevStall, prevDelay := downloadStallTimeout, downloadRetryDelay
	downloadStallTimeout = stall
	downloadRetryDelay = func(int) time.Duration { return 10 * time.Millisecond }
	t.Cleanup(func() { downloadStallTimeout, downloadRetryDelay = prevStall, prevDelay })
}

// flakyServer serves each artifact with a strong ETag and honours Range. The
// first request for each artifact breaks after half its bytes: drop closes the
// connection, otherwise the server stops sending and waits.
func flakyServer(t *testing.T, s *Store, data map[string][]byte, drop bool, calls *atomic.Int32) {
	t.Helper()
	broken := map[string]bool{}
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		key := strings.TrimPrefix(r.URL.Path, "/")
		body := data[key]
		mu.Lock()
		first := !broken[key]
		broken[key] = true
		mu.Unlock()
		w.Header().Set("ETag", `"fixture"`)
		if !first {
			http.ServeContent(w, r, "model", timeZero, bytes.NewReader(body))
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.WriteHeader(http.StatusOK)
		w.Write(body[:len(body)/2])
		w.(http.Flusher).Flush()
		if drop {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	s.URL = func(a Artifact) string { return server.URL + "/" + a.Key }
}

// A connection reset mid-transfer is routine on real networks. The install
// resumes on its own instead of failing the whole model.
func TestAcquireResumesAfterADroppedConnection(t *testing.T) {
	fastDownloadRetries(t, 5*time.Second)
	s, m, data := fixture(t)
	var calls atomic.Int32
	flakyServer(t, s, data, true, &calls)
	if err := s.Acquire(context.Background(), m); err != nil {
		t.Fatalf("Acquire() after one dropped connection = %v", err)
	}
	if err := s.Complete(m); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 4 {
		t.Fatalf("requests = %d, want one broken and one resumed request per artifact", calls.Load())
	}
}

func TestAcquireResumesAfterAStalledTransfer(t *testing.T) {
	fastDownloadRetries(t, 200*time.Millisecond)
	s, m, data := fixture(t)
	var calls atomic.Int32
	flakyServer(t, s, data, false, &calls)
	if err := s.Acquire(context.Background(), m); err != nil {
		t.Fatalf("Acquire() after a stalled transfer = %v", err)
	}
	if err := s.Complete(m); err != nil {
		t.Fatal(err)
	}
}

// A missing file will not appear on retry.
func TestAcquireDoesNotRetryAClientError(t *testing.T) {
	fastDownloadRetries(t, 5*time.Second)
	s, m, _ := fixture(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	s.URL = func(a Artifact) string { return server.URL + "/" + a.Key }
	if err := s.Acquire(context.Background(), m); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("Acquire() = %v, want HTTP 404", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("requests = %d, want 1", calls.Load())
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

// Parakeet Ultra replaces v3 at the same precision, and the int8 build ships
// without bpe.vocab so it decodes greedily, as v3 int8 did.
func TestShippedUltraReplacesV3(t *testing.T) {
	c := Shipped()
	for retired, want := range map[string]string{
		"parakeet-tdt-0.6b-v3":      "parakeet-ultra-0.6b",
		"parakeet-tdt-0.6b-v3-int8": "parakeet-ultra-0.6b-int8",
	} {
		if _, err := c.Model(retired, ""); err == nil {
			t.Errorf("catalogue still ships %s", retired)
		}
		if m, ok := c.Replacement(retired); !ok || m.ID != want {
			t.Errorf("replacement of %s = %q, %v; want %s", retired, m.ID, ok, want)
		}
	}
	if _, ok := c.Replacement("parakeet-tdt-ctc-110m-en-int8"); ok {
		t.Error("a shipped model is reported as replaced")
	}
	m, err := c.Model("parakeet-ultra-0.6b-int8", "")
	if err != nil {
		t.Fatal(err)
	}
	// The revision is the sha256 of the sorted `sha256sum` listing of the
	// exported files, as dist.gocassini.com's manifest records it.
	var listing strings.Builder
	paths := []string{}
	byPath := map[string]string{}
	for _, f := range m.Files {
		if f.Path == "bpe.vocab" {
			t.Fatal("Ultra int8 ships bpe.vocab; it is published without it to decode greedily")
		}
		a, err := c.Artifact(f.Artifact)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, f.Path)
		byPath[f.Path] = a.UncompressedSHA256
	}
	sort.Strings(paths)
	for _, p := range paths {
		fmt.Fprintf(&listing, "%s  %s\n", byPath[p], p)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(listing.String()))); got != m.Revision || m.Revision != "18cb623004dd6c15999b93cb70b14d8a1f6e8f3a2a97ff733f104bfc3494f6a9" {
		t.Fatalf("revision %s, listing hash %s", m.Revision, got)
	}
}

func TestValidateRejectsAmbiguousReplacement(t *testing.T) {
	cases := map[string]func(c *Catalogue){
		"replaces a shipped model": func(c *Catalogue) { c.Models[0].Replaces = []string{"test-diarizer"} },
		"replaces itself":          func(c *Catalogue) { c.Models[0].Replaces = []string{"test-model"} },
		"replaces a path":          func(c *Catalogue) { c.Models[0].Replaces = []string{"../vad"} },
		"two replacements": func(c *Catalogue) {
			c.Models[0].Replaces = []string{"old-model"}
			c.Models[2].Replaces = []string{"old-model"}
		},
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			s, _, _ := fixture(t)
			s.Catalogue.Models[0].Replaces = []string{"old-model"}
			if err := s.Catalogue.Validate(); err != nil {
				t.Fatalf("fixture invalid: %v", err)
			}
			breakIt(&s.Catalogue)
			if err := s.Catalogue.Validate(); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// Parakeet v3 is gone from what can be installed or selected, but the
// catalogue still describes the files an upgraded install has, exactly as the
// last catalogue that shipped them did, so a build can stand in with them
// until Ultra is ready. Their bytes are checked against these artifacts.
func TestShippedCatalogueRetiresV3(t *testing.T) {
	c := Shipped()
	const tokens = "models/files/5b3811f0c23fefc86835981f0a23ecfb817999df272ad744c597e5b669ea1b4a/tokens.txt.zst"
	want := map[string]struct {
		revision, replacement string
		files                 map[string]string
		optional              string
	}{
		"parakeet-tdt-0.6b-v3-int8": {"5793d0fd397c5778d2cf2126994d58e9d56b1be7c04d13c7a15bb1b4eafb16bf", "parakeet-ultra-0.6b-int8", map[string]string{
			"encoder.int8.onnx": "models/files/cb9e98257d3bc2611b43d9897e67454e5893aff1ffc90229ca3fa7f9ed5bb22e/encoder.int8.onnx.zst",
			"decoder.int8.onnx": "models/files/b14c3967107f0a5e4e9c0bde770c204202bde8d1a6255c27a863930e8c887c3d/decoder.int8.onnx.zst",
			"joiner.int8.onnx":  "models/files/1ef3c78137d39d45cfad961be5a45bf7abd3c13fa721c7da2327b0eb3d0e8ce6/joiner.int8.onnx.zst",
			"tokens.txt":        tokens,
		}, ""},
		"parakeet-tdt-0.6b-v3": {"9125e6a7beb392821f19a3633b80bb2847599580e34ed42a4128732978ad759c", "parakeet-ultra-0.6b", map[string]string{
			"encoder.onnx":    "models/files/2dff6d02d238425491fe8a2a9db8db45bdeb290b9e34643f6ae66df62bf9fd34/encoder.onnx.zst",
			"encoder.weights": "models/files/23a61cdbca1f606128fa9c2c05aa81457d3d0cce1aa7414a29f53ed5c626244d/encoder.weights.zst",
			"decoder.onnx":    "models/files/e8038c3ce024db1f304cbdd59775ad1b063a6f2e8e49ea59e318721de4d606f4/decoder.onnx.zst",
			"joiner.onnx":     "models/files/d74aa2809fe0186d99f961acfadceff72a7e42d9d56b51bd79f636d40609eeac/joiner.onnx.zst",
			"tokens.txt":      tokens,
			"bpe.vocab":       "models/files/4e074536b073a7d18ca034e3be779868696d67784765e4ac18511e8f607d4193/bpe.vocab.zst",
		}, "bpe.vocab"},
	}
	if len(c.Retired) != len(want) {
		t.Fatalf("retired models %d, want %d", len(c.Retired), len(want))
	}
	for id, w := range want {
		if _, err := c.Model(id, ""); err == nil {
			t.Fatalf("Model(%s) succeeds; a retired model must never be installed or selected", id)
		}
		m, err := c.RetiredModel(id, w.revision)
		if err != nil {
			t.Fatal(err)
		}
		if m.Kind != KindSpeech || m.VADRevision != "9e2449e1087496d8d4caba907f23e0bd3f78d91fa552479bb9c23ac09cbb1fd6" || len(m.Files) != len(w.files) {
			t.Fatalf("%s: %+v", id, m)
		}
		for _, f := range m.Files {
			if f.Artifact != w.files[f.Path] || f.Required != (f.Path != w.optional) {
				t.Fatalf("%s/%s: %+v", id, f.Path, f)
			}
			if _, err := c.Artifact(f.Artifact); err != nil {
				t.Fatal(err)
			}
		}
		if r, ok := c.Replacement(id); !ok || r.ID != w.replacement {
			t.Fatalf("replacement of %s = %q %v", id, r.ID, ok)
		}
		if got, retired, err := c.Lookup(id, ""); err != nil || !retired || got.Revision != w.revision {
			t.Fatalf("Lookup(%s) = %s %v %v", id, got.Revision, retired, err)
		}
	}
	if _, retired, err := c.Lookup("parakeet-ultra-0.6b-int8", ""); err != nil || retired {
		t.Fatalf("Lookup of a shipped model: retired=%v %v", retired, err)
	}
	// The weights are main's, byte for byte.
	for key, digest := range map[string]string{
		"models/files/cb9e98257d3bc2611b43d9897e67454e5893aff1ffc90229ca3fa7f9ed5bb22e/encoder.int8.onnx.zst": "acfc2b4456377e15d04f0243af540b7fe7c992f8d898d751cf134c3a55fd2247",
		"models/files/23a61cdbca1f606128fa9c2c05aa81457d3d0cce1aa7414a29f53ed5c626244d/encoder.weights.zst":   "3af3f51af5f2d01dbbf5af47d42c7962a2c205f11004254bb4f2b979862f39a8",
	} {
		if a, err := c.Artifact(key); err != nil || a.UncompressedSHA256 != digest {
			t.Fatalf("%s: %+v %v", key, a, err)
		}
	}
}

func TestValidateRejectsBadRetiredModels(t *testing.T) {
	cases := map[string]func(c *Catalogue){
		"replaced by nothing": func(c *Catalogue) { c.Models[0].Replaces = nil },
		"also shipped": func(c *Catalogue) {
			c.Retired[0].ID = "test-diarizer"
			c.Models[0].Replaces = []string{"test-diarizer"}
		},
		"missing artifact": func(c *Catalogue) { c.Retired[0].Files[0].Artifact = "models/files/gone.zst" },
		"not speech":       func(c *Catalogue) { c.Retired[0].Kind = KindDiarization; c.Retired[0].VADRevision = "" },
		"listed twice":     func(c *Catalogue) { c.Retired = append(c.Retired, c.Retired[0]) },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			s, m, _ := fixture(t)
			old := m
			old.ID = "old-model"
			old.Files = append([]File{}, m.Files...)
			s.Catalogue.Retired = []Model{old}
			s.Catalogue.Models[0].Replaces = []string{"old-model"}
			if err := s.Catalogue.Validate(); err != nil {
				t.Fatalf("fixture invalid: %v", err)
			}
			breakIt(&s.Catalogue)
			if err := s.Catalogue.Validate(); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// A retired model is never downloaded, whatever asks for it.
func TestAcquireRefusesARetiredModel(t *testing.T) {
	s, m, data := fixture(t)
	var calls atomic.Int32
	serve(t, s, data, &calls)
	old := m
	old.ID = "old-model"
	s.Catalogue.Retired = []Model{old}
	s.Catalogue.Models[0].Replaces = []string{"old-model"}
	if err := s.Acquire(context.Background(), old); err == nil || !strings.Contains(err.Error(), "never downloaded") {
		t.Fatalf("Acquire(retired) = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("%d requests for a retired model", calls.Load())
	}
	if _, err := os.Stat(s.Dir(old)); err == nil {
		t.Fatal("a retired model was installed")
	}
}
