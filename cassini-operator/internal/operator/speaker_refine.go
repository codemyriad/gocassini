package operator

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// The refine attempt (docs/speaker-separation.md). It enters the pipeline at
// build/queued like a rerun, so the worker, its claim, the requeue dispatcher,
// recording priority and every stage after build are the ones every build
// uses. Only what the build stage runs differs, and that is decided here, by
// what the attempt's row says — never by buildTask, which a restart rebuilds
// from the jobs row alone.

const (
	envDiarizationModel = "CASSINI_DIARIZATION_MODEL"

	// The cassini CLI's exit codes for `speakers diarize` / `speakers apply`.
	speakersExitDiarizationUnavailable = 3
	speakersExitSpeakerNotFound        = 4
	speakersExitTurnsMissing           = 5

	maxSpeakersCLIStderr = 2048

	// speakerDiarizeModelMB is what the native diarizer holds whatever the
	// meeting's length: measured flat at 260-280 MB, with a margin.
	speakerDiarizeModelMB = 384
)

// speakerDiarizeMemMB is the RAM one diarization needs on top of the host's
// headroom: the model, plus the participant's track decoded to 16 kHz
// float32 (64 bytes per audio millisecond, about 230 MB an hour), which it
// holds whole while the model runs.
func speakerDiarizeMemMB(audioMs int64) int {
	return speakerDiarizeModelMB + int((max(audioMs, 0)*64+(1<<20)-1)>>20)
}

// withSpeakerEdits wraps the build stage. A refine attempt runs no build at
// all: it applies its edits to a copy of the meeting the job already
// published. Any other attempt builds, and when it carries a snapshot — a
// rerun of a meeting people edited — the edits are applied to the fresh
// bundle before it is promoted, so a rerun never quietly undoes them.
func (rt *Runtime) withSpeakerEdits(build func(context.Context, buildTask) (string, error)) func(context.Context, buildTask) (string, error) {
	return func(ctx context.Context, task buildTask) (string, error) {
		meetingPath := attemptMeetingPath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
		kind, doc, err := rt.store.AttemptSpeakerEdits(context.Background(), task.JobID, task.AttemptNumber)
		if errors.Is(err, sql.ErrNoRows) {
			// No attempt row: nothing to apply, and the claim decides the rest.
			kind, doc, err = "", nil, nil
		}
		if err != nil {
			return meetingPath, fmt.Errorf("load attempt kind: %w", err)
		}
		if kind == triggerKindRefine {
			if doc == nil {
				return meetingPath, errors.New("refine attempt carries no speaker edits")
			}
			builtPath, err := rt.executeRefineCLI(ctx, task, *doc)
			return rt.speakerEditsResult(ctx, task, builtPath, err)
		}
		builtPath, err := build(ctx, task)
		if err != nil || doc == nil {
			return builtPath, err
		}
		// The replay's output goes on the end of the build's own log.
		logPath := attemptLogPath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber, "build")
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			return rt.speakerEditsResult(ctx, task, builtPath, fmt.Errorf("create attempt log dir: %w", err))
		}
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return rt.speakerEditsResult(ctx, task, builtPath, fmt.Errorf("open build log for speaker edits: %w", err))
		}
		defer logFile.Close()
		fmt.Fprintf(logFile, "rerun: replaying speaker edits revision %d\n", doc.Revision)
		err = rt.applySpeakerEdits(ctx, task, builtPath, *doc, logFile)
		return rt.speakerEditsResult(ctx, task, builtPath, err)
	}
}

// speakerEditsResult keeps the reason a speaker-edits apply failed where the
// page reads it, and returns no bundle path with the failure: the bundle is a
// ready copy, and the build worker would otherwise report its manifest ("build
// stage ready failed") instead of the reason. A recording that started is not
// a failure — the attempt yields and runs again.
func (rt *Runtime) speakerEditsResult(ctx context.Context, task buildTask, builtPath string, err error) (string, error) {
	if err == nil {
		return builtPath, nil
	}
	// Nor is a wait for memory: the attempt goes back to the queue like a
	// build that waits, and nothing about the edits failed.
	if ctx.Err() != nil || transientResourceError(err) {
		return builtPath, err
	}
	if storeErr := rt.store.SetSpeakerEditsError(context.Background(), task.JobID, err.Error()); storeErr != nil {
		rt.logger.Printf("speaker edits error update failed id=%s attempt=%d: %v", task.JobID, task.AttemptNumber, storeErr)
	}
	return "", err
}

// executeRefineCLI builds a refine attempt's meeting: a fresh copy of the
// job's current meeting with the edits applied. The copy carries the same
// meeting.webm byte for byte — `speakers apply` refuses to change it — so the
// seal that follows packs the same audio and every mark stays resolved.
func (rt *Runtime) executeRefineCLI(ctx context.Context, task buildTask, doc speakerEditsDoc) (string, error) {
	meetingPath := attemptMeetingPath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
	// The directory belongs to this attempt alone; a yielded earlier try may
	// have left half a copy.
	if err := os.RemoveAll(meetingPath); err != nil {
		return meetingPath, fmt.Errorf("clear refine meeting dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(meetingPath), 0o755); err != nil {
		return meetingPath, fmt.Errorf("create meeting parent dir: %w", err)
	}
	logPath, logFile, err := openAttemptLogFile(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber, "build")
	if err != nil {
		return meetingPath, err
	}
	defer logFile.Close()
	if err := rt.store.SetAttemptStageLogPath(context.Background(), task.JobID, task.AttemptNumber, "build", logPath); err != nil {
		return meetingPath, err
	}
	source := canonicalMeetingPath(rt.cfg.WorkRoot, task.JobID)
	if _, err := os.Stat(filepath.Join(source, "cassini.json")); err != nil {
		return meetingPath, fmt.Errorf("refine needs the job's current meeting: %w", err)
	}
	if unpublished, err := rt.speakerMeetingUnpublished(ctx, task.JobID); err != nil {
		return meetingPath, err
	} else if unpublished {
		return meetingPath, fmt.Errorf("%s: %s is a rebuild that was never published; a refine republishes only the published meeting", speakerReasonUnpublishedRebuild, source)
	}
	fmt.Fprintf(logFile, "refine: speaker edits revision %d applied to a copy of %s\n", doc.Revision, source)
	if err := copyDirectory(source, meetingPath); err != nil {
		return meetingPath, fmt.Errorf("copy current meeting: %w", err)
	}
	if err := rt.applySpeakerEdits(ctx, task, meetingPath, doc, logFile); err != nil {
		return meetingPath, err
	}
	if _, err := os.Stat(filepath.Join(meetingPath, "cassini.json")); err != nil {
		return meetingPath, fmt.Errorf("refine output missing cassini.json: %w", err)
	}
	return meetingPath, nil
}

// applySpeakerEdits applies doc to the meeting bundle at meetingPath in place:
// diarize each split participant that has no stored turns yet (once, for
// ever), hand the CLI the stored turns and the document, and keep its report
// on the attempt until the attempt publishes.
func (rt *Runtime) applySpeakerEdits(ctx context.Context, task buildTask, meetingPath string, doc speakerEditsDoc, logFile io.Writer) error {
	workDir := filepath.Join(attemptLogsDir(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber), "speakers")
	if err := os.RemoveAll(workDir); err != nil {
		return fmt.Errorf("clear speaker edits dir: %w", err)
	}
	turnsDir := filepath.Join(workDir, "turns")
	if err := os.MkdirAll(turnsDir, 0o755); err != nil {
		return fmt.Errorf("create speaker turns dir: %w", err)
	}
	env := setEnvKey(rt.childEnv(), envDisallowModelDownload, "1")

	for _, split := range doc.Splits {
		if !speakerIDPattern.MatchString(split.SpeakerID) {
			return fmt.Errorf("speaker edits: %q is not a participant id", split.SpeakerID)
		}
		turns, ok, err := rt.store.SpeakerSplitTurns(context.Background(), task.JobID, split.SpeakerID)
		if err != nil {
			return err
		}
		if !ok {
			// Diarizing decodes the participant's whole track and runs a model,
			// next to Nextcloud and Talk: it waits for the memory it needs as a
			// build does, and defers the attempt when that does not come.
			limits := resourceLimitsFromEnv()
			need := speakerDiarizeMemMB(readSpeakerMeetingAudioMs(meetingPath)) + limits.cpuMemHeadroomMB
			if err := limits.waitForMemory(ctx, need, rt.logger.Printf); err != nil {
				return err
			}
			if turns, err = rt.diarizeSpeaker(ctx, task, split.SpeakerID, workDir, env, logFile); err != nil {
				return err
			}
		}
		if err := os.WriteFile(filepath.Join(turnsDir, split.SpeakerID+".json"), []byte(turns), 0o644); err != nil {
			return fmt.Errorf("write speaker turns: %w", err)
		}
	}

	editsPath := filepath.Join(workDir, "speaker-edits.json")
	raw, err := json.Marshal(doc.normalized())
	if err != nil {
		return fmt.Errorf("encode speaker edits: %w", err)
	}
	if err := os.WriteFile(editsPath, raw, 0o644); err != nil {
		return fmt.Errorf("write speaker edits: %w", err)
	}
	// --recording: stored turns are applied only to the capture they were
	// measured on, which the CLI checks by its SHA-256.
	report, err := rt.runSpeakersCLI(ctx, env, logFile, "apply", meetingPath, "--edits", editsPath, "--turns-dir", turnsDir, "--recording", task.ArtifactRunPath, "--json")
	if err != nil {
		return err
	}
	report = bytes.TrimSpace(report)
	if !json.Valid(report) {
		return fmt.Errorf("cassini speakers apply: report is not JSON")
	}
	return rt.store.SetAttemptSpeakerEditsReport(context.Background(), task.JobID, task.AttemptNumber, string(report))
}

// diarizeSpeaker runs the diarizer over one participant's own track of the
// job's capture and stores the turns. When another attempt stored turns for
// the same participant first, those win and these are dropped.
func (rt *Runtime) diarizeSpeaker(ctx context.Context, task buildTask, speakerID, workDir string, env []string, logFile io.Writer) (string, error) {
	out := filepath.Join(workDir, "diarized", speakerID+".json")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", fmt.Errorf("create diarization dir: %w", err)
	}
	if _, err := rt.runSpeakersCLI(ctx, env, logFile, "diarize", task.ArtifactRunPath, "--speaker", speakerID, "--out", out); err != nil {
		return "", err
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		return "", fmt.Errorf("cassini speakers diarize wrote no turns for %s: %w", speakerID, err)
	}
	var set struct {
		SpeakerID string `json:"speakerId"`
		Source    struct {
			SHA256 string `json:"sha256"`
		} `json:"source"`
		Model struct {
			SHA256 string `json:"sha256"`
		} `json:"model"`
	}
	if err := json.Unmarshal(raw, &set); err != nil {
		return "", fmt.Errorf("cassini speakers diarize %s: %w", speakerID, err)
	}
	if set.SpeakerID != speakerID {
		return "", fmt.Errorf("cassini speakers diarize returned turns for %q, asked for %q", set.SpeakerID, speakerID)
	}
	return rt.store.PutSpeakerSplitTurns(context.Background(), task.JobID, speakerID, string(raw), set.Model.SHA256, set.Source.SHA256, nowUTCString())
}

// runSpeakersCLI runs `cassini speakers <args>` and returns its stdout. The
// failure names what the CLI said on stderr, whose first word is the reason
// (`diarization-unavailable:`, `speaker-not-found:`, `turns-missing:`).
func (rt *Runtime) runSpeakersCLI(ctx context.Context, env []string, logFile io.Writer, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, rt.cfg.CassiniBin, append([]string{"speakers"}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = io.MultiWriter(&stdout, logFile)
	cmd.Stderr = io.MultiWriter(&stderr, writerOrDiscard(rt.stderr), logFile)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd.Process) }
	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), nil
	}
	msg := strings.TrimSpace(stderr.String())
	if len(msg) > maxSpeakersCLIStderr {
		msg = msg[len(msg)-maxSpeakersCLIStderr:]
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && msg != "" {
		switch exitErr.ExitCode() {
		case speakersExitDiarizationUnavailable, speakersExitSpeakerNotFound, speakersExitTurnsMissing:
			return nil, fmt.Errorf("cassini speakers %s: %s", args[0], firstLine(msg))
		}
		return nil, fmt.Errorf("cassini speakers %s: %w: %s", args[0], err, msg)
	}
	return nil, fmt.Errorf("cassini speakers %s: %w", args[0], err)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}

// The catalogue kind of a speaker separation model in `cassini models list`,
// and the one Settings installs.
const (
	modelKindDiarization     = "diarization"
	defaultDiarizationModel  = "nemotron-3-diarization-int8"
	diarizationInstallAdvice = "An administrator can download Voice separation in Cassini's Settings, or run `cassini models install " + defaultDiarizationModel + "` (offline: `cassini models import`)."
)

// diarizationAvailability says whether a refine could diarize a new
// participant, and if not, why and what an administrator can do about it. It
// asks the CLI the refine will run, through the same model inventory as the
// Settings page: a diarizer is ready when it is installed in the operator's
// model store and the native runtime can run it. CASSINI_DIARIZATION_MODEL,
// the development override the CLI honours first, is checked as a file.
func (rt *Runtime) diarizationAvailability(ctx context.Context) (bool, string) {
	if path := childEnvValue(rt.childEnv(), envDiarizationModel); path != "" {
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			return false, fmt.Sprintf("%s names %s, which is not a model file.", envDiarizationModel, path)
		}
		return true, ""
	}
	models, err := rt.cachedModelInventory(ctx, "cpu")
	if err != nil {
		return false, fmt.Sprintf("Cassini could not read its installed models: %v", err)
	}
	known, unsupported := false, false
	for _, m := range models {
		if m.Kind != modelKindDiarization {
			continue
		}
		known = true
		if m.Ready {
			return true, ""
		}
		if m.RuntimeSupported != nil && !*m.RuntimeSupported {
			unsupported = true
		}
	}
	switch {
	case !known:
		return false, "This Cassini version has no speaker separation model."
	case unsupported:
		return false, "This server's Cassini runtime cannot run speaker separation (it needs the Linux build with Nemotron support)."
	}
	return false, "The speaker separation model is not installed. " + diarizationInstallAdvice
}

func childEnvValue(env []string, key string) string {
	prefix := key + "="
	value := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			value = kv[len(prefix):]
		}
	}
	return strings.TrimSpace(value)
}
