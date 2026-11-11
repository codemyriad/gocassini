package operator

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The file half of the recordedAtUtc backfill: each delivered .opus of a
// recorded meeting is re-tagged in place with `cassini retag
// --recorded-at-utc`, so the instant travels with the file to downloads,
// native apps and the CLI, not only to the catalog. It goes through the same
// safety pipeline as the annotation write-behind (annotations_workers.go):
// the per-recording write lock, no pending retention transition, an If-Match
// PUT over the exact bytes it read, a re-download to verify, the file id
// kept so shares survive, and the annotation index told about the new
// container digest the way a verified republish does.
//
// Nothing here runs on its own. Each rewrite is a new Nextcloud file version
// and a "file changed" entry in the activity of everyone the recording is
// shared with, so an administrator decides when it happens: the index pass
// only enumerates the files (state needed), and the Doctor's button moves
// them to pending (recorded_at_migration.go). Pausing stops new files from
// starting, and a file not yet uploaded is put back; see drain.
//
// Progress lives in meetings.sqlite3 (recorded_at_file_backfill):
//
//	needed    — found by the index pass; nobody has asked for it yet, and
//	            the workers never take it
//	pending   — requested, or failed and waiting for retry_at
//	uploading — a PUT was sent whose answer is not known; the next attempt
//	            settles it from the file itself
//	done      — the file carries the instant (or states its own)
//	blocked   — needs a person: the file is missing, or the CLI cannot
//	            set the field; last_error says which. Requeued as pending
//	            on the next operator start, or when the administrator runs
//	            the migration again, which is the "retry" the messages ask
//	            for once the person has fixed it.
//
// recorded_at_file_backfill_control holds the one pause switch.

const (
	recordedAtFileWorkers      = 2
	recordedAtFileTimeout      = 10 * time.Minute
	recordedAtFileDeferral     = 5 * time.Minute
	recordedAtFileRetryBase    = time.Minute
	recordedAtFileRetryCap     = 6 * time.Hour
	recordedAtFileScanInterval = 5 * time.Minute
	// recordedAtFilePace is the pause a worker takes between two files. Each
	// file is a download, an upload and a verifying download, and each upload
	// is a new Nextcloud file version (and a change in recipients' activity);
	// once an administrator starts it, the backfill of a whole archive runs at
	// this pace, in the background, rather than as fast as Nextcloud answers.
	recordedAtFilePace = 30 * time.Second
)

// The row states, as stored.
const (
	recordedAtStateNeeded    = "needed"
	recordedAtStatePending   = "pending"
	recordedAtStateUploading = "uploading"
	recordedAtStateDone      = "done"
	recordedAtStateBlocked   = "blocked"
)

type recordedAtFileWorker struct {
	rt      *Runtime
	exapp   ExAppConfig
	client  *http.Client
	bin     string
	logger  *log.Logger
	wake    chan struct{}
	claimed sync.Map
	// active names the files a worker is rewriting right now (inside
	// processOne), which a pause lets finish. /status counts each by the
	// state its row is in.
	active sync.Map
	start  sync.Once
	now    func() time.Time
	// pause waits between two files of one drain; false means stop.
	pause func(ctx context.Context) bool
	// afterAttempt, when set (tests only), runs between an attempt and the
	// write of its outcome, with the per-recording lock still held.
	afterAttempt func(name string)
	// beforeRetryWrite, when set (tests only), runs just before a failed
	// attempt's retry is written.
	beforeRetryWrite func(name string)
	// capabilities reads what Nextcloud says about the apps a rewrite
	// touches (recorded_at_migration.go); tests replace it.
	capabilities func(ctx context.Context) (nextcloudChangeCapabilities, error)
	notices      capabilitiesCache
}

func newRecordedAtFileWorker(rt *Runtime, exapp ExAppConfig, client *http.Client, logger *log.Logger) *recordedAtFileWorker {
	if rt == nil || rt.meetingMetadata == nil || strings.TrimSpace(rt.cfg.CassiniBin) == "" || client == nil {
		return nil
	}
	w := &recordedAtFileWorker{
		rt: rt, exapp: exapp, client: client, bin: rt.cfg.CassiniBin, logger: logger,
		wake: make(chan struct{}, recordedAtFileWorkers), now: time.Now,
		pause: func(ctx context.Context) bool { return sleepCtx(ctx, recordedAtFilePace) },
	}
	w.capabilities = func(ctx context.Context) (nextcloudChangeCapabilities, error) {
		return exapp.changeCapabilities(ctx, client)
	}
	return w
}

// sleepCtx waits d, or less if ctx ends; it answers whether it waited out d.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (w *recordedAtFileWorker) logf(format string, args ...any) {
	if w.logger != nil {
		w.logger.Printf(format, args...)
	}
}

func (rt *Runtime) wakeRecordedAtFiles() {
	if rt == nil {
		return
	}
	rt.recordedAtFiles.Load().poke()
}

func (w *recordedAtFileWorker) poke() {
	if w == nil || w.wake == nil {
		return
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// startWorkers runs a bounded pool. Each worker drains what is due, then
// waits for a wake (a backfill pass, a publish, a retry coming due) or the
// scan interval.
func (w *recordedAtFileWorker) startWorkers() {
	if w == nil || w.rt.ctx == nil {
		return
	}
	w.start.Do(func() {
		if n, err := w.requeueBlocked(w.rt.ctx); err != nil {
			w.logf("recorded-at: requeue blocked files: %v", err)
		} else if n > 0 {
			w.logf("recorded-at: retrying %d blocked file(s)", n)
		}
		for i := 0; i < recordedAtFileWorkers; i++ {
			w.rt.workerWG.Add(1)
			go w.loop()
		}
		w.poke()
	})
}

// requeueBlocked gives every blocked file another attempt. A block needs a
// person (restore the file, upgrade the CLI); a restart is when what they
// fixed can take effect.
func (w *recordedAtFileWorker) requeueBlocked(ctx context.Context) (int64, error) {
	result, err := w.rt.meetingMetadata.db.ExecContext(ctx, `UPDATE recorded_at_file_backfill SET state='pending', attempts=0, retry_at=0, updated_at=? WHERE state='blocked'`, nowUTCString())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (w *recordedAtFileWorker) loop() {
	defer w.rt.workerWG.Done()
	ticker := time.NewTicker(recordedAtFileScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.rt.ctx.Done():
			return
		case <-w.wake:
		case <-ticker.C:
		}
		if processed := w.drain(w.rt.ctx); processed > 0 {
			if counts, err := w.rt.meetingMetadata.recordedAtFileCounts(w.rt.ctx); err == nil {
				w.logf("recorded-at: file backfill progress — %s", formatStateCounts(counts))
			}
		}
	}
}

// drain processes every due file, one at a time per worker and paced
// (recordedAtFilePace); it answers how many it attempted.
//
// A pause never leaves a file half-written. It is checked between files and
// once more inside one, just before the upload: a file not yet uploaded goes
// back to pending untouched, and one whose upload was sent runs to its
// outcome (done, blocked, or due again later). Paused, a worker still settles
// a row whose upload was sent but not confirmed (state uploading): if the
// upload landed the row becomes done, and if it did not the row goes back to
// pending without a new upload — only that makes the index agree with
// Nextcloud, and no new version is written after the pause.
func (w *recordedAtFileWorker) drain(ctx context.Context) int {
	processed := 0
	for ctx.Err() == nil {
		name, err := w.next(ctx)
		if err != nil {
			w.logf("recorded-at: scan file backfill: %v", err)
			return processed
		}
		if name == "" {
			return processed
		}
		if processed > 0 && w.pause != nil && !w.pause(ctx) {
			w.claimed.Delete(name)
			return processed
		}
		// The pace above is long; a pause may have come in during it.
		if processed > 0 {
			if ok, err := w.mayStart(ctx, name); err != nil || !ok {
				w.claimed.Delete(name)
				return processed
			}
		}
		w.active.Store(name, struct{}{})
		w.processOne(ctx, name)
		w.active.Delete(name)
		w.claimed.Delete(name)
		processed++
	}
	return processed
}

// mayStart answers whether name may still be worked on now: always for a
// file whose upload awaits settling, otherwise only while not paused.
func (w *recordedAtFileWorker) mayStart(ctx context.Context, name string) (bool, error) {
	paused, err := w.rt.meetingMetadata.recordedAtFilesPaused(ctx)
	if err != nil || !paused {
		return err == nil, err
	}
	var state string
	err = w.rt.meetingMetadata.db.QueryRowContext(ctx, `SELECT state FROM recorded_at_file_backfill WHERE opus_name=?`, name).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return state == recordedAtStateUploading, err
}

func (w *recordedAtFileWorker) next(ctx context.Context) (string, error) {
	paused, err := w.rt.meetingMetadata.recordedAtFilesPaused(ctx)
	if err != nil {
		return "", err
	}
	states := `('pending','uploading')`
	if paused {
		states = `('uploading')`
	}
	rows, err := w.rt.meetingMetadata.db.QueryContext(ctx, `SELECT opus_name FROM recorded_at_file_backfill WHERE state IN `+states+` AND retry_at <= ? ORDER BY retry_at, opus_name LIMIT 100`, w.now().Unix())
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return "", err
		}
		if _, busy := w.claimed.LoadOrStore(name, true); !busy {
			return name, nil
		}
	}
	return "", rows.Err()
}

// recordedAtFileDeferred is "not now, and not a failure": a write to the same
// recording is in flight or a retention transition owns it.
type recordedAtFileDeferred struct{ reason string }

func (e *recordedAtFileDeferred) Error() string { return e.reason }

// recordedAtFileBlocked needs a person; retrying cannot help.
//
// reason is the English kept in last_error, for the log, the CLI and older
// apps; code and params are stored beside it so the app can word the reason
// in its reader's language (D-921). Every code is listed in
// cassini-app/src/operator/doctorCatalog.json (doctor_catalog_test.go).
type recordedAtFileBlocked struct {
	code   string
	reason string
	params map[string]any
}

func (e *recordedAtFileBlocked) Error() string { return e.reason }

// processOne runs one file and records the outcome. Every error leaves the
// row due again later; only a verified write (or proof none is needed)
// advances it to done.
//
// The outcome is written while the per-recording lock is still held. A
// publish takes the same lock to replace the file and requeue its row; an
// outcome written after release could land on that fresh row and, say,
// block a file this attempt never saw.
func (w *recordedAtFileWorker) processOne(ctx context.Context, name string) {
	ctx, cancel := context.WithTimeout(ctx, recordedAtFileTimeout)
	defer cancel()
	release, err := annotationWriteLocks.acquire(ctx, name)
	if err == nil {
		defer release()
		err = w.retagFile(ctx, name)
	}
	if w.afterAttempt != nil {
		w.afterAttempt(name)
	}
	store := w.rt.meetingMetadata
	persist, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), annotateIndexTimeout)
	defer persistCancel()
	var deferred *recordedAtFileDeferred
	var blocked *recordedAtFileBlocked
	switch {
	case err == nil:
		return
	case errors.As(err, &deferred):
		retry := w.now().Add(recordedAtFileDeferral)
		if _, e := store.db.ExecContext(persist, `UPDATE recorded_at_file_backfill SET retry_at=?, last_error=?, updated_at=? WHERE opus_name=?`, retry.Unix(), deferred.reason, nowUTCString(), name); e != nil {
			w.logf("recorded-at: persist deferral %s: %v", name, e)
		}
		time.AfterFunc(recordedAtFileDeferral, w.poke)
	case errors.As(err, &blocked):
		w.logf("recorded-at: %s blocked: %s", name, blocked.reason)
		params := ""
		if len(blocked.params) > 0 {
			if raw, e := json.Marshal(blocked.params); e == nil {
				params = string(raw)
			}
		}
		if _, e := store.db.ExecContext(persist, `UPDATE recorded_at_file_backfill SET state='blocked', last_error=?, last_error_code=?, last_error_params=?, updated_at=? WHERE opus_name=?`, blocked.reason, blocked.code, params, nowUTCString(), name); e != nil {
			w.logf("recorded-at: persist block %s: %v", name, e)
		}
	default:
		w.logf("recorded-at: retag %s: %v", name, err)
		var attempts int
		_ = store.db.QueryRowContext(persist, `SELECT attempts FROM recorded_at_file_backfill WHERE opus_name=?`, name).Scan(&attempts)
		jitter := 0.8 + rand.Float64()*0.4
		backoff := time.Duration(float64(min(recordedAtFileRetryCap, recordedAtFileRetryBase<<min(attempts, 10))) * jitter)
		// Paused, the migration is pausing until an upload is settled: such a
		// row is retried as soon as a first failure would, not after the
		// backoff its earlier attempts earned. The choice is made in the
		// statement itself, so a pause committing meanwhile is always seen.
		soon := time.Duration(float64(recordedAtFileRetryBase) * jitter)
		now := w.now()
		if w.beforeRetryWrite != nil {
			w.beforeRetryWrite(name)
		}
		var retryAt int64
		err := store.db.QueryRowContext(persist, `UPDATE recorded_at_file_backfill SET attempts=attempts+1,
 retry_at=CASE WHEN state='uploading' AND COALESCE((SELECT paused FROM recorded_at_file_backfill_control WHERE id=1), 0)!=0 THEN ? ELSE ? END,
 last_error=?, updated_at=? WHERE opus_name=? RETURNING retry_at`,
			now.Add(soon).Unix(), now.Add(backoff).Unix(), "delivered file could not be re-tagged yet; retrying", nowUTCString(), name).Scan(&retryAt)
		wait := soon
		if err != nil || retryAt != now.Add(soon).Unix() {
			wait = backoff
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			w.logf("recorded-at: persist retry %s: %v", name, err)
		}
		time.AfterFunc(wait, w.poke)
	}
}

func (w *recordedAtFileWorker) setState(ctx context.Context, name, state string) error {
	_, err := w.rt.meetingMetadata.db.ExecContext(ctx, `UPDATE recorded_at_file_backfill SET state=?, attempts=0, retry_at=0, last_error='', updated_at=? WHERE opus_name=?`, state, nowUTCString(), name)
	return err
}

// retagFile is one pass of the pipeline for one delivered file. The caller
// holds the recording's write lock.
func (w *recordedAtFileWorker) retagFile(ctx context.Context, name string) error {
	// Re-read under the lock: a publish may have replaced the file and its
	// row while this worker waited.
	var value, state string
	err := w.rt.meetingMetadata.db.QueryRowContext(ctx, `SELECT recorded_at_utc, state FROM recorded_at_file_backfill WHERE opus_name=?`, name).Scan(&value, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if state != "pending" && state != "uploading" {
		return nil
	}
	if _, ok := parseRecordedAtUTC(value); !ok {
		return &recordedAtFileBlocked{code: "queued_instant_invalid", reason: "queued recordedAtUtc is not a valid instant"}
	}
	if w.exapp.lifecycle != nil {
		var pending int
		if err := w.exapp.lifecycle.db.QueryRowContext(ctx, `SELECT count(*) FROM remote_retention_operation WHERE name=? AND status!='completed'`, name).Scan(&pending); err != nil {
			return err
		}
		if pending > 0 {
			return &recordedAtFileDeferred{"retention transition is awaiting recovery"}
		}
	}
	annotations := w.rt.annotationReads()
	if annotations != nil {
		if annotations.rebuildPending.Load() {
			return &recordedAtFileDeferred{"the tag index is being rebuilt"}
		}
		// A mark or rename waiting to be written owns the next version of
		// this file; re-tagging under it would race its If-Match baseline.
		var dirty int
		err := annotations.db.QueryRowContext(ctx, `SELECT (desired!=confirmed OR in_flight IS NOT NULL OR republish_json IS NOT NULL) FROM annotation_head WHERE opus_name=?`, name).Scan(&dirty)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if dirty != 0 {
			return &recordedAtFileDeferred{"an annotation write for this recording is in flight"}
		}
	}
	rel, err := w.exapp.currentOwnerMeetingPath(ctx, name)
	if errors.Is(err, errMeetingRetired) {
		// Expired or deleted: there is no file to fix, now or later.
		return w.setState(ctx, name, "done")
	}
	if err != nil {
		return err
	}
	before, err := w.exapp.davPropfindLeafState(ctx, w.client, ncRecordingsOwner, rel)
	if err != nil {
		return err
	}
	if !before.Exists {
		return &recordedAtFileBlocked{code: "recording_missing", reason: "recording is missing; restore it and retry"}
	}
	if before.ETag == "" {
		return fmt.Errorf("missing ETag")
	}
	dir, err := os.MkdirTemp(w.rt.cfg.WorkRoot, "recorded-at-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	in, out := filepath.Join(dir, "in.opus"), filepath.Join(dir, "out.opus")
	if _, _, err := w.exapp.stageRecording(ctx, w.client, ncRecordingsOwner, rel, in, maxAnnotateRecordingBytes, before.ETag); err != nil {
		return err
	}
	retag, err := runRetagRecordedAt(ctx, w.bin, in, out, value)
	if err != nil {
		var cliErr *retagCLIError
		if errors.As(err, &cliErr) && cliErr.Code == 2 {
			return &recordedAtFileBlocked{code: "cli_too_old", reason: "the cassini CLI cannot set recordedAtUtc; upgrade it and retry"}
		}
		return err
	}
	if retag.Previous != "" && normalizedRecordedAtUTC(retag.Previous) == "" {
		return &recordedAtFileBlocked{code: "file_instant_invalid", reason: "the delivered file states an invalid recordedAtUtc; repair it and retry"}
	}
	ownValue := retag.Previous != "" && normalizedRecordedAtUTC(retag.Previous) != normalizedRecordedAtUTC(value)
	if ownValue {
		// The file already states a different instant. That is the file's
		// own record: the index follows it, and the file is left alone.
		w.logf("recorded-at: %s already states recordedAtUtc %s (index had %s); keeping the file's", name, retag.Previous, value)
		if err := w.rt.adoptFileRecordedAt(ctx, name, retag.Previous); err != nil {
			return err
		}
	}
	if !retag.Changed || ownValue {
		// Our earlier PUT landed but its answer was lost (uploading), or the
		// index otherwise still names other bytes than these — the route a
		// landed upload takes when its row left uploading unsettled, say
		// blocked and requeued: settle the annotation index against the
		// bytes actually there.
		stale := state == recordedAtStateUploading
		if !stale {
			if stale, err = w.annotationIndexBehind(ctx, name, in); err != nil {
				return err
			}
		}
		if stale {
			if err := w.reconcileAnnotations(ctx, name, in); err != nil {
				return err
			}
		}
		return w.setState(ctx, name, "done")
	}
	// Paused since this file was taken (or, for an uploading row, since its
	// earlier PUT failed without landing — the file above does not carry the
	// value): nothing of it is written yet, so it goes back to pending
	// unchanged rather than becoming a new version after the pause. Should
	// that earlier PUT still land later, the pending attempt that then finds
	// the value in the file settles the index (annotationIndexBehind).
	if paused, err := w.rt.meetingMetadata.recordedAtFilesPaused(ctx); err != nil {
		return err
	} else if paused {
		return w.setState(ctx, name, recordedAtStatePending)
	}
	info, err := os.Stat(out)
	if err != nil {
		return err
	}
	if info.Size() > maxAnnotateRecordingBytes {
		return &recordedAtFileBlocked{code: "recording_too_large", reason: "recording exceeds the staging limit"}
	}
	digest, err := fileSHA256(out)
	if err != nil {
		return err
	}
	if _, err := w.rt.meetingMetadata.db.ExecContext(ctx, `UPDATE recorded_at_file_backfill SET state='uploading', updated_at=? WHERE opus_name=?`, nowUTCString(), name); err != nil {
		return err
	}
	if status, _, err := w.exapp.davPutFileIfMatch(ctx, w.client, ncRecordingsOwner, rel, out, meetingContentType(rel), before.ETag); err != nil {
		if status >= 400 && status < 500 && state == recordedAtStatePending {
			// Nextcloud answered and refused it (a 412: the file changed
			// since it was read): nothing of this upload was written, so
			// there is no upload to settle. Any other failure — no answer,
			// a 5xx — may have landed, and stays uploading. So does a row
			// this attempt found uploading: the earlier PUT whose answer
			// was lost may be what changed the file (it can land late, and
			// is the reason for this refusal), so the next attempt still
			// settles it from the file.
			if _, e := w.rt.meetingMetadata.db.ExecContext(ctx, `UPDATE recorded_at_file_backfill SET state='pending', updated_at=? WHERE opus_name=? AND state='uploading'`, nowUTCString(), name); e != nil {
				return errors.Join(err, e)
			}
		}
		return err
	}
	if err := w.exapp.verifyUploadedLeaf(ctx, w.client, rel, info.Size()); err != nil {
		return err
	}
	// OC-Checksum is supplied by the uploader, so it is not independent proof.
	if _, _, err := w.exapp.stageRecording(ctx, w.client, ncRecordingsOwner, rel, in, maxAnnotateRecordingBytes); err != nil {
		return err
	}
	actual, err := fileSHA256(in)
	if err != nil {
		return err
	}
	if actual != digest {
		return errors.New("uploaded recording could not be verified")
	}
	after, err := w.exapp.davPropfindLeafState(ctx, w.client, ncRecordingsOwner, rel)
	if err != nil {
		return err
	}
	if before.FileID > 0 && after.FileID != before.FileID {
		return &recordedAtFileBlocked{code: "file_id_changed",
			reason: fmt.Sprintf("Nextcloud changed the file id from %d to %d; existing shares must be repaired", before.FileID, after.FileID),
			params: map[string]any{"from": before.FileID, "to": after.FileID}}
	}
	if err := w.reconcileAnnotations(ctx, name, in); err != nil {
		return err
	}
	return w.setState(ctx, name, "done")
}

// annotationIndexBehind answers whether the annotation index knows name and
// names a container other than the file at delivered.
func (w *recordedAtFileWorker) annotationIndexBehind(ctx context.Context, name, delivered string) (bool, error) {
	store := w.rt.annotationReads()
	if store == nil {
		return false, nil
	}
	var state, indexed string
	err := store.db.QueryRowContext(ctx, `SELECT state, container_sha256 FROM meeting_annotations WHERE opus_name=?`, name).Scan(&state, &indexed)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// A file the index already recorded as unreadable has no digest to fall
	// behind, and `annotate show` would refuse it again: leave it to the
	// annotation backfill rather than retrying it here forever.
	if state == annotationsStateUnavailable {
		return false, nil
	}
	digest, err := fileSHA256(delivered)
	if err != nil {
		return false, err
	}
	return !strings.EqualFold(indexed, digest), nil
}

// reconcileAnnotations tells the tag index about the file as now delivered —
// a new container digest, the same audio and annotations — exactly as a
// verified republish does (recordAnnotationDelivery).
func (w *recordedAtFileWorker) reconcileAnnotations(ctx context.Context, name, delivered string) error {
	store := w.rt.annotationReads()
	if store == nil {
		return nil
	}
	var indexState string
	err := store.db.QueryRowContext(ctx, `SELECT state FROM meeting_annotations WHERE opus_name=?`, name).Scan(&indexState)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if indexState == annotationsStateUnavailable {
		return nil
	}
	shown, err := runAnnotateShow(ctx, w.bin, delivered)
	if err != nil {
		// The CLI read the file and refused its marks: record the file as
		// unreadable, as the annotation backfill and publish do, rather than
		// leaving the row to retry forever. Launch and transport errors stay
		// errors and are retried.
		if annotateExitCode(err) != 0 && ctx.Err() == nil {
			return store.MarkUnavailable(ctx, name, "")
		}
		return err
	}
	if shown.Unsupported {
		return nil
	}
	return store.recordAnnotationDelivery(ctx, name, shown)
}

// retagRecordedAtResult is what `cassini retag --json` reports for the field.
type retagRecordedAtResult struct {
	// Changed: the file now carries the value and did not before.
	Changed bool
	// Previous is the value the file carried before, "" when none.
	Previous string
}

type retagCLIError struct {
	Code    int
	Message string
}

func (e *retagCLIError) Error() string {
	return fmt.Sprintf("cassini retag exited %d: %s", e.Code, e.Message)
}

// runRetagRecordedAt writes a copy of in at out carrying value as
// meeting.recordedAtUtc (and its tag mirrors), and reports whether the field
// moved. The input is never modified.
func runRetagRecordedAt(ctx context.Context, bin, in, out, value string) (retagRecordedAtResult, error) {
	if strings.TrimSpace(bin) == "" {
		return retagRecordedAtResult{}, errors.New("no cassini binary is configured")
	}
	cmd := exec.CommandContext(ctx, bin, "retag", in, "--out", out, "--recorded-at-utc", value, "--json")
	cmd.Env = contextChildEnv(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd.Process) }
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > maxAnnotateStderr {
			msg = msg[:maxAnnotateStderr]
		}
		return retagRecordedAtResult{}, &retagCLIError{Code: exitErr.ExitCode(), Message: msg}
	}
	if err != nil {
		return retagRecordedAtResult{}, fmt.Errorf("run cassini retag: %w", err)
	}
	var summary struct {
		Changes []struct {
			Field string `json:"field"`
			From  any    `json:"from"`
			To    any    `json:"to"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &summary); err != nil {
		return retagRecordedAtResult{}, fmt.Errorf("read cassini retag summary: %w", err)
	}
	for _, change := range summary.Changes {
		if change.Field != recordedAtUTCField {
			continue
		}
		previous, _ := change.From.(string)
		return retagRecordedAtResult{Changed: true, Previous: previous}, nil
	}
	// Unchanged means the file already holds exactly value.
	return retagRecordedAtResult{Previous: value}, nil
}
