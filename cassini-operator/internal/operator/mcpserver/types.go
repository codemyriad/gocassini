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

// SearchInput is one search. Only Query is required.
type SearchInput struct {
	Query   string `json:"query" jsonschema:"the words to look for"`
	Speaker string `json:"speaker,omitempty" jsonschema:"only moments this speaker id said"`
	Room    string `json:"room,omitempty" jsonschema:"only meetings from this room id"`
	Tag     string `json:"tag,omitempty" jsonschema:"only meetings carrying this tag label or id"`
	Limit   int    `json:"limit,omitempty" jsonschema:"how many moments to return (default 20, max 100)"`
}

// SearchResult is `meetings search --json`: where something was said, never
// what. There is no text field anywhere in it, by construction — reading what
// was said is context's job, which reads the recording as the caller.
type SearchResult struct {
	Hits []SearchHit `json:"hits"`
	// Widened reports that requiring every word found nothing and the query
	// was retried as "any of them", which answers a different question.
	Widened bool `json:"widened,omitempty"`
	// Searched is what was actually looked for after expanding the spellings
	// transcription produces for a word.
	Searched [][]string `json:"searched,omitempty"`
	// Coverage: Searched below Visible means some readable meetings are not
	// indexed yet, and "nothing found" covers only the rest.
	Coverage SearchCoverage `json:"coverage"`
}

// SearchHit is one moment: which meeting, who, and where in the recording.
type SearchHit struct {
	MeetingID       string `json:"meetingId"`
	Title           string `json:"title,omitempty"`
	DateLabel       string `json:"dateLabel,omitempty"`
	RoomID          string `json:"roomId,omitempty"`
	RoomName        string `json:"roomName,omitempty"`
	RoomDisplayName string `json:"roomDisplayName,omitempty"`
	SegmentID       string `json:"segmentId"`
	StartMS         int64  `json:"startMs"`
	EndMS           int64  `json:"endMs"`
	SpeakerID       string `json:"speakerId,omitempty"`
	// Matched says why it matched: "alias" means one of the spellings
	// transcription produces for a word, not the word itself.
	Matched string `json:"matched"`
	// Marks are the tags whose marked stretches this moment overlaps.
	Marks []SearchMark `json:"marks,omitempty"`
}

// SearchMark is a tag on the stretch a hit falls in.
type SearchMark struct {
	TagID string `json:"tagId"`
	Label string `json:"label"`
}

// SearchCoverage is what an answer can honestly claim to have covered.
type SearchCoverage struct {
	Visible  int `json:"visible"`
	Searched int `json:"searched"`
}

// ContextInput names the meetings to read, and optionally a stretch of them.
type ContextInput struct {
	IDs    []string `json:"ids" jsonschema:"1 to 20 meeting ids, in the order to read them"`
	FromMS *int64   `json:"fromMs,omitempty" jsonschema:"read only from this point, in milliseconds from the start (a search hit's startMs, less a minute, reads around it)"`
	ToMS   *int64   `json:"toMs,omitempty" jsonschema:"read only up to this point, in milliseconds from the start"`
}

// ContextResult is cassini.meetings.context.v1 in its container form: every
// element of Meetings is one complete meeting document. (The CLI prints a
// single meeting as the bare document; a tool keeps one shape.)
type ContextResult struct {
	Version  string           `json:"version"`
	Meetings []MeetingContext `json:"meetings"`
}

// MeetingContext is one meeting as an agent reads it.
type MeetingContext struct {
	Version  string           `json:"version"`
	Meeting  ContextMeeting   `json:"meeting"`
	Speakers []ContextSpeaker `json:"speakers,omitempty"`
	// TranscriptSource is "derived-from-words": the prose was assembled from
	// speech recognition, and must not be presented as edited text.
	TranscriptSource       string `json:"transcriptSource"`
	SourceTranscriptID     string `json:"sourceTranscriptId,omitempty"`
	SourceTranscriptFormat string `json:"sourceTranscriptFormat,omitempty"`
	Language               string `json:"language,omitempty"`
	// WordCount is the whole meeting's, window or not.
	WordCount int `json:"wordCount"`
	// Window is set when only a stretch was asked for: Segments then holds
	// only the passages overlapping it.
	Window   *ContextWindow   `json:"window,omitempty"`
	Segments []ContextSegment `json:"segments"`
	Summary  ContextSummary   `json:"summary"`
}

// ContextMeeting is what the meeting was.
type ContextMeeting struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	CreatedAtUTC    string `json:"createdAtUtc,omitempty"`
	DurationMS      int64  `json:"durationMs,omitempty"`
	RoomID          string `json:"roomId,omitempty"`
	RoomName        string `json:"roomName,omitempty"`
	RoomDisplayName string `json:"roomDisplayName,omitempty"`
}

// ContextSpeaker is one member of the meeting's roster.
type ContextSpeaker struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// ContextWindow is the stretch a windowed document holds.
type ContextWindow struct {
	FromMS int64  `json:"fromMs"`
	ToMS   *int64 `json:"toMs,omitempty"`
}

// ContextSegment is one speaker-attributed passage. StartMS is the minute to
// cite.
type ContextSegment struct {
	Speaker      string `json:"speaker,omitempty"`
	SpeakerLabel string `json:"speakerLabel,omitempty"`
	StartMS      int64  `json:"startMs"`
	EndMS        int64  `json:"endMs"`
	Text         string `json:"text"`
}

// ContextSummary is the generated summary, when there is one.
type ContextSummary struct {
	Present  bool   `json:"present"`
	Format   string `json:"format,omitempty"`
	Model    string `json:"model,omitempty"`
	Markdown string `json:"markdown,omitempty"`
}
