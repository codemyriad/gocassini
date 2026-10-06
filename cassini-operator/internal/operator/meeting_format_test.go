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

func TestJSONAnnotationQueuePersistsAndConfirms(t *testing.T) {
	nc := newAnnotationsNextcloud(t, "MEETING1.json")
	store := newTestAnnotationStore(t)
	empty := annotateResult{Format: annotateResultFormat, AudioOpusSHA256: testAudioDigest, DurationMS: 60000}
	raw, _ := json.Marshal(empty)
	const rel = ncRecordingsRoot + "/meetings/MEETING1.json"
	nc.seed(rel, string(raw), nil)
	recordMarks(t, store, "MEETING1.json", empty)
	s, h := tagChangeService(t, nc.url, snapshotCLI(t), store)
	metadata, err := openMeetingMetadataStore(filepath.Join(t.TempDir(), meetingMetadataFilename), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	s.exapp.meetingMetadata = metadata
	if err := s.exapp.meetingMetadata.Put(context.Background(), 42, "MEETING1.json", json.RawMessage(`{"id":"MEETING1","title":"Meeting","meetingPath":"./meetings/MEETING1.json"}`)); err != nil {
		t.Fatal(err)
	}
	first := postAsync(t, h, markRequest("one", "json-1"))
	last := postAsync(t, h, markRequest("two", "json-2"))
	if first.Sync.State != "pending" || last.Sync.Desired <= first.Sync.Desired || len(nc.ifMatches()) != 0 {
		t.Fatal("request did not queue durably")
	}
	if err := s.syncAnnotation(context.Background(), "MEETING1.json"); err != nil {
		t.Fatal(err)
	}
	got, err := store.document(context.Background(), "MEETING1.json")
	if err != nil {
		t.Fatal(err)
	}
	if got.Sync.State != "saved" || !sameAnnotationDocument(got.Annotations, last.Annotations) {
		t.Fatalf("not confirmed: %+v", got)
	}
	var archived annotateResult
	if err := json.Unmarshal([]byte(nc.recording(rel)), &archived); err != nil {
		t.Fatal(err)
	}
	if !sameAnnotationDocument(archived.Annotations, last.Annotations) {
		t.Fatal("wrong archive snapshot")
	}
	replay := postAsync(t, h, markRequest("one", "json-1"))
	if replay.StateToken != first.StateToken {
		t.Fatal("idempotent receipt lost")
	}
}

func TestMeetingFormatUpgradeKeepsLegacyAttempts(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	if err := rt.store.migrateDownTo(13); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"queued", "failed", "succeeded"} {
		insertJob(t, rt.store.db, state, nowUTCString())
		if _, err := rt.store.db.Exec(`UPDATE job_attempts SET state=? WHERE job_id=?`, state, state); err != nil {
			t.Fatal(err)
		}
	}
	if err := rt.store.ensureSchema(); err != nil {
		t.Fatal(err)
	}
	rt.cfg.MeetingFormat = "json"
	for _, id := range []string{"queued", "failed", "succeeded"} {
		if err := rt.enqueueSealJobNonBlocking(id, 2, "unused", "unused", nowUTCString()); err != nil {
			t.Fatal(err)
		}
		for _, attempt := range []int{1, 2} {
			var format string
			if err := rt.store.db.QueryRow(`SELECT format FROM meeting_format WHERE job_id=? AND attempt_number=?`, id, attempt).Scan(&format); err != nil || format != "opus" {
				t.Fatalf("%s attempt %d: %q %v", id, attempt, format, err)
			}
		}
	}
}

func TestMeetingFormatConfiguration(t *testing.T) {
	t.Setenv("CASSINI_REPO_ROOT", makeFakeOperatorRepoRoot(t))
	for _, tc := range []struct {
		name, env string
		args      []string
		want      string
		invalid   bool
	}{
		{name: "default", want: "opus"},
		{name: "json environment", env: "json", want: "json"},
		{name: "flag precedence", env: "json", args: []string{"--meeting-format=opus"}, want: "opus"},
		{name: "unsupported", env: "mp3", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CASSINI_MEETING_FORMAT", tc.env)
			cfg, code, err := loadConfig(tc.args, ioDiscard{})
			if tc.invalid {
				if err == nil || code != 2 {
					t.Fatalf("accepted invalid format: %d %v", code, err)
				}
				return
			}
			if err != nil || code != 0 || cfg.MeetingFormat != tc.want {
				t.Fatalf("format %q code %d error %v", cfg.MeetingFormat, code, err)
			}
		})
	}
}
