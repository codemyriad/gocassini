package operator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRoomNamesRecoverAfterNextcloudStagingCleanup(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	seedTalkJob(t, rt, "old")
	ctx := context.Background()
	file := canonicalOpusPath(rt.cfg.WorkRoot, "old")
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("retained published bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	digest, err := fileSHA256(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.store.db.Exec(`UPDATE jobs SET artifact_site_path='CassiniRecordings' WHERE id='old'`); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.store.db.Exec(`UPDATE job_attempts SET stage='done',state='succeeded',publish_finished_at='2026-10-21',artifact_opus_sha256=? WHERE job_id='old'`, digest); err != nil {
		t.Fatal(err)
	}
	rt.cfg.CassiniBin = filepath.Join(t.TempDir(), "cassini")
	if err := os.WriteFile(rt.cfg.CassiniBin, []byte("#!/bin/sh\n[ \"$1\" = inspect ] && [ \"$2\" = --meeting-identity ] || exit 1\nprintf '%s\\n' '{\"roomId\":\"opaque-existing\",\"jobId\":\"old\",\"attemptNumber\":1}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"id":"old","roomId":"opaque-existing","roomName":"Alice","audioPath":"./meetings/old.opus"}`)
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
	names, err := rt.meetingMetadata.roomNames(ctx, []string{"opaque-existing"})
	if err != nil || names["opaque-existing"].DisplayName != "admin & Alice" || calls != 1 {
		t.Fatalf("names=%+v calls=%d err=%v", names, calls, err)
	}
	job, err := rt.store.GetJob(ctx, "old")
	if err != nil {
		t.Fatal(err)
	}
	if rt.roomEntryHasLocalLineage(ctx, job, roomEntry{ID: "old", RoomID: "imported-other"}) {
		t.Fatal("trusted unrelated imported room")
	}
	if err := os.WriteFile(file, []byte("different unpublished bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if rt.roomEntryHasLocalLineage(ctx, job, roomEntry{ID: "old", RoomID: "opaque-existing"}) {
		t.Fatal("trusted unpublished archive bytes")
	}
}
