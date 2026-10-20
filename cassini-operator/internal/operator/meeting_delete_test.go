package operator

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// deleteTestFixture is the stub owner archive for user-delete tests: one leaf
// whose identity and ETag the scenarios bend.
type deleteTestFixture struct {
	service     *annotationService
	rt          *Runtime
	annotations *annotationStore
	exists      *bool
	deletes     *int
}

func newMeetingDeleteFixture(t *testing.T, ext string) deleteTestFixture {
	t.Helper()
	rt, closeRt := newBareSealRuntime(t)
	t.Cleanup(closeRt)
	rt.retention = newRetentionConfig(filepath.Join(t.TempDir(), "settings.json"))
	annotations, err := openAnnotationStore(filepath.Join(t.TempDir(), "annotations.db"), rt.logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { annotations.Close() })
	rt.annotations = annotations
	metadata, err := openMeetingMetadataStore(filepath.Join(t.TempDir(), "metadata.db"), rt.logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { metadata.Close() })
	rt.meetingMetadata = metadata
	search, err := openSearchStore(filepath.Join(t.TempDir(), "search.db"), rt.logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { search.Close() })
	rt.searchStore = search
	exists := true
	deletes := 0
	etag := `"original"`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !exists {
			w.WriteHeader(404)
			return
		}
		switch r.Method {
		case "PROPFIND":
			w.WriteHeader(207)
			fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:response><d:href>%s</d:href><d:propstat><d:status>HTTP/1.1 200 OK</d:status><d:prop><oc:fileid>42</oc:fileid><d:getetag>%s</d:getetag><d:getcontentlength>5</d:getcontentlength></d:prop></d:propstat></d:response></d:multistatus>`, r.URL.Path, etag)
		case "DELETE":
			if r.Header.Get("If-Match") != etag {
				w.WriteHeader(412)
				return
			}
			deletes++
			exists = false
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected method %s", r.Method)
			w.WriteHeader(405)
		}
	}))
	t.Cleanup(server.Close)
	cfg := testExAppConfig(server.URL)
	cfg.lifecycle = rt.store
	cfg.meetingMetadata = metadata
	return deleteTestFixture{
		service:     &annotationService{rt: rt, exapp: cfg, client: server.Client()},
		rt:          rt,
		annotations: annotations,
		exists:      &exists,
		deletes:     &deletes,
	}
}

// A user delete adopts the lifecycle row on demand (no sweep ever ran, and —
// as for an imported meeting — no job row exists), tombstones with reason
// "deleted", moves the file out of the owner's folder, scrubs every
// projection, and answers replays as already done.
func TestPrepareMeetingDeletion(t *testing.T) {
	for _, ext := range []string{".opus", ".json"} {
		t.Run(ext, func(t *testing.T) {
			f := newMeetingDeleteFixture(t, ext)
			ctx := context.Background()
			name := "m" + ext
			if err := f.rt.meetingMetadata.Put(ctx, 42, name, []byte(`{"id":"m","meetingPath":"meetings/`+name+`"}`)); err != nil {
				t.Fatal(err)
			}
			if err := f.rt.searchStore.ReplaceMeeting(ctx, name, "digest", searchRowSourceWords, []searchRow{{Text: "secret words"}}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.annotations.db.Exec(`INSERT INTO meeting_annotations(opus_name,state) VALUES(?,'ready')`, name); err != nil {
				t.Fatal(err)
			}
			if _, err := f.annotations.db.Exec(`INSERT INTO annotation_receipt(caller,request_id,opus_name,request_hash,response,snapshot) VALUES('alice','r1',?,'hash',?,0)`, name, []byte(`{"notes":"private"}`)); err != nil {
				t.Fatal(err)
			}

			if err := f.service.prepareMeetingDeletion(ctx, name); err != nil {
				t.Fatal(err)
			}
			if *f.exists || *f.deletes != 1 {
				t.Fatalf("exists=%v deletes=%d", *f.exists, *f.deletes)
			}
			got, ok, err := f.rt.store.meetingLifecycle(ctx, name)
			if err != nil || !ok || got.State != "retired" || got.Reason != meetingRetireReasonDeleted {
				t.Fatalf("lifecycle %+v ok=%v err=%v", got, ok, err)
			}
			if got.AnchorSource != anchorSourceDeleteRequest {
				t.Fatalf("anchor source %q", got.AnchorSource)
			}
			var count int
			if err := f.rt.meetingMetadata.db.QueryRow(`SELECT COUNT(*) FROM meeting_metadata`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("metadata count=%d err=%v", count, err)
			}
			if err := f.rt.searchStore.db.QueryRow(`SELECT COUNT(*) FROM segment_ref`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("search count=%d err=%v", count, err)
			}
			var receipt string
			if err := f.annotations.db.QueryRow(`SELECT response FROM annotation_receipt WHERE opus_name=?`, name).Scan(&receipt); err != nil || receipt != `{"deleted":true}` {
				t.Fatalf("receipt %s %v", receipt, err)
			}
			// The serving guard answers "retired" and names the delete.
			if err := f.service.exapp.meetingNotRetired(ctx, name); !errors.Is(err, errMeetingRetired) {
				t.Fatalf("tombstone not honored: %v", err)
			}
			if reason := f.service.exapp.retiredMeetingReason(ctx, name); reason != meetingRetireReasonDeleted {
				t.Fatalf("reason label %q", reason)
			}
			// A replay is already done — not an error, and not a second DELETE.
			if err := f.service.prepareMeetingDeletion(ctx, name); err != nil {
				t.Fatal(err)
			}
			if *f.deletes != 1 {
				t.Fatalf("replay deleted again: %d", *f.deletes)
			}
			// Previews and sweeps present the row as a user delete, not a
			// retention effect, and never re-retire a restored file for it.
			effect := evaluateRemoteRetention(got, f.rt.retention.settings.Nextcloud, time.Now())
			if effect.Action != meetingRetireReasonDeleted {
				t.Fatalf("preview action %q", effect.Action)
			}
		})
	}
}

func TestPrepareMeetingDeletionRefusals(t *testing.T) {
	ctx := context.Background()
	t.Run("busy", func(t *testing.T) {
		f := newMeetingDeleteFixture(t, ".opus")
		unlock := f.rt.store.lockArtifacts("m")
		defer unlock()
		if err := f.service.prepareMeetingDeletion(ctx, "m.opus"); !errors.Is(err, errMeetingDeleteBusy) {
			t.Fatalf("want busy, got %v", err)
		}
		if *f.deletes != 0 {
			t.Fatal("busy delete reached server")
		}
	})
	t.Run("missing-file", func(t *testing.T) {
		f := newMeetingDeleteFixture(t, ".opus")
		*f.exists = false
		if err := f.service.prepareMeetingDeletion(ctx, "m.opus"); err == nil {
			t.Fatal("deletion of a missing file accepted")
		}
	})
	t.Run("changed-identity", func(t *testing.T) {
		f := newMeetingDeleteFixture(t, ".opus")
		// An adopted lifecycle row pins file id 42; a later replacement under
		// the same name must not be deleted.
		m := meetingLifecycle{Name: "m.opus", FileID: 7, Path: ncRecordingsRoot + "/meetings/m.opus", State: "active", Anchor: "2020-01-01T00:00:00Z", AnchorSource: "createdAtUtc"}
		if err := f.rt.store.adoptMeetingLifecycle(ctx, m); err != nil {
			t.Fatal(err)
		}
		if err := f.service.prepareMeetingDeletion(ctx, "m.opus"); err == nil {
			t.Fatal("replacement deleted under a stale identity")
		}
		if *f.deletes != 0 {
			t.Fatal("unsafe delete reached server")
		}
	})
	t.Run("invalid-name", func(t *testing.T) {
		f := newMeetingDeleteFixture(t, ".opus")
		for _, name := range []string{"", "../m.opus", "m.txt", "a/b.opus"} {
			if err := f.service.prepareMeetingDeletion(ctx, name); err == nil {
				t.Fatalf("accepted %q", name)
			}
		}
	})
	t.Run("resume-pending", func(t *testing.T) {
		// A crash after journaling must finish on the next request.
		f := newMeetingDeleteFixture(t, ".opus")
		m := meetingLifecycle{Name: "m.opus", FileID: 42, Path: ncRecordingsRoot + "/meetings/m.opus", State: "active", Anchor: "2020-01-01T00:00:00Z", AnchorSource: "createdAtUtc"}
		if err := f.rt.store.adoptMeetingLifecycle(ctx, m); err != nil {
			t.Fatal(err)
		}
		op := remoteRetentionOperation{Name: "m.opus", Action: remoteRetentionActionDelete, Source: m.Path, FileID: 42, InputETag: `"original"`}
		if err := f.rt.store.saveRemoteOperation(ctx, op, "prepared"); err != nil {
			t.Fatal(err)
		}
		if _, err := f.rt.store.db.Exec(`UPDATE meeting_lifecycle SET state='retiring',reason='deleted' WHERE name='m.opus'`); err != nil {
			t.Fatal(err)
		}
		if err := f.service.prepareMeetingDeletion(ctx, "m.opus"); err != nil {
			t.Fatal(err)
		}
		if *f.exists || *f.deletes != 1 {
			t.Fatalf("pending delete not finished: exists=%v deletes=%d", *f.exists, *f.deletes)
		}
		var status string
		if err := f.rt.store.db.QueryRow(`SELECT status FROM remote_retention_operation WHERE name='m.opus'`).Scan(&status); err != nil || status != "completed" {
			t.Fatalf("status %q err=%v", status, err)
		}
	})
}
