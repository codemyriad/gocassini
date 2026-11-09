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
// Progress lives in meetings.sqlite3 (recorded_at_file_backfill):
//
//	pending   — queued, or failed and waiting for retry_at
//	uploading — a PUT was sent whose answer is not known; the next attempt
//	            settles it from the file itself
//	done      — the file carries the instant (or states its own)
//	blocked   — needs a person: the file is missing, or the CLI cannot
//	            set the field; last_error says which. Requeued as pending
//	            on the next operator start, which is the "retry" the
//	            messages ask for once the person has fixed it.

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
	// the backfill of a whole archive runs at this pace, in the background,
	// rather than as fast as Nextcloud answers.
	recordedAtFilePace = 30 * time.Second
)

type recordedAtFileWorker struct {
	rt      *Runtime
	exapp   ExAppConfig
	client  *http.Client
	bin     string
	logger  *log.Logger
	wake    chan struct{}
	claimed sync.Map
	start   sync.Once
	now     func() time.Time
	// pause waits between two files of one drain; false means stop.
	pause func(ctx context.Context) bool
	// afterAttempt, when set (tests only), runs between an attempt and the
	// write of its outcome, with the per-recording lock still held.
	afterAttempt func(name string)
}

func newRecordedAtFileWorker(rt *Runtime, exapp ExAppConfig, client *http.Client, logger *log.Logger) *recordedAtFileWorker {
	if rt == nil || rt.meetingMetadata == nil || strings.TrimSpace(rt.cfg.CassiniBin) == "" || client == nil {
		return nil
	}
	return &recordedAtFileWorker{
		rt: rt, exapp: exapp, client: client, bin: rt.cfg.CassiniBin, logger: logger,
		wake: make(chan struct{}, recordedAtFileWorkers), now: time.Now,
		pause: func(ctx context.Context) bool { return sleepCtx(ctx, recordedAtFilePace) },
	}
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
		w.processOne(ctx, name)
		w.claimed.Delete(name)
		processed++
	}
	return processed
}

func (w *recordedAtFileWorker) next(ctx context.Context) (string, error) {
	rows, err := w.rt.meetingMetadata.db.QueryContext(ctx, `SELECT opus_name FROM recorded_at_file_backfill WHERE state IN ('pending','uploading') AND retry_at <= ? ORDER BY retry_at, opus_name LIMIT 100`, w.now().Unix())
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
type recordedAtFileBlocked struct{ reason string }

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
		if _, e := store.db.ExecContext(persist, `UPDATE recorded_at_file_backfill SET state='blocked', last_error=?, updated_at=? WHERE opus_name=?`, blocked.reason, nowUTCString(), name); e != nil {
			w.logf("recorded-at: persist block %s: %v", name, e)
		}
	default:
		w.logf("recorded-at: retag %s: %v", name, err)
		var attempts int
		_ = store.db.QueryRowContext(persist, `SELECT attempts FROM recorded_at_file_backfill WHERE opus_name=?`, name).Scan(&attempts)
		wait := min(recordedAtFileRetryCap, recordedAtFileRetryBase<<min(attempts, 10))
		wait = time.Duration(float64(wait) * (0.8 + rand.Float64()*0.4))
		retry := w.now().Add(wait)
		if _, e := store.db.ExecContext(persist, `UPDATE recorded_at_file_backfill SET attempts=attempts+1, retry_at=?, last_error=?, updated_at=? WHERE opus_name=?`, retry.Unix(), "delivered file could not be re-tagged yet; retrying", nowUTCString(), name); e != nil {
			w.logf("recorded-at: persist retry %s: %v", name, e)
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
		return &recordedAtFileBlocked{"queued recordedAtUtc is not a valid instant"}
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
		return &recordedAtFileBlocked{"recording is missing; restore it and retry"}
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
			return &recordedAtFileBlocked{"the cassini CLI cannot set recordedAtUtc; upgrade it and retry"}
		}
		return err
	}
	if retag.Previous != "" && normalizedRecordedAtUTC(retag.Previous) == "" {
		return &recordedAtFileBlocked{"the delivered file states an invalid recordedAtUtc; repair it and retry"}
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
		if state == "uploading" {
			// Our earlier PUT landed but its answer was lost: settle the
			// annotation index against the bytes actually there.
			if err := w.reconcileAnnotations(ctx, name, in); err != nil {
				return err
			}
		}
		return w.setState(ctx, name, "done")
	}
	info, err := os.Stat(out)
	if err != nil {
		return err
	}
	if info.Size() > maxAnnotateRecordingBytes {
		return &recordedAtFileBlocked{"recording exceeds the staging limit"}
	}
	digest, err := fileSHA256(out)
	if err != nil {
		return err
	}
	if _, err := w.rt.meetingMetadata.db.ExecContext(ctx, `UPDATE recorded_at_file_backfill SET state='uploading', updated_at=? WHERE opus_name=?`, nowUTCString(), name); err != nil {
		return err
	}
	if _, _, err := w.exapp.davPutFileIfMatch(ctx, w.client, ncRecordingsOwner, rel, out, meetingContentType(rel), before.ETag); err != nil {
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
		return &recordedAtFileBlocked{fmt.Sprintf("Nextcloud changed the file id from %d to %d; existing shares must be repaired", before.FileID, after.FileID)}
	}
	if err := w.reconcileAnnotations(ctx, name, in); err != nil {
		return err
	}
	return w.setState(ctx, name, "done")
}

// reconcileAnnotations tells the tag index about the file as now delivered —
// a new container digest, the same audio and annotations — exactly as a
// verified republish does (recordAnnotationDelivery).
func (w *recordedAtFileWorker) reconcileAnnotations(ctx context.Context, name, delivered string) error {
	store := w.rt.annotationReads()
	if store == nil {
		return nil
	}
	shown, err := runAnnotateShow(ctx, w.bin, delivered)
	if err != nil {
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
