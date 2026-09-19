//go:build linux || darwin || freebsd

package talk

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"syscall"
	"time"
)

// A separate recorder process owns each meeting. A file lock bounds aggregate
// remux concurrency across those processes and is released even after a crash.
// The operator sets one shared path; standalone recordings opt in via env.
func acquireFinalizeLock(ctx context.Context, path string) (func(), error) {
	if path == "" {
		return func() {}, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open finalization lock: %w", err)
	}
	nextLog := time.Time{}
	for {
		if err := ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			log.Printf("record finalization admitted")
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			f.Close()
			return nil, fmt.Errorf("acquire finalization lock: %w", err)
		}
		if time.Now().After(nextLog) {
			log.Printf("record finalization queued; capture flushed, waiting for another recorder")
			nextLog = time.Now().Add(5 * time.Second)
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
