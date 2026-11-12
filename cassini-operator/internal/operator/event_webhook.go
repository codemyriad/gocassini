package operator

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// The stable event catalog. These names are the public contract: a webhook
// receiver filters on them, so they must not be renamed without a migration
// plan. Secondary features (speaker edits, annotations, insights, imports,
// model installs) can join the catalog later; the recording lifecycle below is
// deliberately the first, bounded surface.
const (
	eventRecordingStarted   = "recording.started"
	eventRecordingStopped   = "recording.stopped"
	eventBuildCompleted     = "build.completed"
	eventSummaryCompleted   = "summary.completed"
	eventSummaryFailed      = "summary.failed"
	eventRecordingPublished = "recording.published"
	eventRecordingFailed    = "recording.failed"
	eventSharesCompleted    = "shares.completed"
)

var eventCatalog = []string{
	eventRecordingStarted,
	eventRecordingStopped,
	eventBuildCompleted,
	eventSummaryCompleted,
	eventSummaryFailed,
	eventRecordingPublished,
	eventRecordingFailed,
	eventSharesCompleted,
}

const (
	eventWebhookDefaultTimeout = 10 * time.Second
	eventWebhookMaxAttempts    = 12
	eventWebhookRetryCap       = 5 * time.Minute
	eventWebhookDrainBatch     = 10
)

type eventWebhookConfig struct {
	URL     string
	Secret  string
	Events  map[string]bool
	Timeout time.Duration
	Version string
	// baseRetryDelay is a test seam: production uses one second.
	baseRetryDelay time.Duration
}

type eventWebhook struct {
	store  *Store
	cfg    eventWebhookConfig
	client *http.Client
	logger *log.Logger
	kick   chan struct{}
}

// outboundEventPayload is the envelope every receiver sees. Data carries the
// event-specific fields; the envelope itself stays the same for every event.
type outboundEventPayload struct {
	ID             string         `json:"id"`
	Type           string         `json:"type"`
	At             string         `json:"at"`
	CassiniVersion string         `json:"cassini_version,omitempty"`
	Data           map[string]any `json:"data"`
}

type outboxEvent struct {
	ID       string
	Type     string
	Payload  string
	Attempts int
}

// normalizeEventWebhookSettings trims and validates the persisted webhook
// policy. An empty URL disables delivery without requiring a secret. Unknown
// event names are rejected rather than silently delivered or ignored, and the
// timeout is the same bounded Go duration the deploy variable used to accept.
func normalizeEventWebhookSettings(s *STTSettings) error {
	s.EventWebhookURL = strings.TrimSpace(s.EventWebhookURL)
	s.EventWebhookSecret = strings.TrimSpace(s.EventWebhookSecret)
	timeout := strings.TrimSpace(s.EventWebhookTimeout)
	if timeout == "" {
		timeout = eventWebhookDefaultTimeout.String()
	}
	parsed, err := time.ParseDuration(timeout)
	if err != nil || parsed <= 0 || parsed > time.Minute {
		return fmt.Errorf("event_webhook_timeout must be a duration between 1ns and 1m (got %q)", timeout)
	}
	s.EventWebhookTimeout = timeout

	if len(s.EventWebhookEvents) == 0 {
		s.EventWebhookEvents = nil
	} else {
		known := make(map[string]bool, len(eventCatalog))
		for _, name := range eventCatalog {
			known[name] = true
		}
		seen := make(map[string]bool, len(s.EventWebhookEvents))
		events := make([]string, 0, len(s.EventWebhookEvents))
		for _, name := range s.EventWebhookEvents {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if !known[name] {
				return fmt.Errorf("unknown event_webhook event %q", name)
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			events = append(events, name)
		}
		sort.Strings(events)
		s.EventWebhookEvents = events
	}

	if s.EventWebhookURL == "" {
		return nil
	}
	parsedURL, err := url.ParseRequestURI(s.EventWebhookURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		return fmt.Errorf("event_webhook_url must be an absolute http or https URL")
	}
	if s.EventWebhookSecret == "" {
		return fmt.Errorf("event_webhook_secret is required when event_webhook_url is set")
	}
	return nil
}

func redactEventWebhookURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "(invalid URL)"
	}
	return parsed.Scheme + "://" + parsed.Host
}

func newEventWebhook(store *Store, logger *log.Logger, settings STTSettings, lookupEnv func(string) string) (*eventWebhook, error) {
	rawURL := strings.TrimSpace(settings.EventWebhookURL)
	if rawURL == "" {
		return nil, nil
	}
	if err := normalizeEventWebhookSettings(&settings); err != nil {
		return nil, err
	}
	events := map[string]bool{}
	if len(settings.EventWebhookEvents) == 0 {
		for _, name := range eventCatalog {
			events[name] = true
		}
	} else {
		for _, name := range settings.EventWebhookEvents {
			events[name] = true
		}
	}
	timeout, err := time.ParseDuration(settings.EventWebhookTimeout)
	if err != nil {
		return nil, err
	}
	logger.Printf("event webhooks enabled receiver=%s events=%d timeout=%s", redactEventWebhookURL(rawURL), len(events), timeout)
	return &eventWebhook{
		store:  store,
		cfg:    eventWebhookConfig{URL: rawURL, Secret: settings.EventWebhookSecret, Events: events, Timeout: timeout, Version: strings.TrimSpace(lookupEnv("APP_VERSION")), baseRetryDelay: time.Second},
		client: &http.Client{Timeout: timeout},
		logger: logger,
		kick:   make(chan struct{}, 1),
	}, nil
}

func (e *eventWebhook) enabled(eventType string) bool {
	return e != nil && e.cfg.Events[eventType]
}

func (e *eventWebhook) start(ctx context.Context, wg *sync.WaitGroup) {
	if e == nil {
		return
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		e.loop(ctx)
	}()
}

func (e *eventWebhook) loop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.deliverDue(ctx)
		case <-e.kick:
			e.deliverDue(ctx)
		}
	}
}

// enqueue writes one event to the outbox and wakes the worker. It never sends
// inline: recording, build, seal and publish must not wait on a receiver.
func (e *eventWebhook) enqueue(ctx context.Context, eventType string, data map[string]any) error {
	if e == nil || !e.cfg.Events[eventType] {
		return nil
	}
	at := nowUTCString()
	payload := outboundEventPayload{
		ID:             "evt_" + ulid.Make().String(),
		Type:           eventType,
		At:             at,
		CassiniVersion: e.cfg.Version,
		Data:           data,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal event %s: %w", eventType, err)
	}
	if err := e.store.insertEvent(ctx, payload.ID, eventType, string(raw), at); err != nil {
		return err
	}
	select {
	case e.kick <- struct{}{}:
	default:
	}
	return nil
}

func (e *eventWebhook) deliverDue(ctx context.Context) {
	for i := 0; i < eventWebhookDrainBatch; i++ {
		event, ok, err := e.store.nextDueEvent(ctx, nowUTCString())
		if err != nil {
			e.logger.Printf("event webhook outbox read failed: %v", err)
			return
		}
		if !ok {
			return
		}
		e.deliver(ctx, event)
	}
}

func (e *eventWebhook) deliver(ctx context.Context, event outboxEvent) {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(e.cfg.Secret))
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write([]byte(event.Payload))
	signature := hex.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.cfg.URL, bytes.NewReader([]byte(event.Payload)))
	if err != nil {
		e.markFailed(event.ID, fmt.Sprintf("build request: %v", err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Cassini/"+e.cfg.Version)
	req.Header.Set("X-Cassini-Event", event.Type)
	req.Header.Set("X-Cassini-Delivery", event.ID)
	req.Header.Set("X-Cassini-Timestamp", timestamp)
	req.Header.Set("X-Cassini-Signature", "sha256="+signature)

	resp, err := e.client.Do(req)
	if err != nil {
		e.reschedule(event, fmt.Sprintf("request: %v", err))
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		if err := e.store.markEventDelivered(context.Background(), event.ID, nowUTCString()); err != nil {
			e.logger.Printf("event webhook mark delivered failed id=%s: %v", event.ID, err)
		}
		return
	}

	detail := fmt.Sprintf("receiver returned HTTP %d", resp.StatusCode)
	permanent := resp.StatusCode >= 400 && resp.StatusCode <= 499 &&
		resp.StatusCode != http.StatusRequestTimeout &&
		resp.StatusCode != http.StatusConflict &&
		resp.StatusCode != http.StatusTooEarly &&
		resp.StatusCode != http.StatusTooManyRequests
	if permanent {
		e.markFailed(event.ID, detail)
		return
	}
	e.reschedule(event, detail)
}

func (e *eventWebhook) reschedule(event outboxEvent, detail string) {
	attempts := event.Attempts + 1
	if attempts >= eventWebhookMaxAttempts {
		e.markFailed(event.ID, fmt.Sprintf("%s after %d attempts", detail, attempts))
		return
	}
	delay := e.cfg.baseRetryDelay
	if delay <= 0 {
		delay = time.Second
	}
	for i := 1; i < attempts; i++ {
		delay *= 2
		if delay >= eventWebhookRetryCap {
			delay = eventWebhookRetryCap
			break
		}
	}
	next := formatUTCString(time.Now().UTC().Add(delay))
	if err := e.store.rescheduleEvent(context.Background(), event.ID, attempts, next, detail); err != nil {
		e.logger.Printf("event webhook reschedule failed id=%s: %v", event.ID, err)
	}
}

func (e *eventWebhook) markFailed(id, detail string) {
	if err := e.store.markEventFailed(context.Background(), id, nowUTCString(), detail); err != nil {
		e.logger.Printf("event webhook mark failed id=%s: %v", id, err)
	}
}

func (s *Store) insertEvent(ctx context.Context, id, eventType, payload, at string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO event_outbox (id, type, payload, created_at, attempts, next_attempt_at)
VALUES (?, ?, ?, ?, 0, ?)`, id, eventType, payload, at, at)
	if err != nil {
		return fmt.Errorf("insert event outbox: %w", err)
	}
	return nil
}

func (s *Store) nextDueEvent(ctx context.Context, at string) (outboxEvent, bool, error) {
	var event outboxEvent
	err := s.db.QueryRowContext(ctx, `
SELECT id, type, payload, attempts
FROM event_outbox
WHERE delivered_at IS NULL AND failed_at IS NULL AND next_attempt_at <= ?
ORDER BY next_attempt_at ASC, created_at ASC
LIMIT 1`, at).Scan(&event.ID, &event.Type, &event.Payload, &event.Attempts)
	if err == sql.ErrNoRows {
		return outboxEvent{}, false, nil
	}
	if err != nil {
		return outboxEvent{}, false, fmt.Errorf("read due event: %w", err)
	}
	return event, true, nil
}

func (s *Store) markEventDelivered(ctx context.Context, id, at string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE event_outbox SET delivered_at = ?, last_error = NULL WHERE id = ?`, at, id)
	if err != nil {
		return fmt.Errorf("mark event delivered: %w", err)
	}
	return nil
}

func (s *Store) markEventFailed(ctx context.Context, id, at, detail string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE event_outbox SET failed_at = ?, last_error = ? WHERE id = ?`, at, detail, id)
	if err != nil {
		return fmt.Errorf("mark event failed: %w", err)
	}
	return nil
}

func (s *Store) rescheduleEvent(ctx context.Context, id string, attempts int, nextAt, detail string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE event_outbox SET attempts = ?, next_attempt_at = ?, last_error = ? WHERE id = ?`, attempts, nextAt, detail, id)
	if err != nil {
		return fmt.Errorf("reschedule event: %w", err)
	}
	return nil
}

// emitEvent assembles the common job facts, adds the event-specific fields and
// queues the webhook. It is deliberately fire-and-forget: every caller is on a
// recording path where a webhook receiver must never become a dependency.
func (rt *Runtime) emitEvent(ctx context.Context, eventType, jobID string, attemptNumber int, extra map[string]any) {
	rt.eventWebhookMu.Lock()
	hook := rt.eventWebhook
	rt.eventWebhookMu.Unlock()
	if hook == nil || !hook.enabled(eventType) {
		return
	}
	// The payload is built from the job's persisted state; a canceled caller
	// context (shutdown) must not prevent the outbox insert, because that write
	// is what makes the event survive the restart (#472 review).
	data := rt.eventBaseData(context.Background(), jobID, attemptNumber)
	for key, value := range extra {
		data[key] = value
	}
	if err := hook.enqueue(context.Background(), eventType, data); err != nil {
		rt.logger.Printf("event %s enqueue failed job=%s: %v", eventType, jobID, err)
	}
}

func (rt *Runtime) eventBaseData(ctx context.Context, jobID string, attemptNumber int) map[string]any {
	data := map[string]any{
		"job_id":         jobID,
		"attempt_number": attemptNumber,
	}
	if job, err := rt.store.GetJob(ctx, jobID); err == nil {
		data["stage"] = job.Stage
		data["state"] = job.State
		if job.Error != nil && strings.TrimSpace(*job.Error) != "" {
			data["error"] = *job.Error
		}
		if job.RecordStartedAt != nil && *job.RecordStartedAt != "" {
			data["record_started_at"] = *job.RecordStartedAt
		}
		if job.RecordFinishedAt != nil && *job.RecordFinishedAt != "" {
			data["record_finished_at"] = *job.RecordFinishedAt
		}
	}
	if _, roomName := rt.talkRoomForJob(jobID); strings.TrimSpace(roomName) != "" {
		data["room_name"] = roomName
	}
	facts := rt.meetingFactsFor(ctx, jobID)
	if facts.Title != "" {
		data["title"] = facts.Title
	}
	if !facts.StartedAt.IsZero() {
		data["started_at"] = facts.StartedAt.UTC().Format(time.RFC3339)
	}
	if facts.Duration > 0 {
		data["duration_seconds"] = int(facts.Duration.Seconds())
	}
	return data
}

// configureEventWebhook replaces the delivery worker after a settings change.
// The outbox is shared, so events already queued for the old receiver remain
// queued and are delivered by the new configuration once it starts.
func (rt *Runtime) configureEventWebhook(settings STTSettings) {
	hook, err := newEventWebhook(rt.store, rt.logger, settings, os.Getenv)
	if err != nil {
		rt.logger.Printf("event webhooks disabled: %v", err)
		hook = nil
	}
	rt.eventWebhookMu.Lock()
	cancel := rt.eventWebhookCancel
	rt.eventWebhook = hook
	rt.eventWebhookCancel = nil
	if hook != nil {
		base := rt.ctx
		if base == nil {
			base = context.Background()
		}
		ctx, nextCancel := context.WithCancel(base)
		rt.eventWebhookCancel = nextCancel
		hook.start(ctx, &rt.workerWG)
	}
	rt.eventWebhookMu.Unlock()
	if cancel != nil {
		cancel()
	}
}
