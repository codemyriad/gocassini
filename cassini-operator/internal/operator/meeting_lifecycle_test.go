package operator

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestMeetingLifecycleSurvivesReopenAndConflicts(t *testing.T) {
	file := filepath.Join(t.TempDir(), "jobs.sqlite")
	s, err := OpenStore(file)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	m := meetingLifecycle{Name: "meeting.opus", FileID: 42, Path: ncRecordingsRoot + "/meetings/meeting.opus", Representation: "opus", State: "active", Anchor: "2026-01-01T00:00:00Z", AnchorSource: "recording-completed"}
	if err = s.adoptMeetingLifecycle(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err = s.adoptMeetingLifecycle(ctx, m); err != nil {
		t.Fatal(err)
	}
	m.Anchor = "2026-02-01T00:00:00Z"
	if err = s.adoptMeetingLifecycle(ctx, m); err == nil {
		t.Fatal("age reset accepted")
	}
	m.Anchor = "2026-01-01T00:00:00Z"
	m.FileID = 43
	if err = s.adoptMeetingLifecycle(ctx, m); err == nil {
		t.Fatal("identity replacement accepted")
	}
	if _, err = s.db.Exec(`UPDATE meeting_lifecycle SET representation='transcription',document_path=? WHERE name=?`, ncRecordingsRoot+"/meetings/meeting"+transcriptionSuffix, m.Name); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = OpenStore(file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cfg := ExAppConfig{lifecycle: s}
	rel, err := cfg.currentOwnerMeetingPath(ctx, m.Name)
	if err != nil || rel != ncRecordingsRoot+"/meetings/meeting"+transcriptionSuffix {
		t.Fatalf("locator: %q %v", rel, err)
	}
	if _, err = s.db.Exec(`UPDATE meeting_lifecycle SET state='retiring' WHERE name=?`, m.Name); err != nil {
		t.Fatal(err)
	}
	if _, err = cfg.currentOwnerMeetingPath(ctx, m.Name); !errors.Is(err, errMeetingRetired) {
		t.Fatal("retirement did not deny access", err)
	}
}

func TestRetiredProjectionCannotBeRebuilt(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	ctx := context.Background()
	m := meetingLifecycle{Name: "m.opus", FileID: 42, Path: ncRecordingsRoot + "/meetings/m.opus", Representation: "opus", State: "active", Anchor: "2026-01-01T00:00:00Z", AnchorSource: "recording-completed"}
	if err := rt.store.adoptMeetingLifecycle(ctx, m); err != nil {
		t.Fatal(err)
	}
	metadata, err := openMeetingMetadataStore(filepath.Join(t.TempDir(), "metadata.sqlite"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	metadata.lifecycle = rt.store
	search, err := openSearchStore(filepath.Join(t.TempDir(), "search.sqlite"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer search.Close()
	search.lifecycle = rt.store
	if _, err = rt.store.db.Exec(`UPDATE meeting_lifecycle SET state='retired' WHERE name=?`, m.Name); err != nil {
		t.Fatal(err)
	}
	if err = metadata.Put(ctx, m.FileID, m.Name, []byte(`{"id":"m","title":"expired title","audioPath":"meetings/m.opus"}`)); !errors.Is(err, errMeetingRetired) {
		t.Fatal("metadata resurrection", err)
	}
	if err = search.ReplaceMeeting(ctx, m.Name, "digest", searchRowSourceWords, []searchRow{{Text: "expired words"}}); !errors.Is(err, errMeetingRetired) {
		t.Fatal("search resurrection", err)
	}
	var count int
	if err = search.db.QueryRow(`SELECT COUNT(*) FROM segment_ref`).Scan(&count); err != nil || count != 0 {
		t.Fatal("expired text returned to index", err)
	}
}
