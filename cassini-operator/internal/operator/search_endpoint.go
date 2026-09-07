package operator

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"path"
	"strconv"
	"strings"
)

// `GET published/search?q=...` (D-623).
//
// Mounts beside published/meetings-list and published/catalog.json, inside the
// manifest's existing `^published\/.+$` route — so no info.xml change and no
// re-registration, at USER access level for GET,HEAD.
//
// ONE ACCESS-CONTROL PATH
//
// The visible set comes from resolveCatalogForCaller, the same resolution
// catalog.json and the meetings list use. Search adds a query surface, not a
// second way of deciding what a caller may read. The set is resolved per
// request and never cached: revocation, group changes and deletions propagate
// with no index maintenance, and caching would be unsound in the permissive
// direction because Nextcloud gives Cassini no permission-change signal.
//
// FAILURE IS LOUD, AND SILENCE MEANS ONE THING
//
// An agent that reads "the archive is unreachable" as "nothing matched" acts on
// a false negative it cannot detect. So every substrate failure is an error
// status, and 200 with no hits means exactly one thing: the caller may read no
// meeting containing those words.
//
// COVERAGE IS PART OF THE ANSWER
//
// A result says how many of the caller's readable meetings were actually
// searched. Without it "no match in the 12 meetings you can read" is a claim
// the index cannot support when three of them failed to ingest — and a partial
// answer a caller can see is worth more than a confident one they cannot check.
//
// THE QUERY STRING IS NEVER LOGGED
//
// requestLogger writes r.URL.RequestURI() to the operator's stderr, i.e. the
// container log, which the artifact-retention policy does not govern. A meeting
// list's filter values are innocuous there; `?q=severance package for Bob` is
// not. The handler therefore never echoes the query into a log line, and the
// route is registered on the logger's skip list.

// searchURLPath is the archive-relative path the proxy forwards here.
const searchURLPath = "search"

// searchResponse is the wire shape. There is no text field anywhere in it: the
// store cannot emit indexed text (content=”), and neither can this.
type searchResponse struct {
	// Hits are references — meeting, segment, when, who, and why it matched.
	Hits []searchResponseHit `json:"hits"`
	// Widened reports that requiring every word found nothing and the query was
	// retried as "any of them", which answers a different question.
	Widened bool `json:"widened,omitempty"`
	// Searched is what the query actually looked for after alias expansion, so
	// a hit on a mistranscription is explicable rather than mysterious.
	Searched [][]string `json:"searched,omitempty"`
	// Coverage is what the answer can honestly claim to have covered.
	Coverage searchResponseCoverage `json:"coverage"`
}

// searchResponseHit is one reference, hydrated with what the CALLER'S OWN
// catalog says about the meeting — never with anything read from the index,
// which deliberately holds no titles, dates or rooms.
type searchResponseHit struct {
	// MeetingID is the catalog id, which is what `meetings context` and the
	// viewer's deep links take. Deliberately not the `.opus` basename: those
	// coincide by convention only, and an agent piping the wrong one gets a 404
	// phrased as a denial.
	MeetingID string `json:"meetingId"`
	Title     string `json:"title,omitempty"`
	DateLabel string `json:"dateLabel,omitempty"`
	RoomID    string `json:"roomId,omitempty"`
	RoomName  string `json:"roomName,omitempty"`
	SegmentID string `json:"segmentId"`
	StartMS   int64  `json:"startMs"`
	EndMS     int64  `json:"endMs"`
	SpeakerID string `json:"speakerId,omitempty"`
	Matched   string `json:"matched"`
}

// searchResponseCoverage is the honesty field.
type searchResponseCoverage struct {
	// Visible is how many meetings this caller may read.
	Visible int `json:"visible"`
	// Searched is how many of those the index actually holds rows for. When it
	// is lower than Visible, the answer is partial and says so.
	Searched int `json:"searched"`
}

// serveSearch answers one query for one caller.
func (c ExAppConfig) serveSearch(
	ctx context.Context, w http.ResponseWriter, r *http.Request,
	client *http.Client, caller string, index *searchStore, logger *log.Logger,
) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeJSONError(w, http.StatusBadRequest, "give something to search for in the q parameter")
		return
	}
	limit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeJSONError(w, http.StatusBadRequest, "limit must be a positive whole number")
			return
		}
		limit = parsed
	}
	if index == nil {
		// Not an empty result: the index is unavailable, and an agent must be
		// able to tell "ask again later" from "there is nothing".
		writeJSONError(w, http.StatusServiceUnavailable,
			"the search index is not available on this deployment; this is not an empty result")
		return
	}

	resolved, outcome := c.resolveCatalogForCaller(ctx, client, caller, logger)
	switch outcome {
	case catalogResolveOK, catalogResolveNoArchive:
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
		writeJSONError(w, http.StatusBadGateway, "the recordings archive could not be read; this is not an empty result")
		return
	}

	entries, err := decodeCatalogEntries(resolved.body)
	if err != nil {
		if logger != nil {
			logger.Printf("search: parse resolved catalog caller=%s: %v", caller, err)
		}
		writeJSONError(w, http.StatusBadGateway, "the recordings archive could not be read; this is not an empty result")
		return
	}

	visible := make([]string, 0, len(entries))
	byOpusName := make(map[string]catalogHydration, len(entries))
	for _, entry := range entries {
		if entry.opusName == "" {
			continue
		}
		visible = append(visible, entry.opusName)
		byOpusName[entry.opusName] = entry
	}

	results, err := index.Search(ctx, searchRequest{
		Text:       query,
		Visible:    visible,
		SpeakerID:  strings.TrimSpace(r.URL.Query().Get("speaker")),
		Limit:      limit,
		UseAliases: r.URL.Query().Get("aliases") != "off",
	})
	if err != nil {
		// A query with no searchable words is the caller's to fix; anything else
		// here is ours, and neither is an empty result.
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	coverage, err := index.CoverageFor(ctx, visible)
	if err != nil {
		if logger != nil {
			logger.Printf("search: coverage caller=%s: %v", caller, err)
		}
		writeJSONError(w, http.StatusBadGateway, "the search index could not be read; this is not an empty result")
		return
	}

	response := searchResponse{
		Hits:     make([]searchResponseHit, 0, len(results.Hits)),
		Widened:  results.Widened,
		Searched: results.Groups,
		Coverage: searchResponseCoverage{Visible: len(visible), Searched: coverage},
	}
	for _, hit := range results.Hits {
		entry := byOpusName[hit.OpusName]
		response.Hits = append(response.Hits, searchResponseHit{
			MeetingID: entry.id,
			Title:     entry.title,
			DateLabel: entry.dateLabel,
			RoomID:    entry.roomID,
			RoomName:  entry.roomName,
			SegmentID: hit.SegmentID,
			StartMS:   hit.StartMS,
			EndMS:     hit.EndMS,
			SpeakerID: hit.SpeakerID,
			Matched:   hit.Matched,
		})
	}

	body, err := json.Marshal(response)
	if err != nil {
		if logger != nil {
			logger.Printf("search: encode response caller=%s: %v", caller, err)
		}
		writeJSONError(w, http.StatusInternalServerError, "could not encode the search results")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set(ncFilesSourceHeader, ncFilesSourceValue)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// catalogHydration is what the caller's own catalog says about one meeting.
type catalogHydration struct {
	id        string
	opusName  string
	title     string
	dateLabel string
	roomID    string
	roomName  string
}

// decodeCatalogEntries reads the fields a hit is hydrated with, keyed by the
// `.opus` basename the index joins on.
func decodeCatalogEntries(raw []byte) ([]catalogHydration, error) {
	var catalog struct {
		Meetings []struct {
			ID           string `json:"id"`
			Title        string `json:"title"`
			DateLabel    string `json:"dateLabel"`
			RoomID       string `json:"roomId"`
			RoomName     string `json:"roomName"`
			AudioPath    string `json:"audioPath"`
			ArtifactPath string `json:"artifactPath"`
		} `json:"meetings"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return nil, err
	}
	entries := make([]catalogHydration, 0, len(catalog.Meetings))
	for _, meeting := range catalog.Meetings {
		ref := strings.TrimSpace(meeting.AudioPath)
		if ref == "" {
			ref = strings.TrimSpace(meeting.ArtifactPath)
		}
		name := ""
		if ref != "" {
			name = path.Base(ref)
		}
		entries = append(entries, catalogHydration{
			id: strings.TrimSpace(meeting.ID), opusName: name,
			title: meeting.Title, dateLabel: meeting.DateLabel,
			roomID: meeting.RoomID, roomName: meeting.RoomName,
		})
	}
	return entries, nil
}

// CoverageFor counts how many of the caller's visible meetings the index
// actually holds rows for.
//
// Scoped to the caller rather than global: a count over the whole archive would
// tell them about meetings they cannot read, and would also be the wrong
// denominator for the claim the answer is making.
func (s *searchStore) CoverageFor(ctx context.Context, visible []string) (int, error) {
	if len(visible) == 0 {
		return 0, nil
	}
	encoded, err := json.Marshal(visible)
	if err != nil {
		return 0, err
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM meeting_index m
  JOIN json_each(?1) v ON v.value = m.opus_name
 WHERE m.state = ?2`, string(encoded), searchStateIndexed).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}
