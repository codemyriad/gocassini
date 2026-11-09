package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"cassini-operator/internal/operator/appapi"
)

// The administrator's side of the recordedAtUtc file backfill
// (recorded_at_files.go). Rewriting a delivered file is not free for the
// people it is shared with — each one is a new Nextcloud version and, with
// the Activity app, a "file changed" entry in their stream — so it never runs
// implicitly. The meeting index pass enumerates the files that need it (state
// needed, a database-only check: nothing is downloaded), GET /status reports
// the counts with what Nextcloud says about versions and notifications, and
// two ADMIN routes start and pause the rewrite:
//
//	POST <base>/migrations/recorded-at/start — every needed (and blocked)
//	     file becomes pending, the pause is lifted, the workers wake
//	POST <base>/migrations/recorded-at/pause — no new file is started; the
//	     files being rewritten finish (drain)
//
// Both answer with the same object /status carries as
// recorded_at_migration. appinfo/info.xml declares them ADMIN.

const recordedAtMigrationPath = "/migrations/recorded-at"

// recordedAtBlockedListed bounds the blocked files /status names one by one.
const recordedAtBlockedListed = 20

// recordedAtMigrationStatus is /status's recorded_at_migration.
type recordedAtMigrationStatus struct {
	// Applicable is false where no delivered file can be rewritten (no
	// Nextcloud sink, no CLI): there is nothing to offer.
	Applicable bool `json:"applicable"`
	// State is what the Doctor leads with: not_applicable, checking (the
	// first index pass has not finished and nothing is known yet), needed,
	// running, pausing (paused, files still finishing), paused, blocked,
	// done (every file asked for is done) or not_needed.
	State string `json:"state"`
	// Checked is true once an index pass completed since this start, so
	// Needed is the full enumeration rather than a partial one.
	Checked bool `json:"checked"`
	Paused  bool `json:"paused"`
	// Needed: found, not yet asked for. Queued: asked for, waiting for a
	// worker or a retry. Settling: an upload was sent and its outcome is
	// being confirmed. InProgress: being rewritten right now.
	Needed       int                         `json:"needed"`
	Queued       int                         `json:"queued"`
	Settling     int                         `json:"settling"`
	InProgress   int                         `json:"in_progress"`
	Done         int                         `json:"done"`
	Blocked      int                         `json:"blocked"`
	BlockedItems []recordedAtBlockedFile     `json:"blocked_items,omitempty"`
	Workers      int                         `json:"workers,omitempty"`
	PaceSeconds  int                         `json:"pace_seconds,omitempty"`
	Notices      *recordedAtNotificationsDoc `json:"notifications,omitempty"`
	Error        string                      `json:"error,omitempty"`
}

type recordedAtBlockedFile struct {
	Meeting   string `json:"meeting"`
	Title     string `json:"title,omitempty"`
	DateLabel string `json:"date_label,omitempty"`
	Reason    string `json:"reason"`
}

// recordedAtMigrationStateFor is the State rule, apart so it is testable.
func recordedAtMigrationStateFor(s recordedAtMigrationStatus) string {
	switch {
	case !s.Applicable:
		return "not_applicable"
	case s.Paused && s.InProgress > 0:
		return "pausing"
	case !s.Paused && s.Queued+s.Settling+s.InProgress > 0:
		return "running"
	case s.Paused && s.Queued+s.Settling > 0:
		return "paused"
	case s.Needed > 0:
		return "needed"
	case s.Blocked > 0:
		return "blocked"
	case !s.Checked:
		return "checking"
	case s.Done > 0:
		return "done"
	default:
		return "not_needed"
	}
}

// recordedAtMigrationStatus reads the counts — one GROUP BY over the backfill
// table, plus the blocked list — and, while a decision or a run is in front
// of the administrator, what Nextcloud says about the consequences.
func (rt *Runtime) recordedAtMigrationStatus(ctx context.Context) recordedAtMigrationStatus {
	w := rt.recordedAtFiles.Load()
	if w == nil {
		return recordedAtMigrationStatus{State: "not_applicable"}
	}
	return w.status(ctx, true)
}

func (w *recordedAtFileWorker) status(ctx context.Context, withNotices bool) recordedAtMigrationStatus {
	s := recordedAtMigrationStatus{
		Applicable: true, Checked: w.rt.recordedAtIndexChecked.Load(),
		Workers: recordedAtFileWorkers, PaceSeconds: int(recordedAtFilePace / time.Second),
		InProgress: int(w.active.Load()),
	}
	store := w.rt.meetingMetadata
	counts, err := store.recordedAtFileCounts(ctx)
	if err == nil {
		s.Paused, err = store.recordedAtFilesPaused(ctx)
	}
	if err == nil {
		s.BlockedItems, err = store.recordedAtBlockedFiles(ctx, recordedAtBlockedListed)
	}
	if err != nil {
		s.State, s.Error = "unknown", "Cassini could not read the migration's progress: "+err.Error()
		return s
	}
	s.Needed, s.Done, s.Blocked = counts[recordedAtStateNeeded], counts[recordedAtStateDone], counts[recordedAtStateBlocked]
	s.Queued, s.Settling = counts[recordedAtStatePending], counts[recordedAtStateUploading]
	// The files being rewritten are still pending or uploading in the table;
	// counted once, as in progress.
	for left := s.InProgress; left > 0 && s.Queued+s.Settling > 0; left-- {
		if s.Queued > 0 {
			s.Queued--
		} else {
			s.Settling--
		}
	}
	s.State = recordedAtMigrationStateFor(s)
	switch s.State {
	case "needed", "running", "pausing", "paused", "blocked":
		if withNotices {
			s.Notices = w.notifications(ctx)
		}
	}
	return s
}

func (s *meetingMetadataStore) recordedAtFilesPaused(ctx context.Context) (bool, error) {
	var paused int
	err := s.db.QueryRowContext(ctx, `SELECT paused FROM recorded_at_file_backfill_control WHERE id=1`).Scan(&paused)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return paused != 0, err
}

func setRecordedAtFilesPausedTx(ctx context.Context, tx *sql.Tx, paused bool) error {
	value := 0
	if paused {
		value = 1
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO recorded_at_file_backfill_control(id, paused, updated_at) VALUES(1, ?, ?) ON CONFLICT(id) DO UPDATE SET paused=excluded.paused, updated_at=excluded.updated_at`, value, nowUTCString())
	return err
}

func (s *meetingMetadataStore) recordedAtBlockedFiles(ctx context.Context, limit int) ([]recordedAtBlockedFile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT b.opus_name, b.last_error,
 COALESCE((SELECT CASE WHEN json_valid(m.entry_json) THEN json_extract(m.entry_json, '$.title') END FROM meeting_metadata m WHERE m.opus_name=b.opus_name LIMIT 1), ''),
 COALESCE((SELECT m.date_label FROM meeting_metadata m WHERE m.opus_name=b.opus_name LIMIT 1), '')
 FROM recorded_at_file_backfill b WHERE b.state='blocked' ORDER BY b.opus_name LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []recordedAtBlockedFile
	for rows.Next() {
		var item recordedAtBlockedFile
		var title any
		if err := rows.Scan(&item.Meeting, &item.Reason, &title, &item.DateLabel); err != nil {
			return nil, err
		}
		if text, ok := title.(string); ok {
			item.Title = text
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// startMigration asks for every needed file, and retries every blocked one
// (a person may have fixed what blocked it), then lifts any pause. It
// answers how many files became pending.
func (w *recordedAtFileWorker) startMigration(ctx context.Context) (int64, error) {
	tx, err := w.rt.meetingMetadata.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE recorded_at_file_backfill SET state='pending', attempts=0, retry_at=0, last_error='', updated_at=? WHERE state IN ('needed','blocked')`, nowUTCString())
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := setRecordedAtFilesPausedTx(ctx, tx, false); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	for i := 0; i < recordedAtFileWorkers; i++ {
		w.poke()
	}
	return n, nil
}

// pauseMigration stops new files from starting. It does not wait: the files
// in progress finish on their own, and /status says how many are left.
func (w *recordedAtFileWorker) pauseMigration(ctx context.Context) error {
	tx, err := w.rt.meetingMetadata.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setRecordedAtFilesPausedTx(ctx, tx, true); err != nil {
		return err
	}
	return tx.Commit()
}

// recordedAtMigrationHandler serves the two ADMIN actions.
func (rt *Runtime) recordedAtMigrationHandler(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, recordedAtMigrationPath+"/"), "/")
	if action != "start" && action != "pause" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}
	worker := rt.recordedAtFiles.Load()
	if worker == nil {
		writeJSONError(w, http.StatusConflict, "this deployment has no delivered files Cassini can rewrite")
		return
	}
	who := appapi.UserID(r.Context())
	if who == "" {
		who = "an administrator"
	}
	switch action {
	case "start":
		n, err := worker.startMigration(r.Context())
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "could not start the migration: "+err.Error())
			return
		}
		worker.logf("recorded-at: file migration started by %s; %d file(s) queued", who, n)
	case "pause":
		if err := worker.pauseMigration(r.Context()); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "could not pause the migration: "+err.Error())
			return
		}
		worker.logf("recorded-at: file migration paused by %s; %d file(s) in progress finish first", who, worker.active.Load())
	}
	writeJSON(w, http.StatusOK, worker.status(r.Context(), true))
}

// nextcloudChangeCapabilities is what Nextcloud's capabilities document says
// about the apps a file rewrite reaches. A capability provider only exists
// while its app is enabled, so an absent block is a disabled app.
type nextcloudChangeCapabilities struct {
	Version          string
	ActivityApp      bool
	NotificationsApp bool
	WebPush          bool
	Versioning       bool
}

// capabilitiesAs reads /ocs/v2.php/cloud/capabilities as a user, and returns
// its capabilities object (nil when the document has none). As a USER, not as the app: some providers (Talk's)
// need a user context to report anything.
func (c ExAppConfig) capabilitiesAs(ctx context.Context, client *http.Client, user string) (version string, capabilities json.RawMessage, err error) {
	status, body, err := c.apiGetAs(ctx, client, user, strings.TrimRight(c.NextcloudURL, "/")+"/ocs/v2.php/cloud/capabilities")
	if err != nil {
		return "", nil, err
	}
	if status < 200 || status >= 300 {
		return "", nil, fmt.Errorf("capabilities -> %d", status)
	}
	var payload struct {
		OCS struct {
			Data struct {
				Version struct {
					String string `json:"string"`
				} `json:"version"`
				Capabilities json.RawMessage `json:"capabilities"`
			} `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", nil, fmt.Errorf("decode capabilities: %w", err)
	}
	return payload.OCS.Data.Version.String, payload.OCS.Data.Capabilities, nil
}

func (c ExAppConfig) changeCapabilities(ctx context.Context, client *http.Client) (nextcloudChangeCapabilities, error) {
	version, raw, err := c.capabilitiesAs(ctx, client, c.provisioningUser())
	if err != nil {
		return nextcloudChangeCapabilities{}, err
	}
	if len(raw) == 0 {
		// Nothing to read absence from: not "every app is disabled".
		return nextcloudChangeCapabilities{}, errors.New("capabilities document has no capabilities")
	}
	return parseChangeCapabilities(version, raw)
}

func parseChangeCapabilities(version string, raw json.RawMessage) (nextcloudChangeCapabilities, error) {
	var caps struct {
		Activity      *json.RawMessage `json:"activity"`
		Notifications *struct {
			Push []string `json:"push"`
		} `json:"notifications"`
		Files *struct {
			Versioning bool `json:"versioning"`
		} `json:"files"`
	}
	if err := json.Unmarshal(raw, &caps); err != nil {
		return nextcloudChangeCapabilities{}, fmt.Errorf("decode capabilities: %w", err)
	}
	out := nextcloudChangeCapabilities{Version: version, ActivityApp: caps.Activity != nil}
	if caps.Notifications != nil {
		out.NotificationsApp = true
		out.WebPush = slices.Contains(caps.Notifications.Push, "webpush")
	}
	if caps.Files != nil {
		out.Versioning = caps.Files.Versioning
	}
	return out, nil
}

// recordedAtNotificationsDoc is what the Doctor tells an administrator about
// the side effects of rewriting, with every fact it could establish.
type recordedAtNotificationsDoc struct {
	// Known is false when the capabilities could not be read; the booleans
	// are then absent and Details says what Nextcloud does by default.
	Known            bool   `json:"known"`
	Error            string `json:"error,omitempty"`
	Account          string `json:"account"`
	NextcloudVersion string `json:"nextcloud_version,omitempty"`
	ActivityApp      *bool  `json:"activity_app,omitempty"`
	NotificationsApp *bool  `json:"notifications_app,omitempty"`
	WebPush          *bool  `json:"web_push,omitempty"`
	Versioning       *bool  `json:"versioning,omitempty"`
	// Details are the sentences to show, in order.
	Details []string `json:"details"`
}

const (
	recordedAtNoticesTTL      = 5 * time.Minute
	recordedAtNoticesErrorTTL = 30 * time.Second
	recordedAtNoticesTimeout  = 5 * time.Second
)

type capabilitiesCache struct {
	mu  sync.Mutex
	at  time.Time
	doc *recordedAtNotificationsDoc
}

// notifications reads (or reuses, for a few minutes) the capabilities and
// words their consequences.
func (w *recordedAtFileWorker) notifications(ctx context.Context) *recordedAtNotificationsDoc {
	w.notices.mu.Lock()
	defer w.notices.mu.Unlock()
	now := w.now()
	if doc := w.notices.doc; doc != nil {
		ttl := recordedAtNoticesTTL
		if !doc.Known {
			ttl = recordedAtNoticesErrorTTL
		}
		if now.Sub(w.notices.at) < ttl {
			copied := *doc
			return &copied
		}
	}
	ctx, cancel := context.WithTimeout(ctx, recordedAtNoticesTimeout)
	defer cancel()
	var doc *recordedAtNotificationsDoc
	if w.capabilities == nil {
		doc = recordedAtNotices(nextcloudChangeCapabilities{}, errors.New("no Nextcloud to ask"))
	} else {
		caps, err := w.capabilities(ctx)
		if err != nil {
			w.logf("recorded-at: read Nextcloud capabilities: %v", err)
		}
		doc = recordedAtNotices(caps, err)
	}
	w.notices.doc, w.notices.at = doc, now
	copied := *doc
	return &copied
}

// recordedAtNotices words what a rewrite does in Nextcloud, from what the
// capabilities say. The defaults it quotes are Nextcloud's own, read from its
// source: the Activity app records a change to a shared file for the owner
// and for every account the file is shared with (FilesHooks::fileUpdate,
// subject "changed_by" for the others); the "A file or folder has been
// changed" setting (apps/files FileChanged) is always shown in the stream and
// defaults to off for email and for notifications; an administrator can
// change those defaults (Administration settings › Activity) and each person
// their own, neither of which an app can read. files_versions keeps the
// previous content as a version of the writer's file, expired by
// versions_retention_obligation in config.php, which is not exposed either.
func recordedAtNotices(caps nextcloudChangeCapabilities, err error) *recordedAtNotificationsDoc {
	doc := &recordedAtNotificationsDoc{Account: ncRecordingsOwner}
	defaults := "With Nextcloud's defaults the change is not emailed and not sent as a notification: both are off for “A file or folder has been changed”. An administrator can change those defaults under Administration settings › Activity, and each person can turn them on in their own Activity settings. Cassini cannot read either, so anyone who did gets an email or a notification for each recording."
	if err != nil {
		doc.Error = "Cassini could not read Nextcloud's capabilities, so it cannot tell whether the Activity and Notifications apps or file versioning are enabled."
		doc.Details = []string{
			"Each recording is saved again by Cassini's " + ncRecordingsOwner + " account, over the same file: its shares, links and file id stay as they are, and the audio is unchanged.",
			doc.Error,
			"Where file versioning is enabled (Nextcloud's default), the file as it was before is kept as an earlier version. Where the Activity app is enabled (also the default), everyone the recording is shared with gets a “file changed” entry in their Activity stream, one per recording.",
			defaults,
		}
		return doc
	}
	doc.Known = true
	doc.NextcloudVersion = caps.Version
	doc.ActivityApp, doc.NotificationsApp, doc.Versioning = &caps.ActivityApp, &caps.NotificationsApp, &caps.Versioning
	if caps.NotificationsApp {
		doc.WebPush = &caps.WebPush
	}
	doc.Details = []string{
		"Each recording is saved again by Cassini's " + ncRecordingsOwner + " account, over the same file: its shares, links and file id stay as they are, and the audio is unchanged.",
	}
	if caps.Versioning {
		doc.Details = append(doc.Details, "File versioning is enabled, so the file as it was before is kept as an earlier version in the "+ncRecordingsOwner+" account and counts toward its storage until Nextcloud expires it. How long versions are kept is set by versions_retention_obligation in Nextcloud's config.php, which Cassini cannot read.")
	} else {
		doc.Details = append(doc.Details, "File versioning is not enabled, so Nextcloud keeps no copy of the file as it was before.")
	}
	if caps.ActivityApp {
		doc.Details = append(doc.Details, "The Activity app is enabled, so everyone the recording is shared with gets a “file changed by "+ncRecordingsOwner+"” entry in their Activity stream, one per recording.", defaults)
	} else {
		doc.Details = append(doc.Details, "The Activity app is not enabled, so the change appears in nobody's activity stream and no activity email is sent.")
	}
	switch {
	case !caps.NotificationsApp:
		doc.Details = append(doc.Details, "The Notifications app is not enabled, so no notification can be sent for the change.")
	case caps.ActivityApp:
		line := "The Notifications app is enabled, so a person who turned on notifications for changed files gets one per recording in Nextcloud, and on their devices where push notifications are set up."
		if caps.WebPush {
			line += " Browser push is enabled as well."
		}
		doc.Details = append(doc.Details, line)
	}
	return doc
}
