package cassini

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// The meetings commands read published recordings out of Nextcloud through the
// app's own USER-level routes, proxied by Nextcloud's AppAPI. Nextcloud does
// the authorization: the app fetches from Nextcloud Files as the calling user,
// so a caller sees exactly the recordings Nextcloud says they may read and
// never learns that the others exist.
//
//	cassini meetings list ─┐
//	                       │  GET <nc>/index.php/apps/app_api/proxy/gocassini/published/catalog.json
//	                       │      Authorization: Basic <user>:<app password>
//	                       ▼
//	            Nextcloud AppAPI proxy ── mints AUTHORIZATION-APP-API for the session
//	                       ▼
//	              Cassini ExApp ── PROPFINDs / GETs Nextcloud Files AS THE CALLER
//	                       ▼
//	   catalog.json filtered to the caller  ·  meetings/<id>.opus or 404
//
// Nothing here is privileged and nothing here mutates: the whole surface is
// GET/HEAD, and job control stays on the operator's ADMIN routes.
const (
	// appAPIProxyPath is where Nextcloud's AppAPI exposes an ExApp's own
	// routes to an authenticated caller.
	appAPIProxyPath = "/index.php/apps/app_api/proxy/"

	// meetingsDefaultAppID is the ExApp id from appinfo/info.xml. Overridable
	// because an appstore-flavoured build can be registered under another id.
	meetingsDefaultAppID = "gocassini"

	// meetingsCatalogPath is the per-caller meeting index, relative to the
	// proxied app root.
	meetingsCatalogPath = "published/catalog.json"

	// meetingsSourceHeader is set by the app on every response it served out
	// of Nextcloud Files. Its absence on a 200 means the reply came from
	// somewhere else — a dev operator serving a local archive with no access
	// control — which is worth telling the caller about.
	meetingsSourceHeader = "X-Cassini-Meeting-Source"
)

// meetingsHTTPClient handles the small JSON request (the catalog). Package-level
// so tests can point it at an httptest server's transport.
var meetingsHTTPClient = &http.Client{Timeout: 20 * time.Second}

// meetingsConfig is the resolved connection configuration shared by every
// meetings subcommand.
type meetingsConfig struct {
	nextcloudURL string
	user         string
	appPassword  string
	appID        string
	insecure     bool

	// appPasswordSource records where the credential came from, so the CLI can
	// say which knob to fix without ever echoing the secret.
	appPasswordSource string
}

func runMeetings(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printMeetingsUsage(stdout)
		return 0
	}

	switch args[0] {
	case "help", "-h", "--help":
		printMeetingsUsage(stdout)
		return 0
	case "list":
		return runMeetingsList(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown meetings command %q\n\n", args[0])
		printMeetingsUsage(stderr)
		return 2
	}
}

func printMeetingsUsage(w io.Writer) {
	fmt.Fprint(w, `Read the meeting recordings your Nextcloud account may access.

Nextcloud enforces the permissions: you see exactly the recordings it says you
may read, and a recording you may not read reports as not found.

Usage:
  cassini meetings list
  cassini meetings list --json

Commands:
  list     List the meetings your account may read

Connection:
  --nextcloud-url URL   Nextcloud base URL           (env CASSINI_NC_URL)
  --user NAME           Nextcloud user id            (env CASSINI_NC_USER)
  --app-password VALUE  Nextcloud app password       (env CASSINI_NC_APP_PASSWORD)
  --app-id ID           Cassini app id               (env CASSINI_NC_APP_ID, default `+meetingsDefaultAppID+`)
  --insecure            Skip TLS verification (testing only)

  Create an app password in Nextcloud under Settings -> Security -> Devices &
  sessions. Prefer CASSINI_NC_APP_PASSWORD over --app-password so the secret
  stays out of your shell history and process list.
`+"\n")
}

// registerMeetingsConnectionFlags adds the connection flags every subcommand
// shares. Env fallbacks are applied afterwards by resolveMeetingsConfig, which
// needs to know which flags were actually passed.
func registerMeetingsConnectionFlags(fs *flag.FlagSet, cfg *meetingsConfig) {
	fs.StringVar(&cfg.nextcloudURL, "nextcloud-url", "", "Nextcloud base URL (defaults to CASSINI_NC_URL)")
	fs.StringVar(&cfg.user, "user", "", "Nextcloud user id to act as (defaults to CASSINI_NC_USER)")
	fs.StringVar(&cfg.appPassword, "app-password", "", "Nextcloud app password (prefer CASSINI_NC_APP_PASSWORD)")
	fs.StringVar(&cfg.appID, "app-id", "", "Cassini app id as registered in Nextcloud (defaults to CASSINI_NC_APP_ID, then "+meetingsDefaultAppID+")")
	fs.BoolVar(&cfg.insecure, "insecure", false, "disable TLS certificate verification (testing only)")
}

// resolveMeetingsConfig fills unset flags from the environment and validates
// the result. Precedence is flag, then environment, then default; an empty
// environment variable counts as unset.
func resolveMeetingsConfig(fs *flag.FlagSet, cfg *meetingsConfig) error {
	passed := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { passed[f.Name] = true })

	if !passed["nextcloud-url"] {
		cfg.nextcloudURL = envOrDefault("CASSINI_NC_URL", "")
	}
	if !passed["user"] {
		cfg.user = envOrDefault("CASSINI_NC_USER", "")
	}
	cfg.appPasswordSource = "flag --app-password"
	if !passed["app-password"] {
		cfg.appPassword = envOrDefault("CASSINI_NC_APP_PASSWORD", "")
		cfg.appPasswordSource = "env CASSINI_NC_APP_PASSWORD"
	}
	if !passed["app-id"] {
		cfg.appID = envOrDefault("CASSINI_NC_APP_ID", meetingsDefaultAppID)
	}

	cfg.nextcloudURL = strings.TrimSpace(cfg.nextcloudURL)
	cfg.user = strings.TrimSpace(cfg.user)
	cfg.appID = strings.TrimSpace(cfg.appID)

	if cfg.nextcloudURL == "" {
		return errors.New("--nextcloud-url is required (or set CASSINI_NC_URL)")
	}
	if cfg.user == "" {
		return errors.New("--user is required (or set CASSINI_NC_USER)")
	}
	if cfg.appPassword == "" {
		return errors.New("an app password is required: set CASSINI_NC_APP_PASSWORD (preferred) or pass --app-password")
	}
	if cfg.appID == "" {
		return errors.New("--app-id must not be empty")
	}

	normalized, err := normalizeNextcloudURL(cfg.nextcloudURL)
	if err != nil {
		return err
	}
	cfg.nextcloudURL = normalized
	return nil
}

// normalizeNextcloudURL accepts a bare host or a full base URL and returns a
// scheme-qualified URL with no trailing slash. A bare host becomes https:// —
// never http://, since silently downgrading would send the app password in
// clear text.
func normalizeNextcloudURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", errors.New("--nextcloud-url must not be empty")
	}
	// Check for a scheme before touching the trailing slashes: trimming first
	// would turn "https://" into "https:/" and then re-prefix it.
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("invalid --nextcloud-url %q: %w", raw, err)
	}
	switch parsed.Scheme {
	case "http", "https":
	default:
		return "", fmt.Errorf("invalid --nextcloud-url %q: scheme must be http or https, got %q", raw, parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("invalid --nextcloud-url %q: no host", raw)
	}
	// A base URL carries neither a query nor a fragment, and route paths are
	// appended to it, so normalise both away and drop any trailing slash.
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

// meetingsClient talks to the app's published routes through Nextcloud's AppAPI
// proxy, authenticating as a Nextcloud user with an app password.
//
// It is deliberately not an OCS client: these are plain HTTP routes with no OCS
// envelope, so nextcloud.OCSClient cannot serve — it always sets
// OCS-APIRequest and always tries to JSON-decode the reply, which a binary
// .opus body cannot satisfy.
type meetingsClient struct {
	cfg meetingsConfig
	// json handles the catalog: small, so a whole-request timeout is right.
	json *http.Client
	// stream handles .opus bodies: an hour-long meeting is many megabytes over
	// an arbitrary link, so it bounds the time to first byte rather than the
	// whole transfer, and lets the context govern the rest.
	stream *http.Client
}

func newMeetingsClient(cfg meetingsConfig) *meetingsClient {
	jsonClient := meetingsHTTPClient
	if cfg.insecure {
		jsonClient = &http.Client{Timeout: jsonClient.Timeout, Transport: meetingsTransport(true)}
	}
	return &meetingsClient{
		cfg:  cfg,
		json: jsonClient,
		stream: &http.Client{
			Transport: meetingsTransport(cfg.insecure),
		},
	}
}

func meetingsTransport(insecure bool) http.RoundTripper {
	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		baseTransport = &http.Transport{}
	}
	transport := baseTransport.Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	if insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return transport
}

// appRootURL is the proxied root of the Cassini app: every published route is
// resolved relative to it. The trailing slash matters — without it, resolving a
// relative reference against this URL would drop the app id segment.
func (c *meetingsClient) appRootURL() (*url.URL, error) {
	root, err := url.Parse(c.cfg.nextcloudURL + appAPIProxyPath + url.PathEscape(c.cfg.appID) + "/")
	if err != nil {
		return nil, fmt.Errorf("build Cassini app URL: %w", err)
	}
	return root, nil
}

// catalogURL is the absolute URL of the caller's meeting index. It is also the
// base every catalog entry's relative asset path resolves against.
func (c *meetingsClient) catalogURL() (*url.URL, error) {
	root, err := c.appRootURL()
	if err != nil {
		return nil, err
	}
	return root.Parse(meetingsCatalogPath)
}

// get issues an authenticated GET and returns the live response. The caller
// closes the body. Non-2xx statuses are returned as a *meetingsHTTPError so
// each command can phrase them itself; the transport error path is wrapped.
func (c *meetingsClient) get(ctx context.Context, target *url.URL, client *http.Client) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.cfg.user, c.cfg.appPassword)
	// No OCS-APIRequest header: these are the app's own HTTP routes, not OCS.
	// No AUTHORIZATION-APP-API either — the AppAPI proxy mints that from the
	// authenticated session, and a client-supplied one is meaningless.
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", target.Redacted(), err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, &meetingsHTTPError{
			URL:     target.Redacted(),
			Status:  resp.StatusCode,
			Snippet: strings.TrimSpace(string(snippet)),
		}
	}
	return resp, nil
}

// meetingsHTTPError is a non-2xx reply from the proxied app routes.
type meetingsHTTPError struct {
	URL     string
	Status  int
	Snippet string
}

func (e *meetingsHTTPError) Error() string {
	if e.Snippet != "" {
		return fmt.Sprintf("GET %s -> HTTP %d: %s", e.URL, e.Status, e.Snippet)
	}
	return fmt.Sprintf("GET %s -> HTTP %d", e.URL, e.Status)
}

// meetingsHTTPStatus returns the HTTP status an error carries, or 0.
func meetingsHTTPStatus(err error) int {
	var httpErr *meetingsHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Status
	}
	return 0
}

// reportMeetingsError writes err to stderr with the phrasing the status
// deserves and returns the process exit code.
//
// The 404 wording is a security property, not politeness: the app answers 404
// for a recording that is absent AND for one the caller may not read, on
// purpose, so that a recording you cannot see never reveals that it exists.
// Saying "forbidden" or "no such meeting" would both leak what 404 exists to
// hide.
func reportMeetingsError(stderr io.Writer, verb string, cfg meetingsConfig, err error) int {
	switch meetingsHTTPStatus(err) {
	case http.StatusUnauthorized:
		fmt.Fprintf(stderr, "meetings %s failed: Nextcloud rejected the credentials for user %q — check the app password (%s)\n",
			verb, cfg.user, cfg.appPasswordSource)
	case http.StatusForbidden:
		fmt.Fprintf(stderr, "meetings %s failed: Nextcloud refused the request for user %q; the account may lack access to the Cassini app\n",
			verb, cfg.user)
	case http.StatusNotFound:
		fmt.Fprintf(stderr, "meetings %s failed: no recording you can read at that id\n", verb)
		fmt.Fprintf(stderr, "hint=an id that is absent and one you may not read are answered the same way on purpose; run `cassini meetings list` to see what this account can read\n")
	case http.StatusMethodNotAllowed:
		fmt.Fprintf(stderr, "meetings %s failed: only GET and HEAD are allowed on the published routes\n", verb)
	case http.StatusBadGateway:
		fmt.Fprintf(stderr, "meetings %s failed: Nextcloud Files is unavailable — an outage, not a permissions problem: %v\n", verb, err)
	default:
		fmt.Fprintf(stderr, "meetings %s failed: %v\n", verb, err)
	}
	return 1
}

// meetingsSource reports where a response's bytes came from, for the
// diagnostic line each command prints. An empty header on a 200 means the
// reply did not come from Nextcloud Files — a dev operator serving a local
// archive has no per-caller access control at all.
func meetingsSource(header http.Header) string {
	if value := strings.TrimSpace(header.Get(meetingsSourceHeader)); value != "" {
		return value
	}
	return "unknown"
}

// openMeetingsOutput returns the writer for a subcommand's primary output:
// either the file named by --out, or stdout. The returned close function is
// always safe to call.
func openMeetingsOutput(outPath string, stdout io.Writer) (io.Writer, func() error, error) {
	if strings.TrimSpace(outPath) == "" {
		return stdout, func() error { return nil }, nil
	}
	file, err := os.Create(outPath)
	if err != nil {
		return nil, nil, fmt.Errorf("create %s: %w", outPath, err)
	}
	return file, file.Close, nil
}
