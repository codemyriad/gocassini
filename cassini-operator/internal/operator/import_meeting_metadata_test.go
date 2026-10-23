package operator

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestImportRoomNamesStayLocalAcrossResetAndReimport(t *testing.T) {
	ctx := context.Background()
	store, err := openMeetingMetadataStore(filepath.Join(t.TempDir(), "meetings.sqlite3"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	original := json.RawMessage(`{"id":"A","audioPath":"./meetings/A.opus","roomId":"source-opaque-id","roomName":"Alice","roomDisplayName":"Source override","extension":{"x":1}}`)
	catalog := siteCatalog{Version: "cassini.viewer.catalog.v1", Meetings: []json.RawMessage{original}}
	rows, err := mapSeedMetadata(catalog, map[int64]string{44: "A.opus"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, rows[0].id, rows[0].name, rows[0].entry); err != nil {
		t.Fatal(err)
	}
	states, _ := store.roomNames(ctx, []string{"source-opaque-id"})
	if states["source-opaque-id"].DisplayName != "Alice" {
		t.Fatalf("import adopted alias: %+v", states)
	}
	alias := "Destination name"
	saved, err := store.saveRoomName(ctx, "source-opaque-id", &alias, 1, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM meeting_metadata`); err != nil {
		t.Fatal(err)
	}
	rows, err = mapSeedMetadata(catalog, map[int64]string{99: "A.opus"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, rows[0].id, rows[0].name, rows[0].entry); err != nil {
		t.Fatal(err)
	}
	states, _ = store.roomNames(ctx, []string{"source-opaque-id"})
	if states["source-opaque-id"].DisplayName != alias || states["source-opaque-id"].Revision != saved.Revision {
		t.Fatalf("local state changed: %+v", states)
	}
	data, _ := store.EntriesFor(ctx, []int64{99})
	var fields map[string]json.RawMessage
	json.Unmarshal(data[99], &fields)
	if fields["roomDisplayName"] != nil || string(fields["roomName"]) != `"Alice"` || fields["extension"] == nil {
		t.Fatalf("metadata %s", data[99])
	}
	for id, raw := range map[int64]string{100: `{"id":"B","audioPath":"./meetings/B.opus","roomId":"id-only"}`, 101: `{"id":"C","audioPath":"./meetings/C.opus","roomName":"Legacy"}`} {
		name := "B.opus"
		if id == 101 {
			name = "C.opus"
		}
		if err := store.Put(ctx, id, name, json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
	}
	names, _ := store.roomNames(ctx, []string{"id-only", "Legacy"})
	if names["id-only"].DisplayName != "id-only" || len(names) != 1 {
		t.Fatal(names)
	}
}

func TestMapSeedMetadataUsesDestinationIDsAndPreservesFields(t *testing.T) {
	raw := json.RawMessage(`{"id":"meeting","audioPath":"./meetings/daily--12:30.opus","title":"Daily","roomName":"Team","extension":{"x":1}}`)
	catalog := siteCatalog{Version: "cassini.viewer.catalog.v1", Meetings: []json.RawMessage{raw}}
	rows, err := mapSeedMetadata(catalog, map[int64]string{987: "daily--12:30.opus"})
	if err != nil || len(rows) != 1 || rows[0].id != 987 || string(rows[0].entry) != string(raw) {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if _, err := mapSeedMetadata(catalog, map[int64]string{123: "other.opus"}); err == nil {
		t.Fatal("accepted absent recording")
	}
	catalog.Meetings = append(catalog.Meetings, raw)
	if _, err := mapSeedMetadata(catalog, map[int64]string{987: "daily--12:30.opus"}); err == nil {
		t.Fatal("accepted duplicate")
	}
}
