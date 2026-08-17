package cassini

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
)

// meetingsRoom is one row of `cassini meetings rooms`: a distinct conversation
// the caller has readable recordings from.
//
// The rooms are derived from the catalog, not fetched from Talk. That is a
// property worth keeping: the catalog is already filtered to what this account
// may read, so a room the caller has no readable recording from cannot appear
// here — and a room's existence is never disclosed by this command alone.
type meetingsRoom struct {
	// Selector is the value to paste into `meetings list --room`. It is the
	// room's id when it has one, and "name:<display name>" when the room is
	// known only by name.
	Selector string `json:"room"`
	RoomID   string `json:"roomId,omitempty"`
	RoomName string `json:"roomName,omitempty"`
	Meetings int    `json:"meetings"`
	// Latest and Earliest are the dateLabels of the newest and oldest readable
	// meeting in the room, empty when none of them carried a usable label.
	Latest   string `json:"latest,omitempty"`
	Earliest string `json:"earliest,omitempty"`
}

func runMeetingsRooms(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var cfg meetingsConfig
	fs := flag.NewFlagSet("cassini meetings rooms", flag.ContinueOnError)
	fs.SetOutput(stderr)
	registerMeetingsConnectionFlags(fs, &cfg)
	asJSON := fs.Bool("json", false, "emit the rooms as JSON instead of one line per room")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini meetings rooms
  cassini meetings rooms --json

List the conversations your Nextcloud account has readable recordings from.

The rooms are derived from the recordings you may read, so a room you have no
readable recording from does not appear — this command discloses nothing that
`+"`cassini meetings list`"+` does not.

The room= value is what `+"`cassini meetings list --room`"+` accepts. It is the
room's id where one is known, and "name:<display name>" for a recording made
before Cassini recorded room ids, whose room can only be identified by name.

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
		fmt.Fprintf(stderr, "rooms does not accept positional arguments: %v\n", redactMeetingsArgs(fs.Args()))
		fs.Usage()
		return 2
	}
	if err := resolveMeetingsConfig(fs, &cfg); err != nil {
		fmt.Fprintf(stderr, "rooms configuration error: %v\n", err)
		return 2
	}
	warnAboutInsecureTLS(stderr, cfg)

	client := newMeetingsClient(cfg)
	listing, err := client.fetchCatalog(ctx)
	if err != nil {
		return reportMeetingsError(stderr, "rooms", cfg, err)
	}
	warnAboutMeetingsSource(stderr, listing)

	rooms, unattributed := groupMeetingsByRoom(listing)

	if *asJSON {
		if err := writeMeetingsRoomsJSON(stdout, listing, rooms, unattributed); err != nil {
			fmt.Fprintf(stderr, "rooms failed: write JSON: %v\n", err)
			return 1
		}
		return 0
	}

	fmt.Fprintf(stdout, "rooms=%d caller=%s source=%s\n", len(rooms), cfg.user, listing.Source)
	if len(rooms) == 0 && unattributed == 0 {
		// Same ambiguity `list` reports, for the same reason: the app answers
		// 200 with an empty list both when the caller has no readable
		// recordings and when the recordings substrate is mis-provisioned.
		fmt.Fprintln(stdout, "note=no recordings are visible to this account, so there are no rooms to list; this is also what a mis-provisioned recordings folder looks like")
		return 0
	}
	for _, room := range rooms {
		fmt.Fprintf(stdout, "room=%s name=%s meetings=%d latest=%s earliest=%s\n",
			blankMeetingsDash(room.Selector),
			blankMeetingsDash(room.RoomName),
			room.Meetings,
			blankMeetingsDash(room.Latest),
			blankMeetingsDash(room.Earliest),
		)
	}
	if unattributed > 0 {
		// Counted, never hidden. A rooms listing whose numbers do not add up to
		// the meeting list is a discrepancy nobody ever tracks down, and these
		// meetings are real — they were recorded before Cassini kept the room,
		// or their room lookup failed at record time.
		fmt.Fprintf(stdout, "note=%d readable meeting(s) carry no room at all and are in none of the rooms above; `cassini meetings list` still shows them\n", unattributed)
	}
	return 0
}

// groupMeetingsByRoom folds a listing into one row per distinct room, plus a
// count of the meetings that name no room whatsoever.
//
// Rows are keyed by selector, so two recordings from one room group together
// whether the room is identified by id or by name — but a room whose id is
// known and a room known only by name are NOT merged, even when the names
// match. Merging them would be a guess about identity that nothing in the data
// supports: two conversations can share a display name, and a room can be
// renamed between recordings.
func groupMeetingsByRoom(listing meetingsListing) (rooms []meetingsRoom, unattributed int) {
	index := map[string]int{}
	// Newest-first order comes from the listing itself, and the first and last
	// item seen per room are therefore its latest and earliest.
	for _, item := range listing.Items {
		selector := item.entry.roomSelector()
		if selector == "" {
			unattributed++
			continue
		}
		position, seen := index[selector]
		if !seen {
			index[selector] = len(rooms)
			rooms = append(rooms, meetingsRoom{
				Selector: selector,
				RoomID:   strings.TrimSpace(item.entry.RoomID),
				RoomName: strings.TrimSpace(item.entry.RoomName),
			})
			position = len(rooms) - 1
		}
		room := &rooms[position]
		room.Meetings++
		if room.RoomName == "" {
			// An id-identified room whose first (newest) recording predates the
			// room name being recorded still deserves a label if a later one
			// carries it.
			room.RoomName = strings.TrimSpace(item.entry.RoomName)
		}
		if !item.dated {
			continue
		}
		if room.Latest == "" {
			room.Latest = strings.TrimSpace(item.entry.DateLabel)
		}
		room.Earliest = strings.TrimSpace(item.entry.DateLabel)
	}
	// Most recently active first, which is the order the meeting list is in and
	// so the order someone scanning both expects. Rooms whose meetings are all
	// undated sort last; ties break by selector so the output is deterministic.
	sort.SliceStable(rooms, func(i, j int) bool {
		left, right := rooms[i], rooms[j]
		leftAt, leftDated := parseMeetingDateLabel(left.Latest)
		rightAt, rightDated := parseMeetingDateLabel(right.Latest)
		switch {
		case leftDated && rightDated:
			if !leftAt.Equal(rightAt) {
				return leftAt.After(rightAt)
			}
		case leftDated != rightDated:
			return leftDated
		}
		return left.Selector < right.Selector
	})
	return rooms, unattributed
}

// writeMeetingsRoomsJSON emits the machine form.
//
// It carries source and skipped for the same reason `list --json` does: --json
// is the path an agent reads, and it must not be the only one that never hears
// the underlying catalog may be incomplete.
func writeMeetingsRoomsJSON(out io.Writer, listing meetingsListing, rooms []meetingsRoom, unattributed int) error {
	if rooms == nil {
		rooms = []meetingsRoom{}
	}
	document := struct {
		Version string `json:"version"`
		Source  string `json:"source"`
		Skipped int    `json:"skipped"`
		// Unattributed counts readable meetings that name no room. They are in
		// none of the rooms below and no --room value selects them.
		Unattributed int            `json:"unattributed"`
		Rooms        []meetingsRoom `json:"rooms"`
	}{
		Version:      listing.Version,
		Source:       listing.Source,
		Skipped:      listing.Skipped,
		Unattributed: unattributed,
		Rooms:        rooms,
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(document)
}
