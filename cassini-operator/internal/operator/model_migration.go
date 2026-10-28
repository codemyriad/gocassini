package operator

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// speechModelCheckRetry spaces the retries of the startup speech model check
// (retired pin, active model install) when it could not finish: the model
// inventory could not be read, or the install could not be queued.
var speechModelCheckRetry = 30 * time.Second

// errSettingsChangedDuringMigration is returned by migrateRetiredSpeechModel
// when Settings changed what transcribes while it queued the install; the
// retried check migrates the settings as they are now.
var errSettingsChangedDuringMigration = errors.New("settings changed while the retired speech model was being migrated; checking again")

// errModelDownloadsDisabled is returned by ensureModelInstall for a model that
// is not installed on an operator that may not download one.
var errModelDownloadsDisabled = errors.New("downloads are disabled")

// settingsDevice is the device a settings snapshot runs on, for choosing and
// installing its model. A pinned GPU that is not visible right now still
// decides the tier, so the install a later start needs is not swapped for a
// CPU model.
func settingsDevice(s STTSettings) string {
	device, err := resolveDeviceForSettings(s)
	if err == nil {
		return device
	}
	if isCUDA(s.DeviceOverride) {
		return deviceCUDA
	}
	return deviceCPU
}

// retiredReplacement resolves a speech model id the catalogue has retired to
// the model its quality tier uses on device now. ok is false for an id that is
// listed or that no shipped model names in Replaces: an id no catalogue ever
// shipped is never swapped for a model nobody chose.
func retiredReplacement(models []modelInfo, id, quality, device string) (modelInfo, bool, error) {
	if strings.TrimSpace(id) == "" {
		return modelInfo{}, false, nil
	}
	if _, err := findModel(models, id, ""); err == nil {
		return modelInfo{}, false, nil
	}
	retired := false
	for _, m := range models {
		retired = retired || slices.Contains(m.Replaces, id)
	}
	if !retired {
		return modelInfo{}, false, nil
	}
	target, err := findModel(models, modelForQuality(quality, device), "")
	if err != nil {
		return modelInfo{}, false, fmt.Errorf("replacement for retired %s: %w", id, err)
	}
	return target, true, nil
}

// ensureModelInstall makes sure an install job will bring m onto device. It
// returns a queued or working job as it is, queues one when there is none,
// and, with requeue set, runs a finished one again (a failed, cancelled or
// stale "ready" job of a model whose files are not there). Without requeue a
// finished job is returned untouched for the caller to report. A model that is
// installed and ready needs nothing and returns a zero job.
func (rt *Runtime) ensureModelInstall(ctx context.Context, m modelInfo, device string, requeue bool) (modelJob, error) {
	if m.Installed && m.Ready {
		return modelJob{}, nil
	}
	if !m.Installed && rt.cfg.DisallowModelDownload {
		return modelJob{}, errModelDownloadsDisabled
	}
	job, err := rt.enqueueModel(ctx, m, device)
	if err != nil {
		return modelJob{}, err
	}
	if requeue && !modelJobRunning(job) {
		if _, err := rt.store.db.ExecContext(ctx, `UPDATE model_install_jobs SET state='queued',error='',updated_at=? WHERE id=? AND state IN ('ready','failed','cancelled')`, nowUTCString(), job.ID); err != nil {
			return modelJob{}, err
		}
		job.State, job.Error = "queued", ""
	}
	rt.invalidateModelInventory()
	return job, nil
}

// migrateRetiredSpeechModel moves settings pinned to a speech model the
// catalogue has retired (Parakeet v3, replaced by Parakeet Ultra) to the model
// their quality tier uses on their device, and queues it through the ordinary
// install job: the download, then the runtime check. Settings shows the job's
// progress, and builds wait for it rather than publishing without a transcript
// (see admitTranscription). The replaced model's files are removed by
// `cassini models probe` once the new model has passed its runtime check.
//
// The install is queued before the new pin is saved. A crash or a failed
// enqueue in between leaves the old pin, so the next attempt migrates again
// (enqueueing is idempotent) instead of finding the pin already moved and
// nothing installing it. recoverActiveSpeechModel is the backstop for an
// active model that is missing for any other reason.
//
// Only an id a shipped model lists in Replaces moves. An id no catalogue ever
// shipped stays as it is: admission reports it unavailable, by name, instead of
// transcribing with a model nobody chose. It reports whether it moved the pin.
func (rt *Runtime) migrateRetiredSpeechModel(ctx context.Context) (bool, error) {
	s := rt.currentSettings()
	if strings.TrimSpace(s.ActiveModel) == "" {
		return false, nil
	}
	device := settingsDevice(s)
	models, err := rt.modelInventory(ctx, device)
	if err != nil {
		return false, err
	}
	target, retired, err := retiredReplacement(models, s.ActiveModel, s.Quality, device)
	if err != nil || !retired {
		return false, err
	}

	// Transcription that is off downloads nothing: enabling it later goes
	// through Settings, which installs the selected model first.
	if s.TranscriptionEnabled {
		// This install needs the model now, so a finished earlier attempt at
		// it runs again.
		_, err := rt.ensureModelInstall(ctx, target, device, true)
		switch {
		case errors.Is(err, errModelDownloadsDisabled):
			rt.logger.Printf("stt_settings: %s is not installed and downloads are disabled; import it with cassini models import", target.ID)
		case err != nil:
			return false, fmt.Errorf("queue %s: %w", target.ID, err)
		}
	}

	// The inventory read and the enqueue are slow, and Settings may have been
	// saved meanwhile: move the pin on the settings as they are now, and only
	// if they still select the retired model on the same tier and device.
	// Everything else in them is kept as the other writer left it.
	rt.settingsWriteMu.Lock()
	defer rt.settingsWriteMu.Unlock()
	now := rt.currentSettings()
	if now.ActiveModel != s.ActiveModel || now.ActiveRevision != s.ActiveRevision ||
		normalizeQuality(now.Quality) != normalizeQuality(s.Quality) || settingsDevice(now) != device ||
		(now.TranscriptionEnabled && !s.TranscriptionEnabled) {
		// The next check looks again.
		return false, errSettingsChangedDuringMigration
	}
	previous := now.ActiveModel
	now.ActiveModel, now.ActiveRevision = target.ID, target.Revision
	if err := Save(rt.settingsPath, now); err != nil {
		return false, fmt.Errorf("save settings: %w", err)
	}
	rt.setSettings(now)
	rt.logger.Printf("stt_settings migrated: %s is retired; selected %s (%s) for quality=%s on %s", previous, target.ID, target.Revision, normalizeQuality(now.Quality), device)
	return true, nil
}

// recoverActiveSpeechModel makes sure the active speech model of enabled
// transcription is installed and has passed its runtime check on this
// runtime, or that an install job is on its way to that. It runs on every
// start: an app or native-runtime change invalidates the runtime check
// without touching the files, and an install interrupted before its job was
// durable (or a job that failed or was cancelled) would otherwise leave
// transcription on with no model and nothing installing it. A model that is
// not installed is downloaded only when downloads are allowed.
func (rt *Runtime) recoverActiveSpeechModel(ctx context.Context) error {
	s := rt.currentSettings()
	if !s.TranscriptionEnabled || strings.TrimSpace(s.ActiveModel) == "" {
		return nil
	}
	device := settingsDevice(s)
	models, err := rt.modelInventory(ctx, device)
	if err != nil {
		return err
	}
	m, err := findModel(models, s.ActiveModel, s.ActiveRevision)
	if err != nil {
		// Not a model this version runs: readiness and admission name it.
		return nil
	}
	if m.Installed && m.Ready {
		return nil
	}
	if _, running := rt.runningModelJob(ctx, m.ID, m.Revision, device); running {
		return nil
	}
	job, err := rt.ensureModelInstall(ctx, m, device, true)
	if errors.Is(err, errModelDownloadsDisabled) {
		rt.logger.Printf("stt_settings: active speech model %s is not installed and downloads are disabled; import it with cassini models import", m.ID)
		return nil
	}
	if err != nil {
		return fmt.Errorf("queue %s: %w", m.ID, err)
	}
	step := "install"
	if m.Installed {
		step = "its runtime check"
	}
	rt.logger.Printf("stt_settings: active speech model %s (%s) on %s queued for %s (job %s)", m.ID, m.Revision, device, step, job.ID)
	return nil
}

// modelJobRunning reports whether an install job is still queued or working.
func modelJobRunning(j modelJob) bool {
	switch j.State {
	case "ready", "failed", "cancelled":
		return false
	}
	return true
}

// runningModelJob returns the queued or working install job of a model
// revision on a device, if any.
func (rt *Runtime) runningModelJob(ctx context.Context, model, revision, device string) (modelJob, bool) {
	j, ok := rt.modelJobFor(ctx, model, revision, device)
	return j, ok && modelJobRunning(j)
}

// modelJobFor returns the install job of a model revision on a device, in
// whatever state, if there is one (enqueueModel keeps at most one).
func (rt *Runtime) modelJobFor(ctx context.Context, model, revision, device string) (modelJob, bool) {
	jobs, err := rt.modelJobs(ctx)
	if err != nil {
		return modelJob{}, false
	}
	for _, j := range jobs {
		if j.Model == model && j.Revision == revision && j.Device == device {
			return j, true
		}
	}
	return modelJob{}, false
}

// modelJobProgressText describes a running install job for a log line or a
// readiness message.
func modelJobProgressText(j modelJob) string {
	if j.State == "downloading" && j.Progress.Total > 0 {
		return fmt.Sprintf("downloading, %d%%", min(100, j.Progress.Completed*100/j.Progress.Total))
	}
	return strings.ReplaceAll(j.State, "_", " ")
}
