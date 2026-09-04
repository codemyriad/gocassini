package cassini

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// The CLI prefers the app's server-side list route (D-701) and falls back to
// catalog.json only when the app does not serve it. These tests pin which of
// the two answered, because the difference is not cosmetic: the route
// distinguishes a substrate failure from an empty archive and the catalog
// cannot, so silently falling back would restore the ambiguity it removes.

// serveMeetingsList answers the list route, echoing back what it was asked so a
// test can assert the query reached the server. Everything else 404s.
func serveMeetingsList(t *testing.T, body func(query string) string) func(http.ResponseWriter, *http.Request) {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != meetingsTestListPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Cassini-Meeting-Source", "nextcloud-files")
		fmt.Fprint(w, body(r.URL.RawQuery))
	}
}

func TestMeetingsListPrefersTheServerRouteAndSendsTheFilter(t *testing.T) {
	const filtered = `{
  "version": "cassini.viewer.catalog.v1",
  "meetings": [
    {"id": "NEWER", "title": "Daily Standup", "dateLabel": "2026-08-11 10:32",
     "audioPath": "./meetings/NEWER.opus"}
  ],
  "filter": {"from": "2026-08-11 00:00:00"},
  "excluded": {"total": 3, "undated": 2}
}`
	fake := newMeetingsFakeNextcloud(t, serveMeetingsList(t, func(string) string { return filtered }))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL,
		"list", "--from", "2026-08-11", "--room", "rm_0123456789abcdef")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	// One request: the route answered, so the catalog is never asked for.
	if len(fake.requests) != 1 || fake.requests[0] != meetingsTestListPath {
		t.Fatalf("requested %v, want only the list route", fake.requests)
	}
	// The filter must reach the server in the stamp layout it parses back. A
	// bare --from date means the start of that day.
	query := fake.queries[0]
	for _, want := range []string{"from=2026-08-11+00%3A00%3A00", "room=rm_0123456789abcdef"} {
		if !strings.Contains(query, want) {
			t.Errorf("query %q is missing %q", query, want)
		}
	}
	if strings.Contains(query, "to=") {
		t.Errorf("query %q sent a `to` bound that was never asked for", query)
	}
	if !strings.Contains(stdout, "meeting=NEWER") {
		t.Errorf("stdout did not list the server's meeting:\n%s", stdout)
	}
	// The counts that explain a short list are the SERVER's. Re-filtering an
	// already-narrowed set would report every count as zero.
	if !strings.Contains(stdout, "excluded=3") {
		t.Errorf("stdout did not carry the server's excluded count:\n%s", stdout)
	}
	if !strings.Contains(stdout, "note=2 meeting(s) have a date this build cannot read") {
		t.Errorf("stdout did not report the server's undated count:\n%s", stdout)
	}
}

// An app that predates the route answers 404, and the CLI must keep working:
// it ships on laptops and is not upgraded in lockstep with the server.
func TestMeetingsListFallsBackToTheCatalogOn404(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalog(twoMeetingCatalog))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "list", "--from", "2026-08-11")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if len(fake.requests) != 2 || fake.requests[1] != meetingsTestCatalogPath {
		t.Fatalf("requested %v, want the list route then the catalog", fake.requests)
	}
	// Client-side filtering still narrows, and still explains what it removed.
	if !strings.Contains(stdout, "meeting=NEWER") || strings.Contains(stdout, "meeting=OLDER") {
		t.Errorf("fallback did not filter client-side:\n%s", stdout)
	}
	if !strings.Contains(stdout, "excluded=1") {
		t.Errorf("fallback did not report what it excluded:\n%s", stdout)
	}
}

// THE distinction the route exists for. A 502 must not be answered by asking
// the catalog, which would turn an outage back into a plausible empty list.
func TestMeetingsListDoesNotFallBackOnSubstrateFailure(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != meetingsTestListPath {
			t.Errorf("unexpected fallback to %s after a 502", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, `{"error":"the recordings archive is unreachable; this is not an empty result"}`)
	})

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "list")

	if code == 0 {
		t.Fatalf("exit=0 on a substrate failure; stdout=%q", stdout)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("requested %v, want only the list route", fake.requests)
	}
	if strings.Contains(stdout, "no recordings are visible to this account") {
		t.Errorf("an outage was reported as an empty archive:\n%s", stdout)
	}
	if !strings.Contains(stderr, "outage") {
		t.Errorf("stderr did not name this an outage:\n%s", stderr)
	}
}

// The query string carries filter values today and would carry search terms
// next (D-623). url.Redacted() masks only the userinfo password, so before this
// the whole query landed in the error text that reportMeetingsError prints.
func TestMeetingsListKeepsTheQueryOutOfErrorOutput(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, `{"error":"unreachable"}`)
	})

	_, _, stderr := runMeetingsCLI(t, fake.server.URL,
		"list", "--from", "2026-08-11", "--room", "rm_0123456789abcdef")

	for _, leaked := range []string{"2026-08-11", "rm_0123456789abcdef", "from=", "room=", "?"} {
		if strings.Contains(stderr, leaked) {
			t.Errorf("error output leaked %q from the request query:\n%s", leaked, stderr)
		}
	}
}

// A version the CLI cannot read is refused from the route exactly as it is from
// the catalog — the envelope is the same contract either way.
func TestMeetingsListRefusesAnUnknownVersionFromTheRoute(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveMeetingsList(t, func(string) string {
		return `{"version":"cassini.viewer.catalog.v2","meetings":[]}`
	}))

	code, _, stderr := runMeetingsCLI(t, fake.server.URL, "list")

	if code == 0 {
		t.Fatal("exit=0 on an unsupported catalog version")
	}
	if !strings.Contains(stderr, "unsupported catalog version") {
		t.Errorf("stderr did not name the version problem:\n%s", stderr)
	}
}

// An unfiltered listing asks for the route with no query at all, so a server
// that echoes a filter back cannot be responding to something invented here.
func TestMeetingsListSendsNoQueryWhenUnfiltered(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveMeetingsList(t, func(string) string {
		return `{"version":"cassini.viewer.catalog.v1","meetings":[]}`
	}))

	if code, _, stderr := runMeetingsCLI(t, fake.server.URL, "list"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if fake.queries[0] != "" {
		t.Errorf("query = %q, want empty for an unfiltered listing", fake.queries[0])
	}
}

// `rooms` and `fetch` go through the same path, so the CLI cannot report an
// outage in one subcommand and a denial or an empty archive in the next.
func TestMeetingsSiblingCommandsUseTheServerRoute(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       []string
		wantAbsent string
	}{
		{"rooms", []string{"rooms"}, "no rooms"},
		{"context", []string{"context", "NEWER"}, "no recording you can read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newMeetingsFakeNextcloud(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != meetingsTestListPath {
					t.Errorf("unexpected fallback to %s after a 502", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(http.StatusBadGateway)
				fmt.Fprint(w, `{"error":"the recordings archive is unreachable"}`)
			})

			code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, tc.args...)

			if code == 0 {
				t.Fatalf("exit=0 on a substrate failure; stdout=%q", stdout)
			}
			if !strings.Contains(stderr, "outage") {
				t.Errorf("stderr did not name this an outage:\n%s", stderr)
			}
			if strings.Contains(stdout, tc.wantAbsent) || strings.Contains(stderr, tc.wantAbsent) {
				t.Errorf("an outage was reported as %q:\nstdout=%s\nstderr=%s", tc.wantAbsent, stdout, stderr)
			}
		})
	}
}
