package operator

import (
	"context"
	"encoding/json"
	"errors"
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
	MeetingID       string `json:"meetingId"`
	Title           string `json:"title,omitempty"`
	DateLabel       string `json:"dateLabel,omitempty"`
	RoomID          string `json:"roomId,omitempty"`
	RoomName        string `json:"roomName,omitempty"`
	RoomDisplayName string `json:"roomDisplayName,omitempty"`
	SegmentID       string `json:"segmentId"`
	StartMS         int64  `json:"startMs"`
	EndMS           int64  `json:"endMs"`
	SpeakerID       string `json:"speakerId,omitempty"`
	Matched         string `json:"matched"`
	// Marks are the tags whose time-range marks this hit's range overlaps
	// (D-737). Absent, not empty, when the meeting's marks are unknown: "no
	// marks here" and "marks unknown" are different answers.
	Marks *[]searchHitMark `json:"marks,omitempty"`
	// Snippet is a BOUNDED cut of the matching segment, centred on the term that
	// hit — never the whole segment, and never the whole transcript.
	//
	// This field is why the visibility join in search_query.go matters more than
	// it used to. Before D-736 a filtering bug leaked the existence of a
	// meeting; now it would leak words. The join is fail-closed by construction
	// (json_each + INNER JOIN, so an empty visible set yields no rows even with
	// the MATCH removed), and TestSearchResponseNeverCarriesInvisibleText pins
	// the property this field puts at risk.
	Snippet string `json:"snippet,omitempty"`
}

// searchResponseCoverage is the honesty field.
type searchResponseCoverage struct {
	// Visible is how many meetings this caller may read.
	Visible int `json:"visible"`
	// Searched is how many of those the index actually holds rows for. When it
	// is lower than Visible, the answer is partial and says so.
	Searched int `json:"searched"`
}

// searchParams is one query, however it arrived: the search route's query
// string or an MCP tool's arguments.
type searchParams struct {
	// Query is the words to look for. Required.
	Query string
	// Limit caps the hits; zero means the store's default.
	Limit int
	// PerMeeting caps how many hits one meeting may contribute. Zero means no
	// cap, which is what the CLI's flat list wants; the meeting list asks for
	// one, because there a meeting crowded out of the page is a meeting the
	// caller is told does not match.
	PerMeeting int
	// Room narrows the same way tag does, and for the same reason: a viewer or
	// an agent that has narrowed to one room is asking a question about that
	// room, and answering it from a page ranked over every room can return
	// nothing while matches sit just below the cut. Narrowing the visible set
	// first makes Limit mean what it says.
	Room string
	// Tag is a label, any case, or a tag id. Validated by validateTagParam.
	Tag     string
	Speaker string
	// NoAliases turns off searching for the spellings transcription produces
	// for a word.
	NoAliases bool
}

// serveSearch answers one query for one caller.
func (c ExAppConfig) serveSearch(
	ctx context.Context, w http.ResponseWriter, r *http.Request,
	client *http.Client, caller string, search searchDeps, logger *log.Logger,
) {
	params := searchParams{
		Query:     r.URL.Query().Get("q"),
		Room:      strings.TrimSpace(r.URL.Query().Get("room")),
		Speaker:   strings.TrimSpace(r.URL.Query().Get("speaker")),
		NoAliases: r.URL.Query().Get("aliases") == "off",
	}
	if strings.TrimSpace(params.Query) == "" {
		writeJSONError(w, http.StatusBadRequest, "give something to search for in the q parameter")
		return
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeJSONError(w, http.StatusBadRequest, "limit must be a positive whole number")
			return
		}
		params.Limit = parsed
	}
	tag, err := parseTagParam(r.URL.Query())
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	params.Tag = tag
	if raw := strings.TrimSpace(r.URL.Query().Get("perMeeting")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeJSONError(w, http.StatusBadRequest, "perMeeting must be a positive whole number")
			return
		}
		params.PerMeeting = parsed
	}

	response, err := c.searchMeetings(ctx, client, caller, params, search, logger)
	if err != nil {
		writeReadFailure(w, r, err)
		return
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

// searchMeetings runs one query for one caller. Every failure is a
// *readFailure; nothing the caller typed is logged.
func (c ExAppConfig) searchMeetings(
	ctx context.Context, client *http.Client, caller string, params searchParams, search searchDeps, logger *log.Logger,
) (searchResponse, error) {
	index := search.index
	query := strings.TrimSpace(params.Query)
	if query == "" {
		return searchResponse{}, readFail(http.StatusBadRequest, "give something to search for")
	}
	if params.Limit < 0 {
		return searchResponse{}, readFail(http.StatusBadRequest, "limit must be a positive whole number")
	}
	if params.PerMeeting < 0 {
		return searchResponse{}, readFail(http.StatusBadRequest, "perMeeting must be a positive whole number")
	}
	tag, err := validateTagParam(params.Tag)
	if err != nil {
		return searchResponse{}, readFail(http.StatusBadRequest, err.Error())
	}
	room := strings.TrimSpace(params.Room)
	// Before the expensive part, which is the point: a refused request must not
	// have already cost a PROPFIND and a catalog GET against Nextcloud.
	if allowed, wait := search.limiter.allow(caller); !allowed {
		return searchResponse{}, readRateLimited(wait,
			"too many searches; each one asks Nextcloud what you may read, so they are rate limited — retry shortly")
	}
	if index == nil {
		// Not an empty result: the index is unavailable, and an agent must be
		// able to tell "ask again later" from "there is nothing".
		return searchResponse{}, readFail(http.StatusServiceUnavailable,
			"the search index is not available on this deployment; this is not an empty result")
	}
	if tag != "" && search.annotations == nil {
		// Silently unnarrowed would be a false answer.
		return searchResponse{}, readFail(http.StatusServiceUnavailable, tagIndexUnavailableMessage)
	}

	entries, err := c.visibleMeetings(ctx, client, caller, logger, "search")
	if err != nil {
		return searchResponse{}, err
	}

	visible := make([]string, 0, len(entries))
	byOpusName := make(map[string]catalogHydration, len(entries))
	for _, entry := range entries {
		if entry.opusName == "" {
			continue
		}
		// The room is part of the caller's own catalog, so this narrows the
		// visible set BEFORE the statement rather than filtering a ranked page.
		if room != "" && entry.roomID != room {
			continue
		}
		visible = append(visible, entry.opusName)
		byOpusName[entry.opusName] = entry
	}

	// A tag narrows the VISIBLE SET before the statement, never a ranked page
	// afterwards, which would miss a tagged meeting ranked below the page.
	bound := visible
	if tag != "" {
		tagged, err := search.annotations.taggedMeetings(ctx, tag, visible)
		if err != nil {
			if logger != nil {
				logger.Printf("search: tag narrowing failed caller=%s: %v", caller, err)
			}
			return searchResponse{}, readFail(http.StatusBadGateway, tagIndexUnreadableMessage)
		}
		bound = make([]string, 0, len(tagged))
		for _, name := range visible {
			if tagged[name] {
				bound = append(bound, name)
			}
		}
	}

	results, err := index.Search(ctx, searchRequest{
		Text:       query,
		Visible:    bound,
		SpeakerID:  strings.TrimSpace(params.Speaker),
		Limit:      params.Limit,
		PerMeeting: params.PerMeeting,
		UseAliases: !params.NoAliases,
		AliasIndex: search.aliasIndex(),
	})
	if err != nil {
		// Exactly one of these is the caller's fault. Everything else — a closed
		// handle, a locked database, a table missing after a partial rebuild —
		// is an outage, and answering 400 for it would tell an agent to rewrite
		// a query that was never the problem, so it would never retry.
		if errors.Is(err, errSearchNoWords) {
			return searchResponse{}, readFail(http.StatusBadRequest, err.Error())
		}
		if logger != nil {
			logger.Printf("search: query failed caller=%s: %v", caller, err)
		}
		return searchResponse{}, readFail(http.StatusBadGateway, "the search index could not be queried; this is not an empty result")
	}

	coverage, err := index.CoverageFor(ctx, visible)
	if err != nil {
		if logger != nil {
			logger.Printf("search: coverage caller=%s: %v", caller, err)
		}
		return searchResponse{}, readFail(http.StatusBadGateway, "the search index could not be read; this is not an empty result")
	}

	// Every hit's meeting is in the caller's visible set: the statement was
	// bound to it.
	var marks [][]searchHitMark
	if search.annotations != nil {
		spans := make([]markSpan, 0, len(results.Hits))
		for _, hit := range results.Hits {
			spans = append(spans, markSpan{opusName: hit.OpusName, startMS: hit.StartMS, endMS: hit.EndMS})
		}
		marks, err = search.annotations.marksOverlapping(ctx, spans)
		if err != nil {
			if logger != nil {
				logger.Printf("search: marks for hits caller=%s: %v", caller, err)
			}
			return searchResponse{}, readFail(http.StatusBadGateway, tagIndexUnreadableMessage)
		}
	}

	response := searchResponse{
		Hits:     make([]searchResponseHit, 0, len(results.Hits)),
		Widened:  results.Widened,
		Searched: results.Groups,
		Coverage: searchResponseCoverage{Visible: len(visible), Searched: coverage},
	}
	for i, hit := range results.Hits {
		entry := byOpusName[hit.OpusName]
		var hitMarks *[]searchHitMark
		if i < len(marks) && marks[i] != nil {
			hitMarks = &marks[i]
		}
		response.Hits = append(response.Hits, searchResponseHit{
			MeetingID:       entry.id,
			Title:           entry.title,
			DateLabel:       entry.dateLabel,
			RoomID:          entry.roomID,
			RoomName:        entry.roomName,
			RoomDisplayName: entry.roomDisplayName,
			SegmentID:       hit.SegmentID,
			StartMS:         hit.StartMS,
			EndMS:           hit.EndMS,
			SpeakerID:       hit.SpeakerID,
			Matched:         hit.Matched,
			Marks:           hitMarks,
			Snippet:         snippetAround(hit.Text, flattenSearchGroups(results.Groups)),
		})
	}
	return response, nil
}

// resolveVisibleMeetings is visibleMeetings for a handler that answers its own
// failures: on any outcome that is not an answer it has already written the
// loud error and returns false.
func (c ExAppConfig) resolveVisibleMeetings(
	ctx context.Context, w http.ResponseWriter, client *http.Client, caller string, logger *log.Logger, route string,
) ([]catalogHydration, bool) {
	entries, err := c.visibleMeetings(ctx, client, caller, logger, route)
	if err != nil {
		writeReadFailure(w, nil, err)
		return nil, false
	}
	return entries, true
}

// visibleMeetings is the caller's readable meetings, resolved the way search
// has always resolved them: resolveCatalogForCaller, the one access-control
// path, then the caller's own catalog entries. Shared by search, the tag
// vocabulary (D-737) and the MCP tools so they cannot disagree about what a
// caller may read.
//
// Every failure is a *readFailure. route names the caller in the log; nothing
// the caller typed is logged.
func (c ExAppConfig) visibleMeetings(
	ctx context.Context, client *http.Client, caller string, logger *log.Logger, route string,
) ([]catalogHydration, error) {
	resolved, err := c.resolveCatalogForCaller(ctx, client, caller, logger)
	if err != nil {
		return nil, readFail(http.StatusBadGateway, "could not determine which recordings you may read; this is not an empty result")
	}

	entries, err := decodeCatalogEntries(resolved)
	if err != nil {
		if logger != nil {
			logger.Printf("%s: parse resolved catalog caller=%s: %v", route, caller, err)
		}
		return nil, readFail(http.StatusBadGateway, "the recordings archive could not be read; this is not an empty result")
	}
	return entries, nil
}

// visibleOpusNames is the join keys of a caller's entries, skipping any that
// cannot be keyed.
func visibleOpusNames(entries []catalogHydration) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.opusName != "" {
			names = append(names, entry.opusName)
		}
	}
	return names
}

// catalogEntryOpusName is the join key of one catalog entry: the basename of
// its audioPath, or of its artifactPath when it has none. The index, the
// projection and the visibility scan all key on this string.
func catalogEntryOpusName(audioPath, artifactPath string) string {
	ref := strings.TrimSpace(audioPath)
	if ref == "" {
		ref = strings.TrimSpace(artifactPath)
	}
	if ref == "" {
		return ""
	}
	if name := logicalMeetingName(path.Base(ref)); name != "" {
		return name
	}
	return path.Base(ref)
}

// catalogHydration is what the caller's own catalog says about one meeting.
type catalogHydration struct {
	id              string
	opusName        string
	title           string
	dateLabel       string
	roomID          string
	roomName        string
	roomDisplayName string
}

// decodeCatalogEntries reads the fields a hit is hydrated with, keyed by the
// `.opus` basename the index joins on.
func decodeCatalogEntries(raw []byte) ([]catalogHydration, error) {
	var catalog struct {
		Meetings []struct {
			ID              string `json:"id"`
			Title           string `json:"title"`
			DateLabel       string `json:"dateLabel"`
			RoomID          string `json:"roomId"`
			RoomName        string `json:"roomName"`
			RoomDisplayName string `json:"roomDisplayName"`
			AudioPath       string `json:"audioPath"`
			MeetingPath     string `json:"meetingPath"`
			ArtifactPath    string `json:"artifactPath"`
		} `json:"meetings"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return nil, err
	}
	entries := make([]catalogHydration, 0, len(catalog.Meetings))
	for _, meeting := range catalog.Meetings {
		entries = append(entries, catalogHydration{
			id: strings.TrimSpace(meeting.ID), opusName: catalogEntryOpusName(meetingPath(meeting.AudioPath, meeting.MeetingPath), meeting.ArtifactPath),
			title: meeting.Title, dateLabel: meeting.DateLabel,
			roomID: meeting.RoomID, roomName: meeting.RoomName, roomDisplayName: meeting.RoomDisplayName,
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

// searchDeps is what the search route needs beyond the caller's identity: the
// index, and the alias table as it is configured RIGHT NOW.
//
// The aliases arrive as a function rather than a value because they are
// operator settings: an edit through /settings must take effect on the next
// search, not the next restart. The index is a handle because it has a
// lifetime; the aliases are a snapshot because they do not.
type searchDeps struct {
	index    *searchStore
	metadata *meetingMetadataStore
	aliases  func() [][]string
	// limiter bounds how often one caller can make this app talk to Nextcloud.
	// Nil outside a running operator, which the limiter itself tolerates.
	limiter *searchRateLimiter
	// annotations is the tag index (D-737), for search and the meeting list.
	// Nil when it could not be opened: hits carry no `marks`, and `tag=` is 503.
	annotations       *annotationStore
	importAnnotations func(context.Context, string, []catalogHydration)
}

// aliasIndex merges the shipped groups with whatever the operator has
// configured. Built here rather than cached because the settings can change
// between requests and the table is small.
func (d searchDeps) aliasIndex() map[string][]string {
	var configured [][]string
	if d.aliases != nil {
		configured = d.aliases()
	}
	return mergeSearchAliasGroups(searchAliasGroups, configured)
}
