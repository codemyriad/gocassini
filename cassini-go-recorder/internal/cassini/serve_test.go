package cassini

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type syncStringWriter struct {
	mu sync.Mutex
	b  strings.Builder
}

func (w *syncStringWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *syncStringWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

func TestResolveServeRootForSiteBundle(t *testing.T) {
	tmp := t.TempDir()
	siteDir := filepath.Join(tmp, "site")
	if err := os.MkdirAll(siteDir, 0o755); err != nil {
		t.Fatalf("mkdir site dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(siteDir, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(siteDir, "cassini.json"), []byte(`{
  "kind": "site",
  "version": "cassini.site.v1",
  "source_path": "/tmp/meetings",
  "catalog_path": "catalog.json",
  "meeting_count": 1
}
`), 0o644); err != nil {
		t.Fatalf("write site manifest: %v", err)
	}

	root, summary, err := resolveServeRoot(siteDir)
	if err != nil {
		t.Fatalf("resolveServeRoot: %v", err)
	}
	if root != siteDir || summary != siteDir {
		t.Fatalf("unexpected serve root: root=%q summary=%q", root, summary)
	}
}

func TestResolveServeRootForGenericStaticDir(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}

	root, _, err := resolveServeRoot(tmp)
	if err != nil {
		t.Fatalf("resolveServeRoot: %v", err)
	}
	if root != tmp {
		t.Fatalf("unexpected root: got=%q want=%q", root, tmp)
	}
}

func TestRunServeServesFiles(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "index.html"), []byte("<html>ok</html>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdout := &syncStringWriter{}
	stderr := &syncStringWriter{}

	done := make(chan int, 1)
	go func() {
		done <- runServe(ctx, []string{tmp, "--addr", "127.0.0.1:18765"}, stdout, stderr)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(stdout.String(), "url -> http://127.0.0.1:18765/") {
		if time.Now().After(deadline) {
			t.Fatalf("server did not start: stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	resp, err := http.Get("http://127.0.0.1:18765/")
	if err != nil {
		t.Fatalf("get served page: %v", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), "ok") {
		t.Fatalf("unexpected body: %q", string(body))
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("expected exit 0, got %d stderr=%q", code, stderr.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("serve did not shut down")
	}
}
