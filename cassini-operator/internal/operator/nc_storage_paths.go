package operator

import "strings"

// A new owner-private root prevents an older release from serving directly
// shared recordings through its broad, mode-selected read path after rollback.
const ncRecordingsRoot = "CassiniRecordings"

func ncArchiveReadIdentity(caller string) (readAs, root string) {
	return caller, ncRecordingsRoot
}

func ncArchiveRoot() string { return ncRecordingsRoot }

func recordingsTreeDirs(root string) []string {
	parts := strings.Split(strings.Trim(root, "/"), "/")
	dirs := make([]string, 0, len(parts)+1)
	for i := range parts {
		if parts[i] == "" {
			continue
		}
		dirs = append(dirs, strings.Join(parts[:i+1], "/"))
	}
	return append(dirs, strings.Trim(root, "/")+"/meetings")
}
