package cassini

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// `cassini meetings search` — find the moments where something was said.
//
// It reports REFERENCES, never transcript text: which meeting, which speaker,
// and where in the recording. Reading what was actually said stays with
// `meetings context`, which fetches the `.opus` AS THE CALLER — so the words
// still cross Nextcloud's own permission check on the way out, and a bug here
// can at worst reveal that something exists.
//
// There is no fallback to a local scan. An app that does not serve the route
// says so and the command stops, because inventing an answer from a different
// method would report a different question's result under this one's name.

// meetingsSearchPath is the app's search route, relative to the proxied root.
const meetingsSearchPath = "published/search"

func runMeetingsSearch(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var cfg meetingsConfig
	fs := flag.NewFlagSet("cassini meetings search", flag.ContinueOnError)
	fs.SetOutput(stderr)
	registerMeetingsConnectionFlags(fs, &cfg)
	speaker := fs.String("speaker", "", "only moments spoken by this speaker id")
	limit := fs.Int("limit", 0, "how many moments to return (default 20, max 100)")
	noAliases := fs.Bool("no-aliases", false,
		"do not also search the spellings transcription produces for a name\n(by default, searching for a project name finds it however it was misheard)")
	asJSON := fs.Bool("json", false, "emit the server's answer as JSON")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini meetings search "<words>" [--speaker ID] [--limit N] [--json]

Find where something was said across the meetings you may read. Prints one
line per moment: which meeting, when in it, and who was speaking.

It reports where to look, never what was said. Use `+"`cassini meetings context <id>`"+`
to read a meeting, which fetches it as you and so stays inside Nextcloud's
permissions.

`+"\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(meetingsParseArgs(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	// Exactly one positional, like `fetch`: Go's flag package stops parsing at
	// the first non-flag argument, so a multi-word query has to arrive quoted or
	// every flag after it is silently ignored.
	if fs.NArg() != 1 {
		if fs.NArg() == 0 {
			fmt.Fprintf(stderr, "search configuration error: give something to search for, in quotes\n")
		} else {
			fmt.Fprintf(stderr, "search takes one quoted query, got %d arguments: %v\n", fs.NArg(), redactMeetingsArgs(fs.Args()))
			// Decide the hint from what the CALLER typed, not from the leftover:
			// a flag's own value is not flag-shaped, so leftovers cannot tell an
			// unquoted query from a misplaced flag.
			if searchQueryLooksUnquoted(args) {
				fmt.Fprintf(stderr, "hint=quote the whole query: cassini meetings search \"one two\"\n")
			} else {
				fmt.Fprintf(stderr, "hint=flags must come before the query, or the query must come last\n")
			}
		}
		fs.Usage()
		return 2
	}
	query := strings.TrimSpace(fs.Arg(0))
	if query == "" {
		fmt.Fprintf(stderr, "search configuration error: the query must not be empty\n")
		return 2
	}
	if err := resolveMeetingsConfig(fs, &cfg); err != nil {
		fmt.Fprintf(stderr, "search configuration error: %v\n", err)
		return 2
	}
	warnAboutInsecureTLS(stderr, cfg)

	client := newMeetingsClient(cfg)
	results, err := client.search(ctx, meetingsSearchRequest{
		Query: query, Speaker: strings.TrimSpace(*speaker), Limit: *limit, NoAliases: *noAliases,
	})
	if err != nil {
		if errors.Is(err, errMeetingsSearchUnavailable) {
			// Distinguished from every other failure: the app is reachable and
			// working, it just does not offer this. Retrying will not help, and
			// neither will checking permissions.
			fmt.Fprintf(stderr, "meetings search failed: this Cassini app does not offer search; ask an administrator to update it\n")
			return 1
		}
		return reportMeetingsError(stderr, "search", cfg, err)
	}

	if *asJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(results); err != nil {
			fmt.Fprintf(stderr, "search failed: write JSON: %v\n", err)
			return 1
		}
		return 0
	}

	fmt.Fprintf(stdout, "moments=%d caller=%s searched=%d of %d meeting(s) you can read\n",
		len(results.Hits), cfg.user, results.Coverage.Searched, results.Coverage.Visible)
	if results.Widened {
		// A widened search answered a different question from the one asked, so
		// it must not be reported as though it answered this one.
		fmt.Fprintf(stdout, "note=no single moment contained all of those words, so these contain some of them\n")
	}
	if aliasNote := describeSearchAliases(query, results.Searched); aliasNote != "" {
		fmt.Fprintf(stdout, "note=%s\n", aliasNote)
	}
	if results.Coverage.Searched < results.Coverage.Visible {
		// The honest sentence. Without it, "no moments" reads as "nobody said
		// that", when some of the caller's meetings were never indexed at all.
		fmt.Fprintf(stdout, "note=%d meeting(s) you can read are not in the search index, so this answer does not cover them; an administrator can run `cassini-operator backfill-search`\n",
			results.Coverage.Visible-results.Coverage.Searched)
	}
	if len(results.Hits) == 0 {
		if results.Coverage.Visible == 0 {
			fmt.Fprintln(stdout, "note=no recordings are visible to this account; this is also what a mis-provisioned recordings folder looks like")
			return 0
		}
		fmt.Fprintf(stdout, "note=nothing matched in the %d meeting(s) searched\n", results.Coverage.Searched)
		return 0
	}
	for _, hit := range results.Hits {
		fmt.Fprintf(stdout, "moment=%s at=%s speaker=%s matched=%s room=%s date=%s title=%s\n",
			blankMeetingsDash(hit.MeetingID),
			formatMeetingsSpan(hit.StartMS, hit.EndMS),
			blankMeetingsDash(hit.SpeakerID),
			blankMeetingsDash(hit.Matched),
			blankMeetingsDash(firstNonBlank(hit.RoomName, hit.RoomID)),
			blankMeetingsDash(hit.DateLabel),
			blankMeetingsDash(hit.Title))
	}
	fmt.Fprintf(stdout, "hint=read one with `cassini meetings context <meeting-id>`\n")
	return 0
}

// meetingsSearchRequest is what the caller asked for.
type meetingsSearchRequest struct {
	Query     string
	Speaker   string
	Limit     int
	NoAliases bool
}

// meetingsSearchResults mirrors the endpoint's answer. Note the absence of any
// text field: the server does not send transcript content and this does not
// invent a place to put it.
type meetingsSearchResults struct {
	Hits     []meetingsSearchHit `json:"hits"`
	Widened  bool                `json:"widened,omitempty"`
	Searched [][]string          `json:"searched,omitempty"`
	Coverage struct {
		Visible  int `json:"visible"`
		Searched int `json:"searched"`
	} `json:"coverage"`
}

type meetingsSearchHit struct {
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

// errMeetingsSearchUnavailable means the app does not serve the search route:
// one older than D-623, or one publishing to the local sink. Distinct from
// every other failure because no retry and no permission change will help.
var errMeetingsSearchUnavailable = errors.New("this Cassini app does not serve the search route")

func (c *meetingsClient) search(ctx context.Context, req meetingsSearchRequest) (meetingsSearchResults, error) {
	root, err := c.appRootURL()
	if err != nil {
		return meetingsSearchResults{}, err
	}
	target, err := root.Parse(meetingsSearchPath)
	if err != nil {
		return meetingsSearchResults{}, fmt.Errorf("build search URL: %w", err)
	}
	query := url.Values{}
	query.Set("q", req.Query)
	if req.Speaker != "" {
		query.Set("speaker", req.Speaker)
	}
	if req.Limit > 0 {
		query.Set("limit", fmt.Sprintf("%d", req.Limit))
	}
	if req.NoAliases {
		query.Set("aliases", "off")
	}
	target.RawQuery = query.Encode()

	body, _, err := c.readListing(ctx, target)
	if err != nil {
		var httpErr *meetingsHTTPError
		if errors.As(err, &httpErr) && httpErr.Status == http.StatusNotFound {
			return meetingsSearchResults{}, errMeetingsSearchUnavailable
		}
		return meetingsSearchResults{}, err
	}
	var results meetingsSearchResults
	if err := json.Unmarshal(body, &results); err != nil {
		return meetingsSearchResults{}, fmt.Errorf(
			"parse search results from %s: %w", meetingsTargetLabel(target), err)
	}
	return results, nil
}

// describeSearchAliases explains a hit on a spelling the caller did not type.
//
// Only when something was actually expanded: saying "we also searched for
// cassini" when that is what was typed is noise.
func describeSearchAliases(query string, groups [][]string) string {
	typed := map[string]bool{}
	for _, word := range strings.Fields(strings.ToLower(query)) {
		typed[word] = true
	}
	var extra []string
	for _, group := range groups {
		if len(group) < 2 {
			continue
		}
		for _, variant := range group {
			if !typed[strings.ToLower(variant)] {
				extra = append(extra, variant)
			}
		}
	}
	if len(extra) == 0 {
		return ""
	}
	shown := extra
	if len(shown) > 6 {
		shown = shown[:6]
	}
	return "also searched the spellings transcription produces for those names: " +
		strings.Join(shown, ", ") + " — a moment marked matched=alias contains one of these, not what you typed"
}

// formatMeetingsTimestamp renders a position in a recording as h:mm:ss, which
// is what a person reads off a player.
func formatMeetingsTimestamp(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	totalSeconds := ms / 1000
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%d:%02d", minutes, seconds)
}

// formatMeetingsSpan renders where in a recording to listen.
//
// A span rather than an instant, because that is what the answer actually
// supports: the words matched somewhere inside this stretch of speech and the
// index cannot say where. Printing only the start would read as "they said it
// at 0:01" when the match may be most of a window later — false precision
// dressed as a citation.
func formatMeetingsSpan(startMS, endMS int64) string {
	start := formatMeetingsTimestamp(startMS)
	if endMS <= startMS {
		return start
	}
	return start + "-" + formatMeetingsTimestamp(endMS)
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// searchQueryLooksUnquoted reports whether the caller began with two bare words,
// which means they typed a multi-word query without quoting it. Anything else
// that leaves surplus arguments is a flag in the wrong place.
func searchQueryLooksUnquoted(args []string) bool {
	return len(args) >= 2 &&
		!strings.HasPrefix(args[0], "-") &&
		!strings.HasPrefix(args[1], "-")
}
