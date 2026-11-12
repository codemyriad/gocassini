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
	// Delivered and failed rows are kept a week as evidence of what was sent,
	// then deleted: a row holds a meeting's title and its whole summary.
	eventOutboxRetention           = 7 * 24 * time.Hour
	eventOutboxMaintenanceInterval = 24 * time.Hour

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
	// done is closed when the worker's loop has returned; nil until start.
	done chan struct{}
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
// timeout is a Go duration of at most one minute.
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
func newEventWebhook(store *Store, logger *log.Logger, settings STTSettings, version string) *eventWebhook {
	rawURL := settings.EventWebhookURL
	if rawURL == "" {
		return nil
	}
	// Normalisation has already bounded the timeout; the default covers a
	// caller that built the settings by hand.
	timeout, err := time.ParseDuration(settings.EventWebhookTimeout)
	if err != nil || timeout <= 0 {
		timeout = eventWebhookDefaultTimeout
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
			Version: strings.TrimSpace(version), baseRetryDelay: time.Second, retryWindow: eventWebhookRetryWindow,
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
	}
}

func (e *eventWebhook) enabled(eventType string) bool {
	return e != nil && e.cfg.Events[eventType]
}

func (e *eventWebhook) start(ctx context.Context, wg *sync.WaitGroup) {
	if e == nil {
		return
	}
	e.done = make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(e.done)
		e.loop(ctx)
	}()
}

// wait blocks until a started worker has returned. Its context must already
// be cancelled.
func (e *eventWebhook) wait() {
	if e != nil && e.done != nil {
		<-e.done
	}
}

func (e *eventWebhook) loop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	maintenance := time.NewTicker(eventOutboxMaintenanceInterval)
	defer maintenance.Stop()
	e.store.maintainEventOutbox(e.logger, time.Now().UTC())
	for {
		select {
		case <-ctx.Done():
			return
		case <-maintenance.C:
			e.store.maintainEventOutbox(e.logger, time.Now().UTC())
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
		// A cancelled worker is being replaced or the operator is stopping:
		// it stops here, and a read it can no longer make is not a fault.
		if ctx.Err() != nil {
			return
		}
		event, ok, err := e.store.nextDueEvent(ctx, nowUTCString(), eventReceiverID(e.cfg.URL))
		if err != nil {
			if ctx.Err() == nil {
				e.logger.Printf("event webhook outbox read failed: %v", err)
			}
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

	// Everything that is not a 2xx is retried until the event is a day old,
	// a 4xx included: a 401 while a secret is being rotated, or a 404 while
	// the receiver is redeployed, says the receiver is not ready, not that
	// the event is wrong.
	e.reschedule(event, fmt.Sprintf("receiver returned HTTP %d", resp.StatusCode))
}

func (e *eventWebhook) reschedule(event outboxEvent, detail string) {
	attempts := event.Attempts + 1
	window := e.cfg.retryWindow
	if created, err := time.Parse(sortableUTCLayout, event.CreatedAt); err == nil && time.Since(created) >= window {
		e.markFailed(event.ID, fmt.Sprintf("%s; gave up after %s and %d attempts", detail, window, attempts))
		return
	}
	delay := e.cfg.baseRetryDelay
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
// delivery is off). It runs only when an administrator changes or clears the
// receiver: a payload carries meeting titles and summaries, and goes to the
// receiver it was written for or to nobody.
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

// failExpiredEvents closes every undelivered event older than the retry
// window, whichever receiver it was queued for. It is what ends an event no
// worker reads any more, such as one queued for a receiver that a damaged
// settings file hid across a restart and that never came back.
func (s *Store) failExpiredEvents(ctx context.Context, createdBefore, at string) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
UPDATE event_outbox SET failed_at = ?, last_error = COALESCE(last_error || '; ', '') || ?
WHERE delivered_at IS NULL AND failed_at IS NULL AND created_at < ?`,
		at, fmt.Sprintf("gave up after %s", eventWebhookRetryWindow), createdBefore)
	if err != nil {
		return 0, fmt.Errorf("fail expired events: %w", err)
	}
	count, _ := result.RowsAffected()
	return count, nil
}

// pruneEvents deletes the rows that were delivered or failed before cutoff.
func (s *Store) pruneEvents(ctx context.Context, cutoff string) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
DELETE FROM event_outbox
WHERE (delivered_at IS NOT NULL AND delivered_at < ?) OR (failed_at IS NOT NULL AND failed_at < ?)`, cutoff, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune event outbox: %w", err)
	}
	count, _ := result.RowsAffected()
	return count, nil
}

// maintainEventOutbox ends the events nobody will deliver any more and deletes
// the rows that are past their retention. Best-effort and logged.
func (s *Store) maintainEventOutbox(logger *log.Logger, now time.Time) {
	ctx := context.Background()
	if expired, err := s.failExpiredEvents(ctx, formatUTCString(now.Add(-eventWebhookRetryWindow)), formatUTCString(now)); err != nil {
		logger.Printf("event webhook outbox expiry failed: %v", err)
	} else if expired > 0 {
		logger.Printf("event webhooks: %d undelivered events closed after %s", expired, eventWebhookRetryWindow)
	}
	if pruned, err := s.pruneEvents(ctx, formatUTCString(now.Add(-eventOutboxRetention))); err != nil {
		logger.Printf("event webhook outbox prune failed: %v", err)
	} else if pruned > 0 {
		logger.Printf("event webhooks: %d outbox rows older than %s deleted", pruned, eventOutboxRetention)
	}
}

func (s *Store) rescheduleEvent(ctx context.Context, id string, attempts int, nextAt, detail string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE event_outbox SET attempts = ?, next_attempt_at = ?, last_error = ? WHERE id = ?`, attempts, nextAt, detail, id)
	if err != nil {
		return fmt.Errorf("reschedule event: %w", err)
	}
	return nil
}

// eventEnabled reports whether eventType would be queued right now. Callers
// ask before any lookup or file read that only an event needs: with webhooks
// off, which is the default, an emission must cost nothing.
func (rt *Runtime) eventEnabled(eventTypes ...string) bool {
	rt.eventWebhookMu.RLock()
	defer rt.eventWebhookMu.RUnlock()
	for _, eventType := range eventTypes {
		if rt.eventWebhook.enabled(eventType) {
			return true
		}
	}
	return false
}

// emitEvent assembles the common job facts, adds the event-specific fields and
// queues the webhook. It is deliberately fire-and-forget: every caller is on a
// recording path where a webhook receiver must never become a dependency. It
// takes no context: the outbox insert is what makes an event survive a
// restart, so a caller that is shutting down must not be able to cancel it.
//
// The read lock is held from reading the worker to the insert, so a change of
// receiver (which takes the write lock, then closes the old receiver's rows)
// can never be followed by a row written for the receiver it replaced.
func (rt *Runtime) emitEvent(eventType, jobID string, attemptNumber int, extra map[string]any) {
	rt.eventWebhookMu.RLock()
	defer rt.eventWebhookMu.RUnlock()
	hook := rt.eventWebhook
	if !hook.enabled(eventType) {
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

// attemptAnnouncesRecording reports whether an attempt is the recording
// itself: the first build or a full rerun. A summarize or speaker-edit attempt
// rebuilds, reseals and republishes a meeting that is already out, so its
// build, publication and shares are not news about a recording.
func (rt *Runtime) attemptAnnouncesRecording(jobID string, attemptNumber int) bool {
	kind := rt.attemptTriggerKind(jobID, attemptNumber)
	return kind != triggerKindSummarize && kind != triggerKindRefine
}

func (rt *Runtime) attemptTriggerKind(jobID string, attemptNumber int) string {
	attempt, err := rt.store.GetJobAttempt(context.Background(), jobID, attemptNumber)
	if err != nil {
		return ""
	}
	return attempt.TriggerKind
}

// emitRecordingEvent emits one of the events that announce a recording
// (build.completed, recording.published, shares.completed at publication),
// and only for an attempt that is one.
func (rt *Runtime) emitRecordingEvent(eventType, jobID string, attemptNumber int, extra map[string]any) {
	if !rt.eventEnabled(eventType) || !rt.attemptAnnouncesRecording(jobID, attemptNumber) {
		return
	}
	rt.emitEvent(eventType, jobID, attemptNumber, extra)
}

// emitAttemptFailure reports that one attempt reached a terminal failure at
// phase. What that means to a receiver depends on what the attempt was for. A
// summarize attempt works on a copy of a meeting that is already published,
// so its failure at any stage is a summary that was not written, never a lost
// recording. A speaker-edit attempt likewise leaves the published meeting as
// it was, and the catalog has no event for it. Every other attempt is the
// recording itself.
func (rt *Runtime) emitAttemptFailure(jobID string, attemptNumber int, phase, detail string, extra map[string]any) {
	if !rt.eventEnabled(eventSummaryFailed, eventRecordingFailed) {
		return
	}
	data := map[string]any{"phase": phase, "error": detail}
	for key, value := range extra {
		data[key] = value
	}
	switch rt.attemptTriggerKind(jobID, attemptNumber) {
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

// eventBaseData reads the job once and hands it to the room and title
// lookups, which would otherwise each read it again.
func (rt *Runtime) eventBaseData(ctx context.Context, jobID string, attemptNumber int) map[string]any {
	data := map[string]any{
		"job_id":         jobID,
		"attempt_number": attemptNumber,
	}
	var known *Job
	if job, err := rt.store.GetJob(ctx, jobID); err == nil {
		known = &job
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
	_, roomName := rt.talkRoomForKnownJob(jobID, known)
	if strings.TrimSpace(roomName) != "" {
		data["room_name"] = roomName
	}
	facts := rt.meetingFactsFor(ctx, jobID)
	title := facts.Title
	if title == "" {
		// The metadata index learns a meeting's title when it is published.
		// Until then the title is the one the build stamps into the bundle.
		title = strings.TrimSpace(rt.defaultMeetingTitleForKnownJob(jobID, roomName, known))
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
// at startup and on every settings read, so it does nothing unless the webhook
// values changed, and it never closes a queued event: a settings file that
// could not be read, or whose webhook values were refused, hides the receiver
// only until it is fixed.
func (rt *Runtime) configureEventWebhook(settings STTSettings) {
	rt.applyEventWebhook(settings, false)
}

// applyEventWebhook replaces the delivery worker when the webhook values
// changed. receiverChanged is true only when an administrator saved a
// different receiver URL, or none: the events still queued for the old one are
// then closed, under the same lock that keeps emitEvent from adding to them.
func (rt *Runtime) applyEventWebhook(settings STTSettings, receiverChanged bool) {
	key := eventWebhookSettingsKey(settings)
	rt.eventWebhookMu.Lock()
	defer rt.eventWebhookMu.Unlock()
	if rt.eventWebhookConfigured && rt.eventWebhookKey == key {
		return
	}
	// The old worker goes first and is waited for: two workers for the same
	// receiver would each read the same undelivered row and POST it.
	if rt.eventWebhookCancel != nil {
		rt.eventWebhookCancel()
		rt.eventWebhookCancel = nil
	}
	rt.eventWebhook.wait()
	rt.eventWebhook = nil
	rt.eventWebhookKey, rt.eventWebhookConfigured = key, true
	if settings.eventWebhookInvalid != "" {
		rt.logger.Printf("event webhooks disabled: settings.json was not accepted: %s", settings.eventWebhookInvalid)
	}
	if rt.store == nil {
		// A Runtime built without a store (some settings tests) has no outbox.
		return
	}
	hook := newEventWebhook(rt.store, rt.logger, settings, os.Getenv(envAppVersion))
	if receiverChanged {
		receiver := ""
		if hook != nil {
			receiver = eventReceiverID(hook.cfg.URL)
		}
		if dropped, err := rt.store.failPendingEventsForOtherReceivers(context.Background(), receiver, nowUTCString()); err != nil {
			rt.logger.Printf("event webhook outbox cleanup failed: %v", err)
		} else if dropped > 0 {
			rt.logger.Printf("event webhooks: %d undelivered events closed, the receiver changed", dropped)
		}
	}
	if hook == nil {
		// No worker to do it: end what is past the retry window and delete
		// what is past its retention now.
		rt.store.maintainEventOutbox(rt.logger, time.Now().UTC())
		return
	}
	base := rt.ctx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithCancel(base)
	rt.eventWebhook, rt.eventWebhookCancel = hook, cancel
	hook.start(ctx, &rt.workerWG)
}
