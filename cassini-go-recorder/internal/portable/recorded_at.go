package portable

import (
	"regexp"
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
