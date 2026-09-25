package operator

import (
	"errors"
	"os"
	"path/filepath"
)

func requireReadyRunBundle(path string) (string, error) {
	m, err := readRunManifest(filepath.Join(path, "cassini.json"))
	if err != nil {
		return "", err
	}
	if m.State != bundleStateReady || m.Stage != "ready" || m.Recording.Path == "" {
		return "", errors.New("source recording is not ready")
	}
	media := filepath.Join(path, m.Recording.Path)
	if err = safeArtifactPath(path, media); err != nil {
		return "", err
	}
	info, err := os.Stat(media)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("recording is not a regular file")
	}
	return media, nil
}

type artifactAvailability struct {
	Source             string `json:"source"`
	Output             string `json:"output"`
	RerunBlockedReason string `json:"rerun_blocked_reason,omitempty"`
	PublishedAttempt   int    `json:"published_attempt"`
}

func (rt *Runtime) artifactAvailability(job Job) artifactAvailability {
	a := artifactAvailability{Source: "missing", Output: "missing"}
	if job.ArtifactRunPath != nil {
		if _, err := requireReadyRunBundle(*job.ArtifactRunPath); err == nil {
			a.Source = "present"
		}
	}
	if job.ArtifactOpusPath != nil {
		if _, err := os.Stat(*job.ArtifactOpusPath); err == nil {
			a.Output = "present"
		}
	}
	var source, output string
	_ = rt.store.db.QueryRow(`SELECT published_attempt,source,output FROM artifact_availability WHERE job_id=?`, job.ID).Scan(&a.PublishedAttempt, &source, &output)
	if source == "expired" {
		a.Source = source
	}
	if output == "expired" {
		a.Output = output
	}
	if a.Source != "present" {
		a.RerunBlockedReason = "Source recording is " + a.Source + "; this job can no longer be rerun."
	}
	if rt.pendingArtifactOperation(job.ID) {
		a.RerunBlockedReason = "Archive operation awaiting recovery"
		a.Output = "pending"
	}
	return a
}
