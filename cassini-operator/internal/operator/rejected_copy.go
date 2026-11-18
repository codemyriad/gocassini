package operator

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// keepRejectedCopy runs only after a settings file failed to load at start.
// The running policy is then a stand-in, and the next save replaces the file,
// so the file is kept first as a hard link: no data is copied, the mode (0600
// for llm-settings.json) stays the file's own, and the link survives the
// save's rename. The name comes from the file's mtime, so a file that stays
// rejected across restarts is kept once. It returns the copy's base name, or
// "" when nothing was kept (no file, or links not supported).
func keepRejectedCopy(path string) string {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	base := fmt.Sprintf("%s.rejected-%d", path, info.ModTime().UnixNano())
	keep := base
	for suffix := 1; ; suffix++ {
		if err := os.Link(path, keep); err == nil {
			return filepath.Base(keep)
		} else if !errors.Is(err, fs.ErrExist) {
			return ""
		}
		if st, err := os.Lstat(keep); err == nil && os.SameFile(info, st) {
			return filepath.Base(keep)
		}
		// A restored or replaced file can have the same mtime as a
		// different rejected file already kept under this name.
		keep = fmt.Sprintf("%s-%d", base, suffix)
	}
}

// settingsLoadFailedMessage is the start-up log line for a settings file that
// could not be loaded. name is the log prefix, running says what Cassini runs
// on instead, and panel names the place in the UI whose save writes the file.
func settingsLoadFailedMessage(name, path, running, panel string, err error) string {
	base := filepath.Base(path)
	msg := fmt.Sprintf("%s could not be loaded (%v); %s.", name, err, running)
	if kept := keepRejectedCopy(path); kept != "" {
		return msg + fmt.Sprintf(" The file is kept as %s. Restart to retry, or move that file back over %s and restart. Saving %s now replaces %s and does not restore it.", kept, base, panel, base)
	}
	return msg + fmt.Sprintf(" Restart to retry; saving %s now writes a new %s.", panel, base)
}
