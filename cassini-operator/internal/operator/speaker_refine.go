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
	"os/user"
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
)

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
	if ctx.Err() != nil {
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
	report, err := rt.runSpeakersCLI(ctx, env, logFile, "apply", meetingPath, "--edits", editsPath, "--turns-dir", turnsDir, "--json")
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

// diarizationModelInstalled is the operator's best guess at whether a refine
// could diarize a new participant: the model file the CLI would resolve is
// there. It cannot see whether the native runtime supports Nemotron; a refine
// that finds it does not fails with the CLI's own reason.
func (rt *Runtime) diarizationModelInstalled() bool {
	env := rt.childEnv()
	path := childEnvValue(env, envDiarizationModel)
	if path == "" {
		root := childEnvValue(env, envCacheRoot)
		if root == "" {
			root = defaultCLICacheRoot()
		}
		path = filepath.Join(root, "models", "nemotron-3-diarization-int8", "model.int8.onnx")
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// cliHomeDir is the home directory the cassini CLI falls back to; a seam for
// tests.
var cliHomeDir = func() string {
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		return u.HomeDir
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	return ""
}

// defaultCLICacheRoot is the cache root the cassini CLI uses when nothing sets
// CASSINI_CACHE_ROOT (defaultCassiniCacheRoot in the recorder): outside the
// ExApp the operator must look where the CLI it runs will look.
func defaultCLICacheRoot() string {
	if home := cliHomeDir(); home != "" {
		return filepath.Join(home, ".cache", "cassini")
	}
	return filepath.Join(".", ".cache", "cassini")
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
