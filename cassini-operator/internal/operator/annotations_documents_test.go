package operator

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func TestAnnotationDocumentsMigrateAndRetainGeneration(t *testing.T) {
	ctx := context.Background()
	name := filepath.Join(t.TempDir(), "annotations.sqlite3")
	old, err := openSidecarDB(name, "legacy", annotationsSchemaSQL, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.Exec(`INSERT INTO meeting_annotations(opus_name,state) VALUES('old.opus','indexed'); INSERT INTO annotations_meta VALUES('built','yes')`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	store, err := openAnnotationStore(name, nil)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM meeting_annotations`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("legacy rows: %d %v", n, err)
	}
	if built, err := store.builtOnce(ctx); err != nil || built {
		t.Fatalf("full documents need import: %v %v", built, err)
	}
	var generation string
	store.db.QueryRow(`SELECT value FROM annotations_meta WHERE key='generation'`).Scan(&generation)
	var result annotateResult
	if err := json.Unmarshal([]byte(annTestApplied), &result); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(ctx, "old.opus", result); err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = openAnnotationStore(name, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var reopened string
	store.db.QueryRow(`SELECT value FROM annotations_meta WHERE key='generation'`).Scan(&reopened)
	if generation == "" || generation != reopened {
		t.Fatal("generation changed on restart")
	}
	got, err := store.document(ctx, "old.opus")
	if err != nil || string(got.Annotations) != string(result.Annotations) {
		t.Fatalf("full snapshot lost: %+v %v", got, err)
	}
}

func TestAnnotationReadImportsOlderRecording(t *testing.T) {
	nc := newAnnotationsNextcloud(t, "MEETING1.opus")
	store, err := openAnnotationStore(filepath.Join(t.TempDir(), "annotations.sqlite3"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bin := fakeCassini(t, annTestCLIPrints(annTestApplied))
	h, _ := annTestService(t, nc.url, bin, store)
	rec := annTestCall(h, http.MethodGet, "MEETING1", "alice", "")
	if rec.Code != 503 {
		t.Fatalf("unhydrated response: %d %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := store.document(context.Background(), "MEETING1.opus"); err == nil {
			rec = annTestCall(h, http.MethodGet, "MEETING1", "alice", "")
			if rec.Code != 200 {
				t.Fatalf("hydrated: %d %s", rec.Code, rec.Body.String())
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("import did not complete")
}
