package operator

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func seedTalkNotificationJob(t *testing.T, s *Store, id string) {
	t.Helper()
	insertJob(t, s.db, id, nowUTCString())
	if _, err := s.db.Exec(`UPDATE jobs SET request_json = ?, talk_binding = ? WHERE id = ?`, `{"baseUrl":"https://cloud.example/nc"}`, `{"backend_url":"http://internal","room_token":"room1234","owner":"alice"}`, id); err != nil {
		t.Fatal(err)
	}
}

func chatResponse(w http.ResponseWriter, data any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"ocs": map[string]any{"meta": map[string]any{"status": "ok", "statuscode": 200}, "data": data}})
}

type notifyingTestSink struct {
	okPublishSink
	client *talkNotificationClient
	err    error
}

func (s *notifyingTestSink) talkNotificationClient() *talkNotificationClient { return s.client }
func (s *notifyingTestSink) Deliver(ctx context.Context, d publishDelivery) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.okPublishSink.Deliver(ctx, d)
}

func TestTalkNotificationBestEffortPublication(t *testing.T) {
	for _, mode := range []string{"success", "forbidden", "rate-limited", "server-error", "lost-response", "delivery-failed", "commit-failed", "unbound", "invalid-url", "republish", "local"} {
		t.Run(mode, func(t *testing.T) {
			var logs bytes.Buffer
			rt, cleanup := newBarePublishRuntime(t, log.New(&logs, "", 0))
			defer cleanup()
			seedTalkNotificationJob(t, rt.store, "one")
			switch mode {
			case "unbound":
				_, err := rt.store.db.Exec(`UPDATE jobs SET talk_binding = NULL WHERE id = 'one'`)
				if err != nil {
					t.Fatal(err)
				}
			case "invalid-url":
				_, err := rt.store.db.Exec(`UPDATE jobs SET request_json = '{}' WHERE id = 'one'`)
				if err != nil {
					t.Fatal(err)
				}
			case "republish":
				_, err := rt.store.db.Exec(`INSERT INTO job_attempts (job_id, attempt_number, trigger_kind, request_json, stage, state, created_at, updated_at) VALUES ('one', 0, 'initial', '{}', 'done', 'succeeded', ?, ?)`, nowUTCString(), nowUTCString())
				if err != nil {
					t.Fatal(err)
				}
			case "commit-failed":
				_, err := rt.store.db.Exec(`CREATE TRIGGER fail_publication BEFORE UPDATE ON jobs WHEN NEW.state = 'succeeded' BEGIN SELECT RAISE(ABORT, 'test failure'); END`)
				if err != nil {
					t.Fatal(err)
				}
			}
			posts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				posts++
				auth, _ := base64.StdEncoding.DecodeString(r.Header.Get("AUTHORIZATION-APP-API"))
				if string(auth) != "alice:test-secret" || r.Header.Get("EX-APP-ID") != "gocassini" || r.Header.Get("OCS-APIRequest") != "true" {
					t.Error("wrong moderator act-as headers")
				}
				if r.Method != http.MethodPost || r.URL.Path != "/ocs/v2.php/apps/spreed/api/v1/chat/room1234" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				if r.FormValue("message") != "Recording available: https://cloud.example/nc/index.php/apps/app_api/embedded/gocassini/viewer#meeting=one" {
					t.Error("wrong recording link")
				}
				job, err := rt.store.GetJob(context.Background(), "one")
				if err != nil || job.State != "succeeded" {
					t.Error("message sent before publication succeeded")
				}
				switch mode {
				case "forbidden":
					http.Error(w, "sensitive upstream text", 403)
				case "rate-limited":
					http.Error(w, "sensitive upstream text", 429)
				case "server-error":
					http.Error(w, "sensitive upstream text", 502)
				case "lost-response":
					fmt.Fprint(w, `{`)
				default:
					chatResponse(w, talkChatMessage{ID: 42})
				}
			}))
			defer server.Close()
			client := newTalkNotificationClient(ExAppConfig{NextcloudURL: server.URL, AppID: "gocassini", AppSecret: "test-secret", AppVersion: "1"})
			sink := &notifyingTestSink{okPublishSink: okPublishSink{location: "remote"}, client: client}
			if mode == "delivery-failed" {
				sink.err = fmt.Errorf("share setup refused")
			}
			rt.publishSink = sink
			if mode == "local" {
				rt.publishSink = &okPublishSink{location: "local"}
			}
			site := filepath.Join(t.TempDir(), "attempt.site")
			rt.publishJobFn = func(context.Context, publishTask) (string, error) { return site, nil }
			if err := rt.store.MarkPublishQueued(rt.ctx, "one", "meeting", "meeting", nowUTCString()); err != nil {
				t.Fatal(err)
			}
			task := publishTask{JobID: "one", AttemptNumber: 1}
			rt.runPublishJob(task)
			rt.runPublishJob(task) // Duplicate queue delivery must not send again.
			want := 1
			switch mode {
			case "delivery-failed", "commit-failed", "unbound", "invalid-url", "republish", "local":
				want = 0
			}
			if posts != want {
				t.Fatalf("requests=%d want=%d", posts, want)
			}
			if mode != "delivery-failed" && mode != "commit-failed" && mustGetJob(t, rt.store, "one").State != "succeeded" {
				t.Fatal("notification failure changed publication result")
			}
			switch mode {
			case "forbidden", "rate-limited", "server-error", "lost-response":
				if !strings.Contains(logs.String(), "Talk notification failed") {
					t.Fatal("failure not logged")
				}
			}
			if strings.Contains(logs.String(), "sensitive upstream text") || strings.Contains(logs.String(), "test-secret") {
				t.Fatal("sensitive details leaked")
			}
		})
	}
}

func TestTalkMeetingURL(t *testing.T) {
	for _, base := range []string{"", "//cloud.example", "file:///tmp", "https://alice:secret@cloud.example", "https://cloud.example?secret=x", "https://cloud.example/#x"} {
		if _, err := talkMeetingURL(base, "gocassini", "meeting"); err == nil {
			t.Errorf("accepted %q", base)
		}
	}
	got, err := talkMeetingURL("https://cloud.example/nc/", "gocassini", "01ABC")
	if err != nil || !strings.HasSuffix(got, "/nc/index.php/apps/app_api/embedded/gocassini/viewer#meeting=01ABC") {
		t.Fatalf("link: %q %v", got, err)
	}
}

func TestTalkNotificationDoesNotFollowRedirect(t *testing.T) {
	calls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client := newTalkNotificationClient(ExAppConfig{NextcloudURL: server.URL})
	if _, err := client.send(context.Background(), talkNotification{Owner: "alice", RoomToken: "room", Message: "link"}); err == nil || calls != 0 {
		t.Fatalf("redirect followed or accepted: calls=%d err=%v", calls, err)
	}
}
