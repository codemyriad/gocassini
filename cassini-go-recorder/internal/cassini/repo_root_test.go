package cassini

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindRepoRootFrom(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "repo")
	start := filepath.Join(root, "cassini-go-recorder", "internal", "cassini")
	if err := os.MkdirAll(filepath.Join(root, "cassini-transcriber", "bin"), 0o755); err != nil {
		t.Fatalf("mkdir transcriber bin: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "cassini-viewer"), 0o755); err != nil {
		t.Fatalf("mkdir viewer: %v", err)
	}
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatalf("mkdir start dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "cassini-transcriber", "bin", "docker-run-local.sh"), []byte("#!/usr/bin/env bash\n"), 0o755); err != nil {
		t.Fatalf("write runner: %v", err)
	}

	got, err := findRepoRootFrom(start)
	if err != nil {
		t.Fatalf("findRepoRootFrom: %v", err)
	}
	if got != root {
		t.Fatalf("unexpected repo root: got=%q want=%q", got, root)
	}
}
