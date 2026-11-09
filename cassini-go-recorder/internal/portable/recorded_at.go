package portable

import (
	"regexp"
	"strings"
	"time"
)

// recordedAtUTCPattern is the format's pattern for meeting.recordedAtUtc: RFC
// 3339 in UTC, ending in Z, fractional seconds allowed.
var recordedAtUTCPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$`)

// ParseRecordedAtUTC parses a meeting.recordedAtUtc value, refusing anything
// the format's pattern rejects (an offset other than Z, a missing seconds
// field, a zone-less label) as well as impossible dates.
func ParseRecordedAtUTC(value string) (time.Time, bool) {
	if !recordedAtUTCPattern.MatchString(value) {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}

// ValidRecordedAtUTC reports whether value is a well-formed recordedAtUtc.
func ValidRecordedAtUTC(value string) bool {
	_, ok := ParseRecordedAtUTC(value)
	return ok
}

// MeetingDateTag is the value of the plain DATE OpusTag: when the recording
// started if the manifest knows it, else the pack time as before.
func MeetingDateTag(meeting Meeting) string {
	if meeting.RecordedAtUTC != "" {
		return meeting.RecordedAtUTC
	}
	return meeting.CreatedAtUTC
}

// Where a meeting start came from; see MeetingStartOf.
const (
	MeetingStartRecordedAtUTC   = "recordedAtUtc"
	MeetingStartDATE            = "DATE"
	MeetingStartRecordedAtLocal = "recordedAtLocal"
)

// MeetingStart is when a meeting happened as the format's "When the meeting
// happened" rule picks it. Source is empty when the file does not say.
type MeetingStart struct {
	// Source is MeetingStartRecordedAtUTC, MeetingStartDATE,
	// MeetingStartRecordedAtLocal, or empty.
	Source string `json:"source"`
	// Value is the chosen field's text exactly as the file carries it.
	Value string `json:"value"`
	// At is the instant, set only when Instant is true: a recordedAtLocal
	// label is never converted to one.
	At      time.Time `json:"-"`
	Instant bool      `json:"-"`
}

// Known reports whether the file says when the meeting happened.
func (s MeetingStart) Known() bool { return s.Source != "" }

// dateTagPattern is RFC 3339 with an offset, Z or ±HH:MM.
var dateTagPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[Tt]\d{2}:\d{2}:\d{2}(\.\d+)?([Zz]|[+-]\d{2}:\d{2})$`)

// MeetingStartOf picks when a meeting happened from its manifest and the
// file's plain DATE tag, the first of:
//
//  1. meeting.recordedAtUtc;
//  2. DATE, when it is RFC 3339 with an offset and does not name the same
//     instant as createdAtUtc (files written before DATE meant the start
//     carry the pack time there);
//  3. meeting.recordedAtLocal, a label shown as written.
//
// createdAtUtc is never returned: it is when the file was built.
func MeetingStartOf(meeting Meeting, dateTag string) MeetingStart {
	if start := ParseMeetingStart(MeetingStartRecordedAtUTC, meeting.RecordedAtUTC); start.Known() {
		return start
	}
	if start := ParseMeetingStart(MeetingStartDATE, dateTag); start.Known() {
		created, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(meeting.CreatedAtUTC))
		if err != nil || !created.Equal(start.At) {
			return start
		}
	}
	return ParseMeetingStart(MeetingStartRecordedAtLocal, meeting.RecordedAtLocal)
}

// ParseMeetingStart rebuilds a MeetingStart from its source and value, as a
// document that carries one serialises it, recovering the instant. It is the
// zero MeetingStart when the pair does not hold together.
func ParseMeetingStart(source, value string) MeetingStart {
	value = strings.TrimSpace(value)
	switch source {
	case MeetingStartRecordedAtUTC:
		if at, ok := ParseRecordedAtUTC(value); ok {
			return MeetingStart{Source: source, Value: value, At: at, Instant: true}
		}
	case MeetingStartDATE:
		if dateTagPattern.MatchString(value) {
			if at, err := time.Parse(time.RFC3339Nano, strings.ToUpper(value)); err == nil {
				return MeetingStart{Source: source, Value: value, At: at.UTC(), Instant: true}
			}
		}
	case MeetingStartRecordedAtLocal:
		if value != "" {
			return MeetingStart{Source: source, Value: value}
		}
	}
	return MeetingStart{}
}
