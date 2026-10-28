package operator

import (
	"context"
	"fmt"
	"io"
	"log"
)

// Run offline under the same work-root lock as the operator. Room names are
// durable; only file-ID-bound derived rows are discarded during a restore.
func runClearMeetingMetadata(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "clear-meeting-metadata takes no arguments; uses operator environment")
		return 2
	}
	cfg, code, err := loadConfig(nil, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if code != 0 {
		return code
	}
	unlock, err := lockWorkRoot(cfg.WorkRoot)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer unlock()
	store, err := openMeetingMetadataStore(meetingMetadataPath(cfg.DBPath), log.New(stderr, "metadata: ", 0))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer store.Close()
	if _, err = store.db.ExecContext(ctx, `DELETE FROM meeting_metadata`); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, "cleared derived meeting metadata; preserved room names")
	return 0
}
