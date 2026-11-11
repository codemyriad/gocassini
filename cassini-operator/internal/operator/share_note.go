package operator

import (
	"fmt"
	"strings"
	"time"
)

// maxShareNoteTitle keeps a note one line a person can read in Files.
const maxShareNoteTitle = 120

// oneLineTitle is a meeting title as it is told to a person: on one line,
// and at most maxShareNoteTitle characters, cut between characters. A title
// is display text, not echoed input, so the byte-cutting oneLineListField
// is not for it: cut mid-character, a Cyrillic or CJK title becomes invalid
// UTF-8, which a utf8mb4 Nextcloud database refuses on the share note.
func oneLineTitle(title string) string {
	cleaned := strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, title))
	if runes := []rune(cleaned); len(runes) > maxShareNoteTitle {
		return strings.TrimSpace(string(runes[:maxShareNoteTitle])) + "…"
	}
	return cleaned
}

// shareNote is the text on every recording share Cassini makes: what the
// file is, so the person recognises it in Files and in Cassini's own list of
// recordings waiting for them. Nextcloud also puts this text in the body of
// the share email, so it carries the whole explanation a recipient needs:
// the meeting, its date and length, what the file contains, and how to open
// it. No URL — the deep link into Cassini is not stable across versions, and
// a stale link would be worse than none.
//
//	Cassini meeting recording — Weekly sync, Friday 30 October 2026, 10:00 UTC (42 min). One portable file containing the audio, transcript and AI summary. Open it in Nextcloud Files, or in Cassini to read the transcript and summary.
//	Imported Cassini meeting — Weekly sync, Friday 30 October 2026, 10:00 UTC (42 min). One portable file containing the audio, transcript and AI summary. Open it in Nextcloud Files, or in Cassini to read the transcript and summary.
//
// The clock time says it is UTC: Browse shows the same start in the reader's
// own zone, and the two must not look like different meetings. A missing
// title or date is left out rather than written as a blank.
func (f meetingFacts) shareNote(imported bool) string {
	title := oneLineTitle(f.Title)
	var b strings.Builder
	if imported {
		b.WriteString("Imported Cassini meeting")
	} else {
		b.WriteString("Cassini meeting recording")
	}
	if title != "" {
		b.WriteString(" — ")
		b.WriteString(title)
	}
	if !f.StartedAt.IsZero() {
		if title != "" {
			b.WriteString(", ")
		} else {
			b.WriteString(" — ")
		}
		b.WriteString(f.StartedAt.UTC().Format("Monday 2 January 2006, 15:04") + " UTC")
	}
	if f.Duration > 0 {
		b.WriteString(" (" + formatMeetingDuration(f.Duration) + ")")
	}
	b.WriteString(". One portable file containing the audio, transcript and AI summary. Open it in Nextcloud Files, or in Cassini to read the transcript and summary.")
	return b.String()
}

// formatMeetingDuration is the length as a person says it: "42 min", "1 h 05".
func formatMeetingDuration(d time.Duration) string {
	minutes := int((d + 30*time.Second) / time.Minute)
	switch {
	case minutes < 1:
		return "under a minute"
	case minutes < 60:
		return fmt.Sprintf("%d min", minutes)
	default:
		return fmt.Sprintf("%d h %02d", minutes/60, minutes%60)
	}
}
