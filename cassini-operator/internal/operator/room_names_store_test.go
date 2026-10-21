package operator

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestRoomNamesMigrationPreservesOriginalsAndFutureSchema(t *testing.T) {
	file := filepath.Join(t.TempDir(), "meetings.sqlite3")
	old, err := openSidecarDB(file, "fixture", meetingMetadataSchema, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"id":"A","audioPath":"./meetings/A.opus","roomId":"rm_one","roomName":"Alice","extension":{"n":1}}`
	if _, err := old.db.Exec(`INSERT INTO meeting_metadata VALUES(1,'A.opus','2026-10-21',?,'now')`, raw); err != nil {
		t.Fatal(err)
	}
	old.Close()
	s, err := openMeetingMetadataStore(file, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	got, err := s.EntriesFor(ctx, []int64{1})
	if err != nil || string(got[1]) != raw {
		t.Fatalf("original changed: %s %v", got[1], err)
	}
	if err := s.observeRoom(ctx, "rm_one", "admin & Alice", "participants", "2026-10-21"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE room_names SET custom_name='Planning'`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = openMeetingMetadataStore(file, nil)
	if err != nil {
		t.Fatal(err)
	}
	names, err := s.roomNames(ctx, []string{"rm_one"})
	if err != nil || names["rm_one"].DisplayName != "Planning" {
		t.Fatalf("restart: %v %v", names, err)
	}
	if _, err := s.db.Exec(`PRAGMA user_version=99`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if broken, err := openMeetingMetadataStore(file, nil); err == nil {
		broken.Close()
		t.Fatal("future schema accepted")
	}
	check, err := openSidecarAt(file, "check")
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var name string
	if err := check.db.QueryRow(`SELECT custom_name FROM room_names`).Scan(&name); err != nil || name != "Planning" {
		t.Fatalf("future DB destroyed: %q %v", name, err)
	}
}

func TestRoomNamesDefaultsProjectionAndRebuild(t *testing.T) {
	s, err := openMeetingMetadataStore(filepath.Join(t.TempDir(), "meetings.sqlite3"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	raw := json.RawMessage(`{"id":"A","audioPath":"./meetings/A.opus","roomId":"one","roomName":"Alice"}`)
	if err := s.Put(ctx, 1, "A.opus", raw); err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct{ name, source, at string }{
		{"admin & Alice", "participants", "2026-10-21"},
		{"Alice", "talk", "2026-10-22"},
		{"old pair", "participants", "2026-01-01"},
	} {
		if err := s.observeRoom(ctx, "one", v.name, v.source, v.at); err != nil {
			t.Fatal(err)
		}
	}
	names, err := s.roomNames(ctx, []string{"one"})
	if err != nil || names["one"].DefaultName != "admin & Alice" {
		t.Fatalf("default degraded: %+v %v", names, err)
	}
	projected, err := s.projectRoomNames(ctx, []json.RawMessage{raw})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	json.Unmarshal(projected[0], &fields)
	if fields["roomName"] != "Alice" || fields["roomDisplayName"] != "admin & Alice" {
		t.Fatal(fields)
	}
	got, _ := s.EntriesFor(ctx, []int64{1})
	if string(got[1]) != string(raw) {
		t.Fatal("projection persisted")
	}
	if _, err := s.db.Exec(`DELETE FROM meeting_metadata`); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, 22, "A.opus", raw); err != nil {
		t.Fatal(err)
	}
	names, _ = s.roomNames(ctx, []string{"one"})
	if names["one"].DefaultName != "admin & Alice" {
		t.Fatal("rebuild lost default")
	}
}

func TestRoomDefaultUsesConversationSnapshot(t *testing.T) {
	pair := oneToOneTitleParticipants([]participantRow{{ActorType: "users", ActorID: "alice", DisplayName: "Alice"}, {ActorType: "users", ActorID: "admin", DisplayName: "admin"}})
	name, source := roomDefault(talkRoomInfo{Name: "Alice", Type: 1, TitleParticipants: pair})
	if name != "admin & Alice" || source != "participants" {
		t.Fatalf("%q %s", name, source)
	}
	for _, typ := range []int{2, 3} {
		name, source := roomDefault(talkRoomInfo{Name: "Design", Type: typ, TitleParticipants: pair})
		if name != "Design" || source != "talk-group" {
			t.Fatalf("%q %s", name, source)
		}
	}
}

func TestRoomNamesReconcileLegacyAndRejectUnrelatedIdentity(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	seedTalkJob(t, rt, "old")
	site := t.TempDir()
	raw := json.RawMessage(`{"id":"old","roomId":"opaque-existing","roomName":"Alice","audioPath":"./meetings/old.opus"}`)
	if err := writeSiteCatalog(site, siteCatalog{Version: "cassini.viewer.catalog.v1", Meetings: []json.RawMessage{raw}}); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.store.db.Exec(`UPDATE jobs SET artifact_site_path=? WHERE id='old'`, site); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.store.db.Exec(`UPDATE job_attempts SET stage='done',state='succeeded' WHERE job_id='old'`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := rt.meetingMetadata.Put(ctx, 1, "old.opus", raw); err != nil {
		t.Fatal(err)
	}
	calls := 0
	rt.fetchTalkRoomName = func(context.Context, string, string) (talkRoomInfo, error) {
		calls++
		return talkRoomInfo{Name: "Alice", Type: 1, TitleParticipants: []talkTitleParticipant{{ID: "admin", Name: "admin"}, {ID: "alice", Name: "Alice"}}}, nil
	}
	if err := rt.reconcileRoomNames(ctx); err != nil {
		t.Fatal(err)
	}
	names, _ := rt.meetingMetadata.roomNames(ctx, []string{"opaque-existing"})
	if calls != 1 || names["opaque-existing"].DisplayName != "admin & Alice" {
		t.Fatalf("calls=%d names=%+v", calls, names)
	}
	if err := rt.reconcileRoomNames(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("resolved room fetched again")
	}
	// Same imported meeting/job id but a different opaque room is not lineage.
	other := json.RawMessage(`{"id":"old","roomId":"imported-other","roomName":"Other","audioPath":"./meetings/old.opus"}`)
	if err := rt.meetingMetadata.Put(ctx, 2, "old.opus", other); err != nil {
		t.Fatal(err)
	}
	if err := rt.reconcileRoomNames(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("imported identity trusted as local lineage")
	}
}
