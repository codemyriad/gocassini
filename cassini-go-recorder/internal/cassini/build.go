package cassini

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type buildOptions struct {
	inputPath string
	outDir    string
	device    string
	keepWork  bool
	rebuild   bool
}

func runBuild(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	opts := buildOptions{
		device: "auto",
	}

	fs := flag.NewFlagSet("cassini build", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.outDir, "out", "", "output .meeting bundle directory or portable .opus file")
	fs.StringVar(&opts.device, "device", "auto", "transcriber device: auto, cpu, cuda")
	fs.BoolVar(&opts.keepWork, "keep-work", false, "keep transcriber work files inside the meeting bundle")
	fs.BoolVar(&opts.rebuild, "rebuild-image", false, "force rebuild of the transcriber container image")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini build ./runs/meeting.run --out "./2026-03-11 Weekly Sync.opus"
  cassini build /path/to/meeting.mkv --out "./2026-03-11 Weekly Sync.opus"
  cassini build ./runs/meeting.run --out ./meetings/meeting.meeting
  cassini build /path/to/meeting.mkv --out ./meetings/meeting.meeting

`+"\n")
		fs.PrintDefaults()
	}

	parseArgs := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		parseArgs = append(append([]string{}, args[1:]...), args[0])
	}

	if err := fs.Parse(parseArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}

	opts.inputPath = fs.Arg(0)
	if err := validateBuildOptions(opts); err != nil {
		fmt.Fprintf(stderr, "build configuration error: %v\n", err)
		return 2
	}
	if isPortableMeetingOutput(opts.outDir) {
		return runBuildPortable(ctx, opts, stdout, stderr)
	}

	fmt.Fprintln(stdout, "[1/4] Preparing meeting bundle")
	bundle, err := PrepareMeetingBundle(opts.outDir)
	if err != nil {
		fmt.Fprintf(stderr, "prepare meeting bundle: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, "[2/4] Validating environment")
	_ = UpdateMeetingBundleStatus(bundle, bundleStatePreparing, "doctor", "")
	if err := runDoctorChecks("build", stdout); err != nil {
		_ = UpdateMeetingBundleStatus(bundle, bundleStateFailed, "doctor", "build blocked by doctor failures")
		fmt.Fprintf(stderr, "build blocked by doctor failures\n")
		fmt.Fprintf(stderr, "partial_meeting -> %s\n", bundle.RootDir)
		return 1
	}

	input, err := resolveBuildInput(opts.inputPath)
	if err != nil {
		_ = UpdateMeetingBundleStatus(bundle, bundleStateFailed, "resolve-input", err.Error())
		fmt.Fprintf(stderr, "resolve build input: %v\n", err)
		fmt.Fprintf(stderr, "partial_meeting -> %s\n", bundle.RootDir)
		return 1
	}

	fmt.Fprintln(stdout, "[3/4] Transcribing and building meeting artifact")
	fmt.Fprintln(stdout, "[4/4] Finalizing meeting bundle")
	if err := executeBuildIntoBundle(ctx, input, bundle, opts, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		fmt.Fprintf(stderr, "partial_meeting -> %s\n", bundle.RootDir)
		return 1
	}

	fmt.Fprintf(stdout, "meeting -> %s\n", bundle.RootDir)
	return 0
}

func runBuildPortable(ctx context.Context, opts buildOptions, stdout, stderr io.Writer) int {
	outPath, err := preparePortableMeetingOutput(opts.outDir)
	if err != nil {
		fmt.Fprintf(stderr, "prepare portable meeting output: %v\n", err)
		return 1
	}
	if ready, err := portableMeetingAlreadyReady(outPath); err != nil {
		fmt.Fprintf(stderr, "inspect portable meeting output: %v\n", err)
		return 1
	} else if ready {
		fmt.Fprintf(stdout, "portable_meeting -> %s (already ready)\n", outPath)
		return 0
	}

	workspace, err := ensurePortableWorkspace(outPath)
	if err != nil {
		fmt.Fprintf(stderr, "prepare work directory: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, "[1/4] Validating environment")
	if err := runDoctorChecks("build", stdout); err != nil {
		fmt.Fprintf(stderr, "build blocked by doctor failures\n")
		printPortableResumeHint(stderr, workspace.RootDir, outPath)
		return 1
	}

	input, err := resolveBuildInput(opts.inputPath)
	if err != nil {
		fmt.Fprintf(stderr, "resolve build input: %v\n", err)
		printPortableResumeHint(stderr, workspace.RootDir, outPath)
		return 1
	}

	fmt.Fprintln(stdout, "[2/4] Preparing or resuming meeting workspace")
	bundle, reusedMeeting, err := reusableMeetingBundle(workspace.MeetingDir, input, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "prepare meeting bundle: %v\n", err)
		printPortableResumeHint(stderr, workspace.RootDir, outPath)
		return 1
	}
	if !reusedMeeting {
		fmt.Fprintln(stdout, "[3/4] Transcribing and building meeting artifact")
		if err := executeBuildIntoBundle(ctx, input, bundle, opts, stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "%v\n", err)
			printPortableResumeHint(stderr, workspace.RootDir, outPath)
			return 1
		}
	}

	fmt.Fprintln(stdout, "[4/4] Writing portable meeting file")
	if err := packMeetingBundle(ctx, bundle.RootDir, outPath, portablePackOptions{
		Title:        titleFromOutputPath(outPath),
		CreatedAtUTC: buildInputCreatedAtUTC(input),
	}); err != nil {
		fmt.Fprintf(stderr, "write portable meeting file: %v\n", err)
		printPortableResumeHint(stderr, workspace.RootDir, outPath)
		return 1
	}

	_ = os.RemoveAll(workspace.RootDir)
	fmt.Fprintf(stdout, "portable_meeting -> %s\n", outPath)
	return 0
}

func validateBuildOptions(opts buildOptions) error {
	if opts.outDir == "" {
		return errors.New("--out is required")
	}
	switch opts.device {
	case "auto", "cpu", "cuda":
	default:
		return fmt.Errorf("invalid --device %q", opts.device)
	}
	return nil
}

type buildInput struct {
	SourceKind    string
	SourcePath    string
	RecordingPath string
}

func resolveBuildInput(inputPath string) (buildInput, error) {
	if bundle, ok, err := LoadRunBundle(inputPath); err != nil {
		return buildInput{}, err
	} else if ok {
		if !bundleIsReady(bundle.Manifest.State) {
			return buildInput{}, fmt.Errorf("run bundle is not ready: %s", bundleStateSummary(bundle.Manifest.State, bundle.Manifest.Stage))
		}
		recordingPath := filepath.Join(bundle.RootDir, bundle.Manifest.Recording.Path)
		switch strings.ToLower(bundle.Manifest.Recording.Format) {
		case "mkv":
			if _, err := os.Stat(recordingPath); err != nil {
				return buildInput{}, fmt.Errorf("run bundle recording missing: %s", recordingPath)
			}
			return buildInput{
				SourceKind:    "run",
				SourcePath:    bundle.RootDir,
				RecordingPath: recordingPath,
			}, nil
		case "csr":
			return buildInput{}, fmt.Errorf("run bundle contains %s, but cassini build currently requires an MKV recording", bundle.Manifest.Recording.Format)
		default:
			return buildInput{}, fmt.Errorf("unsupported run bundle recording format %q", bundle.Manifest.Recording.Format)
		}
	}

	resolved, err := filepath.Abs(inputPath)
	if err != nil {
		return buildInput{}, fmt.Errorf("resolve input path: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return buildInput{}, fmt.Errorf("stat input path: %w", err)
	}
	if info.IsDir() {
		return buildInput{}, fmt.Errorf("unsupported build input directory: %s", resolved)
	}
	if !strings.EqualFold(filepath.Ext(resolved), ".mkv") {
		return buildInput{}, fmt.Errorf("build input must be a .run bundle or .mkv file: %s", resolved)
	}
	return buildInput{
		SourceKind:    "mkv",
		SourcePath:    resolved,
		RecordingPath: resolved,
	}, nil
}

func transcriberRunnerPath() (string, error) {
	if override := os.Getenv("CASSINI_TRANSCRIBER_RUNNER"); override != "" {
		resolved, err := filepath.Abs(override)
		if err != nil {
			return "", fmt.Errorf("resolve CASSINI_TRANSCRIBER_RUNNER: %w", err)
		}
		return resolved, nil
	}

	repoRoot, err := findRepoRoot()
	if err != nil {
		return "", err
	}
	path := filepath.Join(repoRoot, "cassini-transcriber", "bin", "docker-run-local.sh")
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("transcriber runner not found at %s: %w", path, err)
	}
	if info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("transcriber runner is not executable: %s", path)
	}
	return path, nil
}

func runDoctorChecks(target string, stdout io.Writer) error {
	checks := collectDoctorChecks(target)
	hasFail := false
	for _, check := range checks {
		fmt.Fprintf(stdout, "%s %s\n", check.status, check.summary)
		if check.advice != "" {
			fmt.Fprintf(stdout, "fix %s\n", check.advice)
		}
		if check.status == doctorFail {
			hasFail = true
		}
	}
	if hasFail {
		return errors.New("doctor failures")
	}
	return nil
}

func executeBuildIntoBundle(ctx context.Context, input buildInput, bundle MeetingBundle, opts buildOptions, stdout, stderr io.Writer) error {
	_ = UpdateMeetingBundleSource(bundle, input.SourceKind, input.SourcePath, "build")

	runnerPath, err := transcriberRunnerPath()
	if err != nil {
		_ = UpdateMeetingBundleStatus(bundle, bundleStateFailed, "resolve-runner", err.Error())
		return fmt.Errorf("resolve transcriber runner: %w", err)
	}

	cmdArgs := []string{
		"--input", input.RecordingPath,
		"--output-dir", bundle.RootDir,
		"--device", opts.device,
	}
	if opts.rebuild {
		cmdArgs = append(cmdArgs, "--rebuild")
	}
	cmd := exec.CommandContext(ctx, runnerPath, cmdArgs...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		_ = UpdateMeetingBundleStatus(bundle, bundleStateFailed, "build", err.Error())
		return fmt.Errorf("build failed: %w", err)
	}

	if !opts.keepWork {
		_ = os.RemoveAll(filepath.Join(bundle.RootDir, "_work"))
	}

	if err := FinalizeMeetingBundle(bundle, MeetingBundleManifest{
		SourceKind: input.SourceKind,
		SourcePath: input.SourcePath,
	}); err != nil {
		_ = UpdateMeetingBundleStatus(bundle, bundleStateFailed, "finalize", err.Error())
		return fmt.Errorf("finalize meeting bundle: %w", err)
	}
	return nil
}

func buildInputCreatedAtUTC(input buildInput) string {
	switch input.SourceKind {
	case "run":
		if bundle, ok, err := LoadRunBundle(input.SourcePath); err == nil && ok {
			return strings.TrimSpace(bundle.Manifest.CreatedAtUTC)
		}
	case "mkv":
		if info, err := os.Stat(input.SourcePath); err == nil {
			return info.ModTime().UTC().Format(time.RFC3339)
		}
	}
	return ""
}
