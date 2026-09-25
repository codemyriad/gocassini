package operator

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestMeetingMetadataIndexJoinsOnlyRequestedFileIDs(t *testing.T) {
	store, err := openMeetingMetadataStore(filepath.Join(t.TempDir(), meetingMetadataFilename), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	for id, name := range map[int64]string{11: "A.opus", 22: "B.opus"} {
		entry := json.RawMessage(`{"id":"` + name[:1] + `","audioPath":"./meetings/` + name + `","dateLabel":"2026-09-23"}`)
		if err := store.Put(ctx, id, name, entry); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.EntriesFor(ctx, []int64{22, 0, 33})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[22] == nil {
		t.Fatalf("joined entries = %v", got)
	}
	if err := store.Forget(ctx, 22); err != nil {
		t.Fatal(err)
	}
	got, err = store.EntriesFor(ctx, []int64{22})
	if err != nil || len(got) != 0 {
		t.Fatalf("forgotten entries = %v, %v", got, err)
	}
}
