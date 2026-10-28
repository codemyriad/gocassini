package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestClearMeetingMetadataPreservesNamesAndRequiresOfflineLock(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	workRoot := filepath.Join(dir, "jobs")
	repoRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CASSINI_REPO_ROOT", repoRoot)
	t.Setenv("CASSINI_OPERATOR_WORK_ROOT", workRoot)
	t.Setenv("CASSINI_OPERATOR_DB_PATH", filepath.Join(dir, "jobs.sqlite3"))
	s, err := openMeetingMetadataStore(filepath.Join(dir, "meetings.sqlite3"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Put(ctx, 1, "A.opus", json.RawMessage(`{"id":"A","audioPath":"./meetings/A.opus","roomId":"one","roomName":"Alice"}`)); err != nil {
		t.Fatal(err)
	}
	name := "Planning"
	saved, err := s.saveRoomName(ctx, "one", &name, 1, "alice")
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := lockWorkRoot(workRoot)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runClearMeetingMetadata(ctx, nil, &stdout, &stderr)
	unlock()
	if code == 0 {
		t.Fatal("cleared metadata while operator lock held")
	}
	entries, err := s.EntriesFor(ctx, []int64{1})
	if err != nil || len(entries) != 1 {
		t.Fatalf("locked command changed metadata: %v", err)
	}
	stderr.Reset()
	if code := runClearMeetingMetadata(ctx, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("clear failed: %d %s", code, stderr.String())
	}
	entries, err = s.EntriesFor(ctx, []int64{1})
	if err != nil || len(entries) != 0 {
		t.Fatalf("derived rows retained: %v", err)
	}
	names, err := s.roomNames(ctx, []string{"one"})
	if err != nil || names["one"].DisplayName != name || names["one"].Revision != saved.Revision {
		t.Fatalf("durable name changed: %+v %v", names, err)
	}
}
