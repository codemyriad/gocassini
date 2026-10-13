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
	"os"
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
				if r.Method != http.MethodPost || r.URL.Path != "/ocs/v2.php/apps/spreed/api/v1/chat/room1234/share" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				var metadata map[string]string
				if json.Unmarshal([]byte(r.FormValue("metaData")), &metadata) != nil || metadata["link"] != "https://cloud.example/nc/index.php/apps/app_api/embedded/gocassini/viewer#meeting=one" || metadata["name"] != "Meeting recording available: Untitled meeting" || r.FormValue("objectType") != "highlight" || r.FormValue("objectId") != "one" || r.FormValue("message") != "" {
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

func TestTalkNotificationMetadata(t *testing.T) {
	for _, extension := range []string{"opus", "json"} {
		t.Run(extension, func(t *testing.T) {
			dir := t.TempDir()
			write := func(name, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			write("catalog.json", `{"meetings":[{"id":"other","title":"Wrong meeting"},{"id":"one","title":"Weekly\nplanning","dateLabel":"2026-10-13 10:00","digestDurationMs":1920000,"meetingPath":"./meetings/one.`+extension+`"}]}`)
			n := talkNotification{MeetingID: "one", Link: "https://cloud.example/viewer#meeting=one"}
			n.enrich(dir, dir, "")
			want := "Meeting recording available: Weekly planning · 13 Oct · 32 min"
			if n.Message != want {
				t.Fatalf("catalog fallback=%q", n.Message)
			}
			write("transcript.words.v1.json", `{"speakers":[{"label":"Ivan"},{"label":"Alex"},{"label":"Chris"},{"label":"Silvio"},{"label":"Ivan"},{"label":" "}]}`)
			n.enrich(dir, dir, "Daily\nStandup")
			want = "Meeting recording available: Daily Standup · 13 Oct · 32 min · Speakers: Alex, Chris, Ivan +1"
			if n.Message != want {
				t.Fatalf("label=%q want=%q", n.Message, want)
			}
			write("transcript.words.v1.json", `{"speakers":[]}`)
			write("catalog.json", `{"meetings":[{"id":"other","title":"Private unrelated meeting"}]}`)
			n.enrich(dir, dir, "")
			if n.Message != "Meeting recording available: Untitled meeting" {
				t.Fatalf("unrelated metadata leaked: %q", n.Message)
			}
			write("catalog.json", `{`)
			write("transcript.words.v1.json", `{`)
			n.enrich(dir, dir, "")
			if n.Message != "Meeting recording available: Untitled meeting" {
				t.Fatalf("malformed fallback: %q", n.Message)
			}
		})
	}
	if got := notificationText("  αβγ\n δ ", 3); got != "αβγ…" {
		t.Fatalf("bounded label: %q", got)
	}
}
