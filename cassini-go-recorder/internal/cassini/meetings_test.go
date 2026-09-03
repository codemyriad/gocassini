package cassini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
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
	// room=- because this catalog predates room metadata, which is what most of
	// a real archive looks like.
	if !strings.Contains(stdout, "meeting=NEWER date=2026-08-11 10:32 room=- title=Daily Standup speakers=3 segments=120 duration_ms=1800000 fetchable=yes") {
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
		code := Run(context.Background(), []string{"meetings", "bogus"}, &stdout, &stderr)
		if code != 2 {
			t.Fatalf("exit=%d, want 2", code)
		}
		if !strings.Contains(stderr.String(), `unknown meetings command "bogus"`) {
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

// A catalog body of unbounded length must not be read into memory until the
// machine dies. This is capped rather than left to the request timeout, which a
// fast link outruns by gigabytes.
func TestMeetingsListRefusesAnOversizedCatalog(t *testing.T) {
	// One byte past the cap is enough to prove the limit; serving gigabytes here
	// would just OOM the test runner, which is exactly the bug being fixed.
	oversized := maxCatalogBytes + 1
	fake := newMeetingsFakeNextcloud(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Cassini-Meeting-Source", "nextcloud-files")
		w.WriteHeader(http.StatusOK)
		chunk := bytes.Repeat([]byte("A"), 1<<16)
		for sent := 0; sent < oversized; sent += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})

	code, _, stderr := runMeetingsCLI(t, fake.server.URL, "list")

	if code != 1 {
		t.Fatalf("exit=%d, want 1 (stderr=%q)", code, stderr)
	}
	if !strings.Contains(stderr, "larger than") {
		t.Errorf("stderr=%q, want it to name the size refusal", stderr)
	}
}

// A flag that lands in the surplus arguments gets echoed back so the caller can
// see what was misread. The app password must not be echoed with it.
//
// This needs a flag positioned AFTER a positional argument: Go's flag package
// stops parsing there, so everything following becomes surplus. A leading id is
// reordered to the end (meetingsParseArgs) and parses cleanly, so that shape is
// safe already — these are the shapes that are not.
func TestMeetingsSurplusArgumentsNeverEchoTheAppPassword(t *testing.T) {
	const secret = "super-secret-app-password"
	for _, form := range [][]string{
		{"meetings", "fetch", "--out", "/tmp/x.opus", "ID", "--app-password", secret},
		{"meetings", "fetch", "--out", "/tmp/x.opus", "ID", "--app-password=" + secret},
		{"meetings", "context", "--json", "ID", "--app-password", secret},
		{"meetings", "list", "surplus", "--app-password", secret},
	} {
		t.Run(strings.Join(form[1:3], " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(context.Background(), form, &stdout, &stderr)
			if code != 2 {
				t.Fatalf("exit=%d, want 2", code)
			}
			if strings.Contains(stdout.String()+stderr.String(), secret) {
				t.Errorf("the app password was echoed:\nstdout=%q\nstderr=%q", stdout.String(), stderr.String())
			}
			if !strings.Contains(stderr.String(), "redacted") {
				t.Errorf("expected the value to be marked redacted, got %q", stderr.String())
			}
		})
	}
}

// Titles come from the server. A newline in one would let a catalog forge extra
// "meeting=" lines that a caller parsing this output reads as real recordings.
func TestMeetingsListFlattensControlCharactersInServerText(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalog(`{"version":"cassini.viewer.catalog.v1","meetings":[
	  {"id":"REAL","dateLabel":"2026-08-11","audioPath":"./meetings/REAL.opus",
	   "title":"Standup\nmeeting=FORGED date=2026-01-01 title=Injected speakers=1 segments=1 duration_ms=1 fetchable=yes"}]}`))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "list")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if strings.Contains(stdout, "\nmeeting=FORGED") {
		t.Errorf("a forged record was injected into the output:\n%s", stdout)
	}
	// One summary line plus exactly one meeting line.
	if got := strings.Count(strings.TrimSpace(stdout), "\n"); got != 1 {
		t.Errorf("expected 2 lines of output, got %d newlines:\n%s", got, stdout)
	}
}

// A downloaded recording is a private meeting's audio and transcript. It must not
// be created world-readable, which would undo the access control it was fetched
// under.
func TestMeetingsFetchWritesAnOwnerOnlyFile(t *testing.T) {
	tmp := t.TempDir()
	outPath := filepath.Join(tmp, "m.opus")
	fake := newMeetingsFakeNextcloud(t, serveCatalogAndOpus(oneMeetingCatalog, []byte("opus-bytes")))

	if code, _, stderr := runMeetingsCLI(t, fake.server.URL, "fetch", "MEETING1", "--out", outPath); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}

	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("mode = %#o, want no group or other access", mode)
	}
}

// Percent-encoded traversal must be refused like literal traversal. The escapes
// survive EscapedPath(), so an escaped-path prefix check passes while the decoded
// path has already left the tree — and the server decodes before it routes.
func TestResolveMeetingAudioURLRefusesPercentEncodedTraversal(t *testing.T) {
	catalogURL, err := url.Parse("https://cloud.example.com/index.php/apps/app_api/proxy/gocassini/published/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, audioPath := range []string{
		"%2e%2e%2f%2e%2e%2fadmin/jobs",
		"%2e%2e/%2e%2e/admin/jobs",
		".%2e/.%2e/admin/jobs",
		"meetings/%2e%2e%2f%2e%2e%2f%2e%2e%2fremote.php/dav",
	} {
		t.Run(audioPath, func(t *testing.T) {
			resolved, err := resolveMeetingAudioURL(catalogURL, meetingsCatalogEntry{ID: "M1", AudioPath: audioPath})
			if err == nil {
				t.Fatalf("expected a refusal, got %s", resolved)
			}
			if !strings.Contains(err.Error(), "escapes the published tree") {
				t.Errorf("error = %v, want it to name the escape", err)
			}
		})
	}
}

// A base URL can carry userinfo. A validation failure must not echo the password
// into the terminal or into an agent's captured transcript.
func TestNormalizeNextcloudURLNeverEchoesEmbeddedCredentials(t *testing.T) {
	const secret = "SuperSecret123"
	for _, raw := range []string{
		"ftp://alice:" + secret + "@nc.example.com",
		"https://alice:" + secret + "@nc.example.com:notaport",
		"gopher://alice:" + secret + "@host/path@notuserinfo",
	} {
		_, err := normalizeNextcloudURL(raw)
		if err == nil {
			t.Fatalf("expected %q to be rejected", raw)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error leaked the embedded password: %v", err)
		}
	}

	// Userinfo on an otherwise valid URL is dropped: SetBasicAuth overrides it, so
	// carrying it would only smuggle a dead credential into every request URL.
	got, err := normalizeNextcloudURL("https://alice:" + secret + "@nc.example.com")
	if err != nil {
		t.Fatalf("normalizeNextcloudURL: %v", err)
	}
	if strings.Contains(got, secret) || strings.Contains(got, "alice") {
		t.Errorf("normalized URL still carries userinfo: %q", got)
	}
	if got != "https://nc.example.com" {
		t.Errorf("normalized = %q, want %q", got, "https://nc.example.com")
	}
}

// --json is the path an agent reads, so it must not be the only one that never
// hears the list is incomplete or unverified.
func TestMeetingsListJSONStillReportsWarningsAndSkippedCount(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"version":"cassini.viewer.catalog.v1","meetings":[
		  {"id":"OK1","dateLabel":"2026-08-11","audioPath":"./meetings/OK1.opus"},
		  {"title":"no id"}, null]}`)
	})

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "list", "--json")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}

	var got struct {
		Source   string           `json:"source"`
		Skipped  int              `json:"skipped"`
		Meetings []map[string]any `json:"meetings"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout must be exactly one JSON document: %v\n%s", err, stdout)
	}
	if got.Skipped != 2 {
		t.Errorf("skipped = %d, want 2", got.Skipped)
	}
	if got.Source != "unknown" {
		t.Errorf("source = %q, want unknown", got.Source)
	}
	if !strings.Contains(stderr, "were skipped") {
		t.Errorf("stderr should warn about the skipped entries: %q", stderr)
	}
	if !strings.Contains(stderr, "per-caller access control may not be in effect") {
		t.Errorf("stderr should warn about the missing source header: %q", stderr)
	}
}

// When every entry is unusable the server did return meetings, so claiming none
// are visible to this account would be false.
func TestMeetingsListDistinguishesAMalformedCatalogFromAnEmptyOne(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalog(`{"version":"cassini.viewer.catalog.v1","meetings":[{"title":"a"},{"title":"b"},null]}`))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "list")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "malformed rather than empty") {
		t.Errorf("expected the malformed-catalog note:\n%s", stdout)
	}
	if strings.Contains(stdout, "no recordings are visible to this account") {
		t.Errorf("must not claim the account can read none:\n%s", stdout)
	}
}

// The source value is server-controlled and lands in a key=value line, so it must
// not be able to append further pairs a parser would read as facts.
func TestMeetingsSourceRejectsAnInjectedHeaderValue(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Cassini-Meeting-Source", "nextcloud-files caller=root trust=full")
		fmt.Fprint(w, `{"version":"cassini.viewer.catalog.v1","meetings":[]}`)
	})

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "list")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if strings.Contains(stdout, "caller=root") {
		t.Errorf("an injected key=value pair reached the summary line:\n%s", stdout)
	}
	if !strings.Contains(stdout, "source=unrecognised") {
		t.Errorf("expected source=unrecognised:\n%s", stdout)
	}
	if !strings.Contains(stderr, "unrecognised") {
		t.Errorf("expected a warning about the value: %q", stderr)
	}
}

// fetch and context must warn about a missing source header too — an agent
// driving only context would otherwise never learn access control was not applied.
func TestMeetingsFetchAndContextWarnWhenNotServedFromNextcloudFiles(t *testing.T) {
	body := []byte("opus-bytes")
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case meetingsTestCatalogPath:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, oneMeetingCatalog)
		case "/index.php/apps/app_api/proxy/gocassini/published/meetings/MEETING1.opus":
			_, _ = w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}

	tmp := t.TempDir()
	fake := newMeetingsFakeNextcloud(t, handler)
	if code, _, stderr := runMeetingsCLI(t, fake.server.URL, "fetch", "MEETING1", "--out", filepath.Join(tmp, "m.opus")); code != 0 {
		t.Fatalf("fetch exit=%d stderr=%q", code, stderr)
	} else if !strings.Contains(stderr, "per-caller access control may not be in effect") {
		t.Errorf("fetch should warn: %q", stderr)
	}

	// context fails later (the body is not a real .opus), but the warning is owed
	// regardless and must appear.
	_, _, stderr := runMeetingsCLI(t, fake.server.URL, "context", "MEETING1")
	if !strings.Contains(stderr, "per-caller access control may not be in effect") {
		t.Errorf("context should warn: %q", stderr)
	}
}

// A 200 with an empty body is not a meeting; committing it reports success for a
// file that only fails later.
func TestMeetingsFetchRefusesAnEmptyRecording(t *testing.T) {
	tmp := t.TempDir()
	outPath := filepath.Join(tmp, "m.opus")
	fake := newMeetingsFakeNextcloud(t, serveCatalogAndOpus(oneMeetingCatalog, []byte{}))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "fetch", "MEETING1", "--out", outPath)

	if code != 1 {
		t.Fatalf("exit=%d, want 1 (stdout=%q stderr=%q)", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "is empty (0 bytes)") {
		t.Errorf("stderr=%q, want it to name the empty recording", stderr)
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Errorf("no file should be written, stat err=%v", err)
	}
	assertNoStrayFiles(t, tmp)
}

// roomCatalog exercises every room state a real archive holds at once: two
// recordings from a room whose id was derived from its Talk token, one
// backfilled recording whose id was derived from its name instead (its file
// never carried a token), and one from before the field existed at all.
//
// The ids are opaque by design — a one-way derivation, never the token — so the
// fixture uses plausible literals rather than recomputing the derivation, which
// is pinned in internal/portable.
const roomCatalogTokenRoomID = "rm_9f2a1c3d4e5b6a70"
const roomCatalogNameRoomID = "rm_11bb22cc33dd44ee"

const roomCatalog = `{
  "version": "cassini.viewer.catalog.v1",
  "meetings": [
    {"id": "SYNC1", "title": "Weekly Sync (Parakeet Tdt-0.6b-v2)", "dateLabel": "2026-08-11 10:32",
     "audioPath": "./meetings/SYNC1.opus", "roomId": "rm_9f2a1c3d4e5b6a70", "roomName": "Weekly Sync"},
    {"id": "SYNC2", "title": "Weekly Sync (Parakeet Tdt-0.6b-v2)", "dateLabel": "2026-08-04 10:30",
     "audioPath": "./meetings/SYNC2.opus", "roomId": "rm_9f2a1c3d4e5b6a70", "roomName": "Weekly Sync"},
    {"id": "LEGACY", "title": "Old Standup", "dateLabel": "2026-07-02 09:00",
     "audioPath": "./meetings/LEGACY.opus", "roomId": "rm_11bb22cc33dd44ee", "roomName": "Old Standup"},
    {"id": "NOROOM", "title": "Untitled meeting", "dateLabel": "2026-06-01 08:00",
     "audioPath": "./meetings/NOROOM.opus"}
  ]
}`

func TestMeetingsCatalogEntryCarriesTheRoom(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalog(roomCatalog))
	client := newMeetingsClient(meetingsConfig{
		nextcloudURL: fake.server.URL, user: "alice", appPassword: "app-pw-1234", appID: meetingsDefaultAppID,
	})
	listing, err := client.fetchCatalog(context.Background())
	if err != nil {
		t.Fatalf("fetchCatalog: %v", err)
	}

	byID := map[string]meetingsCatalogEntry{}
	for _, entry := range listing.Entries() {
		byID[entry.ID] = entry
	}
	if got := byID["SYNC1"]; got.RoomID != roomCatalogTokenRoomID || got.RoomName != "Weekly Sync" {
		t.Errorf("SYNC1 room = %q/%q, want %q/%q", got.RoomID, got.RoomName, roomCatalogTokenRoomID, "Weekly Sync")
	}
	if got := byID["LEGACY"]; got.RoomID != roomCatalogNameRoomID || got.RoomName != "Old Standup" {
		t.Errorf("LEGACY room = %q/%q, want %q/%q", got.RoomID, got.RoomName, roomCatalogNameRoomID, "Old Standup")
	}
	if got := byID["NOROOM"]; got.RoomID != "" || got.RoomName != "" {
		t.Errorf("NOROOM room = %q/%q, want both empty", got.RoomID, got.RoomName)
	}
}

func TestMeetingsCatalogEntryRoomSelector(t *testing.T) {
	cases := []struct {
		name  string
		entry meetingsCatalogEntry
		want  string
	}{
		{"the id is the selector", meetingsCatalogEntry{RoomID: "rm_abc", RoomName: "Weekly Sync"}, "rm_abc"},
		// A name alone selects nothing. Every room that can be identified has an
		// id — derived from its token, or from its name by the backfill — so a
		// name with no id means the entry was never given one.
		{"a name without an id is not a room", meetingsCatalogEntry{RoomName: "Old Standup"}, ""},
		{"neither", meetingsCatalogEntry{}, ""},
		{"blank id", meetingsCatalogEntry{RoomID: "   ", RoomName: "Old Standup"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.entry.roomSelector(); got != tc.want {
				t.Errorf("roomSelector() = %q, want %q", got, tc.want)
			}
		})
	}
}
