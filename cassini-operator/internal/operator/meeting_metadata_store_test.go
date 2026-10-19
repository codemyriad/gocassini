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

func TestMeetingMetadataFileIDForPrefersLatestDelivery(t *testing.T) {
	store, err := openMeetingMetadataStore(filepath.Join(t.TempDir(), meetingMetadataFilename), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	entry := json.RawMessage(`{"id":"J","audioPath":"./meetings/J.opus","dateLabel":"2026-09-23"}`)
	if got, err := store.FileIDFor(ctx, "J.opus"); err != nil || got != 0 {
		t.Fatalf("unindexed file id = %d, %v", got, err)
	}
	if err := store.Put(ctx, 11, "J.opus", entry); err != nil {
		t.Fatal(err)
	}
	// Re-delivered after the first file was removed by hand: a new id, and the
	// old row lingers with the same indexed_at second.
	if err := store.Put(ctx, 42, "J.opus", entry); err != nil {
		t.Fatal(err)
	}
	if got, err := store.FileIDFor(ctx, "J.opus"); err != nil || got != 42 {
		t.Fatalf("file id = %d, %v; want 42", got, err)
	}
	var none *meetingMetadataStore
	if got, err := none.FileIDFor(ctx, "J.opus"); err != nil || got != 0 {
		t.Fatalf("nil store file id = %d, %v", got, err)
	}
}
