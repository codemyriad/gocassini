package operator

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnnotationRepublishReconcilesRepairedAndChangedAudio(t *testing.T) {
	for _, changed := range []bool{false, true} {
		for _, damaged := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{false: "same", true: "changed"}[changed], map[bool]string{false: "readable", true: "damaged"}[damaged]}, "/"), func(t *testing.T) {
				nc, s, h, store := asyncFixture(t)
				ctx := context.Background()
				postAsync(t, h, markRequest("archived", "first"))
				if err := s.syncAnnotation(ctx, "MEETING1.opus"); err != nil {
					t.Fatal(err)
				}
				latest := postAsync(t, h, markRequest("pending", "second"))
				if damaged {
					nc.seed(annTestRecording, "truncated", recordingACLRules(nil, false))
				}
				sealed := annotateResult{Format: annotateResultFormat, AudioOpusSHA256: testAudioDigest, DurationMS: 60000}
				if changed {
					sealed.AudioOpusSHA256 = strings.Repeat("f", 64)
				}
				data, _ := json.Marshal(sealed)
				local := filepath.Join(t.TempDir(), "sealed.opus")
				if err := os.WriteFile(local, data, 0600); err != nil {
					t.Fatal(err)
				}
				sink := &nextcloudFilesPublishSink{cfg: s.exapp, client: s.client, cassiniBin: s.bin, rt: s.rt}
				state, err := s.exapp.davPropfindLeafState(ctx, s.client, ncRecordingsOwner, annTestRecording)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = sink.putOverDeliveredCopy(ctx, upload{local: local, remote: annTestRecording, size: int64(len(data))}, state, true); err != nil {
					t.Fatal(err)
				}
				got, err := store.document(ctx, "MEETING1.opus")
				if err != nil {
					t.Fatal(err)
				}
				if changed {
					if got.Sync.State != "saved" || !sameAnnotationDocument(got.Annotations, nil) || got.AudioOpusSHA256 != sealed.AudioOpusSHA256 {
						t.Fatalf("old annotations survived replacement: %+v", got)
					}
					tags, err := store.Vocabulary(ctx, []string{"MEETING1.opus"})
					if err != nil || len(tags) != 0 {
						t.Fatalf("stale query rows: %+v %v", tags, err)
					}
				} else if got.Sync.State != "pending" || !sameAnnotationDocument(got.Annotations, latest.Annotations) {
					t.Fatalf("repair lost latest desired: %+v", got)
				}
				if err := s.syncAnnotation(ctx, "MEETING1.opus"); err != nil {
					t.Fatal(err)
				}
				got, _ = store.document(ctx, "MEETING1.opus")
				var remote annotateResult
				json.Unmarshal([]byte(nc.recording(annTestRecording)), &remote)
				if got.Sync.State != "saved" || !sameAnnotationDocument(remote.Annotations, got.Annotations) {
					t.Fatal("DB and repaired recording did not converge")
				}
			})
		}
	}
}

func TestAnnotationCleanRepublishRecoversLostUploadResponse(t *testing.T) {
	nc, s, h, store := asyncFixture(t)
	ctx := context.Background()
	latest := postAsync(t, h, markRequest("archived", "first"))
	if err := s.syncAnnotation(ctx, "MEETING1.opus"); err != nil {
		t.Fatal(err)
	}
	nc.seed(annTestRecording, "truncated", recordingACLRules(nil, false))
	data, _ := json.Marshal(annotateResult{Format: annotateResultFormat, AudioOpusSHA256: testAudioDigest, DurationMS: 60000})
	local := filepath.Join(t.TempDir(), "sealed.opus")
	if err := os.WriteFile(local, data, 0600); err != nil {
		t.Fatal(err)
	}
	transport := s.client.Transport
	s.client.Transport = annotationRoundTripper(func(r *http.Request) (*http.Response, error) {
		resp, err := transport.RoundTrip(r)
		if r.Method == http.MethodPut && err == nil {
			drainClose(resp.Body)
			return nil, errors.New("lost PUT response")
		}
		return resp, err
	})
	sink := &nextcloudFilesPublishSink{cfg: s.exapp, client: s.client, cassiniBin: s.bin, rt: s.rt}
	if err := sink.putAssetBytes(ctx, upload{local: local, remote: annTestRecording, size: int64(len(data))}, true, ""); err == nil {
		t.Fatal("expected lost response")
	}
	s.client.Transport = transport
	// Reopen the durable store, with no in-memory wake-up or publish callback.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openAnnotationStore(store.path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	s.rt.annotations = reopened
	got, _ := reopened.document(ctx, "MEETING1.opus")
	if got.Sync.State != "pending" {
		t.Fatalf("unknown replacement reported saved: %+v", got.Sync)
	}
	name, err := s.nextAnnotation()
	if err != nil || name != "MEETING1.opus" {
		t.Fatalf("restart skipped clean repair: %q %v", name, err)
	}
	s.claimed.Delete(name)
	if err := s.syncAnnotation(ctx, name); err != nil {
		t.Fatal(err)
	}
	got, _ = reopened.document(ctx, name)
	if got.Sync.State != "saved" || !sameAnnotationDocument(got.Annotations, latest.Annotations) {
		t.Fatalf("lost acknowledged tags: %+v", got)
	}
}

func TestAnnotationRepublishMigrationPreservesPendingHead(t *testing.T) {
	_, _, h, store := asyncFixture(t)
	latest := postAsync(t, h, markRequest("pending", "first"))
	if _, err := store.db.Exec(`ALTER TABLE annotation_head DROP COLUMN republish_json; PRAGMA user_version=5`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openAnnotationStore(store.path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.document(context.Background(), "MEETING1.opus")
	if err != nil {
		t.Fatal(err)
	}
	if got.StateToken != latest.StateToken || got.Sync.State != "pending" || !sameAnnotationDocument(got.Annotations, latest.Annotations) {
		t.Fatalf("migration changed pending head: %+v", got)
	}
}
