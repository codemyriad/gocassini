package operator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The topics reconciler (docs/proposals/automatic-tagging, "Triggers") finds
// missing or stale work from durable state: at startup, every hour and when
// the feature is switched on.

// topic_meta keys.
const (
	topicMetaEnabledAt     = "enabled_at"
	topicMetaBackfillSince = "backfill_since"
)

// topicSecondsPerMeeting is the backfill estimate: about 10 s of CPU per
// meeting on a shared host.
const topicSecondsPerMeeting = 10

func (s *topicsService) reconcile(ctx context.Context) error {
	if _, err := s.store.recoverRunning(ctx); err != nil {
		return err
	}
	versions, err := s.currentVersions(ctx)
	if err != nil {
		return err
	}
	queued, err := s.store.requeueStale(ctx, s.rt.store, versions, s.vectors.generation)
	if err != nil {
		return err
	}
	if err := s.forgetRetiredVectors(ctx); err != nil {
		s.logf("reconcile: retired vectors: %v", err)
	}
	missing, err := s.queueMissedPublishes(ctx)
	if err != nil {
		s.logf("reconcile: missed publishes: %v", err)
	}
	s.statsMu.Lock()
	s.stats.LastReconcileUTC = nowUTCString()
	s.statsMu.Unlock()
	if queued+missing > 0 {
		s.logf("reconcile queued %d stale and %d missed meetings", queued, missing)
		s.wakeUp()
	}
	return nil
}

// requeueStale queues done and unavailable rows whose stored result no longer
// matches: vectors lost or from another model, chunker or recipe, a changed
// scorer, or a rerun whose publish hook never ran.
func (s *topicsStore) requeueStale(ctx context.Context, jobs *Store, v topicVersions, generation string) (int, error) {
	type stale struct {
		name, job string
		priority  int
		attempt   int
	}
	rows, err := s.db.QueryContext(ctx, `SELECT opus_name,job_id,state,COALESCE(embed_key,''),COALESCE(payload_sha256,''),COALESCE(vectors_generation,''),COALESCE(scorer_key,''),COALESCE(published_attempt,0)
 FROM topic_meeting WHERE state IN (?,?,?)`, topicStateDone, topicStateUnavailable, topicStateBlocked)
	if err != nil {
		return 0, err
	}
	var work []stale
	for rows.Next() {
		var name, job, state, key, payload, gen, scorer string
		var attempt int
		if err := rows.Scan(&name, &job, &state, &key, &payload, &gen, &scorer, &attempt); err != nil {
			rows.Close()
			return 0, err
		}
		published := publishedAttempt(ctx, jobs, job)
		switch {
		case published > attempt:
			work = append(work, stale{name, job, topicPriorityPublish, published})
		case state != topicStateDone:
		case gen != generation:
			work = append(work, stale{name, job, topicPriorityBackfill, 0})
		case v.ModelRevision != "" && payload != "" && key != v.embedKey(payload):
			work = append(work, stale{name, job, topicPriorityBackfill, 0})
		case scorer != topicsScorerKey():
			work = append(work, stale{name, job, topicPriorityRescore, 0})
		}
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	queued := 0
	for _, w := range work {
		// A newer delivery: forget the older one's digest, and give a blocked
		// meeting a new try.
		newer := w.attempt > 0
		changed, err := s.enqueue(ctx, w.name, w.job, w.priority, topicEnqueue{Attempt: w.attempt, ResetDigest: newer, Unblock: newer})
		if errors.Is(err, errMeetingRetired) {
			continue
		}
		if err != nil {
			return queued, err
		}
		if changed {
			queued++
		}
	}
	return queued, nil
}

// publishedAttempt is the attempt the archive holds for a job, 0 if unknown.
func publishedAttempt(ctx context.Context, jobs *Store, job string) int {
	if jobs == nil {
		return 0
	}
	var n int
	_ = jobs.db.QueryRowContext(ctx, `SELECT published_attempt FROM artifact_availability WHERE job_id=?`, job).Scan(&n)
	return n
}

// queueMissedPublishes queues meetings published since the feature was
// switched on that have no row: the publish hook did not run (a crash between
// the publish record and the hook).
func (s *topicsService) queueMissedPublishes(ctx context.Context) (int, error) {
	enabledAt, err := s.store.meta(ctx, topicMetaEnabledAt)
	if err != nil || enabledAt == "" || s.targets == nil {
		return 0, err
	}
	rows, err := s.rt.store.db.QueryContext(ctx, `SELECT id FROM jobs WHERE state='succeeded' AND publish_finished_at>=?`, enabledAt)
	if err != nil {
		return 0, err
	}
	published := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		published[id] = true
	}
	if err := rows.Close(); err != nil || len(published) == 0 {
		return 0, err
	}
	targets, err := s.targets(ctx)
	if err != nil {
		return 0, err
	}
	queued := 0
	for _, t := range targets {
		if !published[t.JobID] {
			continue
		}
		n, err := s.store.queueIfMissing(ctx, s.rt.store, t, topicPriorityPublish)
		if err != nil {
			return queued, err
		}
		queued += n
	}
	return queued, nil
}

// queueIfMissing queues a meeting that has no row; 1 if it did.
func (s *topicsStore) queueIfMissing(ctx context.Context, jobs *Store, t searchBackfillTarget, priority int) (int, error) {
	attempt := publishedAttempt(ctx, jobs, t.JobID)
	var n int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := s.notRetired(ctx, t.OpusName); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO topic_meeting(opus_name,job_id,state,priority,published_attempt,queued_at) VALUES(?,?,?,?,NULLIF(?,0),?) ON CONFLICT(opus_name) DO NOTHING`,
			t.OpusName, t.JobID, topicStateQueued, priority, attempt, s.unix())
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	if errors.Is(err, errMeetingRetired) {
		return 0, nil
	}
	return int(n), err
}

// forgetRetiredVectors drops vectors of meetings retired while topic
// suggestions could not see it happen (an operator that crashed mid-way).
func (s *topicsService) forgetRetiredVectors(ctx context.Context) error {
	rows, err := s.vectors.db.QueryContext(ctx, `SELECT opus_name FROM passage_set`)
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		names = append(names, name)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, name := range names {
		if !errors.Is(s.store.notRetired(ctx, name), errMeetingRetired) {
			continue
		}
		release, err := meetingProjectionLocks.acquire(ctx, name)
		if err != nil {
			return err
		}
		err = s.vectors.forgetMeeting(ctx, name)
		release()
		if err != nil {
			return err
		}
	}
	return nil
}

// topicBackfillResult is what a "Check past meetings" request queued.
type topicBackfillResult struct {
	Queued          int `json:"queued"`
	EstimateSeconds int `json:"estimateSeconds"`
}

// queueTopicBackfill queues every archived meeting with no row, recorded on
// or after since (zero: all), at most limit (0: no limit).
func queueTopicBackfill(ctx context.Context, store *topicsStore, jobs *Store, targets []searchBackfillTarget, since time.Time, limit int) (topicBackfillResult, error) {
	var out topicBackfillResult
	for _, t := range targets {
		if limit > 0 && out.Queued >= limit {
			break
		}
		if !since.IsZero() {
			if recorded, ok := meetingRecordedAt(ctx, jobs, t); ok && recorded.Before(since) {
				continue
			}
		}
		n, err := store.queueIfMissing(ctx, jobs, t, topicPriorityBackfill)
		if err != nil {
			return out, err
		}
		out.Queued += n
	}
	var waiting int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM topic_meeting WHERE state IN (?,?)`, topicStateQueued, topicStateRunning).Scan(&waiting); err != nil {
		return out, err
	}
	out.EstimateSeconds = waiting * topicSecondsPerMeeting
	value := "all"
	if !since.IsZero() {
		value = since.UTC().Format(time.DateOnly)
	}
	if current, err := store.meta(ctx, topicMetaBackfillSince); err == nil && (current == "" || current != "all" && (value == "all" || value < current)) {
		if err := store.setMeta(ctx, topicMetaBackfillSince, value); err != nil {
			return out, err
		}
	}
	return out, nil
}

// meetingRecordedAt is when a meeting was recorded, from its job, else from
// the retention record; ok is false when neither knows.
func meetingRecordedAt(ctx context.Context, jobs *Store, t searchBackfillTarget) (time.Time, bool) {
	if jobs == nil {
		return time.Time{}, false
	}
	var created string
	err := jobs.db.QueryRowContext(ctx, `SELECT created_at FROM jobs WHERE id=?`, t.JobID).Scan(&created)
	if errors.Is(err, sql.ErrNoRows) || created == "" {
		m, ok, err := jobs.meetingLifecycle(ctx, t.OpusName)
		if err != nil || !ok {
			return time.Time{}, false
		}
		created = m.CreatedAtUTC
	}
	at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(created))
	return at, err == nil
}

// parseTopicSince reads a backfill start date, YYYY-MM-DD; "" is no limit.
func parseTopicSince(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	at, err := time.Parse(time.DateOnly, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("since must be a date like 2026-10-01")
	}
	return at, nil
}
