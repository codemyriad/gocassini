package operator

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Runs synchronously so callers can inspect the result and resulting artifacts.
// This uses the same admission gate and policy evaluator as scheduled sweeps.
func (rt *Runtime) retentionSweepHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	if rt.ctx != nil {
		stop := context.AfterFunc(rt.ctx, cancel)
		defer stop()
	}
	started := time.Now().UTC()
	if err := rt.runRetentionSweep(ctx, started); err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, errRetentionSweepBusy):
			status = http.StatusConflict
		case errors.Is(err, errRetentionUnavailable):
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, map[string]any{"error": err.Error(), "nextcloud": rt.remoteSweepCounts(ctx, started)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "completed", "nextcloud": rt.remoteSweepCounts(ctx, started)})
}

func (rt *Runtime) remoteSweepCounts(ctx context.Context, since time.Time) map[string]any {
	result := map[string]any{"conversions": 0, "retirements": 0, "incomplete": 0, "historyNotice": nextcloudHistoryNotice}
	if rt.store == nil {
		return result
	}
	var conversions, retirements, incomplete int
	err := rt.store.db.QueryRowContext(ctx, `SELECT
 COALESCE(SUM(CASE WHEN status='completed' AND json_extract(operation_json,'$.action')='convert' AND julianday(updated_at)>=julianday(?) THEN 1 ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN status='completed' AND json_extract(operation_json,'$.action')='retire' AND julianday(updated_at)>=julianday(?) THEN 1 ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN status!='completed' THEN 1 ELSE 0 END),0) FROM remote_retention_operation`, since.Format(time.RFC3339Nano), since.Format(time.RFC3339Nano)).Scan(&conversions, &retirements, &incomplete)
	if err != nil {
		result["status"] = "unavailable"
		return result
	}
	result["conversions"], result["retirements"], result["incomplete"] = conversions, retirements, incomplete
	return result
}
