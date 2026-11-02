package operator

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cassini-operator/internal/operator/appapi"
)

const (
	supportUserPath    = "/support/bundle"
	supportAdminPath   = "/support-bundle" // mounted under the operator base
	supportMaxBytes    = 20 << 20
	supportLogTail     = 256 << 10
	supportMaxJobs     = 50
	supportMaxAttempts = 3
	supportDeadline    = 15 * time.Second
)

// An interrupted process cannot run request defers. Remove only old regular
// files created by this exporter; never traverse a directory or follow a link.
func cleanupSupportTemps(dir string, now time.Time) {
	paths, _ := filepath.Glob(filepath.Join(dir, "cassini-support-*.zip"))
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err == nil && info.Mode().IsRegular() && now.Sub(info.ModTime()) > 24*time.Hour {
			_ = os.Remove(path)
		}
	}
}

type supportGate struct {
	mu     sync.Mutex
	active map[string]bool
}

func (g *supportGate) acquire(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active == nil {
		g.active = map[string]bool{}
	}
	if len(g.active) >= 2 || g.active[key] {
		return false
	}
	g.active[key] = true
	return true
}
func (g *supportGate) release(key string) { g.mu.Lock(); delete(g.active, key); g.mu.Unlock() }

// Browser context accepts bounded catalog-error text and fixed diagnostic fields,
// never a console dump, browser storage, request headers, actor or scope override.
type supportBrowser struct {
	CatalogError   string `json:"catalog_error,omitempty"`
	Privacy        string `json:"privacy"`
	Surface        string `json:"surface"`
	TimezoneOffset int    `json:"timezone_offset_minutes"`
	Online         bool   `json:"online"`
	CatalogState   string `json:"catalog_state"`
	CatalogReason  string `json:"catalog_reason"`
}

func readSupportBrowser(w http.ResponseWriter, r *http.Request) (supportBrowser, error) {
	var b supportBrowser
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if err := d.Decode(&b); err != nil {
		return b, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return b, errors.New("extra data")
	}
	if b.Privacy == "" {
		b.Privacy = "original"
	}
	if b.Privacy != "original" && b.Privacy != "scrubbed" {
		return b, errors.New("invalid privacy mode")
	}
	if len(b.CatalogError) > 3000 {
		return b, errors.New("catalog error exceeds limit")
	}
	if b.Privacy == "scrubbed" {
		b.CatalogError = ""
	}
	if supportEnum(b.Surface, "browse", "operator") == "unknown" || b.TimezoneOffset < -840 || b.TimezoneOffset > 840 ||
		supportEnum(b.CatalogState, "not_requested", "loading", "loaded", "failed") == "unknown" ||
		supportEnum(b.CatalogReason, "none", "network_error", "request_failed") == "unknown" {
		return b, errors.New("invalid browser context")
	}
	return b, nil
}

func (rt *Runtime) supportBundleHandler(c ExAppConfig, admin bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		wantPath := supportUserPath
		if admin {
			wantPath = supportAdminPath
		}
		if r.URL.Path != wantPath {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		controller := http.NewResponseController(w)
		_ = controller.SetReadDeadline(time.Now().Add(5 * time.Second))
		defer controller.SetReadDeadline(time.Time{})
		_ = controller.SetWriteDeadline(time.Now().Add(40 * time.Second))
		defer controller.SetWriteDeadline(time.Time{})
		caller := appapi.UserID(r.Context())
		if !appapi.Authenticated(r.Context()) {
			// The installed ADMIN route is the AppAPI permission boundary, as
			// for the other operator APIs. Direct access additionally requires a
			// configured token, even on an otherwise open standalone operator.
			token := strings.TrimSpace(rt.cfg.APIToken)
			auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !admin || token == "" || subtle.ConstantTimeCompare([]byte(auth), []byte(token)) != 1 {
				writeJSONError(w, http.StatusUnauthorized, "support download requires authentication")
				return
			}
		} else if caller == "" {
			writeJSONError(w, http.StatusUnauthorized, "support download requires a signed-in user")
			return
		}
		browser, err := readSupportBrowser(w, r)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid support request")
			return
		}
		exportKey := "user:" + caller
		if caller == "" {
			exportKey = "standalone"
		}
		if !rt.supportGate.acquire(exportKey) {
			w.Header().Set("Retry-After", "15")
			writeJSONError(w, http.StatusTooManyRequests, "a support download is already being prepared; try again shortly")
			return
		}
		defer rt.supportGate.release(exportKey)
		ctx, cancel := context.WithTimeout(r.Context(), supportDeadline)
		defer cancel()
		bundle, err := rt.collectSupport(ctx, c, caller, admin, browser)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "could not prepare support bundle")
			return
		}
		file, err := os.CreateTemp("", "cassini-support-*.zip")
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "could not prepare support bundle")
			return
		}
		defer os.Remove(file.Name())
		defer file.Close()
		if err := bundle.writeZIP(file); err != nil || r.Context().Err() != nil {
			writeJSONError(w, http.StatusInternalServerError, "could not prepare support bundle")
			return
		}
		info, err := file.Stat()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "could not prepare support bundle")
			return
		}
		name := "cassini-support-" + bundle.manifest.Privacy + "-" + bundle.now.Format("20060102T150405Z") + "-" + bundle.id + ".zip"
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
		// The ZIP is complete before any response bytes are sent. ServeContent
		// would interpret If-Modified-Since and Range on this private POST.
		_, _ = file.Seek(0, io.SeekStart)
		_, _ = io.Copy(w, file)
	})
}

type supportSource struct {
	Name       string `json:"name"`
	State      string `json:"state"`
	Bytes      int    `json:"bytes,omitempty"`
	Reason     string `json:"reason,omitempty"`
	CapturedAt string `json:"captured_at"`
}
type supportManifest struct {
	Privacy     string          `json:"privacy"`
	Schema      string          `json:"schema"`
	ID          string          `json:"id"`
	Scope       string          `json:"scope"`
	GeneratedAt string          `json:"generated_at"`
	WindowStart string          `json:"window_start"`
	Limits      map[string]int  `json:"limits"`
	Sources     []supportSource `json:"sources"`
}
type supportBundle struct {
	now       time.Time
	id        string
	salt      []byte
	manifest  supportManifest
	entries   map[string][]byte
	remaining int
}

func newSupportBundle(admin bool) (*supportBundle, error) {
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	b := &supportBundle{now: now, id: hex.EncodeToString(salt[:6]), salt: salt, entries: map[string][]byte{}, remaining: supportMaxBytes - 256*1024}
	scope := "user"
	if admin {
		scope = "administrator"
	}
	b.manifest = supportManifest{Schema: "cassini.support.v1", Privacy: "original", ID: b.id, Scope: scope, GeneratedAt: now.Format(time.RFC3339Nano), WindowStart: now.AddDate(0, 0, -7).Format(time.RFC3339Nano), Sources: []supportSource{}, Limits: map[string]int{"jobs": supportMaxJobs, "attempts_per_job": supportMaxAttempts, "log_tail_bytes": supportLogTail, "uncompressed_bytes": supportMaxBytes, "collection_seconds": 15}}
	return b, nil
}

func (b *supportBundle) ref(value string) string {
	if value == "" {
		return ""
	}
	h := hmac.New(sha256.New, b.salt)
	_, _ = h.Write([]byte(value))
	return "ref-" + hex.EncodeToString(h.Sum(nil)[:8])
}
func (b *supportBundle) omitted(name, reason string) {
	b.manifest.Sources = append(b.manifest.Sources, supportSource{Name: name, State: "omitted", Reason: reason, CapturedAt: nowUTCString()})
}
func (b *supportBundle) add(name string, value any) {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		b.omitted(name, "encoding_failed")
		return
	}
	b.addBytes(name, append(raw, '\n'), false)
}
func (b *supportBundle) addBytes(name string, raw []byte, truncated bool) {
	if len(raw) > b.remaining {
		b.omitted(name, "bundle_size_limit")
		return
	}
	b.remaining -= len(raw)
	b.entries[name] = raw
	state := "included"
	if truncated {
		state = "truncated"
	}
	b.manifest.Sources = append(b.manifest.Sources, supportSource{Name: name, State: state, Bytes: len(raw), CapturedAt: nowUTCString()})
}
func (b *supportBundle) writeZIP(w io.Writer) error {
	// Fixed entry names and generated pseudonyms only. No persisted filename
	// or error text can control a ZIP entry name.
	privacyNote := "Privacy filtering is OFF. Original diagnostic logs and error messages may contain\nnames, URLs, credentials or meeting text. Review and edit the files before sharing\nif needed, then create a new ZIP, or download again with scrubbing enabled.\n"
	if b.scrubbed() {
		privacyNote = "Privacy filtering is ON. This bundle uses projected diagnostics and pseudonymous\njob references; original log prose and error details are excluded. Review before sharing.\n"
	}
	// The initial byte reserve covers mandatory metadata and this explanation,
	// so even a full original-log budget cannot omit the README.
	readme := []byte("Cassini support bundle\n\n" + privacyNote + supportReadme)
	b.entries["README.txt"] = readme
	b.manifest.Sources = append(b.manifest.Sources, supportSource{Name: "README.txt", State: "included", Bytes: len(readme), CapturedAt: nowUTCString()})
	raw, err := json.MarshalIndent(b.manifest, "", "  ")
	if err != nil {
		return err
	}
	b.entries["manifest.json"] = raw
	total := 0
	names := make([]string, 0, len(b.entries))
	for name, content := range b.entries {
		total += len(content)
		names = append(names, name)
	}
	if total > supportMaxBytes {
		return errors.New("support bundle exceeds size limit")
	}
	sort.Strings(names)
	z := zip.NewWriter(w)
	for _, name := range names {
		entry, err := z.Create(name)
		if err != nil {
			_ = z.Close()
			return err
		}
		if _, err = entry.Write(b.entries[name]); err != nil {
			_ = z.Close()
			return err
		}
	}
	return z.Close()
}

const supportReadme = `
This ZIP was downloaded locally. Cassini has not sent it to support.
Review its contents before attaching it to your support request.

The manifest records the scope, coverage window, limits and unavailable sources.
This is a best-effort snapshot, not a transaction across all sources.
The manifest records whether privacy filtering was enabled. Original bundles keep
diagnostic text as captured, within the stated limits. Scrubbed bundles retain
recognized categories and status codes; unfamiliar errors remain unclassified.
Archive entry names always use generated references, independently of filtering.
Media, transcript/summary documents, speaker-edit files, raw databases and settings
files are not collected. Their contents or credentials may still occur in original logs.

A user bundle contains browser context, setup verdict and the caller's fresh
received-share and pending-share results. It contains no installation logs or inaccessible meetings.
If recordings are missing, ask an administrator to download a bundle as well.
An administrator bundle adds bounded recent job history, stage logs,
cached health findings, configuration metadata and persistent diagnostic events.
Operator/share diagnostics start with the release that introduced support bundles;
older stderr and process-local warnings cannot be recovered. Rotated, expired or
unrecognized diagnostics may be absent. Current share policy may differ from the
policy that applied when a recording was published.
`

func (b *supportBundle) scrubbed() bool { return b.manifest.Privacy == "scrubbed" }

func supportOriginalError(err error) string {
	if err == nil {
		return ""
	}
	detail := err.Error()
	var share *ncShareError
	if errors.As(err, &share) {
		detail += "\nNextcloud message: " + share.Message
	}
	if len(detail) > 16<<10 {
		const marker = "\n[truncated]"
		detail, _ = supportTextPrefix(detail, (16<<10)-len(marker))
		detail += marker
	}
	return detail
}

func (rt *Runtime) collectSupport(ctx context.Context, c ExAppConfig, caller string, admin bool, browser supportBrowser) (*supportBundle, error) {
	b, err := newSupportBundle(admin)
	if err != nil {
		return nil, err
	}
	if browser.Privacy == "scrubbed" {
		b.manifest.Privacy = "scrubbed"
	}
	b.add("browser.json", browser)
	// The setup verdict is the same deliberately small USER-visible verdict as
	// /setup. Avoid readiness(): it can inspect the archive and contains room URLs.
	access := ncAccessSubstrate.snapshot(rt.resolvedPublishSinkName())
	b.add("setup.json", map[string]any{"recordings_access": access.OK, "state": supportEnum(access.State, "provisioned", "degraded", "unavailable", "not_applicable", "unknown")})
	if c.appAPIActive() && caller != "" {
		started := time.Now()
		// Keep this request well below the outer collection deadline. It acts
		// only as the caller and makes no mutations or provisioning calls.
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		shares, probeErr := c.supportReceivedShares(probeCtx, caller)
		pending, pendingErr := c.supportPendingShares(probeCtx, caller)
		cancel()
		reason, httpStatus, ocsStatus := supportError(probeErr)
		pendingReason, pendingHTTP, pendingOCS := supportError(pendingErr)
		visibility := map[string]any{
			"checked_at": time.Now().UTC().Format(time.RFC3339Nano), "ok": probeErr == nil,
			"received_recording_shares": supportRecordingShareCount(shares, true), "reason": reason,
			"http_status": httpStatus, "ocs_status": ocsStatus,
			"pending_check_ok": pendingErr == nil, "pending_recording_shares": supportRecordingShareCount(pending, false),
			"pending_reason": pendingReason, "pending_http_status": pendingHTTP, "pending_ocs_status": pendingOCS,
			"duration_ms": time.Since(started).Milliseconds(),
		}
		if !b.scrubbed() {
			// The error belongs to the caller's probe. A transport URL is server
			// configuration rather than the caller's data: never export its
			// embedded credentials to a non-administrator.
			selfErr := probeErr
			var transportErr *url.Error
			if !admin && errors.As(selfErr, &transportErr) {
				selfErr = transportErr.Err
			}
			visibility["error"] = supportOriginalError(selfErr)
			selfPendingErr := pendingErr
			if !admin && errors.As(selfPendingErr, &transportErr) {
				selfPendingErr = transportErr.Err
			}
			visibility["pending_error"] = supportOriginalError(selfPendingErr)
			visibility["caller"] = caller
		}
		b.add("visibility.json", visibility)
	} else {
		b.omitted("visibility.json", "nextcloud_caller_unavailable")
	}
	if !admin {
		return b, nil
	}
	rt.collectSupportHealth(ctx, b)
	if rt.supportJournal == nil {
		b.omitted("logs/operator.jsonl", "diagnostic_history_unavailable")
	} else {
		events, failed := rt.supportJournal.read(b.now.AddDate(0, 0, -7))
		var out bytes.Buffer
		enc := json.NewEncoder(&out)
		for _, e := range events {
			if b.scrubbed() {
				if e.Event == "operator_log" && (e.Reason == "" || e.Reason == "none") {
					continue
				}
				e = safeSupportEvent(e, b.ref)
			}
			_ = enc.Encode(e)
		}
		b.addBytes("logs/operator.jsonl", out.Bytes(), failed)
		capture := map[string]any{"events": len(events), "rotation_bytes": 2 * supportJournalLimit, "io_failed": failed, "older_history": "unavailable_before_support_release_or_after_rotation_and_retention"}
		if len(events) > 0 {
			capture["oldest_event_at"] = supportTime(events[0].At)
			capture["newest_event_at"] = supportTime(events[len(events)-1].At)
		}
		b.add("diagnostic-capture.json", capture)
		if failed {
			b.omitted("diagnostic_capture", "diagnostic_io_failed")
		}
	}
	rt.collectSupportJobs(ctx, b)
	if ctx.Err() != nil {
		b.omitted("collection", "collection_deadline")
	}
	return b, nil
}

// A smaller response bound than general receivedShares: support must work on
// large servers without materializing an arbitrarily large roster. A truncated
// response is a reported failure, never a misleading zero-result success.
func (c ExAppConfig) supportReceivedShares(ctx context.Context, caller string) ([]ncShare, error) {
	client := &http.Client{Timeout: 3 * time.Second, Transport: supportTransport{}}
	return c.receivedShares(ctx, client, caller)
}

func (c ExAppConfig) supportPendingShares(ctx context.Context, caller string) ([]ncShare, error) {
	client := &http.Client{Timeout: 3 * time.Second, Transport: supportTransport{}}
	data, err := c.shareRequest(ctx, client, caller, http.MethodGet, c.shareAPIURL()+"/pending", nil)
	if err != nil {
		return nil, err
	}
	var shares []ncShare
	if err := json.Unmarshal(data, &shares); err != nil {
		return nil, fmt.Errorf("decode pending shares: %w", err)
	}
	return shares, nil
}

func supportRecordingShareCount(shares []ncShare, requireRead bool) int {
	count := 0
	for _, s := range shares {
		// Pending shares legitimately have permissions=0 until acceptance.
		// Counting them diagnoses a missing mount; it never grants access.
		if s.UIDFileOwner == ncRecordingsOwner && (!requireRead || s.Permissions&ncShareRead != 0) && (s.ItemType == "" || s.ItemType == "file") {
			count++
		}
	}
	return count
}

type supportTransport struct{}

func (supportTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err == nil {
		resp.Body = http.MaxBytesReader(nil, resp.Body, 1<<20)
	}
	return resp, err
}

func (rt *Runtime) collectSupportHealth(ctx context.Context, b *supportBundle) {
	s := rt.currentSettings()
	e := rt.effectiveFor(s)
	access := ncAccessSubstrate.snapshot(rt.resolvedPublishSinkName())
	dbOK := rt.store != nil && rt.store.Ping(ctx) == nil
	// No subprocesses, model loads, write checks or network repairs. Health
	// probes remain explicitly initiated in Doctor. Export only cached enums.
	checks := []map[string]any{}
	rt.recordingSetup.mu.Lock()
	for _, check := range append(append([]readinessCheck{}, rt.recordingSetup.checks...), rt.recordingSetup.hostChecks...) {
		item := map[string]any{"id": supportCheckID(check.ID), "state": supportEnum(check.State, "passed", "ok", "failed", "needs_action", "not_verified", "running", "warning", "warn"), "checked_at": supportTime(check.CheckedAt)}
		if !b.scrubbed() {
			item["id"] = check.ID
			item["message"] = check.Message
			item["code"] = check.Code
		}
		checks = append(checks, item)
	}
	rt.recordingSetup.mu.Unlock()
	models := []map[string]any{}
	rt.modelInventoryMu.Lock()
	for device, cache := range rt.modelInventoryCache {
		for _, m := range cache.models {
			if _, known := speechModelSpecs[m.ID]; !known {
				continue
			}
			models = append(models, map[string]any{"model": m.ID, "device": supportEnum(device, "cpu", "cuda"), "installed": m.Installed, "ready": m.Ready, "checked_at": cache.at.UTC().Format(time.RFC3339Nano), "check_failed": cache.err != nil})
		}
	}
	rt.modelInventoryMu.Unlock()
	if len(models) > 0 {
		b.add("models.json", models)
	} else {
		b.omitted("models.json", "model_inventory_not_cached")
	}
	health := map[string]any{"database_ok": dbOK, "recordings_access": supportEnum(access.State, "provisioned", "degraded", "unavailable", "not_applicable", "unknown"), "access_checked_at": supportTime(access.CheckedAt), "access_failure": supportFailure(access.Detail), "share_warning": supportFailure(access.Warning), "checks": checks, "sharing_group_policy": "unknown"}
	if !b.scrubbed() {
		health["recordings_access_detail"] = access
	}
	b.add("health.json", health)
	model := "unknown"
	if _, known := speechModelSpecs[e.Model]; known {
		model = e.Model
	}
	configuration := map[string]any{"app_version": supportVersion(os.Getenv(envAppVersion)), "vcs_revision": supportRevision(buildVCSRevision()), "go_version": runtime.Version(), "os": runtime.GOOS, "architecture": runtime.GOARCH, "publish_sink": supportEnum(rt.resolvedPublishSinkName(), "local", "nextcloud-files"), "transcription_enabled": s.TranscriptionEnabled, "quality": supportEnum(e.Quality, "fast", "balanced", "best"), "device": supportEnum(e.Device, "cpu", "cuda"), "model": model, "recording_secret_configured": rt.cfg.TalkSharedSecret != "", "signalling_secret_configured": rt.signalingInternalSecretConfigured(), "model_downloads_allowed": !rt.cfg.DisallowModelDownload}
	if !b.scrubbed() {
		configuration["app_version"] = os.Getenv(envAppVersion)
		configuration["model"] = e.Model
		configuration["work_root"] = rt.cfg.WorkRoot
		configuration["site_root"] = rt.cfg.SiteRoot
	}
	b.add("configuration.json", configuration)
}

func supportCheckID(id string) string {
	return supportEnum(id, "configuration", "storage", "talk.discovery", "talk.hpb", "talk.handoff", "test", "host.workdir", "host.tmpdir.writable", "archive.search", "processing")
}
func supportVersion(v string) string {
	if !supportVersionPattern.MatchString(v) {
		return "unknown"
	}
	return v
}

var supportVersionPattern = regexp.MustCompile(`^v?[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}(?:-(?:rc|beta|alpha)[0-9]{0,4})?$`)

func supportRevision(v string) string {
	if len(v) != 40 {
		return "unknown"
	}
	if _, err := hex.DecodeString(v); err != nil {
		return "unknown"
	}
	return v
}

type supportAttempt struct {
	Detail              string `json:"detail,omitempty"`
	TranscriptionDetail string `json:"transcription_detail,omitempty"`
	DetailTruncated     bool   `json:"detail_truncated,omitempty"`
	Number              int    `json:"number"`
	Stage               string `json:"stage"`
	State               string `json:"state"`
	CreatedAt           string `json:"created_at"`
	UpdatedAt           string `json:"updated_at"`
	Failure             string `json:"failure"`
	Transcription       string `json:"transcription"`
	TranscriptionReason string `json:"transcription_reason"`
	Summary             string `json:"summary"`
	SummaryReason       string `json:"summary_reason"`
	RecordExitCode      *int   `json:"record_exit_code,omitempty"`
	HTTPStatus          int    `json:"http_status,omitempty"`
	OCSStatus           int    `json:"ocs_status,omitempty"`
}
type supportJob struct {
	ID       string           `json:"id,omitempty"`
	Ref      string           `json:"ref"`
	Attempts []supportAttempt `json:"attempts"`
}

func (rt *Runtime) collectSupportJobs(ctx context.Context, b *supportBundle) {
	if rt.store == nil {
		b.omitted("jobs.json", "database_unavailable")
		return
	}
	rows, err := rt.store.db.QueryContext(ctx, `SELECT id FROM jobs WHERE updated_at >= ? ORDER BY updated_at DESC, id DESC LIMIT ?`, formatUTCString(b.now.AddDate(0, 0, -7)), supportMaxJobs+1)
	if err != nil {
		b.omitted("jobs.json", "database_unavailable")
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		b.omitted("jobs.json", "database_unavailable")
		return
	}
	if len(ids) > supportMaxJobs {
		b.omitted("older_jobs", "job_count_limit")
		ids = ids[:supportMaxJobs]
	}
	jobs := []supportJob{}
	type logRequest struct {
		id, ref string
		attempt int
	}
	var logRequests []logRequest
	for _, id := range ids {
		if ctx.Err() != nil {
			b.omitted("remaining_jobs", "collection_deadline")
			break
		}
		job := supportJob{Ref: b.ref(id), Attempts: []supportAttempt{}}
		if !b.scrubbed() {
			job.ID = id
		}
		attempts, err := rt.store.db.QueryContext(ctx, `SELECT attempt_number, stage, state, created_at, updated_at, substr(COALESCE(error,''),1,16385), transcription_status, transcription_reason, substr(COALESCE(transcription_detail,''),1,4096), summary_status, summary_reason, record_exit_code FROM job_attempts WHERE job_id=? AND updated_at>=? ORDER BY attempt_number DESC LIMIT ?`, id, formatUTCString(b.now.AddDate(0, 0, -7)), supportMaxAttempts+1)
		if err != nil {
			b.omitted("jobs/"+job.Ref, "attempts_unavailable")
			continue
		}
		for attempts.Next() {
			var a supportAttempt
			var failure string
			var transcriptionDetail string
			var transcription, transcriptionReason, summary, summaryReason sql.NullString
			if err = attempts.Scan(&a.Number, &a.Stage, &a.State, &a.CreatedAt, &a.UpdatedAt, &failure, &transcription, &transcriptionReason, &transcriptionDetail, &summary, &summaryReason, &a.RecordExitCode); err != nil {
				break
			}
			if len(job.Attempts) == supportMaxAttempts {
				b.omitted("jobs/"+job.Ref+"/older_attempts", "attempt_count_limit")
				break
			}
			a.Stage = supportStage(a.Stage)
			a.State = supportState(a.State)
			a.CreatedAt = supportTime(a.CreatedAt)
			a.UpdatedAt = supportTime(a.UpdatedAt)
			a.Failure = supportFailure(failure)
			if !b.scrubbed() {
				a.Detail, a.DetailTruncated = supportTextPrefix(failure, 16<<10)
				a.TranscriptionDetail = transcriptionDetail
			}
			if match := supportHTTPPattern.FindStringSubmatch(failure); len(match) > 1 {
				a.HTTPStatus, _ = strconv.Atoi(match[1])
			}
			if match := supportOCSPattern.FindStringSubmatch(failure); len(match) > 1 {
				a.OCSStatus, _ = strconv.Atoi(match[1])
			}
			a.Transcription = supportState(transcription.String)
			a.TranscriptionReason = supportReason(transcriptionReason.String)
			a.Summary = supportState(summary.String)
			a.SummaryReason = supportReason(summaryReason.String)
			job.Attempts = append(job.Attempts, a)
		}
		err = errors.Join(err, attempts.Err(), attempts.Close())
		if err != nil {
			b.omitted("jobs/"+job.Ref, "attempts_unavailable")
		}
		jobs = append(jobs, job)
		for _, a := range job.Attempts {
			logRequests = append(logRequests, logRequest{id, job.Ref, a.Number})
		}
	}
	b.add("jobs.json", jobs)
	// Reserve the identifying job/error metadata and operator evidence before
	// large original log tails consume the archive's byte budget.
	for _, request := range logRequests {
		if ctx.Err() != nil {
			b.omitted("remaining_stage_logs", "collection_deadline")
			break
		}
		for _, stage := range []string{"record", "build", "seal", "publish"} {
			if ctx.Err() != nil {
				break
			}
			rt.collectSupportStageLog(b, request.id, request.ref, request.attempt, stage)
		}
	}
}

func (rt *Runtime) collectSupportStageLog(b *supportBundle, id, ref string, attempt int, stage string) {
	extension := "jsonl"
	if !b.scrubbed() {
		extension = "log"
	}
	name := fmt.Sprintf("logs/%s/attempt-%d/%s.%s", ref, attempt, stage, extension)
	if !validArtifactJob(id) || attempt < 1 {
		b.omitted(name, "invalid_log_reference")
		return
	}
	root, err := os.OpenRoot(rt.cfg.WorkRoot)
	if err != nil {
		b.omitted(name, "source_unavailable")
		return
	}
	defer root.Close()
	rel, err := filepath.Rel(rt.cfg.WorkRoot, attemptLogPath(rt.cfg.WorkRoot, id, attempt, stage))
	if err != nil {
		b.omitted(name, "source_unavailable")
		return
	}
	f, err := supportOpen(root, rel, os.O_RDONLY)
	if err != nil {
		b.omitted(name, "source_missing_or_unsafe")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		b.omitted(name, "source_unavailable")
		return
	}
	start := max(int64(0), info.Size()-supportLogTail)
	if !b.scrubbed() {
		raw, err := io.ReadAll(io.NewSectionReader(f, start, info.Size()-start))
		if err != nil {
			b.omitted(name, "source_unavailable")
			return
		}
		b.addBytes(name, raw, start > 0)
		return
	}
	reader := bufio.NewReader(io.NewSectionReader(f, start, info.Size()-start))
	if start > 0 {
		_, _ = reader.ReadString('\n')
	} // drop a potentially partial line
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), supportLogTail)
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	lines := 0
	for scanner.Scan() {
		lines++
		line := scanner.Text()
		reason := supportFailure(line)
		if reason == "unclassified_failure" || reason == "none" {
			continue
		}
		e := supportEvent{Event: "stage_diagnostic", Job: ref, Attempt: attempt, Stage: stage, Reason: reason}
		if match := supportHTTPPattern.FindStringSubmatch(line); len(match) > 1 {
			e.HTTP, _ = strconv.Atoi(match[1])
		}
		if match := supportOCSPattern.FindStringSubmatch(line); len(match) > 1 {
			e.OCS, _ = strconv.Atoi(match[1])
		}
		_ = enc.Encode(e)
	}
	_ = enc.Encode(map[string]any{"event": "log_projection", "lines_examined": lines, "original_bytes": info.Size(), "raw_text_excluded": true})
	b.addBytes(name, out.Bytes(), start > 0 || scanner.Err() != nil)
}
