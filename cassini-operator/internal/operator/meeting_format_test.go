package operator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMeetingFormatPinnedBeforeSealAndAcrossReruns(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	insertJob(t, rt.store.db, "json-meeting", nowUTCString())
	rt.cfg.MeetingFormat = "json"
	if err := rt.enqueueSealJobNonBlocking("json-meeting", 1, "unused", "unused", nowUTCString()); err != nil {
		t.Fatal(err)
	}
	rt.cfg.MeetingFormat = "opus"
	if err := rt.enqueueSealJobNonBlocking("json-meeting", 1, "unused", "unused", nowUTCString()); err != nil {
		t.Fatal(err)
	}
	if err := rt.enqueueSealJobNonBlocking("json-meeting", 2, "unused", "unused", nowUTCString()); err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []int{1, 2} {
		var format string
		if err := rt.store.db.QueryRow(`SELECT format FROM meeting_format WHERE job_id=? AND attempt_number=?`, "json-meeting", attempt).Scan(&format); err != nil || format != "json" {
			t.Fatalf("attempt %d: %s %v", attempt, format, err)
		}
	}
	insertJob(t, rt.store.db, "audio-meeting", nowUTCString())
	if err := rt.enqueueSealJobNonBlocking("audio-meeting", 1, "unused", "unused", nowUTCString()); err != nil {
		t.Fatal(err)
	}
	var format string
	rt.store.db.QueryRow(`SELECT format FROM meeting_format WHERE job_id='audio-meeting'`).Scan(&format)
	if format != "opus" {
		t.Fatal(format)
	}
}

func TestJSONPublishedPairPromotion(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	const id = "json-pair"
	insertJob(t, rt.store.db, id, nowUTCString())
	meeting := attemptMeetingPath(rt.cfg.WorkRoot, id, 1)
	if err := os.MkdirAll(meeting, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(attemptSealDir(rt.cfg.WorkRoot, id, 1), id+".json")
	os.MkdirAll(filepath.Dir(file), 0700)
	os.WriteFile(file, []byte(`{"kind":"cassini-transcription"}`), 0600)
	digest, _ := fileSHA256(file)
	if _, err := rt.store.db.Exec(`UPDATE job_attempts SET state='succeeded',stage='done',artifact_opus_path=?,artifact_opus_sha256=?,publish_finished_at=? WHERE job_id=?`, file, digest, nowUTCString(), id); err != nil {
		t.Fatal(err)
	}
	if err := rt.promotePublishedPair(id, 1); err != nil {
		t.Fatal(err)
	}
	job, err := rt.store.GetJob(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(currentRoot(rt.cfg.WorkRoot), id+".json")
	if job.ArtifactOpusPath == nil || *job.ArtifactOpusPath != want {
		t.Fatalf("wrong published path: %+v", job.ArtifactOpusPath)
	}
	got, err := fileSHA256(want)
	if err != nil || got != digest {
		t.Fatalf("bad promotion %s %v", got, err)
	}
}

func TestMixedCatalogDiscoveryAndAssets(t *testing.T) {
	raw := []byte(`{"meetings":[{"id":"audio","audioPath":"./meetings/audio.opus"},{"id":"text","meetingPath":"./meetings/text.json"}]}`)
	entries, err := decodeCatalogEntries(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[1].opusName != "text.json" {
		t.Fatalf("%+v", entries)
	}
	assets, err := catalogEntryAssets(json.RawMessage(`{"id":"text","meetingPath":"./meetings/text.json"}`))
	if err != nil || len(assets) != 1 || assets[0] != "meetings/text.json" {
		t.Fatalf("%v %v", assets, err)
	}
}
