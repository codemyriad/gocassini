package operator

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRemoteRetentionIsolatesRecoveryConflicts(t *testing.T) {
	for _, scenario := range []string{"continue", "capability-error", "inventory-error", "journal-error"} {
		t.Run(scenario, func(t *testing.T) {
			resetProvisioningUser(t)
			rt, close := newBareSealRuntime(t)
			defer close()
			ctx := context.Background()
			if err := os.MkdirAll(rt.cfg.WorkRoot, 0700); err != nil {
				t.Fatal(err)
			}
			annotations, err := openAnnotationStore(filepath.Join(t.TempDir(), "annotations.db"), rt.logger)
			if err != nil {
				t.Fatal(err)
			}
			defer annotations.Close()
			rt.annotations = annotations
			rt.retention = newRetentionConfig(filepath.Join(t.TempDir(), "retention.json"))
			rt.retention.settings.Nextcloud = nextcloudRetentionSettings{
				Recordings:     retentionPolicy{Count: 30, Unit: "days"},
				Transcriptions: retentionPolicy{Count: 30, Unit: "days"},
			}
			now := time.Now().UTC()
			root := ncRecordingsRoot + "/meetings"
			a := meetingLifecycle{Name: "a.opus", FileID: 42, Path: root + "/a.opus", Representation: "opus", State: "active", Anchor: now.AddDate(0, 0, -2).Format(time.RFC3339), AnchorSource: "recording-completed"}
			b := meetingLifecycle{Name: "b.opus", FileID: 43, Path: root + "/b.opus", Representation: "opus", State: "active", Anchor: now.AddDate(0, 0, -120).Format(time.RFC3339), AnchorSource: "recording-completed"}
			for _, m := range []meetingLifecycle{a, b} {
				insertJob(t, rt.store.db, strings.TrimSuffix(m.Name, ".opus"), m.Anchor)
				if _, err := rt.store.db.Exec(`UPDATE jobs SET stage='done',state='succeeded' WHERE id=?`, strings.TrimSuffix(m.Name, ".opus")); err != nil {
					t.Fatal(err)
				}
				if err := rt.store.adoptMeetingLifecycle(ctx, m); err != nil {
					t.Fatal(err)
				}
			}
			files := map[string]string{a.Path: "external edit", b.Path: "audio"}
			ids := map[string]int64{a.Path: a.FileID, b.Path: b.FileID}
			etags := map[string]string{a.Path: `"changed"`, b.Path: `"1"`}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/ocs/") {
					data := `{"users":["admin"]}`
					switch {
					case strings.Contains(r.URL.Path, "capabilities"):
						data = `{"version":{"major":35,"minor":0,"micro":0}}`
					case r.URL.Path == "/ocs/v2.php/cloud/apps":
						data = `{"apps":["files_versions"]}`
					case strings.Contains(r.URL.Path, "serverinfo"):
						if scenario == "capability-error" {
							w.WriteHeader(404)
							return
						}
						data = `{"nextcloud":{"storage":{"num_storages_home":1,"num_storages_other":0}}}`
					}
					fmt.Fprintf(w, `{"ocs":{"meta":{"status":"ok","statuscode":200},"data":%s}}`, data)
					return
				}
				rel := strings.TrimPrefix(r.URL.Path, "/remote.php/dav/files/cassini/")
				if rel == ncRecordingsRoot {
					w.WriteHeader(207)
					io.WriteString(w, `<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns" xmlns:nc="http://nextcloud.org/ns"><d:response><d:propstat><d:status>HTTP/1.1 200 OK</d:status><d:prop><oc:owner-id>cassini</oc:owner-id><nc:mount-type/></d:prop></d:propstat></d:response></d:multistatus>`)
					return
				}
				if rel == root {
					if scenario == "inventory-error" {
						w.WriteHeader(500)
						return
					}
					w.WriteHeader(207)
					io.WriteString(w, `<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">`)
					for leaf := range files {
						fmt.Fprintf(w, `<d:response><d:href>/remote.php/dav/files/cassini/%s</d:href><d:propstat><d:status>HTTP/1.1 200 OK</d:status><d:prop><oc:fileid>%d</oc:fileid></d:prop></d:propstat></d:response>`, leaf, ids[leaf])
					}
					io.WriteString(w, `</d:multistatus>`)
					return
				}
				body, exists := files[rel]
				if !exists {
					w.WriteHeader(404)
					return
				}
				if match := r.Header.Get("If-Match"); match != "" && match != etags[rel] {
					w.WriteHeader(412)
					return
				}
				switch r.Method {
				case "PROPFIND":
					w.WriteHeader(207)
					fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:response><d:href>%s</d:href><d:propstat><d:status>HTTP/1.1 200 OK</d:status><d:prop><oc:fileid>%d</oc:fileid><d:getetag>%s</d:getetag><d:getcontentlength>%d</d:getcontentlength></d:prop></d:propstat></d:response></d:multistatus>`, r.URL.Path, ids[rel], etags[rel], len(body))
				case "GET":
					io.WriteString(w, body)
				case "DELETE":
					delete(files, rel)
					w.WriteHeader(204)
				case "PUT":
					raw, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						w.WriteHeader(500)
						return
					}
					files[rel], etags[rel] = string(raw), `"2"`
					w.WriteHeader(204)
				case "MOVE":
					u, err := url.Parse(r.Header.Get("Destination"))
					if err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					to := strings.TrimPrefix(u.Path, "/remote.php/dav/files/cassini/")
					if _, exists := files[to]; exists {
						w.WriteHeader(412)
						return
					}
					files[to], ids[to], etags[to] = body, ids[rel], etags[rel]
					delete(files, rel)
					w.WriteHeader(201)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(405)
				}
			}))
			defer server.Close()
			cfg := testExAppConfig(server.URL)
			cfg.lifecycle = rt.store
			service := &annotationService{rt: rt, exapp: cfg, client: server.Client()}
			dir, err := os.MkdirTemp(rt.cfg.WorkRoot, "remote-retention-")
			if err != nil {
				t.Fatal(err)
			}
			output := `{"format":"cassini.transcription.v1"}`
			if err := os.WriteFile(filepath.Join(dir, "source.opus"), []byte("audio"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "output.json"), []byte(output), 0600); err != nil {
				t.Fatal(err)
			}
			inputSHA, err := fileSHA256(filepath.Join(dir, "source.opus"))
			if err != nil {
				t.Fatal(err)
			}
			outputSHA, err := fileSHA256(filepath.Join(dir, "output.json"))
			if err != nil {
				t.Fatal(err)
			}
			op := remoteRetentionOperation{Name: a.Name, Action: "convert", Source: a.Path, Destination: root + "/a" + transcriptionSuffix, FileID: a.FileID, InputETag: `"1"`, InputSHA: inputSHA, OutputSHA: outputSHA, Directory: dir}
			if err := rt.store.saveRemoteOperation(ctx, op, "prepared"); err != nil {
				t.Fatal(err)
			}
			if scenario == "journal-error" {
				if _, err := rt.store.db.Exec(`CREATE TRIGGER reject_recovery_error BEFORE UPDATE OF last_error ON remote_retention_operation BEGIN SELECT RAISE(ABORT,'journal unavailable'); END`); err != nil {
					t.Fatal(err)
				}
			}
			err = service.runRemoteRetention(ctx, now)
			if err == nil {
				t.Fatal("sweep concealed recovery/global failure")
			}
			if files[a.Path] != "external edit" {
				t.Fatal("conflicted source was changed")
			}
			var journal string
			if err := rt.store.db.QueryRow(`SELECT operation_json FROM remote_retention_operation WHERE name=?`, a.Name).Scan(&journal); err != nil {
				t.Fatal(err)
			}
			pending, err := rt.store.pendingRemoteOperations(ctx)
			if err != nil || len(pending) != 1 || pending[0] != op {
				t.Fatalf("original intent changed: %+v, %v", pending, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "output.json")); err != nil {
				t.Fatal("recovery staging lost", err)
			}
			_, bExists := files[b.Path]
			if scenario != "continue" {
				if !bExists {
					t.Fatal("global failure allowed new expiry")
				}
				return
			}
			if bExists {
				t.Fatal("conflicted A blocked expiry of healthy B")
			}
			retired, _, err := rt.store.meetingLifecycle(ctx, b.Name)
			if err != nil || retired.State != "retired" {
				t.Fatalf("B was not retired: %+v, %v", retired, err)
			}
			// Even an active lifecycle row cannot permit a fresh operation to
			// replace the unfinished conversion intent.
			if err := service.prepareRemoteRetention(ctx, a, "retire", rt.retention.settings.Revision); err != nil {
				t.Fatal(err)
			}
			var after string
			if err := rt.store.db.QueryRow(`SELECT operation_json FROM remote_retention_operation WHERE name=?`, a.Name).Scan(&after); err != nil || after != journal {
				t.Fatalf("preparation replaced pending intent: %v", err)
			}
			// Resolve the external edit: retry must finish the original conversion.
			files[a.Path], etags[a.Path] = "audio", `"1"`
			if err := service.runRemoteRetention(ctx, now); err != nil {
				t.Fatal("resolved conflict did not recover", err)
			}
			if files[op.Destination] != output || ids[op.Destination] != a.FileID {
				t.Fatal("recovery did not deliver the original staged output")
			}
			if _, exists := files[a.Path]; exists {
				t.Fatal("old source remains after conversion")
			}
			pending, err = rt.store.pendingRemoteOperations(ctx)
			if err != nil || len(pending) != 0 {
				t.Fatalf("resolved intent remains pending: %+v, %v", pending, err)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatal("completed recovery staging remains", err)
			}
		})
	}
}

func TestRemoteRetentionRecoveryRejectsUnreadableJournal(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	ctx := context.Background()
	if err := rt.store.saveRemoteOperation(ctx, remoteRetentionOperation{Name: "m.opus"}, "prepared"); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.store.db.Exec(`UPDATE remote_retention_operation SET operation_json='{'`); err != nil {
		t.Fatal(err)
	}
	// No DAV client is provided: an unreadable journal must abort the pass
	// before attempting any remote work.
	service := &annotationService{rt: rt}
	result, err := service.recoverRemoteRetentionPass(ctx)
	if err == nil || result.failures != nil {
		t.Fatalf("unreadable journal was treated as a meeting conflict: %+v, %v", result, err)
	}
}
