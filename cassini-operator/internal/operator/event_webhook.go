package operator

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

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

// Retry, give-up and retention rules are explained in docs/events.md.
const (
	eventWebhookDefaultTimeoutSeconds = 10
	eventWebhookMaxTimeoutSeconds     = 60
	eventWebhookRetryWindow           = 24 * time.Hour
	eventWebhookRetryCap              = 5 * time.Minute
	eventWebhookBaseRetryDelay        = time.Second
	eventWebhookDrainBatch            = 10
	eventOutboxRetention              = 7 * 24 * time.Hour
	eventOutboxPruneInterval          = time.Hour
	// The prune gives up on waiting rows this long after the worker would,
	// so a POST the worker started just before the window can finish.
	eventOutboxGiveUpMargin = 5 * time.Minute
	// For a secret set through the API; a shorter one 0.4.0 stored still works.
	eventWebhookMinSecretLength = 32
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
	// Events is the allow-list, required with a URL.
	Events []string `json:"events,omitempty"`
	// TimeoutSeconds is 1 to 60; zero means the default.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

// eventReceiverKey tags every queued row with the URL it was queued for; a row
// is only ever sent there. Derived, never stored. No URL: "".
func eventReceiverKey(receiverURL string) string {
	if receiverURL == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(receiverURL))
	return "rcv_" + hex.EncodeToString(sum[:8])
}

func (s EventWebhookSettings) timeoutSeconds() int {
	if s.TimeoutSeconds > 0 {
		return s.TimeoutSeconds
	}
	return eventWebhookDefaultTimeoutSeconds
}

func (s EventWebhookSettings) requestTimeout() time.Duration {
	return time.Duration(s.timeoutSeconds()) * time.Second
}

func eventWebhookSettingsPath(cfg Config) string {
	return filepath.Join(filepath.Dir(cfg.DBPath), "event-webhook-settings.json")
}

// eventWebhookSettingsError is a refusal; Code is the PUT's error_code, which
// the Webhooks panel shows under the field it is about.
type eventWebhookSettingsError struct {
	Code    string
	Message string
}

func (e *eventWebhookSettingsError) Error() string { return e.Message }

func eventWebhookSettingsRefusal(code, format string, args ...any) error {
	return &eventWebhookSettingsError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// normalizeEventWebhookSettings trims and validates the receiver. An empty URL
// turns delivery off and drops the secret and events with it.
func normalizeEventWebhookSettings(s EventWebhookSettings) (EventWebhookSettings, error) {
	s.URL = strings.TrimSpace(s.URL)
	s.Secret = strings.TrimSpace(s.Secret)
	if s.TimeoutSeconds < 0 || s.TimeoutSeconds > eventWebhookMaxTimeoutSeconds {
		return s, eventWebhookTimeoutRefusal(float64(s.TimeoutSeconds))
	}
	if s.URL == "" {
		return EventWebhookSettings{TimeoutSeconds: s.TimeoutSeconds}, nil
	}
	asked := make([]string, 0, len(s.Events))
	for _, name := range s.Events {
		name = strings.TrimSpace(name)
		if !slices.Contains(eventCatalog, name) {
			return s, eventWebhookSettingsRefusal("webhook_event_unknown", "unknown event %q", name)
		}
		asked = append(asked, name)
	}
	// In catalog order, once each.
	s.Events = nil
	for _, name := range eventCatalog {
		if slices.Contains(asked, name) {
			s.Events = append(s.Events, name)
		}
	}
	parsed, err := url.ParseRequestURI(s.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return s, eventWebhookSettingsRefusal("webhook_url_invalid", "url must be an absolute http or https URL")
	}
	if s.Secret == "" {
		return s, eventWebhookSettingsRefusal("webhook_secret_required", "secret is required when url is set")
	}
	if len(s.Events) == 0 {
		return s, eventWebhookSettingsRefusal("webhook_events_required", "events must name at least one event when url is set")
	}
	return s, nil
}

func eventWebhookTimeoutRefusal(got float64) error {
	return eventWebhookSettingsRefusal("webhook_timeout_invalid", "timeout_seconds must be a whole number from 1 to %d (got %v)", eventWebhookMaxTimeoutSeconds, got)
}

// eventWebhookEvents040 is what 0.4.0 sent a receiver with no event list.
// Frozen: it must not grow with the catalog.
var eventWebhookEvents040 = []string{
	"recording.started",
	"recording.stopped",
	"recording.published",
	"recording.failed",
	"summary.completed",
	"summary.failed",
}

// loadEventWebhookSettings reads the settings file; a missing file is delivery
// off. converted reports a 0.4.0 file brought up to date (its duration timeout,
// its missing event list), which the caller saves back.
func loadEventWebhookSettings(path string) (s EventWebhookSettings, converted bool, err error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return EventWebhookSettings{}, false, nil
	}
	if err != nil {
		return EventWebhookSettings{}, false, fmt.Errorf("read event webhook settings: %w", err)
	}
	var file struct {
		EventWebhookSettings
		Timeout *string `json:"timeout"` // 0.4.0: a Go duration
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return EventWebhookSettings{}, false, fmt.Errorf("parse event webhook settings: %w", err)
	}
	s = file.EventWebhookSettings
	if file.Timeout != nil {
		if raw := strings.TrimSpace(*file.Timeout); raw != "" && s.TimeoutSeconds == 0 {
			timeout, err := time.ParseDuration(raw)
			if err != nil || timeout <= 0 {
				return EventWebhookSettings{}, false, fmt.Errorf("invalid event webhook settings: timeout %q is not a positive duration", raw)
			}
			s.TimeoutSeconds = min(int((timeout+time.Second-1)/time.Second), eventWebhookMaxTimeoutSeconds)
		}
		converted = true
	}
	if strings.TrimSpace(s.URL) != "" && len(s.Events) == 0 {
		s.Events = slices.Clone(eventWebhookEvents040)
		converted = true
	}
	s, err = normalizeEventWebhookSettings(s)
	if err != nil {
		return EventWebhookSettings{}, false, fmt.Errorf("invalid event webhook settings: %w", err)
	}
	return s, converted, nil
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
	if err := writeFileAtomic(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write event webhook settings: %w", err)
	}
	return nil
}

type eventWebhookConfig struct {
	URL     string
	Secret  string
	Events  []string
	Version string
	// Test seams.
	baseRetryDelay time.Duration
	retryWindow    time.Duration
}

func (c eventWebhookConfig) receiver() string { return eventReceiverKey(c.URL) }

type eventWebhook struct {
	store  *Store
	cfg    eventWebhookConfig
	client *http.Client
	logger *log.Logger
	kick   chan struct{}
	// done is closed when the worker's loop has returned; nil until start.
	done chan struct{}
	// The receiver as a whole backs off: nothing is sent before pausedUntil.
	// Touched only by the worker's goroutine.
	failStreak  int
	pausedUntil time.Time
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

func newOutboundEvent(eventType, version string, data map[string]any) outboundEventPayload {
	return outboundEventPayload{
		ID:             "evt_" + ulid.Make().String(),
		Type:           eventType,
		At:             nowUTCString(),
		CassiniVersion: version,
		Data:           data,
	}
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

// newEventWebhook builds the delivery worker from normalized settings. An
// empty URL means no worker.
func newEventWebhook(store *Store, logger *log.Logger, settings EventWebhookSettings, version string) *eventWebhook {
	if settings.URL == "" {
		return nil
	}
	logger.Printf("event webhooks enabled receiver=%s events=%d timeout=%s", redactEventWebhookURL(settings.URL), len(settings.Events), settings.requestTimeout())
	return &eventWebhook{
		store: store,
		cfg: eventWebhookConfig{
			URL: settings.URL, Secret: settings.Secret, Events: slices.Clone(settings.Events),
			Version: strings.TrimSpace(version), baseRetryDelay: eventWebhookBaseRetryDelay, retryWindow: eventWebhookRetryWindow,
		},
		client: newEventWebhookClient(settings.requestTimeout()),
		logger: logger,
		kick:   make(chan struct{}, 1),
	}
}

// newEventWebhookClient sends every request to a receiver, test events
// included. Redirects are not followed (Go would re-send the POST as a bodiless
// GET, and its 200 would count as a delivery), and blocked addresses are never
// dialled.
func newEventWebhookClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: eventWebhookDialControl}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = dialer.DialContext
	return &http.Client{
		Timeout:       timeout,
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// eventWebhookBlockedPrefixes are link-local ranges and the cloud metadata
// addresses (AWS, GCP, Azure, Oracle, Alibaba). Private ranges and loopback
// stay allowed: a receiver on the same network is the recommended setup.
var eventWebhookBlockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("fd00:ec2::254/128"),
	netip.MustParsePrefix("fd20:ce::254/128"),
	netip.MustParsePrefix("fd00:c1::a9fe:a9fe/128"),
	netip.MustParsePrefix("100.100.100.200/32"),
	netip.MustParsePrefix("168.63.129.16/32"),
}

// eventWebhookNAT64 is the well-known NAT64 prefix: the IPv4 address in its
// last 32 bits is the one reached, so that is the one checked.
var eventWebhookNAT64 = netip.MustParsePrefix("64:ff9b::/96")

type eventWebhookBlockedAddressError struct{ addr netip.Addr }

func (e *eventWebhookBlockedAddressError) Error() string {
	return fmt.Sprintf("address not allowed: link-local/metadata (%s)", e.addr)
}

// eventWebhookDialControl runs after name resolution, on the address actually
// dialled, so a name resolving to a blocked address is refused too.
func eventWebhookDialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	addr = addr.Unmap().WithZone("")
	if eventWebhookNAT64.Contains(addr) {
		b := addr.As16()
		addr = netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
	}
	for _, prefix := range eventWebhookBlockedPrefixes {
		if prefix.Contains(addr) {
			return &eventWebhookBlockedAddressError{addr: addr}
		}
	}
	return nil
}

// newSignedEventRequest builds the POST for one event, outbox or test: the
// same headers and signature either way.
func newSignedEventRequest(ctx context.Context, receiverURL, secret, version, eventType, deliveryID string, payload []byte) (*http.Request, error) {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write(payload)
	signature := hex.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, receiverURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Cassini/"+version)
	req.Header.Set("X-Cassini-Event", eventType)
	req.Header.Set("X-Cassini-Delivery", deliveryID)
	req.Header.Set("X-Cassini-Timestamp", timestamp)
	req.Header.Set("X-Cassini-Signature", "sha256="+signature)
	return req, nil
}

// eventWebhookRequestError is why a receiver gave no answer, without the URL
// (its query can carry a token, and the reason is shown in the panel).
func eventWebhookRequestError(err error, timeout time.Duration) string {
	var blocked *eventWebhookBlockedAddressError
	if errors.As(err, &blocked) {
		return blocked.Error()
	}
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		return err.Error()
	}
	if urlErr.Timeout() {
		return fmt.Sprintf("no answer within %ds", int(timeout/time.Second))
	}
	return urlErr.Err.Error()
}

func (e *eventWebhook) enabled(eventType string) bool {
	return e != nil && slices.Contains(e.cfg.Events, eventType)
}

// start runs the worker, with a first pass at once.
func (e *eventWebhook) start(ctx context.Context, wg *sync.WaitGroup) {
	e.done = make(chan struct{})
	select {
	case e.kick <- struct{}{}:
	default:
	}
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
	defer e.client.CloseIdleConnections()
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
	if !e.enabled(eventType) {
		return nil
	}
	payload := newOutboundEvent(eventType, e.cfg.Version, data)
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal event %s: %w", eventType, err)
	}
	if err := e.store.insertEvent(ctx, payload.ID, e.cfg.receiver(), eventType, string(raw), payload.At); err != nil {
		return err
	}
	select {
	case e.kick <- struct{}{}:
	default:
	}
	return nil
}

func (e *eventWebhook) deliverDue(ctx context.Context) {
	if time.Now().Before(e.pausedUntil) {
		return
	}
	for i := 0; i < eventWebhookDrainBatch; i++ {
		// A cancelled worker is being replaced or the operator is stopping:
		// it stops here, and a read it can no longer make is not a fault.
		if ctx.Err() != nil {
			return
		}
		event, ok, err := e.store.nextDueEvent(ctx, nowUTCString(), e.cfg.receiver(), e.cfg.Events)
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
			if err := e.store.markEventFailed(context.Background(), event.ID, nowUTCString(), fmt.Sprintf("gave up after %s and %d attempts", e.cfg.retryWindow, event.Attempts)); err != nil {
				e.logger.Printf("event webhook mark failed id=%s: %v", event.ID, err)
				e.pause(0)
				return
			}
			continue
		}
		if !e.deliver(ctx, event) {
			return
		}
	}
}

// deliver reports whether the pass may go on to the next event.
func (e *eventWebhook) deliver(ctx context.Context, event outboxEvent) bool {
	req, err := newSignedEventRequest(ctx, e.cfg.URL, e.cfg.Secret, e.cfg.Version, event.Type, event.ID, []byte(event.Payload))
	if err != nil {
		return e.fail(event, fmt.Sprintf("build request: %v", err), false, 0)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			// Replaced or stopping: not the receiver's failure.
			return true
		}
		return e.fail(event, "request: "+eventWebhookRequestError(err, e.client.Timeout), true, 0)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	status := resp.StatusCode
	if status >= 200 && status <= 299 {
		if err := e.store.markEventDelivered(context.Background(), event.ID, nowUTCString()); err != nil {
			e.logger.Printf("event webhook mark delivered failed id=%s: %v", event.ID, err)
			return e.fail(event, fmt.Sprintf("delivered, but not recorded: %v", err), true, 0)
		}
		e.failStreak = 0
		return true
	}
	// Every other status is retried. A 5xx, 429 or redirect says the receiver
	// is down; any other 4xx answers this one request.
	var retryAfter time.Duration
	if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
		retryAfter = eventWebhookRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	}
	down := status >= 500 || status == http.StatusTooManyRequests || (status >= 300 && status <= 399)
	return e.fail(event, fmt.Sprintf("receiver returned HTTP %d", status), down, retryAfter)
}

// fail records a failed attempt, retried after the usual backoff or the
// receiver's Retry-After, whichever is longer. It reports whether the pass may
// go on: not when the receiver is down, nor when the attempt could not be
// recorded (the same event would be due again at once). Then the whole worker
// pauses, so neither turns into a POST every pass.
func (e *eventWebhook) fail(event outboxEvent, detail string, receiverDown bool, retryAfter time.Duration) bool {
	attempts := event.Attempts + 1
	now := time.Now().UTC()
	next := formatUTCString(now.Add(max(eventWebhookRetryDelay(e.cfg.baseRetryDelay, attempts), retryAfter)))
	err := e.store.rescheduleEvent(context.Background(), event.ID, attempts, next, formatUTCString(now), detail)
	if err != nil {
		e.logger.Printf("event webhook reschedule failed id=%s: %v", event.ID, err)
	} else if !receiverDown {
		return true
	}
	e.pause(retryAfter)
	return false
}

func (e *eventWebhook) pause(retryAfter time.Duration) {
	e.failStreak++
	e.pausedUntil = time.Now().Add(max(eventWebhookRetryDelay(e.cfg.baseRetryDelay, e.failStreak), retryAfter))
}

// eventWebhookRetryAfter reads Retry-After (seconds or an HTTP date), capped
// at the retry window; missing, invalid or past is zero.
func eventWebhookRetryAfter(header string, now time.Time) time.Duration {
	var wait time.Duration
	if seconds, err := strconv.Atoi(strings.TrimSpace(header)); err == nil {
		wait = time.Duration(min(seconds, int(eventWebhookRetryWindow/time.Second))) * time.Second
	} else if at, err := http.ParseTime(header); err == nil {
		wait = at.Sub(now)
	}
	return min(max(wait, 0), eventWebhookRetryWindow)
}

// eventWebhookRetryDelay is how long after its attempts-th failed attempt an
// event is tried again: base doubled per earlier attempt, capped.
func eventWebhookRetryDelay(base time.Duration, attempts int) time.Duration {
	delay := base
	for i := 1; i < attempts; i++ {
		delay *= 2
		if delay >= eventWebhookRetryCap {
			return eventWebhookRetryCap
		}
	}
	return delay
}

func (s *Store) insertEvent(ctx context.Context, id, receiverID, eventType, payload, at string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO event_outbox (id, receiver_id, type, payload, created_at, attempts, next_attempt_at)
VALUES (?, ?, ?, ?, ?, 0, ?)`, id, receiverID, eventType, payload, at, at)
	if err != nil {
		return fmt.Errorf("insert event outbox: %w", err)
	}
	return nil
}

// eventWaiting is a row neither delivered nor given up on.
const eventWaiting = `delivered_at IS NULL AND failed_at IS NULL`

// eventWanted matches the rows the receiver at key wants: queued for its URL,
// of a type it ticks. Only those are sent or counted as waiting.
func eventWanted(key string, events []string) (string, []any) {
	if key == "" || len(events) == 0 {
		return `0`, nil
	}
	args := []any{key}
	for _, name := range events {
		args = append(args, name)
	}
	return `(receiver_id = ? AND type IN (?` + strings.Repeat(", ?", len(events)-1) + `))`, args
}

func (s *Store) nextDueEvent(ctx context.Context, at, receiverID string, events []string) (outboxEvent, bool, error) {
	var event outboxEvent
	wanted, args := eventWanted(receiverID, events)
	err := s.db.QueryRowContext(ctx, `
SELECT id, type, payload, attempts, created_at
FROM event_outbox
WHERE `+eventWaiting+` AND `+wanted+` AND next_attempt_at <= ?
ORDER BY next_attempt_at ASC, created_at ASC
LIMIT 1`, append(args, at)...).Scan(&event.ID, &event.Type, &event.Payload, &event.Attempts, &event.CreatedAt)
	if err == sql.ErrNoRows {
		return outboxEvent{}, false, nil
	}
	if err != nil {
		return outboxEvent{}, false, fmt.Errorf("read due event: %w", err)
	}
	return event, true, nil
}

// markEventDelivered and markEventFailed finish a row and empty its payload. A
// delivery wins over a give-up, so a row is never both.
func (s *Store) markEventDelivered(ctx context.Context, id, at string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE event_outbox SET delivered_at = ?, failed_at = NULL, payload = '', last_error = NULL, last_error_at = NULL WHERE id = ?`, at, id)
	if err != nil {
		return fmt.Errorf("mark event delivered: %w", err)
	}
	return nil
}

func (s *Store) markEventFailed(ctx context.Context, id, at, detail string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE event_outbox SET failed_at = ?, payload = '', last_error_at = ?, last_error = COALESCE(last_error || '; ', '') || ? WHERE id = ? AND delivered_at IS NULL`, at, at, detail, id)
	if err != nil {
		return fmt.Errorf("mark event failed: %w", err)
	}
	return nil
}

func (s *Store) rescheduleEvent(ctx context.Context, id string, attempts int, nextAt, errorAt, detail string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE event_outbox SET attempts = ?, next_attempt_at = ?, last_error = ?, last_error_at = ? WHERE id = ?`, attempts, nextAt, detail, errorAt, id)
	if err != nil {
		return fmt.Errorf("reschedule event: %w", err)
	}
	return nil
}

// eventDeliverySummary is the last week of delivery to one URL. Times are in
// the store's layout; empty or zero when there is nothing to report.
type eventDeliverySummary struct {
	LastDeliveredAt string `json:"last_delivered_at"`
	Delivered       int    `json:"delivered"`
	Waiting         int    `json:"waiting"`
	Failed          int    `json:"failed"`
	// The newest error on a waiting event.
	LastError   string `json:"last_error"`
	LastErrorAt string `json:"last_error_at"`
}

func (s *Store) eventDeliverySummary(ctx context.Context, settings EventWebhookSettings) (eventDeliverySummary, error) {
	var out eventDeliverySummary
	key := eventReceiverKey(settings.URL)
	if key == "" {
		return out, nil
	}
	wanted, args := eventWanted(key, settings.Events)
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(delivered_at), COUNT(failed_at), COALESCE(MAX(delivered_at), ''),
  COUNT(*) FILTER (WHERE `+eventWaiting+` AND `+wanted+`)
FROM event_outbox WHERE receiver_id = ?`, append(slices.Clone(args), key)...).Scan(&out.Delivered, &out.Failed, &out.LastDeliveredAt, &out.Waiting); err != nil {
		return eventDeliverySummary{}, fmt.Errorf("read event outbox counts: %w", err)
	}
	err := s.db.QueryRowContext(ctx, `
SELECT last_error, COALESCE(last_error_at, '')
FROM event_outbox
WHERE `+eventWaiting+` AND `+wanted+` AND COALESCE(last_error, '') <> ''
ORDER BY COALESCE(last_error_at, '') DESC, created_at DESC
LIMIT 1`, args...).Scan(&out.LastError, &out.LastErrorAt)
	if err != nil && err != sql.ErrNoRows {
		return eventDeliverySummary{}, fmt.Errorf("read event outbox last error: %w", err)
	}
	return out, nil
}

// settleEventOutbox deletes every waiting row the receiver in settings does
// not want. It runs after every save and every start whose settings loaded,
// with no worker running.
func (s *Store) settleEventOutbox(ctx context.Context, settings EventWebhookSettings) (int64, error) {
	wanted, args := eventWanted(eventReceiverKey(settings.URL), settings.Events)
	result, err := s.db.ExecContext(ctx, `DELETE FROM event_outbox WHERE `+eventWaiting+` AND NOT `+wanted, args...)
	if err != nil {
		return 0, fmt.Errorf("delete waiting events the receiver does not want: %w", err)
	}
	return result.RowsAffected()
}

// adoptEventsWithoutReceiver tags rows 0.4.0 queued, which have no receiver, with the
// stored URL's key: the URL they were queued for.
func (s *Store) adoptEventsWithoutReceiver(ctx context.Context, key string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE event_outbox SET receiver_id = ? WHERE receiver_id = ''`, key); err != nil {
		return fmt.Errorf("adopt events queued before receivers: %w", err)
	}
	return nil
}

// pruneEventOutbox gives up on every waiting row past the retry window (plus
// the margin), whoever it was queued for, and deletes rows older than a week.
// Best-effort and logged.
func (s *Store) pruneEventOutbox(logger *log.Logger, now time.Time) {
	at := formatUTCString(now)
	if _, err := s.db.ExecContext(context.Background(), `
UPDATE event_outbox SET failed_at = ?, payload = '', last_error_at = ?,
  last_error = COALESCE(last_error || '; ', '') || ?
WHERE `+eventWaiting+` AND created_at < ?`,
		at, at, fmt.Sprintf("gave up after %s", eventWebhookRetryWindow), formatUTCString(now.Add(-eventWebhookRetryWindow-eventOutboxGiveUpMargin))); err != nil {
		logger.Printf("event webhook outbox give-up failed: %v", err)
	}
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
// the outbox insert. A row a replaced worker writes is tagged with its own
// URL, so it is never sent anywhere else.
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

// applyEventWebhook puts settings in effect. The caller holds eventWebhookMu.
// The old worker is stopped and waited for, the new one put in place, the
// queue settled, and only then the new worker started: two workers never read
// the same row, and none runs while rows are deleted.
func (rt *Runtime) applyEventWebhook(settings EventWebhookSettings) {
	if rt.eventWebhookCancel != nil {
		rt.eventWebhookCancel()
		rt.eventWebhookCancel = nil
	}
	rt.eventWebhook.Load().wait()

	hook := newEventWebhook(rt.store, rt.logger, settings, os.Getenv(envAppVersion))
	if rt.eventWebhookStopped || rt.ctx.Err() != nil {
		// Shutdown may already be waiting on the workers: none is added.
		hook = nil
	}
	rt.eventWebhook.Store(hook)
	if dropped, err := rt.store.settleEventOutbox(context.Background(), settings); err != nil {
		rt.logger.Printf("event webhooks: %v", err)
	} else if dropped > 0 {
		rt.logger.Printf("event webhooks: %d waiting events the receiver no longer wants deleted", dropped)
	}
	if hook == nil {
		return
	}
	ctx, cancel := context.WithCancel(rt.ctx)
	rt.eventWebhookCancel = cancel
	hook.start(ctx, &rt.workerWG)
}

// startEventWebhook starts the prune, loads the stored receiver and applies
// it. Settings that cannot be loaded leave delivery off, with the reason kept
// for GET, and the queue untouched until the next save.
func (rt *Runtime) startEventWebhook() {
	rt.eventWebhookMu.Lock()
	defer rt.eventWebhookMu.Unlock()
	rt.startEventOutboxPrune()
	settings, converted, err := loadEventWebhookSettings(rt.eventWebhookSettingsPath)
	if err != nil {
		rt.logger.Printf("event webhooks disabled: %v", err)
		rt.eventWebhookLoadError = err.Error()
		rt.eventWebhookSettings = EventWebhookSettings{}
		rt.eventWebhook.Store(nil)
		return
	}
	if converted {
		// Not fatal: the converted settings apply, and the next start retries.
		if err := saveEventWebhookSettings(rt.eventWebhookSettingsPath, settings); err != nil {
			rt.logger.Printf("event webhooks: stored 0.4.0 settings converted (%d events, timeout %ds); saving that failed: %v", len(settings.Events), settings.timeoutSeconds(), err)
		} else {
			rt.logger.Printf("event webhooks: stored 0.4.0 settings converted (%d events, timeout %ds, saved)", len(settings.Events), settings.timeoutSeconds())
		}
	}
	if settings.URL != "" {
		if err := rt.store.adoptEventsWithoutReceiver(context.Background(), eventReceiverKey(settings.URL)); err != nil {
			rt.logger.Printf("event webhooks: %v", err)
		}
	}
	rt.eventWebhookSettings = settings
	rt.applyEventWebhook(settings)
}

// startEventOutboxPrune prunes now and then hourly until the operator stops,
// with or without a worker. The caller holds eventWebhookMu.
func (rt *Runtime) startEventOutboxPrune() {
	rt.store.pruneEventOutbox(rt.logger, time.Now().UTC())
	if rt.eventOutboxPruning || rt.eventWebhookStopped || rt.ctx == nil {
		return
	}
	rt.eventOutboxPruning = true
	rt.workerWG.Add(1)
	go func() {
		defer rt.workerWG.Done()
		ticker := time.NewTicker(eventOutboxPruneInterval)
		defer ticker.Stop()
		for {
			select {
			case <-rt.ctx.Done():
				return
			case <-ticker.C:
				rt.store.pruneEventOutbox(rt.logger, time.Now().UTC())
			}
		}
	}()
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
	URL            string   `json:"url"`
	SecretSet      bool     `json:"secret_set"`
	Events         []string `json:"events"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	// Error is why the stored settings were not accepted at startup, when
	// they were not: delivery is off until a PUT replaces them.
	Error    string               `json:"error,omitempty"`
	Catalog  []string             `json:"catalog"`
	Delivery eventDeliverySummary `json:"delivery"`
}

// eventWebhookSettingsUpdate is the PUT body. A field left out keeps its
// stored value, so a client that sends back what it read (which never
// includes the secret) does not erase it.
type eventWebhookSettingsUpdate struct {
	URL    *string   `json:"url"`
	Secret *string   `json:"secret"`
	Events *[]string `json:"events"`
	// A float, so 2.5 is refused with a code rather than as a bad body.
	TimeoutSeconds *float64 `json:"timeout_seconds"`
}

// eventWebhookSettingsHandler serves GET and PUT /settings/webhook.
func (rt *Runtime) eventWebhookSettingsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		writeMethodNotAllowed(w, http.MethodGet+", "+http.MethodPut)
		return
	}
	out, settings, ok := rt.updateEventWebhookSettings(w, r)
	if !ok {
		return
	}
	// Outside the settings lock. A summary that cannot be read is not worth
	// failing the request: the settings are what the panel needs.
	delivery, err := rt.store.eventDeliverySummary(r.Context(), settings)
	if err != nil {
		rt.logger.Printf("event webhook delivery summary failed: %v", err)
	}
	out.Delivery = delivery
	writeJSON(w, http.StatusOK, out)
}

// updateEventWebhookSettings applies a PUT, when it is one, and reports the
// settings in effect. A refusal has been written to w when ok is false.
func (rt *Runtime) updateEventWebhookSettings(w http.ResponseWriter, r *http.Request) (out eventWebhookSettingsResponse, s EventWebhookSettings, ok bool) {
	rt.eventWebhookMu.Lock()
	defer rt.eventWebhookMu.Unlock()
	if r.Method == http.MethodPut {
		var in eventWebhookSettingsUpdate
		dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return out, s, false
		}
		updated := rt.eventWebhookSettings
		if in.URL != nil {
			updated.URL = *in.URL
		}
		if in.Secret != nil {
			updated.Secret = *in.Secret
			// Not checked when turning delivery off, which discards it.
			if secret := strings.TrimSpace(*in.Secret); strings.TrimSpace(updated.URL) != "" && secret != "" && utf8.RuneCountInString(secret) < eventWebhookMinSecretLength {
				refuseEventWebhookSettings(w, eventWebhookSettingsRefusal("webhook_secret_too_short", "secret must be at least %d characters", eventWebhookMinSecretLength))
				return out, s, false
			}
		}
		if in.Events != nil {
			updated.Events = *in.Events
		}
		if in.TimeoutSeconds != nil {
			seconds := *in.TimeoutSeconds
			if seconds != math.Trunc(seconds) || seconds < 1 || seconds > eventWebhookMaxTimeoutSeconds {
				refuseEventWebhookSettings(w, eventWebhookTimeoutRefusal(seconds))
				return out, s, false
			}
			updated.TimeoutSeconds = int(seconds)
		}
		updated, err := normalizeEventWebhookSettings(updated)
		if err != nil {
			refuseEventWebhookSettings(w, err)
			return out, s, false
		}
		// A new address must not be signed with the old address's secret.
		if current := rt.eventWebhookSettings.URL; current != "" && updated.URL != "" && updated.URL != current && in.Secret == nil {
			refuseEventWebhookSettings(w, eventWebhookSettingsRefusal("webhook_secret_required_for_new_url", "a new url needs a new secret: send secret with it"))
			return out, s, false
		}
		if err := saveEventWebhookSettings(rt.eventWebhookSettingsPath, updated); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return out, s, false
		}
		rt.eventWebhookSettings, rt.eventWebhookLoadError = updated, ""
		rt.applyEventWebhook(updated)
	}
	s = rt.eventWebhookSettings
	events := s.Events
	if events == nil {
		events = []string{}
	}
	return eventWebhookSettingsResponse{
		URL: s.URL, SecretSet: s.Secret != "", Events: events, TimeoutSeconds: s.timeoutSeconds(),
		Error: rt.eventWebhookLoadError, Catalog: slices.Clone(eventCatalog),
	}, s, true
}

// refuseEventWebhookSettings answers a refused PUT: a coded 400 when the
// refusal is about a field.
func refuseEventWebhookSettings(w http.ResponseWriter, err error) {
	var refusal *eventWebhookSettingsError
	if errors.As(err, &refusal) {
		writeCodedJSONError(w, http.StatusBadRequest, refusal.Code, refusal.Message)
		return
	}
	writeJSONError(w, http.StatusBadRequest, err.Error())
}

// eventWebhookTestEvent is not in the catalog and never enters the outbox.
const eventWebhookTestEvent = "webhook.test"

type eventWebhookTestResponse struct {
	Status int    `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
	// ErrorCode is set only for a refused address (webhook_address_not_allowed).
	ErrorCode  string `json:"error_code,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

// eventWebhookTestHandler serves POST /settings/webhook/test: one signed
// webhook.test event to the saved receiver, now, outside the outbox. Whatever
// status the receiver answers is a result, not an error of this endpoint.
func (rt *Runtime) eventWebhookTestHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}
	// Locked only to read: a slow receiver must not hold up a save.
	rt.eventWebhookMu.Lock()
	settings := rt.eventWebhookSettings
	rt.eventWebhookMu.Unlock()
	if settings.URL == "" {
		writeCodedJSONError(w, http.StatusConflict, "webhook_not_configured", "No webhook receiver is saved. Save one before sending a test event.")
		return
	}

	version := strings.TrimSpace(os.Getenv(envAppVersion))
	event := newOutboundEvent(eventWebhookTestEvent, version, map[string]any{})
	payload, err := json.Marshal(event)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	req, err := newSignedEventRequest(r.Context(), settings.URL, settings.Secret, version, eventWebhookTestEvent, event.ID, payload)
	if err != nil {
		writeJSON(w, http.StatusOK, eventWebhookTestResponse{Error: fmt.Sprintf("build request: %v", err)})
		return
	}
	timeout := settings.requestTimeout()
	started := time.Now()
	client := newEventWebhookClient(timeout)
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		out := eventWebhookTestResponse{Error: eventWebhookRequestError(err, timeout), DurationMS: time.Since(started).Milliseconds()}
		var blocked *eventWebhookBlockedAddressError
		if errors.As(err, &blocked) {
			out.ErrorCode = "webhook_address_not_allowed"
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	writeJSON(w, http.StatusOK, eventWebhookTestResponse{Status: resp.StatusCode, DurationMS: time.Since(started).Milliseconds()})
}
