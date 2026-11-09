package operator

import (
	"context"
	"fmt"
	"io"
	"log"
)

// Run offline under the same work-root lock as the operator. Room names are
// durable; only file-ID-bound derived rows are discarded during a restore,
// and with them the recordedAtUtc file backfill's progress: the restored
// files may predate it, so the next startup pass queues them afresh.
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
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer tx.Rollback()
	for _, table := range []string{"meeting_metadata", "recorded_at_file_backfill"} {
		if _, err = tx.ExecContext(ctx, `DELETE FROM `+table); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if err = tx.Commit(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, "cleared derived meeting metadata; preserved room names")
	return 0
}
