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
	if err := rt.runRetentionSweep(ctx, time.Now()); err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, errRetentionSweepBusy):
			status = http.StatusConflict
		case errors.Is(err, errRetentionUnavailable):
			status = http.StatusServiceUnavailable
		}
		writeJSONError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "completed"})
}
