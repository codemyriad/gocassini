package operator

import (
	"context"
	"log"
	"net/http"
	"path/filepath"
	"testing"
)

// The delete route behind the full catalog resolution: the same 404-for-denial
// discipline as the mark writes, the owner DELETE against the archive, and the
// terminal replay answer.
func TestDeleteMeetingRoute(t *testing.T) {
	ctx := context.Background()
	nc := newAnnotationsNextcloud(t, "MEETING1.opus")

	rt, closeRt := newBareSealRuntime(t)
	defer closeRt()
	rt.retention = newRetentionConfig(filepath.Join(t.TempDir(), "settings.json"))
	annotations, err := openAnnotationStore(filepath.Join(t.TempDir(), "annotations.db"), rt.logger)
	if err != nil {
		t.Fatal(err)
	}
	defer annotations.Close()
	rt.annotations = annotations
	metadata, err := openMeetingMetadataStore(filepath.Join(t.TempDir(), "metadata.db"), rt.logger)
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	rt.meetingMetadata = metadata
	search, err := openSearchStore(filepath.Join(t.TempDir(), "search.db"), rt.logger)
	if err != nil {
		t.Fatal(err)
	}
	defer search.Close()
	rt.searchStore = search

	cfg := testExAppConfig(nc.url)
	cfg.lifecycle = rt.store
	cfg.meetingMetadata = metadata
	if err := metadata.Put(ctx, 42, "MEETING1.opus", []byte(`{"id":"MEETING1","meetingPath":"meetings/MEETING1.opus"}`)); err != nil {
		t.Fatal(err)
	}
	if err := metadata.Put(ctx, 43, "SECRET.opus", []byte(`{"id":"SECRET","meetingPath":"meetings/SECRET.opus"}`)); err != nil {
		t.Fatal(err)
	}

	service := &annotationService{rt: rt, exapp: cfg, client: http.DefaultClient, logger: log.New(testLogWriter{t}, "", 0)}
	mux := http.NewServeMux()
	mux.HandleFunc(annotationsURLPath+"/", service.route)

	if rec := annTestCall(mux, "POST", "MEETING1/delete", "", ""); rec.Code != 502 {
		t.Fatalf("missing identity answered %d", rec.Code)
	}
	if rec := annTestCall(mux, "GET", "MEETING1/delete", "alice", ""); rec.Code != 405 {
		t.Fatalf("GET answered %d", rec.Code)
	}
	// SECRET is in the archive but not shared with alice: the answer must be
	// indistinguishable from a meeting that never existed, and nothing deleted.
	for _, id := range []string{"SECRET", "NEVER"} {
		if rec := annTestCall(mux, "POST", id+"/delete", "alice", ""); rec.Code != 404 {
			t.Fatalf("%s answered %d", id, rec.Code)
		}
	}
	if nc.recording(annTestSecret) == "" {
		t.Fatal("a denial deleted the recording")
	}

	rec := annTestCall(mux, "POST", "MEETING1/delete", "alice", "")
	if rec.Code != 200 {
		t.Fatalf("delete answered %d: %s", rec.Code, rec.Body.String())
	}
	body := annTestDecode(t, rec)
	if string(body["deleted"]) != "true" {
		t.Fatalf("response %s", rec.Body.String())
	}
	if nc.recording(annTestRecording) != "" {
		t.Fatal("recording still in the owner archive")
	}
	if err := cfg.meetingNotRetired(ctx, "MEETING1.opus"); err == nil {
		t.Fatal("no tombstone after delete")
	}
	var rows int
	if err := metadata.db.QueryRow(`SELECT COUNT(*) FROM meeting_metadata WHERE opus_name='MEETING1.opus'`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("metadata rows=%d err=%v", rows, err)
	}
	// A replay: the meeting is out of every visible set, so the terminal
	// answer is the same 404 as for a meeting that never existed.
	if rec := annTestCall(mux, "POST", "MEETING1/delete", "alice", ""); rec.Code != 404 {
		t.Fatalf("replay answered %d", rec.Code)
	}
}

type testLogWriter struct{ t *testing.T }

func (w testLogWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}
