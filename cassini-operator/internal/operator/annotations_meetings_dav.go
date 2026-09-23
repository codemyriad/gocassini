package operator

import (
	"context"
	"sync"
)

// maxAnnotateRecordingBytes bounds the recording one annotations request may
// stage, per copy: the bound meetings-context gives a whole bundle.
const maxAnnotateRecordingBytes = maxContextStagedBytes

func (c ExAppConfig) recordingReadIdentity(caller, relPath string) string { return caller }

// annotationWriteLocks serialises writes to one recording within this process.
// Workers, imports and republish share it. If-Match and content verification
// remain necessary, but ETags alone are not sufficient writer coordination.
var annotationWriteLocks keyedLocks

// keyedLocks is a set of mutexes by key, each existing only while held or
// awaited. Acquiring one honours the context.
type keyedLocks struct {
	mu    sync.Mutex
	locks map[string]*keyedLock
}

type keyedLock struct {
	slot chan struct{}
	refs int
}

// acquire takes key's lock, or returns ctx's error if ctx ends first. The
// returned release must be called exactly once.
func (k *keyedLocks) acquire(ctx context.Context, key string) (release func(), err error) {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[string]*keyedLock{}
	}
	lock := k.locks[key]
	if lock == nil {
		lock = &keyedLock{slot: make(chan struct{}, 1)}
		k.locks[key] = lock
	}
	lock.refs++
	k.mu.Unlock()

	select {
	case lock.slot <- struct{}{}:
	case <-ctx.Done():
		k.forget(key, lock)
		return nil, ctx.Err()
	}
	return func() {
		<-lock.slot
		k.forget(key, lock)
	}, nil
}

func (k *keyedLocks) forget(key string, lock *keyedLock) {
	k.mu.Lock()
	defer k.mu.Unlock()
	lock.refs--
	if lock.refs == 0 {
		delete(k.locks, key)
	}
}
