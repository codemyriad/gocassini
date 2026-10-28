package cassini

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gocassini/internal/meetingcontext"
)

func TestRoomDisplayContextPreservesOriginal(t *testing.T) {
	entry := meetingsCatalogEntry{RoomID: "opaque", RoomName: "Alice", RoomDisplayName: "Planning"}
	context := buildMeetingContext("A", extractedMeetingFixture(standupWords(), ""), entry)
	if context.Meeting.RoomName != "Alice" || context.Meeting.RoomDisplayName != "Planning" {
		t.Fatalf("%+v", context.Meeting)
	}
	markdown, err := meetingcontext.RenderMarkdown(meetingcontext.Bundle{Meetings: []meetingcontext.MeetingContext{context}}, meetingcontext.RenderOpts{})
	if err != nil || !strings.Contains(markdown, "Room: Planning (`opaque`)") {
		t.Fatalf("%s %v", markdown, err)
	}
	rooms, _ := groupMeetingsByRoom(meetingsListing{Items: []meetingsCatalogItem{{entry: entry}}})
	if len(rooms) != 1 || rooms[0].RoomName != "Alice" || rooms[0].RoomDisplayName != "Planning" || rooms[0].Selector != "opaque" {
		t.Fatalf("%+v", rooms)
	}
}

func TestRoomDisplayExcludedFromPortablePack(t *testing.T) {
	dir := t.TempDir()
	raw := json.RawMessage(`{"id":"A","roomId":"opaque","roomName":"Alice","roomDisplayName":"Local secret alias","extension":{"n":7}}`)
	if err := writeSeedPackCatalog(dir, "cassini.viewer.catalog.v1", []seedPackPlan{{raw: raw}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Meetings []map[string]json.RawMessage `json:"meetings"`
	}
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatal(err)
	}
	e := catalog.Meetings[0]
	if e["roomDisplayName"] != nil || string(e["roomName"]) != `"Alice"` || e["extension"] == nil {
		t.Fatalf("%s", data)
	}
}
