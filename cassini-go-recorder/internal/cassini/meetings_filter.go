package cassini

import (
	"fmt"
	"strings"
	"time"
)

// meetingsFilter is the optional narrowing `cassini meetings list` applies to a
// fetched catalog. A zero value matches everything.
//
// It is applied client-side, over the catalog the caller already fetched. There
// is no server-side query to push it into: the published catalog is one
// document, already filtered to what this account may read, and it is the only
// index there is.
type meetingsFilter struct {
	// from and to are inclusive bounds on the meeting's dateLabel, compared in
	// the label's own zone-less wall-clock space.
	from    time.Time
	hasFrom bool
	to      time.Time
	hasTo   bool
	// room is a selector as printed by `meetings rooms`: a room id, or
	// "name:<display name>" for a room known only by name.
	room string
}

func (f meetingsFilter) active() bool {
	return f.hasFrom || f.hasTo || f.room != ""
}

// describe renders the filter for the summary line and the JSON echo, so a
// short list is self-explaining rather than mysterious.
func (f meetingsFilter) describe() string {
	var parts []string
	if f.hasFrom {
		parts = append(parts, "from:"+f.from.Format(meetingsFilterStampLayout))
	}
	if f.hasTo {
		parts = append(parts, "to:"+f.to.Format(meetingsFilterStampLayout))
	}
	if f.room != "" {
		parts = append(parts, "room:"+oneLineField(f.room))
	}
	return strings.Join(parts, " ")
}

const meetingsFilterStampLayout = "2006-01-02 15:04:05"

// parseMeetingsFilter validates the flags and turns them into a filter.
//
// Validation happens before the network call on purpose: a malformed date is a
// usage error the caller must fix, and making them wait for a round trip to
// hear it is pure cost.
func parseMeetingsFilter(from, to, room string) (meetingsFilter, error) {
	var filter meetingsFilter
	var err error
	if trimmed := strings.TrimSpace(from); trimmed != "" {
		filter.from, err = parseMeetingsFilterBound(trimmed, false)
		if err != nil {
			return meetingsFilter{}, fmt.Errorf("--from: %w", err)
		}
		filter.hasFrom = true
	}
	if trimmed := strings.TrimSpace(to); trimmed != "" {
		filter.to, err = parseMeetingsFilterBound(trimmed, true)
		if err != nil {
			return meetingsFilter{}, fmt.Errorf("--to: %w", err)
		}
		filter.hasTo = true
	}
	if filter.hasFrom && filter.hasTo && filter.to.Before(filter.from) {
		// An inverted range can only ever match nothing, and silently returning
		// an empty list would look identical to "you may read no recordings" —
		// a far more alarming answer than "you typed the dates the wrong way
		// round".
		return meetingsFilter{}, fmt.Errorf(
			"--from %s is after --to %s, which can never match anything; did you swap them?",
			filter.from.Format(meetingsFilterStampLayout), filter.to.Format(meetingsFilterStampLayout))
	}
	filter.room = strings.TrimSpace(room)
	return filter, nil
}

// parseMeetingsFilterBound parses one end of the range, accepting exactly the
// layouts the catalog's own dateLabel uses — so the CLI accepts what it prints.
//
// A bare date means the whole day: 00:00:00 at the start of a range and
// 23:59:59 at the end. `--from 2026-08-01 --to 2026-08-31` has to mean August,
// which is what anyone typing it intends; a --to that stopped at midnight would
// silently drop the last day.
//
// No timezone is parsed or applied. dateLabel deliberately does not know its
// zone (D-484), so item.at is a wall clock valid only for relative ordering.
// Comparisons stay in that space; asserting a zone here would invent an instant
// the data cannot justify.
func parseMeetingsFilterBound(value string, endOfRange bool) (time.Time, error) {
	for _, layout := range meetingDateLabelLayouts {
		at, err := time.Parse(layout, value)
		if err != nil {
			continue
		}
		if endOfRange && layout == "2006-01-02" {
			return at.Add(24*time.Hour - time.Second), nil
		}
		return at, nil
	}
	return time.Time{}, fmt.Errorf(
		"%q is not a date this catalog uses; write it as 2026-08-11, 2026-08-11 14:30 or 2026-08-11 14:30:05 (no timezone — meeting dates do not carry one)",
		oneLineField(value))
}

// meetingsFilterResult is what applying a filter produced: the surviving items
// and why the rest went, split so the caller can explain a short list.
type meetingsFilterResult struct {
	items []meetingsCatalogItem
	// excluded counts items the filter removed for a reason it can state.
	excluded int
	// undated counts items dropped because a date filter was applied and their
	// dateLabel could not be parsed. They are inside excluded as well; this is
	// the subset that deserves its own sentence, since nothing the caller typed
	// is wrong.
	undated int
}

// applyMeetingsFilter narrows a listing.
//
// Undated entries are dropped explicitly whenever a date bound is set, rather
// than compared as a zero time. A zero timestamp fails every --from and matches
// every --to, so the same meeting would appear or vanish depending on which end
// of the range the caller happened to type — a silent, direction-dependent lie.
// Dropping them and saying how many is the only answer true in both directions.
func applyMeetingsFilter(items []meetingsCatalogItem, filter meetingsFilter) meetingsFilterResult {
	if !filter.active() {
		return meetingsFilterResult{items: items}
	}
	result := meetingsFilterResult{items: make([]meetingsCatalogItem, 0, len(items))}
	dated := filter.hasFrom || filter.hasTo
	for _, item := range items {
		if dated && !item.dated {
			result.excluded++
			result.undated++
			continue
		}
		if filter.hasFrom && item.at.Before(filter.from) {
			result.excluded++
			continue
		}
		if filter.hasTo && item.at.After(filter.to) {
			result.excluded++
			continue
		}
		if filter.room != "" && !meetingMatchesRoom(item.entry, filter.room) {
			result.excluded++
			continue
		}
		result.items = append(result.items, item)
	}
	return result
}

// meetingMatchesRoom reports whether an entry belongs to the room a selector
// names.
//
// Matching is exact in both forms. A room id is an opaque Talk token, so
// substring-matching one is meaningless; a name selector is exact because the
// selector was produced from a room listing and pasted back, not typed from
// memory. Neither form ever matches a meeting that carries no room — those are
// selected by no --room value, which is why `rooms` counts them separately.
func meetingMatchesRoom(entry meetingsCatalogEntry, selector string) bool {
	if name, ok := strings.CutPrefix(selector, meetingsRoomNameSelectorPrefix); ok {
		wanted := strings.TrimSpace(name)
		// A "name:" selector matches on the name and only the name. An entry
		// that has an id is a different room from a same-named entry that has
		// none — the room listing shows them as separate rows, and the filter
		// must agree with the listing that produced the selector.
		return wanted != "" && strings.TrimSpace(entry.RoomID) == "" && strings.TrimSpace(entry.RoomName) == wanted
	}
	return strings.TrimSpace(entry.RoomID) == selector
}
