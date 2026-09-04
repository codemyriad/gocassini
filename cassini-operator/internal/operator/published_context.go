package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"cassini-operator/internal/operator/appapi"
)

// GET published/meetings-context?id=…&id=… — one cassini.meetings.context.v1
// document for a set of meetings the caller may read (D-717).
//
// The document must be byte-identical to what `cassini meetings context` prints
// for the same meetings, and "identical" is only true by construction if there
// is one implementation. cassini-operator and gocassini are separate Go modules
// with no dependency between them, so the operator cannot import the contract
// package the CLI renders from — it invokes the CLI, exactly as the build, seal
// and publish stages already do, and streams what it printed.
//
// The CLI's own id path is closed to the operator: fetching a meeting by id
// needs the caller's Nextcloud app password, which the operator does not hold
// and cannot mint. So the operator fetches each recording itself — as the
// caller, over WebDAV, so Nextcloud enforces the per-file ACL — and hands the
// CLI the files (`meetings context --local`).
//
// Access is decided in exactly one place: serveFilteredCatalog, the same
// authoritative-catalog-as-owner + PROPFIND-as-caller intersect the viewer's
// catalog goes through. An id outside that set is a 404, identical to an id
// that does not exist, because a recording you may not read must never reveal
// that it exists (the same rule ncFilesProxy applies to meetings/<id>.opus).
// The per-caller download is then a second, independent gate on the same
// question, so the intersect is belt and braces rather than the only lock.
//
// It is archive-relative, a sibling of catalog.json, so it rides the manifest's
// existing `^published\/.+$` USER GET,HEAD route and needs no appinfo/info.xml
// change.
const meetingsContextPath = "meetings-context"

const (
	// maxContextMeetings caps how many meetings one bundle may hold.
	//
	// This is a GET behind a USER gate and every id costs a whole recording
	// download, so an uncapped one is a way to make the operator pull the
	// archive on any account's behalf. Twenty is far past what the feature is
	// for: a bundle is what somebody asks one question of, and twenty meetings
	// of derived prose already overruns the context window of every model that
	// would read it. Above it, 400 — before any Nextcloud call.
	maxContextMeetings = 20

	// maxContextIDLength bounds one id. Ids are ULIDs (26 characters) in
	// practice; the bound exists because the id becomes a file name in the
	// staging directory.
	maxContextIDLength = 128

	// maxContextStagedBytes bounds the recordings one request may stage on
	// disk, across all of its meetings. A published Opus meeting is a few tens
	// of megabytes even for a long call, so a gigabyte is generous for twenty
	// of them and still a real bound on what one request can write.
	maxContextStagedBytes = 1 << 30

	// maxContextDocumentBytes bounds the document itself. It is text — a
	// three-hour meeting renders to a few hundred kilobytes — so this is two
	// orders of magnitude of headroom, and exists so a wedged child cannot
	// fill the volume.
	maxContextDocumentBytes = 64 << 20

	// meetingsContextTimeout bounds the whole run: N downloads plus one CLI
	// invocation that spawns one ffprobe per meeting. Generous, because a slow
	// Nextcloud is an outage rather than a hang, and finite, because a request
	// that never finishes holds a connection and a staging directory.
	meetingsContextTimeout = 5 * time.Minute
)

// meetingsContextRequest is a parsed, validated query. Everything in it is
// checked before the first call to Nextcloud, so a malformed request is a 400
// and never a partially-executed one.
type meetingsContextRequest struct {
	ids        []string
	asJSON     bool
	timestamps bool
}

// meetingsContextHandler returns the handler for published/meetings-context, or
// nil when this deployment cannot serve it.
//
// Nil rather than a handler that always fails: outside an AppAPI deployment
// there is no caller identity and no Nextcloud Files to read as them, and under
// the local sink the archive is a directory on disk with no access model at
// all. In both cases the route falls through to the published file server,
// which 404s — the same answer the rest of the archive gives there.
func (c ExAppConfig) meetingsContextHandler(logger *log.Logger) http.Handler {
	if !c.appAPIActive() || c.PublishSink != publishSinkNextcloudFiles {
		return nil
	}
	if strings.TrimSpace(c.CassiniBin) == "" {
		if logger != nil {
			logger.Printf("meetings context: no cassini binary configured — published/%s is not served", meetingsContextPath)
		}
		return nil
	}
	// Same client shape as the read proxy: no overall timeout, because bodies
	// stream and the request context governs; a hung upstream is bounded on
	// headers.
	client := &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: ncFilesProxyHeadersTTL}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.serveMeetingsContext(w, r, client, logger)
	})
}

func (c ExAppConfig) serveMeetingsContext(w http.ResponseWriter, r *http.Request, client *http.Client, logger *log.Logger) {
	request, err := parseMeetingsContextRequest(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// These routes are USER-gated by the manifest, so a request with no verified
	// identity means the AppAPI middleware did not run. Answering "you may read
	// nothing" would be a claim about a caller nobody identified, and answering
	// 404 would say the meetings do not exist; both are lies an outage can tell
	// forever. 502 says what is actually true (D-701).
	caller := appapi.UserID(r.Context())
	if caller == "" {
		if logger != nil {
			logger.Printf("meetings context: missing caller identity — refusing to answer")
		}
		http.Error(w, "no verified caller identity", http.StatusBadGateway)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), meetingsContextTimeout)
	defer cancel()

	readable, catalog, ok := c.readableMeetingsForCaller(ctx, client, caller, logger)
	if !ok {
		http.Error(w, "Nextcloud Files unavailable", http.StatusBadGateway)
		return
	}

	staging, err := os.MkdirTemp("", "cassini-meetings-context-*")
	if err != nil {
		if logger != nil {
			logger.Printf("meetings context: create staging directory: %v", err)
		}
		http.Error(w, "meeting context unavailable", http.StatusInternalServerError)
		return
	}
	// Every path, including a cancelled request: the directory holds whole
	// recordings, and one leaked per abandoned request is an archive on the
	// ExApp volume outside the access model.
	defer os.RemoveAll(staging)

	catalogPath := filepath.Join(staging, "catalog.json")
	if err := os.WriteFile(catalogPath, catalog, 0o600); err != nil {
		if logger != nil {
			logger.Printf("meetings context: stage catalog: %v", err)
		}
		http.Error(w, "meeting context unavailable", http.StatusInternalServerError)
		return
	}

	budget := int64(maxContextStagedBytes)
	staged := make([]string, 0, len(request.ids))
	for _, id := range request.ids {
		source, permitted := readable[id]
		if !permitted {
			// Denied and absent are the same answer on purpose. Logged, so an
			// operator can still tell them apart.
			if logger != nil {
				logger.Printf("meetings context: caller=%s asked for id=%s, which is not in their readable set (served as 404)", caller, id)
			}
			http.NotFound(w, r)
			return
		}
		destPath := filepath.Join(staging, id+".opus")
		status, err := c.stageMeetingForContext(ctx, client, caller, source, destPath, &budget)
		switch {
		case err == nil:
		case status == http.StatusNotFound || status == http.StatusUnauthorized || status == http.StatusForbidden:
			// The second gate disagreed with the intersect — the ACL changed
			// between the scan and the fetch, or the recording was removed. Same
			// answer as the first gate gives.
			if logger != nil {
				logger.Printf("meetings context: caller=%s denied id=%s at fetch -> %d (served as 404)", caller, id, status)
			}
			http.NotFound(w, r)
			return
		default:
			if logger != nil {
				logger.Printf("meetings context: stage id=%s for caller=%s: %v", id, caller, err)
			}
			http.Error(w, "Nextcloud Files unavailable", http.StatusBadGateway)
			return
		}
		staged = append(staged, destPath)
	}

	document, size, err := c.renderMeetingsContext(ctx, staging, catalogPath, staged, request, logger)
	if err != nil {
		http.Error(w, "meeting context unavailable", http.StatusBadGateway)
		return
	}
	defer document.Close()

	w.Header().Set("Content-Type", request.contentType())
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set(ncFilesSourceHeader, ncFilesSourceValue)
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, document); err != nil && logger != nil {
		logger.Printf("meetings context: write body for caller=%s: %v", caller, err)
	}
}

func (q meetingsContextRequest) contentType() string {
	if q.asJSON {
		return "application/json"
	}
	return "text/markdown; charset=utf-8"
}

// parseMeetingsContextRequest validates the query completely, before anything
// reaches Nextcloud, so a bad request costs no downloads and cannot half-run.
func parseMeetingsContextRequest(query url.Values) (meetingsContextRequest, error) {
	var request meetingsContextRequest
	seen := make(map[string]bool, len(query["id"]))
	for _, raw := range query["id"] {
		id := strings.TrimSpace(raw)
		if id == "" {
			return request, errors.New("an id parameter is empty")
		}
		if len(id) > maxContextIDLength {
			return request, fmt.Errorf("an id is longer than %d characters", maxContextIDLength)
		}
		if !isPlainMeetingID(id) {
			// The id becomes a file name in the staging directory, so a path
			// separator or a dot segment is refused here rather than sanitised
			// somewhere further in.
			return request, errors.New("an id contains characters a meeting id cannot contain")
		}
		if seen[id] {
			// The same meeting twice is context spent twice on one meeting while
			// reading as coverage of two; the CLI refuses it for the same reason.
			return request, fmt.Errorf("id %q was given more than once", id)
		}
		seen[id] = true
		request.ids = append(request.ids, id)
	}
	if len(request.ids) == 0 {
		return request, errors.New("at least one id parameter is required")
	}
	if len(request.ids) > maxContextMeetings {
		return request, fmt.Errorf("a context bundle holds at most %d meetings, got %d", maxContextMeetings, len(request.ids))
	}

	switch strings.TrimSpace(query.Get("format")) {
	case "", "markdown":
	case "json":
		request.asJSON = true
	default:
		return request, errors.New(`format must be "markdown" or "json"`)
	}
	if raw := strings.TrimSpace(query.Get("timestamps")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return request, errors.New("timestamps must be true or false")
		}
		request.timestamps = value
	}
	return request, nil
}

// isPlainMeetingID reports whether id is safe to use as a file name: one path
// segment of the characters a published meeting id is made of, and not a dot
// segment.
func isPlainMeetingID(id string) bool {
	if id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// readableMeetingsForCaller resolves which meetings this caller may read, keyed
// by id and valued by the archive-relative path of each one's recording, and
// returns the caller's catalog verbatim alongside.
//
// It reuses serveFilteredCatalog rather than repeating its three steps, because
// a second reader must not mean a second access-control path — the intersect
// and its fail-closed behaviour have to be the same ones the viewer's catalog
// goes through, or a bug fixed in one is still live in the other. Capturing the
// response is the price of that: serveFilteredCatalog is written to write, and
// it lives in a file this change does not own.
//
// The catalog bytes are handed to the CLI as --catalog, so each meeting's room
// is read from exactly the entry an id run would have read it from. That is
// what makes the two documents identical rather than merely similar.
//
// A failed per-caller scan reaches here as an EMPTY catalog, not as an error —
// serveFilteredCatalog fails closed — so every requested id then answers 404.
// That is the safe direction (over-restriction, never disclosure) and it is
// indistinguishable from a denial, which is the same thing every other read of
// this archive says. See followups: telling a scan failure apart needs the
// resolveCatalogForCaller extraction D-701 makes.
func (c ExAppConfig) readableMeetingsForCaller(ctx context.Context, client *http.Client, caller string, logger *log.Logger) (map[string]string, []byte, bool) {
	captured := &capturedResponse{header: http.Header{}}
	c.serveFilteredCatalog(ctx, captured, client, caller, logger)
	if captured.statusCode() != http.StatusOK {
		if logger != nil {
			logger.Printf("meetings context: per-caller catalog for caller=%s -> %d", caller, captured.statusCode())
		}
		return nil, nil, false
	}

	body := captured.body.Bytes()
	var document struct {
		Meetings []struct {
			ID           string `json:"id"`
			AudioPath    string `json:"audioPath"`
			ArtifactPath string `json:"artifactPath"`
		} `json:"meetings"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		if logger != nil {
			logger.Printf("meetings context: parse per-caller catalog for caller=%s: %v", caller, err)
		}
		return nil, nil, false
	}

	readable := make(map[string]string, len(document.Meetings))
	for _, entry := range document.Meetings {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			continue
		}
		// audioPath is the contract, not the id; the two coincide today but the
		// exporter owns the path. A meeting recorded before the single-file
		// format has only an artifactPath directory and no recording to read, so
		// it is absent here and answers 404 for the same reason
		// meetings/<id>.opus does: there is no file.
		base := path.Base(strings.TrimSpace(entry.AudioPath))
		if !strings.HasSuffix(base, ".opus") {
			continue
		}
		if _, taken := readable[id]; !taken {
			readable[id] = ncRecordingsRoot + "/meetings/" + base
		}
	}
	return readable, body, true
}

// stageMeetingForContext downloads one recording AS THE CALLER into destPath,
// so Nextcloud enforces the per-file ACL a second time and the catalog
// intersect is not the only thing standing between a caller and a recording.
//
// It returns the upstream status so the caller can keep denied and absent
// indistinguishable, and draws down a shared byte budget so one request cannot
// stage the archive.
func (c ExAppConfig) stageMeetingForContext(ctx context.Context, client *http.Client, caller, relPath, destPath string, budget *int64) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.davFileURL(caller, relPath), nil)
	if err != nil {
		return 0, err
	}
	c.setAppAPIDAVHeadersForUser(req, caller)
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("GET %s -> %d", relPath, resp.StatusCode)
	}

	file, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return resp.StatusCode, err
	}
	defer file.Close()
	// One past the budget, so exhausting it is detectable rather than a silently
	// truncated recording that fails to parse and blames the file.
	written, err := io.Copy(file, io.LimitReader(resp.Body, *budget+1))
	if err != nil {
		return resp.StatusCode, err
	}
	if written > *budget {
		return resp.StatusCode, fmt.Errorf("the requested meetings exceed the %d MiB a single bundle may stage", maxContextStagedBytes>>20)
	}
	if written == 0 {
		return resp.StatusCode, fmt.Errorf("GET %s returned an empty recording", relPath)
	}
	*budget -= written
	return resp.StatusCode, file.Close()
}

// renderMeetingsContext runs the CLI over the staged recordings and returns the
// document it printed, open for reading, with its size.
//
// The output is staged to a file rather than streamed straight to the response
// because the child's exit code is only known after its last byte. Streaming
// would commit a 200 before finding out the run failed, and a failed run that
// looks like a successful empty one is the exact failure this endpoint must not
// have.
func (c ExAppConfig) renderMeetingsContext(ctx context.Context, staging, catalogPath string, staged []string, request meetingsContextRequest, logger *log.Logger) (*os.File, int64, error) {
	args := []string{"meetings", "context", "--local", "--catalog", catalogPath}
	if request.asJSON {
		args = append(args, "--json")
	}
	if request.timestamps {
		args = append(args, "--timestamps")
	}
	args = append(args, staged...)

	outPath := filepath.Join(staging, "context.out")
	out, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, 0, err
	}
	var stderr truncatingBuffer
	stderr.remaining = 8 << 10

	cmd := exec.CommandContext(ctx, c.CassiniBin, args...)
	cmd.Stdout = newCappedWriter(out, "context bundle", maxContextDocumentBytes)
	cmd.Stderr = &stderr
	// Not os.Environ(): this child reads three staged files and prints a
	// document, and it was inheriting the operator's own credentials to do it.
	// APP_SECRET is the one that matters — a process holding the AppAPI shared
	// secret can act as ANY account on the instance, so handing it to a child
	// spawned by any logged-in caller made every bundle request a full
	// impersonation capability for its duration. The LLM variables go too: this
	// verb never calls a model. Same environment the insight path's identical
	// child gets, from the same function, so the two cannot drift (D-700).
	cmd.Env = contextChildEnv(os.Environ())
	// Kill the whole process group on ctx cancel so the ffprobe grandchildren
	// the reader spawns do not outlive an abandoned request.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd.Process) }
	runErr := cmd.Run()
	closeErr := out.Close()
	if runErr == nil {
		runErr = closeErr
	}
	if runErr != nil {
		if logger != nil {
			logger.Printf("meetings context: cassini meetings context failed: %v: %s", runErr, strings.TrimSpace(stderr.buf.String()))
		}
		return nil, 0, runErr
	}

	document, err := os.Open(outPath)
	if err != nil {
		return nil, 0, err
	}
	info, err := document.Stat()
	if err != nil {
		document.Close()
		return nil, 0, err
	}
	if info.Size() == 0 {
		// A bundle always has bytes. An empty 200 here would read as "these
		// meetings say nothing", which is a claim no failure is allowed to make.
		document.Close()
		if logger != nil {
			logger.Printf("meetings context: cassini printed no document: %s", strings.TrimSpace(stderr.buf.String()))
		}
		return nil, 0, errors.New("the context bundle is empty")
	}
	return document, info.Size(), nil
}

// capturedResponse collects a handler's response in memory so a function
// written to serve HTTP can be reused for its answer rather than its output.
type capturedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (c *capturedResponse) Header() http.Header { return c.header }

func (c *capturedResponse) Write(p []byte) (int, error) { return c.body.Write(p) }

func (c *capturedResponse) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}

// statusCode mirrors net/http: a handler that wrote a body without a status
// sent 200.
func (c *capturedResponse) statusCode() int {
	if c.status == 0 {
		return http.StatusOK
	}
	return c.status
}

// cappedWriter fails the write that would cross its limit, rather than
// truncating. A truncated context document is worse than no document: a
// consumer would answer a question out of half a transcript and nothing in the
// bytes would say so.
//
// what and limit exist so the refusal names the bound it actually enforced. Two
// callers pass two different caps — a context bundle here, a much smaller
// insight document in insight_runtime.go — and a message that quoted whichever
// one this file happens to know about would send whoever read it looking at the
// wrong knob (D-700).
type cappedWriter struct {
	out       io.Writer
	what      string
	limit     int64
	remaining int64
}

func newCappedWriter(out io.Writer, what string, limit int64) *cappedWriter {
	return &cappedWriter{out: out, what: what, limit: limit, remaining: limit}
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, fmt.Errorf("the %s exceeds the %d MiB one request may produce", w.what, w.limit>>20)
	}
	w.remaining -= int64(len(p))
	return w.out.Write(p)
}

// truncatingBuffer keeps the first n bytes and swallows the rest. For a child's
// stderr that costs nothing — it is a diagnostic, and the tail of a runaway one
// is not worth holding. For a child's stdout that a caller then decodes
// (settings_workflows.go) the truncation is the point: past the cap the bytes
// stop being parseable JSON, so an unbounded child fails loudly instead of
// being silently believed.
type truncatingBuffer struct {
	buf       bytes.Buffer
	remaining int
}

func (b *truncatingBuffer) Write(p []byte) (int, error) {
	if b.remaining > 0 {
		keep := p
		if len(keep) > b.remaining {
			keep = keep[:b.remaining]
		}
		b.buf.Write(keep)
		b.remaining -= len(keep)
	}
	return len(p), nil
}
