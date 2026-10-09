package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Manual speaker separation (docs/speaker-separation.md): somebody who can read
// a meeting says a participant's device was shared, the operator diarizes that
// participant's own track once, and a `refine` attempt applies the people's
// edits document to the original words and republishes through the ordinary
// seal → publish path. Nothing is re-transcribed and the audio is never
// re-encoded, so the published file keeps its marks.
//
// This file is the document and its storage. The rules mirror the recorder's
// transcribe.SpeakerEdits.Validate exactly: the CLI re-validates whatever it is
// given, and a document the operator accepted but the CLI refused would fail a
// refine nobody can fix from the page.

const (
	triggerKindRerun  = "rerun"
	triggerKindRefine = "refine"

	speakerEditsFormat    = "cassini.speaker-edits.v1"
	maxSpeakerLabelRunes  = 64
	maxSpeakerEditEntries = 256
)

var (
	errSpeakerEditsRevisionConflict = errors.New("speaker edits revision conflict")
	errSpeakerEditsBusy             = errors.New("job is busy")
	// errSpeakerEditsSourceExpired: retention removed the capture a split
	// diarizes and every apply is checked against.
	errSpeakerEditsSourceExpired = errors.New("source audio expired")
)

// speakerIDPattern is what a participant id may be when it is used as a file
// name under the attempt's turns directory: exactly the CLI's speakerFileIDRE,
// so a split the operator accepts is never one `speakers apply` refuses after
// its turns were stored for good.
var speakerIDPattern = regexp.MustCompile(`^[a-z0-9_]{1,128}$`)

// speakerEditsDoc is cassini.speaker-edits.v1, the desired state of a
// meeting's speakers.
type speakerEditsDoc struct {
	Format   string              `json:"format"`
	Revision int                 `json:"revision"`
	Splits   []speakerEditsSplit `json:"splits"`
	Merges   []speakerEditsMerge `json:"merges"`
	Labels   []speakerEditsLabel `json:"labels"`
}

type speakerEditsSplit struct {
	SpeakerID string `json:"speakerId"`
}

type speakerEditsMerge struct {
	From string `json:"from"`
	Into string `json:"into"`
}

type speakerEditsLabel struct {
	SpeakerID string `json:"speakerId"`
	Label     string `json:"label"`
}

// emptySpeakerEditsDoc is the document of a meeting nobody has edited.
func emptySpeakerEditsDoc() speakerEditsDoc {
	return speakerEditsDoc{Format: speakerEditsFormat}.normalized()
}

// normalized gives every list a non-nil value, so the document always
// serialises with [] rather than null.
func (d speakerEditsDoc) normalized() speakerEditsDoc {
	if d.Splits == nil {
		d.Splits = []speakerEditsSplit{}
	}
	if d.Merges == nil {
		d.Merges = []speakerEditsMerge{}
	}
	if d.Labels == nil {
		d.Labels = []speakerEditsLabel{}
	}
	return d
}

// isSpeakerVoiceID reports whether id names a voice split from a shared device:
// "<participant>~<n>".
func isSpeakerVoiceID(id string) bool { return strings.Contains(id, "~") }

// speakerVoiceParent returns the participant a voice was split from, or "".
func speakerVoiceParent(id string) string {
	if i := strings.LastIndex(id, "~"); i > 0 {
		return id[:i]
	}
	return ""
}

// validate applies transcribe.SpeakerEdits.Validate's rules, then one the
// operator adds because it knows the meeting: only a participant of the
// original transcript can be split, since that is whose track gets diarized.
// Labels are trimmed first — a stray space is a typing accident, not a reason
// to refuse a name.
func (d *speakerEditsDoc) validate(participants map[string]bool) error {
	if len(d.Splits)+len(d.Merges)+len(d.Labels) > maxSpeakerEditEntries {
		return fmt.Errorf("too many edits")
	}
	seen := map[string]bool{}
	for _, s := range d.Splits {
		if s.SpeakerID == "" || isSpeakerVoiceID(s.SpeakerID) {
			return fmt.Errorf("cannot split %q", s.SpeakerID)
		}
		if seen[s.SpeakerID] {
			return fmt.Errorf("%q is split twice", s.SpeakerID)
		}
		seen[s.SpeakerID] = true
		if !participants[s.SpeakerID] || !speakerIDPattern.MatchString(s.SpeakerID) {
			return fmt.Errorf("%q is not a participant of this meeting", s.SpeakerID)
		}
	}
	merged := map[string]bool{}
	for _, m := range d.Merges {
		// Only two voices of one device can be the same person: speakers on
		// different microphones were never confused in the first place.
		if !isSpeakerVoiceID(m.From) || !isSpeakerVoiceID(m.Into) || m.From == m.Into || speakerVoiceParent(m.From) != speakerVoiceParent(m.Into) {
			return fmt.Errorf("invalid merge %q into %q", m.From, m.Into)
		}
		if merged[m.From] {
			return fmt.Errorf("%q is merged twice", m.From)
		}
		merged[m.From] = true
	}
	for _, m := range d.Merges {
		if merged[m.Into] {
			return fmt.Errorf("%q is merged into %q, which is itself merged", m.From, m.Into)
		}
	}
	labelled := map[string]bool{}
	for i := range d.Labels {
		l := &d.Labels[i]
		if l.SpeakerID == "" {
			return fmt.Errorf("label without speaker")
		}
		if labelled[l.SpeakerID] {
			return fmt.Errorf("%q is labelled twice", l.SpeakerID)
		}
		labelled[l.SpeakerID] = true
		l.Label = strings.TrimSpace(l.Label)
		if err := validateSpeakerLabel(l.Label); err != nil {
			return err
		}
	}
	return nil
}

// validateSpeakerLabel is transcribe.ValidateSpeakerLabel: 1-64 runes, no
// control characters, no leading or trailing space.
func validateSpeakerLabel(label string) error {
	n := utf8.RuneCountInString(label)
	if n == 0 || n > maxSpeakerLabelRunes {
		return fmt.Errorf("speaker label must be 1-%d characters", maxSpeakerLabelRunes)
	}
	if strings.TrimSpace(label) != label {
		return fmt.Errorf("speaker label must not start or end with a space")
	}
	for _, r := range label {
		if unicode.IsControl(r) {
			return fmt.Errorf("speaker label must not contain control characters")
		}
	}
	return nil
}

func parseSpeakerEditsDoc(raw string) (speakerEditsDoc, error) {
	var doc speakerEditsDoc
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return speakerEditsDoc{}, fmt.Errorf("decode speaker edits: %w", err)
	}
	if doc.Format != speakerEditsFormat {
		return speakerEditsDoc{}, fmt.Errorf("speaker edits: unsupported format %q", doc.Format)
	}
	return doc.normalized(), nil
}

// speakerEditsRecord is the job's row in speaker_edits.
type speakerEditsRecord struct {
	Revision        int
	Doc             speakerEditsDoc
	AppliedRevision int
	LastError       string
	LastReport      json.RawMessage
}

// speakerEditsAttempt is the newest attempt that carries a speaker-edits
// snapshot: what the page reports as applying or failed.
type speakerEditsAttempt struct {
	AttemptNumber int
	Revision      int
	Stage, State  string
	Error         string
	// Doc is the snapshot the attempt applies.
	Doc speakerEditsDoc
	// QueuedAt is when the attempt was queued: build_queued_at, which a
	// resource deferral keeps, or created_at for a row without one.
	QueuedAt string
	// StartedAt is build_started_at: when a build worker claimed the attempt,
	// kept through seal and publish. Empty while it waits, and cleared again
	// by a resource deferral that sends it back to the queue.
	StartedAt string
}

// GetSpeakerEdits returns the job's edits, or the empty document at revision 0.
func (s *Store) GetSpeakerEdits(ctx context.Context, jobID string) (speakerEditsRecord, error) {
	var docJSON string
	var lastError, report sql.NullString
	rec := speakerEditsRecord{Doc: emptySpeakerEditsDoc()}
	err := s.db.QueryRowContext(ctx, `
SELECT revision, doc_json, applied_revision, last_error, last_report_json
FROM speaker_edits WHERE job_id = ?`, jobID).Scan(&rec.Revision, &docJSON, &rec.AppliedRevision, &lastError, &report)
	if errors.Is(err, sql.ErrNoRows) {
		return rec, nil
	}
	if err != nil {
		return rec, fmt.Errorf("load speaker edits: %w", err)
	}
	if rec.Doc, err = parseSpeakerEditsDoc(docJSON); err != nil {
		return rec, err
	}
	rec.LastError = lastError.String
	if report.Valid && strings.TrimSpace(report.String) != "" {
		rec.LastReport = json.RawMessage(report.String)
	}
	return rec, nil
}

// LatestSpeakerEditsAttempt returns the newest attempt of jobID that applies a
// speaker-edits snapshot, if any.
func (s *Store) LatestSpeakerEditsAttempt(ctx context.Context, jobID string) (speakerEditsAttempt, bool, error) {
	var a speakerEditsAttempt
	var snapshot string
	var attemptError sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT attempt_number, stage, state, error, speaker_edits_json, COALESCE(build_queued_at, created_at), COALESCE(build_started_at, '')
FROM job_attempts
WHERE job_id = ? AND speaker_edits_json IS NOT NULL
ORDER BY attempt_number DESC
LIMIT 1`, jobID).Scan(&a.AttemptNumber, &a.Stage, &a.State, &attemptError, &snapshot, &a.QueuedAt, &a.StartedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return a, false, nil
	}
	if err != nil {
		return a, false, fmt.Errorf("load speaker edits attempt: %w", err)
	}
	doc, err := parseSpeakerEditsDoc(snapshot)
	if err != nil {
		return a, false, err
	}
	a.Revision, a.Doc = doc.Revision, doc
	a.Error = attemptError.String
	return a, true, nil
}

// AttemptSpeakerEdits returns an attempt's kind and the edits snapshot it
// applies (nil when it applies none). The build worker reads both here rather
// than from buildTask, which is rebuilt from the jobs row after a restart.
func (s *Store) AttemptSpeakerEdits(ctx context.Context, jobID string, attemptNumber int) (string, *speakerEditsDoc, error) {
	var kind string
	var snapshot sql.NullString
	if err := s.db.QueryRowContext(ctx, `
SELECT trigger_kind, speaker_edits_json
FROM job_attempts WHERE job_id = ? AND attempt_number = ?`, jobID, attemptNumber).Scan(&kind, &snapshot); err != nil {
		return "", nil, err
	}
	if !snapshot.Valid {
		return kind, nil, nil
	}
	doc, err := parseSpeakerEditsDoc(snapshot.String)
	if err != nil {
		return kind, nil, err
	}
	return kind, &doc, nil
}

// QueueSpeakerEdits stores doc as the job's next revision and queues the
// refine attempt that applies it, in one transaction: an accepted edit is
// always on its way to the published recording, across any restart.
//
// It is admitted the way a rerun is (QueueRerunAttempt), under the job's
// artifact lock: not while an archive operation is pending, and not once the
// capture has expired. The lock is waited for only briefly
// (speakerEditsLockWait): a worker holds it for a whole build, seal or
// publish, and a person saving names must not wait on one. A job whose lock
// stays taken is busy.
func (s *Store) QueueSpeakerEdits(ctx context.Context, jobID string, expectRevision int, doc speakerEditsDoc, updatedBy, queuedAt string) (int, error) {
	unlock, ok := s.lockArtifactsWithin(ctx, jobID, speakerEditsLockWait)
	if !ok {
		return 0, errSpeakerEditsBusy
	}
	defer unlock()
	pending, expired := s.artifactAdmission(ctx, jobID)
	switch {
	case pending:
		return 0, errSpeakerEditsBusy
	case expired:
		return 0, errSpeakerEditsSourceExpired
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin speaker edits: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var revision int
	err = tx.QueryRowContext(ctx, `SELECT revision FROM speaker_edits WHERE job_id = ?`, jobID).Scan(&revision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("load speaker edits revision: %w", err)
	}
	if expectRevision != revision {
		return revision, errSpeakerEditsRevisionConflict
	}
	var stage, state string
	if err := tx.QueryRowContext(ctx, `SELECT stage, state FROM jobs WHERE id = ?`, jobID).Scan(&stage, &state); err != nil {
		return revision, err
	}
	// A refine replaces whatever the job is doing, so it waits until the job
	// is doing nothing. A failed job is idle — that is how a failed refine is
	// retried — and so is one a restart interrupted, and one whose rebuild
	// was blocked for want of resources a refine does not need (it never
	// started, so the published meeting is still the one in current/).
	if !((stage == "done" && (state == "succeeded" || state == "failed")) || state == "interrupted" || state == "blocked") {
		return revision, errSpeakerEditsBusy
	}

	doc.Format = speakerEditsFormat
	doc.Revision = revision + 1
	raw, err := json.Marshal(doc.normalized())
	if err != nil {
		return revision, fmt.Errorf("encode speaker edits: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO speaker_edits (job_id, revision, doc_json, updated_by, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(job_id) DO UPDATE SET
  revision = excluded.revision, doc_json = excluded.doc_json,
  updated_by = excluded.updated_by, updated_at = excluded.updated_at,
  last_error = NULL`, jobID, doc.Revision, string(raw), updatedBy, queuedAt); err != nil {
		return revision, fmt.Errorf("store speaker edits: %w", err)
	}
	snapshot := string(raw)
	attemptNumber, err := queueAttemptTx(ctx, tx, jobID, triggerKindRefine, &snapshot, queuedAt)
	if err != nil {
		if errors.Is(err, ErrJobNotEligibleForRerun) {
			return revision, errSpeakerEditsBusy
		}
		return revision, err
	}
	if err := tx.Commit(); err != nil {
		return revision, fmt.Errorf("commit speaker edits: %w", err)
	}
	s.emitStateChange(ctx, "job.updated", jobID, attemptNumber)
	return doc.Revision, nil
}

// speakerEditsLockWait is how long a save waits for the job's artifact lock.
// A retention sweep, or a publish promoting and pruning after it marked its
// attempt succeeded (when the page has just shown the edit applied), holds it
// for moments; a build, seal or publish holds it for minutes.
const speakerEditsLockWait = 2 * time.Second

// lockArtifactsWithin takes the job's artifact lock if it comes free within
// wait, trying it every speakerEditsLockPoll.
func (s *Store) lockArtifactsWithin(ctx context.Context, jobID string, wait time.Duration) (func(), bool) {
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	poll := time.NewTicker(speakerEditsLockPoll)
	defer poll.Stop()
	for {
		if unlock, ok := s.tryLockArtifacts(jobID); ok {
			return unlock, true
		}
		select {
		case <-ctx.Done():
			return nil, false
		case <-deadline.C:
			return nil, false
		case <-poll.C:
		}
	}
}

const speakerEditsLockPoll = 20 * time.Millisecond

// speakerEditsReplaySnapshotTx is the document a rerun of jobID must replay:
// the one the published recording carries, or nil when none ever applied. Not
// the latest revision — one that never applied may never apply (a split the
// runtime cannot diarize), and replaying it would fail every rerun of the
// meeting until somebody undid it.
func speakerEditsReplaySnapshotTx(ctx context.Context, tx *sql.Tx, jobID string) (*string, error) {
	var applied int
	var docJSON sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT applied_revision, applied_doc_json FROM speaker_edits WHERE job_id = ?`, jobID).Scan(&applied, &docJSON)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (applied <= 0 || !docJSON.Valid)) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load speaker edits for rerun: %w", err)
	}
	return &docJSON.String, nil
}

// SpeakerSplitTurns returns the stored diarizer output for one participant.
func (s *Store) SpeakerSplitTurns(ctx context.Context, jobID, speakerID string) (string, bool, error) {
	var turns string
	err := s.db.QueryRowContext(ctx, `
SELECT turns_json FROM speaker_split_turns WHERE job_id = ? AND speaker_id = ?`, jobID, speakerID).Scan(&turns)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("load speaker turns: %w", err)
	}
	return turns, true, nil
}

// HasSpeakerSplitTurns reports whether any participant of jobID was diarized.
func (s *Store) HasSpeakerSplitTurns(ctx context.Context, jobID string) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM speaker_split_turns WHERE job_id = ?`, jobID).Scan(&n); err != nil {
		return false, fmt.Errorf("count speaker turns: %w", err)
	}
	return n > 0, nil
}

// SpeakerSplitTurnsStoredAt returns, for each participant of jobID whose turns
// are stored, when they were stored. A participant missing from the map has
// none yet: a refine that splits it has to diarize first.
func (s *Store) SpeakerSplitTurnsStoredAt(ctx context.Context, jobID string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT speaker_id, created_at FROM speaker_split_turns WHERE job_id = ?`, jobID)
	if err != nil {
		return nil, fmt.Errorf("list speaker turns: %w", err)
	}
	defer rows.Close()
	stored := map[string]string{}
	for rows.Next() {
		var speakerID, createdAt string
		if err := rows.Scan(&speakerID, &createdAt); err != nil {
			return nil, fmt.Errorf("scan speaker turns: %w", err)
		}
		stored[speakerID] = createdAt
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate speaker turns: %w", err)
	}
	return stored, nil
}

// speakerDiarizationRun is how long one stored turn set took to compute
// (elapsedMs, the model's own time) for how much audio (durationMs), and when
// it was stored.
type speakerDiarizationRun struct {
	ElapsedMs, DurationMs int64
	StoredAt              string
}

// SpeakerDiarizationRuns returns every turn set this operator stored, of any
// job, that says how long it took: what the refine estimate learns its pace
// from. A set without both numbers, or whose JSON is not readable, says
// nothing about speed and is left out.
func (s *Store) SpeakerDiarizationRuns(ctx context.Context) ([]speakerDiarizationRun, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT elapsed_ms, duration_ms, created_at FROM (
  SELECT CASE WHEN json_valid(turns_json) THEN CAST(json_extract(turns_json, '$.elapsedMs') AS INTEGER) END AS elapsed_ms,
         CASE WHEN json_valid(turns_json) THEN CAST(json_extract(turns_json, '$.durationMs') AS INTEGER) END AS duration_ms,
         created_at
  FROM speaker_split_turns
)
WHERE elapsed_ms > 0 AND duration_ms > 0`)
	if err != nil {
		return nil, fmt.Errorf("list speaker diarization runs: %w", err)
	}
	defer rows.Close()
	var runs []speakerDiarizationRun
	for rows.Next() {
		var run speakerDiarizationRun
		if err := rows.Scan(&run.ElapsedMs, &run.DurationMs, &run.StoredAt); err != nil {
			return nil, fmt.Errorf("scan speaker diarization run: %w", err)
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate speaker diarization runs: %w", err)
	}
	return runs, nil
}

// MissingSpeakerSplitTurns lists the splits of doc that have no stored turns
// yet: the ones a refine would have to diarize.
func (s *Store) MissingSpeakerSplitTurns(ctx context.Context, jobID string, doc speakerEditsDoc) ([]string, error) {
	var missing []string
	for _, split := range doc.Splits {
		_, ok, err := s.SpeakerSplitTurns(ctx, jobID, split.SpeakerID)
		if err != nil {
			return nil, err
		}
		if !ok {
			missing = append(missing, split.SpeakerID)
		}
	}
	return missing, nil
}

// PutSpeakerSplitTurns stores a participant's turns unless some are already
// stored, and returns the ones that are. Write-once is the point: the first
// diarization of a participant is the one every later apply reuses.
func (s *Store) PutSpeakerSplitTurns(ctx context.Context, jobID, speakerID, turnsJSON, modelSHA256, sourceSHA256, createdAt string) (string, error) {
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO speaker_split_turns (job_id, speaker_id, turns_json, model_sha256, source_sha256, created_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(job_id, speaker_id) DO NOTHING`, jobID, speakerID, turnsJSON, modelSHA256, sourceSHA256, createdAt); err != nil {
		return "", fmt.Errorf("store speaker turns: %w", err)
	}
	turns, _, err := s.SpeakerSplitTurns(ctx, jobID, speakerID)
	return turns, err
}

// SetAttemptSpeakerEditsReport keeps what the CLI reported for an attempt's
// apply until that attempt's publish succeeds.
func (s *Store) SetAttemptSpeakerEditsReport(ctx context.Context, jobID string, attemptNumber int, report string) error {
	if _, err := s.db.ExecContext(ctx, `
UPDATE job_attempts SET speaker_edits_report_json = ?, updated_at = ?
WHERE job_id = ? AND attempt_number = ?`, report, nowUTCString(), jobID, attemptNumber); err != nil {
		return fmt.Errorf("store speaker edits report: %w", err)
	}
	return nil
}

// SetSpeakerEditsError records why applying the job's edits failed. The
// published recording is untouched by the failure.
func (s *Store) SetSpeakerEditsError(ctx context.Context, jobID, detail string) error {
	if _, err := s.db.ExecContext(ctx, `
UPDATE speaker_edits SET last_error = ? WHERE job_id = ?`, strings.TrimSpace(detail), jobID); err != nil {
		return fmt.Errorf("store speaker edits error: %w", err)
	}
	return nil
}

// markSpeakerEditsPublishedTx records, in the transaction that marks the
// attempt's publish succeeded, that the attempt's snapshot is now what the
// published recording carries — so the page never sees a succeeded attempt
// whose revision is not applied yet. An attempt with no snapshot changes
// nothing. applied_revision never moves backwards, so a late publish of an
// older snapshot cannot un-apply a newer one; and a rerun replaying the
// applied revision leaves the error of a newer one that failed in place.
func markSpeakerEditsPublishedTx(ctx context.Context, tx *sql.Tx, jobID string, attemptNumber int) error {
	var snapshot, report sql.NullString
	if err := tx.QueryRowContext(ctx, `
SELECT speaker_edits_json, speaker_edits_report_json
FROM job_attempts WHERE job_id = ? AND attempt_number = ?`, jobID, attemptNumber).Scan(&snapshot, &report); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("load attempt speaker edits: %w", err)
	}
	if !snapshot.Valid {
		return nil
	}
	doc, err := parseSpeakerEditsDoc(snapshot.String)
	if err != nil {
		return err
	}
	// Every expression reads the row as it was before this UPDATE.
	if _, err := tx.ExecContext(ctx, `
UPDATE speaker_edits
SET applied_revision = MAX(applied_revision, ?1),
    applied_doc_json = CASE WHEN ?1 >= applied_revision THEN ?2 ELSE applied_doc_json END,
    last_report_json = CASE WHEN ?1 >= applied_revision THEN COALESCE(?3, last_report_json) ELSE last_report_json END,
    last_error = CASE WHEN ?1 >= revision THEN NULL ELSE last_error END
WHERE job_id = ?4`, doc.Revision, snapshot.String, nullableString(report.String), jobID); err != nil {
		return fmt.Errorf("mark speaker edits applied: %w", err)
	}
	return nil
}
