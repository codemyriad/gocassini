package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Who a published meeting could not be shared with (D-885).
//
// Publication is best-effort around one person: the recording's starter must
// receive it, or the publish fails. Anyone else Nextcloud refuses — a user
// outside the groups a sharing policy allows, a group Cassini is not in — is
// skipped and recorded here, so the run says who is missing instead of the
// meeting silently not appearing for them.
//
// Kept per job, not per attempt: shares belong to the delivered file, which a
// rerun keeps and does not re-share (audience edits stick). An attempt-level
// record would lose the warning on the next rerun while the people it names
// still have no access.

const (
	shareOutcomeComplete       = "complete"
	shareOutcomePartial        = "partial"
	shareOutcomeStarterRefused = "starter_refused"
)

// skippedShare is one audience member Nextcloud refused a share for.
type skippedShare struct {
	// Type is user, group or circle (a Nextcloud Team), as the audience names it.
	Type string `json:"type"`
	ID   string `json:"id"`
	// Reason is Nextcloud's own refusal, e.g. "Sharing is only allowed with
	// group members".
	Reason string `json:"reason,omitempty"`
}

type jobShareOutcome struct {
	Status    string         `json:"status"`
	Skipped   []skippedShare `json:"skipped,omitempty"`
	UpdatedAt string         `json:"updated_at,omitempty"`
	// AudienceCount is how many recipients the publish shared with: the
	// recording's starter plus the room audience captured for it, the same
	// list a re-share re-applies. Absent when no audience was captured.
	AudienceCount int `json:"audience_count,omitempty"`
}

// capturedAudience is the list a publish shared with, from the job row
// alone: the starter in its Talk binding, then the captured room audience,
// without the recordings owner and without repeats — as
// reconcileRecordingShares walks them. captured is false when no audience was
// captured, because the publish then resolved the room live and that list is
// not kept.
func capturedAudience(job Job) (audience []sharePrincipal, captured bool) {
	if job.RoomAudienceAt == nil || strings.TrimSpace(*job.RoomAudienceAt) == "" || job.RoomAudience == nil {
		return nil, false
	}
	roster, err := decodeRoomAudience(*job.RoomAudience)
	if err != nil {
		return nil, false
	}
	seen := map[string]bool{}
	add := func(kind, id string) {
		kind, id = strings.TrimSpace(kind), strings.TrimSpace(id)
		if id == "" || (kind == "user" && id == ncRecordingsOwner) || seen[kind+"\x00"+id] {
			return
		}
		seen[kind+"\x00"+id] = true
		audience = append(audience, sharePrincipal{Type: kind, ID: id})
	}
	if job.TalkBinding != nil {
		if state, err := decodeTalkBinding(*job.TalkBinding); err == nil {
			add("user", state.Owner)
		}
	}
	for _, principal := range roster {
		add(principal.Type, principal.ID)
	}
	return audience, true
}

// capturedAudienceCount is how many recipients the publish used, for the run
// detail. Zero when no audience was captured.
func capturedAudienceCount(job Job) int {
	audience, _ := capturedAudience(job)
	return len(audience)
}

const maxShareRefusalReason = 200

func shareRefusalReason(err error) string {
	var refusal *ncShareError
	reason := ""
	if errors.As(err, &refusal) {
		reason = strings.TrimSpace(oneLineListField(refusal.Message))
		if reason == "" {
			reason = fmt.Sprintf("refused (OCS %d)", refusal.OCSStatus)
		}
	} else if err != nil {
		reason = err.Error()
	}
	if len(reason) > maxShareRefusalReason {
		reason = reason[:maxShareRefusalReason] + "…"
	}
	return reason
}

// RecordJobShareOutcome replaces what the job's last share reconciliation
// found. Each reconciliation sees the whole audience, so the newest one is
// the whole truth.
func (s *Store) RecordJobShareOutcome(ctx context.Context, jobID, status string, skipped []skippedShare) error {
	if skipped == nil {
		skipped = []skippedShare{}
	}
	raw, err := json.Marshal(skipped)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO job_share_outcomes (job_id, status, skipped_count, skipped, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(job_id) DO UPDATE SET
  status = excluded.status, skipped_count = excluded.skipped_count,
  skipped = excluded.skipped, updated_at = excluded.updated_at`,
		jobID, status, len(skipped), string(raw), nowUTCString()); err != nil {
		return fmt.Errorf("record share outcome: %w", err)
	}
	return nil
}

// ClearJobShareOutcome forgets the job's last reconciliation, before a new one
// replaces it.
func (s *Store) ClearJobShareOutcome(ctx context.Context, jobID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM job_share_outcomes WHERE job_id = ?`, jobID); err != nil {
		return fmt.Errorf("clear share outcome: %w", err)
	}
	return nil
}

// JobShareOutcome is the job's last share reconciliation, or nil when none was
// recorded (published before D-885, or never published).
func (s *Store) JobShareOutcome(ctx context.Context, jobID string) (*jobShareOutcome, error) {
	var out jobShareOutcome
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT status, skipped, updated_at FROM job_share_outcomes WHERE job_id = ?`, jobID).
		Scan(&out.Status, &raw, &out.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read share outcome: %w", err)
	}
	if err := json.Unmarshal([]byte(raw), &out.Skipped); err != nil {
		return nil, fmt.Errorf("decode share outcome: %w", err)
	}
	return &out, nil
}

// retiredMeetingJobs lists the jobs whose meeting is retiring or retired, for
// any reason — expired or deleted.
func (s *Store) retiredMeetingJobs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM meeting_lifecycle WHERE state IN ('retiring','retired')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	retired := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		retired[meetingStem(name)] = true
	}
	return retired, rows.Err()
}

func (s *Store) meetingRetired(ctx context.Context, jobID string) bool {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM meeting_lifecycle WHERE name IN (?,?) AND state IN ('retiring','retired')`, jobID+".opus", jobID+".json").Scan(&n)
	return err == nil && n > 0
}

// partiallySharedJobs counts, per published job, the audience members it was
// not shared with — for the run list's flag.
func (s *Store) partiallySharedJobs(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT job_id, skipped_count FROM job_share_outcomes WHERE status = ?`, shareOutcomePartial)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// jobSharing reads the job's share outcome for a response, and sets the run
// list's count on the job from it. A read that fails leaves both unset: the
// outcome explains a run, it never stops one being shown.
//
// A refused starter explains a failure only while it is still the failure: a
// later attempt that failed for another reason, or succeeded, is its own
// story. A meeting that was deleted or expired has nobody left to share with.
func (rt *Runtime) jobSharing(ctx context.Context, job *Job) *jobShareOutcome {
	if job.MeetingDeleted || rt.store.meetingRetired(ctx, job.ID) {
		return nil
	}
	outcome, err := rt.store.JobShareOutcome(ctx, job.ID)
	if err != nil {
		rt.logger.Printf("read share outcome job=%s: %v", job.ID, err)
		return nil
	}
	if outcome != nil && outcome.Status == shareOutcomeStarterRefused &&
		(job.State != "failed" || job.Error == nil || !strings.Contains(*job.Error, starterRefusedMarker)) {
		return nil
	}
	if outcome != nil && outcome.Status == shareOutcomePartial {
		job.UnsharedCount = len(outcome.Skipped)
	}
	if outcome != nil {
		outcome.AudienceCount = capturedAudienceCount(*job)
	}
	return outcome
}

// refusedShareCounts is how many published meetings Nextcloud would not share
// with this caller: refused counts partial outcomes that left the caller
// without the recording, as refusedToCaller judges it; refusedStarted names
// the jobs of recordings the caller started that were not published because
// the caller was refused — while that is still why the run failed, as
// jobSharing judges it, and unless the caller dismissed that notice. Retired
// meetings are not counted: nobody has them.
//
// Teams are not matched. Whether the caller is in a Team takes the Teams
// app's own API, which is not worth a call on every page open; a meeting
// refused only to a Team the caller is in is not counted here.
func (s *Store) refusedShareCounts(ctx context.Context, caller string, groups []string, retired map[string]bool) (refused int, refusedStarted []string, err error) {
	dismissed, err := s.dismissedShareNotices(ctx, caller)
	if err != nil {
		return 0, nil, err
	}
	inGroup := map[string]bool{}
	for _, group := range groups {
		inGroup[group] = true
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT o.job_id, o.status, o.skipped, COALESCE(j.state, ''), COALESCE(j.error, ''),
  j.talk_binding, j.room_audience, j.room_audience_at
FROM job_share_outcomes o LEFT JOIN jobs j ON j.id = o.job_id
WHERE o.status IN (?, ?)
ORDER BY o.job_id DESC`, shareOutcomePartial, shareOutcomeStarterRefused)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var jobID, status, raw, state, jobErr string
		var binding, audience, audienceAt sql.NullString
		if err := rows.Scan(&jobID, &status, &raw, &state, &jobErr, &binding, &audience, &audienceAt); err != nil {
			return 0, nil, err
		}
		if retired[jobID] {
			continue
		}
		var skipped []skippedShare
		if err := json.Unmarshal([]byte(raw), &skipped); err != nil {
			continue
		}
		switch status {
		case shareOutcomePartial:
			job := Job{TalkBinding: nullableStringPtr(binding), RoomAudience: nullableStringPtr(audience), RoomAudienceAt: nullableStringPtr(audienceAt)}
			if refusedToCaller(skipped, caller, inGroup, job) {
				refused++
			}
		case shareOutcomeStarterRefused:
			if state == "failed" && strings.Contains(jobErr, starterRefusedMarker) &&
				len(skipped) > 0 && skipped[0].Type == "user" && skipped[0].ID == caller && !dismissed[jobID] {
				refusedStarted = append(refusedStarted, jobID)
			}
		}
	}
	return refused, refusedStarted, rows.Err()
}

// refusedToCaller says whether a partial outcome left the caller without the
// recording: Nextcloud refused them by name, or refused a group they are in
// and no share of their own reached them. Someone in a refused group who was
// in the room themselves holds their own user share, so the group's refusal
// costs them nothing — with "Allow sharing with groups" off, every group in
// every room is refused, and counting those would tell each person in the
// room that a recording they can open could not be shared with them.
//
// Whether their own share reached them is read from the audience the publish
// used (the starter and the captured room, the list a re-share re-applies):
// the caller named there as a user, and not among the skipped, was shared
// with by name. When no audience was captured that cannot be known, and the
// refusal counts, as it does for someone the group alone reached.
func refusedToCaller(skipped []skippedShare, caller string, inGroup map[string]bool, job Job) bool {
	viaGroup := false
	for _, skip := range skipped {
		if skip.Type == "user" && skip.ID == caller {
			return true
		}
		if skip.Type == "group" && inGroup[skip.ID] {
			viaGroup = true
		}
	}
	if !viaGroup {
		return false
	}
	audience, captured := capturedAudience(job)
	if !captured {
		return true
	}
	for _, principal := range audience {
		if principal.Type == "user" && principal.ID == caller {
			return false
		}
	}
	return true
}

// dismissedShareNotices is the jobs whose Browse notice this person dismissed.
func (s *Store) dismissedShareNotices(ctx context.Context, user string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT job_id FROM share_notice_dismissals WHERE user_id = ?`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// DismissShareNotices records that this person dismissed the Browse notices
// of these jobs, for good. Ids of jobs the operator does not know are left
// out: there is no notice to dismiss.
func (s *Store) DismissShareNotices(ctx context.Context, user string, jobIDs []string) error {
	at := nowUTCString()
	for _, id := range jobIDs {
		if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO share_notice_dismissals (user_id, job_id, dismissed_at)
SELECT ?, id, ? FROM jobs WHERE id = ?`, user, at, id); err != nil {
			return err
		}
	}
	return nil
}
