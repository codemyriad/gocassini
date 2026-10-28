package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

const roomNamesSchema = `
CREATE TABLE IF NOT EXISTS room_names (
 room_id TEXT PRIMARY KEY,
 default_name TEXT NOT NULL DEFAULT '',
 default_source TEXT NOT NULL DEFAULT 'unknown',
 default_observed_at TEXT NOT NULL DEFAULT '',
 custom_name TEXT,
 revision INTEGER NOT NULL DEFAULT 1,
 updated_at TEXT NOT NULL,
 updated_by TEXT
);`

type roomNameState struct {
	RoomID      string  `json:"roomId"`
	DisplayName string  `json:"displayName"`
	DefaultName string  `json:"defaultName"`
	CustomName  *string `json:"customName"`
	Revision    int64   `json:"revision"`
}

type roomEntry struct {
	ID        string `json:"id"`
	JobID     string `json:"jobId"`
	RoomID    string `json:"roomId"`
	RoomName  string `json:"roomName"`
	DateLabel string `json:"dateLabel"`
}

func (s *meetingMetadataStore) registerRoom(ctx context.Context, id, name string) error {
	if s == nil || strings.TrimSpace(id) == "" {
		return nil
	}
	source := "metadata"
	name = sanitizeTalkRoomName(name)
	if name == "" {
		source = "unknown"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO room_names(room_id,default_name,default_source,updated_at)
 VALUES(?,?,?,?) ON CONFLICT(room_id) DO NOTHING`, id, name, source, nowUTCString())
	return err
}

// Seed from the same newest-first inventory for all callers; a viewer's subset
// cannot choose a shared default. Never rewrite the original JSON.
func (s *meetingMetadataStore) registerMetadataRooms(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT entry_json FROM meeting_metadata ORDER BY date_label DESC, file_id DESC`)
	if err != nil {
		return err
	}
	var entries []roomEntry
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		var e roomEntry
		if json.Unmarshal(raw, &e) == nil && e.RoomID != "" {
			entries = append(entries, e)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := s.registerRoom(ctx, e.RoomID, e.RoomName); err != nil {
			return err
		}
	}
	return nil
}

// Stronger complete evidence can enrich a fallback; old reruns and transient
// failures cannot replace a known pair. Neither path writes custom_name.
// An observation that repeats the stored name and source changes nothing a
// viewer can see, so it must not touch the row: revision guards concurrent
// edits, and a background bump would 409 an open rename dialog.
func (s *meetingMetadataStore) observeRoom(ctx context.Context, id, name, source, at string) error {
	if err := s.registerRoom(ctx, id, name); err != nil {
		return err
	}
	if s == nil || id == "" || name == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE room_names SET default_name=?, default_source=?, default_observed_at=?,
 revision=revision+1, updated_at=? WHERE room_id=? AND
 (default_source IN ('unknown','metadata') OR
  (? >= default_observed_at AND (?='participants' OR default_source<>'participants' OR ?='talk-group')))
 AND (default_name<>? OR default_source<>?)`,
		name, source, at, nowUTCString(), id, at, source, source, name, source)
	return err
}

func (s *meetingMetadataStore) roomNames(ctx context.Context, ids []string) (map[string]roomNameState, error) {
	out := map[string]roomNameState{}
	if s == nil {
		return out, nil
	}
	for start := 0; start < len(ids); start += 500 {
		end := min(start+500, len(ids))
		args := []any{}
		marks := []string{}
		for _, id := range ids[start:end] {
			args = append(args, id)
			marks = append(marks, "?")
		}
		rows, err := s.db.QueryContext(ctx, `SELECT room_id,default_name,custom_name,revision FROM room_names WHERE room_id IN (`+strings.Join(marks, ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var r roomNameState
			var custom sql.NullString
			if err := rows.Scan(&r.RoomID, &r.DefaultName, &custom, &r.Revision); err != nil {
				rows.Close()
				return nil, err
			}
			r.DisplayName = r.DefaultName
			if custom.Valid {
				r.CustomName = &custom.String
				r.DisplayName = custom.String
			}
			if r.DisplayName == "" {
				r.DisplayName = r.RoomID
			}
			out[r.RoomID] = r
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *meetingMetadataStore) projectRoomNames(ctx context.Context, entries []json.RawMessage) ([]json.RawMessage, error) {
	ids := []string{}
	for _, raw := range entries {
		var e roomEntry
		if json.Unmarshal(raw, &e) == nil && e.RoomID != "" {
			ids = append(ids, e.RoomID)
		}
	}
	names, err := s.roomNames(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]json.RawMessage, 0, len(entries))
	for _, raw := range entries {
		var e roomEntry
		_ = json.Unmarshal(raw, &e)
		if n, ok := names[e.RoomID]; ok {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				return nil, err
			}
			fields["roomDisplayName"], _ = json.Marshal(n.DisplayName)
			raw, err = json.Marshal(fields)
			if err != nil {
				return nil, err
			}
		}
		out = append(out, raw)
	}
	return out, nil
}
