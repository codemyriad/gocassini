package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// errInsightRunBusy is what a caller gets for beginning an attempt on a run that
// is already queued or running. The handler turns it into a 409: the status is
// the lock, so two retries pressed at once must produce one attempt and one
// refusal, never two attempts against one document path.
var errInsightRunBusy = errors.New("insight run is already queued or running")

// errInsightRunNotRunning is what a caller gets for finishing an attempt that
// was never begun, or that a crash sweep has already failed underneath it. It is
// a programming error rather than a user-visible state, so it has no status code
// of its own.
var errInsightRunNotRunning = errors.New("insight run is not running")

// insightProcessStartedAt is captured before any run can be written by this
// process. A row last written before it belongs to a process that is gone, which
// is one of the two proofs of strandedness sweepStranded uses.
var insightProcessStartedAt = time.Now()

const (
	// insightSweepGrace is the slack past an attempt's own deadline before a row
	// that has stopped moving is presumed stranded. An attempt that hits
	// insightRunTimeout records its own failure, so anything still queued or
	// running this long afterwards is a row nothing is going to write again.
	insightSweepGrace = 5 * time.Minute

	// insightSweepInterval is how often a read pays for the sweep. The UPDATE is
	// a no-op when nothing is stranded, but it is still a write transaction, and
	// the card polls: once a minute unwedges a run quickly enough while keeping
	// the cost off the polling path.
	insightSweepInterval = time.Minute
)

var insightSweep struct {
	sync.Mutex
	last time.Time
}

// InsightRun is one insight: a question asked once of several meetings, and
// whatever the latest attempt at answering it produced.
//
// Provider, Model, DocumentPath and Error describe the attempt that ran, not the
// request that was made. A retry re-resolves the endpoint from current settings
// — if it replayed the stored one, "no provider configured" and "401" would be
// unfixable by the action that exists to fix them (D-720 §4) — so nothing here
// may be read back as an input to the next attempt.
type InsightRun struct {
	ID              string    `json:"id"`
	CreatedBy       string    `json:"createdBy"`
	Status          string    `json:"status"`
	WorkflowID      string    `json:"workflowId"`
	WorkflowVersion string    `json:"workflowVersion"`
	WorkflowSHA256  string    `json:"workflowSha256"`
	MeetingIDs      []string  `json:"meetingIds"`
	RoomIDs         []string  `json:"roomIds"`
	Question        string    `json:"question"`
	Provider        string    `json:"provider"`
	Model           string    `json:"model"`
	DocumentPath    string    `json:"documentPath"`
	Error           string    `json:"error"`
	AttemptNumber   int       `json:"attemptNumber"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// InsightRunAttempt is one try at answering an InsightRun, kept after the run
// row has moved on. It exists for the same reason job_attempts does: "it failed
// twice against a provider we have since changed" is a different diagnosis from
// "it failed once", and the run row can only hold the latest.
type InsightRunAttempt struct {
	RunID         string     `json:"runId"`
	AttemptNumber int        `json:"attemptNumber"`
	Status        string     `json:"status"`
	Provider      string     `json:"provider"`
	Model         string     `json:"model"`
	DocumentPath  string     `json:"documentPath"`
	Error         string     `json:"error"`
	StartedAt     time.Time  `json:"startedAt"`
	FinishedAt    *time.Time `json:"finishedAt"`
}

// InsightOutcome is how an attempt ended. A succeeded outcome must name the
// document it wrote and a failed one must carry an error a user can act on:
// "the provider returned 401 -> Add a key" is the point of the failed card, and
// a blank message renders as a spinner that merely stopped.
type InsightOutcome struct {
	Status       string
	Provider     string
	Model        string
	DocumentPath string
	Error        string
}

type insightStore struct {
	db *sql.DB
}

// newInsightStore returns the run store. The repair a stranded run makes
// necessary happens on first use rather than here — see sweepStranded — so that
// a constructor stays a constructor, and so that a sweep which fails reports
// itself to the caller that needed it instead of deciding whether the surface
// mounts at all.
func newInsightStore(store *Store) *insightStore {
	return &insightStore{db: store.db}
}

// sweepStranded fails every run that has stopped moving and can never move
// again.
//
// A run is answered on a goroutine the request that asked for it started; there
// is no dispatcher to pick a stranded row back up, so a run nothing is going to
// finish will never finish. Failing it makes the card say so and — because the
// status is the lock — makes it retryable, where leaving it queued or running
// would wedge it at 409 for ever.
//
// STALENESS, not process start, is the criterion. Latching the sweep after the
// first read (which is what this did) covered a crash and nothing else: a row
// this live process wrote and then failed to move — a claim that could not be
// written, an outcome the store refused, a goroutine that died between the two —
// was excluded from every later sweep by the very predicate meant to protect it,
// and stayed unrecoverable until the next restart. So the cutoff is the LATER of
// two proofs that nothing will write the row again:
//
//   - it was last written before this process began, so whatever owned it is
//     gone; or
//   - it has not moved for longer than an attempt is allowed to take, so even a
//     live attempt would have recorded its own timeout by now.
//
// Both are "older than", so their union is one comparison against the later of
// the two, and a row this process is actively working on is outside both.
//
// The repair lives here rather than in the operator's startup block because these
// tables are reached through exactly one door, so it stays beside the state
// machine it repairs. A sweep that fails does not record itself as done, so the
// next caller retries it and its error reaches that caller rather than a log
// line.
func (s *insightStore) sweepStranded(ctx context.Context) error {
	insightSweep.Lock()
	defer insightSweep.Unlock()
	now := time.Now()
	if !insightSweep.last.IsZero() && now.Sub(insightSweep.last) < insightSweepInterval {
		return nil
	}
	cutoff := now.Add(-(insightRunTimeout + insightSweepGrace))
	if insightProcessStartedAt.After(cutoff) {
		cutoff = insightProcessStartedAt
	}
	if _, err := s.MarkInterruptedRunsFailed(ctx, cutoff); err != nil {
		return err
	}
	insightSweep.last = now
	return nil
}

// CreateRun records an insight before its content exists. The card has to appear
// the moment Generate is pressed; the alternative is a row that materialises
// from nowhere a minute later, which reads as a bug.
//
// The run is always stored queued at attempt 1, whatever the caller set: an
// attempt has not happened yet, and a queued run that already named a provider
// would be claiming an endpoint nothing has resolved.
func (s *insightStore) CreateRun(ctx context.Context, run InsightRun) error {
	// Re-checked here, and not only at the HTTP edge, so that an id which never
	// came from this operator cannot become a row whichever side of the module
	// boundary made it. isInsightRunID (insight_runtime.go) is the one definition
	// of the scheme gocassini's internal/insight package fixed; a second copy of
	// it here would be a second scheme waiting to drift.
	if !isInsightRunID(run.ID) {
		return fmt.Errorf("insight run id %q is not ins_ followed by sixteen hex characters", run.ID)
	}
	createdBy := strings.TrimSpace(run.CreatedBy)
	if createdBy == "" {
		return errors.New("insight run has no caller: a run nobody owns can never be listed")
	}
	if strings.TrimSpace(run.WorkflowID) == "" {
		return errors.New("insight run has no workflow")
	}
	if run.Status != "" && run.Status != insightStatusQueued {
		return fmt.Errorf("insight run created with status %q, want %q", run.Status, insightStatusQueued)
	}
	if len(run.MeetingIDs) == 0 {
		return errors.New("insight run names no meetings")
	}
	meetingIDs, err := encodeInsightIDList("meeting", run.MeetingIDs)
	if err != nil {
		return err
	}
	roomIDs, err := encodeInsightIDList("room", run.RoomIDs)
	if err != nil {
		return err
	}

	createdAt := run.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	stamp := formatUTCString(createdAt)
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO insight_runs (
  id, created_by, status,
  workflow_id, workflow_version, workflow_sha256,
  meeting_ids, room_ids, question,
  attempt_number, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID,
		createdBy,
		insightStatusQueued,
		run.WorkflowID,
		run.WorkflowVersion,
		run.WorkflowSHA256,
		meetingIDs,
		roomIDs,
		run.Question,
		1,
		stamp,
		stamp,
	); err != nil {
		return fmt.Errorf("insert insight run: %w", err)
	}
	return nil
}

// GetRun returns one run, or sql.ErrNoRows when there is none. Absent is
// deliberately the same answer as "not this caller's": the handler turns both
// into a 404, so the id space leaks nothing about other people's insights.
func (s *insightStore) GetRun(ctx context.Context, id string) (InsightRun, error) {
	if err := s.sweepStranded(ctx); err != nil {
		return InsightRun{}, err
	}
	row := s.db.QueryRowContext(ctx, insightRunSelect+`
WHERE id = ?`, id)
	run, err := scanInsightRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return InsightRun{}, sql.ErrNoRows
	}
	return run, err
}

// ListRuns returns this caller's runs, newest first.
func (s *insightStore) ListRuns(ctx context.Context, createdBy string) ([]InsightRun, error) {
	createdBy = strings.TrimSpace(createdBy)
	if createdBy == "" {
		return nil, errors.New("cannot list insight runs without a caller")
	}
	if err := s.sweepStranded(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, insightRunSelect+`
WHERE created_by = ?
ORDER BY created_at DESC, id DESC`, createdBy)
	if err != nil {
		return nil, fmt.Errorf("query insight runs: %w", err)
	}
	defer rows.Close()

	runs := []InsightRun{}
	for rows.Next() {
		run, err := scanInsightRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate insight runs: %w", err)
	}
	return runs, nil
}

// BeginAttempt moves a queued or failed run to running and returns it, or
// errInsightRunBusy if it is already queued or running.
//
// This is the lock. The update is a compare-and-swap against the exact status
// that was read, so of two retries pressed at once the second finds its status
// gone and is refused; the attempt row's primary key refuses it a second time.
// A first attempt keeps the run's attempt number and a retry bumps it, because
// a retry is another try at one insight rather than a second insight — one card,
// not a growing pile.
//
// Everything the previous attempt resolved is cleared here. What the next
// attempt uses is whatever settings resolve to now, and leaving the old provider
// visible while the new one runs would make the failed card lie for the length
// of the run.
func (s *insightStore) BeginAttempt(ctx context.Context, id string) (InsightRun, error) {
	if err := s.sweepStranded(ctx); err != nil {
		return InsightRun{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return InsightRun{}, fmt.Errorf("begin insight attempt: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var status string
	var attemptNumber int
	if err := tx.QueryRowContext(ctx, `
SELECT status, attempt_number
FROM insight_runs
WHERE id = ?`, id).Scan(&status, &attemptNumber); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return InsightRun{}, sql.ErrNoRows
		}
		return InsightRun{}, fmt.Errorf("load insight run for attempt: %w", err)
	}
	if status != insightStatusQueued && status != insightStatusFailed {
		return InsightRun{}, errInsightRunBusy
	}
	if status == insightStatusFailed {
		attemptNumber++
	}

	startedAt := nowUTCString()
	result, err := tx.ExecContext(ctx, `
UPDATE insight_runs
SET status = ?, attempt_number = ?,
    provider = '', model = '', document_path = '', error = '',
    updated_at = ?
WHERE id = ? AND status = ?`, insightStatusRunning, attemptNumber, startedAt, id, status)
	if err != nil {
		return InsightRun{}, fmt.Errorf("start insight attempt: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return InsightRun{}, fmt.Errorf("rows affected: %w", err)
	}
	if changed == 0 {
		return InsightRun{}, errInsightRunBusy
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO insight_run_attempts (
  run_id, attempt_number, status, started_at
) VALUES (?, ?, ?, ?)`, id, attemptNumber, insightStatusRunning, startedAt); err != nil {
		return InsightRun{}, fmt.Errorf("insert insight attempt: %w", err)
	}

	run, err := scanInsightRun(tx.QueryRowContext(ctx, insightRunSelect+`
WHERE id = ?`, id))
	if err != nil {
		return InsightRun{}, err
	}
	if err := tx.Commit(); err != nil {
		return InsightRun{}, fmt.Errorf("commit insight attempt: %w", err)
	}
	return run, nil
}

// FinishAttempt records how the running attempt ended, on both the run and the
// attempt row.
func (s *insightStore) FinishAttempt(ctx context.Context, id string, outcome InsightOutcome) error {
	switch outcome.Status {
	case insightStatusSucceeded:
		if strings.TrimSpace(outcome.DocumentPath) == "" {
			return errors.New("insight run succeeded without a document path")
		}
	case insightStatusFailed:
		if strings.TrimSpace(outcome.Error) == "" {
			return errors.New("insight run failed without an error a user could act on")
		}
	default:
		return fmt.Errorf("insight outcome status %q is neither %q nor %q", outcome.Status, insightStatusSucceeded, insightStatusFailed)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin insight outcome: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var status string
	var attemptNumber int
	if err := tx.QueryRowContext(ctx, `
SELECT status, attempt_number
FROM insight_runs
WHERE id = ?`, id).Scan(&status, &attemptNumber); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sql.ErrNoRows
		}
		return fmt.Errorf("load insight run for outcome: %w", err)
	}
	if status != insightStatusRunning {
		return errInsightRunNotRunning
	}

	finishedAt := nowUTCString()
	result, err := tx.ExecContext(ctx, `
UPDATE insight_runs
SET status = ?, provider = ?, model = ?, document_path = ?, error = ?, updated_at = ?
WHERE id = ? AND status = ?`,
		outcome.Status, outcome.Provider, outcome.Model, outcome.DocumentPath, outcome.Error,
		finishedAt, id, insightStatusRunning)
	if err != nil {
		return fmt.Errorf("finish insight run: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if changed == 0 {
		return errInsightRunNotRunning
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE insight_run_attempts
SET status = ?, provider = ?, model = ?, document_path = ?, error = ?, finished_at = ?
WHERE run_id = ? AND attempt_number = ?`,
		outcome.Status, outcome.Provider, outcome.Model, outcome.DocumentPath, outcome.Error,
		finishedAt, id, attemptNumber); err != nil {
		return fmt.Errorf("finish insight attempt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit insight outcome: %w", err)
	}
	return nil
}

// ListAttempts returns a run's attempts, newest first.
func (s *insightStore) ListAttempts(ctx context.Context, runID string) ([]InsightRunAttempt, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT run_id, attempt_number, status, provider, model, document_path, error, started_at, finished_at
FROM insight_run_attempts
WHERE run_id = ?
ORDER BY attempt_number DESC`, runID)
	if err != nil {
		return nil, fmt.Errorf("query insight run attempts: %w", err)
	}
	defer rows.Close()

	attempts := []InsightRunAttempt{}
	for rows.Next() {
		var attempt InsightRunAttempt
		var startedAt string
		var finishedAt sql.NullString
		if err := rows.Scan(
			&attempt.RunID, &attempt.AttemptNumber, &attempt.Status,
			&attempt.Provider, &attempt.Model, &attempt.DocumentPath, &attempt.Error,
			&startedAt, &finishedAt,
		); err != nil {
			return nil, fmt.Errorf("scan insight run attempt: %w", err)
		}
		if attempt.StartedAt, err = parseInsightTime(startedAt); err != nil {
			return nil, err
		}
		if finishedAt.Valid {
			parsed, err := parseInsightTime(finishedAt.String)
			if err != nil {
				return nil, err
			}
			attempt.FinishedAt = &parsed
		}
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate insight run attempts: %w", err)
	}
	return attempts, nil
}

// MarkInterruptedRunsFailed fails every run left queued or running that has not
// been written since `startedBefore`, and returns how many it repaired.
//
// Only rows last written before the cutoff are in scope, which is what lets this
// run at any moment rather than only during startup, and what makes it a no-op
// when nothing is stranded. sweepStranded owns the choice of cutoff and the
// reasoning behind it.
//
// One message for the two causes it covers, and it names both rather than
// asserting either: from the row alone, an operator that restarted mid-run and
// an attempt that stopped writing are the same evidence, and a sentence that
// picked one of them would be a guess printed on somebody's card.
func (s *insightStore) MarkInterruptedRunsFailed(ctx context.Context, startedBefore time.Time) (int64, error) {
	const message = "Cassini stopped before this insight finished — it restarted, or the run stopped making progress. Retry it."

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin insight interrupt sweep: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	cutoff := formatUTCString(startedBefore)
	sweptAt := nowUTCString()
	result, err := tx.ExecContext(ctx, `
UPDATE insight_runs
SET status = ?, error = ?, updated_at = ?
WHERE status IN (?, ?) AND updated_at < ?`,
		insightStatusFailed, message, sweptAt,
		insightStatusQueued, insightStatusRunning, cutoff)
	if err != nil {
		return 0, fmt.Errorf("mark interrupted insight runs failed: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE insight_run_attempts
SET status = ?, error = ?, finished_at = ?
WHERE status = ? AND started_at < ?`,
		insightStatusFailed, message, sweptAt,
		insightStatusRunning, cutoff); err != nil {
		return 0, fmt.Errorf("mark interrupted insight attempts failed: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit insight interrupt sweep: %w", err)
	}
	return count, nil
}

const insightRunSelect = `
SELECT id, created_by, status,
       workflow_id, workflow_version, workflow_sha256,
       meeting_ids, room_ids, question,
       provider, model, document_path, error,
       attempt_number, created_at, updated_at
FROM insight_runs`

func scanInsightRun(scanner rowScanner) (InsightRun, error) {
	var run InsightRun
	var meetingIDs, roomIDs, createdAt, updatedAt string
	if err := scanner.Scan(
		&run.ID, &run.CreatedBy, &run.Status,
		&run.WorkflowID, &run.WorkflowVersion, &run.WorkflowSHA256,
		&meetingIDs, &roomIDs, &run.Question,
		&run.Provider, &run.Model, &run.DocumentPath, &run.Error,
		&run.AttemptNumber, &createdAt, &updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return InsightRun{}, err
		}
		return InsightRun{}, fmt.Errorf("scan insight run: %w", err)
	}
	var err error
	if run.MeetingIDs, err = decodeInsightIDList("meeting", meetingIDs); err != nil {
		return InsightRun{}, err
	}
	if run.RoomIDs, err = decodeInsightIDList("room", roomIDs); err != nil {
		return InsightRun{}, err
	}
	if run.CreatedAt, err = parseInsightTime(createdAt); err != nil {
		return InsightRun{}, err
	}
	if run.UpdatedAt, err = parseInsightTime(updatedAt); err != nil {
		return InsightRun{}, err
	}
	return run, nil
}

// encodeInsightIDList stores a source list as JSON in one column. The lists are
// short, always read whole, and never joined against, so a side table would buy
// nothing but a join; `request_json` on jobs is the same trade.
func encodeInsightIDList(kind string, ids []string) (string, error) {
	if ids == nil {
		ids = []string{}
	}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return "", fmt.Errorf("insight run has a blank %s id", kind)
		}
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return "", fmt.Errorf("encode insight %s ids: %w", kind, err)
	}
	return string(encoded), nil
}

func decodeInsightIDList(kind, raw string) ([]string, error) {
	ids := []string{}
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, fmt.Errorf("decode insight %s ids: %w", kind, err)
	}
	if ids == nil {
		ids = []string{}
	}
	return ids, nil
}

func parseInsightTime(value string) (time.Time, error) {
	parsed, err := time.Parse(sortableUTCLayout, value)
	if err == nil {
		return parsed, nil
	}
	// Timestamps written before the fixed-width layout landed are still valid
	// RFC3339Nano; only their ordering was wrong (see migration 0007).
	parsed, rfcErr := time.Parse(time.RFC3339Nano, value)
	if rfcErr != nil {
		return time.Time{}, fmt.Errorf("parse insight timestamp %q: %w", value, err)
	}
	return parsed, nil
}
