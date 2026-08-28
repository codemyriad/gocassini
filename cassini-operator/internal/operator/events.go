package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type StateChangeEvent struct {
	Type          string      `json:"type"`
	JobID         string      `json:"job_id"`
	AttemptNumber int         `json:"attempt_number,omitempty"`
	At            string      `json:"at"`
	Job           Job         `json:"job"`
	Attempt       *JobAttempt `json:"attempt,omitempty"`
}

type stateChangePublisher func(StateChangeEvent)

type eventHub struct {
	mu          sync.Mutex
	nextID      int
	subscribers map[int]chan StateChangeEvent
}

func newEventHub() *eventHub {
	return &eventHub{subscribers: map[int]chan StateChangeEvent{}}
}

func (h *eventHub) Subscribe() (<-chan StateChangeEvent, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	id := h.nextID
	h.nextID++
	ch := make(chan StateChangeEvent, 64)
	h.subscribers[id] = ch
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		subscriber, ok := h.subscribers[id]
		if !ok {
			return
		}
		delete(h.subscribers, id)
		close(subscriber)
	}
}

func (h *eventHub) Publish(event StateChangeEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, subscriber := range h.subscribers {
		select {
		case subscriber <- event:
		default:
			// Overflowed subscriber: close it so the SSE handler ends the
			// stream and the client (EventSource) reconnects and resyncs
			// from a fresh snapshot, instead of silently losing events
			// (D-367). Deleting before closing keeps a later unsubscribe
			// from double-closing the channel.
			delete(h.subscribers, id)
			close(subscriber)
		}
	}
}

func (s *Store) SetStateChangePublisher(publisher stateChangePublisher) {
	s.stateChangePublisher = publisher
}

func (s *Store) emitStateChange(ctx context.Context, eventType, jobID string, attemptNumber int) {
	if s.stateChangePublisher == nil {
		return
	}
	job, err := s.GetJob(ctx, jobID)
	if err != nil {
		return
	}
	event := StateChangeEvent{
		Type:          eventType,
		JobID:         jobID,
		AttemptNumber: attemptNumber,
		At:            nowUTCString(),
		Job:           job,
	}
	if attemptNumber > 0 {
		attempt, err := s.GetJobAttempt(ctx, jobID, attemptNumber)
		if err == nil {
			event.Attempt = &attempt
		}
	}
	s.stateChangePublisher(event)
}

func (rt *Runtime) publishStateChangeEvent(event StateChangeEvent) {
	if rt.events == nil {
		return
	}
	rt.events.Publish(event)
}

func (rt *Runtime) eventsHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/events" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	subscriber, unsubscribe := rt.events.Subscribe()
	defer unsubscribe()

	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-rt.ctx.Done():
			return
		case <-keepAlive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case event, ok := <-subscriber:
			if !ok {
				return
			}
			if err := writeSSEEvent(w, flusher, event.Type, event); err != nil {
				return
			}
		}
	}
}

func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, eventType string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal event payload: %w", err)
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, body); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func (s *Store) GetJobAttempt(ctx context.Context, jobID string, attemptNumber int) (JobAttempt, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT job_id, attempt_number, trigger_kind, request_json,
       stage, state,
       artifact_run_path, artifact_meeting_path, artifact_opus_path, artifact_opus_sha256, artifact_site_path,
       error, stop_reason, stop_requested_at, stop_signal_sent_at, record_exit_code, record_stop_detail,
       record_log_path, build_log_path, seal_log_path, publish_log_path,
       created_at, updated_at,
       record_queued_at, record_started_at, record_finished_at,
       build_queued_at, build_retry_not_before, build_deferral_count, build_started_at, build_finished_at,
       seal_queued_at, seal_started_at, seal_finished_at,
       publish_queued_at, publish_started_at, publish_finished_at,
       interrupted_at, completed_at
FROM job_attempts
WHERE job_id = ? AND attempt_number = ?`, jobID, attemptNumber)
	attempt, err := scanJobAttempt(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return JobAttempt{}, err
		}
		return JobAttempt{}, err
	}
	return attempt, nil
}
