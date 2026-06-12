package cassini

import "os"

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
