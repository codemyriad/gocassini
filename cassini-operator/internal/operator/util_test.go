package operator

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestWriteFileAtomicLeavesExistingTempFileAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte("another writer's data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, []byte("new catalog"), 0o640); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(tmp); err != nil || string(got) != "another writer's data" {
		t.Fatalf("existing temporary file = %q, %v", got, err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "new catalog" {
		t.Fatalf("catalog = %q, %v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("permissions = %o, want 640", info.Mode().Perm())
	}
}

func TestWriteFileAtomicConcurrentWriters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")
	const writers = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := writeFileAtomic(path, bytes.Repeat([]byte{byte(i)}, 64<<10), 0o600); err != nil {
				t.Errorf("writer %d: %v", i, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 64<<10 || !bytes.Equal(got, bytes.Repeat(got[:1], len(got))) {
		t.Fatal("destination does not contain one complete write")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "catalog.json" {
		t.Fatalf("temporary files remain: %v", entries)
	}
}

func TestWriteFileAtomicCleansUpFailedRename(t *testing.T) {
	dir := t.TempDir()
	if err := writeFileAtomic(dir, []byte("cannot replace a directory"), 0o600); err == nil {
		t.Fatal("expected rename failure")
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(dir), "."+filepath.Base(dir)+"-*.tmp"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files after failed rename = %v, %v", matches, err)
	}
}
