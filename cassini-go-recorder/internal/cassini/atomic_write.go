package cassini

import (
	"os"
	"path/filepath"
)

// writeFileAtomic writes body to path through a temp file in the same
// directory followed by a rename, so a crash or failed write mid-update never
// leaves a truncated or partially written file at path. Bundle manifests are
// read back by build/publish and the operator, so a torn cassini.json would
// poison every later stage.
func writeFileAtomic(path string, body []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// durableSync flushes a file or a directory to disk; tests replace it to see
// what was flushed, and when.
var durableSync = (*os.File).Sync

// writeFileDurable is writeFileAtomic for a file that must survive a power
// loss before anything that depends on it is written: the bytes are flushed
// before the rename, and the directory after it. A speaker edit keeps the
// build's own transcript and summary this way before replacing them, so the
// edited file can never be on disk without the original it was made from.
func writeFileDurable(path string, body []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := durableSync(f); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return durableSync(dir)
}
