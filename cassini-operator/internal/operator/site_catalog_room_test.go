package operator

import (
	"encoding/json"
	"testing"
)

// The room's identity is split across two homes on purpose (D-640):
//
//	roomId   ─ in the .opus, so the exporter re-derives it on every republish
//	roomName ─ in the catalog only, because a display name is editable and a
//	           published recording is not
//
// That split only works if the catalog end of it survives a republish. These
// tests are that guarantee.

func catalogFrom(t *testing.T, entries ...map[string]any) siteCatalog {
	t.Helper()
	catalog := siteCatalog{Version: "cassini.viewer.catalog.v1"}
	for _, entry := range entries {
		raw, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("marshal entry: %v", err)
		}
		catalog.Meetings = append(catalog.Meetings, raw)
	}
	return catalog
}

func catalogEntryFields(t *testing.T, catalog siteCatalog, id string) map[string]any {
	t.Helper()
	for _, raw := range catalog.Meetings {
		var fields map[string]any
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatalf("parse merged entry: %v", err)
		}
		if fields["id"] == id {
			return fields
		}
	}
	t.Fatalf("no entry with id %q in the merged catalog", id)
	return nil
}

func TestUpsertStampsTheOperatorsCurrentRoomName(t *testing.T) {
	existing := catalogFrom(t, map[string]any{"id": "job-1", "title": "Weekly Sync", "roomId": "rm_9f2a1c3d4e5b6a70"})
	// What the exporter produces after D-640: a room id out of the file and no
	// name, because the artifact no longer carries one.
	incoming := catalogFrom(t, map[string]any{"id": "job-1", "title": "Weekly Sync", "roomId": "rm_9f2a1c3d4e5b6a70"})

	merged, err := upsertSiteCatalog(existing, incoming, catalogEntryOverlay{RoomName: "Weekly Sync"})
	if err != nil {
		t.Fatalf("upsertSiteCatalog: %v", err)
	}
	if got := catalogEntryFields(t, merged, "job-1")["roomName"]; got != "Weekly Sync" {
		t.Errorf("roomName = %v, want the operator's current name", got)
	}
}

func TestUpsertLetsARenameReachTheArchiveWithoutTouchingAnyArtifact(t *testing.T) {
	// The point of moving the name out of the file: renaming a Talk room is one
	// catalog write on that room's next publish, not a rewrite of every
	// recording it ever produced.
	existing := catalogFrom(t, map[string]any{"id": "job-1", "roomId": "rm_9f2a1c3d4e5b6a70", "roomName": "Weekly Sync"})
	incoming := catalogFrom(t, map[string]any{"id": "job-1", "roomId": "rm_9f2a1c3d4e5b6a70"})

	merged, err := upsertSiteCatalog(existing, incoming, catalogEntryOverlay{RoomName: "Monday Sync"})
	if err != nil {
		t.Fatalf("upsertSiteCatalog: %v", err)
	}
	if got := catalogEntryFields(t, merged, "job-1")["roomName"]; got != "Monday Sync" {
		t.Errorf("roomName = %v, want the renamed %q", got, "Monday Sync")
	}
}

func TestUpsertCarriesTheRoomNameAcrossARepublishWithNoOverlay(t *testing.T) {
	// A rerun whose room lookup failed, or a re-seal of an old meeting: the
	// operator has nothing to say, and must not therefore erase what an earlier
	// publish resolved.
	existing := catalogFrom(t, map[string]any{"id": "job-1", "roomId": "rm_9f2a1c3d4e5b6a70", "roomName": "Weekly Sync"})
	incoming := catalogFrom(t, map[string]any{"id": "job-1", "roomId": "rm_9f2a1c3d4e5b6a70"})

	merged, err := upsertSiteCatalog(existing, incoming, catalogEntryOverlay{})
	if err != nil {
		t.Fatalf("upsertSiteCatalog: %v", err)
	}
	if got := catalogEntryFields(t, merged, "job-1")["roomName"]; got != "Weekly Sync" {
		t.Errorf("roomName = %v, want the name an earlier publish resolved", got)
	}
}

func TestUpsertCarriesABackfilledRoomIDAcrossARepublish(t *testing.T) {
	// The D-640 regression this closes: a backfill recovers an old meeting's
	// real room id into the catalog, then a re-seal republishes it from an
	// artifact that was never re-tagged and the recovery is silently undone.
	//
	// Re-tagging the artifact is the real fix and the backfill does it; this is
	// the belt to that pair of braces, for an archive backfilled with
	// --no-retag or by an older run.
	existing := catalogFrom(t, map[string]any{"id": "job-1", "roomId": "rm_9f2a1c3d4e5b6a70", "roomName": "Weekly Sync"})
	incoming := catalogFrom(t, map[string]any{"id": "job-1", "title": "Weekly Sync"})

	merged, err := upsertSiteCatalog(existing, incoming, catalogEntryOverlay{})
	if err != nil {
		t.Fatalf("upsertSiteCatalog: %v", err)
	}
	if got := catalogEntryFields(t, merged, "job-1")["roomId"]; got != "rm_9f2a1c3d4e5b6a70" {
		t.Errorf("roomId = %v, want the backfilled id carried across the republish", got)
	}
}

func TestUpsertLetsTheArtifactWinOverWhatWasCarriedForward(t *testing.T) {
	// A re-tagged artifact must be able to CHANGE a room, not merely fill a
	// blank one — otherwise the first id ever published would pin the meeting
	// forever, and a reattribution followed by a republish could never land.
	existing := catalogFrom(t, map[string]any{"id": "job-1", "roomId": "rm_1111111111111111", "roomName": "Old"})
	incoming := catalogFrom(t, map[string]any{"id": "job-1", "roomId": "rm_2222222222222222"})

	merged, err := upsertSiteCatalog(existing, incoming, catalogEntryOverlay{})
	if err != nil {
		t.Fatalf("upsertSiteCatalog: %v", err)
	}
	fields := catalogEntryFields(t, merged, "job-1")
	if fields["roomId"] != "rm_2222222222222222" {
		t.Errorf("roomId = %v, want the freshly derived id from the file", fields["roomId"])
	}
	if fields["roomName"] != "Old" {
		t.Errorf("roomName = %v, want the catalog-only name still carried", fields["roomName"])
	}
}

func TestUpsertLeavesAnEntryByteIdenticalWhenNothingApplies(t *testing.T) {
	// The archive is carried as opaque JSON so a field this code has never
	// heard of survives. Re-marshalling every entry on every publish would
	// churn key order across the whole file and make a hand diff useless, so
	// the common case must not touch the bytes.
	raw := json.RawMessage(`{"id":"job-1","zzz_future":"keep","title":"Weekly Sync"}`)
	incoming := siteCatalog{Version: "v1", Meetings: []json.RawMessage{raw}}

	merged, err := upsertSiteCatalog(siteCatalog{}, incoming, catalogEntryOverlay{})
	if err != nil {
		t.Fatalf("upsertSiteCatalog: %v", err)
	}
	if string(merged.Meetings[0]) != string(raw) {
		t.Errorf("entry was rewritten:\n got %s\nwant %s", merged.Meetings[0], raw)
	}
}

func TestUpsertKeepsUnknownFieldsWhenItDoesRewriteAnEntry(t *testing.T) {
	incoming := catalogFrom(t, map[string]any{"id": "job-1", "zzz_future": "keep", "speakerCount": 3.0})

	merged, err := upsertSiteCatalog(siteCatalog{}, incoming, catalogEntryOverlay{RoomName: "Weekly Sync"})
	if err != nil {
		t.Fatalf("upsertSiteCatalog: %v", err)
	}
	fields := catalogEntryFields(t, merged, "job-1")
	if fields["zzz_future"] != "keep" {
		t.Errorf("unknown field dropped by the overlay rewrite: %+v", fields)
	}
	if fields["speakerCount"] != 3.0 {
		t.Errorf("speakerCount = %v, want 3", fields["speakerCount"])
	}
}

func TestUpsertSurvivesAMalformedEntryAlreadyInTheArchive(t *testing.T) {
	// A malformed entry already published is not a reason to fail the publish:
	// the incoming entry is well-formed and replacing it is an improvement.
	// (It still needs a readable id — that is what it is keyed by.)
	existing := siteCatalog{Meetings: []json.RawMessage{json.RawMessage(`{"id":"job-1","roomName":`)}}
	existing.Meetings[0] = json.RawMessage(`{"id":"job-1","roomName":[1,2,3]}`)
	incoming := catalogFrom(t, map[string]any{"id": "job-1", "roomId": "rm_9f2a1c3d4e5b6a70"})

	merged, err := upsertSiteCatalog(existing, incoming, catalogEntryOverlay{})
	if err != nil {
		t.Fatalf("upsertSiteCatalog: %v", err)
	}
	fields := catalogEntryFields(t, merged, "job-1")
	if fields["roomId"] != "rm_9f2a1c3d4e5b6a70" {
		t.Errorf("roomId = %v, want the incoming entry to have replaced the malformed one", fields["roomId"])
	}
	// A non-string roomName reads as absent rather than as an error, so nothing
	// is carried forward from it.
	if _, ok := fields["roomName"]; ok {
		t.Errorf("roomName = %v, want nothing carried from a non-string value", fields["roomName"])
	}
}
