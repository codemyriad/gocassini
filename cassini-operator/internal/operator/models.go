package operator

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type modelInfo struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	Revision       string `json:"revision"`
	DownloadBytes  int64  `json:"download_bytes"`
	InstalledBytes int64  `json:"installed_bytes"`
	Installed      bool   `json:"installed"`
	Ready          bool   `json:"ready"`
	Device         string `json:"device"`
}
type modelProgress struct {
	Version   int    `json:"version"`
	Phase     string `json:"phase"`
	File      string `json:"file,omitempty"`
	Completed int64  `json:"completed_bytes"`
	Total     int64  `json:"total_bytes"`
	Reused    int64  `json:"reused_bytes"`
}
type modelJob struct {
	ID        string        `json:"id"`
	Model     string        `json:"model"`
	Revision  string        `json:"revision"`
	Device    string        `json:"device"`
	State     string        `json:"state"`
	Progress  modelProgress `json:"progress"`
	Error     string        `json:"error,omitempty"`
	CreatedAt string        `json:"created_at"`
	UpdatedAt string        `json:"updated_at"`
}

func (rt *Runtime) modelEnv() []string {
	env := setEnvKey(os.Environ(), envCacheRoot, rt.cfg.ModelCacheRoot)
	value := "0"
	if rt.cfg.DisallowModelDownload {
		value = "1"
	}
	return setEnvKey(env, envDisallowModelDownload, value)
}
func (rt *Runtime) modelInventory(ctx context.Context, device string) ([]modelInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, rt.cfg.CassiniBin, "models", "list", "--json", "--cache-root", rt.cfg.ModelCacheRoot, "--device", device)
	cmd.Env = rt.modelEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("model inventory: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var models []modelInfo
	if err := json.Unmarshal(out, &models); err != nil {
		return nil, fmt.Errorf("decode model inventory: %w", err)
	}
	return models, nil
}
func findModel(models []modelInfo, id, revision string) (modelInfo, error) {
	for _, m := range models {
		if m.ID == id && (revision == "" || m.Revision == revision) {
			return m, nil
		}
	}
	return modelInfo{}, fmt.Errorf("unsupported model/revision %s/%s", id, revision)
}
func (rt *Runtime) modelJobs(ctx context.Context) ([]modelJob, error) {
	rows, err := rt.store.db.QueryContext(ctx, `SELECT id,model,revision,device,state,progress,error,created_at,updated_at FROM model_install_jobs ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []modelJob{}
	for rows.Next() {
		var j modelJob
		var progress string
		if err := rows.Scan(&j.ID, &j.Model, &j.Revision, &j.Device, &j.State, &progress, &j.Error, &j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(progress), &j.Progress); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}
func (rt *Runtime) enqueueModel(ctx context.Context, m modelInfo, device string) (modelJob, error) {
	id := fmt.Sprintf("%x", sha256.Sum256([]byte(m.ID+"/"+m.Revision+"/"+device)))[:24]
	now := nowUTCString()
	_, err := rt.store.db.ExecContext(ctx, `INSERT INTO model_install_jobs(id,model,revision,device,state,created_at,updated_at) VALUES(?,?,?,?,'queued',?,?) ON CONFLICT(id) DO NOTHING`, id, m.ID, m.Revision, device, now, now)
	if err != nil {
		return modelJob{}, err
	}
	jobs, err := rt.modelJobs(ctx)
	if err != nil {
		return modelJob{}, err
	}
	for _, j := range jobs {
		if j.ID == id {
			return j, nil
		}
	}
	return modelJob{}, sql.ErrNoRows
}
func (rt *Runtime) updateModelJob(j modelJob) error {
	p, _ := json.Marshal(j.Progress)
	_, err := rt.store.db.ExecContext(context.Background(), `UPDATE model_install_jobs SET state=?,progress=?,error=?,updated_at=? WHERE id=? AND state!='cancelled'`, j.State, string(p), j.Error, nowUTCString(), j.ID)
	return err
}
func (rt *Runtime) modelsHandler(w http.ResponseWriter, r *http.Request) {
	// The HTTP router has already stripped the deployment base path.
	path := strings.TrimPrefix(r.URL.Path, "/settings/models")
	if path == "" && r.Method == http.MethodGet {
		device := r.URL.Query().Get("device")
		if device == "" {
			device, _ = effectiveDevice(rt.currentSettings().DeviceOverride)
		}
		if device != "cpu" && device != "cuda" {
			writeJSONError(w, 400, "device must be cpu or cuda")
			return
		}
		models, err := rt.modelInventory(r.Context(), device)
		if err != nil {
			writeJSONError(w, 503, err.Error())
			return
		}
		jobs, err := rt.modelJobs(r.Context())
		if err != nil {
			writeJSONError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"models": models, "jobs": jobs, "downloads_allowed": !rt.cfg.DisallowModelDownload, "device": device})
		return
	}
	if path == "/install" && r.Method == http.MethodPost {
		var in struct {
			Model    string `json:"model"`
			Revision string `json:"revision"`
			Device   string `json:"device"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
			writeJSONError(w, 400, "invalid installation request")
			return
		}
		if in.Device != "cpu" && in.Device != "cuda" {
			writeJSONError(w, 400, "device must be cpu or cuda")
			return
		}
		models, err := rt.modelInventory(r.Context(), in.Device)
		if err != nil {
			writeJSONError(w, 503, err.Error())
			return
		}
		m, err := findModel(models, in.Model, in.Revision)
		if err != nil {
			writeJSONError(w, 400, err.Error())
			return
		}
		if in.Device == "cuda" && in.Model != "parakeet-tdt-0.6b-v3" {
			writeJSONError(w, 400, "CUDA requires the fp32 model")
			return
		}
		if rt.cfg.DisallowModelDownload && !m.Installed {
			writeJSONError(w, 403, "Downloads are disabled. Use cassini models import with local files.")
			return
		}
		j, err := rt.enqueueModel(r.Context(), m, in.Device)
		if err != nil {
			writeJSONError(w, 500, err.Error())
			return
		}
		w.Header().Set("Location", strings.TrimRight(rt.cfg.BasePath, "/")+"/settings/models/jobs/"+j.ID)
		writeJSON(w, http.StatusAccepted, j)
		return
	}
	if strings.HasPrefix(path, "/jobs/") {
		parts := strings.Split(strings.TrimPrefix(path, "/jobs/"), "/")
		jobs, err := rt.modelJobs(r.Context())
		if err != nil {
			writeJSONError(w, 500, err.Error())
			return
		}
		var job *modelJob
		for i := range jobs {
			if jobs[i].ID == parts[0] {
				job = &jobs[i]
				break
			}
		}
		if job == nil {
			http.NotFound(w, r)
			return
		}
		if len(parts) == 1 && r.Method == http.MethodGet {
			writeJSON(w, 200, job)
			return
		}
		if len(parts) == 2 && r.Method == http.MethodPost {
			rt.modelMu.Lock()
			defer rt.modelMu.Unlock()
			switch parts[1] {
			case "cancel":
				if job.State == "ready" || job.State == "failed" || job.State == "cancelled" {
					writeJSON(w, 200, job)
					return
				}
				_, err = rt.store.db.ExecContext(r.Context(), `UPDATE model_install_jobs SET state='cancelled',updated_at=? WHERE id=?`, nowUTCString(), job.ID)
				if rt.modelCancel != nil && rt.modelJobID == job.ID {
					rt.modelCancel()
				}
			case "retry":
				if job.State != "failed" && job.State != "cancelled" && job.State != "ready" {
					writeJSON(w, 200, job)
					return
				}
				// Do not let a cancelled worker overwrite a retried job's state.
				if rt.modelJobID == job.ID {
					writeJSONError(w, 409, "Cancellation is still finishing; retry in a moment.")
					return
				}
				if rt.cfg.DisallowModelDownload {
					models, e := rt.modelInventory(r.Context(), job.Device)
					if e != nil {
						writeJSONError(w, 503, e.Error())
						return
					}
					m, e := findModel(models, job.Model, job.Revision)
					if e != nil || !m.Installed {
						writeJSONError(w, 403, "Downloads are disabled; import local files first.")
						return
					}
				}
				_, err = rt.store.db.ExecContext(r.Context(), `UPDATE model_install_jobs SET state='queued',error='',updated_at=? WHERE id=?`, nowUTCString(), job.ID)
			default:
				http.NotFound(w, r)
				return
			}
			if err != nil {
				writeJSONError(w, 500, err.Error())
				return
			}
			writeJSON(w, 202, map[string]bool{"accepted": true})
			return
		}
	}
	writeMethodNotAllowed(w, "GET, POST")
}
func (rt *Runtime) startModelWorker() {
	rt.workerWG.Add(1)
	go func() {
		defer rt.workerWG.Done()
		// Durable intent survives process death; cancelled/failed jobs remain terminal.
		_, err := rt.store.db.ExecContext(rt.ctx, `UPDATE model_install_jobs SET state='queued' WHERE state NOT IN ('ready','failed','cancelled')`)
		if err != nil {
			rt.logger.Printf("model job recovery: %v", err)
			return
		}
		// App/native-runtime changes invalidate readiness, not the model bytes.
		settings := rt.currentSettings()
		if settings.TranscriptionEnabled && settings.ActiveModel != "" {
			device, e := resolveDeviceForSettings(settings)
			if e == nil {
				if models, e := rt.modelInventory(rt.ctx, device); e == nil {
					if m, e := findModel(models, settings.ActiveModel, settings.ActiveRevision); e == nil && m.Installed && !m.Ready {
						j, e := rt.enqueueModel(rt.ctx, m, device)
						if e == nil && j.State == "ready" {
							_, _ = rt.store.db.ExecContext(rt.ctx, `UPDATE model_install_jobs SET state='queued' WHERE id=? AND state='ready'`, j.ID)
						}
					}
				}
			}
		}
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			if rt.ctx.Err() != nil {
				return
			}
			jobs, err := rt.modelJobs(rt.ctx)
			if err == nil {
				for _, j := range jobs {
					if j.State == "queued" {
						rt.runModelJob(j)
						break
					}
				}
			}
			select {
			case <-rt.ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
}
func (rt *Runtime) runModelJob(job modelJob) {
	rt.modelMu.Lock()
	ctx, cancel := context.WithCancel(rt.ctx)
	// Recheck cancellation after selecting a queued snapshot.
	var state string
	err := rt.store.db.QueryRowContext(ctx, `SELECT state FROM model_install_jobs WHERE id=?`, job.ID).Scan(&state)
	if err != nil || state != "queued" {
		cancel()
		rt.modelMu.Unlock()
		return
	}
	rt.modelCancel = cancel
	rt.modelJobID = job.ID
	rt.modelMu.Unlock()
	defer func() { cancel(); rt.modelMu.Lock(); rt.modelCancel = nil; rt.modelJobID = ""; rt.modelMu.Unlock() }()
	run := func() error {
		if err := rt.modelCommand(ctx, &job, "install", true); err != nil {
			return err
		}
		// Network transfer never holds the inference gate. The expensive probe
		// shares the same admission gate and memory/VRAM policy as meeting builds.
		rt.buildExecutionMu.Lock()
		defer rt.buildExecutionMu.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		limits := resourceLimitsFromEnv()
		job.State = "checking"
		job.Progress.Phase = "checking"
		if err := rt.updateModelJob(job); err != nil {
			return err
		}
		if err := limits.waitForMemory(ctx, limits.minFreeMemForBuild(job.Device, job.Model), rt.logger.Printf); err != nil {
			return err
		}
		return rt.modelCommand(ctx, &job, "probe", false)
	}
	err = run()
	if ctx.Err() != nil {
		if rt.ctx.Err() != nil {
			job.State = "queued"
			job.Error = ""
			_ = rt.updateModelJob(job)
		}
		return
	}
	if err != nil {
		job.State = "failed"
		job.Error = err.Error()
	} else {
		job.State = "ready"
		job.Error = ""
	}
	if err := rt.updateModelJob(job); err != nil {
		rt.logger.Printf("persist model result: %v", err)
	}
}
func (rt *Runtime) modelCommand(ctx context.Context, job *modelJob, action string, noProbe bool) error {
	args := []string{"models", action, job.Model, "--revision", job.Revision, "--cache-root", rt.cfg.ModelCacheRoot, "--device", job.Device, "--progress-json"}
	if noProbe {
		args = append(args, "--no-probe")
	}
	cmd := exec.CommandContext(ctx, rt.cfg.CassiniBin, args...)
	cmd.Env = rt.modelEnv()
	if action == "probe" {
		var err error
		cmd.Env, err = resourceLimitsFromEnv().applyToEnv(cmd.Env, job.Device, job.Model)
		if err != nil {
			return err
		}
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd.Process) }
	if err := cmd.Start(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 64<<10)
	var saveErr error
	for scanner.Scan() {
		var p modelProgress
		if e := json.Unmarshal(scanner.Bytes(), &p); e != nil || p.Version != 1 {
			saveErr = errors.New("invalid model progress protocol")
			_ = killProcessGroup(cmd.Process)
			break
		}
		// Terminal readiness belongs to the job result, after successful child exit.
		job.Progress = p
		job.State = p.Phase
		if p.Phase == "ready" {
			job.State = "checking"
		}
		if e := rt.updateModelJob(*job); e != nil {
			saveErr = e
			_ = killProcessGroup(cmd.Process)
			break
		}
	}
	waitErr := cmd.Wait()
	if saveErr != nil {
		return saveErr
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if waitErr != nil {
		detail := strings.TrimSpace(stderr.String())
		if len(detail) > 2000 {
			detail = detail[len(detail)-2000:]
		}
		return fmt.Errorf("%s: %w: %s", action, waitErr, detail)
	}
	return nil
}
