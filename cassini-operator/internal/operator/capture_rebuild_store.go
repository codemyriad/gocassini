package operator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Persistence for "a participant's capture arrived after the transcript was
// made" (D-698).
//
// The operator queues the build the instant Talk reports the recording stopped,
// and the browser starts its upload on that same signal, so on any link slower
// than the build the audio lands too late to be in the transcript. Waiting
// before building cannot cover that — a wait tries to bound something the
// server does not control, and the participants it would skip are exactly the
// ones the feature exists for. Noticing afterwards can, and this is where the
// noticing is written down.
//
// Two counters carry it. NoteSourceAudioUpload bumps one for every upload that
// actually changed the bytes on disk; a build stamps the other with the value
// it consumed, and only once it has succeeded. Any gap is audio no build has
// seen. Both live on the jobs row, so the intent survives a restart with no
// recovery path of its own: the dispatcher's first pass after startup finds the
// same gap the last pass before the crash did.

// ErrCaptureJobAmbiguous is returned when an upload's room and call window
// match more than one recording. It is deliberately an error and not a choice:
// picking one would put a meeting's speech in another meeting's transcript, and
// a missing rebuild is recoverable by hand where a wrong one is not.
var ErrCaptureJobAmbiguous = errors.New("capture matches more than one recording")

// ErrNoSuchJob is returned when a note is written for a job that is not there.
var ErrNoSuchJob = errors.New("no such job")

// captureJobMatch is one recording an upload could belong to.
type captureJobMatch struct {
	JobID     string
	StartMS   int64
	EndMS     int64
	Stage     string
	State     string
	Succeeded bool
}

// captureRecordingWindow is a job's recorded span in wall-clock milliseconds.
// Both ends are always resolved: see effectiveRecordingEndMS for what stands in
// when a recording has no finish time.
type captureRecordingWindow struct {
	StartMS int64
	EndMS   int64
}

// effectiveRecordingEndMS is where a recording's window ends for the purpose of
// matching captures to it.
//
// record_finished_at when it is there. When it is not, the recording is either
// still running or was cut short by a crash, and neither may be given an
// unbounded end: that makes a dead recording from last month overlap every call
// that room ever holds again, which is a wrong attribution on its own and, once
// two of them do it, an ambiguity that stops every later rebuild in that room.
//
// updated_at is the bound. Nothing about a recording can have happened after
// the last time its row changed, so it is an upper bound that is honest for a
// live recording (the row is written as the job moves) and tight for a dead one
// (it stops at the crash). record_started_at is the last resort, which with the
// overlap slack still matches a capture from the same moment.
func effectiveRecordingEndMS(startedAt, finishedAt, updatedAt sql.NullString) int64 {
	if end := parseStoredMS(finishedAt); end > 0 {
		return end
	}
	if end := parseStoredMS(updatedAt); end > 0 {
		return end
	}
	return parseStoredMS(startedAt)
}

// captureWindowSlackMS mirrors windowsOverlap in the recorder's
// internal/transcribe/sourceaudio.go, which is what the build itself uses to
// decide whether a capture belongs to a recording.
//
// Keeping the two identical is the point. A capture the build would happily
// splice but this could not resolve would be stored, attributed to nothing and
// never rebuilt for; a capture this resolved but the build would reject would
// schedule a rebuild that changes nothing. Either way the two halves of the
// feature would disagree about the same directory.
const captureWindowSlackMS = 60_000

// captureWindowsOverlap is windowsOverlap, copied rather than imported: the
// operator and the recorder are separate Go modules. The comment above says why
// it must not drift.
func captureWindowsOverlap(aStart, aEnd, bStart, bEnd int64) bool {
	if aStart <= 0 || bStart <= 0 {
		return false
	}
	return aStart-captureWindowSlackMS <= bEnd && bStart-captureWindowSlackMS <= aEnd
}

// parseStoredMS converts a persisted timestamp to epoch milliseconds. Zero
// means absent or unreadable; effectiveRecordingEndMS is what decides what
// stands in for a missing end, and no caller treats zero as an open window.
func parseStoredMS(value sql.NullString) int64 {
	raw := strings.TrimSpace(value.String)
	if !value.Valid || raw == "" {
		return 0
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return 0
	}
	return parsed.UnixMilli()
}

// ResolveJobForCapture finds the ONE recording an upload belongs to, by Talk
// room and by the capture's call window overlapping the recording's.
//
// The room alone is not enough. A busy room holds many recordings, and a
// standup room can hold two in an afternoon; matching on the room would put one
// meeting's speech in another's transcript. Neither is "the most recent
// recording in this room", which is the same mistake wearing a tie-break.
//
// More than one match is refused rather than resolved. Two overlapping
// recordings of one room is not a state this operator produces, so a capture
// that matches two of them describes something nobody modelled — and the honest
// answer to that is a log line an administrator can act on, not a coin toss
// whose result is somebody's words in the wrong meeting.
//
// The comparison happens in Go rather than in SQL because the persisted
// timestamps are fixed-width RFC3339Nano with nine fractional digits, which
// SQLite's strftime does not parse; doing it here also means this and the
// build's own discovery run the identical overlap test.
func (s *Store) ResolveJobForCapture(ctx context.Context, roomToken string, callStartMS, callEndMS int64) (captureJobMatch, error) {
	token := strings.TrimSpace(roomToken)
	if token == "" {
		return captureJobMatch{}, errors.New("no room token")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, stage, state, record_started_at, record_finished_at, updated_at
FROM jobs
WHERE json_extract(talk_binding, '$.room_token') = ?
  AND record_started_at IS NOT NULL`, token)
	if err != nil {
		return captureJobMatch{}, fmt.Errorf("query jobs for capture: %w", err)
	}
	defer rows.Close()

	var matches []captureJobMatch
	for rows.Next() {
		var (
			match      captureJobMatch
			startedAt  sql.NullString
			finishedAt sql.NullString
			updatedAt  sql.NullString
		)
		if err := rows.Scan(&match.JobID, &match.Stage, &match.State, &startedAt, &finishedAt, &updatedAt); err != nil {
			return captureJobMatch{}, fmt.Errorf("scan job for capture: %w", err)
		}
		match.StartMS = parseStoredMS(startedAt)
		match.EndMS = effectiveRecordingEndMS(startedAt, finishedAt, updatedAt)
		if match.StartMS <= 0 {
			continue
		}
		if !captureWindowsOverlap(match.StartMS, match.EndMS, callStartMS, callEndMS) {
			continue
		}
		match.Succeeded = match.State == "succeeded"
		matches = append(matches, match)
	}
	if err := rows.Err(); err != nil {
		return captureJobMatch{}, fmt.Errorf("iterate jobs for capture: %w", err)
	}
	switch len(matches) {
	case 0:
		return captureJobMatch{}, sql.ErrNoRows
	case 1:
		return matches[0], nil
	default:
		ids := make([]string, 0, len(matches))
		for _, match := range matches {
			ids = append(ids, match.JobID)
		}
		return captureJobMatch{}, fmt.Errorf("%w: %s", ErrCaptureJobAmbiguous, strings.Join(ids, ", "))
	}
}

// RecordingWindowForJob reads the span a job recorded, which is the window its
// captures are discovered against.
func (s *Store) RecordingWindowForJob(ctx context.Context, jobID string) (captureRecordingWindow, error) {
	var startedAt, finishedAt, updatedAt sql.NullString
	if err := s.db.QueryRowContext(ctx,
		`SELECT record_started_at, record_finished_at, updated_at FROM jobs WHERE id = ?`, jobID).
		Scan(&startedAt, &finishedAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return captureRecordingWindow{}, ErrNoSuchJob
		}
		return captureRecordingWindow{}, fmt.Errorf("read recording window: %w", err)
	}
	return captureRecordingWindow{
		StartMS: parseStoredMS(startedAt),
		EndMS:   effectiveRecordingEndMS(startedAt, finishedAt, updatedAt),
	}, nil
}

// NoteSourceAudioUpload records that audio arrived for a recording.
//
// Unconditional on stage and state on purpose. An upload that lands before the
// build starts, while it runs, or long after it published must all count the
// same way: this says only that audio exists, and the dispatcher decides
// separately whether a build has since consumed it. Making the write
// conditional is how an upload gets lost, because the interesting cases are
// exactly the ones where the job is somewhere unexpected.
func (s *Store) NoteSourceAudioUpload(ctx context.Context, jobID, at string) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET source_audio_upload_seq = source_audio_upload_seq + 1,
    source_audio_upload_at = ?,
    updated_at = ?
WHERE id = ?`, at, at, jobID)
	if err != nil {
		return fmt.Errorf("note source audio upload: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("note source audio upload: %w", err)
	}
	if affected == 0 {
		return ErrNoSuchJob
	}
	return nil
}

// SourceAudioUploadSeq reads the counter a build is about to consume.
func (s *Store) SourceAudioUploadSeq(ctx context.Context, jobID string) (int64, error) {
	var seq int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT source_audio_upload_seq FROM jobs WHERE id = ?`, jobID).Scan(&seq); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNoSuchJob
		}
		return 0, fmt.Errorf("read source audio upload seq: %w", err)
	}
	return seq, nil
}

// MarkSourceAudioBuilt records which uploads a successful build consumed, and
// what it read while consuming them.
//
// Stamped from the value the build read when it CLAIMED its work, never from
// the current value: an upload that landed while the build was running has
// already bumped the counter, and stamping the newer figure would swallow audio
// the build never saw — counted as consumed by a build that never opened it,
// with no rebuild ever owed. Called only on success, so a build that failed or
// was interrupted leaves its debt standing and the next pass tries again.
//
// The guard keeps the stamp monotonic, so two builds finishing out of order
// cannot move it backwards and re-owe work that is already done.
func (s *Store) MarkSourceAudioBuilt(ctx context.Context, jobID string, consumed int64, digest string) error {
	if _, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET source_audio_built_seq = ?, source_audio_built_digest = ?, updated_at = ?
WHERE id = ? AND source_audio_built_seq <= ?`,
		consumed, digest, nowUTCString(), jobID, consumed); err != nil {
		return fmt.Errorf("mark source audio built: %w", err)
	}
	return nil
}

// ClearSourceAudioDebt settles a debt without rebuilding.
//
// Used when the audio is owed but rebuilding would achieve nothing: the capture
// set on disk is byte-for-byte the one the last build already read, or the
// retention sweep has removed it, or the meeting is older than the retention
// window. Left unsettled, each of those would be rescanned every fifteen
// seconds for the life of the installation.
func (s *Store) ClearSourceAudioDebt(ctx context.Context, jobID string, consumed int64) error {
	if _, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET source_audio_built_seq = ?, updated_at = ?
WHERE id = ? AND source_audio_built_seq <= ?`,
		consumed, nowUTCString(), jobID, consumed); err != nil {
		return fmt.Errorf("clear source audio debt: %w", err)
	}
	return nil
}

// NoteSourceAudioRebuildQueued counts one rebuild against this job's ceiling.
//
// Called AFTER the rerun attempt is committed, deliberately. Counting first and
// failing to queue would burn an allowance on a rebuild that never ran; this
// way the worst case is an allowance not charged, and the rebuild that was
// queued still clears the debt when it succeeds, so nothing loops.
func (s *Store) NoteSourceAudioRebuildQueued(ctx context.Context, jobID string) error {
	if _, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET source_audio_rebuild_count = source_audio_rebuild_count + 1, updated_at = ?
WHERE id = ?`, nowUTCString(), jobID); err != nil {
		return fmt.Errorf("note source audio rebuild: %w", err)
	}
	return nil
}

// sourceAudioRebuildCandidate is a job holding audio no build has seen.
type sourceAudioRebuildCandidate struct {
	JobID        string
	RoomToken    string
	Stage        string
	State        string
	UploadSeq    int64
	BuiltSeq     int64
	BuiltDigest  string
	RebuildCount int
	// UploadedAt is when the newest upload was attributed to this job, which
	// the quiet period is measured from. Zero when it predates this column.
	UploadedAt time.Time
	Window     captureRecordingWindow
	// RecordedAt is when the recording ended, or started when it never
	// finished. The retention bound is measured from it.
	RecordedAt time.Time
}

// ListJobsAwaitingSourceAudioRebuild returns terminal jobs holding audio no
// build has seen.
//
// 'succeeded' and 'interrupted', and not the other two terminal states.
//
//   - succeeded is the case this exists for: the transcript is made and
//     published, and a participant's audio turned up afterwards.
//   - interrupted is how a rebuild that the operator died in the middle of
//     comes back. It is left owed by MarkSourceAudioBuilt only stamping on
//     success, and the startup sweep puts the job in exactly this state; being
//     listed here is what re-detects it, with no recovery path of its own.
//     QueueRerunAttempt refuses anything without a ready run bundle, so an
//     interrupted RECORDING — which has no bundle — is filtered out there
//     rather than needing a second rule here.
//   - failed and blocked are excluded. Each has a reason of its own and an
//     administrator to decide about it, and quietly re-running one on the back
//     of an upload would hide that. Their debt is not lost: whenever an
//     administrator does rerun them, that build reads the counter when it
//     claims its work and the audio is used.
//
// Jobs that have spent their rebuild ceiling are filtered here rather than by
// the caller. Their debt is deliberately never settled -- settling it would
// claim the audio was used -- so they stay in the partial index for ever, and a
// caller-side check would let a handful of them fill the LIMIT below and starve
// every other job in the installation of its rebuild.
func (s *Store) ListJobsAwaitingSourceAudioRebuild(ctx context.Context, limit int) ([]sourceAudioRebuildCandidate, error) {
	if limit <= 0 {
		limit = 16
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, json_extract(talk_binding, '$.room_token'),
       stage, state,
       source_audio_upload_seq, source_audio_built_seq,
       source_audio_built_digest, source_audio_rebuild_count,
       source_audio_upload_at, record_started_at, record_finished_at, updated_at
FROM jobs
WHERE source_audio_upload_seq > source_audio_built_seq
  AND state IN ('succeeded', 'interrupted')
  AND source_audio_rebuild_count < ?
ORDER BY updated_at ASC
LIMIT ?`, maxSourceAudioRebuilds, limit)
	if err != nil {
		return nil, fmt.Errorf("query jobs awaiting source audio rebuild: %w", err)
	}
	defer rows.Close()

	var candidates []sourceAudioRebuildCandidate
	for rows.Next() {
		var (
			candidate   sourceAudioRebuildCandidate
			roomToken   sql.NullString
			builtDigest sql.NullString
			uploadedAt  sql.NullString
			startedAt   sql.NullString
			finishedAt  sql.NullString
			updatedAt   sql.NullString
		)
		if err := rows.Scan(&candidate.JobID, &roomToken, &candidate.Stage, &candidate.State,
			&candidate.UploadSeq, &candidate.BuiltSeq, &builtDigest, &candidate.RebuildCount,
			&uploadedAt, &startedAt, &finishedAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan job awaiting source audio rebuild: %w", err)
		}
		candidate.RoomToken = strings.TrimSpace(roomToken.String)
		candidate.BuiltDigest = strings.TrimSpace(builtDigest.String)
		candidate.Window = captureRecordingWindow{
			StartMS: parseStoredMS(startedAt),
			EndMS:   effectiveRecordingEndMS(startedAt, finishedAt, updatedAt),
		}
		if ms := parseStoredMS(uploadedAt); ms > 0 {
			candidate.UploadedAt = time.UnixMilli(ms).UTC()
		}
		if recordedMS := candidate.Window.EndMS; recordedMS > 0 {
			candidate.RecordedAt = time.UnixMilli(recordedMS).UTC()
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate jobs awaiting source audio rebuild: %w", err)
	}
	return candidates, nil
}

// CountSourceAudioRebuilds reports how many jobs are owed a rebuild and how
// many rebuilds this installation has run, for /operator/status.
func (s *Store) CountSourceAudioRebuilds(ctx context.Context) (pending int, run int, err error) {
	if err := s.db.QueryRowContext(ctx, `
SELECT
  (SELECT COUNT(*) FROM jobs
     WHERE source_audio_upload_seq > source_audio_built_seq
       AND state IN ('succeeded', 'interrupted')
       AND source_audio_rebuild_count < ?),
  (SELECT COALESCE(SUM(source_audio_rebuild_count), 0) FROM jobs)`,
		maxSourceAudioRebuilds).Scan(&pending, &run); err != nil {
		return 0, 0, fmt.Errorf("count source audio rebuilds: %w", err)
	}
	return pending, run, nil
}

// SourceAudioRebuildState is what a job's row says about late uploads. It rides
// on the job JSON (`source_audio_rebuild`) so an administrator, and eventually
// the control panel, can see that a meeting is queued to be transcribed again
// rather than having to infer it from a second attempt appearing.
type SourceAudioRebuildState struct {
	// UploadSeq counts uploads attributed to this recording; BuiltSeq is how
	// many of them a successful build has read. A gap is audio owed.
	UploadSeq int64 `json:"upload_seq"`
	BuiltSeq  int64 `json:"built_seq"`
	// Pending says a rebuild is owed AND still allowed: the gap is real and the
	// per-job ceiling is not spent. A job with a gap it can no longer act on
	// reports false, which is the honest answer to "will this be rebuilt".
	Pending      bool   `json:"pending"`
	RebuildCount int    `json:"rebuild_count"`
	LastUploadAt string `json:"last_upload_at,omitempty"`
}

// sourceAudioRebuildStateFrom assembles the reported state from the scanned
// columns. Pending mirrors the dispatcher's own admission rules on the two
// facts the row can answer; the rest of them (the quiet period, the retention
// bound, whether the capture is still on disk) are answered at dispatch time
// and would be a lie to precompute here.
func sourceAudioRebuildStateFrom(uploadSeq, builtSeq int64, rebuildCount int, lastUploadAt string) SourceAudioRebuildState {
	return SourceAudioRebuildState{
		UploadSeq:    uploadSeq,
		BuiltSeq:     builtSeq,
		Pending:      uploadSeq > builtSeq && rebuildCount < maxSourceAudioRebuilds,
		RebuildCount: rebuildCount,
		LastUploadAt: strings.TrimSpace(lastUploadAt),
	}
}
