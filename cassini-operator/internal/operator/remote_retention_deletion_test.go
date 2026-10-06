package operator

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestWholeMeetingDeletionIdentityAndRecovery(t *testing.T) {
	for _, scenario := range []string{"delete", "changed-id", "changed-etag", "changed-path", "missing", "busy", "stale-settings", "restore"} {
		t.Run(scenario, func(t *testing.T) {
			rt, close := newBareSealRuntime(t)
			defer close()
			ctx := context.Background()
			rt.retention = newRetentionConfig(filepath.Join(t.TempDir(), "settings.json"))
			annotations, err := openAnnotationStore(filepath.Join(t.TempDir(), "annotations.db"), rt.logger)
			if err != nil {
				t.Fatal(err)
			}
			defer annotations.Close()
			rt.annotations = annotations
			insertJob(t, rt.store.db, "m", nowUTCString())
			if _, err := rt.store.db.Exec(`UPDATE jobs SET stage='done',state='succeeded' WHERE id='m'`); err != nil {
				t.Fatal(err)
			}
			m := meetingLifecycle{Name: "m.opus", FileID: 42, Path: ncRecordingsRoot + "/meetings/m.opus", State: "active", Anchor: "2020-01-01T00:00:00Z", CreatedAtUTC: "2020-01-01T00:00:00Z", AnchorSource: "createdAtUtc"}
			if err := rt.store.adoptMeetingLifecycle(ctx, m); err != nil {
				t.Fatal(err)
			}
			exists := scenario != "missing"
			id := 42
			etag := `"original"`
			deletes := 0
			if scenario == "changed-id" {
				id = 43
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !exists {
					w.WriteHeader(404)
					return
				}
				switch r.Method {
				case "PROPFIND":
					href := r.URL.Path
					if scenario == "changed-path" {
						href += "-replacement"
					}
					w.WriteHeader(207)
					fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:response><d:href>%s</d:href><d:propstat><d:status>HTTP/1.1 200 OK</d:status><d:prop><oc:fileid>%d</oc:fileid><d:getetag>%s</d:getetag><d:getcontentlength>5</d:getcontentlength></d:prop></d:propstat></d:response></d:multistatus>`, href, id, etag)
				case "DELETE":
					if scenario == "changed-etag" {
						etag = `"edited"`
					}
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
			defer server.Close()
			cfg := testExAppConfig(server.URL)
			cfg.lifecycle = rt.store
			service := &annotationService{rt: rt, exapp: cfg, client: server.Client()}
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
			if err := metadata.Put(ctx, m.FileID, m.Name, []byte(`{"id":"m","audioPath":"meetings/m.opus"}`)); err != nil {
				t.Fatal(err)
			}
			if err := search.ReplaceMeeting(ctx, m.Name, "digest", searchRowSourceWords, []searchRow{{Text: "secret words"}}); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{m.Name, "other.opus"} {
				if _, err := annotations.db.Exec(`INSERT INTO meeting_annotations(opus_name,state) VALUES(?,'ready')`, name); err != nil {
					t.Fatal(err)
				}
				if _, err := annotations.db.Exec(`INSERT INTO annotation_receipt(caller,request_id,opus_name,request_hash,response,snapshot) VALUES('alice',?,?, 'hash',?,0)`, name, name, []byte(`{"notes":"private"}`)); err != nil {
					t.Fatal(err)
				}
			}
			revision := rt.retention.settings.Revision
			if scenario == "stale-settings" {
				revision++
			}
			var unlock func()
			if scenario == "busy" {
				unlock = rt.store.lockArtifacts("m")
			}
			err = service.prepareRemoteRetention(ctx, m, "retire", revision)
			if unlock != nil {
				unlock()
			}
			if scenario != "delete" && scenario != "restore" {
				if scenario != "busy" && err == nil {
					t.Fatal("unsafe deletion accepted")
				}
				if deletes != 0 {
					t.Fatal("unsafe deletion reached server")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, _, err := rt.store.meetingLifecycle(ctx, m.Name)
			if err != nil || got.State != "retired" || exists || deletes != 1 {
				t.Fatalf("%+v exists=%v deletes=%d err=%v", got, exists, deletes, err)
			}
			var count int
			for _, check := range []struct {
				query string
				db    *sql.DB
			}{
				{`SELECT COUNT(*) FROM meeting_metadata`, metadata.db},
				{`SELECT COUNT(*) FROM segment_ref`, search.db},
				{`SELECT COUNT(*) FROM meeting_annotations WHERE opus_name='m.opus'`, annotations.db},
			} {
				if err := check.db.QueryRow(check.query).Scan(&count); err != nil || count != 0 {
					t.Fatalf("cleanup %s count=%d err=%v", check.query, count, err)
				}
			}
			var receipt string
			if err := annotations.db.QueryRow(`SELECT response FROM annotation_receipt WHERE opus_name='m.opus'`).Scan(&receipt); err != nil || receipt != `{"expired":true}` {
				t.Fatalf("receipt %s %v", receipt, err)
			}
			if err := annotations.db.QueryRow(`SELECT response FROM annotation_receipt WHERE opus_name='other.opus'`).Scan(&receipt); err != nil || receipt != `{"notes":"private"}` {
				t.Fatalf("unrelated receipt %s %v", receipt, err)
			}
			if scenario == "restore" {
				exists = true
				etag = `"restored"`
				if err := service.prepareRemoteRetention(ctx, got, "retire", revision); err != nil {
					t.Fatal(err)
				}
				if exists || deletes != 2 {
					t.Fatal("restored identity not removed")
				}
				exists = true
				id = 43
				if err := service.prepareRemoteRetention(ctx, got, "retire", revision); err == nil {
					t.Fatal("replacement accepted as restored identity")
				}
				if !exists || deletes != 2 {
					t.Fatal("unrelated replacement deleted")
				}
			}
		})
	}
}
