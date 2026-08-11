package operator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// verifySealedPublishInput confirms that the artifact a publish is about to
// deliver is the one its seal recorded.
//
// Publishing used to hand `cassini publish` the whole `current/` library, so
// every recording re-exported every meeting — O(archive) per publish, ~7.5
// minutes at 67 meetings in production (D-459). Naming one meeting fixed the
// cost; naming the *sealed* one fixes what gets delivered.
//
// It used to prefer `current/<job>.meeting`, and had to: the `.opus` was packed
// by a detached goroutine started after the publish was enqueued, so on a rerun
// it still held the previous attempt's audio. There is no preference left to
// express. The publish task carries an attempt-scoped path that only its own
// seal ever wrote, and the digest check here is what makes that a verified
// claim rather than a naming convention — it catches a truncated write, a
// half-copied artifact, and any later mutation of a file that is supposed to be
// immutable.
//
// A `.meeting` fallback is deliberately absent. Falling back would re-create
// exactly the double-pack and the staleness window this stage exists to close,
// and it would let a job publish audio nothing verified (D-583).
func verifySealedPublishInput(jobID, opusPath, expectedSHA256 string) error {
	opusPath = strings.TrimSpace(opusPath)
	if opusPath == "" {
		return fmt.Errorf("job %s has no sealed portable meeting to publish", jobID)
	}
	info, err := os.Stat(opusPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("job %s is missing its sealed portable meeting %s", jobID, opusPath)
		}
		return fmt.Errorf("stat %s: %w", opusPath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("sealed portable meeting %s is a directory", opusPath)
	}
	expectedSHA256 = strings.TrimSpace(expectedSHA256)
	if expectedSHA256 == "" {
		return fmt.Errorf("job %s has a sealed portable meeting with no recorded digest", jobID)
	}
	actual, err := fileSHA256(opusPath)
	if err != nil {
		return err
	}
	if actual != expectedSHA256 {
		return fmt.Errorf("sealed portable meeting %s changed since it was sealed: sha256 %s, want %s", opusPath, actual, expectedSHA256)
	}
	return nil
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

// attemptSealDir holds one attempt's sealed portable meeting, beside that
// attempt's `.run`, `.meeting`, `.site` and `.logs`.
func attemptSealDir(workRoot, jobID string, attemptNumber int) string {
	return filepath.Join(runsRoot(workRoot), attemptBaseName(jobID, attemptNumber)+".seal")
}

// attemptOpusPath is the portable meeting the seal stage produces for one
// attempt, and the exact file the publish that follows delivers (D-583).
//
// Two constraints meet here, and the directory is what satisfies both:
//
//   - It must be attempt-scoped. The durable `.opus` used to be written only to
//     current/<jobID>.opus, by a detached pack per attempt; two reruns raced for
//     one path and the winner was whichever pack finished last, not whichever
//     attempt was current. A rerun now seals into its own `.seal` directory
//     while attempt 1's artifact stays exactly as it was sealed.
//   - Its *file name* must still be the job id. The static-site exporter derives
//     a meeting's catalog id from the input file's stem, so a file named
//     `<job>--attempt-002.opus` would publish a rerun as a second, separate
//     meeting instead of updating the first — and the installed-ExApp harness
//     asserts exactly one catalog entry per job.
func attemptOpusPath(workRoot, jobID string, attemptNumber int) string {
	return filepath.Join(attemptSealDir(workRoot, jobID, attemptNumber), jobID+".opus")
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
