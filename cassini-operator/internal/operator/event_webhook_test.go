package operator

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testSecret is long enough for a PUT: one sets at least 32 characters.
const testSecret = "0123456789abcdef0123456789abcdef"

type capturedEvent struct {
	event     string
	delivery  string
	timestamp string
	signature string
	body      []byte
}

func captureEvent(r *http.Request) capturedEvent {
	body, _ := io.ReadAll(r.Body)
	return capturedEvent{
		event: r.Header.Get("X-Cassini-Event"), delivery: r.Header.Get("X-Cassini-Delivery"),
		timestamp: r.Header.Get("X-Cassini-Timestamp"), signature: r.Header.Get("X-Cassini-Signature"), body: body,
	}
}

func checkSignature(t *testing.T, event capturedEvent, secret string) {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(event.timestamp))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write(event.body)
	if want := "sha256=" + hex.EncodeToString(mac.Sum(nil)); event.signature != want {
		t.Fatalf("X-Cassini-Signature = %q, want %q", event.signature, want)
	}
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

// newTestEventWebhook builds a worker for events, or for the whole catalog
// when none are named.
func newTestEventWebhook(t *testing.T, store *Store, url, secret string, events ...string) *eventWebhook {
	t.Helper()
	if len(events) == 0 {
		events = eventCatalog
	}
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

// queueDueEvents writes n events for hook, due now.
func queueDueEvents(t *testing.T, hook *eventWebhook, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := hook.store.insertEvent(context.Background(), "evt_"+strconv.Itoa(i), hook.cfg.receiver(), eventRecordingStarted, `{}`, nowUTCString()); err != nil {
			t.Fatal(err)
		}
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

func outboxIDs(t *testing.T, store *Store) string {
	t.Helper()
	var ids sql.NullString
	if err := store.db.QueryRow(`SELECT group_concat(id) FROM (SELECT id FROM event_outbox ORDER BY id)`).Scan(&ids); err != nil {
		t.Fatal(err)
	}
	return ids.String
}

func TestEventWebhookSignsAndDelivers(t *testing.T) {
	store := newTestEventStore(t)
	received := make(chan capturedEvent, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- captureEvent(r)
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
	if event.event != eventSummaryCompleted || event.delivery == "" {
		t.Fatalf("X-Cassini-Event = %q, X-Cassini-Delivery = %q", event.event, event.delivery)
	}
	checkSignature(t, event, secret)
	var payload outboundEventPayload
	if err := json.Unmarshal(event.body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Type != eventSummaryCompleted || payload.CassiniVersion != "test" || payload.Data["summary_markdown"] != "# Summary\n\nDecided to ship." {
		t.Fatalf("payload = %+v", payload)
	}

	// Delivered, and the payload (title, summary) is not kept.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var delivered, body string
		if err := store.db.QueryRow(`SELECT COALESCE(delivered_at, ''), payload FROM event_outbox WHERE id = ?`, event.delivery).Scan(&delivered, &body); err != nil {
			t.Fatal(err)
		}
		if delivered != "" {
			if body != "" {
				t.Fatalf("delivered row keeps its payload %q", body)
			}
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
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		received <- captureEvent(r)
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
}

// Every non-2xx is retried, not closed. A 4xx answers one request, so the pass
// goes on; a receiver that is down (5xx, 429, redirect, refused address, no
// answer) ends the pass after one attempt and is left alone until its backoff
// is over. A redirect is never followed.
func TestEventWebhookFailedAttempts(t *testing.T) {
	var landed atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { landed.Add(1) }))
	defer target.Close()
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()

	for _, tc := range []struct {
		name      string
		status    int
		url       string // instead of the test server
		wantError string
		wantCalls int32 // in one pass over three due events
	}{
		{name: "400", status: 400, wantError: "HTTP 400", wantCalls: 3},
		{name: "401", status: 401, wantError: "HTTP 401", wantCalls: 3},
		{name: "404", status: 404, wantError: "HTTP 404", wantCalls: 3},
		{name: "500", status: 500, wantError: "HTTP 500", wantCalls: 1},
		{name: "503", status: 503, wantError: "HTTP 503", wantCalls: 1},
		{name: "429", status: 429, wantError: "HTTP 429", wantCalls: 1},
		{name: "redirect", status: http.StatusFound, wantError: "HTTP 302", wantCalls: 1},
		{name: "no answer", url: closed.URL, wantError: "connection refused"},
		{name: "metadata address", url: "http://169.254.169.254/latest/meta-data", wantError: "request: address not allowed: link-local/metadata (169.254.169.254)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if tc.status == http.StatusFound {
					http.Redirect(w, r, target.URL, http.StatusFound)
					return
				}
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			url := server.URL
			if tc.url != "" {
				url = tc.url
			}
			store := newTestEventStore(t)
			hook := newTestEventWebhook(t, store, url, "secret", eventRecordingStarted)
			queueDueEvents(t, hook, 3)
			hook.deliverDue(context.Background())
			if calls.Load() != tc.wantCalls {
				t.Fatalf("calls = %d, want %d", calls.Load(), tc.wantCalls)
			}
			var tried, waiting int
			var lastError, lastErrorAt string
			if err := store.db.QueryRow(`SELECT COUNT(*) FILTER (WHERE attempts > 0), COUNT(*) FILTER (WHERE `+eventWaiting+`), MAX(last_error), MAX(last_error_at) FROM event_outbox`).
				Scan(&tried, &waiting, &lastError, &lastErrorAt); err != nil {
				t.Fatal(err)
			}
			wantTried := 3
			if tc.wantCalls < 3 {
				wantTried = 1
			}
			if tried != wantTried || waiting != 3 || !strings.Contains(lastError, tc.wantError) || lastErrorAt == "" {
				t.Fatalf("tried=%d waiting=%d last_error=%q at %q; want %d tried, all still waiting, an error naming %q", tried, waiting, lastError, lastErrorAt, wantTried, tc.wantError)
			}
			if wantTried == 1 {
				// The next pass comes before the receiver's backoff is over.
				hook.deliverDue(context.Background())
				if err := store.db.QueryRow(`SELECT COUNT(*) FROM event_outbox WHERE attempts > 0`).Scan(&tried); err != nil || tried != 1 {
					t.Fatalf("tried after a second pass = %d, want the receiver left alone", tried)
				}
			}
		})
	}
	if landed.Load() != 0 {
		t.Fatalf("a redirect was followed %d times", landed.Load())
	}
}

// When the outbox cannot record a failed attempt, the same event is due again
// at once: the worker backs off instead of posting it every pass.
func TestEventWebhookStopsWhenAFailedAttemptCannotBeRecorded(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	store := newTestEventStore(t)
	hook := newTestEventWebhook(t, store, server.URL, "secret", eventRecordingStarted)
	hook.cfg.baseRetryDelay = time.Hour
	queueDueEvents(t, hook, 2)
	if _, err := store.db.Exec(`CREATE TRIGGER outbox_read_only BEFORE UPDATE ON event_outbox BEGIN SELECT RAISE(ABORT, 'read-only'); END`); err != nil {
		t.Fatal(err)
	}
	hook.deliverDue(context.Background())
	hook.deliverDue(context.Background())
	if calls.Load() != 1 {
		t.Fatalf("receiver calls = %d, want one: the pass stops and the worker backs off", calls.Load())
	}
}

// Retry-After on a 429 or 503 (seconds or an HTTP date) delays the retry, never
// below the usual backoff nor beyond the retry window.
func TestEventWebhookHonoursRetryAfter(t *testing.T) {
	const inAnHour = "<an hour after the answer>"
	for _, tc := range []struct {
		name       string
		status     int
		retryAfter string
		attempts   int
		want       time.Duration
	}{
		{"429 seconds", http.StatusTooManyRequests, "120", 0, 120 * time.Second},
		{"503 date", http.StatusServiceUnavailable, inAnHour, 0, time.Hour},
		{"capped at the window", http.StatusTooManyRequests, "999999", 0, eventWebhookRetryWindow},
		{"never sooner than the backoff", http.StatusServiceUnavailable, "5", 8, 256 * time.Second},
		{"not a number or a date", http.StatusTooManyRequests, "soon", 0, time.Second},
		{"ignored on a 500", http.StatusInternalServerError, "120", 0, time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestEventStore(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				retryAfter := tc.retryAfter
				if retryAfter == inAnHour {
					retryAfter = time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)
				}
				w.Header().Set("Retry-After", retryAfter)
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			hook := newTestEventWebhook(t, store, server.URL, "secret", eventRecordingStarted)
			queueDueEvents(t, hook, 1)
			if _, err := store.db.Exec(`UPDATE event_outbox SET attempts = ?`, tc.attempts); err != nil {
				t.Fatal(err)
			}
			before := time.Now()
			hook.deliverDue(context.Background())
			var next string
			if err := store.db.QueryRow(`SELECT next_attempt_at FROM event_outbox`).Scan(&next); err != nil {
				t.Fatal(err)
			}
			at, err := time.Parse(sortableUTCLayout, next)
			if err != nil {
				t.Fatal(err)
			}
			// HTTP dates have whole seconds: allow a couple either way.
			if got := at.Sub(before); got < tc.want-2*time.Second || got > tc.want+2*time.Second {
				t.Fatalf("next attempt in %s, want about %s", got, tc.want)
			}
		})
	}
}

// Retries are bounded by the event's age, not a count; a day-old event is
// given up on without another request.
func TestEventWebhookRetriesUntilTheEventIsADayOld(t *testing.T) {
	store := newTestEventStore(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	hook := newTestEventWebhook(t, store, server.URL, "secret")
	queueDueEvents(t, hook, 1)
	if _, err := store.db.Exec(`UPDATE event_outbox SET attempts = 200`); err != nil {
		t.Fatal(err)
	}
	hook.deliverDue(context.Background())
	attempts, _, failed, _ := outboxRow(t, store)
	if failed != "" || attempts != 201 || calls.Load() != 1 {
		t.Fatalf("attempts=%d failed=%q calls=%d, want the fresh event retried", attempts, failed, calls.Load())
	}

	old := formatUTCString(time.Now().Add(-eventWebhookRetryWindow - time.Minute))
	if _, err := store.db.Exec(`UPDATE event_outbox SET created_at = ?, next_attempt_at = ?`, old, old); err != nil {
		t.Fatal(err)
	}
	hook.deliverDue(context.Background())
	if _, _, failed, lastError := outboxRow(t, store); failed == "" || !strings.Contains(lastError, "gave up") || calls.Load() != 1 {
		t.Fatalf("failed=%q last_error=%q calls=%d, want a day-old event given up unsent", failed, lastError, calls.Load())
	}
}

// The prune gives up on waiting rows past the retry window plus its margin,
// whoever they were queued for and with or without a worker, emptying their
// payload; it deletes rows a week old whatever became of them.
func TestEventOutboxPrune(t *testing.T) {
	store := newTestEventStore(t)
	now := time.Now().UTC()
	const body = `{"data":{"title":"Board meeting"}}`
	for id, created := range map[string]time.Time{
		"evt_week":   now.Add(-8 * 24 * time.Hour),
		"evt_stale":  now.Add(-eventWebhookRetryWindow - eventOutboxGiveUpMargin - time.Minute),
		"evt_margin": now.Add(-eventWebhookRetryWindow - time.Minute),
		"evt_recent": now.Add(-time.Hour),
	} {
		if err := store.insertEvent(context.Background(), id, "rcv_gone", eventSummaryCompleted, body, formatUTCString(created)); err != nil {
			t.Fatal(err)
		}
	}
	store.pruneEventOutbox(log.New(io.Discard, "", 0), now)
	if ids := outboxIDs(t, store); ids != "evt_margin,evt_recent,evt_stale" {
		t.Fatalf("rows left = %q, want the week-old one deleted", ids)
	}
	for id, wantGivenUp := range map[string]bool{"evt_stale": true, "evt_margin": false, "evt_recent": false} {
		var payload, failed, lastError string
		if err := store.db.QueryRow(`SELECT payload, COALESCE(failed_at, ''), COALESCE(last_error, '') FROM event_outbox WHERE id = ?`, id).Scan(&payload, &failed, &lastError); err != nil {
			t.Fatal(err)
		}
		if givenUp := failed != "" && payload == "" && strings.Contains(lastError, "gave up"); givenUp != wantGivenUp || (!wantGivenUp && payload != body) {
			t.Errorf("%s: payload=%q failed=%q last_error=%q, want given up=%v", id, payload, failed, lastError, wantGivenUp)
		}
	}

	// Started with delivery off, the operator prunes at once.
	rt := eventWebhookRuntimeForTest(t)
	if err := rt.store.insertEvent(context.Background(), "evt_stale", "rcv_gone", eventSummaryCompleted, body, formatUTCString(now.Add(-eventWebhookRetryWindow-eventOutboxGiveUpMargin-time.Minute))); err != nil {
		t.Fatal(err)
	}
	rt.startEventWebhook()
	if _, _, failed, _ := outboxRow(t, rt.store); rt.eventWebhook.Load() != nil || failed == "" {
		t.Fatalf("worker=%v failed_at=%q, want no worker and the stale row given up", rt.eventWebhook.Load(), failed)
	}
}

// Finishing a row empties its payload, and a row is never both delivered and
// given up: a delivery wins, in either order.
func TestFinishedEvents(t *testing.T) {
	store := newTestEventStore(t)
	ctx := context.Background()
	const body = `{"data":{"title":"Board meeting"}}`
	for _, id := range []string{"evt_delivered", "evt_failed", "evt_waiting", "evt_failed_then_delivered", "evt_delivered_then_failed"} {
		if err := store.insertEvent(ctx, id, "rcv_test", eventSummaryCompleted, body, nowUTCString()); err != nil {
			t.Fatal(err)
		}
	}
	for _, step := range []struct {
		id        string
		delivered bool
	}{
		{"evt_delivered", true}, {"evt_failed", false},
		{"evt_failed_then_delivered", false}, {"evt_failed_then_delivered", true},
		{"evt_delivered_then_failed", true}, {"evt_delivered_then_failed", false},
	} {
		var err error
		if step.delivered {
			err = store.markEventDelivered(ctx, step.id, nowUTCString())
		} else {
			err = store.markEventFailed(ctx, step.id, nowUTCString(), "gave up")
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	for id, want := range map[string]string{
		"evt_delivered": "delivered", "evt_failed": "failed", "evt_waiting": "waiting",
		"evt_failed_then_delivered": "delivered", "evt_delivered_then_failed": "delivered",
	} {
		var payload, state, errorAt string
		if err := store.db.QueryRow(`SELECT payload, CASE WHEN delivered_at IS NOT NULL AND failed_at IS NOT NULL THEN 'both'
  WHEN delivered_at IS NOT NULL THEN 'delivered' WHEN failed_at IS NOT NULL THEN 'failed' ELSE 'waiting' END, COALESCE(last_error_at, '')
FROM event_outbox WHERE id = ?`, id).Scan(&payload, &state, &errorAt); err != nil {
			t.Fatal(err)
		}
		if state != want || (want == "waiting") != (payload == body) || (want == "failed") != (errorAt != "") {
			t.Errorf("%s: state=%s payload=%q last_error_at=%q, want %s", id, state, payload, errorAt, want)
		}
	}
}

func TestEventWebhookFiltersEvents(t *testing.T) {
	store := newTestEventStore(t)
	hook := newTestEventWebhook(t, store, "http://127.0.0.1:1/never", "secret", eventSummaryCompleted)
	for _, eventType := range []string{eventRecordingStarted, eventSummaryCompleted} {
		if err := hook.enqueue(context.Background(), eventType, map[string]any{"job_id": "job-1"}); err != nil {
			t.Fatal(err)
		}
	}
	var types string
	if err := store.db.QueryRow(`SELECT group_concat(type) FROM event_outbox`).Scan(&types); err != nil || types != eventSummaryCompleted {
		t.Fatalf("queued = %q, %v; want only the ticked event", types, err)
	}
	// A worker built with no events sends nothing.
	if newEventWebhook(store, log.New(io.Discard, "", 0), EventWebhookSettings{URL: "https://hooks.example/x", Secret: "s"}, "test").enabled(eventSummaryCompleted) {
		t.Fatal("a worker with no events is enabled")
	}
}

func TestEventWebhookLogNeverCarriesTheSecretOrPath(t *testing.T) {
	var logs strings.Builder
	settings := EventWebhookSettings{URL: "https://hooks.example/path?token=very-secret", Secret: "signing-secret", Events: eventCatalog}
	newEventWebhook(newTestEventStore(t), log.New(&logs, "", 0), settings, "test")
	if strings.Contains(logs.String(), "very-secret") || strings.Contains(logs.String(), "/path") || strings.Contains(logs.String(), "signing-secret") || !strings.Contains(logs.String(), "https://hooks.example") {
		t.Fatalf("startup log = %q, want the host only", logs.String())
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
	if ids := outboxIDs(t, store); ids == "" {
		t.Fatal("no outbox row, want the event inserted although the operator is stopping")
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

// putEventWebhookSettings PUTs body, or GETs when it is empty.
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

func mustPutEventWebhookSettings(t *testing.T, rt *Runtime, body string) map[string]any {
	t.Helper()
	code, out := putEventWebhookSettings(t, rt, body)
	if code != http.StatusOK {
		t.Fatalf("PUT %s = %d %v", body, code, out)
	}
	return out
}

const zeroDelivery = `{"delivered":0,"failed":0,"last_delivered_at":"","last_error":"","last_error_at":"","waiting":0}`

// The receiver has its own endpoint and owner-only file. The secret goes in
// and never comes back out; a field left out keeps its stored value.
func TestEventWebhookSettingsEndpoint(t *testing.T) {
	rt := eventWebhookRuntimeForTest(t)
	rt.startEventWebhook()
	code, out := putEventWebhookSettings(t, rt, "")
	if got, _ := json.Marshal(out["delivery"]); code != http.StatusOK || out["url"] != "" || out["secret_set"] != false || string(got) != zeroDelivery {
		t.Fatalf("GET with nothing stored = %d %v, want delivery off", code, out)
	}
	if catalog, _ := json.Marshal(out["catalog"]); string(catalog) != `["recording.started","recording.stopped","recording.published","recording.failed","summary.completed","summary.failed"]` {
		t.Fatalf("catalog = %s, want the declared order", catalog)
	}

	out = mustPutEventWebhookSettings(t, rt, `{"url":"https://hooks.example/x?token=t","secret":"`+testSecret+`","events":["summary.failed","summary.completed","summary.failed"]}`)
	if out["secret_set"] != true || out["url"] != "https://hooks.example/x?token=t" || out["timeout_seconds"] != float64(10) {
		t.Fatalf("PUT = %v, want the receiver saved with the default timeout", out)
	}
	if events, _ := json.Marshal(out["events"]); string(events) != `["summary.completed","summary.failed"]` {
		t.Fatalf("events = %s, want catalog order, once each", events)
	}
	if _, leaked := out["secret"]; leaked {
		t.Fatalf("the response carries the secret: %v", out)
	}
	if !rt.eventEnabled(eventSummaryCompleted) || rt.eventEnabled(eventRecordingStarted) {
		t.Fatal("the worker does not follow the saved allow-list")
	}
	if info, err := os.Stat(rt.eventWebhookSettingsPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("settings file mode = %v, %v; want 0600", info, err)
	}

	out = mustPutEventWebhookSettings(t, rt, `{"events":["recording.started"],"timeout_seconds":30}`)
	if out["secret_set"] != true || !rt.eventEnabled(eventRecordingStarted) || rt.eventWebhook.Load().client.Timeout != 30*time.Second {
		t.Fatalf("PUT without a secret = %v, want the secret kept and the new list and timeout on", out)
	}
}

// Every refused PUT about a field carries an error_code, saves nothing and
// changes nothing. A body that does not parse stays uncoded.
func TestEventWebhookSettingsRefusalsAreCoded(t *testing.T) {
	rt := eventWebhookRuntimeForTest(t)
	rt.startEventWebhook()
	mustPutEventWebhookSettings(t, rt, `{"url":"https://hooks.example/x","secret":"`+testSecret+`","events":["summary.completed"]}`)
	stored, _ := os.ReadFile(rt.eventWebhookSettingsPath)
	with := func(fields string) string {
		return `{"url":"https://hooks.example/x","secret":"` + testSecret + `","events":["summary.completed"]` + fields + `}`
	}
	for _, tc := range []struct{ body, want string }{
		{`{"url":"/hook"}`, "webhook_url_invalid"},
		{`{"url":"ftp://hooks.example/x"}`, "webhook_url_invalid"},
		{`{"secret":""}`, "webhook_secret_required"},
		{`{"events":[]}`, "webhook_events_required"},
		{`{"events":["summary.complete"]}`, "webhook_event_unknown"},
		{`{"events":[" "]}`, "webhook_event_unknown"},
		{`{"events":["webhook.test"]}`, "webhook_event_unknown"},
		{with(`,"timeout_seconds":0`), "webhook_timeout_invalid"},
		{with(`,"timeout_seconds":-1`), "webhook_timeout_invalid"},
		{with(`,"timeout_seconds":61`), "webhook_timeout_invalid"},
		{with(`,"timeout_seconds":2.5`), "webhook_timeout_invalid"},
		{`{"secret":"short"}`, "webhook_secret_too_short"},
		{`{"secret":"` + strings.Repeat("x", 31) + `"}`, "webhook_secret_too_short"},
		{`{"secret":"  ` + strings.Repeat("x", 31) + `  "}`, "webhook_secret_too_short"},
		{`{"secret":"` + strings.Repeat("é", 31) + `"}`, "webhook_secret_too_short"},
		{`{"url":"https://hooks.example/other"}`, "webhook_secret_required_for_new_url"},
		{`{"url":`, ""},
		{`{"retries":3}`, ""},
		{with(`,"timeout_seconds":"10s"`), ""},
		{with(`,"timeout":"10s"`), ""},
	} {
		code, out := putEventWebhookSettings(t, rt, tc.body)
		gotCode, coded := out["error_code"]
		if code != http.StatusBadRequest || out["error"] == "" || (tc.want == "" && coded) || (tc.want != "" && gotCode != tc.want) {
			t.Errorf("PUT %s = %d %v, want 400 %q", tc.body, code, out, tc.want)
		}
	}
	if now, _ := os.ReadFile(rt.eventWebhookSettingsPath); string(now) != string(stored) || rt.eventWebhookSettings.Secret != testSecret {
		t.Fatalf("a refused PUT changed the settings: %s", now)
	}
}

// A new address needs a secret sent with it; the same address does not. A
// secret set through the API is at least 32 characters, while a shorter one
// 0.4.0 stored keeps working until it is replaced.
func TestEventWebhookSecretRules(t *testing.T) {
	rt := eventWebhookRuntimeForTest(t)
	if err := os.WriteFile(rt.eventWebhookSettingsPath, []byte(`{"url":"http://127.0.0.1:1/a","secret":"short","events":["summary.completed"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rt.startEventWebhook()
	if out := mustPutEventWebhookSettings(t, rt, `{"url":"http://127.0.0.1:1/a","timeout_seconds":5}`); out["secret_set"] != true || rt.eventWebhookSettings.Secret != "short" {
		t.Fatalf("same URL = %v, want the stored short secret kept", out)
	}
	mustPutEventWebhookSettings(t, rt, `{"url":"http://127.0.0.1:1/b","secret":"`+strings.Repeat("é", 32)+`"}`)
	if rt.eventWebhookSettings.URL != "http://127.0.0.1:1/b" {
		t.Fatalf("receiver = %q, want the new address with its own secret", rt.eventWebhookSettings.URL)
	}
}

// Turning delivery off keeps nothing of the receiver: its secret, its events
// and its waiting rows go (history stays), whatever else the PUT carried, so
// turning it back on needs a secret again.
func TestTurningDeliveryOffClearsTheReceiver(t *testing.T) {
	rt := eventWebhookRuntimeForTest(t)
	const url = "http://127.0.0.1:1/never"
	mustPutEventWebhookSettings(t, rt, `{"url":"`+url+`","secret":"`+testSecret+`","events":["summary.completed"]}`)
	for _, id := range []string{"evt_wait", "evt_done", "evt_lost"} {
		if err := rt.store.insertEvent(context.Background(), id, eventReceiverKey(url), eventSummaryCompleted, `{}`, nowUTCString()); err != nil {
			t.Fatal(err)
		}
	}
	_ = rt.store.markEventDelivered(context.Background(), "evt_done", nowUTCString())
	_ = rt.store.markEventFailed(context.Background(), "evt_lost", nowUTCString(), "gave up")

	out := mustPutEventWebhookSettings(t, rt, `{"url":"","secret":"short","events":["no.such.event"]}`)
	events, _ := json.Marshal(out["events"])
	delivery, _ := json.Marshal(out["delivery"])
	if out["url"] != "" || out["secret_set"] != false || string(events) != "[]" || string(delivery) != zeroDelivery || rt.eventWebhook.Load() != nil {
		t.Fatalf("off = %v, worker=%v; want nothing of the receiver left", out, rt.eventWebhook.Load())
	}
	if ids := outboxIDs(t, rt.store); ids != "evt_done,evt_lost" {
		t.Fatalf("rows left = %q, want the waiting one deleted and the history kept", ids)
	}
	if stored, _, err := loadEventWebhookSettings(rt.eventWebhookSettingsPath); err != nil || stored.Secret != "" || len(stored.Events) != 0 {
		t.Fatalf("stored = %+v, %v; want no secret and no events", stored, err)
	}
	code, out := putEventWebhookSettings(t, rt, `{"url":"`+url+`","events":["summary.completed"]}`)
	if code != http.StatusBadRequest || out["error_code"] != "webhook_secret_required" {
		t.Fatalf("on again without a secret = %d %v, want webhook_secret_required", code, out)
	}
}

// Every save and every start that loads its settings deletes the waiting rows
// the receiver now in effect does not want: another URL's, an unticked type's,
// or, with delivery off, all of them. A start adopts the rows 0.4.0 queued
// (no receiver) for the stored URL. Settings that fail to load leave the
// queue alone. History is kept.
func TestSavesAndStartsSettleTheQueue(t *testing.T) {
	const url = "http://127.0.0.1:1/never"
	both := `{"url":"` + url + `","secret":"s","events":["recording.started","summary.completed"]}`
	for _, tc := range []struct {
		name, stored, put, left string
	}{
		{"start: settings that failed to load", `{"url":"ftp://nope","secret":"s"}`, "", "evt_done,evt_legacy,evt_mine,evt_other,evt_unticked"},
		{"start: no file", "", "", "evt_done"},
		{"start: delivery off", `{"timeout":"30s"}`, "", "evt_done"},
		{"start: a receiver", `{"url":"` + url + `","secret":"s","events":["summary.completed"]}`, "", "evt_done,evt_legacy,evt_mine"},
		{"save: an event unticked", both, `{"events":["summary.completed"]}`, "evt_done,evt_legacy,evt_mine"},
		{"save: a new URL", both, `{"url":"http://127.0.0.1:1/new","secret":"` + testSecret + `"}`, "evt_done"},
		{"save: delivery off", both, `{"url":""}`, "evt_done"},
		{"save: delivery on after a failed load", `{"url":"ftp://nope","secret":"s"}`, `{"url":"` + url + `","secret":"` + testSecret + `","events":["summary.completed"]}`, "evt_done,evt_mine"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := eventWebhookRuntimeForTest(t)
			if tc.stored != "" {
				if err := os.WriteFile(rt.eventWebhookSettingsPath, []byte(tc.stored), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			later := formatUTCString(time.Now().Add(time.Hour))
			for _, row := range [][3]string{
				{"evt_mine", eventReceiverKey(url), eventSummaryCompleted},
				{"evt_unticked", eventReceiverKey(url), eventRecordingStarted},
				{"evt_other", eventReceiverKey("https://other.example/x"), eventSummaryCompleted},
				{"evt_legacy", "", eventSummaryCompleted},
				{"evt_done", "rcv_gone", eventSummaryCompleted},
			} {
				if err := rt.store.insertEvent(context.Background(), row[0], row[1], row[2], `{}`, later); err != nil {
					t.Fatal(err)
				}
			}
			if err := rt.store.markEventDelivered(context.Background(), "evt_done", nowUTCString()); err != nil {
				t.Fatal(err)
			}
			rt.startEventWebhook()
			if tc.put != "" {
				mustPutEventWebhookSettings(t, rt, tc.put)
			}
			if ids := outboxIDs(t, rt.store); ids != tc.left {
				t.Fatalf("rows left = %q, want %q", ids, tc.left)
			}
		})
	}
}

// Changing the address from A to B: B is sent only what was queued for B, not
// A's waiting event, not a late one a worker still holding A writes after the
// switch, and not a type B does not tick.
func TestChangingTheURLNeverForwardsQueuedEvents(t *testing.T) {
	rt := eventWebhookRuntimeForTest(t)
	received := make(chan capturedEvent, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- captureEvent(r)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	ctx := context.Background()
	mustPutEventWebhookSettings(t, rt, `{"url":"http://127.0.0.1:1/a","secret":"`+testSecret+`","events":["summary.completed"]}`)
	stale := rt.eventWebhook.Load()
	if err := stale.enqueue(ctx, eventSummaryCompleted, map[string]any{"job_id": "job-a"}); err != nil {
		t.Fatal(err)
	}
	mustPutEventWebhookSettings(t, rt, `{"url":"`+server.URL+`","secret":"`+strings.Repeat("b", 32)+`"}`)
	if ids := outboxIDs(t, rt.store); ids != "" {
		t.Fatalf("rows left = %q, want A's waiting event deleted", ids)
	}

	if err := stale.enqueue(ctx, eventSummaryCompleted, map[string]any{"job_id": "job-late"}); err != nil {
		t.Fatal(err)
	}
	if err := rt.store.insertEvent(ctx, "evt_unticked", eventReceiverKey(server.URL), eventRecordingStarted, `{}`, nowUTCString()); err != nil {
		t.Fatal(err)
	}
	if err := rt.eventWebhook.Load().enqueue(ctx, eventSummaryCompleted, map[string]any{"job_id": "job-b"}); err != nil {
		t.Fatal(err)
	}
	got := waitForEvent(t, received)
	var payload outboundEventPayload
	if err := json.Unmarshal(got.body, &payload); err != nil || payload.Data["job_id"] != "job-b" {
		t.Fatalf("B was sent %s, want only its own event", got.body)
	}
	time.Sleep(300 * time.Millisecond)
	select {
	case got := <-received:
		t.Fatalf("B was also sent %s", got.body)
	default:
	}
	out := mustPutEventWebhookSettings(t, rt, "")
	if delivery := out["delivery"].(map[string]any); delivery["delivered"] != float64(1) || delivery["waiting"] != float64(0) {
		t.Fatalf("delivery = %v, want B's one event and nothing else counted", delivery)
	}
}

// 0.4.0's settings file is converted on load: its duration timeout to whole
// seconds (rounded up, at most 60), and a receiver with no event list to the
// events 0.4.0 sent it. A start writes the converted file back, owner-only.
func TestStored040SettingsAreConverted(t *testing.T) {
	for _, tc := range []struct {
		stored        string
		wantEvents    []string
		wantTimeout   int
		wantConverted bool
		wantErr       string
	}{
		{`{"url":"https://hooks.example/x","secret":"s","timeout":"30s"}`, eventWebhookEvents040, 30, true, ""},
		{`{"url":"https://hooks.example/x","secret":"s","events":[]}`, eventWebhookEvents040, 0, true, ""},
		{`{"timeout":"30s"}`, nil, 30, true, ""},
		{`{"url":"https://hooks.example/x","secret":"s","events":["summary.completed"]}`, []string{"summary.completed"}, 0, false, ""},
		{`{"url":"https://hooks.example/x","secret":"s","events":["summary.completed"],"timeout":"500ms"}`, []string{"summary.completed"}, 1, true, ""},
		{`{"url":"https://hooks.example/x","secret":"s","events":["summary.completed"],"timeout":"1500ms"}`, []string{"summary.completed"}, 2, true, ""},
		{`{"url":"https://hooks.example/x","secret":"s","events":["summary.completed"],"timeout":"2m"}`, []string{"summary.completed"}, 60, true, ""},
		{`{"url":"https://hooks.example/x","secret":"s","events":["summary.completed"],"timeout":"soon"}`, nil, 0, false, "timeout"},
	} {
		path := filepath.Join(t.TempDir(), "event-webhook-settings.json")
		if err := os.WriteFile(path, []byte(tc.stored), 0o600); err != nil {
			t.Fatal(err)
		}
		s, converted, err := loadEventWebhookSettings(path)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: err = %v, want one about %s", tc.stored, err, tc.wantErr)
			}
			continue
		}
		if err != nil || converted != tc.wantConverted || !slices.Equal(s.Events, tc.wantEvents) || s.TimeoutSeconds != tc.wantTimeout {
			t.Errorf("%s: %+v converted=%v err=%v; want events %v, timeout %d, converted=%v", tc.stored, s, converted, err, tc.wantEvents, tc.wantTimeout, tc.wantConverted)
		}
	}

	rt := eventWebhookRuntimeForTest(t)
	if err := os.WriteFile(rt.eventWebhookSettingsPath, []byte(`{"url":"https://hooks.example/x","secret":"s","timeout":"30s"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rt.startEventWebhook()
	raw, err := os.ReadFile(rt.eventWebhookSettingsPath)
	info, _ := os.Stat(rt.eventWebhookSettingsPath)
	if err != nil || strings.Contains(string(raw), `"timeout"`) || !strings.Contains(string(raw), `"timeout_seconds": 30`) || !strings.Contains(string(raw), "summary.failed") || info.Mode().Perm() != 0o600 {
		t.Fatalf("file after start = %s (%v), want it written back converted, owner-only", raw, err)
	}
}

// A stored file that is not valid turns delivery off and says why, until a
// PUT replaces it.
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
	out = mustPutEventWebhookSettings(t, rt, `{"url":"https://hooks.example/x","secret":"`+testSecret+`","events":["summary.completed"]}`)
	if _, stillThere := out["error"]; stillThere || rt.eventWebhook.Load() == nil {
		t.Fatalf("PUT = %v, want the receiver accepted and the reason gone", out)
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

	mustPutEventWebhookSettings(t, rt, `{"url":"`+server.URL+`","secret":"`+strings.Repeat("o", 32)+`","events":["recording.started"]}`)
	old := rt.eventWebhook.Load()
	if err := old.enqueue(context.Background(), eventRecordingStarted, map[string]any{"job_id": "job-1"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-inFlight:
	case <-time.After(5 * time.Second):
		t.Fatal("the first worker never posted")
	}

	mustPutEventWebhookSettings(t, rt, `{"secret":"`+strings.Repeat("n", 32)+`"}`)
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
	mustPutEventWebhookSettings(t, rt, `{"url":"https://hooks.example/events","secret":"`+testSecret+`","events":["recording.started"]}`)
	if rt.eventWebhook.Load() != nil {
		t.Fatal("a delivery worker was started after shutdown began")
	}
	rt.workerWG.Wait()
}

// The delivery block covers the current URL only: rows by state, and the
// newest error on an event still waiting for it.
func TestEventWebhookSettingsReportDelivery(t *testing.T) {
	rt := eventWebhookRuntimeForTest(t)
	// A receiver in effect without a worker, so nothing changes the rows.
	rt.eventWebhookSettings = EventWebhookSettings{URL: "https://hooks.example/x", Secret: "s", Events: []string{eventSummaryCompleted}}
	key := eventReceiverKey(rt.eventWebhookSettings.URL)
	for _, row := range []struct {
		id, receiver, eventType, delivered, failed, lastError, lastErrorAt string
	}{
		{"evt_a", key, eventSummaryCompleted, "2026-11-17T09:00:01Z", "", "", ""},
		{"evt_b", key, eventSummaryCompleted, "2026-11-17T09:08:11Z", "", "", ""},
		{"evt_c", key, eventSummaryCompleted, "", "2026-11-17T08:00:00Z", "receiver returned HTTP 500; gave up", "2026-11-17T08:00:00Z"},
		{"evt_d", key, eventSummaryCompleted, "", "", "", ""},
		{"evt_e", key, eventSummaryCompleted, "", "", "receiver returned HTTP 401", "2026-11-17T10:00:00Z"},
		// Another URL's rows, and an unticked type's: newer, and not counted.
		{"evt_f", "rcv_old", eventSummaryCompleted, "2026-11-17T11:00:01Z", "", "", ""},
		{"evt_g", "rcv_old", eventSummaryCompleted, "", "", "receiver returned HTTP 500", "2026-11-17T11:00:00Z"},
		{"evt_h", key, eventRecordingStarted, "", "", "receiver returned HTTP 503", "2026-11-17T11:00:00Z"},
	} {
		if _, err := rt.store.db.Exec(`INSERT INTO event_outbox (id, receiver_id, type, payload, created_at, attempts, next_attempt_at, delivered_at, failed_at, last_error, last_error_at)
VALUES (?, ?, ?, '{}', '2026-11-17T09:00:00Z', 1, '2026-11-17T09:00:00Z', NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''))`,
			row.id, row.receiver, row.eventType, row.delivered, row.failed, row.lastError, row.lastErrorAt); err != nil {
			t.Fatal(err)
		}
	}
	out := mustPutEventWebhookSettings(t, rt, "")
	if got, _ := json.Marshal(out["delivery"]); string(got) != `{"delivered":2,"failed":1,"last_delivered_at":"2026-11-17T09:08:11Z","last_error":"receiver returned HTTP 401","last_error_at":"2026-11-17T10:00:00Z","waiting":2}` {
		t.Fatalf("delivery = %s", got)
	}
	// With evt_e delivered no waiting event has an error: the given-up row's
	// is history.
	if err := rt.store.markEventDelivered(context.Background(), "evt_e", "2026-11-17T10:01:00Z"); err != nil {
		t.Fatal(err)
	}
	out = mustPutEventWebhookSettings(t, rt, "")
	if delivery := out["delivery"].(map[string]any); delivery["last_error"] != "" || delivery["failed"] != float64(1) {
		t.Fatalf("delivery = %v, want no error from the given-up row, still counted", delivery)
	}
	// A summary that cannot be read is zeros, not a failed request.
	if _, err := rt.store.db.Exec(`DROP TABLE event_outbox`); err != nil {
		t.Fatal(err)
	}
	if got, _ := json.Marshal(mustPutEventWebhookSettings(t, rt, "")["delivery"]); string(got) != zeroDelivery {
		t.Fatalf("delivery = %s, want zeros", got)
	}
}

// One test event to the saved receiver, signed like a real one, outside the
// outbox: the receiver's status or why there was none, and how long it took.
// A redirect is reported, not followed; a refused address is never dialled.
func TestEventWebhookTestSend(t *testing.T) {
	var landed atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { landed.Add(1) }))
	defer target.Close()
	release := make(chan struct{})
	defer close(release)
	received := make(chan capturedEvent, 1)
	for _, tc := range []struct {
		name      string
		handler   http.HandlerFunc
		url       string // instead of the test server
		wantCode  string
		wantError string
		status    int
	}{
		{name: "204", status: 204, handler: func(w http.ResponseWriter, r *http.Request) {
			received <- captureEvent(r)
			w.WriteHeader(http.StatusNoContent)
		}},
		{name: "401", status: 401, handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }},
		{name: "500", status: 500, handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }},
		{name: "redirect", status: 302, handler: func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }},
		{name: "slow", wantError: "no answer within 1s", handler: func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body) // so the server notices the client hang up
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}},
		{name: "closed", url: "closed", wantError: "connection refused"},
		{name: "metadata", url: "http://169.254.169.254/latest/meta-data", wantCode: "webhook_address_not_allowed", wantError: "address not allowed: link-local/metadata (169.254.169.254)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := eventWebhookRuntimeForTest(t)
			rt.startEventWebhook()
			url := tc.url
			if tc.handler != nil {
				server := httptest.NewServer(tc.handler)
				defer server.Close()
				url = server.URL + "/hook?token=very-secret"
			} else if url == "closed" {
				server := httptest.NewServer(http.NotFoundHandler())
				url = server.URL + "/hook?token=very-secret"
				server.Close()
			}
			body, _ := json.Marshal(map[string]any{"url": url, "secret": testSecret, "events": []string{eventSummaryCompleted}, "timeout_seconds": 1})
			mustPutEventWebhookSettings(t, rt, string(body))
			rec := httptest.NewRecorder()
			rt.eventWebhookTestHandler(rec, httptest.NewRequest(http.MethodPost, "/settings/webhook/test", nil))
			var out map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &out)
			reason, _ := out["error"].(string)
			_, timed := out["duration_ms"].(float64)
			if rec.Code != http.StatusOK || !timed || out["error_code"] != nilIfEmpty(tc.wantCode) {
				t.Fatalf("POST test = %d %v", rec.Code, out)
			}
			if tc.status != 0 && (out["status"] != float64(tc.status) || reason != "") {
				t.Fatalf("POST test = %v, want status %d", out, tc.status)
			}
			if tc.status == 0 && (!strings.Contains(reason, tc.wantError) || strings.Contains(reason, "very-secret") || out["status"] != nil) {
				t.Fatalf("error = %q, want %q without the URL", reason, tc.wantError)
			}
			if ids := outboxIDs(t, rt.store); ids != "" {
				t.Fatalf("outbox rows %q, want the test event kept out", ids)
			}
			if tc.status == 204 {
				event := waitForEvent(t, received)
				checkSignature(t, event, testSecret)
				var payload map[string]any
				_ = json.Unmarshal(event.body, &payload)
				if data, _ := payload["data"].(map[string]any); event.event != "webhook.test" || payload["type"] != "webhook.test" || payload["id"] != event.delivery || data == nil || len(data) != 0 {
					t.Fatalf("headers %+v, payload %v; want a webhook.test envelope", event, payload)
				}
			}
		})
	}
	if landed.Load() != 0 {
		t.Fatalf("the redirect was followed %d times", landed.Load())
	}
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func TestEventWebhookTestRefusals(t *testing.T) {
	rt := eventWebhookRuntimeForTest(t)
	rt.startEventWebhook()
	rec := httptest.NewRecorder()
	rt.eventWebhookTestHandler(rec, httptest.NewRequest(http.MethodPost, "/settings/webhook/test", nil))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "webhook_not_configured") {
		t.Fatalf("POST test with no receiver = %d %s, want 409 webhook_not_configured", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	rt.eventWebhookTestHandler(rec, httptest.NewRequest(http.MethodGet, "/settings/webhook/test", nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("GET test = %d Allow=%q, want 405 Allow: POST", rec.Code, rec.Header().Get("Allow"))
	}
	// AppAPI only forwards what the manifest declares.
	if got := manifestAccess(manifestRoutes(t), "operator/settings/webhook/test", "POST"); got != "ADMIN" {
		t.Fatalf("manifest access for POST operator/settings/webhook/test = %q, want ADMIN", got)
	}
}

// Link-local and cloud-metadata addresses are refused at dial time, after the
// name is resolved, however they are spelt; private ranges and loopback stay
// allowed.
func TestEventWebhookDialControl(t *testing.T) {
	for address, blocked := range map[string]bool{
		"169.254.169.254:80":          true,
		"169.254.0.1:443":             true,
		"[fe80::1]:80":                true,
		"[fe80::1%eth0]:80":           true,
		"[fd00:ec2::254]:80":          true, // AWS
		"[fd20:ce::254]:80":           true, // GCP
		"[fd00:c1::a9fe:a9fe]:80":     true, // Oracle
		"100.100.100.200:80":          true, // Alibaba
		"168.63.129.16:80":            true, // Azure WireServer
		"[::ffff:169.254.169.254]:80": true,
		"[64:ff9b::a9fe:a9fe]:80":     true, // NAT64
		"[64:ff9b::169.254.0.1]:80":   true,
		"[64:ff9b::a83f:8110]:80":     true,
		"[fd20:ce::253]:80":           false,
		"[fd00:c1::a9fe:a9ff]:80":     false,
		"168.63.129.17:80":            false,
		"[64:ff9b::a00:5]:80":         false,
		"[64:ff9b:1::a9fe:a9fe]:80":   false,
		"10.0.0.5:443":                false,
		"172.16.0.1:80":               false,
		"192.168.1.20:8080":           false,
		"127.0.0.1:80":                false,
		"[::1]:80":                    false,
		"[fd00:ec2::253]:80":          false,
		"100.100.100.201:80":          false,
		"203.0.113.7:443":             false,
	} {
		err := eventWebhookDialControl("tcp", address, nil)
		var refused *eventWebhookBlockedAddressError
		if got := errors.As(err, &refused); got != blocked {
			t.Errorf("%s: err = %v, want blocked=%v", address, err, blocked)
		}
	}
}
