package cassini

import (
	"context"
	"strings"
	"time"

	"gocassini/internal/meetingcontext"
	"gocassini/internal/portable"
)

// meetingsLocationKey carries the zone `cassini meetings` renders and reads
// times in. Commands run with time.Local; tests inject a fixed zone through
// the context instead of changing time.Local for the whole process.
type meetingsLocationKey struct{}

func withMeetingsLocation(ctx context.Context, loc *time.Location) context.Context {
	return context.WithValue(ctx, meetingsLocationKey{}, loc)
}

// meetingsLocation is the zone of the person running the CLI.
func meetingsLocation(ctx context.Context) *time.Location {
	if ctx != nil {
		if loc, ok := ctx.Value(meetingsLocationKey{}).(*time.Location); ok && loc != nil {
			return loc
		}
	}
	return time.Local
}

// meetingTime places a catalog entry on one time axis for sorting, filtering
// and grouping.
//
// An entry with recordedAtUtc is placed at that instant. An entry with only a
// dateLabel keeps today's meaning, a zone-less wall clock: its digits are read
// in the reader's zone, the same zone --from/--to are read in, so comparing
// labels with labels or with the bounds is exactly the digit comparison it
// always was, and comparing them with instants is the reading a person makes
// of an unzoned time. instant reports which of the two it was.
func meetingTime(entry meetingsCatalogEntry, loc *time.Location) (at time.Time, dated, instant bool) {
	if started, ok := portable.ParseRecordedAtUTC(strings.TrimSpace(entry.RecordedAtUTC)); ok {
		return started, true, true
	}
	at, dated = parseMeetingDateLabelIn(entry.DateLabel, loc)
	return at, dated, false
}

// meetingDisplayDate is the date= value of the text outputs: the recording
// start in the reader's zone with its offset written out, as one token
// (2026-11-09T12:31+01:00), or the legacy dateLabel exactly as published when
// the entry does not carry the instant. The offset is what tells the two
// apart, so an agent never mistakes converted digits for a label.
func meetingDisplayDate(entry meetingsCatalogEntry, loc *time.Location) string {
	if started, ok := portable.ParseRecordedAtUTC(strings.TrimSpace(entry.RecordedAtUTC)); ok {
		return started.In(loc).Format(meetingcontext.StartLayout)
	}
	return strings.TrimSpace(entry.DateLabel)
}

// meetingSearchHitDisplayDate is meetingDisplayDate for a search hit, which
// carries the same two fields under the same names.
func meetingSearchHitDisplayDate(hit meetingsSearchHit, loc *time.Location) string {
	return meetingDisplayDate(meetingsCatalogEntry{RecordedAtUTC: hit.RecordedAtUTC, DateLabel: hit.DateLabel}, loc)
}
