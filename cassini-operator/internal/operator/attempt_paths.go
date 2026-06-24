package operator

import (
	"fmt"
	"os"
	"path/filepath"
)

func currentRoot(workRoot string) string {
	return filepath.Join(workRoot, "current")
}

func runsRoot(workRoot string) string {
	return filepath.Join(workRoot, "runs")
}

func currentStagingRoot(workRoot string) string {
	return filepath.Join(currentRoot(workRoot), ".staging")
}

func canonicalRunPath(workRoot, jobID string) string {
	return filepath.Join(currentRoot(workRoot), jobID+".run")
}

func canonicalMeetingPath(workRoot, jobID string) string {
	return filepath.Join(currentRoot(workRoot), jobID+".meeting")
}

// canonicalOpusPath is the durable portable artifact stored next to the
// promoted `.meeting` bundle in current/. The `.opus` is the format that must
// survive once `.meeting` stops being a publish input (D-428); it is produced
// at promotion time by packing the promoted `.meeting` bundle.
func canonicalOpusPath(workRoot, jobID string) string {
	return filepath.Join(currentRoot(workRoot), jobID+".opus")
}

func attemptBaseName(jobID string, attemptNumber int) string {
	return fmt.Sprintf("%s--attempt-%03d", jobID, attemptNumber)
}

func attemptRunPath(workRoot, jobID string, attemptNumber int) string {
	return filepath.Join(runsRoot(workRoot), attemptBaseName(jobID, attemptNumber)+".run")
}

func attemptMeetingPath(workRoot, jobID string, attemptNumber int) string {
	return filepath.Join(runsRoot(workRoot), attemptBaseName(jobID, attemptNumber)+".meeting")
}

func attemptSitePath(workRoot, jobID string, attemptNumber int) string {
	return filepath.Join(runsRoot(workRoot), attemptBaseName(jobID, attemptNumber)+".site")
}

func siteStagingRoot(siteRoot string) string {
	return filepath.Clean(siteRoot) + ".staging"
}

func attemptLogsDir(workRoot, jobID string, attemptNumber int) string {
	return filepath.Join(runsRoot(workRoot), attemptBaseName(jobID, attemptNumber)+".logs")
}

func attemptLogPath(workRoot, jobID string, attemptNumber int, stage string) string {
	return filepath.Join(attemptLogsDir(workRoot, jobID, attemptNumber), stage+".log")
}

func openAttemptLogFile(workRoot, jobID string, attemptNumber int, stage string) (string, *os.File, error) {
	path := attemptLogPath(workRoot, jobID, attemptNumber, stage)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", nil, fmt.Errorf("create attempt log dir: %w", err)
	}
	file, err := os.Create(path)
	if err != nil {
		return "", nil, fmt.Errorf("create attempt log file: %w", err)
	}
	return path, file, nil
}
