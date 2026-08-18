package cassini

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	inspectpkg "gocassini/internal/inspect"
)

type publishOptions struct {
	inputPath     string
	outDir        string
	rebuildViewer bool
}

type publishSkippedBundle struct {
	Path   string
	Reason string
}

func runPublish(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var opts publishOptions

	fs := flag.NewFlagSet("cassini publish", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.outDir, "out", "", "output site directory")
	fs.BoolVar(&opts.rebuildViewer, "rebuild-viewer", false, "embed the viewer shell (index.html + assets) into the site; default is lightweight (catalog.json + meetings/ only, viewer served from the image)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini publish ./meetings --out ./site
  cassini publish ./meetings/demo.meeting --out ./site
  cassini publish ./meetings --out ./site --rebuild-viewer   # also embed the viewer shell

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
	if opts.outDir == "" {
		fmt.Fprintln(stderr, "publish configuration error: --out is required")
		return 2
	}
	// A deployment can opt an internal publish into embedding the viewer shell
	// via env without passing the flag (the operator forwards os.Environ() to
	// this subprocess). The standalone operator image sets this so its
	// self-contained nginx viewer keeps working (D-531).
	if !opts.rebuildViewer && envRebuildViewer() {
		opts.rebuildViewer = true
	}

	fmt.Fprintln(stdout, "[1/4] Preparing site bundle")
	site, err := PrepareSiteBundle(opts.outDir)
	if err != nil {
		fmt.Fprintf(stderr, "prepare site bundle: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, "[2/4] Staging meeting bundles")
	_ = UpdateSiteBundleStatus(site, bundleStatePreparing, "stage-input", "")
	stagingRoot, sourceSummary, skippedBundles, err := stagePublishInput(ctx, opts.inputPath)
	if err != nil {
		_ = UpdateSiteBundleStatus(site, bundleStateFailed, "stage-input", err.Error())
		fmt.Fprintf(stderr, "stage publish input: %v\n", err)
		fmt.Fprintf(stderr, "partial_site -> %s\n", site.RootDir)
		return 1
	}
	defer func() {
		_ = os.RemoveAll(stagingRoot)
	}()
	for _, item := range skippedBundles {
		fmt.Fprintf(stdout, "skipped %s (%s)\n", item.Path, item.Reason)
	}

	runnerPath, err := exporterRunnerPath()
	if err != nil {
		_ = UpdateSiteBundleStatus(site, bundleStateFailed, "resolve-runner", err.Error())
		fmt.Fprintf(stderr, "resolve exporter runner: %v\n", err)
		fmt.Fprintf(stderr, "partial_site -> %s\n", site.RootDir)
		return 1
	}
	_ = UpdateSiteBundleSource(site, sourceSummary, "publish")

	fmt.Fprintln(stdout, "[3/4] Publishing static site")
	runnerArgs := []string{"--source-dir", stagingRoot, "--output-dir", site.RootDir}
	if opts.rebuildViewer {
		runnerArgs = append(runnerArgs, "--rebuild-viewer")
	}
	cmd := exec.CommandContext(ctx, runnerPath, runnerArgs...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		_ = UpdateSiteBundleStatus(site, bundleStateFailed, "publish", err.Error())
		fmt.Fprintf(stderr, "publish failed: %v\n", err)
		fmt.Fprintf(stderr, "partial_site -> %s\n", site.RootDir)
		return 1
	}

	fmt.Fprintln(stdout, "[4/4] Finalizing site bundle")
	if err := FinalizeSiteBundle(site, SiteBundleManifest{SourcePath: sourceSummary}); err != nil {
		_ = UpdateSiteBundleStatus(site, bundleStateFailed, "finalize", err.Error())
		fmt.Fprintf(stderr, "finalize site bundle: %v\n", err)
		fmt.Fprintf(stderr, "partial_site -> %s\n", site.RootDir)
		return 1
	}

	fmt.Fprintf(stdout, "site -> %s\n", site.RootDir)
	return 0
}

// envRebuildViewer reports whether CASSINI_PUBLISH_REBUILD_VIEWER requests the
// legacy shell-embedding publish. It lets a container opt in without a CLI flag
// (D-531).
func envRebuildViewer() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CASSINI_PUBLISH_REBUILD_VIEWER"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func exporterRunnerPath() (string, error) {
	if override := os.Getenv("CASSINI_EXPORTER_RUNNER"); override != "" {
		resolved, err := filepath.Abs(override)
		if err != nil {
			return "", fmt.Errorf("resolve CASSINI_EXPORTER_RUNNER: %w", err)
		}
		return resolved, nil
	}
	repoRoot, err := findRepoRoot()
	if err != nil {
		return "", err
	}
	path := filepath.Join(repoRoot, "cassini-publisher", "bin", "export-static-meetings.sh")
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("exporter runner not found at %s: %w", path, err)
	}
	if info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("exporter runner is not executable: %s", path)
	}
	return path, nil
}

func stagePublishInput(ctx context.Context, inputPath string) (string, string, []publishSkippedBundle, error) {
	stagingRoot, err := os.MkdirTemp("", "cassini-publish-source-")
	if err != nil {
		return "", "", nil, fmt.Errorf("create publish staging dir: %w", err)
	}

	added := map[string]string{}
	skipped := []publishSkippedBundle{}
	// addMeeting packs a ready `.meeting` bundle into a single `.opus` portable
	// meeting file in the staging root. The static-site exporter then treats the
	// `.opus` as a portable meeting and points the catalog entry at it via
	// `audioPath` (no artifactPath). The `.meeting` directory bundle is treated
	// as transient build scratch and is never shipped verbatim.
	addMeeting := func(bundle LoadedMeetingBundle) error {
		if !bundleIsReady(bundle.Manifest.State) {
			skipped = append(skipped, publishSkippedBundle{
				Path:   bundle.RootDir,
				Reason: meetingBundleStatusReason(bundle.Manifest),
			})
			return nil
		}
		if err := validateReadyMeetingBundleContents(bundle.RootDir); err != nil {
			skipped = append(skipped, publishSkippedBundle{
				Path:   bundle.RootDir,
				Reason: fmt.Sprintf("invalid ready bundle: %v", err),
			})
			return nil
		}
		sourceDir := bundle.RootDir
		name := filepath.Base(sourceDir)
		trimmed := strings.TrimSuffix(name, ".meeting")
		if trimmed == "" {
			trimmed = name
		}
		if existing, ok := added[trimmed]; ok {
			return fmt.Errorf("meeting id %q collides between %s and %s", trimmed, existing, sourceDir)
		}
		target := filepath.Join(stagingRoot, trimmed+".opus")
		// Prefer the bundle manifest's title (the Talk room name stamped by
		// the operator) so a re-pack here names the meeting the same way the
		// durable `.opus` would — publish often runs before that `.opus`
		// exists for a just-recorded meeting.
		packTitle := strings.TrimSpace(bundle.Manifest.Title)
		if packTitle == "" {
			packTitle = trimmed
		}
		if err := packMeetingBundle(ctx, sourceDir, target, portablePackOptions{
			Title: packTitle,
			// The room comes from the same stamped manifest as the title, and
			// for the same reason: publishing a `.meeting` bundle directly is a
			// second path to a published meeting, and a meeting that reached the
			// catalog this way would otherwise be the only kind that silently
			// lost its room (D-622).
			RoomID:   strings.TrimSpace(bundle.Manifest.RoomID),
			RoomName: strings.TrimSpace(bundle.Manifest.RoomName),
		}); err != nil {
			return fmt.Errorf("pack meeting bundle %s: %w", sourceDir, err)
		}
		added[trimmed] = sourceDir
		return nil
	}

	// A `.opus` portable meeting file is a durable, ready-to-publish artifact:
	// verify it and pass it through into the staging root unchanged.
	if staged, summary, ok, err := stagePortableMeetingInput(inputPath, stagingRoot, added); err != nil {
		_ = os.RemoveAll(stagingRoot)
		return "", "", nil, err
	} else if ok {
		return staged, summary, skipped, nil
	}

	if bundle, ok, err := LoadMeetingBundle(inputPath); err != nil {
		// A corrupt manifest must not abort the publish outright: record it
		// and fall through to the directory scan so sibling bundles still ship.
		skipped = append(skipped, publishSkippedBundle{
			Path:   inputPath,
			Reason: fmt.Sprintf("unreadable bundle manifest: %v", err),
		})
	} else if ok {
		if err := addMeeting(bundle); err != nil {
			_ = os.RemoveAll(stagingRoot)
			return "", "", nil, err
		}
		if len(added) == 0 {
			_ = os.RemoveAll(stagingRoot)
			return "", "", nil, summarizeSkippedMeetings(inputPath, skipped)
		}
		return stagingRoot, bundle.RootDir, skipped, nil
	}

	root, err := filepath.Abs(inputPath)
	if err != nil {
		_ = os.RemoveAll(stagingRoot)
		return "", "", nil, fmt.Errorf("resolve publish input path: %w", err)
	}
	if strings.EqualFold(filepath.Ext(root), ".meeting") {
		_ = os.RemoveAll(stagingRoot)
		if len(skipped) > 0 {
			return "", "", nil, summarizeSkippedMeetings(root, skipped)
		}
		return "", "", nil, fmt.Errorf("meeting bundle is partial or missing cassini.json: %s", root)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		_ = os.RemoveAll(stagingRoot)
		return "", "", nil, fmt.Errorf("read publish input directory: %w", err)
	}
	// Two passes so a durable `.opus` always wins over re-packing its `.meeting`
	// sibling. The deploy sequence (D-428 before D-429) leaves both
	// current/<id>.opus and current/<id>.meeting in the publish source; both
	// would stage to meetings/<id>.opus and collide on the meeting id. Pass 1
	// stages the `.opus` files; pass 2 packs `.meeting` bundles but skips any
	// whose id a `.opus` sibling already staged. A `.meeting` whose `.opus`
	// sibling is missing or unverifiable is still packed, so no meeting is lost.
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		candidate := filepath.Join(root, entry.Name())
		// A `.opus` portable meeting sitting alongside `.meeting` bundles is a
		// durable artifact: verify and pass it through. Other loose files are
		// ignored.
		if isPortableMeetingOutput(candidate) {
			if err := addPortableMeeting(candidate, stagingRoot, added, &skipped); err != nil {
				_ = os.RemoveAll(stagingRoot)
				return "", "", nil, err
			}
		}
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(root, entry.Name())
		if bundle, ok, err := LoadMeetingBundle(candidate); err != nil {
			// One corrupt cassini.json (meeting or otherwise; the manifest is
			// parsed before the Kind filter) must not abort the publish of
			// every other bundle.
			skipped = append(skipped, publishSkippedBundle{
				Path:   candidate,
				Reason: fmt.Sprintf("unreadable bundle manifest: %v", err),
			})
		} else if ok {
			id := strings.TrimSuffix(filepath.Base(candidate), ".meeting")
			if id == "" {
				id = filepath.Base(candidate)
			}
			if existing, dup := added[id]; dup {
				// A durable `.opus` sibling (current/<id>.opus, D-428) already
				// staged this meeting; skip re-packing the `.meeting` so the
				// two siblings do not collide on meetings/<id>.opus.
				skipped = append(skipped, publishSkippedBundle{
					Path:   candidate,
					Reason: fmt.Sprintf("superseded by durable .opus sibling %s", existing),
				})
				continue
			}
			if err := addMeeting(bundle); err != nil {
				_ = os.RemoveAll(stagingRoot)
				return "", "", nil, err
			}
		} else if strings.EqualFold(filepath.Ext(candidate), ".meeting") {
			skipped = append(skipped, publishSkippedBundle{
				Path:   candidate,
				Reason: "partial bundle (missing cassini.json)",
			})
		}
	}
	if len(added) == 0 {
		_ = os.RemoveAll(stagingRoot)
		if len(skipped) > 0 {
			return "", "", nil, summarizeSkippedMeetings(root, skipped)
		}
		return "", "", nil, fmt.Errorf("no meeting bundles found in %s", root)
	}
	return stagingRoot, root, skipped, nil
}

// stagePortableMeetingInput handles the case where the publish input path is
// itself a single `.opus` portable meeting file. It returns ok=true when the
// input was a `.opus` file (whether or not staging succeeded via err), so the
// caller can short-circuit the `.meeting` bundle scanning paths.
func stagePortableMeetingInput(inputPath string, stagingRoot string, added map[string]string) (string, string, bool, error) {
	if !isPortableMeetingOutput(inputPath) {
		return "", "", false, nil
	}
	info, err := os.Stat(inputPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", true, fmt.Errorf("portable meeting input not found: %s", inputPath)
		}
		return "", "", true, fmt.Errorf("stat portable meeting input: %w", err)
	}
	if info.IsDir() {
		// A directory whose name ends in `.opus` is not a portable meeting file.
		return "", "", false, nil
	}
	skipped := []publishSkippedBundle{}
	if err := addPortableMeeting(inputPath, stagingRoot, added, &skipped); err != nil {
		return "", "", true, err
	}
	if len(added) == 0 {
		// addPortableMeeting only skips on verification failure; surface it.
		if len(skipped) > 0 {
			return "", "", true, summarizeSkippedMeetings(inputPath, skipped)
		}
		return "", "", true, fmt.Errorf("no ready portable meeting found at %s", inputPath)
	}
	return stagingRoot, inputPath, true, nil
}

// addPortableMeeting verifies a `.opus` portable meeting file and passes it
// through into the staging root unchanged. Invalid files are recorded as
// skipped (mirroring the ready/partial skip handling for `.meeting` bundles);
// meeting-id collisions are a hard error.
func addPortableMeeting(opusPath string, stagingRoot string, added map[string]string, skipped *[]publishSkippedBundle) error {
	if err := verifyPortableMeetingInput(opusPath); err != nil {
		*skipped = append(*skipped, publishSkippedBundle{
			Path:   opusPath,
			Reason: fmt.Sprintf("invalid portable meeting: %v", err),
		})
		return nil
	}
	name := filepath.Base(opusPath)
	trimmed := strings.TrimSuffix(name, filepath.Ext(name))
	if trimmed == "" {
		trimmed = name
	}
	if existing, ok := added[trimmed]; ok {
		return fmt.Errorf("meeting id %q collides between %s and %s", trimmed, existing, opusPath)
	}
	target := filepath.Join(stagingRoot, trimmed+".opus")
	if err := copyFile(opusPath, target, 0o644); err != nil {
		return fmt.Errorf("stage portable meeting %s: %w", opusPath, err)
	}
	added[trimmed] = opusPath
	return nil
}

// verifyPortableMeetingInput confirms a `.opus` file is a valid Cassini
// portable meeting (decodable embedded manifest + intact audio integrity)
// before it is shipped into the published site.
func verifyPortableMeetingInput(opusPath string) error {
	var out bytes.Buffer
	if err := inspectpkg.InspectPath(&out, opusPath); err != nil {
		return err
	}
	if !strings.Contains(out.String(), "cassini=ok") {
		return fmt.Errorf("portable meeting failed integrity check")
	}
	return nil
}

func meetingBundleStatusReason(manifest MeetingBundleManifest) string {
	status := bundleStateSummary(manifest.State, manifest.Stage)
	if status == "" {
		status = "partial"
	}
	if strings.TrimSpace(manifest.Error) != "" {
		return fmt.Sprintf("%s: %s", status, manifest.Error)
	}
	return status
}

func summarizeSkippedMeetings(root string, skipped []publishSkippedBundle) error {
	if len(skipped) == 0 {
		return fmt.Errorf("no ready meeting bundles found in %s", root)
	}
	parts := make([]string, 0, len(skipped))
	for _, item := range skipped {
		parts = append(parts, fmt.Sprintf("%s (%s)", filepath.Base(item.Path), item.Reason))
	}
	return fmt.Errorf("no ready meeting bundles found in %s; skipped %s", root, strings.Join(parts, ", "))
}

func copyFile(src string, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		_ = in.Close()
	}()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer func() {
		_ = out.Close()
	}()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
