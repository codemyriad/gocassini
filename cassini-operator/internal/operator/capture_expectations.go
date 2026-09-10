package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"cassini-operator/internal/operator/appapi"
)

const captureArrivalGrace = 2 * time.Minute

type captureExpectation struct {
	CaptureID   string `json:"capture_id,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	Owner       string `json:"owner"`
	CallStartMS int64  `json:"call_start_ms"`
	CallEndMS   int64  `json:"call_end_ms"`
	Status      string `json:"status"`
	UpdatedAt   string `json:"updated_at"`
}

// Announcements never prove that audio exists on the server. Only a complete
// stored upload satisfies one. Identity and recording assignment are server-owned.
func (rt *Runtime) captureRegisterHandler(isMember roomMembershipChecker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", 405)
			return
		}
		if !sourceCaptureEnabled() {
			http.Error(w, "collection disabled", 403)
			return
		}
		owner := strings.TrimSpace(appapi.UserID(r.Context()))
		if owner == "" {
			http.Error(w, "authentication required", 401)
			return
		}
		var input struct {
			CaptureID   string `json:"captureId"`
			SessionID   string `json:"sessionId"`
			RoomToken   string `json:"roomToken"`
			CallStartMS int64  `json:"callStartWallMs"`
			CallEndMS   int64  `json:"callEndWallMs"`
			Status      string `json:"status"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if json.NewDecoder(r.Body).Decode(&input) != nil || (input.CaptureID != "" && !captureSafeName.MatchString(input.CaptureID)) || len(input.SessionID) > 512 || strings.ContainsAny(input.SessionID, "\x00\r\n\t") || !captureSafeName.MatchString(input.RoomToken) || input.CallStartMS <= 0 || input.CallEndMS < input.CallStartMS || input.CallEndMS > time.Now().Add(5*time.Minute).UnixMilli() || (input.Status != "recording" && input.Status != "uploading") {
			http.Error(w, "invalid capture announcement", 400)
			return
		}
		if isMember != nil {
			member, err := isMember(r.Context(), owner, input.RoomToken)
			if err != nil {
				http.Error(w, "could not verify room membership", 502)
				return
			}
			if !member {
				http.Error(w, "not a participant of this room", 403)
				return
			}
		}
		match, err := rt.store.ResolveJobForCapture(r.Context(), input.RoomToken, input.CallStartMS, input.CallEndMS)
		if err != nil {
			http.Error(w, "no unambiguous recording matches this capture", 409)
			return
		}
		// A retry updates the same session. Out-of-order heartbeats cannot turn an
		// uploading session back into a recording or shorten its known audio span.
		_, err = rt.store.db.ExecContext(r.Context(), `INSERT INTO capture_expectations(job_id,owner,call_start_ms,call_end_ms,status,updated_at,capture_id,session_id)
    VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(job_id,owner,call_start_ms,capture_id) DO UPDATE SET
    call_end_ms=MAX(call_end_ms,excluded.call_end_ms),
    session_id=CASE WHEN excluded.call_end_ms>=call_end_ms THEN excluded.session_id ELSE session_id END,
    status=CASE WHEN status='uploading' THEN status ELSE excluded.status END,
    updated_at=MAX(updated_at,excluded.updated_at)`, match.JobID, owner, input.CallStartMS, input.CallEndMS, input.Status, nowUTCString(), input.CaptureID, input.SessionID)
		if err != nil {
			http.Error(w, "could not register capture", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"job_id": match.JobID, "status": "registered"})
	}
}

func (rt *Runtime) captureExpectationsForJob(ctx context.Context, jobID string, uploads []sourceAudioUpload) ([]captureExpectation, *time.Time, error) {
	result := []captureExpectation{}
	if rt.store == nil {
		return result, nil, nil
	}
	var finished sql.NullString
	if err := rt.store.db.QueryRowContext(ctx, `SELECT record_finished_at FROM jobs WHERE id=?`, jobID).Scan(&finished); err != nil {
		return result, nil, err
	}
	var deadline *time.Time
	if finished.Valid {
		if at, err := time.Parse(time.RFC3339Nano, finished.String); err == nil {
			until := at.Add(captureArrivalGrace)
			deadline = &until
		}
	}
	rows, err := rt.store.db.QueryContext(ctx, `SELECT owner,call_start_ms,call_end_ms,status,updated_at,capture_id,session_id FROM capture_expectations WHERE job_id=? ORDER BY owner,call_start_ms`, jobID)
	if err != nil {
		return result, nil, err
	}
	defer rows.Close()
	pending := false
	for rows.Next() {
		var entry captureExpectation
		if err := rows.Scan(&entry.Owner, &entry.CallStartMS, &entry.CallEndMS, &entry.Status, &entry.UpdatedAt, &entry.CaptureID, &entry.SessionID); err != nil {
			return result, nil, err
		}
		for _, upload := range uploads {
			if upload.Owner == entry.Owner && upload.CaptureID == entry.CaptureID && upload.CallStartMS == entry.CallStartMS && upload.CallEndMS >= entry.CallEndMS && upload.Complete {
				entry.Status = "stored"
				break
			}
		}
		if entry.Status != "stored" {
			pending = true
			if deadline != nil {
				entry.Status = "uploading"
				if !time.Now().Before(*deadline) {
					entry.Status = "timed_out"
				}
			}
		}
		result = append(result, entry)
	}
	if !pending || deadline == nil || !time.Now().Before(*deadline) {
		deadline = nil
	}
	return result, deadline, rows.Err()
}

// Leave the durable job queued; the dispatcher revisits it without occupying
// a build worker or the GPU lock. The deadline is fixed to recording completion,
// so heartbeats, retries and restarts can never extend it.
func (rt *Runtime) waitingForCaptureUploads(jobID string) bool {
	if rt.store == nil || !sourceAudioIngestEnabled() || !sourceCaptureEnabled() {
		return false
	}
	set, err := rt.sourceCaptureSetForJob(rt.ctx, jobID)
	if err != nil {
		return false
	}
	_, deadline, err := rt.captureExpectationsForJob(rt.ctx, jobID, set.Uploads)
	return err == nil && deadline != nil
}
