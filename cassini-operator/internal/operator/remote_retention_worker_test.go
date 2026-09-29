package operator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteRetentionSkipsAdmittedRerun(t *testing.T) {
	for _, action := range []string{"convert", "retire"} {
		t.Run(action, func(t *testing.T) {
			rt, close := newBareSealRuntime(t)
			defer close()
			ctx := context.Background()
			insertJob(t, rt.store.db, "m", nowUTCString())
			run := seedReadyRunBundle(t, rt.cfg.WorkRoot, "m")
			if _, err := rt.store.db.Exec(`UPDATE jobs SET stage='done',state='succeeded',artifact_run_path=? WHERE id='m'`, run); err != nil {
				t.Fatal(err)
			}
			m := meetingLifecycle{Name: "m.opus", FileID: 42, Path: ncRecordingsRoot + "/meetings/m.opus", Representation: "opus", State: "active", Anchor: "2020-01-01T00:00:00Z", AnchorSource: "recording-completed"}
			if err := rt.store.adoptMeetingLifecycle(ctx, m); err != nil {
				t.Fatal(err)
			}
			// The sweep selected this terminal job, then rerun admission won the
			// artifact lock before remote preparation started.
			job := mustGetJob(t, rt.store, "m")
			if _, err := rt.store.QueueRerunAttempt(ctx, job, nowUTCString()); err != nil {
				t.Fatal(err)
			}
			service := &annotationService{rt: rt, client: &http.Client{Transport: annotationRoundTripper(func(*http.Request) (*http.Response, error) {
				t.Error("remote retention contacted Nextcloud for a queued rerun")
				return nil, errors.New("unexpected remote request")
			})}}
			if err := service.prepareRemoteRetention(ctx, m, action, 0); err != nil {
				t.Fatal(err)
			}
			pending, err := rt.store.pendingRemoteOperations(ctx)
			if err != nil || len(pending) != 0 {
				t.Fatalf("queued rerun acquired retention intent: %+v, %v", pending, err)
			}
			got, _, err := rt.store.meetingLifecycle(ctx, m.Name)
			if err != nil || got != m {
				t.Fatalf("queued rerun lifecycle changed: %+v, %v", got, err)
			}
		})
	}
}

func TestRerunWaitsForRemoteRetentionCompletion(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	ctx := context.Background()
	insertJob(t, rt.store.db, "m", nowUTCString())
	run := seedReadyRunBundle(t, rt.cfg.WorkRoot, "m")
	if _, err := rt.store.db.Exec(`UPDATE jobs SET stage='done',state='succeeded',artifact_run_path=? WHERE id='m'`, run); err != nil {
		t.Fatal(err)
	}
	op := remoteRetentionOperation{Name: "m.opus", Action: "convert"}
	if err := rt.store.saveRemoteOperation(ctx, op, "prepared"); err != nil {
		t.Fatal(err)
	}
	job := mustGetJob(t, rt.store, "m")
	if _, err := rt.store.QueueRerunAttempt(ctx, job, nowUTCString()); !errors.Is(err, ErrJobNotEligibleForRerun) {
		t.Fatalf("pending remote operation admitted rerun: %v", err)
	}
	if got := mustGetJob(t, rt.store, "m"); got.Stage != "done" || got.CurrentAttemptNumber != 1 {
		t.Fatalf("rejected rerun changed job: %+v", got)
	}
	if err := rt.store.saveRemoteOperation(ctx, op, "completed"); err != nil {
		t.Fatal(err)
	}
	if got, err := rt.store.QueueRerunAttempt(ctx, job, nowUTCString()); err != nil || got.Stage != "build" || got.CurrentAttemptNumber != 2 {
		t.Fatalf("completed remote operation blocked rerun: %+v, %v", got, err)
	}
}

func TestRemoteRetentionRecovery(t *testing.T) {
	for _, start := range []string{"input", "json-at-source", "json-at-destination", "collision", "external-edit", "lost-put", "lost-move", "busy"} {
		t.Run(start, func(t *testing.T) {
			rt, close := newBareSealRuntime(t)
			defer close()
			ctx := context.Background()
			if err := os.MkdirAll(rt.cfg.WorkRoot, 0700); err != nil {
				t.Fatal(err)
			}
			old := ncRecordingsRoot + "/meetings/m.opus"
			dest := ncRecordingsRoot + "/meetings/m" + transcriptionSuffix
			files := map[string]string{old: "audio"}
			ids := map[string]int64{old: 42}
			etags := map[string]string{old: `"1"`}
			switch start {
			case "json-at-source":
				files[old] = "json"
				etags[old] = `"2"`
			case "json-at-destination":
				delete(files, old)
				files[dest] = "json"
				ids[dest] = 42
				etags[dest] = `"2"`
			case "collision":
				files[dest] = "unrelated"
				ids[dest] = 43
				etags[dest] = `"other"`
			case "external-edit":
				files[old] = "external"
				etags[old] = `"changed"`
			}
			failed := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				rel := strings.TrimPrefix(r.URL.Path, "/remote.php/dav/files/cassini/")
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
				case "PUT":
					b, _ := io.ReadAll(r.Body)
					files[rel] = string(b)
					etags[rel] = `"2"`
					if start == "lost-put" && !failed {
						failed = true
						w.WriteHeader(503)
						return
					}
					w.WriteHeader(204)
				case "MOVE":
					u, _ := url.Parse(r.Header.Get("Destination"))
					to := strings.TrimPrefix(u.Path, "/remote.php/dav/files/cassini/")
					if _, ok := files[to]; ok {
						w.WriteHeader(412)
						return
					}
					files[to] = body
					ids[to] = ids[rel]
					etags[to] = etags[rel]
					delete(files, rel)
					if start == "lost-move" && !failed {
						failed = true
						w.WriteHeader(503)
						return
					}
					w.WriteHeader(201)
				default:
					t.Errorf("unexpected %s", r.Method)
					w.WriteHeader(405)
				}
			}))
			defer server.Close()
			cfg := testExAppConfig(server.URL)
			cfg.lifecycle = rt.store
			service := &annotationService{rt: rt, exapp: cfg, client: server.Client()}
			m := meetingLifecycle{Name: "m.opus", FileID: 42, Path: old, Representation: "opus", State: "active", Anchor: "2026-01-01T00:00:00Z", AnchorSource: "recording-completed"}
			if err := rt.store.adoptMeetingLifecycle(ctx, m); err != nil {
				t.Fatal(err)
			}
			dir, err := os.MkdirTemp(rt.cfg.WorkRoot, "remote-retention-")
			if err != nil {
				t.Fatal(err)
			}
			os.WriteFile(filepath.Join(dir, "source.opus"), []byte("audio"), 0600)
			os.WriteFile(filepath.Join(dir, "output.json"), []byte("json"), 0600)
			input, _ := fileSHA256(filepath.Join(dir, "source.opus"))
			output, _ := fileSHA256(filepath.Join(dir, "output.json"))
			op := remoteRetentionOperation{Name: m.Name, Action: "convert", Source: old, Destination: dest, FileID: 42, InputETag: `"1"`, InputSHA: input, OutputSHA: output, Directory: dir}
			if err := rt.store.saveRemoteOperation(ctx, op, "prepared"); err != nil {
				t.Fatal(err)
			}
			if start == "busy" {
				unlock := rt.store.lockArtifacts("m")
				err := service.recoverRemoteRetention(ctx)
				unlock()
				if err != nil || files[old] != "audio" {
					t.Fatalf("recovery changed a reserved job: %q, %v", files[old], err)
				}
				pending, err := rt.store.pendingRemoteOperations(ctx)
				if err != nil || len(pending) != 1 {
					t.Fatalf("busy recovery lost its pending operation: %+v, %v", pending, err)
				}
			}
			err = service.recoverRemoteRetention(ctx)
			if start == "collision" || start == "external-edit" {
				if err == nil {
					t.Fatal("conflict accepted")
				}
				if start == "collision" && files[dest] != "unrelated" {
					t.Fatal("overwrote collision")
				}
				if start == "external-edit" && files[old] != "external" {
					t.Fatal("overwrote edit")
				}
				return
			}
			if start == "lost-put" || start == "lost-move" {
				if err == nil {
					t.Fatal("expected ambiguous failure")
				}
				err = service.recoverRemoteRetention(ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
			got, _, err := rt.store.meetingLifecycle(ctx, m.Name)
			if err != nil || got.Path != dest || got.Representation != "transcription" || files[dest] != "json" {
				t.Fatalf("failed convergence: %+v %v", got, err)
			}
			if _, err = os.Stat(dir); !os.IsNotExist(err) {
				t.Fatal("staging retained")
			}
			pending, err := rt.store.pendingRemoteOperations(ctx)
			if err != nil || len(pending) != 0 {
				t.Fatal("journal still pending", err)
			}
		})
	}
}

func TestScrubRetiredBatchResult(t *testing.T) {
	raw, err := scrubRetiredResult([]byte(`{"results":[{"meetingId":"m","annotations":{"private":"expired"}},{"meetingId":"other","annotations":{"keep":true}}]}`), "m.opus")
	if err != nil || strings.Contains(string(raw), "private") || !strings.Contains(string(raw), "keep") || !strings.Contains(string(raw), "expired") {
		t.Fatalf("%s %v", raw, err)
	}
}

func TestRemoteRetirementLostDeleteAndCleanup(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			rt, close := newBareSealRuntime(t)
			defer close()
			ctx := context.Background()
			os.MkdirAll(rt.cfg.WorkRoot, 0700)
			annotations, err := openAnnotationStore(filepath.Join(t.TempDir(), "annotations.db"), rt.logger)
			if err != nil {
				t.Fatal(err)
			}
			defer annotations.Close()
			rt.annotations = annotations
			rel := ncRecordingsRoot + "/meetings/m.opus"
			exists := !missing
			deletes := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !exists {
					w.WriteHeader(404)
					return
				}
				switch r.Method {
				case "PROPFIND":
					w.WriteHeader(207)
					fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:response><d:href>%s</d:href><d:propstat><d:status>HTTP/1.1 200 OK</d:status><d:prop><oc:fileid>42</oc:fileid><d:getetag>"1"</d:getetag><d:getcontentlength>5</d:getcontentlength></d:prop></d:propstat></d:response></d:multistatus>`, r.URL.Path)
				case "GET":
					io.WriteString(w, "audio")
				case "DELETE":
					if r.Header.Get("If-Match") != `"1"` {
						t.Error("missing delete precondition")
					}
					deletes++
					exists = false
					w.WriteHeader(503)
				}
			}))
			defer server.Close()
			cfg := testExAppConfig(server.URL)
			cfg.lifecycle = rt.store
			s := &annotationService{rt: rt, exapp: cfg, client: server.Client()}
			m := meetingLifecycle{Name: "m.opus", FileID: 42, Path: rel, Representation: "opus", State: "active", Anchor: "2026-01-01T00:00:00Z", AnchorSource: "recording-completed"}
			if err := rt.store.adoptMeetingLifecycle(ctx, m); err != nil {
				t.Fatal(err)
			}
			dir, _ := os.MkdirTemp(rt.cfg.WorkRoot, "remote-retention-")
			os.WriteFile(filepath.Join(dir, "source.opus"), []byte("audio"), 0600)
			sha, _ := fileSHA256(filepath.Join(dir, "source.opus"))
			op := remoteRetentionOperation{Name: m.Name, Action: "retire", Source: rel, FileID: 42, InputETag: `"1"`, InputSHA: sha, Directory: dir}
			if err := rt.store.saveRemoteOperation(ctx, op, "prepared"); err != nil {
				t.Fatal(err)
			}
			if err := s.recoverRemoteRetention(ctx); err == nil {
				t.Fatal("expected ambiguous or unexplained absence")
			}
			if missing {
				var status string
				rt.store.db.QueryRow(`SELECT status FROM remote_retention_operation`).Scan(&status)
				if status == "completed" {
					t.Fatal("unexplained absence accepted")
				}
				return
			}
			if err := s.recoverRemoteRetention(ctx); err != nil {
				t.Fatal(err)
			}
			got, _, _ := rt.store.meetingLifecycle(ctx, m.Name)
			if got.State != "retired" || deletes != 1 {
				t.Fatalf("%+v deletes=%d", got, deletes)
			}
			if err := cfg.meetingNotRetired(ctx, m.Name); err == nil {
				t.Fatal("retired meeting served")
			}
		})
	}
}
