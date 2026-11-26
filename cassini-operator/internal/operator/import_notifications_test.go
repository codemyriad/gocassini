package operator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// notificationServer is AppAPI's notification route, recording what it was
// asked; off answers as AppAPI does when the Notifications app is disabled.
type notificationServer struct {
	mu   sync.Mutex
	got  []sentNotification
	off  bool
	fail bool
}

type sentNotification struct {
	user, subject, name, message, link string
}

func (n *notificationServer) serve(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != appAPINotificationPath {
			// Anything else the import touches (the staged copy's removal).
			w.WriteHeader(http.StatusNoContent)
			return
		}
		auth, _ := base64.StdEncoding.DecodeString(r.Header.Get("AUTHORIZATION-APP-API"))
		user, _, _ := strings.Cut(string(auth), ":")
		var body struct {
			Params struct {
				Object        string `json:"object"`
				ObjectID      string `json:"object_id"`
				SubjectType   string `json:"subject_type"`
				SubjectParams struct {
					Subject string                       `json:"rich_subject"`
					Params  map[string]map[string]string `json:"rich_subject_params"`
					Message *string                      `json:"rich_message"`
					Link    string                       `json:"link"`
				} `json:"subject_params"`
			} `json:"params"`
		}
		if r.Method != http.MethodPost || r.Header.Get("EX-APP-ID") != "cassini" || r.Header.Get("OCS-APIRequest") != "true" ||
			json.NewDecoder(r.Body).Decode(&body) != nil || body.Params.Object != "app_api" || body.Params.SubjectType != "app_api_ex_app" || body.Params.ObjectID == "" ||
			// Nextcloud refuses an empty message and then never shows the notification.
			(body.Params.SubjectParams.Message != nil && *body.Params.SubjectParams.Message == "") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		n.mu.Lock()
		defer n.mu.Unlock()
		if n.off {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"ocs":{"meta":{"statuscode":404,"message":"The notifications app is not enabled, the notification cannot be delivered"},"data":[]}}`))
			return
		}
		if n.fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		p := body.Params.SubjectParams
		message := ""
		if p.Message != nil {
			message = *p.Message
		}
		n.got = append(n.got, sentNotification{user: user, subject: p.Subject, name: p.Params["meeting"]["name"], message: message, link: p.Link})
		_, _ = w.Write([]byte(`{"ocs":{"meta":{"statuscode":200},"data":{}}}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func (n *notificationServer) sent() []sentNotification {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]sentNotification(nil), n.got...)
}

// notifyingService is an import worker whose AppAPI is the given server and
// whose public Nextcloud address is https://cloud.example.
func notifyingService(t *testing.T, server *httptest.Server, perform func(ctx context.Context, job ImportJob) (meetingImportResult, error)) (*importService, *strings.Builder) {
	t.Helper()
	s := queueWorkerService(t, perform)
	var logged strings.Builder
	var mu sync.Mutex
	s.logger = log.New(writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return logged.Write(p) }), "", 0)
	s.exapp = ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppVersion: "1", AppSecret: "secret"}
	s.client = server.Client()
	s.rt.cfg.TalkBackendURL = "https://cloud.example"
	return s, &logged
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// #523: an import that finishes or fails tells its importer, with a link to
// the meeting (or to Cassini, for a failure) and, for a failure, what to do.
func TestASettledImportNotifiesItsImporter(t *testing.T) {
	n := &notificationServer{}
	server := n.serve(t)
	s, _ := notifyingService(t, server, func(ctx context.Context, job ImportJob) (meetingImportResult, error) {
		if job.ID == "bad" {
			return meetingImportResult{}, errors.New("the download from your Files stalled: less than 64 KiB arrived in 2m0s")
		}
		return meetingImportResult{DeliveredName: job.ID + ".opus", FileID: 7, ImportedAtUTC: nowUTCString()}, nil
	})
	if err := s.rt.store.CreateImportJobs(context.Background(), []ImportJob{queueTestJob("good", "alice"), queueTestJob("bad", "bob")}); err != nil {
		t.Fatal(err)
	}
	s.dispatch()
	waitForImportState(t, s, "good", importStateDone)
	waitForImportState(t, s, "bad", importStateFailed)
	deadline := time.Now().Add(5 * time.Second)
	for len(n.sent()) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	byUser := map[string]sentNotification{}
	for _, sent := range n.sent() {
		byUser[sent.user] = sent
	}
	good, bad := byUser["alice"], byUser["bob"]
	if good.subject != "{meeting} was imported" || good.name != "good.opus" ||
		good.link != "https://cloud.example/index.php/apps/app_api/embedded/cassini/viewer#meeting=good" {
		t.Fatalf("done: %+v", good)
	}
	if bad.subject != "Import of {meeting} failed" || bad.name != "bad.opus" ||
		!strings.Contains(bad.message, "download from your Files stopped") ||
		bad.link != "https://cloud.example/index.php/apps/app_api/embedded/cassini/viewer" {
		t.Fatalf("failed: %+v", bad)
	}
}

// Someone watching their imports in Cassini is not notified: the panel asks
// for the list every few seconds while one runs.
func TestAnImporterWatchingIsNotNotified(t *testing.T) {
	n := &notificationServer{}
	server := n.serve(t)
	s, _ := notifyingService(t, server, nil)
	now := time.Now()
	s.watchers.now = func() time.Time { return now }
	s.watchers.saw("alice")
	job := queueTestJob("a1", "alice")
	s.notifySettled(job, true, "")
	s.rt.workerWG.Wait()
	if got := n.sent(); len(got) != 0 {
		t.Fatalf("notified while watching: %+v", got)
	}
	// Once they stop asking (the tab closed), they are.
	now = now.Add(importWatchingWindow)
	s.notifySettled(job, true, "")
	s.rt.workerWG.Wait()
	if got := n.sent(); len(got) != 1 {
		t.Fatalf("not notified after leaving: %+v", got)
	}
	// A CLI import has nobody to notify.
	cli := job
	cli.StagedPath = "local:/tmp/a.opus"
	s.notifySettled(cli, false, "x")
	s.rt.workerWG.Wait()
	if got := n.sent(); len(got) != 1 {
		t.Fatalf("a CLI import notified: %+v", got)
	}
}

// With the Notifications app off, imports are not held up and the log says
// so once; other failures are logged per import.
func TestNotificationsAppOffIsLoggedOnce(t *testing.T) {
	n := &notificationServer{off: true}
	server := n.serve(t)
	s, logged := notifyingService(t, server, nil)
	for _, id := range []string{"a1", "a2"} {
		s.notifySettled(queueTestJob(id, "alice"), true, "")
		s.rt.workerWG.Wait()
	}
	if got := strings.Count(logged.String(), "Notifications app is not enabled"); got != 1 {
		t.Fatalf("logged %d times:\n%s", got, logged.String())
	}
	n.mu.Lock()
	n.off, n.fail = false, true
	n.mu.Unlock()
	s.notifySettled(queueTestJob("a3", "alice"), true, "")
	s.rt.workerWG.Wait()
	if !strings.Contains(logged.String(), "import a3: notify alice: AppAPI notification: HTTP 500") {
		t.Fatalf("a failed notification is not logged:\n%s", logged.String())
	}
}

func TestImportFailureAdviceSaysWhatToDo(t *testing.T) {
	for errText, want := range map[string]string{
		importDiskShortMessage:                     "Ask your administrator",
		"the import did not finish within 3h50m0s": "took too long",
		"not a valid portable meeting: exit 1":     "not a valid meeting file",
		"mkdir: read-only file system":             "see why and try again",
	} {
		if got := importFailureAdvice(errText); !strings.Contains(got, want) {
			t.Errorf("%q: %q, want %q in it", errText, got, want)
		}
	}
}
