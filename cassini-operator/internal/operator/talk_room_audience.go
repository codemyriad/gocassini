package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Capturing a recording's audience while the meeting is happening (D-769).
//
// Everything else in this codebase asks the audience question too late. The
// publish path resolves it (resolveRecordingAudience) and throws the answer
// away once the PROPPATCH lands, and in the default storage model it never asks
// at all, because there are no rules to write there. So a recording that later
// turns out to need an audience — every recording a default -> access-controlled
// migration carries into the Team folder, readable by everyone — has nothing on
// the instance that can say who it belonged to:
//
//	the Talk room   answers who is in it NOW. A room gains and loses members;
//	                asking it months later is a different question wearing the
//	                same words.
//	the .opus       records who SPOKE. buildSpeakerEntries walks transcript
//	                segments, so a participant who sat through the call in
//	                silence is not in the file at all, and neither is a group.
//
// Neither is the audience. The audience is who had access to the room while the
// recording was being made, and the only moment that is cheaply and exactly
// knowable is while it is being made. So this file writes it down then.
//
// The result is frozen by construction: later room churn cannot reach a column
// nothing rewrites, which is the property the whole feature rests on — a room
// whose membership changed must not change who may open a recording made before
// the change.

// roomAudienceTimeout bounds one capture. Generous relative to the work because
// nothing waits on it: both callers run it in their own goroutine, and the value
// is not read until an administrator opens the settings panel.
const roomAudienceTimeout = 60 * time.Second

// Capture phases, for the log line. They are not stored: the column records the
// union of both, and which half contributed a principal is not a question
// anything asks later.
const (
	roomAudiencePhaseStart = "start"
	roomAudiencePhaseStop  = "stop"
)

// storedAudiencePrincipal is the on-disk shape of one grantable principal.
//
// Spelled out here rather than marshalling aclMapping directly, because this is
// a persisted format: aclMapping is an in-memory argument to the ACL builder and
// may grow a field or rename one without anybody thinking about the rows already
// written. A column read back by a later release is a contract, so it gets a
// type whose only job is to be that contract.
type storedAudiencePrincipal struct {
	// Type is the groupfolders mapping type: "user", "group" or "circle".
	Type string `json:"type"`
	ID   string `json:"id"`
}

// encodeRoomAudience renders principals for storage, canonically.
//
// Canonical means sorted and deduplicated, and that is load-bearing twice over:
// the union of two captures must not depend on which arrived first, and D-769's
// apply step hashes this value so the panel and the write can agree on what was
// shown. Both want the same principals to produce the same bytes.
func encodeRoomAudience(mappings []aclMapping) (string, error) {
	stored := make([]storedAudiencePrincipal, 0, len(mappings))
	seen := make(map[string]bool, len(mappings))
	for _, mapping := range mappings {
		mappingType := strings.TrimSpace(mapping.Type)
		id := strings.TrimSpace(mapping.ID)
		if mappingType == "" || id == "" {
			continue
		}
		key := mappingType + "\x00" + id
		if seen[key] {
			continue
		}
		seen[key] = true
		stored = append(stored, storedAudiencePrincipal{Type: mappingType, ID: id})
	}
	sort.Slice(stored, func(i, j int) bool {
		if stored[i].Type != stored[j].Type {
			return stored[i].Type < stored[j].Type
		}
		return stored[i].ID < stored[j].ID
	})
	raw, err := json.Marshal(stored)
	if err != nil {
		return "", fmt.Errorf("encode room audience: %w", err)
	}
	return string(raw), nil
}

// decodeRoomAudience reads a stored roster back.
//
// An empty or blank column is an empty roster with no error: "captured, and
// nobody in that room could be granted" is a real answer, and the caller tells
// it apart from "never captured" by the timestamp column, not by this.
func decodeRoomAudience(raw string) ([]aclMapping, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	var stored []storedAudiencePrincipal
	if err := json.Unmarshal([]byte(trimmed), &stored); err != nil {
		return nil, fmt.Errorf("decode room audience: %w", err)
	}
	mappings := make([]aclMapping, 0, len(stored))
	for _, principal := range stored {
		mappingType := strings.TrimSpace(principal.Type)
		id := strings.TrimSpace(principal.ID)
		if mappingType == "" || id == "" {
			continue
		}
		mappings = append(mappings, aclMapping{Type: mappingType, ID: id})
	}
	return mappings, nil
}

// MergeJobRoomAudience folds one capture into the job's stored roster.
//
// A merge rather than a write, because the roster is taken twice — once when
// the recording starts and once when it stops — and each end sees something the
// other cannot. Start alone misses somebody invited half way through; stop alone
// misses somebody removed before the end, who was nonetheless in the meeting.
// Union is the only combination that loses neither, and it is idempotent, so a
// repeated capture costs a row update and changes nothing.
//
// room_audience_at is set by the FIRST capture and never moved. It marks that a
// capture happened at all, which is the question every reader actually asks: a
// null there means nothing ever looked, and no later pass can fill it in,
// because the room it would have to ask has moved on.
func (s *Store) MergeJobRoomAudience(ctx context.Context, id string, mappings []aclMapping, capturedAt string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin room audience merge: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var existing, existingAt sql.NullString
	if err := tx.QueryRowContext(ctx, `
SELECT room_audience, room_audience_at FROM jobs WHERE id = ?`, id).Scan(&existing, &existingAt); err != nil {
		return fmt.Errorf("read room audience: %w", err)
	}

	merged := mappings
	if existing.Valid {
		previous, decodeErr := decodeRoomAudience(existing.String)
		if decodeErr != nil {
			// A column this cannot read is a column a previous release (or a
			// hand edit) left in a shape we do not recognise. Replacing it with
			// what we just measured is better than failing the capture and
			// keeping something unusable.
			previous = nil
		}
		merged = append(append([]aclMapping{}, previous...), mappings...)
	}
	encoded, err := encodeRoomAudience(merged)
	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE jobs
SET room_audience = ?,
    room_audience_at = COALESCE(room_audience_at, ?),
    updated_at = ?
WHERE id = ?`, encoded, capturedAt, nowUTCString(), id); err != nil {
		return fmt.Errorf("update room audience: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit room audience merge: %w", err)
	}
	return nil
}

// JobRoomAudience reads a job's captured roster.
//
// `captured` is the whole point of the second return value: it is false when
// nothing ever looked, and true even when the roster is empty. An empty roster
// means the room held nobody grantable — every attendee a guest, an email
// invitee or federated — which is a recording that must be left alone, not one
// to narrow to nobody.
func (s *Store) JobRoomAudience(ctx context.Context, id string) (mappings []aclMapping, captured bool, err error) {
	var audience, audienceAt sql.NullString
	if err := s.db.QueryRowContext(ctx, `
SELECT room_audience, room_audience_at FROM jobs WHERE id = ?`, id).Scan(&audience, &audienceAt); err != nil {
		return nil, false, fmt.Errorf("read room audience: %w", err)
	}
	if !audienceAt.Valid || strings.TrimSpace(audienceAt.String) == "" {
		return nil, false, nil
	}
	decoded, err := decodeRoomAudience(audience.String)
	if err != nil {
		return nil, true, err
	}
	return decoded, true, nil
}

// captureRoomAudience resolves who had access to the room and records it.
//
// Best-effort and entirely off the critical path: it runs in its own goroutine
// at both ends, and a failure costs a roster rather than a recording. That is
// the right trade at the moment it runs — the recording is the thing the user
// asked for — but it is worth saying what a permanent failure costs, because
// nothing repairs it: that recording can never be narrowed from the panel, and
// will sit there as a row explaining why.
//
// It reuses resolveRecordingAudience rather than calling the fetcher directly,
// so a capture escalates through the same two tiers a publish does: the
// recording's starter first, then the recordings owner. The tiers matter more
// here than there, because at stop time the starter may already have left.
func (rt *Runtime) captureRoomAudience(jobID, owner, roomToken, phase string) {
	if rt.fetchTalkParticipants == nil {
		return
	}
	owner = strings.TrimSpace(owner)
	roomToken = strings.TrimSpace(roomToken)
	if owner == "" || roomToken == "" {
		return
	}
	ctx, cancel := context.WithTimeout(rt.ctx, roomAudienceTimeout)
	defer cancel()

	mappings, source, err := rt.resolveRecordingAudience(ctx, jobID, owner, roomToken)
	if err != nil {
		rt.logger.Printf("room audience capture failed id=%s phase=%s room=%s: %v (this recording cannot be narrowed later)", jobID, phase, roomToken, err)
		return
	}
	if err := rt.store.MergeJobRoomAudience(context.Background(), jobID, mappings, nowUTCString()); err != nil {
		rt.logger.Printf("room audience persist failed id=%s phase=%s: %v", jobID, phase, err)
		return
	}
	rt.logger.Printf("room audience captured id=%s phase=%s principals=%d source=%s", jobID, phase, len(mappings), source)
}
