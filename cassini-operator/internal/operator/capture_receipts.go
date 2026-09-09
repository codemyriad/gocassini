package operator

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func (s *Store) captureReceiptKnown(ctx context.Context, id string) (bool, error) {
	var known bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM capture_receipts WHERE id = ?)`, id).Scan(&known)
	return known, err
}

// The receipt and the rebuild generation commit together. An interrupted or
// concurrent replay either creates both or changes neither.
func (s *Store) noteCaptureReceipt(ctx context.Context, jobID, id, at string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO capture_receipts(id,job_id) VALUES (?,?) ON CONFLICT(id) DO NOTHING`, id, jobID)
	if err != nil {
		return err
	}
	added, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if added != 0 {
		result, err = tx.ExecContext(ctx, `UPDATE jobs SET source_audio_upload_seq = source_audio_upload_seq + 1, source_audio_upload_at = ?, updated_at = ? WHERE id = ?`, at, at, jobID)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrNoSuchJob
		}
	}
	return tx.Commit()
}

// A promoted manifest is the durable notification intent. Only committed
// room/owner/capture directories are considered; staging and symlinks cannot
// become arrivals. This also repairs promotion followed by a process crash.
func (rt *Runtime) reconcileCaptureReceipts() {
	if !sourceCaptureEnabled() || rt.cfg.CaptureRoot == "" {
		return
	}
	err := filepath.WalkDir(rt.cfg.CaptureRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if rt.ctx.Err() != nil {
			return rt.ctx.Err()
		}
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(rt.cfg.CaptureRoot, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if strings.HasPrefix(entry.Name(), captureStagingPrefix) || strings.HasSuffix(entry.Name(), captureSupersededSuffix) || strings.Count(rel, string(filepath.Separator)) > 2 {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.Name() != captureSidecarName || strings.Count(rel, string(filepath.Separator)) != 3 {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var sidecar captureSidecar
		if json.Unmarshal(raw, &sidecar) != nil || sidecar.ReceiptID == "" || sidecar.OwnerUserID == "" || sidecar.Format != captureSourceFormat {
			return nil
		}
		rt.noteCaptureArrival(&sidecar, sidecar.OwnerUserID, rt.logger)
		return nil
	})
	if err != nil && !os.IsNotExist(err) && rt.ctx.Err() == nil {
		rt.logger.Printf("capture receipt recovery: %v", err)
	}
}
