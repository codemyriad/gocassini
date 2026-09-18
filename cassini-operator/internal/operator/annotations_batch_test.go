package operator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cassini-operator/internal/operator/appapi"
)

func batchFixture(t *testing.T) (*annotationService, http.Handler, *annotationStore, *annotationsNextcloud) {
	nc, s, h, store := asyncFixture(t)
	nc.frontMu.Lock()
	nc.visible["SECRET.opus"] = true
	nc.frontMu.Unlock()
	empty := annotateResult{Format: annotateResultFormat, AudioOpusSHA256: testAudioDigest, DurationMS: 60000}
	data, _ := json.Marshal(empty)
	nc.seed(annTestSecret, string(data), recordingACLRules(nil, false))
	recordMarks(t, store, "SECRET.opus", empty)
	s.wake = make(chan struct{}, 2)
	return s, h, store, nc
}
func batchCall(h http.Handler, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/annotations/batch", strings.NewReader(body))
	request = request.WithContext(appapi.WithUserID(request.Context(), "alice"))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	return response
}

const batchMark = `{"meetingIds":["MEETING1","SECRET"],"requestId":"batch1","ops":[{"op":"mark","tag":{"label":"new tag"},"target":{"kind":"meeting"}}],"tagStyles":[{"label":"new tag","color":"blue"}]}`

func TestAnnotationBatchCommitReplayAndArchive(t *testing.T) {
	s, h, store, nc := batchFixture(t)
	first := batchCall(h, batchMark)
	if first.Code != 200 {
		t.Fatalf("batch: %d %s", first.Code, first.Body.String())
	}
	var response annotationBatchResponse
	json.Unmarshal(first.Body.Bytes(), &response)
	if len(response.Results) != 2 || len(response.Tags) != 1 || response.Tags[0].Color != "blue" {
		t.Fatalf("response: %+v", response)
	}
	if len(s.wake) != 2 {
		t.Fatal("did not wake both workers")
	}
	if len(nc.ifMatches()) != 0 {
		t.Fatal("POST uploaded recording")
	}
	for _, result := range response.Results {
		if result.Sync.State != "pending" {
			t.Fatalf("not pending: %+v", result)
		}
	}
	vocab, err := store.Vocabulary(context.Background(), []string{"MEETING1.opus", "SECRET.opus"})
	if err != nil || len(vocab) != 1 || vocab[0].Meetings != 2 {
		t.Fatalf("shared identity: %+v %v", vocab, err)
	}
	replay := batchCall(h, batchMark)
	if replay.Code != 200 || replay.Body.String() != first.Body.String() {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
	}
	collision := batchCall(h, strings.ReplaceAll(batchMark, "new tag", "different"))
	if collision.Code != 409 {
		t.Fatalf("collision: %d", collision.Code)
	}
	for _, name := range []string{"MEETING1.opus", "SECRET.opus"} {
		if err := s.syncAnnotation(context.Background(), name); err != nil {
			t.Fatal(err)
		}
		doc, _ := store.document(context.Background(), name)
		if doc.Sync.State != "saved" {
			t.Fatal("not archived")
		}
	}
	if len(nc.ifMatches()) != 2 {
		t.Fatal("expected independent archive uploads")
	}
	// Replay survives restart and returns the original response, not current sync.
	dbPath := store.path
	store.Close()
	reopened, err := openAnnotationStore(dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	_, h = tagChangeService(t, nc.url, snapshotCLI(t), reopened)
	replay = batchCall(h, batchMark)
	if replay.Code != 200 || replay.Body.String() != first.Body.String() {
		t.Fatalf("restart replay: %d %s", replay.Code, replay.Body.String())
	}
}

func TestAnnotationBatchRollbackAndAccess(t *testing.T) {
	for _, failure := range []string{"projection", "denied", "unprepared", "invalid"} {
		t.Run(failure, func(t *testing.T) {
			_, h, store, nc := batchFixture(t)
			before, _ := store.document(context.Background(), "MEETING1.opus")
			body := batchMark
			switch failure {
			case "projection":
				store.db.Exec(`CREATE TRIGGER reject_second BEFORE INSERT ON annotation_item WHEN NEW.opus_name='SECRET.opus' BEGIN SELECT RAISE(ABORT,'injected'); END`)
			case "denied":
				nc.frontMu.Lock()
				delete(nc.visible, "SECRET.opus")
				nc.frontMu.Unlock()
			case "unprepared":
				store.db.Exec(`DELETE FROM annotation_head WHERE opus_name='SECRET.opus'; UPDATE meeting_annotations SET state='unavailable' WHERE opus_name='SECRET.opus'`)
			case "invalid":
				body = strings.Replace(body, `"label":"new tag"`, `"label":" "`, 1)
			}
			result := batchCall(h, body)
			if result.Code == 200 {
				t.Fatal("accepted invalid batch")
			}
			after, _ := store.document(context.Background(), "MEETING1.opus")
			if before.StateToken != after.StateToken {
				t.Fatal("first meeting escaped rollback")
			}
			var n int
			store.db.QueryRow(`SELECT COUNT(*) FROM annotation_batch_receipt`).Scan(&n)
			if n != 0 {
				t.Fatal("receipt escaped rollback")
			}
		})
	}
}

func TestAnnotationBatchValidationAndRetention(t *testing.T) {
	s, h, store, _ := batchFixture(t)
	for _, body := range []string{
		strings.Replace(batchMark, `"batch1"`, `""`, 1),
		strings.Replace(batchMark, `"SECRET"`, `"MEETING1"`, 1),
		strings.Replace(batchMark, `"kind":"meeting"`, `"kind":"time-range","startMs":0,"endMs":100`, 1),
		strings.Replace(batchMark, `"tag":{"label":"new tag"}`, `"tag":null`, 1),
	} {
		if r := batchCall(h, body); r.Code != 400 {
			t.Fatalf("invalid: %d %s", r.Code, r.Body.String())
		}
	}
	if r := batchCall(h, batchMark); r.Code != 200 {
		t.Fatalf("batch: %s", r.Body.String())
	}
	store.db.Exec(`UPDATE annotation_batch_receipt SET created_at=unixepoch()-700000`)
	if err := store.collectSnapshots(context.Background()); err != nil {
		t.Fatal(err)
	}
	var n int
	store.db.QueryRow(`SELECT COUNT(*) FROM annotation_batch_receipt`).Scan(&n)
	if n != 1 {
		t.Fatal("pending batch receipt expired")
	}
	for _, name := range []string{"MEETING1.opus", "SECRET.opus"} {
		if err := s.syncAnnotation(context.Background(), name); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.collectSnapshots(context.Background()); err != nil {
		t.Fatal(err)
	}
	store.db.QueryRow(`SELECT COUNT(*) FROM annotation_batch_receipt`).Scan(&n)
	if n != 0 {
		t.Fatal("settled receipt retained")
	}
	store.db.QueryRow(`SELECT COUNT(*) FROM annotation_batch_target`).Scan(&n)
	if n != 0 {
		t.Fatal("receipt targets retained")
	}
}

func TestAnnotationBatchMigratesVersionFourWithoutLosingPending(t *testing.T) {
	_, h, store, _ := batchFixture(t)
	first := postAsync(t, h, markRequest("pending", "single"))
	store.db.Exec(`DROP TABLE annotation_batch_target; DROP TABLE annotation_batch_receipt; PRAGMA user_version=4`)
	name := store.path
	store.Close()
	next, err := openAnnotationStore(name, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	doc, err := next.document(context.Background(), "MEETING1.opus")
	if err != nil || doc.StateToken != first.StateToken || doc.Sync.State != "pending" {
		t.Fatalf("lost pending state: %+v %v", doc, err)
	}
}
