//go:build linux || darwin || freebsd

package talk

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestFinalizeLockSerializesAndCancelsWaiters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "finalize.lock")
	release, err := acquireFinalizeLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if unlock, err := acquireFinalizeLock(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		if unlock != nil {
			unlock()
		}
		t.Fatalf("second finalizer did not wait: %v", err)
	}
	release()
	unlock, err := acquireFinalizeLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}
