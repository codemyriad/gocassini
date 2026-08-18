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
	roomToken string
	roomName  string
}

func runPack(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var opts packOptions

	fs := flag.NewFlagSet("cassini pack", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.outPath, "out", "", "output portable .opus file")
	fs.StringVar(&opts.title, "title", "", "override the meeting title embedded in the .opus file")
	fs.StringVar(&opts.roomToken, "room-token", "", "token of the conversation this meeting was recorded in (never stored; only a one-way derivation of it is)")
	fs.StringVar(&opts.roomName, "room-name", "", "display name of the conversation this meeting was recorded in")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini pack ./meetings/demo.meeting --out "./My Meetings/Demo.opus"
  cassini pack ./meetings/demo.meeting --out ./Demo.opus \
    --room-token a7bc3k9x --room-name "Weekly Sync"

Pack an already-built .meeting bundle into a portable .opus file without
re-transcribing. The input must be a ready .meeting bundle directory.

--room-token and --room-name record which conversation the recording came from,
so a published meeting can be grouped and filtered by room. Both default to
whatever the bundle manifest carries, and both are optional — a meeting with no
known room is packed without them rather than with a guess.

The token is NOT stored. For a public conversation it is a join link, so what
lands in the file is a deterministic one-way derivation of it that cannot be
turned back into a token. Set CASSINI_ROOM_ID_PEPPER to a stable deployment-wide
secret to make that derivation resistant to being enumerated offline; without it
the id still hides the token from anyone reading a catalog, but not from someone
determined to recover it. Changing the pepper changes every id, so choose it
once.

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

	// Precedence: explicit --title, then the bundle manifest's title (stamped
	// by the operator from the Talk room name), then the output file name.
	title := strings.TrimSpace(opts.title)
	if title == "" {
		title = strings.TrimSpace(bundle.Manifest.Title)
	}
	if title == "" {
		title = titleFromOutputPath(opts.outPath)
	}

	// The room follows the same flag-then-bundle precedence as the title, but
	// stops there: a title has a sensible last resort (the output file name),
	// while a room does not. An unknown room stays unknown.
	roomToken := strings.TrimSpace(opts.roomToken)
	if roomToken == "" {
		roomToken = strings.TrimSpace(bundle.Manifest.RoomToken)
	}
	roomName := strings.TrimSpace(opts.roomName)
	if roomName == "" {
		roomName = strings.TrimSpace(bundle.Manifest.RoomName)
	}

	fmt.Fprintln(stdout, "[1/1] Writing portable meeting file")
	if err := packMeetingBundle(ctx, bundle.RootDir, opts.outPath, portablePackOptions{
		Title:        title,
		CreatedAtUTC: strings.TrimSpace(bundle.Manifest.CreatedAtUTC),
		RoomToken:    roomToken,
		RoomName:     roomName,
	}); err != nil {
		fmt.Fprintf(stderr, "write portable meeting file: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "portable_meeting -> %s\n", opts.outPath)
	return 0
}
