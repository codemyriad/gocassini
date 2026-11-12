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
	// An undelivered event is retried, at most five minutes apart, until it
	// is a day old: long enough for a receiver's upgrade or a bad night, short
	// enough that a receiver does not wake up to last week's meetings.
	eventWebhookRetryWindow = 24 * time.Hour
	eventWebhookRetryCap    = 5 * time.Minute
	eventWebhookDrainBatch  = 10

	eventFailureReceiverChanged = "receiver changed"
)

type eventWebhookConfig struct {
	URL     string
	Secret  string
	Events  map[string]bool
	Timeout time.Duration
	Version string
	// baseRetryDelay and retryWindow are test seams: production uses one
	// second and eventWebhookRetryWindow.
	baseRetryDelay time.Duration
	retryWindow    time.Duration
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
	ID        string
	Type      string
	Payload   string
	Attempts  int
	CreatedAt string
}

// eventReceiverID names the receiver an event was queued for without storing
// its URL, which may carry a token, a second time. An event is only ever sent
// to the receiver it was queued for.
func eventReceiverID(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(rawURL))
	return hex.EncodeToString(sum[:8])
}

// eventWebhookSettingsKey changes exactly when the delivery worker has to be
// replaced: any of the four persisted values, or why they were refused.
func eventWebhookSettingsKey(s STTSettings) string {
	return strings.Join([]string{
		s.EventWebhookURL, s.EventWebhookSecret, strings.Join(s.EventWebhookEvents, ","), s.EventWebhookTimeout, s.eventWebhookInvalid,
	}, "\x00")
}

// withoutEventWebhook is the settings without the receiver and its secret,
// for every copy that leaves the settings file: a job's frozen policy must not
// carry them into request_json.
func (s STTSettings) withoutEventWebhook() STTSettings {
	s.EventWebhookURL, s.EventWebhookSecret, s.EventWebhookEvents, s.EventWebhookTimeout = "", "", nil, ""
	return s
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

// newEventWebhook builds the delivery worker from settings that
// normalizeEventWebhookSettings has accepted. An empty URL means no worker.
func newEventWebhook(store *Store, logger *log.Logger, settings STTSettings, lookupEnv func(string) string) (*eventWebhook, error) {
	rawURL := settings.EventWebhookURL
	if rawURL == "" {
		return nil, nil
	}
	timeout := eventWebhookDefaultTimeout
	if settings.EventWebhookTimeout != "" {
		parsed, err := time.ParseDuration(settings.EventWebhookTimeout)
		if err != nil {
			return nil, fmt.Errorf("event_webhook_timeout: %w", err)
		}
		timeout = parsed
	}
	events := map[string]bool{}
	names := settings.EventWebhookEvents
	if len(names) == 0 {
		names = eventCatalog
	}
	for _, name := range names {
		events[name] = true
	}
	logger.Printf("event webhooks enabled receiver=%s events=%d timeout=%s", redactEventWebhookURL(rawURL), len(events), timeout)
	return &eventWebhook{
		store: store,
		cfg: eventWebhookConfig{
			URL: rawURL, Secret: settings.EventWebhookSecret, Events: events, Timeout: timeout,
			Version: strings.TrimSpace(lookupEnv("APP_VERSION")), baseRetryDelay: time.Second, retryWindow: eventWebhookRetryWindow,
		},
		// A redirect is not followed: Go would re-issue the signed POST as a
		// GET without its body, and the page it lands on answering 200 would
		// mark an event delivered that no receiver saw.
		client: &http.Client{
			Timeout:       timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
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
	if err := e.store.insertEvent(ctx, payload.ID, eventType, string(raw), at, eventReceiverID(e.cfg.URL)); err != nil {
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
		event, ok, err := e.store.nextDueEvent(ctx, nowUTCString(), eventReceiverID(e.cfg.URL))
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
		if ctx.Err() != nil {
			// This worker is being replaced or the operator is stopping: the
			// receiver did not fail, so the event keeps its place in the queue.
			return
		}
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

	// Everything that is not a 2xx is a failure, a redirect included. Only a
	// 4xx that says "this request is wrong" is final.
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
	window := e.cfg.retryWindow
	if window <= 0 {
		window = eventWebhookRetryWindow
	}
	if created, err := time.Parse(sortableUTCLayout, event.CreatedAt); err == nil && time.Since(created) >= window {
		e.markFailed(event.ID, fmt.Sprintf("%s; gave up after %s and %d attempts", detail, window, attempts))
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

func (s *Store) insertEvent(ctx context.Context, id, eventType, payload, at, receiver string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO event_outbox (id, type, payload, created_at, attempts, next_attempt_at, receiver)
VALUES (?, ?, ?, ?, 0, ?, ?)`, id, eventType, payload, at, at, receiver)
	if err != nil {
		return fmt.Errorf("insert event outbox: %w", err)
	}
	return nil
}

func (s *Store) nextDueEvent(ctx context.Context, at, receiver string) (outboxEvent, bool, error) {
	var event outboxEvent
	err := s.db.QueryRowContext(ctx, `
SELECT id, type, payload, attempts, created_at
FROM event_outbox
WHERE delivered_at IS NULL AND failed_at IS NULL AND next_attempt_at <= ? AND receiver = ?
ORDER BY next_attempt_at ASC, created_at ASC
LIMIT 1`, at, receiver).Scan(&event.ID, &event.Type, &event.Payload, &event.Attempts, &event.CreatedAt)
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

// failPendingEventsForOtherReceivers closes every undelivered event that was
// queued for a receiver other than the one now configured (none, when
// delivery is off). A payload carries meeting titles and summaries; it goes to
// the receiver it was written for or to nobody.
func (s *Store) failPendingEventsForOtherReceivers(ctx context.Context, receiver, at string) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
UPDATE event_outbox SET failed_at = ?, last_error = ?
WHERE delivered_at IS NULL AND failed_at IS NULL AND receiver <> ?`, at, eventFailureReceiverChanged, receiver)
	if err != nil {
		return 0, fmt.Errorf("fail events for other receivers: %w", err)
	}
	count, _ := result.RowsAffected()
	return count, nil
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
// recording path where a webhook receiver must never become a dependency. It
// takes no context: the outbox insert is what makes an event survive a
// restart, so a caller that is shutting down must not be able to cancel it.
func (rt *Runtime) emitEvent(eventType, jobID string, attemptNumber int, extra map[string]any) {
	rt.eventWebhookMu.Lock()
	hook := rt.eventWebhook
	rt.eventWebhookMu.Unlock()
	if hook == nil || !hook.enabled(eventType) {
		return
	}
	data := rt.eventBaseData(context.Background(), jobID, attemptNumber)
	for key, value := range extra {
		data[key] = value
	}
	if err := hook.enqueue(context.Background(), eventType, data); err != nil {
		rt.logger.Printf("event %s enqueue failed job=%s: %v", eventType, jobID, err)
	}
}

// emitAttemptFailure reports that one attempt reached a terminal failure at
// phase. What that means to a receiver depends on what the attempt was for. A
// summarize attempt works on a copy of a meeting that is already published,
// so its failure at any stage is a summary that was not written, never a lost
// recording. A speaker-edit attempt likewise leaves the published meeting as
// it was, and the catalog has no event for it. Every other attempt is the
// recording itself.
func (rt *Runtime) emitAttemptFailure(jobID string, attemptNumber int, phase, detail string, extra map[string]any) {
	data := map[string]any{"phase": phase, "error": detail}
	for key, value := range extra {
		data[key] = value
	}
	kind := ""
	if attempt, err := rt.store.GetJobAttempt(context.Background(), jobID, attemptNumber); err == nil {
		kind = attempt.TriggerKind
	}
	switch kind {
	case triggerKindSummarize:
		if phase == "build" {
			// A summarize attempt's build stage is the summary step.
			data["phase"] = "summary"
		}
		rt.emitEvent(eventSummaryFailed, jobID, attemptNumber, data)
	case triggerKindRefine:
	default:
		rt.emitEvent(eventRecordingFailed, jobID, attemptNumber, data)
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
	_, roomName := rt.talkRoomForJob(jobID)
	if strings.TrimSpace(roomName) != "" {
		data["room_name"] = roomName
	}
	facts := rt.meetingFactsFor(ctx, jobID)
	title := facts.Title
	if title == "" {
		// The metadata index learns a meeting's title when it is published.
		// Until then the title is the one the build stamps into the bundle.
		title = strings.TrimSpace(rt.defaultMeetingTitle(jobID, roomName))
	}
	if title != "" {
		data["title"] = title
	}
	if !facts.StartedAt.IsZero() {
		// The store's own layout, as every other time in the payload.
		data["started_at"] = formatUTCString(facts.StartedAt)
	}
	if facts.Duration > 0 {
		data["duration_seconds"] = int(facts.Duration.Seconds())
	}
	return data
}

// configureEventWebhook makes the delivery worker match settings. It is called
// on every settings read and write, so it does nothing unless the webhook
// values changed: replacing the worker cancels a delivery in flight.
func (rt *Runtime) configureEventWebhook(settings STTSettings) {
	key := eventWebhookSettingsKey(settings)
	rt.eventWebhookMu.Lock()
	if rt.eventWebhookConfigured && rt.eventWebhookKey == key {
		rt.eventWebhookMu.Unlock()
		return
	}
	hook, err := newEventWebhook(rt.store, rt.logger, settings, os.Getenv)
	switch {
	case err != nil:
		rt.logger.Printf("event webhooks disabled: %v", err)
		hook = nil
	case settings.eventWebhookInvalid != "":
		rt.logger.Printf("event webhooks disabled: settings.json was not accepted: %s", settings.eventWebhookInvalid)
	}
	cancel := rt.eventWebhookCancel
	rt.eventWebhook = hook
	rt.eventWebhookCancel = nil
	rt.eventWebhookKey, rt.eventWebhookConfigured = key, true
	receiver := ""
	if hook != nil {
		receiver = eventReceiverID(hook.cfg.URL)
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
	if rt.store == nil {
		return
	}
	if dropped, err := rt.store.failPendingEventsForOtherReceivers(context.Background(), receiver, nowUTCString()); err != nil {
		rt.logger.Printf("event webhook outbox cleanup failed: %v", err)
	} else if dropped > 0 {
		rt.logger.Printf("event webhooks: %d undelivered events closed, the receiver changed", dropped)
	}
}
