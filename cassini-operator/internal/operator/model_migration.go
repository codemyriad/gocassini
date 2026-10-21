package operator

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// retiredModelRetry spaces the retries of a retired-model check whose model
// inventory could not be read.
const retiredModelRetry = 30 * time.Second

// migrateRetiredSpeechModel moves settings pinned to a speech model the
// catalogue has retired (Parakeet v3, replaced by Parakeet Ultra) to the model
// their quality tier uses on their device, and queues it through the ordinary
// install job: the download, then the runtime check. Settings shows the job's
// progress, and builds wait for it rather than publishing without a transcript
// (see admitTranscription). The replaced model's files are removed by
// `cassini models probe` once the new model has passed its runtime check.
//
// Only an id a shipped model lists in Replaces moves. An id no catalogue ever
// shipped stays as it is: admission reports it unavailable, by name, instead of
// transcribing with a model nobody chose. It reports whether it moved the pin.
func (rt *Runtime) migrateRetiredSpeechModel(ctx context.Context) (bool, error) {
	s := rt.currentSettings()
	if strings.TrimSpace(s.ActiveModel) == "" {
		return false, nil
	}
	device, err := resolveDeviceForSettings(s)
	if err != nil {
		// A pinned GPU that is not visible right now still decides the tier.
		device = deviceCPU
		if isCUDA(s.DeviceOverride) {
			device = deviceCUDA
		}
	}
	models, err := rt.modelInventory(ctx, device)
	if err != nil {
		return false, err
	}
	if _, err := findModel(models, s.ActiveModel, ""); err == nil {
		return false, nil
	}
	retired := false
	for _, m := range models {
		for _, id := range m.Replaces {
			retired = retired || id == s.ActiveModel
		}
	}
	if !retired {
		return false, nil
	}
	target, err := findModel(models, modelForQuality(s.Quality, device), "")
	if err != nil {
		return false, fmt.Errorf("replacement for retired %s: %w", s.ActiveModel, err)
	}

	previous := s.ActiveModel
	s.ActiveModel, s.ActiveRevision = target.ID, target.Revision
	if err := Save(rt.settingsPath, s); err != nil {
		return false, fmt.Errorf("save settings: %w", err)
	}
	rt.setSettings(s)
	rt.logger.Printf("stt_settings migrated: %s is retired; selected %s (%s) for quality=%s on %s", previous, target.ID, target.Revision, normalizeQuality(s.Quality), device)

	// Transcription that is off downloads nothing: enabling it later goes
	// through Settings, which installs the selected model first.
	if !s.TranscriptionEnabled || target.Ready {
		return true, nil
	}
	if rt.cfg.DisallowModelDownload && !target.Installed {
		rt.logger.Printf("stt_settings: %s is not installed and downloads are disabled; import it with cassini models import", target.ID)
		return true, nil
	}
	job, err := rt.enqueueModel(ctx, target, device)
	if err != nil {
		return true, fmt.Errorf("queue %s: %w", target.ID, err)
	}
	if !modelJobRunning(job) {
		// An earlier attempt at the same model finished one way or another;
		// this install needs it now, so run it again.
		if _, err := rt.store.db.ExecContext(ctx, `UPDATE model_install_jobs SET state='queued',error='',updated_at=? WHERE id=?`, nowUTCString(), job.ID); err != nil {
			return true, fmt.Errorf("queue %s: %w", target.ID, err)
		}
	}
	rt.invalidateModelInventory()
	return true, nil
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
	jobs, err := rt.modelJobs(ctx)
	if err != nil {
		return modelJob{}, false
	}
	for _, j := range jobs {
		if j.Model == model && j.Revision == revision && j.Device == device && modelJobRunning(j) {
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
