package operator

import (
	"context"
	"encoding/json"
	"testing"
)

func TestRoomNamesJobAndSearchProjectionsPreserveOriginal(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	seedTalkJob(t, rt, "job")
	rt.storeTalkRoomInfo("job", talkRoomInfo{Name: "Alice", Type: 1, TitleParticipants: []talkTitleParticipant{{ID: "admin", Name: "admin"}, {ID: "alice", Name: "Alice"}}})
	ctx := context.Background()
	job, err := rt.store.GetJob(ctx, "job")
	if err != nil {
		t.Fatal(err)
	}
	pending := []Job{job}
	rt.projectJobRoomNames(ctx, pending)
	if pending[0].RoomDisplayName != "admin & Alice" || *pending[0].RoomName != "Alice" {
		t.Fatalf("%+v", pending[0])
	}
	raw := json.RawMessage(`{"id":"job","roomId":"one","roomName":"Alice","audioPath":"./meetings/job.opus"}`)
	if err := rt.meetingMetadata.Put(ctx, 1, "job.opus", raw); err != nil {
		t.Fatal(err)
	}
	name := "Planning"
	if _, err := rt.meetingMetadata.saveRoomName(ctx, "one", &name, 1, "alice"); err != nil {
		t.Fatal(err)
	}
	rt.projectJobRoomNames(ctx, pending)
	if pending[0].RoomDisplayName != "Planning" || *pending[0].RoomName != "Alice" {
		t.Fatalf("%+v", pending[0])
	}
	entries, err := rt.meetingMetadata.projectRoomNames(ctx, []json.RawMessage{raw})
	if err != nil {
		t.Fatal(err)
	}
	catalog, _ := json.Marshal(siteCatalog{Version: "cassini.viewer.catalog.v1", Meetings: entries})
	hydration, err := decodeCatalogEntries(catalog)
	if err != nil || len(hydration) != 1 || hydration[0].roomName != "Alice" || hydration[0].roomDisplayName != "Planning" {
		t.Fatalf("%+v %v", hydration, err)
	}
}
