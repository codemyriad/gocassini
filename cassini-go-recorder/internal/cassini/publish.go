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
)

type publishOptions struct {
	inputPath string
	outDir    string
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
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini publish ./meetings --out ./site
  cassini publish ./meetings/demo.meeting --out ./site

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

	fmt.Fprintln(stdout, "[1/4] Preparing site bundle")
	site, err := PrepareSiteBundle(opts.outDir)
	if err != nil {
		fmt.Fprintf(stderr, "prepare site bundle: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, "[2/4] Staging meeting bundles")
	_ = UpdateSiteBundleStatus(site, bundleStatePreparing, "stage-input", "")
	stagingRoot, sourceSummary, skippedBundles, err := stagePublishInput(opts.inputPath)
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
	cmd := exec.CommandContext(ctx, runnerPath, "--source-dir", stagingRoot, "--output-dir", site.RootDir)
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

func stagePublishInput(inputPath string) (string, string, []publishSkippedBundle, error) {
	stagingRoot, err := os.MkdirTemp("", "cassini-publish-source-")
	if err != nil {
		return "", "", nil, fmt.Errorf("create publish staging dir: %w", err)
	}

	added := map[string]string{}
	skipped := []publishSkippedBundle{}
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
		target := filepath.Join(stagingRoot, trimmed)
		if err := copyDir(sourceDir, target); err != nil {
			return fmt.Errorf("copy meeting bundle %s: %w", sourceDir, err)
		}
		added[trimmed] = sourceDir
		return nil
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

func copyDir(src string, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("create target directory: %w", err)
	}
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFile(path, target, info.Mode())
	})
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
