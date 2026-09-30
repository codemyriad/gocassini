package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Remote execution also requires the runtime capability checks.
const remoteRetentionImplemented = true
const nextcloudHistoryNotice = "Nextcloud manages previous versions and Deleted files. Logical active-file bytes do not measure physical disk reclamation."

type retentionLogicalUsage struct {
	Count int   `json:"count"`
	Bytes int64 `json:"bytes"`
}

type remoteRetentionPreview struct {
	Usage         retentionLogicalUsage   `json:"usage"`
	Now           string                  `json:"now"`
	Revision      int                     `json:"revision"`
	Capability    bool                    `json:"capability"`
	Reason        string                  `json:"reason,omitempty"`
	HistoryNotice string                  `json:"historyNotice"`
	Retire        int                     `json:"retire"`
	Meetings      []remoteRetentionEffect `json:"meetings"`
}
type remoteRetentionEffect struct {
	Bytes    *int64 `json:"bytes,omitempty"`
	Name     string `json:"name"`
	Action   string `json:"action"`
	Deadline string `json:"deadline,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

func evaluateRemoteRetention(m meetingLifecycle, p nextcloudRetentionSettings, now time.Time) remoteRetentionEffect {
	effect := remoteRetentionEffect{Name: m.Name, Action: "keep"}
	anchor, err := time.Parse(time.RFC3339Nano, m.Anchor)
	if err != nil || m.AnchorSource == "" {
		effect.Action = "skip"
		effect.Reason = "original age is unknown"
		return effect
	}
	if m.State == "retired" || m.State == "retiring" {
		effect.Action = m.State
		return effect
	}
	if d := p.Meetings.deadline(anchor); !d.IsZero() {
		effect.Deadline = d.Format("2006-01-02")
	}
	if p.Meetings.due(anchor, now) {
		effect.Action = "retire"
	}
	return effect
}
func (s *Store) retainedMeetings(ctx context.Context) ([]meetingLifecycle, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name,file_id,document_path,state,age_anchor,anchor_source FROM meeting_lifecycle ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []meetingLifecycle{}
	for rows.Next() {
		var m meetingLifecycle
		if err := rows.Scan(&m.Name, &m.FileID, &m.Path, &m.State, &m.Anchor, &m.AnchorSource); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}
func (rt *Runtime) remoteRetentionPreviewHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, 405, "method not allowed")
		return
	}
	var proposed retentionSettings
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&proposed); err != nil {
		writeJSONError(w, 400, "invalid retention settings")
		return
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		writeJSONError(w, 400, "trailing settings data")
		return
	}
	if err := proposed.validate(); err != nil {
		writeJSONError(w, 400, err.Error())
		return
	}
	if rt.retention == nil || rt.store == nil {
		writeJSONError(w, 503, "retention unavailable")
		return
	}
	rt.retention.mu.Lock()
	revision := rt.retention.settings.Revision
	rt.retention.mu.Unlock()
	if proposed.Revision != revision {
		writeJSONError(w, 412, "Settings changed; reload before previewing")
		return
	}
	now := time.Now().UTC()
	result := remoteRetentionPreview{Now: now.Format(time.RFC3339), Revision: revision, Capability: remoteRetentionImplemented, HistoryNotice: nextcloudHistoryNotice, Meetings: []remoteRetentionEffect{}}
	if !result.Capability {
		result.Reason = "Remote lifecycle and storage compatibility certification is not complete; remote expiry is unavailable."
	}
	rt.remoteRetentionMu.RLock()
	remote := rt.remoteRetention
	rt.remoteRetentionMu.RUnlock()
	if remote == nil {
		result.Capability = false
		result.Reason = "Remote retention is unavailable."
	}
	meetings, err := rt.store.retainedMeetings(r.Context())
	if remote != nil {
		if e := remote.remoteRetentionCapability(r.Context()); e != nil {
			result.Capability = false
			result.Reason = e.Error()
		}
		var skipped []remoteRetentionEffect
		meetings, skipped, err = remote.retentionInventory(r.Context())
		result.Meetings = append(result.Meetings, skipped...)
	}
	if err != nil {
		writeJSONError(w, 500, "could not read managed meeting inventory")
		return
	}
	for _, m := range meetings {
		effect := evaluateRemoteRetention(m, proposed.Nextcloud, now)
		if remote != nil && m.State != "retired" && m.State != "retiring" {
			state, e := remote.exapp.davRetentionLeaf(r.Context(), remote.client, m.Path)
			if e != nil || !state.Exists || state.FileID != m.FileID {
				effect.Action = "skip"
				effect.Reason = "Current file identity or location could not be verified."
			} else {
				effect.Bytes = &state.Size
				usage := &result.Usage
				usage.Count++
				usage.Bytes += state.Size
			}
			if job, e := rt.store.GetJob(r.Context(), strings.TrimSuffix(m.Name, ".opus")); e == nil && job.Stage != "done" {
				effect.Action = "skip"
				effect.Reason = "Meeting is busy."
			}
		}
		result.Meetings = append(result.Meetings, effect)
		if effect.Action == "retire" {
			result.Retire++
		}
	}
	writeJSON(w, 200, result)
}
func (rt *Runtime) remoteRetentionOperationsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, 405, "method not allowed")
		return
	}
	if rt.store == nil {
		writeJSONError(w, 503, "operations unavailable")
		return
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		var err error
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 {
			writeJSONError(w, 400, "invalid offset")
			return
		}
	}
	rows, err := rt.store.db.QueryContext(r.Context(), `SELECT name,status,last_error,updated_at FROM remote_retention_operation ORDER BY updated_at DESC,name LIMIT 100 OFFSET ?`, offset)
	if err != nil {
		writeJSONError(w, 500, "operations unavailable")
		return
	}
	defer rows.Close()
	results := []map[string]string{}
	for rows.Next() {
		var name, status, message, updated string
		if err := rows.Scan(&name, &status, &message, &updated); err != nil {
			writeJSONError(w, 500, "operations unavailable")
			return
		}
		results = append(results, map[string]string{"name": name, "status": status, "error": message, "updatedAt": updated})
	}
	if err := rows.Err(); err != nil {
		writeJSONError(w, 500, "operations unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"operations": results, "offset": offset, "nextOffset": offset + len(results), "historyNotice": nextcloudHistoryNotice})
}

func remoteRetentionBlockedError() error {
	return fmt.Errorf("remote retention compatibility is not certified")
}
