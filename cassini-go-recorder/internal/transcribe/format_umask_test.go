//go:build linux || darwin

package transcribe

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestWriteJSONHonorsCreationUmask(t *testing.T) {
	originalUmask := syscall.Umask(0)
	syscall.Umask(originalUmask)
	t.Cleanup(func() { syscall.Umask(originalUmask) })

	for _, test := range []struct {
		name string
		mask int
		want os.FileMode
	}{
		{name: "usual umask", mask: 0o022, want: 0o644},
		{name: "restrictive umask", mask: 0o077, want: 0o600},
	} {
		t.Run(test.name, func(t *testing.T) {
			syscall.Umask(test.mask)
			path := filepath.Join(t.TempDir(), "transcript.json")
			if err := writeJSON(path, map[string]string{"hello": "world"}); err != nil {
				t.Fatalf("writeJSON: %v", err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat transcript: %v", err)
			}
			if got := info.Mode().Perm(); got != test.want {
				t.Fatalf("transcript mode = %04o, want %04o for umask %04o", got, test.want, test.mask)
			}
		})
	}
}

func TestWriteJSONDoesNotWidenExistingPermissions(t *testing.T) {
	originalUmask := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(originalUmask) })

	path := filepath.Join(t.TempDir(), "transcript.json")
	if err := os.WriteFile(path, []byte("old document\n"), 0o600); err != nil {
		t.Fatalf("seed transcript: %v", err)
	}
	if err := writeJSON(path, map[string]string{"new": "document"}); err != nil {
		t.Fatalf("writeJSON: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat transcript: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("transcript mode widened from 0600 to %04o", got)
	}
}
