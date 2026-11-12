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
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// The stable event catalog. These names are the public contract: a webhook
// receiver filters on them, so they must not be renamed without a migration
// plan. The recording lifecycle and its summary are deliberately the first,
// bounded surface; other features can join the catalog later.
const (
	eventRecordingStarted   = "recording.started"
	eventRecordingStopped   = "recording.stopped"
	eventRecordingPublished = "recording.published"
	eventRecordingFailed    = "recording.failed"
	eventSummaryCompleted   = "summary.completed"
	eventSummaryFailed      = "summary.failed"
)

var eventCatalog = []string{
	eventRecordingStarted,
	eventRecordingStopped,
	eventRecordingPublished,
	eventRecordingFailed,
	eventSummaryCompleted,
	eventSummaryFailed,
}

const (
	eventWebhookDefaultTimeout = 10 * time.Second
	eventWebhookMaxTimeout     = time.Minute
	// An undelivered event is retried, at most five minutes apart, until it
	// is a day old: long enough for a receiver's upgrade or a bad night, short
	// enough that a receiver does not wake up to last week's meetings.
	eventWebhookRetryWindow = 24 * time.Hour
	eventWebhookRetryCap    = 5 * time.Minute
	eventWebhookDrainBatch  = 10
	// A row is deleted a week after it was written, delivered or not: it holds
	// a meeting's title and its whole summary.
	eventOutboxRetention     = 7 * 24 * time.Hour
	eventOutboxPruneInterval = 24 * time.Hour
)

// EventWebhookSettings is the receiver lifecycle events are sent to. It has
// its own file and its own endpoint, as the LLM settings do, because it holds
// a secret: nothing that copies or serves the transcription policy can carry
// it along.
type EventWebhookSettings struct {
	// URL is the receiver; empty turns delivery off.
	URL string `json:"url,omitempty"`
	// Secret signs every request. Write-only from the API's point of view: a
	// response reports secret_set instead of the value.
	Secret string `json:"secret,omitempty"`
	// Events is the allow-list; empty means the whole catalog.
	Events []string `json:"events,omitempty"`
	// Timeout bounds one request: a Go duration of at most a minute. Empty
	// means eventWebhookDefaultTimeout.
	Timeout string `json:"timeout,omitempty"`
}

func (s EventWebhookSettings) requestTimeout() time.Duration {
	if timeout, err := time.ParseDuration(s.Timeout); err == nil && timeout > 0 {
		return timeout
	}
	return eventWebhookDefaultTimeout
}

func eventWebhookSettingsPath(cfg Config) string {
	return filepath.Join(filepath.Dir(cfg.DBPath), "event-webhook-settings.json")
}

// normalizeEventWebhookSettings trims and validates the receiver. An empty URL
// turns delivery off and needs no secret. An unknown or blank event name is an
// error rather than dropped: dropping one could leave an empty list, which
// means the whole catalog.
func normalizeEventWebhookSettings(s EventWebhookSettings) (EventWebhookSettings, error) {
	s.URL = strings.TrimSpace(s.URL)
	s.Secret = strings.TrimSpace(s.Secret)
	s.Timeout = strings.TrimSpace(s.Timeout)
	if s.Timeout != "" {
		if timeout, err := time.ParseDuration(s.Timeout); err != nil || timeout <= 0 || timeout > eventWebhookMaxTimeout {
			return s, fmt.Errorf("timeout must be a duration of at most %s (got %q)", eventWebhookMaxTimeout, s.Timeout)
		}
	}
	events := make([]string, 0, len(s.Events))
	for _, name := range s.Events {
		name = strings.TrimSpace(name)
		if !slices.Contains(eventCatalog, name) {
			return s, fmt.Errorf("unknown event %q", name)
		}
		if !slices.Contains(events, name) {
			events = append(events, name)
		}
	}
	sort.Strings(events)
	s.Events = events
	if len(events) == 0 {
		s.Events = nil
	}
	if s.URL == "" {
		return s, nil
	}
	parsed, err := url.ParseRequestURI(s.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return s, fmt.Errorf("url must be an absolute http or https URL")
	}
	if s.Secret == "" {
		return s, fmt.Errorf("secret is required when url is set")
	}
	return s, nil
}

// loadEventWebhookSettings reads the settings file. A missing file is
// delivery switched off, which is the default.
func loadEventWebhookSettings(path string) (EventWebhookSettings, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return EventWebhookSettings{}, nil
	}
	if err != nil {
		return EventWebhookSettings{}, fmt.Errorf("read event webhook settings: %w", err)
	}
	var s EventWebhookSettings
	if err := json.Unmarshal(raw, &s); err != nil {
		return EventWebhookSettings{}, fmt.Errorf("parse event webhook settings: %w", err)
	}
	s, err = normalizeEventWebhookSettings(s)
	if err != nil {
		return EventWebhookSettings{}, fmt.Errorf("invalid event webhook settings: %w", err)
	}
	return s, nil
}

// saveEventWebhookSettings writes the file atomically and owner-only: it
// holds the signing secret.
func saveEventWebhookSettings(path string, s EventWebhookSettings) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal event webhook settings: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir settings dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write event webhook settings temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename event webhook settings: %w", err)
	}
	return nil
}

type eventWebhookConfig struct {
	URL     string
	Secret  string
	Events  map[string]bool
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

func redactEventWebhookURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "(invalid URL)"
	}
	return parsed.Scheme + "://" + parsed.Host
}

// newEventWebhook builds the delivery worker from settings that
// normalizeEventWebhookSettings has accepted. An empty URL means no worker.
func newEventWebhook(store *Store, logger *log.Logger, settings EventWebhookSettings, version string) *eventWebhook {
	if settings.URL == "" {
		return nil
	}
	events := map[string]bool{}
	names := settings.Events
	if len(names) == 0 {
		names = eventCatalog
	}
	for _, name := range names {
		events[name] = true
	}
	logger.Printf("event webhooks enabled receiver=%s events=%d timeout=%s", redactEventWebhookURL(settings.URL), len(events), settings.requestTimeout())
	return &eventWebhook{
		store: store,
		cfg: eventWebhookConfig{
			URL: settings.URL, Secret: settings.Secret, Events: events,
			Version: strings.TrimSpace(version), baseRetryDelay: time.Second, retryWindow: eventWebhookRetryWindow,
		},
		// A redirect is not followed: Go would re-issue the signed POST as a
		// GET without its body, and the page it lands on answering 200 would
		// mark an event delivered that no receiver saw.
		client: &http.Client{
			Timeout:       settings.requestTimeout(),
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
	prune := time.NewTicker(eventOutboxPruneInterval)
	defer prune.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.deliverDue(ctx)
		case <-e.kick:
			e.deliverDue(ctx)
		case <-prune.C:
			e.store.pruneEventOutbox(e.logger, time.Now().UTC())
		}
	}
}

// enqueue writes one event to the outbox and wakes the worker. It never sends
// inline: recording, build, seal and publish must not wait on a receiver.
func (e *eventWebhook) enqueue(ctx context.Context, eventType string, data map[string]any) error {
	if !e.enabled(eventType) {
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
		// A cancelled worker is being replaced or the operator is stopping:
		// it stops here, and a read it can no longer make is not a fault.
		if ctx.Err() != nil {
			return
		}
		event, ok, err := e.store.nextDueEvent(ctx, nowUTCString())
		if err != nil {
			if ctx.Err() == nil {
				e.logger.Printf("event webhook outbox read failed: %v", err)
			}
			return
		}
		if !ok {
			return
		}
		if created, err := time.Parse(sortableUTCLayout, event.CreatedAt); err == nil && time.Since(created) >= e.cfg.retryWindow {
			e.markFailed(event.ID, fmt.Sprintf("gave up after %s and %d attempts", e.cfg.retryWindow, event.Attempts))
			continue
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
SELECT id, type, payload, attempts, created_at
FROM event_outbox
WHERE delivered_at IS NULL AND failed_at IS NULL AND next_attempt_at <= ?
ORDER BY next_attempt_at ASC, created_at ASC
LIMIT 1`, at).Scan(&event.ID, &event.Type, &event.Payload, &event.Attempts, &event.CreatedAt)
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
	_, err := s.db.ExecContext(ctx, `
UPDATE event_outbox SET failed_at = ?, last_error = COALESCE(last_error || '; ', '') || ? WHERE id = ?`, at, detail, id)
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

// pruneEventOutbox deletes the rows written more than a week before now,
// whatever became of them. Best-effort and logged.
func (s *Store) pruneEventOutbox(logger *log.Logger, now time.Time) {
	result, err := s.db.ExecContext(context.Background(),
		`DELETE FROM event_outbox WHERE created_at < ?`, formatUTCString(now.Add(-eventOutboxRetention)))
	if err != nil {
		logger.Printf("event webhook outbox prune failed: %v", err)
		return
	}
	if pruned, _ := result.RowsAffected(); pruned > 0 {
		logger.Printf("event webhooks: %d outbox rows older than %s deleted", pruned, eventOutboxRetention)
	}
}

// eventEnabled reports whether any of eventTypes would be queued right now.
// Callers ask before a lookup or file read that only an event needs: with
// webhooks off, which is the default, an emission costs nothing.
func (rt *Runtime) eventEnabled(eventTypes ...string) bool {
	hook := rt.eventWebhook.Load()
	for _, eventType := range eventTypes {
		if hook.enabled(eventType) {
			return true
		}
	}
	return false
}

// emitEvent assembles the common job facts, adds the event-specific fields and
// queues the webhook. It is deliberately fire-and-forget: every caller is on a
// recording path where a webhook receiver must never become a dependency. It
// takes no context: a caller that is shutting down must not be able to cancel
// the outbox insert. A worker that is replaced while this runs is harmless:
// the row it wrote is delivered by its successor.
func (rt *Runtime) emitEvent(eventType, jobID string, attemptNumber int, extra map[string]any) {
	hook := rt.eventWebhook.Load()
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

func (rt *Runtime) attemptTriggerKind(jobID string, attemptNumber int) string {
	attempt, err := rt.store.GetJobAttempt(context.Background(), jobID, attemptNumber)
	if err != nil {
		return ""
	}
	return attempt.TriggerKind
}

// emitRecordingPublished announces a publication, and only for an attempt that
// is the recording itself: the first build or a full rerun. A summarize or
// speaker-edit attempt republishes a meeting that is already out.
func (rt *Runtime) emitRecordingPublished(jobID string, attemptNumber int) {
	if !rt.eventEnabled(eventRecordingPublished) {
		return
	}
	if kind := rt.attemptTriggerKind(jobID, attemptNumber); kind == triggerKindSummarize || kind == triggerKindRefine {
		return
	}
	rt.emitEvent(eventRecordingPublished, jobID, attemptNumber, nil)
}

// emitAttemptFailure reports that one attempt reached a terminal failure at
// phase. What that means to a receiver depends on what the attempt was for. A
// summarize attempt works on a copy of a meeting that is already published,
// so its failure at any stage is a summary that was not written, never a lost
// recording. A speaker-edit attempt likewise leaves the published meeting as
// it was, and the catalog has no event for it. Every other attempt is the
// recording itself.
//
// A stage that failed while the operator was stopping had its child
// cancelled, which is what the startup pass reports for a stage a crash cut
// short: both carry `interrupted`, so a receiver can tell a restart from a
// failure of the recording.
func (rt *Runtime) emitAttemptFailure(jobID string, attemptNumber int, phase, detail string, interrupted bool) {
	if !rt.eventEnabled(eventSummaryFailed, eventRecordingFailed) {
		return
	}
	data := map[string]any{"phase": phase, "error": detail}
	if interrupted || (rt.ctx != nil && rt.ctx.Err() != nil) {
		data["interrupted"] = true
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

// emitInterruptedAtStartup reports the jobs one startup pass marked
// interrupted: their stage was running when the operator last stopped.
func (rt *Runtime) emitInterruptedAtStartup(interruptedAt string) {
	if !rt.eventEnabled(eventSummaryFailed, eventRecordingFailed) {
		return
	}
	jobs, err := rt.store.InterruptedJobsAt(context.Background(), interruptedAt)
	if err != nil {
		rt.logger.Printf("startup interrupted event read failed: %v", err)
		return
	}
	for _, job := range jobs {
		phase := strings.TrimSpace(job.Stage)
		if phase == "" {
			phase = "job"
		}
		rt.emitAttemptFailure(job.ID, job.CurrentAttemptNumber, phase, "operator restarted before the stage finished", true)
	}
}

func (rt *Runtime) eventBaseData(ctx context.Context, jobID string, attemptNumber int) map[string]any {
	data := map[string]any{
		"job_id":         jobID,
		"attempt_number": attemptNumber,
	}
	if job, err := rt.store.GetJob(ctx, jobID); err == nil {
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

// applyEventWebhook makes the delivery worker match settings. The caller holds
// eventWebhookMu. The old worker goes first and is waited for: two workers
// would each read the same undelivered row and POST it.
func (rt *Runtime) applyEventWebhook(settings EventWebhookSettings) {
	if rt.eventWebhookCancel != nil {
		rt.eventWebhookCancel()
		rt.eventWebhookCancel = nil
	}
	// The old worker stays in place for emitEvent until its successor is:
	// an event written in between is delivered by whichever worker runs next.
	rt.eventWebhook.Load().wait()
	hook := newEventWebhook(rt.store, rt.logger, settings, os.Getenv(envAppVersion))
	if hook == nil || rt.eventWebhookStopped || rt.ctx.Err() != nil {
		// Off, or the operator is stopping: Shutdown may already be waiting on
		// the workers, so none is added. Saved settings apply at the next start.
		rt.eventWebhook.Store(nil)
		return
	}
	ctx, cancel := context.WithCancel(rt.ctx)
	rt.eventWebhookCancel = cancel
	hook.start(ctx, &rt.workerWG)
	rt.eventWebhook.Store(hook)
}

// startEventWebhook loads the stored receiver and starts its worker. Settings
// that cannot be read or are not valid leave delivery off, with the reason
// kept for GET /settings/webhook; nothing else depends on this file.
func (rt *Runtime) startEventWebhook() {
	rt.store.pruneEventOutbox(rt.logger, time.Now().UTC())
	rt.eventWebhookMu.Lock()
	defer rt.eventWebhookMu.Unlock()
	settings, err := loadEventWebhookSettings(rt.eventWebhookSettingsPath)
	if err != nil {
		rt.logger.Printf("event webhooks disabled: %v", err)
		rt.eventWebhookLoadError = err.Error()
		return
	}
	rt.eventWebhookSettings = settings
	rt.applyEventWebhook(settings)
}

// stopEventWebhook is called by Shutdown before it waits on the workers, under
// the lock a worker is started with, so a settings request still in flight can
// never add one to a WaitGroup that is already being waited on.
func (rt *Runtime) stopEventWebhook() {
	rt.eventWebhookMu.Lock()
	defer rt.eventWebhookMu.Unlock()
	rt.eventWebhookStopped = true
}

type eventWebhookSettingsResponse struct {
	URL       string   `json:"url"`
	SecretSet bool     `json:"secret_set"`
	Events    []string `json:"events"`
	// Timeout is the one in effect: the stored value, or the default.
	Timeout string `json:"timeout"`
	// Error is why the stored settings were not accepted at startup, when
	// they were not: delivery is off until a PUT replaces them.
	Error string `json:"error,omitempty"`
}

// eventWebhookSettingsUpdate is the PUT body. A field left out keeps its
// stored value, so a client that sends back what it read (which never
// includes the secret) does not erase it.
type eventWebhookSettingsUpdate struct {
	URL     *string   `json:"url"`
	Secret  *string   `json:"secret"`
	Events  *[]string `json:"events"`
	Timeout *string   `json:"timeout"`
}

// eventWebhookSettingsHandler serves GET and PUT /settings/webhook.
func (rt *Runtime) eventWebhookSettingsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		writeMethodNotAllowed(w, http.MethodGet+", "+http.MethodPut)
		return
	}
	rt.eventWebhookMu.Lock()
	defer rt.eventWebhookMu.Unlock()
	if r.Method == http.MethodPut {
		var in eventWebhookSettingsUpdate
		dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		updated := rt.eventWebhookSettings
		if in.URL != nil {
			updated.URL = *in.URL
		}
		if in.Secret != nil {
			updated.Secret = *in.Secret
		}
		if in.Events != nil {
			updated.Events = *in.Events
		}
		if in.Timeout != nil {
			updated.Timeout = *in.Timeout
		}
		updated, err := normalizeEventWebhookSettings(updated)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := saveEventWebhookSettings(rt.eventWebhookSettingsPath, updated); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		rt.eventWebhookSettings, rt.eventWebhookLoadError = updated, ""
		rt.applyEventWebhook(updated)
	}
	s := rt.eventWebhookSettings
	events := s.Events
	if events == nil {
		events = []string{}
	}
	writeJSON(w, http.StatusOK, eventWebhookSettingsResponse{
		URL: s.URL, SecretSet: s.Secret != "", Events: events, Timeout: s.requestTimeout().String(), Error: rt.eventWebhookLoadError,
	})
}
