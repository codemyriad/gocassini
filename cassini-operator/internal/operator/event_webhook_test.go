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
	env := map[string]string{
		"CASSINI_EVENT_WEBHOOK_URL":    url,
		"CASSINI_EVENT_WEBHOOK_SECRET": secret,
		"CASSINI_EVENT_WEBHOOK_EVENTS": events,
		"APP_VERSION":                  "test",
	}
	hook := newEventWebhook(store, log.New(io.Discard, "", 0), func(key string) string { return env[key] })
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

func TestEventWebhookStopsOnPermanentFailure(t *testing.T) {
	store := newTestEventStore(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	hook := newTestEventWebhook(t, store, server.URL, "secret", eventRecordingFailed)
	hook.cfg.baseRetryDelay = 10 * time.Millisecond
	startTestEventWebhook(t, hook)
	if err := hook.enqueue(context.Background(), eventRecordingFailed, map[string]any{"job_id": "job-1"}); err != nil {
		t.Fatalf("enqueue() error = %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var failed string
		err := store.db.QueryRow(`SELECT COALESCE(failed_at, '') FROM event_outbox`).Scan(&failed)
		if err == nil && failed != "" {
			if got := calls.Load(); got != 1 {
				t.Fatalf("receiver calls = %d, want 1 for permanent 400", got)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("event was not marked failed after a permanent 400")
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
