package operator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"testing"
)

func openTestAnnotationStore(t *testing.T) *annotationStore {
	t.Helper()
	store, err := openAnnotationStore(filepath.Join(t.TempDir(), "annotations.sqlite3"), log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestNamespaceWaitsForTheFirstRebuild(t *testing.T) {
	ctx := context.Background()
	store := openTestAnnotationStore(t)
	if err := store.Record(ctx, "M.opus", guardTestDoc(1, "tag_h", "hiring")); err != nil {
		t.Fatal(err)
	}
	store.rebuildPending.Store(true)
	if _, err := store.Namespace(ctx); !errors.Is(err, errAnnotationIndexBuilding) {
		t.Fatalf("a pending first rebuild must refuse, got %v", err)
	}
	store.rebuildPending.Store(false)
	if ns, err := store.Namespace(ctx); err != nil || ns != testTagNamespaceA {
		t.Fatalf("once built, the archive's namespace is answered: %q %v", ns, err)
	}
}

func TestBuildMarkerRoundTrips(t *testing.T) {
	ctx := context.Background()
	store := openTestAnnotationStore(t)
	if built, err := store.builtOnce(ctx); err != nil || built {
		t.Fatalf("a new index has never been built: %v %v", built, err)
	}
	if err := store.markBuilt(ctx); err != nil {
		t.Fatal(err)
	}
	if built, err := store.builtOnce(ctx); err != nil || !built {
		t.Fatalf("after markBuilt: %v %v", built, err)
	}
}

func TestInitialAnnotationBuildReturnsFailureForRetryableImports(t *testing.T) {
	nc := newAnnotationsNextcloud(t, "MEETING1.opus")
	store := openTestAnnotationStore(t)
	rt := &Runtime{ctx: context.Background()}
	rt.cfg.DBPath = filepath.Join(t.TempDir(), "jobs.sqlite3")
	metadata, err := openMeetingMetadataStore(meetingMetadataPath(rt.cfg.DBPath), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ name, id string }{{"MEETING1.opus", "MEETING1"}, {"SECRET.opus", "SECRET"}} {
		entry := json.RawMessage(`{"id":"` + row.id + `","audioPath":"./meetings/` + row.name + `"}`)
		if err := metadata.Put(context.Background(), testMeetingFileID(row.name), row.name, entry); err != nil {
			t.Fatal(err)
		}
	}
	_ = metadata.Close()
	rt.cfg.CassiniBin = filepath.Join(t.TempDir(), "not-installed-yet")
	logger := log.New(io.Discard, "", 0)
	if err := rt.buildAnnotationIndexOnce(meetingsListConfig(nc.url), store, logger); err == nil {
		t.Fatal("partial initial build must fail so startup keeps the gate closed and retries")
	}
	if built, err := store.builtOnce(context.Background()); err != nil || built {
		t.Fatalf("partial build marker: %v %v", built, err)
	}
	rt.cfg.CassiniBin = fakeCassini(t, annTestCLIPrints(annTestApplied))
	if err := rt.buildAnnotationIndexOnce(meetingsListConfig(nc.url), store, logger); err != nil {
		t.Fatalf("retry after recovery: %v", err)
	}
	if built, err := store.builtOnce(context.Background()); err != nil || !built {
		t.Fatalf("complete build marker: %v %v", built, err)
	}
}

// buildingIndex is a projection whose first rebuild has not finished.
type buildingIndex struct{}

func (buildingIndex) Record(context.Context, string, annotateResult) error  { return nil }
func (buildingIndex) MarkUnavailable(context.Context, string, string) error { return nil }
func (buildingIndex) ResolveLabel(context.Context, string, []string) (string, bool, error) {
	return "", false, nil
}
func (buildingIndex) TagVisible(context.Context, string, []string) (bool, error) { return true, nil }
func (buildingIndex) Namespace(context.Context) (string, error) {
	return "", errAnnotationIndexBuilding
}

func TestAnnotationsMeetingPOSTWaitsForTheFirstRebuild(t *testing.T) {
	nc := newAnnotationsNextcloud(t, "MEETING1.opus")
	bin := fakeCassini(t, annTestCLIPrints(annTestApplied))
	h, _ := annTestService(t, nc.url, bin, buildingIndex{})

	rec := annTestCall(h, http.MethodPost, "MEETING1", "alice", `{"ops":[{"op":"mark","tag":{"label":"hiring"},"target":{"kind":"meeting"}}]}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503 while the index rebuilds (%s)", rec.Code, rec.Body.String())
	}
	if runs := annTestRuns(t, bin); runs != 0 {
		t.Fatalf("nothing may be rewritten while the index rebuilds; the CLI ran %d time(s)", runs)
	}
}
