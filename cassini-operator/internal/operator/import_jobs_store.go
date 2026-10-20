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

type importPrincipal struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// ImportJob is one file moving through fetch -> validate -> deliver -> share ->
// index. Terminal states are done, failed and canceled.
type ImportJob struct {
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
// workbench item even though the delivered meeting is shared state.
func (s *Store) ImportJobsFor(ctx context.Context, caller string) ([]ImportJob, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+importJobColumns+` FROM import_jobs WHERE created_by=? AND cleared_at='' ORDER BY created_at DESC, id DESC`, caller)
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
		`SELECT `+importJobColumns+` FROM import_jobs WHERE state=? ORDER BY created_at, id LIMIT 1`, importStateQueued)
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
	result, err := s.db.ExecContext(ctx,
		`UPDATE import_jobs SET state=?,phase='',updated_at=? WHERE state IN (?,?,?,?,?,?)`,
		importStateQueued, nowUTCString(), importStateQueued, importStateFetching, importStateValidating, importStateDelivering, importStateSharing, importStateIndexing)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
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
	_, err := s.db.ExecContext(ctx,
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
