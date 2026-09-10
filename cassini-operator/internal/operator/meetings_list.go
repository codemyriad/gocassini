package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The meetings-list endpoint: `GET published/meetings`.
//
// Until now there was no meetings-list API at all. Every consumer — the viewer
// in each of its three deployment modes, and the `cassini meetings` CLI that
// agents drive — fetched `published/catalog.json` and narrowed it client-side.
// That works, and it stays working: this endpoint is additive, and nothing is
// required to move to it.
//
// What it adds is a place for the narrowing to happen where the caller's
// identity already is. Three properties follow from that, and they are the
// reason it exists rather than being sugar over the same document:
//
//  1. the wire format is the SAME `cassini.viewer.catalog.v1` envelope, so a
//     viewer build and a CLI binary that predate this endpoint can consume it
//     unchanged. The format is a shipped contract with three independent
//     clients, none upgraded in lockstep; adding a query surface is not a
//     reason to open a v2;
//  2. failures are LOUD (see catalogResolveOutcome). An agent that reads
//     "Nextcloud is unreachable" as "you have no meetings" acts on a false
//     negative it has no way to detect, and unlike a human looking at a viewer
//     it will not notice the archive looked fuller yesterday;
//  3. entries are re-emitted VERBATIM. The exporter owns their shape; this code
//     reads exactly three fields (id, audioPath/artifactPath for the join key,
//     dateLabel and roomId for the filters) and never normalises the rest.
//
// It mounts under `published/`, which the AppAPI manifest already exposes as
// `^published\/.+$` at USER access level for GET,HEAD — so no manifest change
// and no re-registration. AppAPI's proxy forwards the query string, so the
// filter arrives intact.

// meetingsListPath is the archive-relative path the proxy forwards here. It sits
// beside catalog.json rather than under a new prefix so it inherits the
// manifest route, the USER access level and the per-caller proxy identity that
// already govern the published surface.
const meetingsListPath = "meetings-list"

// meetingsListFilter is the narrowing a request asks for. A zero value matches
// everything, which is what a bare `published/meetings-list` means.
type meetingsListFilter struct {
	// from and to are INCLUSIVE bounds on a meeting's dateLabel, compared in
	// the label's own zone-less wall-clock space.
	//
	// No timezone is parsed or applied, deliberately (D-484): dateLabel does not
	// know its zone, so asserting one here would invent an instant the data
	// cannot justify. The comparison space is therefore the same one the CLI
	// already filters in, and server and client agree by construction rather
	// than by coincidence.
	from    time.Time
	hasFrom bool
	to      time.Time
	hasTo   bool
	// room is a room id exactly as `cassini meetings rooms` prints it. Matched
	// on equality only: the id is an opaque one-way derivation, so prefix or
	// substring matching it would be meaningless.
	room string
}

func (f meetingsListFilter) active() bool {
	return f.hasFrom || f.hasTo || f.room != ""
}

// meetingsListStampLayout is how the filter echoes a parsed bound back.
const meetingsListStampLayout = "2006-01-02 15:04:05"

// meetingsListDateLayouts are the dateLabel forms the exporter emits, most
// precise first. Kept identical to the CLI's meetingDateLabelLayouts so this
// endpoint accepts exactly what the catalog prints — a caller must never have
// to reformat a date it read out of a listing to filter by it.
var meetingsListDateLayouts = []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"}

// meetingsListBadRequest is a caller error: something they typed is wrong, and
// they can fix it. Distinguished from a substrate failure so the handler can
// answer 400 rather than 502 — telling an agent "Nextcloud is down" when it
// actually swapped two dates would send it retrying forever.
type meetingsListBadRequest struct{ msg string }

func (e *meetingsListBadRequest) Error() string { return e.msg }

// parseMeetingsListFilter validates query parameters into a filter.
func parseMeetingsListFilter(query url.Values) (meetingsListFilter, error) {
	var filter meetingsListFilter
	var err error
	if raw := strings.TrimSpace(query.Get("from")); raw != "" {
		filter.from, err = parseMeetingsListBound(raw, false)
		if err != nil {
			return meetingsListFilter{}, fmt.Errorf("from: %w", err)
		}
		filter.hasFrom = true
	}
	if raw := strings.TrimSpace(query.Get("to")); raw != "" {
		filter.to, err = parseMeetingsListBound(raw, true)
		if err != nil {
			return meetingsListFilter{}, fmt.Errorf("to: %w", err)
		}
		filter.hasTo = true
	}
	if filter.hasFrom && filter.hasTo && filter.to.Before(filter.from) {
		// An inverted range can only ever match nothing, and answering it with
		// an empty list would look identical to "you may read no recordings" —
		// a far more alarming answer than "you typed the dates the wrong way
		// round".
		return meetingsListFilter{}, &meetingsListBadRequest{fmt.Sprintf(
			"from %s is after to %s, which can never match anything; did you swap them?",
			filter.from.Format(meetingsListStampLayout), filter.to.Format(meetingsListStampLayout))}
	}
	filter.room = strings.TrimSpace(query.Get("room"))
	return filter, nil
}

// parseMeetingsListBound parses one end of the range, accepting exactly the
// layouts the catalog's own dateLabel uses.
//
// A bare date means the whole day: 00:00:00 at the start of a range and
// 23:59:59 at the end. `from=2026-08-01&to=2026-08-31` has to mean August,
// which is what anyone writing it intends; a `to` that stopped at midnight
// would silently drop the last day.
func parseMeetingsListBound(value string, endOfRange bool) (time.Time, error) {
	for _, layout := range meetingsListDateLayouts {
		at, err := time.Parse(layout, value)
		if err != nil {
			continue
		}
		if endOfRange && layout == "2006-01-02" {
			return at.Add(24*time.Hour - time.Second), nil
		}
		return at, nil
	}
	return time.Time{}, &meetingsListBadRequest{fmt.Sprintf(
		"%q is not a date this catalog uses; write it as 2026-08-11, "+
			"2026-08-11 14:30 or 2026-08-11 14:30:05 (no timezone — meeting dates do not carry one)",
		oneLineListField(value))}
}

// oneLineListField keeps a echoed-back value from breaking the response or a
// log line, and bounds how much of a caller's input is reflected.
func oneLineListField(value string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, value)
	if len(cleaned) > 80 {
		return cleaned[:80] + "…"
	}
	return cleaned
}

// meetingsListResponse is the endpoint's body.
//
// `version` and `meetings` ARE the catalog envelope, byte-compatible with
// catalog.json — an existing viewer or CLI decodes this with no change, and
// both ignore the extra fields (the viewer rebuilds from a field allowlist, the
// CLI decodes into a struct). The extra fields are therefore additive for old
// clients and informative for new ones.
type meetingsListResponse struct {
	Version  string            `json:"version"`
	Meetings []json.RawMessage `json:"meetings"`
	// Filter echoes what the server actually applied, so a short list is
	// self-explaining rather than mysterious.
	Filter *meetingsListFilterEcho `json:"filter,omitempty"`
	// Excluded counts entries the filter removed. Present only when a filter
	// ran, so an unfiltered listing does not carry a meaningless zero.
	Excluded *meetingsListExcluded `json:"excluded,omitempty"`
}

type meetingsListFilterEcho struct {
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	Room string `json:"room,omitempty"`
}

// meetingsListExcluded explains a short list.
type meetingsListExcluded struct {
	// Total is every entry the filter removed, for any reason.
	Total int `json:"total"`
	// Undated is the subset dropped because a date bound was set and their
	// dateLabel could not be parsed. It deserves its own number because nothing
	// the caller typed is wrong: those meetings are readable and simply cannot
	// be placed in time.
	//
	// This is the endpoint's honesty about a live archive defect. A published
	// dateLabel can be a raw filename slug rather than a date, so a date-range
	// query silently omits meetings the caller can read. Reporting the count
	// turns that from a wrong answer into a partial one the caller can notice.
	Undated int `json:"undated"`
}

// applyMeetingsListFilter narrows the entries of a resolved catalog.
//
// Undated entries are dropped explicitly whenever a date bound is set, rather
// than compared as a zero time. A zero timestamp fails every `from` and matches
// every `to`, so the same meeting would appear or vanish depending on which end
// of the range the caller happened to send — a silent, direction-dependent lie.
// Dropping them and saying how many is the only answer true in both directions.
func applyMeetingsListFilter(entries []json.RawMessage, filter meetingsListFilter) ([]json.RawMessage, meetingsListExcluded) {
	kept := make([]json.RawMessage, 0, len(entries))
	var excluded meetingsListExcluded
	dated := filter.hasFrom || filter.hasTo

	for _, entry := range entries {
		var probe struct {
			DateLabel string `json:"dateLabel"`
			RoomID    string `json:"roomId"`
		}
		// A malformed entry is not this endpoint's to adjudicate — the exporter
		// owns the shape. It simply cannot be placed in time or in a room, so an
		// active filter excludes it exactly like an undated one.
		_ = json.Unmarshal(entry, &probe)

		// The room predicate runs FIRST so the undated count means what its
		// field says: the meetings THIS caller lost to the date range. Counting
		// an undated meeting from another room would suggest re-querying without
		// the dates, which would surface nothing extra.
		if filter.room != "" && strings.TrimSpace(probe.RoomID) != filter.room {
			excluded.Total++
			continue
		}
		if dated {
			at, ok := parseMeetingsListDateLabel(probe.DateLabel)
			if !ok {
				excluded.Total++
				excluded.Undated++
				continue
			}
			if filter.hasFrom && at.Before(filter.from) {
				excluded.Total++
				continue
			}
			if filter.hasTo && at.After(filter.to) {
				excluded.Total++
				continue
			}
		}
		kept = append(kept, entry)
	}
	return kept, excluded
}

// parseMeetingsListDateLabel reads a catalog dateLabel as a wall clock.
func parseMeetingsListDateLabel(label string) (time.Time, bool) {
	trimmed := strings.TrimSpace(label)
	if trimmed == "" {
		return time.Time{}, false
	}
	for _, layout := range meetingsListDateLayouts {
		if at, err := time.Parse(layout, trimmed); err == nil {
			return at, true
		}
	}
	return time.Time{}, false
}

// serveMeetingsList answers `published/meetings-list` for one caller.
//
// The visible set comes from resolveCatalogForCaller — the same resolution
// catalog.json uses, so this endpoint adds a query surface and NOT a second
// access-control path. What differs is only how an outcome maps to a status:
// here every substrate failure is loud, and 200 with an empty list means one
// thing only, that the caller may genuinely read no matching meeting.
func (c ExAppConfig) serveMeetingsList(ctx context.Context, w http.ResponseWriter, r *http.Request, client *http.Client, caller string, logger *log.Logger) {
	filter, err := parseMeetingsListFilter(r.URL.Query())
	if err != nil {
		// Validated BEFORE the network calls: a malformed date is the caller's
		// to fix, and making them wait on two round trips to Nextcloud to hear
		// it is pure cost.
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	resolved, outcome := c.resolveCatalogForCaller(ctx, client, caller, logger)
	switch outcome {
	case catalogResolveOK, catalogResolveNoArchive:
		// Both are real answers. NoArchive means nothing has ever been
		// published, which is an empty list rather than a failure.
	case catalogResolveUnavailable:
		writeJSONError(w, http.StatusBadGateway, "the recordings archive is unreachable; this is not an empty result")
		return
	case catalogResolveScanFailed:
		writeJSONError(w, http.StatusBadGateway, "could not determine which recordings you may read; this is not an empty result")
		return
	case catalogResolveNoMount:
		writeJSONError(w, http.StatusBadGateway, "the recordings folder is not available to your account; this is not an empty result")
		return
	default:
		// An outcome added later without a branch here must not fall through
		// into a 200 that would read as "no meetings".
		writeJSONError(w, http.StatusBadGateway, "the recordings archive could not be read; this is not an empty result")
		return
	}

	var envelope struct {
		Version  string            `json:"version"`
		Meetings []json.RawMessage `json:"meetings"`
	}
	if err := json.Unmarshal(resolved.body, &envelope); err != nil {
		// resolved.body is what serveFilteredCatalog would have served, so a
		// parse failure here is our own output being malformed, not the
		// caller's input. Loud, for the same reason as every branch above.
		if logger != nil {
			logger.Printf("meetings list: parse resolved catalog caller=%s: %v", caller, err)
		}
		writeJSONError(w, http.StatusBadGateway, "the recordings archive could not be read; this is not an empty result")
		return
	}

	response := meetingsListResponse{Version: envelope.Version, Meetings: envelope.Meetings}
	if response.Version == "" {
		response.Version = catalogSchemaVersion
	}
	if filter.active() {
		kept, excluded := applyMeetingsListFilter(envelope.Meetings, filter)
		response.Meetings = kept
		response.Excluded = &excluded
		echo := meetingsListFilterEcho{Room: filter.room}
		if filter.hasFrom {
			echo.From = filter.from.Format(meetingsListStampLayout)
		}
		if filter.hasTo {
			echo.To = filter.to.Format(meetingsListStampLayout)
		}
		response.Filter = &echo
	}
	// Never nil: `meetings` is an array in the wire format, and a null there
	// breaks a client that iterates it without a guard.
	if response.Meetings == nil {
		response.Meetings = []json.RawMessage{}
	}

	body, err := json.Marshal(response)
	if err != nil {
		if logger != nil {
			logger.Printf("meetings list: encode response caller=%s: %v", caller, err)
		}
		writeJSONError(w, http.StatusInternalServerError, "could not encode the meeting list")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// The list changes after every publish, and AppAPI's proxy would otherwise
	// give this response a one-hour browser freshness window — hiding newly
	// published meetings, and pinning a stale visible set after a revocation.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set(ncFilesSourceHeader, ncFilesSourceValue)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
