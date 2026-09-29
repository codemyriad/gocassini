package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Enabled only after the installed compatibility and complete lifecycle gates.
const remoteRetentionImplemented = false
const nextcloudHistoryNotice = "Nextcloud manages previous versions and Deleted files. Logical active-file bytes do not measure physical disk reclamation."

type remoteRetentionPreview struct {
	Now           string                  `json:"now"`
	Revision      int                     `json:"revision"`
	Capability    bool                    `json:"capability"`
	Reason        string                  `json:"reason,omitempty"`
	HistoryNotice string                  `json:"historyNotice"`
	Convert       int                     `json:"convert"`
	Retire        int                     `json:"retire"`
	Meetings      []remoteRetentionEffect `json:"meetings"`
}
type remoteRetentionEffect struct {
	Name                  string `json:"name"`
	Action                string `json:"action"`
	AudioDeadline         string `json:"audioDeadline,omitempty"`
	TranscriptionDeadline string `json:"transcriptionDeadline,omitempty"`
	Reason                string `json:"reason,omitempty"`
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
	if d := p.Recordings.deadline(anchor); !d.IsZero() {
		effect.AudioDeadline = d.Format("2006-01-02")
	}
	if d := p.Transcriptions.deadline(anchor); !d.IsZero() {
		effect.TranscriptionDeadline = d.Format("2006-01-02")
	}
	if p.Transcriptions.due(anchor, now) {
		effect.Action = "retire"
	} else if m.Representation == "opus" && p.Recordings.due(anchor, now) {
		effect.Action = "convert"
	}
	return effect
}
func (s *Store) retainedMeetings(ctx context.Context) ([]meetingLifecycle, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name,file_id,document_path,representation,state,age_anchor,anchor_source,document_id FROM meeting_lifecycle ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []meetingLifecycle{}
	for rows.Next() {
		var m meetingLifecycle
		if err := rows.Scan(&m.Name, &m.FileID, &m.Path, &m.Representation, &m.State, &m.Anchor, &m.AnchorSource, &m.DocumentID); err != nil {
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
	meetings, err := rt.store.retainedMeetings(r.Context())
	if err != nil {
		writeJSONError(w, 500, "could not read managed meeting inventory")
		return
	}
	for _, m := range meetings {
		effect := evaluateRemoteRetention(m, proposed.Nextcloud, now)
		result.Meetings = append(result.Meetings, effect)
		if effect.Action == "convert" {
			result.Convert++
		}
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
