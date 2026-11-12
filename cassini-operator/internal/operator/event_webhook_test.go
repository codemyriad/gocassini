package operator

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type capturedEvent struct {
	event     string
	delivery  string
	timestamp string
	signature string
	body      []byte
}

func newTestEventStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "event-webhook.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newTestEventWebhook(t *testing.T, store *Store, url, secret string, events ...string) *eventWebhook {
	t.Helper()
	settings, err := normalizeEventWebhookSettings(EventWebhookSettings{URL: url, Secret: secret, Events: events})
	if err != nil {
		t.Fatalf("normalizeEventWebhookSettings() error = %v", err)
	}
	hook := newEventWebhook(store, log.New(io.Discard, "", 0), settings, "test")
	if hook == nil {
		t.Fatal("newEventWebhook() = nil, want a configured webhook")
	}
	return hook
}

func startTestEventWebhook(t *testing.T, hook *eventWebhook) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	hook.start(ctx, &wg)
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
}

func waitForEvent(t *testing.T, ch <-chan capturedEvent) capturedEvent {
	t.Helper()
	select {
	case event := <-ch:
		return event
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for webhook delivery")
		return capturedEvent{}
	}
}

func TestEventWebhookSignsAndDelivers(t *testing.T) {
	store := newTestEventStore(t)
	received := make(chan capturedEvent, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- capturedEvent{
			event:     r.Header.Get("X-Cassini-Event"),
			delivery:  r.Header.Get("X-Cassini-Delivery"),
			timestamp: r.Header.Get("X-Cassini-Timestamp"),
			signature: r.Header.Get("X-Cassini-Signature"),
			body:      body,
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	const secret = "test-secret"
	hook := newTestEventWebhook(t, store, server.URL, secret)
	startTestEventWebhook(t, hook)
	if err := hook.enqueue(context.Background(), eventSummaryCompleted, map[string]any{
		"job_id":           "job-1",
		"summary_markdown": "# Summary\n\nDecided to ship.",
	}); err != nil {
		t.Fatalf("enqueue() error = %v", err)
	}

	event := waitForEvent(t, received)
	if event.event != eventSummaryCompleted {
		t.Fatalf("X-Cassini-Event = %q, want %q", event.event, eventSummaryCompleted)
	}
	if event.delivery == "" {
		t.Fatal("X-Cassini-Delivery is empty")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(event.timestamp))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write(event.body)
	if want := "sha256=" + hex.EncodeToString(mac.Sum(nil)); event.signature != want {
		t.Fatalf("X-Cassini-Signature = %q, want %q", event.signature, want)
	}

	var payload outboundEventPayload
	if err := json.Unmarshal(event.body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Type != eventSummaryCompleted {
		t.Fatalf("payload.Type = %q, want %q", payload.Type, eventSummaryCompleted)
	}
	if payload.CassiniVersion != "test" {
		t.Fatalf("payload.CassiniVersion = %q, want test", payload.CassiniVersion)
	}
	if got := payload.Data["summary_markdown"]; got != "# Summary\n\nDecided to ship." {
		t.Fatalf("summary_markdown = %q, want the summary body", got)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var delivered string
		if err := store.db.QueryRow(`SELECT COALESCE(delivered_at, '') FROM event_outbox WHERE id = ?`, event.delivery).Scan(&delivered); err != nil {
			t.Fatalf("read delivered_at: %v", err)
		}
		if delivered != "" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("event was not marked delivered")
}

func TestEventWebhookRetriesTransientFailure(t *testing.T) {
	store := newTestEventStore(t)
	var calls atomic.Int32
	received := make(chan capturedEvent, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		received <- capturedEvent{body: body}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	hook := newTestEventWebhook(t, store, server.URL, "secret", eventRecordingStarted)
	hook.cfg.baseRetryDelay = 10 * time.Millisecond
	startTestEventWebhook(t, hook)
	if err := hook.enqueue(context.Background(), eventRecordingStarted, map[string]any{"job_id": "job-1"}); err != nil {
		t.Fatalf("enqueue() error = %v", err)
	}
	waitForEvent(t, received)
	if got := calls.Load(); got < 2 {
		t.Fatalf("receiver calls = %d, want at least 2 (500 then success)", got)
	}
}

// A 401 while a secret is being rotated, or a 404 while the receiver is being
// redeployed, says the receiver is not ready. The event is retried like any
// other failure, not closed on its first attempt.
func TestEventWebhookRetriesEveryNonSuccessStatus(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			store := newTestEventStore(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			}))
			defer server.Close()

			hook := newTestEventWebhook(t, store, server.URL, "secret", eventRecordingFailed)
			if err := hook.enqueue(context.Background(), eventRecordingFailed, map[string]any{"job_id": "job-1"}); err != nil {
				t.Fatalf("enqueue() error = %v", err)
			}
			hook.deliverDue(context.Background())

			attempts, delivered, failed, lastError := outboxRow(t, store)
			if attempts != 1 || delivered != "" || failed != "" {
				t.Fatalf("attempts=%d delivered=%q failed=%q, want one attempt and the event still queued", attempts, delivered, failed)
			}
			if !strings.Contains(lastError, strconv.Itoa(status)) {
				t.Fatalf("last_error = %q, want it to name HTTP %d", lastError, status)
			}
		})
	}
}

func TestEventWebhookFiltersEvents(t *testing.T) {
	store := newTestEventStore(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	hook := newTestEventWebhook(t, store, server.URL, "secret", eventSummaryCompleted)
	startTestEventWebhook(t, hook)
	if err := hook.enqueue(context.Background(), eventRecordingStarted, map[string]any{"job_id": "job-1"}); err != nil {
		t.Fatalf("enqueue(filtered) error = %v", err)
	}
	if err := hook.enqueue(context.Background(), eventSummaryCompleted, map[string]any{"job_id": "job-1"}); err != nil {
		t.Fatalf("enqueue(allowed) error = %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() == 1 {
			var count int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM event_outbox`).Scan(&count); err != nil {
				t.Fatalf("count outbox: %v", err)
			}
			if count != 1 {
				t.Fatalf("outbox rows = %d, want 1: the filtered event must not be written", count)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("receiver calls = %d, want 1", calls.Load())
}

func TestEventWebhookSettingsValidationAndRedactedLog(t *testing.T) {
	for name, bad := range map[string]EventWebhookSettings{
		"unknown event":      {URL: "https://hooks.example/x", Secret: "s", Events: []string{"bogus.event"}},
		"blank event":        {URL: "https://hooks.example/x", Secret: "s", Events: []string{" "}},
		"url without secret": {URL: "https://hooks.example/x"},
		"relative url":       {URL: "/hook", Secret: "s"},
		"timeout over 1m":    {URL: "https://hooks.example/x", Secret: "s", Timeout: "61s"},
	} {
		if _, err := normalizeEventWebhookSettings(bad); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if s, err := normalizeEventWebhookSettings(EventWebhookSettings{}); err != nil || s.URL != "" {
		t.Fatalf("empty settings = %+v, %v; want delivery off without error", s, err)
	}

	var logs strings.Builder
	settings := EventWebhookSettings{URL: "https://hooks.example/path?token=very-secret", Secret: "signing-secret"}
	if newEventWebhook(newTestEventStore(t), log.New(&logs, "", 0), settings, "test") == nil {
		t.Fatal("newEventWebhook() = nil, want a configured webhook")
	}
	if strings.Contains(logs.String(), "token=very-secret") || strings.Contains(logs.String(), "/path") || strings.Contains(logs.String(), "signing-secret") {
		t.Fatalf("startup log leaked the webhook path, query or secret: %q", logs.String())
	}
	if !strings.Contains(logs.String(), "https://hooks.example") {
		t.Fatalf("startup log does not name the receiver host: %q", logs.String())
	}
}

// A shutdown cancels rt.ctx. The outbox insert must still happen, because that
// is the write that lets the event survive the restart.
func TestEmitEventSurvivesShutdown(t *testing.T) {
	store := newTestEventStore(t)
	insertJob(t, store.db, "job-canceled", nowUTCString())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rt := &Runtime{store: store, logger: log.New(io.Discard, "", 0), ctx: ctx}
	rt.eventWebhook.Store(newTestEventWebhook(t, store, "https://hooks.example/events", "secret"))
	rt.emitEvent(eventRecordingStarted, "job-canceled", 1, nil)

	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM event_outbox`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("outbox rows = %d, want the event inserted although the operator is stopping", count)
	}
}

func outboxRow(t *testing.T, store *Store) (attempts int, delivered, failed, lastError string) {
	t.Helper()
	if err := store.db.QueryRow(`SELECT attempts, COALESCE(delivered_at, ''), COALESCE(failed_at, ''), COALESCE(last_error, '') FROM event_outbox`).
		Scan(&attempts, &delivered, &failed, &lastError); err != nil {
		t.Fatal(err)
	}
	return attempts, delivered, failed, lastError
}

// Go follows a redirect by re-issuing the POST as a GET without its body. The
// page it lands on answering 200 must not count as a delivery.
func TestEventWebhookDoesNotFollowRedirects(t *testing.T) {
	store := newTestEventStore(t)
	var landed atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		landed.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer server.Close()

	hook := newTestEventWebhook(t, store, server.URL, "secret")
	if err := hook.enqueue(context.Background(), eventRecordingStarted, map[string]any{"job_id": "job-1"}); err != nil {
		t.Fatal(err)
	}
	hook.deliverDue(context.Background())

	attempts, delivered, failed, lastError := outboxRow(t, store)
	if delivered != "" || landed.Load() != 0 {
		t.Fatalf("delivered=%q, requests that followed the redirect=%d; want neither", delivered, landed.Load())
	}
	if attempts != 1 || failed != "" || !strings.Contains(lastError, "302") {
		t.Fatalf("attempts=%d failed=%q last_error=%q, want one retryable failure naming the 302", attempts, failed, lastError)
	}
}

// Retries are bounded by the event's age, not by a count: a receiver that is
// down for an upgrade gets its events once it is back, and an event a day old
// is closed without another request.
func TestEventWebhookRetriesUntilTheEventIsADayOld(t *testing.T) {
	store := newTestEventStore(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	hook := newTestEventWebhook(t, store, server.URL, "secret")
	ctx := context.Background()
	fresh := nowUTCString()
	if err := store.insertEvent(ctx, "evt_1", eventRecordingStarted, `{}`, fresh); err != nil {
		t.Fatal(err)
	}
	// Far more attempts than any count-based ceiling, and still retried.
	hook.reschedule(outboxEvent{ID: "evt_1", Attempts: 200, CreatedAt: fresh}, "receiver returned HTTP 502")
	attempts, _, failed, _ := outboxRow(t, store)
	if failed != "" || attempts != 201 {
		t.Fatalf("attempts=%d failed=%q, want the fresh event rescheduled", attempts, failed)
	}
	var next string
	if err := store.db.QueryRow(`SELECT next_attempt_at FROM event_outbox`).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if at, err := time.Parse(sortableUTCLayout, next); err != nil || time.Until(at) > eventWebhookRetryCap+time.Second {
		t.Fatalf("next attempt at %q (%v), want at most the five-minute cap away", next, err)
	}

	old := formatUTCString(time.Now().Add(-eventWebhookRetryWindow - time.Minute))
	if _, err := store.db.Exec(`UPDATE event_outbox SET created_at = ?, next_attempt_at = ?`, old, old); err != nil {
		t.Fatal(err)
	}
	hook.deliverDue(ctx)
	if _, _, failed, lastError := outboxRow(t, store); failed == "" || !strings.Contains(lastError, "gave up") {
		t.Fatalf("failed=%q last_error=%q, want a day-old event closed", failed, lastError)
	}
	if calls.Load() != 0 {
		t.Fatalf("receiver calls = %d, want none for an event past the window", calls.Load())
	}
}

// A row holds a meeting's title and its whole summary. It is deleted a week
// after it was written, whatever became of it.
func TestEventOutboxPrunesRowsAWeekOld(t *testing.T) {
	store := newTestEventStore(t)
	now := time.Now().UTC()
	ctx := context.Background()
	for id, created := range map[string]time.Time{"evt_old": now.Add(-8 * 24 * time.Hour), "evt_recent": now.Add(-time.Hour)} {
		if err := store.insertEvent(ctx, id, eventRecordingStarted, `{}`, formatUTCString(created)); err != nil {
			t.Fatal(err)
		}
	}
	store.pruneEventOutbox(log.New(io.Discard, "", 0), now)
	var ids string
	if err := store.db.QueryRow(`SELECT group_concat(id) FROM event_outbox`).Scan(&ids); err != nil {
		t.Fatal(err)
	}
	if ids != "evt_recent" {
		t.Fatalf("rows left = %q, want only evt_recent", ids)
	}
}

func eventWebhookRuntimeForTest(t *testing.T) *Runtime {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	rt := &Runtime{
		ctx: ctx, store: newTestEventStore(t), logger: log.New(io.Discard, "", 0),
		eventWebhookSettingsPath: filepath.Join(t.TempDir(), "event-webhook-settings.json"),
	}
	t.Cleanup(func() { cancel(); rt.workerWG.Wait() })
	return rt
}

func putEventWebhookSettings(t *testing.T, rt *Runtime, body string) (int, map[string]any) {
	t.Helper()
	method := http.MethodPut
	if body == "" {
		method = http.MethodGet
	}
	rec := httptest.NewRecorder()
	rt.eventWebhookSettingsHandler(rec, httptest.NewRequest(method, "/settings/webhook", strings.NewReader(body)))
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response %q is not JSON: %v", rec.Body.String(), err)
	}
	return rec.Code, out
}

// The receiver is configured through its own endpoint and kept in its own
// owner-only file. The secret goes in and never comes back out.
func TestEventWebhookSettingsEndpoint(t *testing.T) {
	rt := eventWebhookRuntimeForTest(t)
	rt.startEventWebhook()
	if code, out := putEventWebhookSettings(t, rt, ""); code != http.StatusOK || out["url"] != "" || out["secret_set"] != false {
		t.Fatalf("GET with nothing stored = %d %v, want delivery off", code, out)
	}
	for name, body := range map[string]string{
		"url without secret": `{"url":"https://hooks.example/x"}`,
		"unknown event":      `{"url":"https://hooks.example/x","secret":"s","events":["summary.complete"]}`,
		"blank event":        `{"url":"https://hooks.example/x","secret":"s","events":[""]}`,
		"timeout over 1m":    `{"url":"https://hooks.example/x","secret":"s","timeout":"2m"}`,
		"timeout not a time": `{"url":"https://hooks.example/x","secret":"s","timeout":"soon"}`,
		"unknown field":      `{"url":"https://hooks.example/x","secret":"s","retries":3}`,
	} {
		if code, out := putEventWebhookSettings(t, rt, body); code != http.StatusBadRequest {
			t.Errorf("%s: PUT = %d %v, want 400", name, code, out)
		}
	}
	if rt.eventWebhook.Load() != nil {
		t.Fatal("a refused PUT started a worker")
	}

	code, out := putEventWebhookSettings(t, rt, `{"url":"https://hooks.example/x?token=t","secret":"signing-secret","events":["summary.completed"]}`)
	if code != http.StatusOK || out["secret_set"] != true || out["url"] != "https://hooks.example/x?token=t" || out["timeout"] != "10s" {
		t.Fatalf("PUT = %d %v, want the receiver saved with the default timeout", code, out)
	}
	if _, leaked := out["secret"]; leaked {
		t.Fatalf("the response carries the secret: %v", out)
	}
	if !rt.eventEnabled(eventSummaryCompleted) || rt.eventEnabled(eventRecordingStarted) {
		t.Fatal("the worker does not follow the saved allow-list")
	}
	info, err := os.Stat(rt.eventWebhookSettingsPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("settings file mode = %v, %v; want 0600", info, err)
	}

	// A body that leaves the secret out keeps it; clearing the URL turns
	// delivery off.
	if code, out := putEventWebhookSettings(t, rt, `{"events":[]}`); code != http.StatusOK || out["secret_set"] != true || !rt.eventEnabled(eventRecordingStarted) {
		t.Fatalf("PUT without a secret = %d %v, want the stored secret kept and the whole catalog on", code, out)
	}
	if code, out := putEventWebhookSettings(t, rt, `{"timeout":"30s"}`); code != http.StatusOK || out["timeout"] != "30s" || rt.eventWebhook.Load().client.Timeout != 30*time.Second {
		t.Fatalf("PUT timeout = %d %v, want a 30s request timeout", code, out)
	}
	if code, out := putEventWebhookSettings(t, rt, `{"timeout":""}`); code != http.StatusOK || out["timeout"] != "10s" {
		t.Fatalf("PUT empty timeout = %d %v, want the default back", code, out)
	}
	if code, _ := putEventWebhookSettings(t, rt, `{"url":""}`); code != http.StatusOK || rt.eventWebhook.Load() != nil {
		t.Fatalf("clearing the URL = %d, worker=%v; want delivery off", code, rt.eventWebhook.Load())
	}
}

// A stored file that is not valid turns delivery off and says why. Nothing
// else reads it, and the next PUT replaces it.
func TestInvalidStoredEventWebhookSettingsOnlyDisableDelivery(t *testing.T) {
	rt := eventWebhookRuntimeForTest(t)
	if err := os.WriteFile(rt.eventWebhookSettingsPath, []byte(`{"url":"https://hooks.example/x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rt.startEventWebhook()
	code, out := putEventWebhookSettings(t, rt, "")
	if code != http.StatusOK || rt.eventWebhook.Load() != nil || !strings.Contains(out["error"].(string), "secret is required") {
		t.Fatalf("GET = %d %v, worker=%v; want delivery off with the reason", code, out, rt.eventWebhook.Load())
	}
	code, out = putEventWebhookSettings(t, rt, `{"url":"https://hooks.example/x","secret":"s"}`)
	if _, stillThere := out["error"]; code != http.StatusOK || stillThere || rt.eventWebhook.Load() == nil {
		t.Fatalf("PUT = %d %v, want the receiver accepted and the reason gone", code, out)
	}
}

// Replacing the worker (a rotated secret, a new allow-list) waits for the old
// one, so the same undelivered row is never read by two workers, and the
// delivery it interrupted is not counted against the event.
func TestReplacingTheWorkerWaitsForTheOldOne(t *testing.T) {
	rt := eventWebhookRuntimeForTest(t)
	inFlight := make(chan struct{}, 1)
	var posts atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read first: the server only notices a client hanging up once the
		// request body has been consumed.
		_, _ = io.Copy(io.Discard, r.Body)
		if posts.Add(1) == 1 {
			inFlight <- struct{}{}
			select {
			case <-r.Context().Done(): // held until the old worker abandons it
			case <-release:
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	defer close(release)

	if code, out := putEventWebhookSettings(t, rt, `{"url":"`+server.URL+`","secret":"old-secret"}`); code != http.StatusOK {
		t.Fatalf("PUT = %d %v", code, out)
	}
	old := rt.eventWebhook.Load()
	if err := old.enqueue(context.Background(), eventRecordingStarted, map[string]any{"job_id": "job-1"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-inFlight:
	case <-time.After(5 * time.Second):
		t.Fatal("the first worker never posted")
	}

	if code, out := putEventWebhookSettings(t, rt, `{"secret":"new-secret"}`); code != http.StatusOK {
		t.Fatalf("PUT = %d %v", code, out)
	}
	select {
	case <-old.done:
	default:
		t.Fatal("the new worker was started while the old one was still running")
	}
	if now := rt.eventWebhook.Load(); now == old || now == nil {
		t.Fatal("the worker was not replaced")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if attempts, delivered, failed, _ := outboxRow(t, rt.store); delivered != "" {
			if attempts != 0 || failed != "" {
				t.Fatalf("attempts=%d failed=%q, want the interrupted delivery not counted", attempts, failed)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the new worker did not deliver the event the old one was interrupted on")
}

// A worker whose context is cancelled stops; the read it can no longer make is
// not reported as a database fault.
func TestCancelledWorkerStopsQuietly(t *testing.T) {
	store := newTestEventStore(t)
	var logs strings.Builder
	hook := newTestEventWebhook(t, store, "http://127.0.0.1:9/events", "secret")
	hook.logger = log.New(&logs, "", 0)
	if err := hook.enqueue(context.Background(), eventRecordingStarted, map[string]any{"job_id": "job-1"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	hook.deliverDue(ctx)
	if attempts, _, failed, _ := outboxRow(t, store); attempts != 0 || failed != "" {
		t.Fatalf("attempts=%d failed=%q, want the event untouched", attempts, failed)
	}
	if logs.Len() != 0 {
		t.Fatalf("a cancelled worker logged %q", logs.String())
	}
}

// Shutdown waits on the workers. A settings request still in flight must not
// add one behind its back.
func TestNoEventWebhookWorkerStartsAfterShutdownBegan(t *testing.T) {
	rt := eventWebhookRuntimeForTest(t)
	rt.stopEventWebhook()
	if code, out := putEventWebhookSettings(t, rt, `{"url":"https://hooks.example/events","secret":"secret"}`); code != http.StatusOK {
		t.Fatalf("PUT = %d %v", code, out)
	}
	if rt.eventWebhook.Load() != nil {
		t.Fatal("a delivery worker was started after shutdown began")
	}
	rt.workerWG.Wait()
}
