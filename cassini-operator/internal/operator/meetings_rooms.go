package operator

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
)

// `GET published/meetings-rooms` (D-813): the conversations a caller has
// readable recordings from, one row per room.
//
// The grouping was the CLI's alone (`cassini meetings rooms`, which folds the
// list client-side). The MCP `rooms` tool needs the same answer and must not
// carry a second copy of it, so it lives here and both read it; the CLI can
// switch to this route as a follow-up. The document is the CLI's `--json`
// shape, field for field.
//
// It sits beside meetings-list under the manifest's existing `^published\/.+$`
// route — no info.xml change — and inherits its one access-control path: the
// rows are derived from the caller's own meeting list, so a room they have no
// readable recording from cannot appear, and a room's existence is never
// disclosed by this route alone.

// meetingsRoomsPath is the archive-relative path the proxy forwards here.
const meetingsRoomsPath = "meetings-rooms"

// meetingsRoom is one row: a distinct conversation the caller has readable
// recordings from.
type meetingsRoom struct {
	// Selector is the value the list's room filter takes — the room's derived
	// id, kept as its own field so "this is what room= takes" stays explicit.
	Selector        string `json:"room"`
	RoomID          string `json:"roomId,omitempty"`
	RoomName        string `json:"roomName,omitempty"`
	RoomDisplayName string `json:"roomDisplayName,omitempty"`
	Meetings        int    `json:"meetings"`
	// Latest and Earliest are the dateLabels of the newest and oldest readable
	// meeting in the room, empty when none of them carried a usable label.
	Latest   string `json:"latest,omitempty"`
	Earliest string `json:"earliest,omitempty"`
}

// meetingsRoomsResponse is the route's body, the CLI's `rooms --json`.
type meetingsRoomsResponse struct {
	Version string `json:"version"`
	Source  string `json:"source"`
	// Skipped counts entries dropped as unusable (no id), so a consumer can
	// tell the underlying list is incomplete.
	Skipped int `json:"skipped"`
	// Unattributed counts readable meetings that name no room. They are in
	// none of the rooms and no room filter selects them.
	Unattributed int            `json:"unattributed"`
	Rooms        []meetingsRoom `json:"rooms"`
}

// listedMeeting is one entry of a caller's list with what ordering and
// grouping read from it. raw is the entry's bytes, untouched.
type listedMeeting struct {
	raw             json.RawMessage
	id              string
	dateLabel       string
	roomID          string
	roomName        string
	roomDisplayName string
	at              time.Time
	dated           bool
}

// orderListedMeetings decodes a list's entries and puts them in the order the
// viewer and the CLI show them: newest first by dateLabel, entries with an
// unreadable label last, ties broken by id descending. An entry with no id
// cannot be referred to, so it is dropped and counted.
func orderListedMeetings(entries []json.RawMessage) ([]listedMeeting, int) {
	items := make([]listedMeeting, 0, len(entries))
	skipped := 0
	for _, raw := range entries {
		var probe struct {
			ID              string `json:"id"`
			DateLabel       string `json:"dateLabel"`
			RoomID          string `json:"roomId"`
			RoomName        string `json:"roomName"`
			RoomDisplayName string `json:"roomDisplayName"`
		}
		// A JSON null decodes into a zero entry without error, and is skipped
		// below with every other entry that has no id.
		if err := json.Unmarshal(raw, &probe); err != nil || strings.TrimSpace(probe.ID) == "" {
			skipped++
			continue
		}
		at, dated := parseMeetingsListDateLabel(probe.DateLabel)
		items = append(items, listedMeeting{
			raw: raw, id: strings.TrimSpace(probe.ID),
			dateLabel: strings.TrimSpace(probe.DateLabel),
			roomID:    strings.TrimSpace(probe.RoomID), roomName: strings.TrimSpace(probe.RoomName),
			roomDisplayName: strings.TrimSpace(probe.RoomDisplayName),
			at:              at, dated: dated,
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i], items[j]
		switch {
		case left.dated && right.dated:
			if !left.at.Equal(right.at) {
				return left.at.After(right.at)
			}
		case left.dated != right.dated:
			return left.dated
		}
		return left.id > right.id
	})
	return items, skipped
}

// groupMeetingsByRoom folds an ordered list into one row per distinct room,
// plus a count of the meetings that record no room whatsoever.
//
// Rows are keyed by room id. Two rooms may have the same display name, so
// merging them by name would assert an identity the data cannot prove.
func groupMeetingsByRoom(items []listedMeeting) ([]meetingsRoom, int) {
	rooms := []meetingsRoom{}
	index := map[string]int{}
	unattributed := 0
	// items are newest first, so the first and last seen per room are its
	// latest and earliest.
	for _, item := range items {
		if item.roomID == "" {
			unattributed++
			continue
		}
		position, seen := index[item.roomID]
		if !seen {
			index[item.roomID] = len(rooms)
			rooms = append(rooms, meetingsRoom{
				Selector: item.roomID, RoomID: item.roomID,
				RoomName: item.roomName, RoomDisplayName: item.roomDisplayName,
			})
			position = len(rooms) - 1
		}
		room := &rooms[position]
		room.Meetings++
		if room.RoomDisplayName == "" {
			room.RoomDisplayName = item.roomDisplayName
		}
		if room.RoomName == "" {
			// A room whose newest recording predates the room name being
			// recorded still deserves a label if an older one carries it.
			room.RoomName = item.roomName
		}
		if !item.dated {
			continue
		}
		if room.Latest == "" {
			room.Latest = item.dateLabel
		}
		room.Earliest = item.dateLabel
	}
	// Most recently active first, the order the meeting list is in. Rooms
	// whose meetings are all undated sort last; ties break by selector so the
	// output is deterministic.
	sort.SliceStable(rooms, func(i, j int) bool {
		left, right := rooms[i], rooms[j]
		leftAt, leftDated := parseMeetingsListDateLabel(left.Latest)
		rightAt, rightDated := parseMeetingsListDateLabel(right.Latest)
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

// meetingsRooms is the caller's rooms. Every failure is a *readFailure.
func (c ExAppConfig) meetingsRooms(ctx context.Context, client *http.Client, caller string, deps searchDeps, logger *log.Logger) (meetingsRoomsResponse, error) {
	list, err := c.meetingsList(ctx, client, caller, meetingsListFilter{}, deps, logger)
	if err != nil {
		return meetingsRoomsResponse{}, err
	}
	items, skipped := orderListedMeetings(list.Meetings)
	rooms, unattributed := groupMeetingsByRoom(items)
	return meetingsRoomsResponse{
		Version:      list.Version,
		Source:       ncFilesSourceValue,
		Skipped:      skipped,
		Unattributed: unattributed,
		Rooms:        rooms,
	}, nil
}

// serveMeetingsRooms answers `published/meetings-rooms` for one caller.
func (c ExAppConfig) serveMeetingsRooms(ctx context.Context, w http.ResponseWriter, r *http.Request, client *http.Client, caller string, deps searchDeps, logger *log.Logger) {
	response, err := c.meetingsRooms(ctx, client, caller, deps, logger)
	if err != nil {
		writeReadFailure(w, r, err)
		return
	}
	body, err := json.Marshal(response)
	if err != nil {
		if logger != nil {
			logger.Printf("meetings rooms: encode response caller=%s: %v", caller, err)
		}
		writeJSONError(w, http.StatusInternalServerError, "could not encode the room list")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// Derived from the meeting list, so as fresh as it must be.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set(ncFilesSourceHeader, ncFilesSourceValue)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
