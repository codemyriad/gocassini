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

// resolvePublishInputPath names the single meeting a publish should export.
//
// Publishing used to hand `cassini publish` the whole `current/` library, so
// every recording re-exported every meeting — O(archive) per publish, ~7.5
// minutes at 67 meetings in production (D-459). The CLI already accepts one
// bundle, so the fix is to name one.
//
// The `.meeting` is preferred over the `.opus`, deliberately:
//
//   - The `.opus` is packed by a detached goroutine started *after* the publish
//     is enqueued, so on a rerun `current/<jobID>.opus` still holds the previous
//     attempt's audio when the publish worker picks the job up. Preferring it
//     would publish stale audio.
//   - A `.opus` that fails verification makes `cassini publish` fail outright,
//     whereas the `.meeting` branch repacks and keeps the meeting. The library
//     input has always chosen the recovering path; so does this.
//
// The `.opus` remains the fallback for a job whose `.meeting` has been pruned.
func resolvePublishInputPath(workRoot, jobID string) (string, error) {
	meetingPath := canonicalMeetingPath(workRoot, jobID)
	if _, err := os.Stat(meetingPath); err == nil {
		return meetingPath, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat %s: %w", meetingPath, err)
	}
	opusPath := canonicalOpusPath(workRoot, jobID)
	if _, err := os.Stat(opusPath); err == nil {
		return opusPath, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat %s: %w", opusPath, err)
	}
	return "", fmt.Errorf("job %s has no publishable meeting: neither %s nor %s exists", jobID, meetingPath, opusPath)
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

// attemptOpusPath is the portable meeting the seal stage produces for one
// attempt, and the exact file the publish that follows delivers (D-583).
//
// It is attempt-scoped on purpose. The durable `.opus` used to be written only
// to current/<jobID>.opus, by a detached pack per attempt; two reruns therefore
// raced for one path and the winner was whichever pack finished last, not
// whichever attempt was current. A rerun now seals runs/<job>--attempt-002.opus
// while attempt 1's artifact stays exactly as it was sealed.
func attemptOpusPath(workRoot, jobID string, attemptNumber int) string {
	return filepath.Join(runsRoot(workRoot), attemptBaseName(jobID, attemptNumber)+".opus")
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
