package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"cassini-operator/internal/operator/appapi"
)

var errRoomNameConflict = errors.New("room name changed")

func validateRoomCustomName(name *string) (*string, error) {
	if name == nil {
		return nil, nil
	}
	if !utf8.ValidString(*name) || strings.IndexFunc(*name, unicode.IsControl) >= 0 {
		return nil, errors.New("room name must be valid text without control characters")
	}
	value := strings.TrimSpace(*name)
	if value == "" || utf8.RuneCountInString(value) > 200 {
		return nil, errors.New("room name must contain 1 to 200 characters")
	}
	return &value, nil
}

func (s *meetingMetadataStore) saveRoomName(ctx context.Context, id string, name *string, revision int64, caller string) (roomNameState, error) {
	value, err := validateRoomCustomName(name)
	if err != nil {
		return roomNameState{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return roomNameState{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE room_names SET custom_name=?, revision=revision+1,updated_at=?,updated_by=? WHERE room_id=? AND revision=?`, value, nowUTCString(), caller, id, revision)
	if err != nil {
		return roomNameState{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return roomNameState{}, err
	}
	var state roomNameState
	var custom sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT room_id,default_name,custom_name,revision FROM room_names WHERE room_id=?`, id).Scan(&state.RoomID, &state.DefaultName, &custom, &state.Revision)
	if err != nil {
		return roomNameState{}, err
	}
	state.DisplayName = state.DefaultName
	if custom.Valid {
		state.CustomName = &custom.String
		state.DisplayName = custom.String
	}
	if state.DisplayName == "" {
		state.DisplayName = id
	}
	if count == 0 {
		return state, errRoomNameConflict
	}
	if err := tx.Commit(); err != nil {
		return roomNameState{}, err
	}
	return state, nil
}

// The root mux is behind AppAPI authentication; Files shares, checked freshly
// below, authorize both reads and writes. A lookup row never grants access.
func (c ExAppConfig) roomNameHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	rest := strings.TrimPrefix(r.URL.Path, "/rooms/")
	id, action, ok := strings.Cut(strings.TrimSuffix(rest, "/"), "/")
	if !ok || action != "name" || id == "" || len(id) > 256 || strings.IndexFunc(id, unicode.IsControl) >= 0 {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeMethodNotAllowed(w, "GET, POST")
		return
	}
	caller := appapi.UserID(r.Context())
	if caller == "" {
		writeJSONError(w, 503, "verified caller identity is unavailable")
		return
	}
	if c.meetingMetadata == nil {
		writeJSONError(w, 503, "room names are unavailable")
		return
	}
	snapshot, err := c.directShareSnapshot(r.Context(), &http.Client{Timeout: ncProvisionTimeout}, caller, c.meetingMetadata)
	if err != nil {
		writeJSONError(w, 503, "could not determine which recordings you may read")
		return
	}
	visible := false
	for _, raw := range snapshot.entries {
		var e roomEntry
		if json.Unmarshal(raw, &e) == nil && e.RoomID == id {
			visible = true
			break
		}
	}
	if !visible {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet {
		names, err := c.meetingMetadata.roomNames(r.Context(), []string{id})
		if err != nil {
			writeJSONError(w, 503, "room names are unavailable")
			return
		}
		state, ok := names[id]
		if !ok {
			writeJSONError(w, 503, "room name has not been registered yet")
			return
		}
		writeJSON(w, 200, state)
		return
	}
	var request struct {
		CustomName     json.RawMessage `json:"customName"`
		ExpectRevision *int64          `json:"expectRevision"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(new(any)) != io.EOF || request.ExpectRevision == nil || *request.ExpectRevision < 1 || len(request.CustomName) == 0 {
		writeJSONError(w, 400, "send customName (text or null) and expectRevision")
		return
	}
	var name *string
	if err := json.Unmarshal(request.CustomName, &name); err != nil {
		writeJSONError(w, 400, "customName must be text or null")
		return
	}
	name, err = validateRoomCustomName(name)
	if err != nil {
		writeJSONError(w, 400, err.Error())
		return
	}
	state, err := c.meetingMetadata.saveRoomName(r.Context(), id, name, *request.ExpectRevision, caller)
	if errors.Is(err, errRoomNameConflict) {
		writeJSON(w, 409, map[string]any{"error": "This room was renamed while you were editing. Review the saved name and try again.", "current": state})
		return
	}
	if err != nil {
		writeJSONError(w, 503, "the room name could not be saved")
		return
	}
	writeJSON(w, 200, state)
}
