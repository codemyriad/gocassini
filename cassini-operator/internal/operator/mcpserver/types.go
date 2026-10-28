package mcpserver

// The tools' inputs and results. Each result is the document the matching
// `cassini meetings <verb> --json` prints, field for field, so an agent reads
// the same contract whichever door it came through (docs/agent-meeting-access.md
// is the spec). The JSON schemas the tools advertise are inferred from these
// types, so a field absent here is a field no client is ever promised.

// RoomsResult is `meetings rooms --json`.
type RoomsResult struct {
	Version string `json:"version"`
	Source  string `json:"source"`
	// Skipped counts meetings dropped as unusable, so a short answer can be
	// told from a complete one.
	Skipped int `json:"skipped"`
	// Unattributed counts readable meetings that name no room; they are in
	// none of the rooms and no room filter selects them.
	Unattributed int    `json:"unattributed"`
	Rooms        []Room `json:"rooms"`
}

// Room is one conversation the caller has readable recordings from.
type Room struct {
	// Room is the value list's room filter takes.
	Room            string `json:"room"`
	RoomID          string `json:"roomId,omitempty"`
	RoomName        string `json:"roomName,omitempty"`
	RoomDisplayName string `json:"roomDisplayName,omitempty"`
	Meetings        int    `json:"meetings"`
	Latest          string `json:"latest,omitempty"`
	Earliest        string `json:"earliest,omitempty"`
}

// ListInput narrows list. Every field is optional.
type ListInput struct {
	Room string `json:"room,omitempty" jsonschema:"a room id, as rooms reports it"`
	From string `json:"from,omitempty" jsonschema:"earliest date, inclusive: 2026-08-11, 2026-08-11 14:30 or 2026-08-11 14:30:05; no timezone"`
	To   string `json:"to,omitempty" jsonschema:"latest date, inclusive, in the same forms as from"`
	Tag  string `json:"tag,omitempty" jsonschema:"a tag label (any case) or tag id"`
}

// ListResult is `meetings list --json`: meetings newest first, each entry
// exactly as the archive publishes it.
type ListResult struct {
	Version string `json:"version"`
	Source  string `json:"source"`
	Skipped int    `json:"skipped"`
	// Filter echoes the narrowing the server applied, absent when none.
	Filter *ListFilter `json:"filter,omitempty"`
	// Excluded counts meetings the filter removed; ExcludedUndated is the
	// subset dropped because a date bound was set and their date could not be
	// read — readable meetings that simply cannot be placed in time.
	Excluded        int              `json:"excluded"`
	ExcludedUndated int              `json:"excludedUndated"`
	Meetings        []map[string]any `json:"meetings"`
}

// ListFilter is the narrowing as applied, dates normalised.
type ListFilter struct {
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	Room string `json:"room,omitempty"`
	Tag  string `json:"tag,omitempty"`
}

// TagsResult is `meetings tags --json`.
type TagsResult struct {
	Tags []Tag `json:"tags"`
	// Meetings is each readable meeting that carries a mark, by meeting id.
	Meetings []MeetingTags `json:"meetings"`
	// Coverage: Indexed below Visible means some meetings' marks could not be
	// read yet, and the counts are partial.
	Coverage TagCoverage `json:"coverage"`
}

// Tag is one tag across the caller's meetings.
type Tag struct {
	TagID     string `json:"tagId"`
	Namespace string `json:"namespace"`
	Label     string `json:"label"`
	Meetings  int    `json:"meetings"`
	Marks     int    `json:"marks"`
	Color     string `json:"color"`
	Icon      string `json:"icon"`
}

// MeetingTags is the tags one meeting carries.
type MeetingTags struct {
	MeetingID string       `json:"meetingId"`
	Tags      []MeetingTag `json:"tags"`
}

// MeetingTag is one tag on one meeting: on the whole of it, on stretches of
// it, or both.
type MeetingTag struct {
	TagID     string  `json:"tagId"`
	Whole     bool    `json:"whole"`
	Stretches int     `json:"stretches"`
	Color     *string `json:"color,omitempty"`
	Icon      *string `json:"icon,omitempty"`
}

// TagCoverage is how much of the caller's archive the tag counts cover.
type TagCoverage struct {
	Visible int `json:"visible"`
	Indexed int `json:"indexed"`
}

// AnnotationsInput names one meeting.
type AnnotationsInput struct {
	ID string `json:"id" jsonschema:"a meeting id, as list reports it"`
}

// AnnotationsResult is `meetings annotations --json`: one meeting's marks
// document, as the recording carries it.
type AnnotationsResult struct {
	StateToken string      `json:"stateToken"`
	Sync       *SyncStatus `json:"sync"`
	MeetingID  string      `json:"meetingId"`
	Revision   int         `json:"revision"`
	// Annotations is the recording's marks document: tags, and items each
	// with a target (the whole meeting or a startMs–endMs range), an actor
	// and when it was made. Null when the recording carries no marks.
	Annotations any `json:"annotations"`
	// Resolved is false when the marks were made against different audio, so
	// their times do not point into this recording.
	Resolved *bool `json:"resolved"`
}

// SyncStatus is how far the recording file has caught up with its marks.
type SyncStatus struct {
	State     string `json:"state"`
	Desired   int64  `json:"desired"`
	Confirmed int64  `json:"confirmed"`
	Error     string `json:"error,omitempty"`
}
