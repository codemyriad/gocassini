package operator

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Attempt logs (D-816): GET /jobs/{id}/attempts/{n}/logs/{stage} serves one
// stage's log, so an administrator can read why a run failed from the
// Operator instead of from inside the container.
//
// The logs carry no transcript or summary text: they hold progress lines,
// participant display names with word counts, the room token and local paths,
// which the Operator's audience (administrators) already sees elsewhere.
//
// The path served is the one the operator recorded on the attempt, never one
// built from the request, and it must sit under the work root.

var attemptLogStages = map[string]bool{"record": true, "build": true, "seal": true, "publish": true}

// maxAttemptLogView bounds what the viewer gets: the end of the log, where a
// failure is. ?download=1 serves the whole file.
const maxAttemptLogView = 512 << 10

// parseAttemptLogPath matches /jobs/{id}/attempts/{n}/logs/{stage}.
func parseAttemptLogPath(path string) (id string, attempt int, stage string, ok bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/jobs/"), "/")
	if len(parts) != 5 || parts[0] == "" || parts[1] != "attempts" || parts[3] != "logs" || !attemptLogStages[parts[4]] {
		return "", 0, "", false
	}
	n, err := strconv.Atoi(parts[2])
	if err != nil || n < 1 {
		return "", 0, "", false
	}
	return parts[0], n, parts[4], true
}

func attemptLogPathFor(a JobAttempt, stage string) *string {
	switch stage {
	case "record":
		return a.RecordLogPath
	case "build":
		return a.BuildLogPath
	case "seal":
		return a.SealLogPath
	case "publish":
		return a.PublishLogPath
	}
	return nil
}

func (rt *Runtime) handleAttemptLog(w http.ResponseWriter, r *http.Request, id string, attemptNumber int, stage string) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	attempt, err := rt.store.GetJobAttempt(r.Context(), id, attemptNumber)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSONError(w, http.StatusNotFound, "attempt not found")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("get attempt: %v", err))
		return
	}
	recorded := attemptLogPathFor(attempt, stage)
	if recorded == nil || *recorded == "" {
		writeJSONError(w, http.StatusNotFound, "this step wrote no log")
		return
	}
	logPath := filepath.Clean(*recorded)
	root := filepath.Clean(rt.cfg.WorkRoot) + string(filepath.Separator)
	if !strings.HasPrefix(logPath, root) {
		rt.logger.Printf("attempt log outside the work root id=%s attempt=%d stage=%s path=%s", id, attemptNumber, stage, logPath)
		writeJSONError(w, http.StatusNotFound, "this step wrote no log")
		return
	}
	file, err := os.Open(logPath)
	if errors.Is(err, os.ErrNotExist) {
		writeJSONError(w, http.StatusGone, "this log is no longer on disk: retention or a cleanup removed it")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("open log: %v", err))
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("stat log: %v", err))
		return
	}

	name := fmt.Sprintf("%s-attempt-%d-%s.log", id, attemptNumber, stage)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
		http.ServeContent(w, r, name, info.ModTime(), file)
		return
	}
	body, truncated, err := readLogTail(file, info.Size(), maxAttemptLogView)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("read log: %v", err))
		return
	}
	w.Header().Set("X-Cassini-Log-Size", strconv.FormatInt(info.Size(), 10))
	if truncated {
		w.Header().Set("X-Cassini-Log-Truncated", "1")
	}
	_, _ = w.Write(body)
}

// readLogTail returns the last max bytes of a log, starting at a line, and
// whether anything before them was left out.
func readLogTail(file io.ReadSeeker, size int64, max int64) ([]byte, bool, error) {
	if size <= max {
		body, err := io.ReadAll(file)
		return body, false, err
	}
	if _, err := file.Seek(size-max, io.SeekStart); err != nil {
		return nil, false, err
	}
	body, err := io.ReadAll(io.LimitReader(file, max))
	if err != nil {
		return nil, false, err
	}
	if i := bytes.IndexByte(body, '\n'); i >= 0 && i+1 < len(body) {
		body = body[i+1:]
	}
	return body, true, nil
}
