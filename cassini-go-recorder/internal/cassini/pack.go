package cassini

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

// runPack packs an already-built `.meeting` bundle directory into a single
// portable `.opus` file. Unlike `cassini build`, it does not transcribe: it
// reuses the existing meeting artifacts (audio, transcript, summary, ...) and
// only re-encodes them into the portable container.
//
// This is the missing primitive between a built `.meeting` bundle and the
// durable `.opus` artifact. The operator uses it (via `cassini pack`) to store
// a durable `.opus` next to each promoted meeting in current/ so cloud imports
// survive once `.meeting` is no longer a publish input (D-428).
type packOptions struct {
	inputPath string
	outPath   string
	title     string
}

func runPack(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var opts packOptions

	fs := flag.NewFlagSet("cassini pack", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.outPath, "out", "", "output portable .opus file")
	fs.StringVar(&opts.title, "title", "", "override the meeting title embedded in the .opus file")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini pack ./meetings/demo.meeting --out "./My Meetings/Demo.opus"

Pack an already-built .meeting bundle into a portable .opus file without
re-transcribing. The input must be a ready .meeting bundle directory.

`+"\n")
		fs.PrintDefaults()
	}

	// Mirror build/publish: allow the input path either before or after flags.
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
	if strings.TrimSpace(opts.outPath) == "" {
		fmt.Fprintln(stderr, "pack configuration error: --out is required")
		return 2
	}
	if !isPortableMeetingOutput(opts.outPath) {
		fmt.Fprintf(stderr, "pack configuration error: --out must be a .opus file, got %s\n", opts.outPath)
		return 2
	}

	bundle, ok, err := LoadMeetingBundle(opts.inputPath)
	if err != nil {
		fmt.Fprintf(stderr, "load meeting bundle: %v\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintf(stderr, "pack input is not a meeting bundle: %s\n", opts.inputPath)
		return 1
	}
	if !bundleIsReady(bundle.Manifest.State) {
		fmt.Fprintf(stderr, "meeting bundle is not ready: %s\n", bundleStateSummary(bundle.Manifest.State, bundle.Manifest.Stage))
		return 1
	}
	if err := validateReadyMeetingBundleContents(bundle.RootDir); err != nil {
		fmt.Fprintf(stderr, "invalid ready meeting bundle: %v\n", err)
		return 1
	}

	title := strings.TrimSpace(opts.title)
	if title == "" {
		title = titleFromOutputPath(opts.outPath)
	}

	fmt.Fprintln(stdout, "[1/1] Writing portable meeting file")
	if err := packMeetingBundle(ctx, bundle.RootDir, opts.outPath, portablePackOptions{
		Title:        title,
		CreatedAtUTC: strings.TrimSpace(bundle.Manifest.CreatedAtUTC),
	}); err != nil {
		fmt.Fprintf(stderr, "write portable meeting file: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "portable_meeting -> %s\n", opts.outPath)
	return 0
}
