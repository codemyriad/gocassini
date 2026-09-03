package operator

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// published/meetings-context serves a set of meetings as ONE
// cassini.meetings.context.v1 document (D-717). Two properties matter and are
// tested separately: it decides what a caller may read the same way every other
// read of this archive decides it, and the bytes it serves are the CLI's own.

// contextCatalog is the authoritative catalog the stub Nextcloud holds. Only
// MEETING1 and MEETING2 are visible to alice; SECRET belongs to someone else.
const contextCatalog = `{"version":"cassini.viewer.catalog.v1","meetings":[` +
	`{"id":"MEETING1","title":"Daily Standup","dateLabel":"2026-08-11 10:32",` +
	`"audioPath":"./meetings/MEETING1.opus","roomId":"rm_9f2a1c3d4e5b6a70","roomName":"Weekly Sync"},` +
	`{"id":"MEETING2","title":"Backlog Review","dateLabel":"2026-08-18 09:00",` +
	`"audioPath":"./meetings/MEETING2.opus","roomId":"rm_11bb22cc33dd44ee","roomName":"Backlog"},` +
	`{"id":"SECRET","title":"Someone else's","dateLabel":"2026-08-19 09:00",` +
	`"audioPath":"./meetings/SECRET.opus"}]}`

// stubRecordingsDAV answers the three WebDAV calls this surface makes: the
// authoritative catalog as the owner, the caller's Depth-1 scan of meetings/,
// and a per-meeting GET as the caller. visible names what the caller may read;
// anything else 404s, exactly as an advanced-ACL deny does.
func stubRecordingsDAV(t *testing.T, catalog string, opus []byte, visible ...string) (*httptest.Server, *[]string) {
	t.Helper()
	var fetched []string
	allowed := make(map[string]bool, len(visible))
	for _, name := range visible {
		allowed[name] = true
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := filepath.Base(r.URL.Path)
		switch {
		case r.Method == http.MethodGet && base == "catalog.json":
			if !strings.Contains(r.URL.Path, "/files/"+ncRecordingsOwner+"/") {
				t.Errorf("catalog fetched as %s, want the owner", r.URL.Path)
			}
			_, _ = w.Write([]byte(catalog))
		case r.Method == "PROPFIND":
			w.WriteHeader(http.StatusMultiStatus)
			body := strings.Builder{}
			body.WriteString(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:">`)
			body.WriteString(`<d:response><d:href>/remote.php/dav/files/alice/Cassini/Recordings/meetings/</d:href></d:response>`)
			for _, name := range visible {
				body.WriteString(`<d:response><d:href>/remote.php/dav/files/alice/Cassini/Recordings/meetings/` + name + `</d:href></d:response>`)
			}
			body.WriteString(`</d:multistatus>`)
			_, _ = w.Write([]byte(body.String()))
		case r.Method == http.MethodGet && strings.HasSuffix(base, ".opus"):
			fetched = append(fetched, base)
			if !strings.Contains(r.URL.Path, "/files/alice/") {
				t.Errorf("recording fetched as %s, want the caller", r.URL.Path)
			}
			if !allowed[base] {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(opus)
		default:
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &fetched
}

// contextTestConfig is an AppAPI-active, nextcloud-files config pointed at a
// stub Nextcloud and a stub CLI.
func contextTestConfig(ncURL, cassiniBin string) ExAppConfig {
	cfg := testExAppConfig(ncURL)
	cfg.PublishSink = publishSinkNextcloudFiles
	cfg.CassiniBin = cassiniBin
	return cfg
}

// echoingCassini stands in for the CLI: it records the argv it was called with
// and prints document, so the wiring can be asserted without a real recording.
func echoingCassini(t *testing.T, document string) (bin string, argvPath string, catalogCopy string) {
	t.Helper()
	dir := t.TempDir()
	argvPath = filepath.Join(dir, "argv")
	catalogCopy = filepath.Join(dir, "catalog.json")
	docPath := filepath.Join(dir, "document")
	if err := os.WriteFile(docPath, []byte(document), 0o600); err != nil {
		t.Fatalf("write document: %v", err)
	}
	// The staging directory is removed the moment the request finishes, so what
	// the CLI was handed has to be copied out while it still exists.
	script := "printf '%s\\n' \"$@\" > " + argvPath + "\n" +
		"for arg in \"$@\"; do case \"$arg\" in *catalog.json) cp \"$arg\" " + catalogCopy + ";; esac; done\n" +
		"cat " + docPath + "\n"
	return writeFakeCassini(t, script), argvPath, catalogCopy
}

func contextRequest(target string) *http.Request {
	return callerReq(http.MethodGet, target, "alice")
}

func TestMeetingsContextServesTheDocumentTheCLIPrinted(t *testing.T) {
	srv, fetched := stubRecordingsDAV(t, contextCatalog, []byte("OPUSBYTES"), "MEETING1.opus", "MEETING2.opus")
	const document = "# Backlog Review\n\n---\n\n# Daily Standup\n"
	bin, argvPath, catalogCopy := echoingCassini(t, document)

	handler := contextTestConfig(srv.URL, bin).meetingsContextHandler(log.New(&bytes.Buffer{}, "", 0))
	if handler == nil {
		t.Fatal("expected a handler for an AppAPI-active nextcloud-files config")
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, contextRequest("/published/meetings-context?id=MEETING2&id=MEETING1"))

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	if w.Body.String() != document {
		t.Errorf("body = %q, want the CLI's own bytes %q", w.Body.String(), document)
	}
	if got := w.Header().Get("Content-Type"); got != "text/markdown; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := w.Header().Get(ncFilesSourceHeader); got != ncFilesSourceValue {
		t.Errorf("%s = %q, want %q", ncFilesSourceHeader, got, ncFilesSourceValue)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}

	// Each recording is fetched as the caller (asserted by the stub), in the
	// order the ids were given, which is the order the document holds them in.
	if len(*fetched) != 2 || (*fetched)[0] != "MEETING2.opus" || (*fetched)[1] != "MEETING1.opus" {
		t.Errorf("fetched %v, want [MEETING2.opus MEETING1.opus]", *fetched)
	}

	argv := readArgv(t, argvPath)
	if len(argv) < 6 || argv[0] != "meetings" || argv[1] != "context" || argv[2] != "--local" || argv[3] != "--catalog" {
		t.Fatalf("argv = %v, want a --local run with a catalog", argv)
	}
	// The catalog handed to the CLI is the caller's own filtered one, so each
	// meeting's room is read from the entry an id run would have read it from.
	if filepath.Base(argv[4]) != "catalog.json" {
		t.Errorf("--catalog got %q, want a catalog.json", argv[4])
	}
	catalog, err := os.ReadFile(catalogCopy)
	if err != nil {
		t.Fatalf("read the catalog the CLI was handed: %v", err)
	}
	if !strings.Contains(string(catalog), "rm_11bb22cc33dd44ee") || strings.Contains(string(catalog), "SECRET") {
		t.Errorf("staged catalog is not the caller's filtered one: %s", catalog)
	}
	// The staged file names carry the meeting ids: that is where --local reads
	// them from, and it is what makes the two documents agree.
	for i, want := range []string{"MEETING2.opus", "MEETING1.opus"} {
		if got := filepath.Base(argv[len(argv)-2+i]); got != want {
			t.Errorf("staged file %d = %q, want %q (argv=%v)", i, got, want, argv)
		}
	}
}

func TestMeetingsContextPassesTheRequestedFlagsThrough(t *testing.T) {
	srv, _ := stubRecordingsDAV(t, contextCatalog, []byte("OPUSBYTES"), "MEETING1.opus")
	bin, argvPath, _ := echoingCassini(t, `{"version":"cassini.meetings.context.v1"}`)
	handler := contextTestConfig(srv.URL, bin).meetingsContextHandler(nil)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, contextRequest("/published/meetings-context?id=MEETING1&format=json&timestamps=true"))

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	argv := strings.Join(readArgv(t, argvPath), " ")
	for _, want := range []string{"--json", "--timestamps"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv %q does not carry %s", argv, want)
		}
	}
}

// The whole point of the access model: an id you may not read and an id that
// does not exist are the same answer. SECRET is in the authoritative catalog
// and absent from alice's scan.
func TestMeetingsContextAnswers404ForAnIDTheCallerMayNotRead(t *testing.T) {
	srv, fetched := stubRecordingsDAV(t, contextCatalog, []byte("OPUSBYTES"), "MEETING1.opus", "MEETING2.opus")
	bin := writeFakeCassini(t, "echo 'the CLI must not run for a denied id' >&2\nexit 9\n")
	handler := contextTestConfig(srv.URL, bin).meetingsContextHandler(log.New(&bytes.Buffer{}, "", 0))

	for _, id := range []string{"SECRET", "NEVER-EXISTED"} {
		t.Run(id, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, contextRequest("/published/meetings-context?id=MEETING1&id="+id))
			if w.Code != http.StatusNotFound {
				t.Fatalf("code = %d, want 404 (%s)", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), id) {
				t.Errorf("the 404 body names the id: %q", w.Body.String())
			}
		})
	}
	for _, name := range *fetched {
		if name != "MEETING1.opus" {
			t.Errorf("a denied id must not be fetched, saw %s", name)
		}
	}
}

// The second gate: the ACL can change between the caller's scan and the fetch,
// and the fetch is made as the caller precisely so Nextcloud gets the last word.
func TestMeetingsContextAnswers404WhenTheFetchIsDeniedAfterTheScan(t *testing.T) {
	// The scan says alice may read MEETING1; the fetch says otherwise, which is
	// what an ACL changed between the two looks like.
	var logs bytes.Buffer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "catalog.json"):
			_, _ = w.Write([]byte(contextCatalog))
		case r.Method == "PROPFIND":
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = w.Write([]byte(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:">` +
				`<d:response><d:href>/remote.php/dav/files/alice/Cassini/Recordings/meetings/MEETING1.opus</d:href></d:response>` +
				`</d:multistatus>`))
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer srv.Close()
	handler := contextTestConfig(srv.URL, writeFakeCassini(t, "exit 9\n")).meetingsContextHandler(log.New(&logs, "", 0))

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, contextRequest("/published/meetings-context?id=MEETING1"))

	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404 (%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(logs.String(), "denied") {
		t.Errorf("a denial must stay distinguishable in the log: %s", logs.String())
	}
}

// Loud failures: none of these may look like a bundle, and none may look empty.
func TestMeetingsContextFailsLoudly(t *testing.T) {
	opus := []byte("OPUSBYTES")

	t.Run("no caller identity", func(t *testing.T) {
		srv, _ := stubRecordingsDAV(t, contextCatalog, opus, "MEETING1.opus")
		handler := contextTestConfig(srv.URL, writeFakeCassini(t, "exit 9\n")).meetingsContextHandler(log.New(&bytes.Buffer{}, "", 0))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, callerReq(http.MethodGet, "/published/meetings-context?id=MEETING1", ""))
		if w.Code != http.StatusBadGateway {
			t.Fatalf("code = %d, want 502 — a missing identity is not an empty answer", w.Code)
		}
	})

	t.Run("the authoritative catalog is unreadable", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		handler := contextTestConfig(srv.URL, writeFakeCassini(t, "exit 9\n")).meetingsContextHandler(log.New(&bytes.Buffer{}, "", 0))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, contextRequest("/published/meetings-context?id=MEETING1"))
		if w.Code != http.StatusBadGateway {
			t.Fatalf("code = %d, want 502", w.Code)
		}
	})

	t.Run("the CLI fails", func(t *testing.T) {
		srv, _ := stubRecordingsDAV(t, contextCatalog, opus, "MEETING1.opus")
		bin := writeFakeCassini(t, "echo 'read the downloaded meeting: not a portable meeting' >&2\nexit 1\n")
		var logs bytes.Buffer
		handler := contextTestConfig(srv.URL, bin).meetingsContextHandler(log.New(&logs, "", 0))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, contextRequest("/published/meetings-context?id=MEETING1"))
		if w.Code != http.StatusBadGateway {
			t.Fatalf("code = %d, want 502 — a failed render is not an empty document", w.Code)
		}
		if !strings.Contains(logs.String(), "not a portable meeting") {
			t.Errorf("the child's own explanation must reach the log: %s", logs.String())
		}
	})

	t.Run("the CLI prints nothing", func(t *testing.T) {
		srv, _ := stubRecordingsDAV(t, contextCatalog, opus, "MEETING1.opus")
		handler := contextTestConfig(srv.URL, writeFakeCassini(t, "exit 0\n")).meetingsContextHandler(log.New(&bytes.Buffer{}, "", 0))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, contextRequest("/published/meetings-context?id=MEETING1"))
		if w.Code != http.StatusBadGateway {
			t.Fatalf("code = %d, want 502 — an empty 200 would read as 'these meetings say nothing'", w.Code)
		}
	})

	t.Run("the recording is empty", func(t *testing.T) {
		srv, _ := stubRecordingsDAV(t, contextCatalog, nil, "MEETING1.opus")
		handler := contextTestConfig(srv.URL, writeFakeCassini(t, "exit 0\n")).meetingsContextHandler(log.New(&bytes.Buffer{}, "", 0))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, contextRequest("/published/meetings-context?id=MEETING1"))
		if w.Code != http.StatusBadGateway {
			t.Fatalf("code = %d, want 502", w.Code)
		}
	})
}

// Every one of these is refused before a single Nextcloud call: a bad request
// must cost no downloads and must never half-run.
func TestMeetingsContextRejectsABadRequestBeforeCallingNextcloud(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("upstream must not be called for a bad request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	handler := contextTestConfig(srv.URL, writeFakeCassini(t, "exit 9\n")).meetingsContextHandler(nil)

	many := strings.Builder{}
	for i := 0; i <= maxContextMeetings; i++ {
		many.WriteString("&id=M")
		many.WriteString(string(rune('a' + i%26)))
		many.WriteString(string(rune('a' + i/26)))
	}

	for _, tc := range []struct {
		name  string
		query string
	}{
		{"no ids", "/published/meetings-context"},
		{"an empty id", "/published/meetings-context?id=%20"},
		{"the same id twice", "/published/meetings-context?id=M1&id=M1"},
		{"more meetings than a bundle holds", "/published/meetings-context?x=1" + many.String()},
		{"an id that is a path", "/published/meetings-context?id=../../etc/passwd"},
		{"an unknown format", "/published/meetings-context?id=M1&format=yaml"},
		{"an unreadable timestamps flag", "/published/meetings-context?id=M1&timestamps=perhaps"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, contextRequest(tc.query))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("code = %d, want 400 (%s)", w.Code, w.Body.String())
			}
		})
	}
}

// The staging directory holds whole recordings. One leaked per abandoned
// request is an archive on the ExApp volume, outside the access model.
func TestMeetingsContextLeavesNoStagedRecordings(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	srv, _ := stubRecordingsDAV(t, contextCatalog, []byte("OPUSBYTES"), "MEETING1.opus")

	for _, script := range []string{"echo doc\n", "exit 1\n"} {
		handler := contextTestConfig(srv.URL, writeFakeCassini(t, script)).meetingsContextHandler(log.New(&bytes.Buffer{}, "", 0))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, contextRequest("/published/meetings-context?id=MEETING1"))
	}

	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatalf("read %s: %v", tmp, err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "cassini-meetings-context-") {
			t.Errorf("staging directory left behind: %s", entry.Name())
		}
	}
}

// Where the route cannot be served correctly it is not served at all, rather
// than answering with something that looks like an access decision.
func TestMeetingsContextIsNotMountedWhereItCannotBeServed(t *testing.T) {
	bin := writeFakeCassini(t, "exit 0\n")
	for _, tc := range []struct {
		name string
		cfg  ExAppConfig
	}{
		{"outside AppAPI", func() ExAppConfig {
			cfg := contextTestConfig("", bin)
			cfg.AppSecret = ""
			return cfg
		}()},
		{"under the local sink", func() ExAppConfig {
			cfg := contextTestConfig("https://nc.example.com", bin)
			cfg.PublishSink = "local"
			return cfg
		}()},
		{"with no cassini binary", contextTestConfig("https://nc.example.com", "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if handler := tc.cfg.meetingsContextHandler(log.New(&bytes.Buffer{}, "", 0)); handler != nil {
				t.Errorf("expected no handler")
			}
		})
	}
}

// publishedHandler must route the path itself: it is a sibling of catalog.json,
// not a file under meetings/, so nothing in Nextcloud Files answers it.
func TestPublishedHandlerRoutesTheContextPath(t *testing.T) {
	proxy, seen := stubProxy(t, `{"version":"cassini.viewer.catalog.v1","meetings":[]}`)
	served := false
	context := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served = true
		_, _ = w.Write([]byte("bundle"))
	})
	h := publishedHandler("", "/published", log.New(&bytes.Buffer{}, "", 0), proxy, context)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/published/meetings-context?id=M1", nil))
	if !served || w.Body.String() != "bundle" {
		t.Fatalf("context path not routed: served=%v body=%q", served, w.Body.String())
	}
	if len(*seen) != 0 {
		t.Errorf("the Nextcloud proxy was asked for %v; nothing there answers this path", *seen)
	}

	// And without a handler it is an ordinary miss, not a half-answer.
	h = publishedHandler("", "/published", log.New(&bytes.Buffer{}, "", 0), proxy, nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/published/meetings-context?id=M1", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", w.Code)
	}
}

func readArgv(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
}

// TestMeetingsContextBytesMatchTheCLIOverTheSameIDs is the golden the ticket
// asks for, end to end and with nothing stubbed between the two answers.
//
// It builds the real `cassini`, packs a real portable meeting, serves it from a
// stub Nextcloud, and then asks the same question twice: once through
// `published/meetings-context`, and once by running `cassini meetings context
// <ids>` against the operator's own published routes. Both go through the same
// per-caller catalog and the same per-caller recording fetch; only the last
// step differs, which is exactly the step this endpoint adds. The bytes must be
// equal.
//
// Skipped where the toolchain to build the sibling module or the media tools to
// pack a recording are unavailable — the two modules do not build together, so
// this cannot be a plain unit test.
func TestMeetingsContextBytesMatchTheCLIOverTheSameIDs(t *testing.T) {
	cassiniBin := buildCassiniCLI(t)
	opus := packPortableMeeting(t, cassiniBin)

	srv, _ := stubRecordingsDAV(t, contextCatalog, opus, "MEETING1.opus", "MEETING2.opus")
	cfg := contextTestConfig(srv.URL, cassiniBin)
	logger := log.New(&bytes.Buffer{}, "", 0)
	published := publishedHandler("", "/published", logger, cfg.ncFilesProxy(logger), cfg.meetingsContextHandler(logger))

	// The CLI talks to the app through Nextcloud's AppAPI proxy, which mints the
	// caller identity. This mux is that proxy: it strips the proxied prefix and
	// puts alice on the context, so the CLI reads the same per-caller catalog
	// and the same per-caller recordings the endpoint does.
	const proxyPrefix = "/index.php/apps/app_api/proxy/gocassini"
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = strings.TrimPrefix(r.URL.Path, proxyPrefix)
		published.ServeHTTP(w, callerReq(r.Method, r.URL.String(), "alice"))
	}))
	defer app.Close()

	for _, mode := range []struct {
		name  string
		query string
		flags []string
	}{
		{"markdown", "", nil},
		{"json", "&format=json", []string{"--json"}},
		{"markdown with timestamps", "&timestamps=true", []string{"--timestamps"}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			published.ServeHTTP(w, contextRequest("/published/meetings-context?id=MEETING2&id=MEETING1"+mode.query))
			if w.Code != http.StatusOK {
				t.Fatalf("endpoint code = %d (%s)", w.Code, w.Body.String())
			}

			args := append([]string{"meetings", "context", "MEETING2", "MEETING1",
				"--nextcloud-url", app.URL, "--user", "alice", "--app-password", "app-pw-1234"}, mode.flags...)
			cmd := exec.Command(cassiniBin, args...)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("cassini meetings context: %v: %s", err, stderr.String())
			}

			if w.Body.String() != stdout.String() {
				t.Errorf("the endpoint's bundle is not the CLI's.\n--- endpoint ---\n%s\n--- cli ---\n%s", w.Body.String(), stdout.String())
			}
			if !strings.Contains(stdout.String(), "rm_11bb22cc33dd44ee") {
				t.Errorf("the fixture no longer carries a room, so this proves nothing:\n%s", stdout.String())
			}
		})
	}
}

// buildCassiniCLI builds the real CLI out of the sibling module. It is a
// separate Go module with no dependency in either direction, which is the whole
// reason this endpoint shells out — so an end-to-end test has to build it.
func buildCassiniCLI(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	recorder, err := filepath.Abs(filepath.Join("..", "..", "..", "cassini-go-recorder"))
	if err != nil {
		t.Fatalf("resolve the recorder module: %v", err)
	}
	if _, err := os.Stat(filepath.Join(recorder, "go.mod")); err != nil {
		t.Skipf("cassini-go-recorder is not checked out beside this module: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "cassini")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/cassini")
	cmd.Dir = recorder
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot build the cassini CLI here: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return bin
}

// packPortableMeeting builds one real portable .opus, using ffmpeg for the
// audio and the CLI's own packer for the file, so what the endpoint reads is a
// meeting rather than a placeholder.
func packPortableMeeting(t *testing.T, cassiniBin string) []byte {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	dir := t.TempDir()
	bundle := filepath.Join(dir, "demo.meeting")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	if out, err := exec.Command("ffmpeg", "-y", "-v", "error", "-f", "lavfi",
		"-i", "sine=frequency=660:sample_rate=48000:duration=0.25",
		"-c:a", "libopus", "-application", "voip",
		filepath.Join(bundle, "meeting.webm")).CombinedOutput(); err != nil {
		t.Fatalf("write meeting audio: %v: %s", err, out)
	}
	writeFile(t, filepath.Join(bundle, "transcript.words.v1.json"), `{
  "version": "transcript.words.v1",
  "media": {"src": "meeting.webm", "durationMs": 250},
  "speakers": [{"id": "spk_host", "label": "Host"}],
  "segments": [{"speaker": "spk_host", "startMs": 0, "endMs": 200, "text": "hello team",
    "words": [{"text": "hello", "startMs": 0, "endMs": 80}, {"text": "team", "startMs": 100, "endMs": 200}]}]
}`)
	writeFile(t, filepath.Join(bundle, "manifest.json"), `{
  "version": "cassini.meeting-artifact.v1",
  "generatedAt": "2026-03-11T10:00:00Z",
  "source": {"basename": "source.mkv", "durationMs": 250},
  "files": {"audio": "meeting.webm", "transcript": "transcript.words.v1.json"},
  "speakerCount": 1,
  "wordCount": 2
}`)
	// The bundle manifest is what `cassini pack` reads to decide the directory
	// is a finished .meeting rather than a directory of files.
	writeFile(t, filepath.Join(bundle, "cassini.json"), `{
  "kind": "meeting",
  "version": "cassini.meeting.v1",
  "created_at_utc": "2026-03-11T10:00:00Z",
  "state": "ready",
  "stage": "ready",
  "source_kind": "mkv",
  "source_path": "/tmp/source.mkv",
  "artifact_manifest": "manifest.json",
  "files": {"audio": "meeting.webm", "transcript": "transcript.words.v1.json", "artifact_manifest": "manifest.json"}
}`)

	out := filepath.Join(dir, "meeting.opus")
	cmd := exec.Command(cassiniBin, "pack", bundle, "--out", out, "--title", "Daily Standup")
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot pack a portable meeting here: %v: %s", err, strings.TrimSpace(string(combined)))
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read packed meeting: %v", err)
	}
	return body
}
