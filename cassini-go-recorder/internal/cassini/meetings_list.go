package cassini

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"unicode"
)

func runMeetingsList(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var cfg meetingsConfig
	fs := flag.NewFlagSet("cassini meetings list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	registerMeetingsConnectionFlags(fs, &cfg)
	asJSON := fs.Bool("json", false, "emit the catalog as JSON instead of one line per meeting")
	fromDate := fs.String("from", "", "only meetings on or after this date (e.g. 2026-08-01)")
	toDate := fs.String("to", "", "only meetings on or before this date (a bare date includes the whole day)")
	room := fs.String("room", "", "only meetings from this room, as printed by `cassini meetings rooms`")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini meetings list
  cassini meetings list --json
  cassini meetings list --from 2026-08-01 --to 2026-08-31
  cassini meetings list --room <room>

List the meetings your Nextcloud account may read. Prints nothing but a count
when the account may read none — which is also what a mis-provisioned
recordings folder looks like.

Every filter is optional and they combine: a meeting must satisfy all of the
ones you pass.

Dates are written the way this catalog prints them — 2026-08-11, or
2026-08-11 14:30, or 2026-08-11 14:30:05 — and carry no timezone, because
meeting dates do not. A bare date covers the whole day at both ends, so
--from 2026-08-01 --to 2026-08-31 means all of August. A meeting whose date
cannot be read is left out of any dated range and reported separately.

--room takes the room= value from `+"`cassini meetings rooms`"+` — an opaque
derived id, shaped rm_<16 hex>. Copy it; it is not the conversation's name and
not its Talk token, and neither of those will match. A meeting that records no
room at all is matched by no --room value.

`+"\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "list does not accept positional arguments: %v\n", redactMeetingsArgs(fs.Args()))
		fs.Usage()
		return 2
	}
	// Validate the filter before the network call: a malformed date is a usage
	// error the caller has to fix either way, and making them wait for a round
	// trip to hear it is pure cost.
	filter, err := parseMeetingsFilter(*fromDate, *toDate, *room)
	if err != nil {
		fmt.Fprintf(stderr, "list configuration error: %v\n", err)
		return 2
	}
	if err := resolveMeetingsConfig(fs, &cfg); err != nil {
		fmt.Fprintf(stderr, "list configuration error: %v\n", err)
		return 2
	}
	warnAboutInsecureTLS(stderr, cfg)

	client := newMeetingsClient(cfg)
	// Server-side where the app supports it, client-side against an older one.
	// The counts that explain a short list come from whichever side filtered.
	listing, result, err := client.fetchMeetings(ctx, filter)
	if err != nil {
		return reportMeetingsError(stderr, "list", cfg, err)
	}

	// Diagnostics go out before the format branches, and before the filter, so
	// the --json path — the one an agent actually reads — is not the only one
	// that never hears the list may be incomplete, and so the warnings describe
	// the whole catalog rather than the slice that survived it.
	warnAboutMeetingsSource(stderr, listing)

	if *asJSON {
		if err := writeMeetingsCatalogJSON(stdout, listing, filter, result); err != nil {
			fmt.Fprintf(stderr, "list failed: write JSON: %v\n", err)
			return 1
		}
		return 0
	}

	entries := make([]meetingsCatalogEntry, 0, len(result.items))
	for _, item := range result.items {
		entries = append(entries, item.entry)
	}
	fmt.Fprintf(stdout, "meetings=%d caller=%s source=%s\n", len(entries), cfg.user, listing.Source)
	if filter.active() {
		fmt.Fprintf(stdout, "filter=%s excluded=%d\n", filter.describe(), result.excluded)
	}
	if result.undated > 0 {
		// Nothing the caller typed is wrong here, so it gets its own sentence
		// rather than being folded into the excluded count and left mysterious.
		fmt.Fprintf(stdout, "note=%d meeting(s) have a date this build cannot read and are left out of any dated range; list them without --from/--to\n", result.undated)
	}
	if len(entries) == 0 {
		switch {
		case filter.active() && result.excluded > 0:
			// A third explanation, and it must not borrow either of the others:
			// telling someone whose filter matched nothing that the recordings
			// folder may be mis-provisioned would send them to debug a
			// provisioning failure that does not exist.
			fmt.Fprintf(stdout, "note=your filter excluded all %d readable meeting(s); widen it, or run `cassini meetings rooms` to see what to filter by\n", result.excluded)
		case listing.Skipped > 0:
			// Not a permissions or provisioning story: the server did return
			// meetings and every one was unusable. Claiming none are visible to
			// this account would be false.
			fmt.Fprintf(stdout, "note=the catalog held %d entr(y/ies) but none carried an id, so none can be read; the catalog is malformed rather than empty\n", listing.Skipped)
		default:
			// The app answers 200 with an empty list both when the caller really
			// has no readable recordings and when the recordings substrate is
			// mis-provisioned or unreachable. It cannot tell the caller which,
			// by design — so neither can we, and this is not a failure.
			fmt.Fprintln(stdout, "note=no recordings are visible to this account; this is also what a mis-provisioned recordings folder looks like")
		}
		return 0
	}
	for _, entry := range entries {
		fmt.Fprintf(stdout, "meeting=%s date=%s room=%s title=%s speakers=%s segments=%s duration_ms=%s fetchable=%s\n",
			blankMeetingsDash(entry.ID),
			blankMeetingsDash(entry.DateLabel),
			blankMeetingsDash(entry.roomSelector()),
			blankMeetingsDash(entry.Title),
			meetingsCount(entry.SpeakerCount),
			meetingsCount(entry.SegmentCount),
			meetingsCount64(entry.DigestDurationMS),
			meetingsYesNo(strings.TrimSpace(entry.AudioPath) != ""),
		)
	}
	return 0
}

// blankMeetingsDash renders a field for the one-record-per-line output: empty
// becomes "-", matching how inspect prints key=value records, and control
// characters are flattened to spaces.
//
// The flattening is not cosmetic. Titles come from the server, and a title
// containing a newline would let a catalog forge additional "meeting=" lines
// that a caller parsing this output would read as real recordings; an escape
// sequence could rewrite the terminal. Neither can survive into a line-oriented
// format.
func blankMeetingsDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return oneLineField(value)
}

// oneLineField replaces every character that could break out of the line with a
// space, so a server-supplied string cannot forge a record of its own.
//
// unicode.IsControl is not sufficient, and the gap matters more now that room
// names — Talk conversation names, renameable by any participant — are printed
// here. Two classes above U+00FF get through it:
//
//   - U+2028 LINE SEPARATOR and U+2029 PARAGRAPH SEPARATOR (categories Zl/Zp).
//     Go does not treat them as line terminators, but Python's splitlines, a
//     JavaScript multiline regexp and Java's String.lines() all do — so a name
//     carrying one forges a whole extra record for exactly the consumers most
//     likely to be parsing this output.
//   - The bidi overrides and isolates (U+202A–U+202E, U+2066–U+2069) and the
//     directional marks (U+200E, U+200F). These do not forge a line; they
//     reverse how the REST of the line renders, so a room name can make the
//     fields after it read as something else entirely in any terminal that
//     honours bidi.
//
// The rest of category Cf is deliberately left alone: ZWJ and ZWNJ (U+200C,
// U+200D) are load-bearing in Arabic, Indic scripts and emoji sequences, and
// flattening them would corrupt legitimate names to no benefit.
func oneLineField(value string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n' || r == '\r' || unicode.IsControl(r):
			return ' '
		case unicode.In(r, unicode.Zl, unicode.Zp):
			return ' '
		case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r == 0x200E, r == 0x200F:
			return ' '
		}
		return r
	}, value)
}

// meetingsCount renders an absent count as "-" rather than a misleading 0.
func meetingsCount(value int) string {
	if value <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d", value)
}

func meetingsCount64(value int64) string {
	if value <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d", value)
}

func meetingsYesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
