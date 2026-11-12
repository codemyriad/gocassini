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

func newTestEventWebhook(t *testing.T, store *Store, url, secret string, events string) *eventWebhook {
	t.Helper()
	settings := STTSettings{
		EventWebhookURL:     url,
		EventWebhookSecret:  secret,
		EventWebhookEvents:  strings.Split(events, ","),
		EventWebhookTimeout: "10s",
	}
	if err := normalizeEventWebhookSettings(&settings); err != nil {
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
	hook := newTestEventWebhook(t, store, server.URL, secret, "")
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
	store := newTestEventStore(t)
	var logs strings.Builder
	settings := STTSettings{
		EventWebhookURL:     "https://hooks.example/path?token=very-secret",
		EventWebhookSecret:  "signing-secret",
		EventWebhookTimeout: "10s",
		EventWebhookEvents:  []string{"bogus.event"},
	}
	if err := normalizeEventWebhookSettings(&settings); err == nil {
		t.Fatal("unknown event name was accepted")
	}

	settings.EventWebhookEvents = nil
	if newEventWebhook(store, log.New(&logs, "", 0), settings, "test") == nil {
		t.Fatal("newEventWebhook() = nil, want a configured webhook")
	}
	if strings.Contains(logs.String(), "token=very-secret") || strings.Contains(logs.String(), "/path") {
		t.Fatalf("startup log leaked the webhook path/query: %q", logs.String())
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
	rt.eventWebhook = newTestEventWebhook(t, store, "https://hooks.example/events", "secret", "")
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

	hook := newTestEventWebhook(t, store, server.URL, "secret", "")
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
// down for an upgrade gets its events once it is back.
func TestEventWebhookRetriesUntilTheEventIsADayOld(t *testing.T) {
	store := newTestEventStore(t)
	hook := newTestEventWebhook(t, store, "https://hooks.example/events", "secret", "")
	receiver := eventReceiverID(hook.cfg.URL)
	ctx := context.Background()
	fresh := nowUTCString()
	if err := store.insertEvent(ctx, "evt_fresh", eventRecordingStarted, `{}`, fresh, receiver); err != nil {
		t.Fatal(err)
	}
	// Far more attempts than the old ceiling, and still inside the window.
	hook.reschedule(outboxEvent{ID: "evt_fresh", Attempts: 200, CreatedAt: fresh}, "receiver returned HTTP 502")
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
	hook.reschedule(outboxEvent{ID: "evt_fresh", Attempts: 3, CreatedAt: old}, "receiver returned HTTP 502")
	if _, _, failed, lastError := outboxRow(t, store); failed == "" || !strings.Contains(lastError, "gave up") {
		t.Fatalf("failed=%q last_error=%q, want a day-old event closed", failed, lastError)
	}
}

// A queued payload carries titles and summaries. It goes to the receiver it
// was queued for or to nobody: not to one configured later.
func TestEventsQueuedForAnotherReceiverAreNeverSent(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	configure := func(url string) {
		s := STTSettings{EventWebhookURL: url, EventWebhookSecret: "secret"}
		if err := normalizeEventWebhookSettings(&s); err != nil {
			t.Fatal(err)
		}
		// As PUT /settings does when an administrator saves another receiver.
		rt.applyEventWebhook(s, true)
	}
	pending := func() (count int) {
		if err := rt.store.db.QueryRow(`SELECT COUNT(*) FROM event_outbox WHERE delivered_at IS NULL AND failed_at IS NULL`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	queue := func(id, url string) {
		// As if the receiver was down: queued, not yet due.
		later := formatUTCString(time.Now().Add(time.Hour))
		if _, err := rt.store.db.Exec(`INSERT INTO event_outbox (id, type, payload, created_at, attempts, next_attempt_at, receiver) VALUES (?, ?, '{}', ?, 1, ?, ?)`,
			id, eventRecordingStarted, nowUTCString(), later, eventReceiverID(url)); err != nil {
			t.Fatal(err)
		}
	}
	const first, second = "http://127.0.0.1:9/first", "http://127.0.0.1:9/second"

	configure(first)
	queue("evt_one", first)
	configure(first)
	if pending() != 1 {
		t.Fatal("an unchanged receiver lost its queued event")
	}
	configure(second)
	if pending() != 0 {
		t.Fatal("an event queued for the first receiver is still pending for the second")
	}
	if _, _, _, lastError := outboxRow(t, rt.store); lastError != eventFailureReceiverChanged {
		t.Fatalf("last_error = %q, want %q", lastError, eventFailureReceiverChanged)
	}

	queue("evt_two", second)
	configure("")
	if pending() != 0 {
		t.Fatal("turning delivery off left an event waiting for whichever receiver comes next")
	}

	// A row that somehow outlives the change is still not offered to a worker
	// for another receiver.
	queue("evt_three", first)
	if _, ok, err := rt.store.nextDueEvent(context.Background(), formatUTCString(time.Now().Add(2*time.Hour)), eventReceiverID(second)); err != nil || ok {
		t.Fatalf("nextDueEvent() for another receiver ok=%v err=%v, want none", ok, err)
	}
}

func eventTestSettings(t *testing.T, url, secret string) STTSettings {
	t.Helper()
	s := STTSettings{EventWebhookURL: url, EventWebhookSecret: secret}
	if err := normalizeEventWebhookSettings(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func queueEventForTest(t *testing.T, store *Store, id, url string, nextAttempt time.Time) {
	t.Helper()
	if _, err := store.db.Exec(`INSERT INTO event_outbox (id, type, payload, created_at, attempts, next_attempt_at, receiver) VALUES (?, ?, '{}', ?, 1, ?, ?)`,
		id, eventRecordingStarted, nowUTCString(), formatUTCString(nextAttempt), eventReceiverID(url)); err != nil {
		t.Fatal(err)
	}
}

// A restart with a settings file that cannot be read, or whose webhook values
// are refused, starts with no receiver. That is not an administrator turning
// the webhook off: the queued events wait for the file to be fixed.
func TestStartingWithoutAReceiverKeepsQueuedEvents(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	const receiver = "http://127.0.0.1:9/events"
	queueEventForTest(t, rt.store, "evt_waiting", receiver, time.Now().Add(-time.Minute))

	for name, startup := range map[string]STTSettings{
		"unreadable file, detected defaults": detectSettings(),
		"refused webhook values":             {eventWebhookInvalid: "event_webhook_secret is required when event_webhook_url is set"},
	} {
		// What NewRuntime does with the settings it could load, then a
		// settings read by the Operator page.
		rt.eventWebhookConfigured = false
		rt.configureEventWebhook(startup)
		rt.configureEventWebhook(startup)
		if _, _, failed, lastError := outboxRow(t, rt.store); failed != "" {
			t.Fatalf("%s: the queued event was closed (%q)", name, lastError)
		}
	}

	// The file is fixed and read again: the same receiver, the same event.
	rt.configureEventWebhook(eventTestSettings(t, receiver, "secret"))
	event, ok, err := rt.store.nextDueEvent(context.Background(), nowUTCString(), eventReceiverID(receiver))
	if err != nil || !ok || event.ID != "evt_waiting" {
		t.Fatalf("nextDueEvent() = %q ok=%v err=%v, want the event that waited", event.ID, ok, err)
	}
}

// An event nobody reads any more ends when it is a day old, whichever receiver
// it was queued for; delivered and failed rows are deleted a week later.
func TestEventOutboxExpiresAndPrunes(t *testing.T) {
	store := newTestEventStore(t)
	now := time.Now().UTC()
	insert := func(id string, created time.Time, delivered, failed *time.Time) {
		t.Helper()
		var deliveredAt, failedAt any
		if delivered != nil {
			deliveredAt = formatUTCString(*delivered)
		}
		if failed != nil {
			failedAt = formatUTCString(*failed)
		}
		if _, err := store.db.Exec(`INSERT INTO event_outbox (id, type, payload, created_at, attempts, next_attempt_at, receiver, delivered_at, failed_at) VALUES (?, ?, '{}', ?, 0, ?, 'other', ?, ?)`,
			id, eventRecordingStarted, formatUTCString(created), formatUTCString(created), deliveredAt, failedAt); err != nil {
			t.Fatal(err)
		}
	}
	old, recent := now.Add(-8*24*time.Hour), now.Add(-time.Hour)
	insert("evt_pending_fresh", recent, nil, nil)
	insert("evt_pending_stale", now.Add(-25*time.Hour), nil, nil)
	insert("evt_delivered_old", old, &old, nil)
	insert("evt_failed_old", old, nil, &old)
	insert("evt_delivered_recent", recent, &recent, nil)

	var logs strings.Builder
	store.maintainEventOutbox(log.New(&logs, "", 0), now)

	state := map[string]string{}
	rows, err := store.db.Query(`SELECT id, CASE WHEN delivered_at IS NOT NULL THEN 'delivered' WHEN failed_at IS NOT NULL THEN 'failed' ELSE 'pending' END FROM event_outbox`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, s string
		if err := rows.Scan(&id, &s); err != nil {
			t.Fatal(err)
		}
		state[id] = s
	}
	want := map[string]string{"evt_pending_fresh": "pending", "evt_pending_stale": "failed", "evt_delivered_recent": "delivered"}
	if len(state) != len(want) {
		t.Fatalf("rows = %v, want %v", state, want)
	}
	for id, s := range want {
		if state[id] != s {
			t.Errorf("%s = %q, want %q (all: %v)", id, state[id], s, state)
		}
	}
}

// Replacing the worker (a rotated secret, a new allow-list) waits for the old
// one, so the same undelivered row is never read by two workers, and the
// delivery it interrupted is not counted against the event.
func TestReplacingTheWorkerWaitsForTheOldOne(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
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

	rt.applyEventWebhook(eventTestSettings(t, server.URL, "old-secret"), true)
	old := rt.eventWebhook
	if err := old.enqueue(context.Background(), eventRecordingStarted, map[string]any{"job_id": "job-1"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-inFlight:
	case <-time.After(5 * time.Second):
		t.Fatal("the first worker never posted")
	}

	rt.applyEventWebhook(eventTestSettings(t, server.URL, "new-secret"), false)
	select {
	case <-old.done:
	default:
		t.Fatal("the new worker was started while the old one was still running")
	}
	if rt.eventWebhook == old || rt.eventWebhook == nil {
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
	hook := newTestEventWebhook(t, store, "http://127.0.0.1:9/events", "secret", "")
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
