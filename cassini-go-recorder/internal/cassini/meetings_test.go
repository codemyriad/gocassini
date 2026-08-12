package cassini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

// meetingsFakeNextcloud stands in for Nextcloud's AppAPI proxy in front of the
// Cassini app. It records what the CLI actually sent so the tests can pin the
// wire contract: the proxied path, Basic auth, and the absence of OCS headers.
type meetingsFakeNextcloud struct {
	server *httptest.Server

	// requests holds every path the CLI asked for, in order.
	requests []string
	// lastAuth is the Basic-auth pair from the most recent request.
	lastUser, lastPassword string
	lastAuthOK             bool
	// lastHeaders is the most recent request's headers.
	lastHeaders http.Header

	// handler answers each request. Set per test.
	handler func(w http.ResponseWriter, r *http.Request)
}

func newMeetingsFakeNextcloud(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *meetingsFakeNextcloud {
	t.Helper()
	fake := &meetingsFakeNextcloud{handler: handler}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.requests = append(fake.requests, r.URL.Path)
		user, password, ok := r.BasicAuth()
		fake.lastUser, fake.lastPassword, fake.lastAuthOK = user, password, ok
		fake.lastHeaders = r.Header.Clone()
		fake.handler(w, r)
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

// catalogPath is the proxied path the CLI must ask for. Written out literally
// rather than composed from the constants, so a change to either is caught.
const meetingsTestCatalogPath = "/index.php/apps/app_api/proxy/gocassini/published/catalog.json"

// serveCatalog answers the catalog route with body and 200, and everything else
// with 404, mimicking the app's own routing.
func serveCatalog(body string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != meetingsTestCatalogPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Cassini-Meeting-Source", "nextcloud-files")
		fmt.Fprint(w, body)
	}
}

// runMeetingsCLI drives the real CLI entry point with the connection flags
// pointed at a fake, returning the exit code and both streams.
func runMeetingsCLI(t *testing.T, baseURL string, args ...string) (int, string, string) {
	t.Helper()
	full := append([]string{"meetings"}, args...)
	full = append(full,
		"--nextcloud-url", baseURL,
		"--user", "alice",
		"--app-password", "app-pw-1234",
	)
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), full, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

const twoMeetingCatalog = `{
  "version": "cassini.viewer.catalog.v1",
  "generatedAt": "2026-08-11T10:00:00Z",
  "meetings": [
    {"id": "OLDER", "title": "Kickoff", "dateLabel": "2026-08-01 09:00",
     "audioPath": "./meetings/OLDER.opus", "speakerCount": 2, "segmentCount": 40, "digestDurationMs": 600000},
    {"id": "NEWER", "title": "Daily Standup", "dateLabel": "2026-08-11 10:32",
     "audioPath": "./meetings/NEWER.opus", "speakerCount": 3, "segmentCount": 120, "digestDurationMs": 1800000}
  ]
}`

func TestMeetingsListSendsBasicAuthToTheProxiedCatalogRoute(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalog(twoMeetingCatalog))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "list")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if len(fake.requests) != 1 || fake.requests[0] != meetingsTestCatalogPath {
		t.Fatalf("requested %v, want exactly [%s]", fake.requests, meetingsTestCatalogPath)
	}
	if !fake.lastAuthOK {
		t.Fatal("request carried no Basic auth")
	}
	if fake.lastUser != "alice" || fake.lastPassword != "app-pw-1234" {
		t.Errorf("Basic auth = %q:%q, want alice:app-pw-1234", fake.lastUser, fake.lastPassword)
	}
	// These are the app's own HTTP routes, not OCS, and the AppAPI proxy mints
	// the app-API identity itself — sending either header would be wrong.
	if got := fake.lastHeaders.Get("OCS-APIRequest"); got != "" {
		t.Errorf("OCS-APIRequest = %q, want it absent", got)
	}
	if got := fake.lastHeaders.Get("AUTHORIZATION-APP-API"); got != "" {
		t.Errorf("AUTHORIZATION-APP-API = %q, want it absent", got)
	}
	if !strings.Contains(stdout, "meetings=2 caller=alice source=nextcloud-files") {
		t.Errorf("missing summary line in:\n%s", stdout)
	}
	if !strings.Contains(stdout, "meeting=NEWER date=2026-08-11 10:32 title=Daily Standup speakers=3 segments=120 duration_ms=1800000 fetchable=yes") {
		t.Errorf("missing meeting line in:\n%s", stdout)
	}
}

// The list must read newest-first, the same order the viewer shows, so an agent
// asking for "the latest meeting" takes the first line.
func TestMeetingsListOrdersNewestFirst(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalog(twoMeetingCatalog))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "list")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}

	newer := strings.Index(stdout, "meeting=NEWER")
	older := strings.Index(stdout, "meeting=OLDER")
	if newer < 0 || older < 0 {
		t.Fatalf("both meetings should be listed:\n%s", stdout)
	}
	if newer > older {
		t.Errorf("NEWER should precede OLDER:\n%s", stdout)
	}
}

// An empty catalog is a legitimate 200 and must not read as a failure — but it
// is ambiguous, and the CLI has to say so rather than assert "you have none".
func TestMeetingsListEmptyCatalogSucceedsAndExplainsAmbiguity(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalog(`{"version":"cassini.viewer.catalog.v1","meetings":[]}`))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "list")

	if code != 0 {
		t.Fatalf("empty catalog must exit 0, got %d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "meetings=0") {
		t.Errorf("expected meetings=0 in:\n%s", stdout)
	}
	if !strings.Contains(stdout, "mis-provisioned") {
		t.Errorf("expected the ambiguity note in:\n%s", stdout)
	}
}

// A 200 with no source header did not come from Nextcloud Files, which means no
// per-caller access control was applied. Warn rather than pretend.
func TestMeetingsListWarnsWhenResponseIsNotFromNextcloudFiles(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"version":"cassini.viewer.catalog.v1","meetings":[]}`)
	})

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "list")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "source=unknown") {
		t.Errorf("expected source=unknown in:\n%s", stdout)
	}
	if !strings.Contains(stderr, "per-caller access control may not be in effect") {
		t.Errorf("expected the substrate warning in:\n%s", stderr)
	}
}

func TestMeetingsListJSONReEmitsEntriesVerbatim(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalog(twoMeetingCatalog))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "list", "--json")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}

	var got struct {
		Version  string           `json:"version"`
		Source   string           `json:"source"`
		Meetings []map[string]any `json:"meetings"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("parse --json output: %v\n%s", err, stdout)
	}
	if got.Version != meetingsCatalogVersion {
		t.Errorf("version = %q, want %q", got.Version, meetingsCatalogVersion)
	}
	if got.Source != "nextcloud-files" {
		t.Errorf("source = %q, want nextcloud-files", got.Source)
	}
	if len(got.Meetings) != 2 {
		t.Fatalf("got %d meetings, want 2", len(got.Meetings))
	}
	if got.Meetings[0]["id"] != "NEWER" {
		t.Errorf("first meeting = %v, want NEWER (newest first)", got.Meetings[0]["id"])
	}
	// Fields the CLI's own struct does not model must survive the round trip,
	// so the server's payload stays the single contract.
	if _, ok := got.Meetings[0]["digestDurationMs"]; !ok {
		t.Errorf("entry lost digestDurationMs: %v", got.Meetings[0])
	}
}

// Every failure the proxied routes can produce must be phrased for what it
// actually means — and a 404 must never be described as forbidden or absent,
// since the app answers 404 for both on purpose.
func TestMeetingsListErrorMapping(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantExit   int
		wantStderr string
		denyStderr []string
	}{
		{
			name: "credentials rejected", status: http.StatusUnauthorized, wantExit: 1,
			wantStderr: "Nextcloud rejected the credentials",
		},
		{
			name: "app not reachable for this account", status: http.StatusForbidden, wantExit: 1,
			wantStderr: "refused the request",
		},
		{
			name: "absent or denied", status: http.StatusNotFound, wantExit: 1,
			wantStderr: "no recording you can read",
			denyStderr: []string{"forbidden", "permission denied", "does not exist"},
		},
		{
			name: "files outage", status: http.StatusBadGateway, body: "Nextcloud Files unavailable", wantExit: 1,
			wantStderr: "an outage, not a permissions problem",
		},
		{
			name: "write attempted", status: http.StatusMethodNotAllowed, wantExit: 1,
			wantStderr: "only GET and HEAD",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newMeetingsFakeNextcloud(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				if tc.body != "" {
					fmt.Fprint(w, tc.body)
				}
			})

			code, _, stderr := runMeetingsCLI(t, fake.server.URL, "list")

			if code != tc.wantExit {
				t.Errorf("exit=%d, want %d (stderr=%q)", code, tc.wantExit, stderr)
			}
			if !strings.Contains(stderr, tc.wantStderr) {
				t.Errorf("stderr=%q, want it to contain %q", stderr, tc.wantStderr)
			}
			for _, banned := range tc.denyStderr {
				if strings.Contains(strings.ToLower(stderr), banned) {
					t.Errorf("stderr must not say %q (it leaks what 404 hides): %q", banned, stderr)
				}
			}
		})
	}
}

func TestMeetingsListRejectsAnUnknownCatalogVersion(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalog(`{"version":"cassini.viewer.catalog.v9","meetings":[]}`))

	code, _, stderr := runMeetingsCLI(t, fake.server.URL, "list")

	if code != 1 {
		t.Errorf("exit=%d, want 1", code)
	}
	if !strings.Contains(stderr, "unsupported catalog version") {
		t.Errorf("stderr=%q, want it to mention the unsupported version", stderr)
	}
}

func TestMeetingsUsageAndDispatch(t *testing.T) {
	t.Run("family help goes to stdout", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Run(context.Background(), []string{"meetings", "--help"}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("exit=%d", code)
		}
		if !strings.Contains(stdout.String(), "cassini meetings list") {
			t.Errorf("expected usage on stdout, got %q", stdout.String())
		}
		if stderr.Len() != 0 {
			t.Errorf("expected nothing on stderr, got %q", stderr.String())
		}
	})

	t.Run("bare invocation prints usage and succeeds", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Run(context.Background(), []string{"meetings"}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "Usage:") {
			t.Errorf("expected usage on stdout, got %q", stdout.String())
		}
	})

	t.Run("unknown subcommand exits 2", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Run(context.Background(), []string{"meetings", "summarise"}, &stdout, &stderr)
		if code != 2 {
			t.Fatalf("exit=%d, want 2", code)
		}
		if !strings.Contains(stderr.String(), `unknown meetings command "summarise"`) {
			t.Errorf("stderr=%q", stderr.String())
		}
	})

	t.Run("leaf help goes to stderr and succeeds", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Run(context.Background(), []string{"meetings", "list", "--help"}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("exit=%d", code)
		}
		if !strings.Contains(stderr.String(), "cassini meetings list") {
			t.Errorf("expected leaf usage on stderr, got %q", stderr.String())
		}
	})

	t.Run("root usage advertises the family", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Run(context.Background(), []string{"help"}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("exit=%d", code)
		}
		if !strings.Contains(stdout.String(), "meetings Read the meeting recordings") {
			t.Errorf("root usage missing the meetings command: %q", stdout.String())
		}
	})

	t.Run("positional arguments are rejected", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Run(context.Background(), []string{"meetings", "list", "extra"}, &stdout, &stderr)
		if code != 2 {
			t.Fatalf("exit=%d, want 2", code)
		}
		if !strings.Contains(stderr.String(), "does not accept positional arguments") {
			t.Errorf("stderr=%q", stderr.String())
		}
	})
}

// Missing connection settings are a usage error (exit 2), not a runtime one, and
// the message must name the environment variable so an agent can fix it.
func TestMeetingsConfigurationErrorsExitTwo(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantStderr string
	}{
		{"no url", []string{"meetings", "list", "--user", "alice", "--app-password", "pw"}, "--nextcloud-url is required"},
		{"no user", []string{"meetings", "list", "--nextcloud-url", "https://nc.example.com", "--app-password", "pw"}, "--user is required"},
		{"no app password", []string{"meetings", "list", "--nextcloud-url", "https://nc.example.com", "--user", "alice"}, "CASSINI_NC_APP_PASSWORD"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CASSINI_NC_URL", "")
			t.Setenv("CASSINI_NC_USER", "")
			t.Setenv("CASSINI_NC_APP_PASSWORD", "")

			var stdout, stderr bytes.Buffer
			code := Run(context.Background(), tc.args, &stdout, &stderr)
			if code != 2 {
				t.Fatalf("exit=%d, want 2 (stderr=%q)", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Errorf("stderr=%q, want it to contain %q", stderr.String(), tc.wantStderr)
			}
		})
	}
}

// The environment is the preferred way to pass the credential, so it has to
// actually work — and a flag must win over it.
func TestMeetingsConfigReadsEnvironmentAndFlagsWin(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalog(`{"version":"cassini.viewer.catalog.v1","meetings":[]}`))
	t.Setenv("CASSINI_NC_URL", fake.server.URL)
	t.Setenv("CASSINI_NC_USER", "from-env")
	t.Setenv("CASSINI_NC_APP_PASSWORD", "pw-from-env")

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"meetings", "list"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if fake.lastUser != "from-env" || fake.lastPassword != "pw-from-env" {
		t.Errorf("auth = %q:%q, want from-env:pw-from-env", fake.lastUser, fake.lastPassword)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), []string{"meetings", "list", "--user", "from-flag"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if fake.lastUser != "from-flag" {
		t.Errorf("auth user = %q, want the flag to win with from-flag", fake.lastUser)
	}
}

// A missing app password must be reported by naming its source, never by
// echoing the value.
func TestMeetingsErrorNeverEchoesTheAppPassword(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "list")

	if code != 1 {
		t.Fatalf("exit=%d, want 1", code)
	}
	if strings.Contains(stdout+stderr, "app-pw-1234") {
		t.Errorf("output leaked the app password:\nstdout=%q\nstderr=%q", stdout, stderr)
	}
	if !strings.Contains(stderr, "flag --app-password") {
		t.Errorf("stderr should name where the credential came from: %q", stderr)
	}
}

func TestNormalizeNextcloudURL(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "https://cloud.example.com", want: "https://cloud.example.com"},
		{in: "https://cloud.example.com/", want: "https://cloud.example.com"},
		{in: "http://127.0.0.1:28080", want: "http://127.0.0.1:28080"},
		{in: "  https://cloud.example.com/nextcloud/ ", want: "https://cloud.example.com/nextcloud"},
		// A bare host must become https, never http: downgrading would put the
		// app password on the wire in clear text.
		{in: "cloud.example.com", want: "https://cloud.example.com"},
		{in: "ftp://cloud.example.com", wantErr: true},
		{in: "https://", wantErr: true},
		{in: "   ", wantErr: true},
	}
	for _, tc := range cases {
		got, err := normalizeNextcloudURL(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("normalizeNextcloudURL(%q) = %q, want an error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizeNextcloudURL(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("normalizeNextcloudURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The .opus URL comes from the entry's audioPath resolved against the catalog's
// own URL — not from concatenating the id. They coincide today, but the
// exporter owns the path and can emit three different shapes.
func TestResolveMeetingAudioURL(t *testing.T) {
	catalogURL, err := url.Parse("https://cloud.example.com/index.php/apps/app_api/proxy/gocassini/published/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	base := "https://cloud.example.com/index.php/apps/app_api/proxy/gocassini/published/"

	cases := []struct {
		name  string
		entry meetingsCatalogEntry
		want  string
	}{
		{"dot-relative", meetingsCatalogEntry{ID: "X", AudioPath: "./meetings/X.opus"}, base + "meetings/X.opus"},
		{"bare relative", meetingsCatalogEntry{ID: "X", AudioPath: "meetings/X.opus"}, base + "meetings/X.opus"},
		{"id needing escaping", meetingsCatalogEntry{ID: "X", AudioPath: "./meetings/a b.opus"}, base + "meetings/a%20b.opus"},
		// An absolute URL on the configured origin is fine; one on another host
		// is refused — see TestResolveMeetingAudioURLRefusesEscapingThePublishedTree.
		{"absolute on the same origin", meetingsCatalogEntry{ID: "X", AudioPath: base + "meetings/X.opus"}, base + "meetings/X.opus"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveMeetingAudioURL(catalogURL, tc.entry)
			if err != nil {
				t.Fatalf("resolveMeetingAudioURL: %v", err)
			}
			if got.String() != tc.want {
				t.Errorf("got %q, want %q", got.String(), tc.want)
			}
		})
	}

	t.Run("artifactPath-only entry is rejected with a reason", func(t *testing.T) {
		_, err := resolveMeetingAudioURL(catalogURL, meetingsCatalogEntry{ID: "LEGACY", ArtifactPath: "./meetings/LEGACY"})
		if err == nil {
			t.Fatal("expected an error for an entry with no audioPath")
		}
		if !strings.Contains(err.Error(), "predates the single-file format") {
			t.Errorf("error = %v, want it to explain why there is nothing to fetch", err)
		}
	})

	t.Run("entry with no paths at all is rejected", func(t *testing.T) {
		if _, err := resolveMeetingAudioURL(catalogURL, meetingsCatalogEntry{ID: "EMPTY"}); err == nil {
			t.Fatal("expected an error for an entry with no paths")
		}
	})
}

// catalogItems builds items the way fetchCatalog does, so the sort is tested
// through the same date parsing the real path uses.
func catalogItems(entries ...meetingsCatalogEntry) []meetingsCatalogItem {
	items := make([]meetingsCatalogItem, 0, len(entries))
	for _, entry := range entries {
		at, dated := parseMeetingDateLabel(entry.DateLabel)
		items = append(items, meetingsCatalogItem{entry: entry, at: at, dated: dated})
	}
	return items
}

func sortedIDs(items []meetingsCatalogItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.entry.ID)
	}
	return ids
}

func TestSortMeetingsNewestFirstPutsUnparseableLabelsLast(t *testing.T) {
	items := catalogItems(
		meetingsCatalogEntry{ID: "no-label"},
		meetingsCatalogEntry{ID: "older", DateLabel: "2026-08-01"},
		meetingsCatalogEntry{ID: "newest", DateLabel: "2026-08-11 10:32:07"},
		meetingsCatalogEntry{ID: "middle", DateLabel: "2026-08-05 09:00"},
		meetingsCatalogEntry{ID: "garbage", DateLabel: "last tuesday"},
	)

	sortMeetingsNewestFirst(items)

	want := []string{"newest", "middle", "older", "no-label", "garbage"}
	if got := sortedIDs(items); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// Ids are server data and nothing guarantees they are unique. An earlier version
// keyed the parsed dates by id, so duplicate ids shared one date and sorted
// wrongly — and the raw payload of one of them was dropped from --json.
func TestSortMeetingsNewestFirstHandlesDuplicateIDs(t *testing.T) {
	items := catalogItems(
		meetingsCatalogEntry{ID: "A", DateLabel: "2026-08-01", Title: "oldest A"},
		meetingsCatalogEntry{ID: "A", DateLabel: "not-a-date", Title: "undated A"},
		meetingsCatalogEntry{ID: "A", DateLabel: "2026-08-12", Title: "newest A"},
		meetingsCatalogEntry{ID: "B", DateLabel: "2026-08-05", Title: "B"},
	)

	sortMeetingsNewestFirst(items)

	titles := make([]string, 0, len(items))
	for _, item := range items {
		titles = append(titles, item.entry.Title)
	}
	want := []string{"newest A", "B", "oldest A", "undated A"}
	if strings.Join(titles, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", titles, want)
	}
}

// A catalog entry naming another host must never be fetched: the request carries
// the caller's Nextcloud app password, so following it would hand that credential
// to whatever host the catalog asked for.
func TestMeetingsFetchRefusesAForeignHostInTheCatalog(t *testing.T) {
	var harvested struct {
		gotAuth  bool
		password string
	}
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, password, ok := r.BasicAuth()
		harvested.gotAuth, harvested.password = ok, password
		_, _ = w.Write([]byte("harvested"))
	}))
	defer attacker.Close()

	hostileCatalog := fmt.Sprintf(`{"version":"cassini.viewer.catalog.v1","meetings":[
	  {"id":"MEETING1","title":"Daily Standup","dateLabel":"2026-08-11 10:32","audioPath":"%s/harvest.opus"}]}`, attacker.URL)
	fake := newMeetingsFakeNextcloud(t, serveCatalog(hostileCatalog))

	tmp := t.TempDir()
	code, _, stderr := runMeetingsCLI(t, fake.server.URL, "fetch", "MEETING1", "--out", filepath.Join(tmp, "m.opus"))

	if harvested.gotAuth {
		t.Fatalf("LEAK: the app password %q was sent to a host named by the catalog", harvested.password)
	}
	if code != 1 {
		t.Errorf("exit=%d, want 1 (stderr=%q)", code, stderr)
	}
	if !strings.Contains(stderr, "points outside the Nextcloud you configured") {
		t.Errorf("stderr=%q, want it to explain the refusal", stderr)
	}
	assertNoStrayFiles(t, tmp)
}

// Go's default redirect policy keeps Authorization when a redirect moves to a
// SUBDOMAIN of the origin, so a compromised Nextcloud could harvest the app
// password with a 302. Every redirect is refused instead.
func TestMeetingsRefusesRedirectsRatherThanForwardingCredentials(t *testing.T) {
	var harvested struct {
		gotAuth  bool
		password string
	}
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, password, ok := r.BasicAuth()
		harvested.gotAuth, harvested.password = ok, password
		w.Header().Set("X-Cassini-Meeting-Source", "nextcloud-files")
		fmt.Fprint(w, `{"version":"cassini.viewer.catalog.v1","meetings":[]}`)
	}))
	defer attacker.Close()

	fake := newMeetingsFakeNextcloud(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL+"/harvest.json", http.StatusFound)
	})

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "list")

	if harvested.gotAuth {
		t.Fatalf("LEAK: the app password %q was sent to a redirect target", harvested.password)
	}
	if code != 1 {
		t.Errorf("exit=%d, want 1 (stdout=%q stderr=%q)", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "refusing to follow a redirect") {
		t.Errorf("stderr=%q, want it to name the refused redirect", stderr)
	}
}

// An entry whose audioPath climbs out of the published tree would aim
// credentialed requests at unrelated routes on the same Nextcloud.
func TestResolveMeetingAudioURLRefusesEscapingThePublishedTree(t *testing.T) {
	catalogURL, err := url.Parse("https://cloud.example.com/index.php/apps/app_api/proxy/gocassini/published/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, audioPath := range []string{
		"../../../../../../remote.php/dav/files/admin/secret.opus",
		"/ocs/v2.php/cloud/users",
		"https://evil.example.com/harvest.opus",
		"//evil.example.com/harvest.opus",
	} {
		t.Run(audioPath, func(t *testing.T) {
			resolved, err := resolveMeetingAudioURL(catalogURL, meetingsCatalogEntry{ID: "M1", AudioPath: audioPath})
			if err == nil {
				t.Fatalf("expected a refusal, got %s", resolved)
			}
			if !strings.Contains(err.Error(), "refusing to fetch meeting") {
				t.Errorf("error = %v, want an explicit refusal", err)
			}
		})
	}
}

// A JSON null or an entry with no id cannot be fetched or referred to, so it must
// not be listed as a phantom meeting — but it must be reported as skipped, since
// the list is then incomplete.
func TestMeetingsListSkipsEntriesWithNoID(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalog(`{"version":"cassini.viewer.catalog.v1","meetings":[
	  {"id":"REAL","title":"Daily Standup","dateLabel":"2026-08-11","audioPath":"./meetings/REAL.opus"},
	  null,
	  {"title":"No id here","dateLabel":"2026-08-10"}]}`))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "list")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "meetings=1") {
		t.Errorf("expected only the usable entry to be counted:\n%s", stdout)
	}
	if strings.Contains(stdout, "meeting=- ") {
		t.Errorf("a phantom meeting was listed:\n%s", stdout)
	}
	if !strings.Contains(stderr, "had no id and were skipped") {
		t.Errorf("stderr=%q, want it to report the skipped entries", stderr)
	}
}
