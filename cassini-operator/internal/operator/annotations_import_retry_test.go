package operator

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnnotationBackfillRetriesTransientFailuresBeforeMarkingBuilt(t *testing.T) {
	store := newTestAnnotationStore(t)
	archive := newFakeAnnotationArchive()
	archive.put("JOB1.opus", "c1", hiringFile(t, testTagNamespaceA))
	archive.put("JOB2.opus", "c2", hiringFile(t, testTagNamespaceA))
	archive.readErr["JOB2.opus"] = errors.New("download interrupted")
	if report := archive.rebuild(t, store, "JOB1.opus", "JOB2.opus"); report.Indexed != 1 || report.Failed != 1 {
		t.Fatalf("partial pass: %+v", report)
	}
	if built, err := store.builtOnce(context.Background()); err != nil || built {
		t.Fatalf("partial initial pass marked built: %v %v", built, err)
	}
	delete(archive.readErr, "JOB2.opus")
	if report := archive.rebuild(t, store, "JOB1.opus", "JOB2.opus"); report.Unchanged != 1 || report.Indexed != 1 || report.Failed != 0 {
		t.Fatalf("retry: %+v", report)
	}
	if built, err := store.builtOnce(context.Background()); err != nil || !built {
		t.Fatalf("completed pass not marked built: %v %v", built, err)
	}
}

func TestAnnotationSkippedImportRecoversOnListOrOpen(t *testing.T) {
	for _, trigger := range []string{"list", "open"} {
		t.Run(trigger, func(t *testing.T) {
			nc := newAnnotationsNextcloud(t, "MEETING1.opus")
			store := newTestAnnotationStore(t)
			archive := newFakeAnnotationArchive()
			archive.put("MEETING1.opus", "bb", hiringFile(t, testTagNamespaceA))
			archive.readErr["MEETING1.opus"] = errors.New("temporary download failure")
			if report := archive.rebuild(t, store, "MEETING1.opus"); report.Failed != 1 {
				t.Fatalf("initial import: %+v", report)
			}
			bin := fakeCassini(t, annTestCLIPrints(annTestApplied))
			s, handler := tagChangeService(t, nc.url, bin, store)
			defer s.backgroundWG.Wait()
			if trigger == "list" {
				rec := httptest.NewRecorder()
				cfg := meetingsListConfig(nc.url)
				proxy := cfg.ncFilesProxy(nil, searchDeps{annotations: store, importAnnotations: s.importListedDocuments})
				proxy(rec, callerReq(http.MethodGet, "/published/meetings-list", "alice"), meetingsListPath)
				if rec.Code != http.StatusOK {
					t.Fatalf("listing: %d %s", rec.Code, rec.Body.String())
				}
			} else {
				rec := annTestCall(handler, http.MethodGet, "MEETING1", "alice", "")
				if rec.Code != http.StatusServiceUnavailable {
					t.Fatalf("preparing: %d %s", rec.Code, rec.Body.String())
				}
			}
			s.backgroundWG.Wait()
			rec := annTestCall(handler, http.MethodGet, "MEETING1", "alice", "")
			if rec.Code != http.StatusOK {
				t.Fatalf("recovered: %d %s", rec.Code, rec.Body.String())
			}
			if _, err := store.document(context.Background(), "SECRET.opus"); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("invisible recording imported: %v", err)
			}
		})
	}
}
