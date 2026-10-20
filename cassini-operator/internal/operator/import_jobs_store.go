package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Import jobs (D-828): one durable row per uploaded portable meeting file.
//
// The row is both the work queue entry and, once done, the permanent import
// record: who brought the meeting in, when, from which source file, and which
// delivered name it became. Done rows are the second retention-provenance
// source beside recording jobs (see retentionInventory), so they are never
// deleted — "clear completed" in the viewer is a display action only.

const (
	importStateUploading    = "uploading"
	importStateUploadFailed = "upload_failed"
	importStateStaged       = "staged"
	importStateQueued       = "queued"
	importStateFetching     = "fetching"
	importStateValidating   = "validating"
	importStateDelivering   = "delivering"
	importStateSharing      = "sharing"
	importStateIndexing     = "indexing"
	importStateDone         = "done"
	importStateFailed       = "failed"
	importStateCanceled     = "canceled"
)

func importStateTerminal(state string) bool {
	return state == importStateUploadFailed || state == importStateDone || state == importStateFailed || state == importStateCanceled
}

// importAudience is the sharing selection the importer made for one file. The
// importer is always the share starter and is not repeated in Audience.
type importAudience struct {
	Share    bool              `json:"share"`
	Audience []importPrincipal `json:"audience,omitempty"`
}

// importPrincipal is one recipient. Label is the display name the importer saw
// in the sharee search, kept so the row can say who "Shared with 3" means; the
// share reconciler never reads it — only type and id reach Nextcloud.
type importPrincipal struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
}

// ImportJob is one file moving through fetch -> validate -> deliver -> share ->
// index. Terminal states are done, failed and canceled.
const importCLIRecovery = "This import uses a local CLI file. Run import-meeting again with the source file, or upload it as a new import."

type ImportJob struct {
	Origin        string         `json:"origin"`
	ExpectedBytes int64          `json:"expectedBytes,omitempty"`
	StagingState  string         `json:"stagingState,omitempty"`
	ClearedAt     string         `json:"clearedAt,omitempty"`
	ID            string         `json:"id"`
	BatchID       string         `json:"batchId"`
	CreatedBy     string         `json:"createdBy"`
	SourceName    string         `json:"sourceName"`
	StagedPath    string         `json:"stagedPath"`
	Format        string         `json:"format,omitempty"`
	State         string         `json:"state"`
	Phase         string         `json:"phase,omitempty"`
	Error         string         `json:"error,omitempty"`
	Sharing       importAudience `json:"sharing"`
	// Provenance read from the file at validation.
	ContentID              string `json:"contentId,omitempty"`
	SourceRoomID           string `json:"sourceRoomId,omitempty"`
	MeetingCreatedAtUTC    string `json:"meetingCreatedAtUtc,omitempty"`
	MeetingRecordedAtLocal string `json:"meetingRecordedAtLocal,omitempty"`
	DeliveredName          string `json:"deliveredName,omitempty"`
	FileID                 int64  `json:"fileId,omitempty"`
	ImportedAt             string `json:"importedAt,omitempty"`
	CreatedAt              string `json:"createdAt"`
	UpdatedAt              string `json:"updatedAt"`
}

// The persisted local: marker already distinguishes CLI sources, including
// records written before upload tracking. Expose that origin without a second
// database field that could disagree with the source path.
func (j ImportJob) isCLI() bool { return strings.HasPrefix(j.StagedPath, "local:") }

const importJobColumns = `id,batch_id,created_by,source_name,staged_path,format,state,phase,error,audience_json,content_id,source_room_id,meeting_created_at_utc,meeting_recorded_at_local,delivered_name,file_id,imported_at,created_at,updated_at,expected_bytes,staging_state,cleared_at`

func scanImportJob(scan func(dest ...any) error) (ImportJob, error) {
	var j ImportJob
	var audience string
	err := scan(&j.ID, &j.BatchID, &j.CreatedBy, &j.SourceName, &j.StagedPath, &j.Format, &j.State, &j.Phase, &j.Error, &audience,
		&j.ContentID, &j.SourceRoomID, &j.MeetingCreatedAtUTC, &j.MeetingRecordedAtLocal,
		&j.DeliveredName, &j.FileID, &j.ImportedAt, &j.CreatedAt, &j.UpdatedAt, &j.ExpectedBytes, &j.StagingState, &j.ClearedAt)
	if err != nil {
		return ImportJob{}, err
	}
	if err := json.Unmarshal([]byte(audience), &j.Sharing); err != nil {
		return ImportJob{}, fmt.Errorf("import job %s: decode sharing: %w", j.ID, err)
	}
	j.Origin = "upload"
	if j.isCLI() {
		j.Origin = "cli"
	}
	return j, nil
}

// CreateImportJobs enqueues one batch. Every job must already carry an ID, a
// creator, a staged path and a sharing document. Prepared uploads wait for
// confirmation; legacy callers and CLI jobs are inserted queued.
func (s *Store) CreateImportJobs(ctx context.Context, jobs []ImportJob) error {
	if len(jobs) == 0 {
		return fmt.Errorf("an import batch names at least one file")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := nowUTCString()
	for _, j := range jobs {
		if j.ID == "" || j.BatchID == "" || j.CreatedBy == "" || j.StagedPath == "" {
			return fmt.Errorf("invalid import job")
		}
		state := importStateQueued
		if j.State == importStateUploading {
			state = j.State
		}
		audience, err := json.Marshal(j.Sharing)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO import_jobs(id,batch_id,created_by,source_name,staged_path,state,audience_json,created_at,updated_at,expected_bytes,staging_state)
			 VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			j.ID, j.BatchID, j.CreatedBy, j.SourceName, j.StagedPath, state, string(audience), now, now, j.ExpectedBytes, j.StagingState); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ImportJobsFor lists one caller's import jobs, newest first. The list surface
// is always "this caller's imports": like insights, an import is a per-user
// workbench item even though the delivered meeting is shared state. Rows the
// caller removed (cleared_at set) are left out.
func (s *Store) ImportJobsFor(ctx context.Context, caller string) ([]ImportJob, error) {
	return s.queryImportJobs(ctx, `WHERE created_by=? AND cleared_at='' ORDER BY created_at DESC, id DESC`, caller)
}

// importJobsIncludingCleared is ImportJobsFor without the visibility filter:
// the staging sweep reconciles the folder against every row, hidden ones
// included, because a removed row's file may still be there.
func (s *Store) importJobsIncludingCleared(ctx context.Context, caller string) ([]ImportJob, error) {
	return s.queryImportJobs(ctx, `WHERE created_by=? ORDER BY created_at DESC, id DESC`, caller)
}

func (s *Store) queryImportJobs(ctx context.Context, clause string, args ...any) ([]ImportJob, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+importJobColumns+` FROM import_jobs `+clause, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []ImportJob{}
	for rows.Next() {
		j, err := scanImportJob(rows.Scan)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// ImportCreators lists every user who ever registered an import, cleared rows
// included: the staging sweep must reach a folder even when all of its rows
// are hidden.
func (s *Store) ImportCreators(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT created_by FROM import_jobs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []string
	for rows.Next() {
		var user string
		if err := rows.Scan(&user); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

// ImportJob reads one row. ok is false when the id names nothing.
func (s *Store) ImportJob(ctx context.Context, id string) (ImportJob, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+importJobColumns+` FROM import_jobs WHERE id=?`, id)
	j, err := scanImportJob(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return ImportJob{}, false, nil
	}
	if err != nil {
		return ImportJob{}, false, err
	}
	return j, true, nil
}

// nextQueuedImport claims nothing; it reports the oldest queued row for the
// single worker to run. Claiming happens by moving the state off `queued`.
func (s *Store) nextQueuedImport(ctx context.Context) (ImportJob, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+importJobColumns+` FROM import_jobs WHERE state=? AND staged_path NOT GLOB 'local:*' ORDER BY created_at, id LIMIT 1`, importStateQueued)
	j, err := scanImportJob(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return ImportJob{}, false, nil
	}
	if err != nil {
		return ImportJob{}, false, err
	}
	return j, true, nil
}

// SetImportJobState advances a job unless it has been canceled meanwhile — the
// same guard model jobs use, so a cancel can never be overwritten by a slower
// worker update.
func (s *Store) SetImportJobState(ctx context.Context, id, state, phase, errText string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE import_jobs SET state=?,phase=?,error=?,updated_at=? WHERE id=? AND state!=?`,
		state, phase, errText, nowUTCString(), id, importStateCanceled)
	return err
}

// SetImportJobProvenance records what validation read out of the file.
func (s *Store) SetImportJobProvenance(ctx context.Context, id, format, contentID, roomID, createdAtUTC, recordedAtLocal string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE import_jobs SET format=?,content_id=?,source_room_id=?,meeting_created_at_utc=?,meeting_recorded_at_local=?,updated_at=? WHERE id=?`,
		format, contentID, roomID, createdAtUTC, recordedAtLocal, nowUTCString(), id)
	return err
}

// SetImportJobOutcome records a successful delivery and moves the job done.
func (s *Store) SetImportJobOutcome(ctx context.Context, id, deliveredName string, fileID int64, importedAt string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE import_jobs SET state=?,phase='',error='',delivered_name=?,file_id=?,imported_at=?,updated_at=? WHERE id=? AND state!=?`,
		importStateDone, deliveredName, fileID, importedAt, nowUTCString(), id, importStateCanceled)
	return err
}

// RequeueUnfinishedImports returns interrupted work to the queue at startup.
// Only import-processing states are requeued. Uploading and staged records
// must never enter the worker until staging has been confirmed.
func (s *Store) RequeueUnfinishedImports(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	// Local CLI source files cannot be fetched through the web worker. Keep
	// interrupted CLI attempts visible as failures instead of retrying DAV.
	if _, err := tx.ExecContext(ctx, `UPDATE import_jobs SET state=?,phase='',error=?,updated_at=? WHERE staged_path GLOB 'local:*' AND state IN (?,?,?,?,?,?)`,
		importStateFailed, importCLIRecovery, nowUTCString(), importStateQueued, importStateFetching, importStateValidating, importStateDelivering, importStateSharing, importStateIndexing); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE import_jobs SET state=?,phase='',updated_at=? WHERE staged_path NOT GLOB 'local:*' AND state IN (?,?,?,?,?,?)`,
		importStateQueued, nowUTCString(), importStateQueued, importStateFetching, importStateValidating, importStateDelivering, importStateSharing, importStateIndexing)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

// CancelImportJob marks a non-terminal job canceled. The worker's context is
// the service's business; this only settles the durable state.
func (s *Store) CancelImportJob(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE import_jobs SET state=?,phase='',updated_at=? WHERE id=? AND state NOT IN (?,?,?)`,
		importStateCanceled, nowUTCString(), id, importStateDone, importStateFailed, importStateCanceled)
	return err
}

// RetryImportJob queues a staged file or re-queues a failed/canceled import.
// Upload failures are deliberately excluded: they require a new upload.
func (s *Store) RetryImportJob(ctx context.Context, id string) error {
	job, _, err := s.ImportJob(ctx, id)
	if err != nil {
		return err
	}
	if job.isCLI() {
		return errors.New(importCLIRecovery)
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE import_jobs SET state=?,phase='',error='',updated_at=? WHERE id=? AND state IN (?,?,?) AND staging_state!='removed' AND cleared_at=''`,
		importStateQueued, nowUTCString(), id, importStateFailed, importStateCanceled, importStateStaged)
	return err
}

// doneImportDeliveries lists the delivered names of completed imports: the
// import half of the retention provenance gate. A name is trusted here the way
// a job id is in the jobs half — it proves this installation deliberately
// placed that file in the archive.
func (s *Store) doneImportDeliveries(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT delivered_name FROM import_jobs WHERE state=? AND delivered_name!=''`, importStateDone)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	return names, rows.Err()
}

// --- the upload lifecycle -----------------------------------------------------
//
// uploading -> staged | upload_failed, plus the staged file's removal and the
// row's explicit clearing. Kept here beside the import lifecycle so the whole
// state machine is described in one file and each guard is written once.

// HasImportJobForPath reports whether caller already registered an upload at
// staged. Staged names are minted per attempt, so a hit is a client bug and
// the registration is refused rather than silently joined.
func (s *Store) HasImportJobForPath(ctx context.Context, caller, staged string) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM import_jobs WHERE created_by=? AND staged_path=?`, caller, staged).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// TouchImportUpload renews an upload's lease. It reports false when the row is
// no longer uploading — settled by the browser, or by lease-expiry
// reconciliation — which the heartbeat answers with a 409.
func (s *Store) TouchImportUpload(ctx context.Context, id string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE import_jobs SET updated_at=? WHERE id=? AND state=?`, nowUTCString(), id, importStateUploading)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

// ConfirmImportUpload moves an uploading row to staged once its file was seen
// whole in the caller's Files. A row that left uploading meanwhile is left as
// it is.
func (s *Store) ConfirmImportUpload(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE import_jobs SET state=?,staging_state='present',error='',updated_at=? WHERE id=? AND state=?`,
		importStateStaged, nowUTCString(), id, importStateUploading)
	return err
}

// MarkImportStagingRemoved records that the staged file is gone. The row stays
// listed: a done row is retention provenance, and a failed one still carries
// the error its owner may not have read yet. Only ClearImportJob hides a row.
func (s *Store) MarkImportStagingRemoved(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE import_jobs SET staging_state='removed',updated_at=? WHERE id=?`, nowUTCString(), id)
	return err
}

// ClearImportJob hides an unsuccessful row from its owner's list — the explicit
// Remove, the only thing that ever hides one. Done rows are never cleared here;
// the viewer folds those away locally, and they remain provenance regardless.
func (s *Store) ClearImportJob(ctx context.Context, id string) error {
	now := nowUTCString()
	_, err := s.db.ExecContext(ctx, `UPDATE import_jobs SET cleared_at=?,updated_at=? WHERE id=? AND state!=? AND cleared_at=''`, now, now, id, importStateDone)
	return err
}
