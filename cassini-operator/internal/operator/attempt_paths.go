package operator

import (
	"fmt"
	"os"
	"path/filepath"
)

func attemptBaseName(jobID string, attemptNumber int) string {
	return fmt.Sprintf("%s--attempt-%03d", jobID, attemptNumber)
}

func attemptRunPath(workRoot, jobID string, attemptNumber int) string {
	return filepath.Join(workRoot, attemptBaseName(jobID, attemptNumber)+".run")
}

func attemptMeetingPath(workRoot, jobID string, attemptNumber int) string {
	return filepath.Join(workRoot, attemptBaseName(jobID, attemptNumber)+".meeting")
}

func attemptLogsDir(workRoot, jobID string, attemptNumber int) string {
	return filepath.Join(workRoot, attemptBaseName(jobID, attemptNumber)+".logs")
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
