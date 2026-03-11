package cassini

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gocassini/internal/app"
	"gocassini/internal/config"
	inspectpkg "gocassini/internal/inspect"
)

const defaultRecorderName = "CassiniRecorder"

var runRecorderApp = app.RunContext

type recordOptions struct {
	callURL           string
	outDir            string
	name              string
	durationSeconds   int
	stopWhenRoomEmpty bool
	roomEmptyGraceSec float64
	insecure          bool
	turnMode          string
	keepIntermediate  bool
	simulate          bool
	simTracks         int
	simPackets        int
}

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printRootUsage(stdout)
		return 0
	}

	switch args[0] {
	case "help", "-h", "--help":
		printRootUsage(stdout)
		return 0
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	case "build":
		return runBuild(ctx, args[1:], stdout, stderr)
	case "dev":
		return runDev(ctx, args[1:], stdout, stderr)
	case "inspect":
		return runInspect(args[1:], stdout, stderr)
	case "publish":
		return runPublish(ctx, args[1:], stdout, stderr)
	case "record":
		return runRecord(ctx, args[1:], stdout, stderr)
	case "serve":
		return runServe(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		printRootUsage(stderr)
		return 2
	}
}

func runRecord(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	opts := recordOptions{
		name:              defaultRecorderName,
		stopWhenRoomEmpty: true,
		roomEmptyGraceSec: 30,
		turnMode:          "all",
		simTracks:         3,
		simPackets:        150,
	}

	fs := flag.NewFlagSet("cassini record", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.callURL, "call", "", "Nextcloud Talk call URL")
	fs.StringVar(&opts.outDir, "out", "", "output portable .opus file or debug .run bundle directory")
	fs.StringVar(&opts.name, "name", defaultRecorderName, "display name for the recorder guest")
	fs.IntVar(&opts.durationSeconds, "duration", 0, "hard runtime limit in seconds (0 disables fixed timeout)")
	fs.BoolVar(&opts.stopWhenRoomEmpty, "stop-when-room-empty", true, "stop after all remote participants leave")
	fs.Float64Var(&opts.roomEmptyGraceSec, "room-empty-grace", 30, "seconds to wait before stopping after room becomes empty")
	fs.BoolVar(&opts.insecure, "insecure", false, "disable TLS certificate verification (testing only)")
	fs.StringVar(&opts.turnMode, "turn-mode", "all", "TURN usage mode: off, udp-only, all")
	fs.BoolVar(&opts.keepIntermediate, "keep-intermediate", false, "keep recorder intermediate work files inside the run bundle")
	fs.BoolVar(&opts.simulate, "simulate", false, "write a local synthetic capture bundle for testing")
	fs.IntVar(&opts.simTracks, "sim-tracks", 3, "number of synthetic tracks in simulate mode")
	fs.IntVar(&opts.simPackets, "sim-packets", 150, "packets per synthetic track in simulate mode")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini record --call <CALL_URL> --out "./2026-03-11 Weekly Sync.opus"
  cassini record --call <CALL_URL> --out ./runs/meeting.run
  cassini record --simulate --out ./runs/demo.run

`+"\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "record does not accept positional arguments: %v\n", fs.Args())
		return 2
	}
	if err := validateRecordOptions(opts); err != nil {
		fmt.Fprintf(stderr, "record configuration error: %v\n", err)
		return 2
	}
	if isPortableMeetingOutput(opts.outDir) {
		return runRecordPortable(ctx, opts, stdout, stderr)
	}

	fmt.Fprintln(stdout, "[1/3] Preparing run bundle")
	bundle, err := PrepareRunBundle(opts.outDir, opts.simulate)
	if err != nil {
		fmt.Fprintf(stderr, "prepare run bundle: %v\n", err)
		return 1
	}

	cfg := config.Config{
		Mode:                "talk",
		OutputPath:          bundle.RecordingPath,
		CleanupIntermediate: !opts.keepIntermediate,
		Duration:            time.Duration(opts.durationSeconds) * time.Second,
		StopWhenRoomEmpty:   opts.stopWhenRoomEmpty,
		RoomEmptyGrace:      time.Duration(opts.roomEmptyGraceSec * float64(time.Second)),
		CallURL:             opts.callURL,
		GuestName:           opts.name,
		JoinFlags:           1,
		Insecure:            opts.insecure,
		TurnMode:            opts.turnMode,
		SimTracks:           opts.simTracks,
		SimPackets:          opts.simPackets,
	}
	if opts.simulate {
		cfg.Mode = "simulate"
	}
	_ = UpdateRunBundleStatus(bundle, bundleStatePreparing, "record", "")

	fmt.Fprintln(stdout, "[2/3] Recording")
	if err := runRecorderApp(ctx, cfg); err != nil {
		_ = UpdateRunBundleStatus(bundle, bundleStateFailed, "record", err.Error())
		fmt.Fprintf(stderr, "record failed: %v\n", err)
		fmt.Fprintf(stderr, "partial_run -> %s\n", bundle.RootDir)
		return 1
	}

	fmt.Fprintln(stdout, "[3/3] Finalizing run bundle")
	if err := FinalizeRunBundle(bundle, RunManifest{
		SourceMode:   cfg.Mode,
		RecorderName: opts.name,
	}); err != nil {
		_ = UpdateRunBundleStatus(bundle, bundleStateFailed, "finalize", err.Error())
		fmt.Fprintf(stderr, "finalize run bundle: %v\n", err)
		fmt.Fprintf(stderr, "partial_run -> %s\n", bundle.RootDir)
		return 1
	}

	fmt.Fprintf(stdout, "run -> %s\n", bundle.RootDir)
	return 0
}

func runRecordPortable(ctx context.Context, opts recordOptions, stdout, stderr io.Writer) int {
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

	fmt.Fprintln(stdout, "[1/5] Validating environment")
	if err := runDoctorChecks("build", stdout); err != nil {
		fmt.Fprintf(stderr, "record blocked by doctor failures\n")
		printPortableResumeHint(stderr, workspace.RootDir, outPath)
		return 1
	}

	fmt.Fprintln(stdout, "[2/5] Preparing capture workspace")
	bundle, reusedRun, err := reusableRunBundle(workspace.RunDir, opts.name, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "prepare run bundle: %v\n", err)
		printPortableResumeHint(stderr, workspace.RootDir, outPath)
		return 1
	}

	cfg := config.Config{
		Mode:                "talk",
		OutputPath:          bundle.RecordingPath,
		CleanupIntermediate: !opts.keepIntermediate,
		Duration:            time.Duration(opts.durationSeconds) * time.Second,
		StopWhenRoomEmpty:   opts.stopWhenRoomEmpty,
		RoomEmptyGrace:      time.Duration(opts.roomEmptyGraceSec * float64(time.Second)),
		CallURL:             opts.callURL,
		GuestName:           opts.name,
		JoinFlags:           1,
		Insecure:            opts.insecure,
		TurnMode:            opts.turnMode,
		SimTracks:           opts.simTracks,
		SimPackets:          opts.simPackets,
	}

	if !reusedRun {
		_ = UpdateRunBundleStatus(bundle, bundleStatePreparing, "record", "")

		fmt.Fprintln(stdout, "[3/5] Recording meeting")
		if err := runRecorderApp(ctx, cfg); err != nil {
			_ = UpdateRunBundleStatus(bundle, bundleStateFailed, "record", err.Error())
			fmt.Fprintf(stderr, "record failed: %v\n", err)
			printPortableResumeHint(stderr, workspace.RootDir, outPath)
			return 1
		}
		if err := FinalizeRunBundle(bundle, RunManifest{
			SourceMode:   cfg.Mode,
			RecorderName: opts.name,
		}); err != nil {
			_ = UpdateRunBundleStatus(bundle, bundleStateFailed, "finalize", err.Error())
			fmt.Fprintf(stderr, "finalize run bundle: %v\n", err)
			printPortableResumeHint(stderr, workspace.RootDir, outPath)
			return 1
		}
	} else {
		fmt.Fprintln(stdout, "[3/5] Reusing recorded meeting")
	}

	fmt.Fprintln(stdout, "[4/5] Transcribing and preparing meeting artifact")
	input, err := resolveBuildInput(bundle.RootDir)
	if err != nil {
		fmt.Fprintf(stderr, "resolve build input: %v\n", err)
		printPortableResumeHint(stderr, workspace.RootDir, outPath)
		return 1
	}
	meetingBundle, reusedMeeting, err := reusableMeetingBundle(workspace.MeetingDir, input, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "prepare meeting bundle: %v\n", err)
		printPortableResumeHint(stderr, workspace.RootDir, outPath)
		return 1
	}
	if !reusedMeeting {
		if err := executeBuildIntoBundle(ctx, input, meetingBundle, buildOptions{device: "auto"}, stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "%v\n", err)
			printPortableResumeHint(stderr, workspace.RootDir, outPath)
			return 1
		}
	}

	fmt.Fprintln(stdout, "[5/5] Writing portable meeting file")
	if err := packMeetingBundle(ctx, meetingBundle.RootDir, outPath, portablePackOptions{
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

func validateRecordOptions(opts recordOptions) error {
	if opts.outDir == "" {
		return errors.New("--out is required")
	}
	if isPortableMeetingOutput(opts.outDir) && opts.simulate {
		return errors.New("--simulate currently only supports .run output")
	}
	if opts.simulate && opts.callURL != "" {
		return errors.New("--call and --simulate cannot be used together")
	}
	if !opts.simulate && opts.callURL == "" {
		return errors.New("either --call or --simulate is required")
	}
	if opts.durationSeconds < 0 {
		return errors.New("--duration must be >= 0")
	}
	if opts.roomEmptyGraceSec < 0 {
		return errors.New("--room-empty-grace must be >= 0")
	}
	if opts.simTracks < 1 {
		return errors.New("--sim-tracks must be >= 1")
	}
	if opts.simPackets < 1 {
		return errors.New("--sim-packets must be >= 1")
	}
	switch opts.turnMode {
	case "off", "udp-only", "all":
	default:
		return fmt.Errorf("invalid --turn-mode %q", opts.turnMode)
	}
	return nil
}

func printRootUsage(w io.Writer) {
	fmt.Fprint(w, `Cassini is the product-oriented CLI for meeting capture.

Usage:
  cassini doctor
  cassini build <run-or-mkv> --out "./Meeting.opus"
  cassini dev ...
  cassini inspect <path>
  cassini publish ./meetings --out ./site
  cassini record --call <CALL_URL> --out "./Meeting.opus"
  cassini record --simulate --out ./runs/demo.run
  cassini serve ./site

Commands:
  build    Build a browser-ready meeting artifact from a recording
  dev      Access the local harness namespace
  doctor   Validate the local environment before expensive work starts
  inspect  Inspect a run, meeting, site, or lower-level Cassini artifact
  publish  Publish one or more meeting bundles as a static site
  record   Record a meeting into one portable artifact
  serve    Serve a static Cassini site locally

Paths:
  Output paths are resolved from the current working directory.
  Portable .opus output is the normal user path; .run and .meeting outputs remain available for debugging.
  Failed portable builds keep resumable state under a hidden .cassini-work directory next to the final output.
`+"\n")
}

func runInspect(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cassini inspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini inspect ./runs/meeting.run
  cassini inspect ./meetings/meeting.meeting
  cassini inspect ./site
  cassini inspect ./archive/meeting.opus
  cassini inspect /path/to/meeting.mkv
  cassini inspect /path/to/session.json
  cassini inspect /path/to/archive.csr

`+"\n")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}

	path := fs.Arg(0)
	if bundle, ok, err := LoadRunBundle(path); err != nil {
		fmt.Fprintf(stderr, "inspect failed: %v\n", err)
		return 1
	} else if ok {
		line := fmt.Sprintf("run=%s source_mode=%s recording=%s", bundle.RootDir, bundle.Manifest.SourceMode, bundle.Manifest.Recording.Path)
		if bundle.Manifest.Recording.Format != "" {
			line += " recording_format=" + bundle.Manifest.Recording.Format
		}
		if status := bundleStateSummary(bundle.Manifest.State, bundle.Manifest.Stage); status != "" {
			line += " status=" + status
		}
		fmt.Fprintln(stdout, line)
		if strings.TrimSpace(bundle.Manifest.Error) != "" {
			fmt.Fprintf(stdout, "error=%s\n", bundle.Manifest.Error)
		}
		target, err := runBundleInspectTarget(bundle)
		if err != nil {
			if !bundleIsReady(bundle.Manifest.State) {
				fmt.Fprintf(stdout, "note=%v\n", err)
				return 0
			}
			fmt.Fprintf(stderr, "inspect failed: %v\n", err)
			return 1
		}
		if err := inspectpkg.InspectPath(stdout, target); err != nil {
			fmt.Fprintf(stderr, "inspect failed: %v\n", err)
			return 1
		}
		return 0
	}
	if bundle, ok, err := LoadMeetingBundle(path); err != nil {
		fmt.Fprintf(stderr, "inspect failed: %v\n", err)
		return 1
	} else if ok {
		inspectMeetingBundle(stdout, bundle)
		return 0
	}
	if bundle, ok, err := LoadSiteBundle(path); err != nil {
		fmt.Fprintf(stderr, "inspect failed: %v\n", err)
		return 1
	} else if ok {
		inspectSiteBundle(stdout, bundle)
		return 0
	}
	if handled, err := inspectLooseBundleDir(stdout, path); handled {
		if err != nil {
			fmt.Fprintf(stderr, "inspect failed: %v\n", err)
			return 1
		}
		return 0
	}

	if err := inspectpkg.InspectPath(stdout, path); err != nil {
		fmt.Fprintf(stderr, "inspect failed: %v\n", err)
		return 1
	}
	return 0
}

func runBundleInspectTarget(bundle LoadedRunBundle) (string, error) {
	if bundle.Manifest.Session != nil && strings.TrimSpace(bundle.Manifest.Session.Path) != "" {
		target := filepath.Join(bundle.RootDir, bundle.Manifest.Session.Path)
		if _, err := os.Stat(target); err == nil {
			return target, nil
		}
		return "", fmt.Errorf("run bundle session path missing: %s", target)
	}
	recordingPath := filepath.Join(bundle.RootDir, bundle.Manifest.Recording.Path)
	if _, err := os.Stat(recordingPath); err != nil {
		return "", fmt.Errorf("run bundle recording missing: %s", recordingPath)
	}
	return recordingPath, nil
}

func inspectMeetingBundle(out io.Writer, bundle LoadedMeetingBundle) {
	sourceKind := bundle.Manifest.SourceKind
	if strings.TrimSpace(sourceKind) == "" {
		sourceKind = "-"
	}
	sourcePath := bundle.Manifest.SourcePath
	if strings.TrimSpace(sourcePath) == "" {
		sourcePath = "-"
	}
	line := fmt.Sprintf("meeting=%s source_kind=%s source_path=%s", bundle.RootDir, sourceKind, sourcePath)
	if status := bundleStateSummary(bundle.Manifest.State, bundle.Manifest.Stage); status != "" {
		line += " status=" + status
	}
	fmt.Fprintln(out, line)
	if strings.TrimSpace(bundle.Manifest.Error) != "" {
		fmt.Fprintf(out, "error=%s\n", bundle.Manifest.Error)
	}
	keys := make([]string, 0, len(bundle.Manifest.Files))
	for key := range bundle.Manifest.Files {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(out, "file=%s path=%s\n", key, bundle.Manifest.Files[key])
	}
}

func inspectSiteBundle(out io.Writer, bundle LoadedSiteBundle) {
	sourcePath := bundle.Manifest.SourcePath
	if strings.TrimSpace(sourcePath) == "" {
		sourcePath = "-"
	}
	catalogPath := bundle.Manifest.CatalogPath
	if strings.TrimSpace(catalogPath) == "" {
		catalogPath = "-"
	}
	line := fmt.Sprintf("site=%s source_path=%s catalog=%s meetings=%d", bundle.RootDir, sourcePath, catalogPath, bundle.Manifest.MeetingCount)
	if status := bundleStateSummary(bundle.Manifest.State, bundle.Manifest.Stage); status != "" {
		line += " status=" + status
	}
	fmt.Fprintln(out, line)
	if strings.TrimSpace(bundle.Manifest.Error) != "" {
		fmt.Fprintf(out, "error=%s\n", bundle.Manifest.Error)
	}
}

func inspectLooseBundleDir(out io.Writer, path string) (bool, error) {
	root, err := filepath.Abs(path)
	if err != nil {
		return false, fmt.Errorf("resolve inspect path: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, nil
	}

	if hasFile(root, "catalog.json") || hasFile(root, "index.html") {
		fmt.Fprintf(out, "site=%s source_path=- catalog=%s meetings=%d status=legacy\n", root, looseCatalogPath(root), looseSiteMeetingCount(root))
		return true, nil
	}
	if strings.EqualFold(filepath.Ext(root), ".meeting") || hasFile(root, "meeting.webm") || hasFile(root, "transcript.words.v1.json") || hasFile(root, "manifest.json") {
		fmt.Fprintf(out, "meeting=%s source_kind=artifact source_path=- status=partial:manifest-missing\n", root)
		files := meetingBundleFiles(root)
		keys := make([]string, 0, len(files))
		for key := range files {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(out, "file=%s path=%s\n", key, files[key])
		}
		return true, nil
	}
	return false, nil
}

func hasFile(root string, name string) bool {
	info, err := os.Stat(filepath.Join(root, name))
	return err == nil && !info.IsDir()
}

func looseCatalogPath(root string) string {
	if hasFile(root, "catalog.json") {
		return "catalog.json"
	}
	return "-"
}

func looseSiteMeetingCount(root string) int {
	catalogPath := filepath.Join(root, "catalog.json")
	if hasFile(root, "catalog.json") {
		return readCatalogMeetingCount(catalogPath)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		child := filepath.Join(root, entry.Name())
		if hasFile(child, "manifest.json") || hasFile(child, "meeting.webm") || hasFile(child, "transcript.words.v1.json") {
			count++
		}
	}
	return count
}
