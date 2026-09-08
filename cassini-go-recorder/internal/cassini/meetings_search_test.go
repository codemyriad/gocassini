package cassini

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

const meetingsTestSearchPath = "/index.php/apps/app_api/proxy/gocassini/published/search"

// serveSearch answers the search route with body; everything else 404s.
func serveSearchRoute(body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != meetingsTestSearchPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Cassini-Meeting-Source", "nextcloud-files")
		fmt.Fprint(w, body)
	}
}

const searchOneHit = `{
  "hits":[{"meetingId":"MEET-1","title":"Standup","dateLabel":"2026-09-01 09:00",
           "roomName":"Daily","segmentId":"seg_7","startMs":872000,"endMs":875500,
           "speakerId":"S1","matched":"exact"}],
  "coverage":{"visible":12,"searched":12}}`

func TestMeetingsSearchPrintsMomentsNotText(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveSearchRoute(searchOneHit))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "search", "acquisition")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "moment=MEET-1") {
		t.Errorf("stdout did not name the meeting:\n%s", stdout)
	}
	// A position a person can read off a player, not raw milliseconds.
	if !strings.Contains(stdout, "at=14:32") {
		t.Errorf("stdout did not render the timestamp:\n%s", stdout)
	}
	if !strings.Contains(stdout, "speaker=S1") || !strings.Contains(stdout, "matched=exact") {
		t.Errorf("stdout lost the reference detail:\n%s", stdout)
	}
	if !strings.Contains(stdout, "searched=12 of 12") {
		t.Errorf("stdout did not report coverage:\n%s", stdout)
	}
	if !strings.Contains(stdout, "meetings context") {
		t.Errorf("stdout should point at how to read one:\n%s", stdout)
	}
	// The query reaches the server as a query parameter.
	if !strings.Contains(fake.queries[0], "q=acquisition") {
		t.Errorf("query = %q, want the search term", fake.queries[0])
	}
}

// Partial coverage must be said out loud: without it, "nothing matched" reads
// as "nobody said that" when some meetings were never indexed.
func TestMeetingsSearchReportsPartialCoverage(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveSearchRoute(
		`{"hits":[],"coverage":{"visible":12,"searched":9}}`))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "search", "acquisition")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "3 meeting(s) you can read are not in the search index") {
		t.Errorf("stdout did not report the gap:\n%s", stdout)
	}
	if !strings.Contains(stdout, "backfill-search") {
		t.Errorf("stdout should say what fixes it:\n%s", stdout)
	}
}

// A widened search answered a different question, so it must say so.
func TestMeetingsSearchReportsWidening(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveSearchRoute(
		`{"hits":[{"meetingId":"MEET-1","segmentId":"s1","startMs":0,"endMs":900,"matched":"exact"}],
		  "widened":true,"coverage":{"visible":3,"searched":3}}`))

	_, stdout, _ := runMeetingsCLI(t, fake.server.URL, "search", "deployment migration")

	if !strings.Contains(stdout, "no single moment contained all of those words") {
		t.Errorf("stdout did not report that the search widened:\n%s", stdout)
	}
}

// An alias hit is explained, so "matched=alias" is not mysterious.
func TestMeetingsSearchExplainsAliasMatches(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveSearchRoute(
		`{"hits":[{"meetingId":"MEET-1","segmentId":"s1","startMs":0,"endMs":900,"matched":"alias"}],
		  "searched":[["cassini","casini","casino"]],"coverage":{"visible":1,"searched":1}}`))

	_, stdout, _ := runMeetingsCLI(t, fake.server.URL, "search", "cassini")

	if !strings.Contains(stdout, "casino") {
		t.Errorf("stdout did not show the spellings searched:\n%s", stdout)
	}
	if !strings.Contains(stdout, "matched=alias") {
		t.Errorf("stdout did not label the hit:\n%s", stdout)
	}
}

// Nothing to explain when nothing was expanded.
func TestMeetingsSearchIsQuietWhenNothingWasExpanded(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveSearchRoute(
		`{"hits":[],"searched":[["quarterly"]],"coverage":{"visible":1,"searched":1}}`))

	_, stdout, _ := runMeetingsCLI(t, fake.server.URL, "search", "quarterly")

	if strings.Contains(stdout, "also searched the spellings") {
		t.Errorf("stdout explained an expansion that did not happen:\n%s", stdout)
	}
}

// An app without the route says so, and does not fall back to some other
// method that would answer a different question under this one's name.
func TestMeetingsSearchReportsAnAppWithoutSearch(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "search", "acquisition")

	if code == 0 {
		t.Fatalf("exit=0 without the route; stdout=%q", stdout)
	}
	if !strings.Contains(stderr, "does not offer search") {
		t.Errorf("stderr should name the real problem:\n%s", stderr)
	}
	if strings.Contains(stderr, "permissions") {
		t.Errorf("a missing route is not a permissions problem:\n%s", stderr)
	}
}

// A substrate failure is an outage, not an empty result.
func TestMeetingsSearchReportsAnOutage(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, `{"error":"the recordings archive is unreachable; this is not an empty result"}`)
	})

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "search", "acquisition")

	if code == 0 {
		t.Fatalf("exit=0 on an outage; stdout=%q", stdout)
	}
	if !strings.Contains(stderr, "outage") {
		t.Errorf("stderr did not name this an outage:\n%s", stderr)
	}
	if strings.Contains(stdout, "nothing matched") {
		t.Errorf("an outage was reported as no matches:\n%s", stdout)
	}
}

// The search term must never appear in error output: it is the one query value
// that is genuinely sensitive.
func TestMeetingsSearchKeepsTheQueryOutOfErrors(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, `{"error":"unreachable"}`)
	})

	_, _, stderr := runMeetingsCLI(t, fake.server.URL, "search", "severance package for Bob")

	for _, leaked := range []string{"severance", "Bob", "q="} {
		if strings.Contains(stderr, leaked) {
			t.Errorf("error output leaked %q from the query:\n%s", leaked, stderr)
		}
	}
}

func TestMeetingsSearchRequiresATerm(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveSearchRoute(searchOneHit))
	if code, _, _ := runMeetingsCLI(t, fake.server.URL, "search"); code != 2 {
		t.Fatalf("exit = %d, want 2 for a missing search term", code)
	}
}

func TestMeetingsSearchPassesFlagsThrough(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveSearchRoute(searchOneHit))

	if code, _, stderr := runMeetingsCLI(t, fake.server.URL,
		"search", "acquisition", "--speaker", "S2", "--limit", "5", "--no-aliases"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	query := fake.queries[0]
	for _, want := range []string{"speaker=S2", "limit=5", "aliases=off"} {
		if !strings.Contains(query, want) {
			t.Errorf("query %q is missing %q", query, want)
		}
	}
}

// --json re-emits the server's answer, which carries no transcript text.
func TestMeetingsSearchJSONCarriesNoText(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveSearchRoute(searchOneHit))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "search", "acquisition", "--json")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, `"meetingId": "MEET-1"`) {
		t.Errorf("json did not carry the reference:\n%s", stdout)
	}
	if strings.Contains(strings.ToLower(stdout), `"text"`) {
		t.Errorf("json carries a text field, which search never returns:\n%s", stdout)
	}
}

func TestFormatMeetingsTimestamp(t *testing.T) {
	for _, tc := range []struct {
		ms   int64
		want string
	}{
		{0, "0:00"},
		{5_000, "0:05"},
		{872_000, "14:32"},
		{3_600_000, "1:00:00"},
		{3_930_000, "1:05:30"},
		{-1, "0:00"},
	} {
		if got := formatMeetingsTimestamp(tc.ms); got != tc.want {
			t.Errorf("formatMeetingsTimestamp(%d) = %q, want %q", tc.ms, got, tc.want)
		}
	}
}

// Go's flag package stops at the first non-flag argument, so a flag placed
// after the query is silently ignored. The command refuses rather than running
// a half-configured search, and says which orderings work.
func TestMeetingsSearchRefusesFlagsAfterTheQuery(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveSearchRoute(searchOneHit))

	code, _, stderr := runMeetingsCLI(t, fake.server.URL, "search", "--speaker", "S2", "acquisition")

	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "flags must come before the query") {
		t.Errorf("stderr should say which ordering works:\n%s", stderr)
	}
}

// A multi-word query has to be quoted, and saying so beats a confusing
// argument-count error.
func TestMeetingsSearchAsksForAQuotedQuery(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveSearchRoute(searchOneHit))

	code, _, stderr := runMeetingsCLI(t, fake.server.URL, "search", "deployment", "migration")

	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "quote the whole query") {
		t.Errorf("stderr should ask for quoting:\n%s", stderr)
	}
}
