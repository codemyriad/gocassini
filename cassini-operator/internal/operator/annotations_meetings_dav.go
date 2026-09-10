package operator

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// maxAnnotateRecordingBytes bounds the recording one annotations request may
// stage, per copy. A published meeting is a few tens of megabytes even for a
// long call; the bound is the one meetings-context gives a whole bundle, which
// makes it generous for one recording and still a real limit on what a request
// can write to the ExApp volume.
const maxAnnotateRecordingBytes = maxContextStagedBytes

// stageAnnotatedRecording streams relPath, read as userID, into destPath for
// `cassini annotate`, and reports the upstream status so the caller can keep
// denied and absent alike.
//
// It is davDownloadFile with this path's two refusals: a limit, so one request
// cannot fill the ExApp volume, and an empty body, because no recording that can
// be annotated is empty.
func (c ExAppConfig) stageAnnotatedRecording(ctx context.Context, client *http.Client, userID, relPath, destPath string, limit int64) (int, error) {
	_, written, status, err := c.davDownloadFile(ctx, client, userID, relPath, destPath, limit)
	if err != nil {
		return status, err
	}
	if written == 0 {
		return status, fmt.Errorf("GET %s returned an empty recording", relPath)
	}
	return status, nil
}

// annotationReadIdentity is the identity a recording at relPath is read as for
// its caller: the service account inside the default model's private root, the
// caller everywhere else.
//
// It is derived from relPath rather than asked of ncArchiveReadIdentity a second
// time, because the root in relPath was already chosen by the catalog
// resolution that decided the caller may read it. Asking the mode again could
// answer differently if it changed in between — and pairing the Team folder
// with the owner's identity is the disclosure ncArchiveReadIdentity exists to
// prevent. Anything not under the private root reads as the caller, which is
// the direction that fails closed.
func annotationReadIdentity(caller, relPath string) string {
	if annotationInPrivateRoot(relPath) {
		return ncRecordingsOwner
	}
	return caller
}

// annotationInPrivateRoot reports whether relPath is in the default model's
// private root, where no leaf carries rules by design. Everywhere else a
// recording must carry its broad-group rule after every write.
func annotationInPrivateRoot(relPath string) bool {
	return strings.HasPrefix(relPath, ncDefaultRecordingsRoot+"/")
}

// annotationWriteLocks serialises writes to one recording within this process.
//
// If-Match is what makes a concurrent write safe; this only makes it cheap. Two
// people marking the same meeting through the same operator would otherwise
// both read one ETag, both rewrite tens of megabytes, and one would lose the PUT
// and do it all again. Other writers — another replica, a republish, someone in
// the Files UI — are still caught by If-Match, which is why this lock is not
// the lock.
var annotationWriteLocks keyedLocks

// keyedLocks is a set of mutexes by key, each existing only while held or
// awaited. Acquiring one honours the context, so a request whose caller gave up
// stops waiting.
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
