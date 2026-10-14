package operator

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"time"
)

// Durable topic suggestion state (docs/proposals/automatic-tagging, "Data
// model"). It lives in annotations.sqlite3 so a confirm can write the mark and
// the verdict in one transaction. CREATE ... IF NOT EXISTS, because tests that
// downgrade the schema to replay an older migration keep these tables.
const topicsSchemaSQL = `
CREATE TABLE IF NOT EXISTS topic_library (
  tag_id TEXT PRIMARY KEY,
  label TEXT NOT NULL,
  label_folded TEXT NOT NULL,
  definition TEXT NOT NULL,
  definition_sha TEXT NOT NULL,
  color TEXT, icon TEXT,
  status TEXT NOT NULL,
  merged_into TEXT,
  origin TEXT NOT NULL,
  proposal_id TEXT,
  revision INTEGER NOT NULL,
  created_by TEXT NOT NULL, created_at INTEGER NOT NULL,
  updated_by TEXT NOT NULL, updated_at INTEGER NOT NULL);
CREATE UNIQUE INDEX IF NOT EXISTS topic_library_active_label ON topic_library(label_folded) WHERE status = 'active';
CREATE TABLE IF NOT EXISTS topic_library_event (
  id INTEGER PRIMARY KEY, tag_id TEXT NOT NULL, revision INTEGER NOT NULL,
  actor TEXT NOT NULL, at INTEGER NOT NULL, change_json BLOB NOT NULL,
  redacted_by TEXT, redacted_at INTEGER);
CREATE INDEX IF NOT EXISTS topic_library_event_by_tag ON topic_library_event(tag_id, revision);
CREATE TABLE IF NOT EXISTS topic_tag_alias (
  alias_tag_id TEXT PRIMARY KEY, tag_id TEXT NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS topic_verdict (
  opus_name TEXT NOT NULL, tag_id TEXT NOT NULL,
  verdict TEXT NOT NULL,
  source TEXT NOT NULL,
  actor_id TEXT,
  at INTEGER NOT NULL,
  operation_id TEXT,
  local_tag_id TEXT,
  payload_sha256 TEXT, definition_sha TEXT, scorer_key TEXT,
  rule TEXT, support_words INTEGER, peak REAL, min_margin REAL,
  PRIMARY KEY (opus_name, tag_id));
CREATE TABLE IF NOT EXISTS topic_receipt (
  caller TEXT NOT NULL, request_id TEXT NOT NULL, request_hash TEXT NOT NULL,
  opus_name TEXT,
  response BLOB NOT NULL, created_at INTEGER NOT NULL, PRIMARY KEY (caller, request_id));
CREATE INDEX IF NOT EXISTS topic_receipt_by_meeting ON topic_receipt(opus_name);
CREATE TABLE IF NOT EXISTS topic_receipt_target (
  caller TEXT NOT NULL, request_id TEXT NOT NULL, opus_name TEXT NOT NULL,
  PRIMARY KEY (caller, request_id, opus_name));
CREATE INDEX IF NOT EXISTS topic_receipt_target_by_meeting ON topic_receipt_target(opus_name);
CREATE TABLE IF NOT EXISTS topic_text_vector (
  kind TEXT NOT NULL,
  sha TEXT NOT NULL, model_revision TEXT NOT NULL, recipe_version TEXT NOT NULL,
  vector BLOB NOT NULL,
  PRIMARY KEY (kind, sha, model_revision, recipe_version));
CREATE TABLE IF NOT EXISTS topic_meeting (
  opus_name TEXT PRIMARY KEY,
  job_id TEXT NOT NULL,
  state TEXT NOT NULL,
  priority INTEGER NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  delivered_digest TEXT,
  published_attempt INTEGER,
  requeue INTEGER NOT NULL DEFAULT 0,
  claim TEXT, claimed_at INTEGER,
  payload_sha256 TEXT, audio_sha256 TEXT,
  language TEXT, language_confidence REAL, detector_version TEXT,
  embed_key TEXT, vectors_generation TEXT, scorer_key TEXT,
  model_revision TEXT, chunker_version TEXT, recipe_version TEXT,
  passages INTEGER, tokens INTEGER,
  attempts INTEGER NOT NULL DEFAULT 0, retry_at INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  queued_at INTEGER NOT NULL, finished_at INTEGER);
CREATE INDEX IF NOT EXISTS topic_meeting_queue ON topic_meeting(state, priority, retry_at, queued_at);
CREATE TABLE IF NOT EXISTS topic_suggestion (
  opus_name TEXT NOT NULL, tag_id TEXT NOT NULL,
  state TEXT NOT NULL,
  rule TEXT NOT NULL,
  support_words INTEGER NOT NULL,
  peak REAL, min_margin REAL,
  definition_sha TEXT NOT NULL, scorer_key TEXT NOT NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
  PRIMARY KEY (opus_name, tag_id));
CREATE INDEX IF NOT EXISTS topic_suggestion_by_tag ON topic_suggestion(tag_id, state);
CREATE TABLE IF NOT EXISTS topic_evidence (
  opus_name TEXT NOT NULL, tag_id TEXT NOT NULL, ordinal INTEGER NOT NULL,
  start_ms INTEGER NOT NULL, end_ms INTEGER NOT NULL,
  word_count INTEGER NOT NULL, counted INTEGER NOT NULL,
  PRIMARY KEY (opus_name, tag_id, ordinal),
  FOREIGN KEY (opus_name, tag_id) REFERENCES topic_suggestion ON DELETE CASCADE);
CREATE TABLE IF NOT EXISTS topic_proposal (
  id TEXT PRIMARY KEY, room_id TEXT NOT NULL,
  state TEXT NOT NULL,
  prior_state TEXT,
  centroid BLOB,
  model_revision TEXT NOT NULL, recipe_version TEXT NOT NULL, chunker_version TEXT NOT NULL,
  draft_name TEXT, hints_json TEXT, flags_json TEXT NOT NULL,
  nearest_tag_id TEXT,
  meeting_count INTEGER NOT NULL, day_count INTEGER NOT NULL, passage_count INTEGER NOT NULL,
  linked_tag_id TEXT, decided_by TEXT, decided_at INTEGER,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS topic_proposal_member (
  proposal_id TEXT NOT NULL REFERENCES topic_proposal(id) ON DELETE CASCADE,
  opus_name TEXT NOT NULL, start_ms INTEGER NOT NULL, end_ms INTEGER NOT NULL, sim REAL NOT NULL,
  PRIMARY KEY (proposal_id, opus_name, start_ms));
CREATE INDEX IF NOT EXISTS topic_proposal_member_by_meeting ON topic_proposal_member(opus_name);
CREATE TABLE IF NOT EXISTS topic_room_dirty (room_id TEXT PRIMARY KEY, dirty_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS topic_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
`

// topic_meeting states.
const (
	topicStateQueued      = "queued"
	topicStateRunning     = "running"
	topicStateDone        = "done"
	topicStateUnavailable = "unavailable"
	topicStateBlocked     = "blocked"
)

// topic_meeting priorities: lower runs first.
const (
	topicPriorityPublish  = 0
	topicPriorityRescore  = 1
	topicPriorityBackfill = 2
)

// topicMaxAttempts failures block a meeting until an admin retries it.
const topicMaxAttempts = 5

// topicRetryDelay is the wait after the n-th failure (n >= 1): one minute,
// doubling, at most six hours, as the annotation outbox backs off.
func topicRetryDelay(attempts int) time.Duration {
	d := time.Duration(math.Pow(2, float64(max(attempts-1, 0)))) * time.Minute
	return min(d, 6*time.Hour)
}

// topicMeetingRow is one meeting's work state.
type topicMeetingRow struct {
	OpusName         string
	JobID            string
	State            string
	Priority         int
	Reason           string
	DeliveredDigest  string
	PublishedAttempt int
	Claim            string
	PayloadSHA256    string
	EmbedKey         string
	Attempts         int
}

// topicsStore is the topic part of the durable annotations database.
type topicsStore struct {
	*annotationStore
	// boot identifies this process in claims: a running row claimed by
	// another boot is a crash leftover.
	boot string
	now  func() time.Time
	// lifecycle holds the retirement tombstones; nil in tests of the store
	// alone.
	lifecycle *Store
}

func newTopicsStore(store *annotationStore) *topicsStore {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return &topicsStore{annotationStore: store, boot: hex.EncodeToString(b[:]), now: time.Now}
}

func (s *topicsStore) unix() int64 { return s.now().Unix() }

// notRetired refuses a retired meeting. Called inside a write transaction:
// the database has one connection, so a retirement whose tombstone this does
// not see yet runs its delete transaction after this one commits.
func (s *topicsStore) notRetired(ctx context.Context, name string) error {
	if s.lifecycle == nil {
		return nil
	}
	return (ExAppConfig{lifecycle: s.lifecycle}).meetingNotRetired(ctx, name)
}

func (s *topicsStore) newClaim() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return s.boot + "-" + hex.EncodeToString(b[:])
}

// meta reads a topic_meta value, "" when unset.
func (s *topicsStore) meta(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM topic_meta WHERE key=?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func (s *topicsStore) setMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO topic_meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// enqueuePublished records a delivery. A running row is not overwritten: it
// is flagged so the worker queues it again when it finishes. The digest and
// attempt say which build was delivered, so the worker reads the transcript
// of that build and the reconciler can tell a rerun the hook missed.
func (s *topicsStore) enqueuePublished(ctx context.Context, opusName, jobID, digest string, attempt int) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := s.notRetired(ctx, opusName); err != nil {
			return err
		}
		var state string
		err := tx.QueryRowContext(ctx, `SELECT state FROM topic_meeting WHERE opus_name=?`, opusName).Scan(&state)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			_, err = tx.ExecContext(ctx, `INSERT INTO topic_meeting(opus_name,job_id,state,priority,delivered_digest,published_attempt,queued_at) VALUES(?,?,?,?,?,?,?)`,
				opusName, jobID, topicStateQueued, topicPriorityPublish, digest, attempt, s.unix())
			return err
		case err != nil:
			return err
		case state == topicStateRunning:
			_, err = tx.ExecContext(ctx, `UPDATE topic_meeting SET requeue=1,delivered_digest=?,published_attempt=?,job_id=? WHERE opus_name=?`, digest, attempt, jobID, opusName)
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE topic_meeting SET state=?,priority=?,reason='',delivered_digest=?,published_attempt=?,job_id=?,attempts=0,retry_at=0,last_error='',requeue=0,queued_at=? WHERE opus_name=?`,
			topicStateQueued, topicPriorityPublish, digest, attempt, jobID, s.unix(), opusName)
		return err
	})
}

// topicEnqueue says what a queueing knows about the delivery.
type topicEnqueue struct {
	// Digest and Attempt are kept when unknown ("" and 0).
	Digest  string
	Attempt int
	// ResetDigest forgets the stored digest: it belongs to an older delivery,
	// and the worker must ask the archive what was delivered.
	ResetDigest bool
	// Unblock queues a blocked row too: a new delivery is worth a new try.
	Unblock bool
}

// enqueue queues a meeting at priority unless it is running (flagged instead)
// or already queued at a higher priority. A retired meeting is refused. It
// returns whether anything changed.
func (s *topicsStore) enqueue(ctx context.Context, opusName, jobID string, priority int, opt topicEnqueue) (bool, error) {
	var changed bool
	digest := "SET_KEEP"
	if opt.Digest != "" {
		digest = opt.Digest
	}
	if opt.ResetDigest {
		digest = ""
	}
	// digestSQL keeps, sets or clears delivered_digest.
	digestSQL := `delivered_digest=CASE WHEN ?='SET_KEEP' THEN delivered_digest ELSE NULLIF(?,'') END`
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := s.notRetired(ctx, opusName); err != nil {
			return err
		}
		var state string
		var current int
		err := tx.QueryRowContext(ctx, `SELECT state,priority FROM topic_meeting WHERE opus_name=?`, opusName).Scan(&state, &current)
		if errors.Is(err, sql.ErrNoRows) {
			changed = true
			first := opt.Digest
			if opt.ResetDigest {
				first = ""
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO topic_meeting(opus_name,job_id,state,priority,delivered_digest,published_attempt,queued_at) VALUES(?,?,?,?,NULLIF(?,''),NULLIF(?,0),?)`,
				opusName, jobID, topicStateQueued, priority, first, opt.Attempt, s.unix())
			return err
		}
		if err != nil {
			return err
		}
		switch {
		case state == topicStateBlocked && !opt.Unblock:
			// Only a new delivery or an admin's retry unblocks a meeting.
		case state == topicStateRunning:
			changed = true
			_, err = tx.ExecContext(ctx, `UPDATE topic_meeting SET requeue=1,`+digestSQL+`,published_attempt=COALESCE(NULLIF(?,0),published_attempt) WHERE opus_name=?`, digest, digest, opt.Attempt, opusName)
		case state == topicStateQueued && current <= priority:
			_, err = tx.ExecContext(ctx, `UPDATE topic_meeting SET `+digestSQL+`,published_attempt=COALESCE(NULLIF(?,0),published_attempt) WHERE opus_name=?`, digest, digest, opt.Attempt, opusName)
		default:
			// done, unavailable or blocked, or queued at a lower priority.
			changed = true
			_, err = tx.ExecContext(ctx, `UPDATE topic_meeting SET state=?,priority=?,reason='',`+digestSQL+`,published_attempt=COALESCE(NULLIF(?,0),published_attempt),attempts=CASE WHEN state=? THEN attempts ELSE 0 END,retry_at=CASE WHEN state=? THEN retry_at ELSE 0 END,last_error=CASE WHEN state=? THEN last_error ELSE '' END,queued_at=? WHERE opus_name=?`,
				topicStateQueued, priority, digest, digest, opt.Attempt, topicStateQueued, topicStateQueued, topicStateQueued, s.unix(), opusName)
		}
		return err
	})
	return changed, err
}

// claimNext takes the next queued meeting, publish work first. Backfill work
// is skipped when allowBackfill is false (a build is queued).
func (s *topicsStore) claimNext(ctx context.Context, allowBackfill bool) (topicMeetingRow, bool, error) {
	limit := topicPriorityBackfill
	if !allowBackfill {
		limit = topicPriorityRescore
	}
	// One statement: what is claimed is exactly what is read, so a publish
	// that lands meanwhile either precedes it (and is read) or finds the row
	// running (and flags it).
	row := topicMeetingRow{Claim: s.newClaim(), State: topicStateRunning}
	var digest, payload, key sql.NullString
	var attempt sql.NullInt64
	err := s.db.QueryRowContext(ctx, `UPDATE topic_meeting SET state=?,claim=?,claimed_at=?,requeue=0
 WHERE opus_name=(SELECT opus_name FROM topic_meeting WHERE state=? AND retry_at<=? AND priority<=? ORDER BY priority,queued_at,opus_name LIMIT 1) AND state=?
 RETURNING opus_name,job_id,priority,delivered_digest,published_attempt,payload_sha256,embed_key,attempts`,
		topicStateRunning, row.Claim, s.unix(), topicStateQueued, s.unix(), limit, topicStateQueued).
		Scan(&row.OpusName, &row.JobID, &row.Priority, &digest, &attempt, &payload, &key, &row.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return topicMeetingRow{}, false, nil
	}
	if err != nil {
		return topicMeetingRow{}, false, err
	}
	row.DeliveredDigest, row.PublishedAttempt = digest.String, int(attempt.Int64)
	row.PayloadSHA256, row.EmbedKey = payload.String, key.String
	return row, true, nil
}

// release puts a claimed row back in the queue without using an attempt: the
// worker yielded (a recording, low memory, the inference lock).
func (s *topicsStore) release(ctx context.Context, row topicMeetingRow, retryAt time.Time, reason string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE topic_meeting SET state=?,claim=NULL,retry_at=?,last_error=? WHERE opus_name=? AND claim=?`,
		topicStateQueued, retryAt.Unix(), reason, row.OpusName, row.Claim)
	return err
}

// fail records a failed attempt: queued again after a backoff, blocked after
// topicMaxAttempts. A row flagged for requeue goes back without the attempt.
func (s *topicsStore) fail(ctx context.Context, row topicMeetingRow, cause error) (blocked bool, err error) {
	message := cause.Error()
	if len(message) > 500 {
		message = message[:500]
	}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var attempts, requeue int
		var claim sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT attempts,requeue,claim FROM topic_meeting WHERE opus_name=?`, row.OpusName).Scan(&attempts, &requeue, &claim); err != nil {
			return err
		}
		if claim.String != row.Claim {
			return nil
		}
		if requeue != 0 {
			_, err := tx.ExecContext(ctx, `UPDATE topic_meeting SET state=?,claim=NULL,requeue=0,attempts=0,retry_at=0,last_error=? WHERE opus_name=?`, topicStateQueued, message, row.OpusName)
			return err
		}
		attempts++
		if attempts >= topicMaxAttempts {
			blocked = true
			_, err := tx.ExecContext(ctx, `UPDATE topic_meeting SET state=?,claim=NULL,attempts=?,reason=?,last_error=?,finished_at=? WHERE opus_name=?`,
				topicStateBlocked, attempts, "failed", message, s.unix(), row.OpusName)
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE topic_meeting SET state=?,claim=NULL,attempts=?,retry_at=?,last_error=? WHERE opus_name=?`,
			topicStateQueued, attempts, s.now().Add(topicRetryDelay(attempts)).Unix(), message, row.OpusName)
		return err
	})
	return blocked, err
}

// blockNow blocks a row at once with a reason, such as model-fault, which a
// retry of the same input cannot fix.
func (s *topicsStore) blockNow(ctx context.Context, row topicMeetingRow, reason, message string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		ok, requeued, err := s.finishClaim(ctx, tx, row)
		if err != nil || !ok || requeued {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE topic_meeting SET state=?,claim=NULL,reason=?,last_error=?,finished_at=? WHERE opus_name=?`,
			topicStateBlocked, reason, message, s.unix(), row.OpusName)
		return err
	})
}

// dropRetired deletes a claimed row whose meeting was retired while it ran:
// retirement already deleted the rest, and nothing else would.
func (s *topicsStore) dropRetired(ctx context.Context, row topicMeetingRow) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM topic_meeting WHERE opus_name=? AND claim=?`, row.OpusName, row.Claim)
	return err
}

// markUnavailable records that a meeting cannot be checked, with a reason:
// it counts as not checked, never as having no topics. Its suggestions are
// withdrawn: they were made from a transcript the meeting no longer has.
func (s *topicsStore) markUnavailable(ctx context.Context, row topicMeetingRow, reason string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		ok, requeued, err := s.finishClaim(ctx, tx, row)
		if err != nil || !ok || requeued {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE topic_suggestion SET state='withdrawn',updated_at=? WHERE opus_name=? AND state='pending'`, s.unix(), row.OpusName); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE topic_meeting SET state=?,claim=NULL,reason=?,last_error='',attempts=0,finished_at=?,embed_key=NULL,payload_sha256=NULL,passages=NULL,tokens=NULL WHERE opus_name=?`,
			topicStateUnavailable, reason, s.unix(), row.OpusName)
		return err
	})
}

// finishClaim checks inside a finishing transaction that the claim still
// holds. A row flagged for requeue goes back to the queue instead, and the
// caller writes nothing else (requeued true).
func (s *topicsStore) finishClaim(ctx context.Context, tx *sql.Tx, row topicMeetingRow) (ok, requeued bool, err error) {
	var claim sql.NullString
	var requeue int
	err = tx.QueryRowContext(ctx, `SELECT claim,requeue FROM topic_meeting WHERE opus_name=?`, row.OpusName).Scan(&claim, &requeue)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil || claim.String != row.Claim {
		return false, false, err
	}
	if requeue != 0 {
		_, err = tx.ExecContext(ctx, `UPDATE topic_meeting SET state=?,claim=NULL,requeue=0,attempts=0,retry_at=0 WHERE opus_name=?`, topicStateQueued, row.OpusName)
		return true, true, err
	}
	return true, false, nil
}

// recoverRunning queues every running row another process claimed: the
// operator died while it ran.
func (s *topicsStore) recoverRunning(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE topic_meeting SET state=?,claim=NULL,requeue=0 WHERE state=? AND (claim IS NULL OR claim NOT LIKE ?)`,
		topicStateQueued, topicStateRunning, s.boot+"-%")
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// retryBlocked queues blocked rows again: the named meetings, or all.
func (s *topicsStore) retryBlocked(ctx context.Context, names []string) (int64, error) {
	var total int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		query := `UPDATE topic_meeting SET state=?,attempts=0,retry_at=0,reason='',last_error='',queued_at=? WHERE state=?`
		if len(names) == 0 {
			res, err := tx.ExecContext(ctx, query, topicStateQueued, s.unix(), topicStateBlocked)
			if err != nil {
				return err
			}
			total, _ = res.RowsAffected()
			return nil
		}
		for _, name := range names {
			res, err := tx.ExecContext(ctx, query+` AND opus_name=?`, topicStateQueued, s.unix(), topicStateBlocked, name)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			total += n
		}
		return nil
	})
	return total, err
}

// forgetMeeting deletes every topic row of a retired meeting inside the
// annotations retire transaction, and expires or scrubs its receipts.
func forgetTopicMeeting(ctx context.Context, tx *sql.Tx, name string) error {
	for _, query := range []string{
		`DELETE FROM topic_evidence WHERE opus_name=?`,
		`DELETE FROM topic_suggestion WHERE opus_name=?`,
		`DELETE FROM topic_verdict WHERE opus_name=?`,
		`DELETE FROM topic_meeting WHERE opus_name=?`,
	} {
		if _, err := tx.ExecContext(ctx, query, name); err != nil {
			return fmt.Errorf("forget topic rows: %w", err)
		}
	}
	// Proposals drawn on this meeting are hidden until recomputed from live
	// members (S7); their room is marked so the recompute runs.
	if _, err := tx.ExecContext(ctx, `INSERT INTO topic_room_dirty(room_id,dirty_at)
 SELECT DISTINCT p.room_id,unixepoch() FROM topic_proposal p JOIN topic_proposal_member m ON m.proposal_id=p.id WHERE m.opus_name=?
 ON CONFLICT(room_id) DO UPDATE SET dirty_at=excluded.dirty_at`, name); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE topic_proposal SET prior_state=CASE WHEN state='recompute' THEN prior_state ELSE state END,state='recompute',draft_name=NULL,hints_json=NULL,centroid=NULL,updated_at=unixepoch()
 WHERE id IN (SELECT proposal_id FROM topic_proposal_member WHERE opus_name=?)`, name); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM topic_proposal_member WHERE opus_name=?`, name); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE topic_receipt SET response=? WHERE opus_name=?`, []byte(`{"expired":true}`), name); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT r.caller,r.request_id,r.response FROM topic_receipt r JOIN topic_receipt_target t ON t.caller=r.caller AND t.request_id=r.request_id WHERE t.opus_name=?`, name)
	if err != nil {
		return err
	}
	type receipt struct {
		caller, id string
		raw        []byte
	}
	var receipts []receipt
	for rows.Next() {
		var r receipt
		if err := rows.Scan(&r.caller, &r.id, &r.raw); err != nil {
			rows.Close()
			return err
		}
		receipts = append(receipts, r)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, r := range receipts {
		raw, err := scrubRetiredResult(r.raw, name)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE topic_receipt SET response=? WHERE caller=? AND request_id=?`, raw, r.caller, r.id); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM topic_receipt_target WHERE opus_name=?`, name)
	return err
}
