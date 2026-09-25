package operator

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// The supported server platforms are Linux and macOS. Kernel ownership is
// released even on a crash; a second operator must never mutate this root.
func lockWorkRoot(root string) (func(), error) {
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, err
	}
	p := filepath.Join(root, ".operator.lock")
	if err := safeArtifactPath(root, p); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("work root is owned by another operator: %w", err)
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}
