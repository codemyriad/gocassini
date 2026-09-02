package cassini

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func runMeetingsFetch(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var cfg meetingsConfig
	fs := flag.NewFlagSet("cassini meetings fetch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	registerMeetingsConnectionFlags(fs, &cfg)
	outPath := fs.String("out", "", "required path to write the portable .opus to")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini meetings fetch <meeting-id> --out "./Meeting.opus"

Download one meeting's portable .opus — audio plus embedded transcript and
summary in a single file. Use `+"`cassini meetings list`"+` to find the id.

`+"\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(meetingsParseArgs(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(stderr, "fetch takes exactly one meeting id, got %d arguments: %v\n", fs.NArg(), redactMeetingsArgs(fs.Args()))
		// Go's flag package stops parsing at the first non-flag argument, so a
		// flag after the id lands here as a surplus positional rather than as
		// an unknown-flag error. Say which ordering works.
		if meetingsArgsLookLikeFlagsAfterPositional(fs.Args()) {
			fmt.Fprintf(stderr, "hint=flags must come before the meeting id, or the id must come last\n")
		}
		fs.Usage()
		return 2
	}
	meetingID := strings.TrimSpace(fs.Arg(0))
	if meetingID == "" {
		fmt.Fprintf(stderr, "fetch configuration error: the meeting id must not be empty\n")
		return 2
	}
	if strings.TrimSpace(*outPath) == "" {
		fmt.Fprintf(stderr, "fetch configuration error: --out is required\n")
		return 2
	}
	if err := resolveMeetingsConfig(fs, &cfg); err != nil {
		fmt.Fprintf(stderr, "fetch configuration error: %v\n", err)
		return 2
	}
	warnAboutInsecureTLS(stderr, cfg)

	client := newMeetingsClient(cfg)
	audioURL, listing, err := client.resolveMeeting(ctx, meetingID)
	warnAboutMeetingsSource(stderr, listing)
	if err != nil {
		return reportMeetingsError(stderr, "fetch", cfg, err)
	}

	written, err := client.downloadMeeting(ctx, audioURL, *outPath)
	if err != nil {
		return reportMeetingsError(stderr, "fetch", cfg, err)
	}

	fmt.Fprintf(stdout, "portable_meeting -> %s bytes=%d\n", *outPath, written)
	return 0
}

// resolveMeeting looks an id up in the caller's catalog and returns the URL of
// its portable .opus.
//
// Going through the catalog rather than guessing the asset path is what makes an
// id the caller may not read indistinguishable from one that does not exist:
// the catalog is already filtered to the caller, so the id is simply not there.
func (c *meetingsClient) resolveMeeting(ctx context.Context, meetingID string) (*url.URL, meetingsListing, error) {
	// Same loud path as `list`. It matters more here, not less: an unreachable
	// archive yields an empty listing, `find` then reports the id as absent, and
	// reportMeetingsError phrases that as "no recording you can read at that id"
	// with a permissions hint — an outage described as a denial (D-701).
	listing, _, err := c.fetchMeetings(ctx, meetingsFilter{})
	if err != nil {
		return nil, meetingsListing{}, err
	}
	entry, err := listing.find(meetingID)
	if err != nil {
		return nil, listing, err
	}
	catalogURL, err := c.catalogURL()
	if err != nil {
		return nil, listing, err
	}
	audioURL, err := resolveMeetingAudioURL(catalogURL, entry)
	return audioURL, listing, err
}

// downloadMeeting streams the .opus to outPath and returns the byte count.
//
// It writes to a sibling temp file and renames on success, so an interrupted
// transfer never leaves a truncated .opus at the destination that a later
// extract would report as a corrupt meeting. The temp file is removed on every
// failure path.
func (c *meetingsClient) downloadMeeting(ctx context.Context, audioURL *url.URL, outPath string) (int64, error) {
	// Check the destination before spending a download on it. Without this, a
	// --out naming an existing directory (or ending in a slash) only fails at the
	// final rename, reporting "file exists" about a path the caller never named.
	if err := checkMeetingOutPath(outPath); err != nil {
		return 0, err
	}

	resp, err := c.get(ctx, audioURL, c.stream)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	dir := filepath.Dir(outPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(outPath)+".partial-*")
	if err != nil {
		return 0, fmt.Errorf("create temp file next to %s: %w", outPath, err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		tmp.Close()
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()

	written, err := io.Copy(tmp, resp.Body)
	if err != nil {
		return 0, fmt.Errorf("download %s: %w", audioURL.Redacted(), err)
	}
	if err := tmp.Close(); err != nil {
		return 0, fmt.Errorf("finish writing %s: %w", tmpName, err)
	}
	// A 200 with an empty body is not a meeting. Committing it would report
	// success for a 0-byte .opus that only fails later, when something tries to
	// read it — which is the same class of half-written file the temp-and-rename
	// dance exists to prevent.
	if written == 0 {
		return 0, fmt.Errorf("the published recording at %s is empty (0 bytes), so there is nothing to save", audioURL.Redacted())
	}
	// Deliberately NOT widened to 0644. os.CreateTemp makes the file readable by
	// its owner only, and that is the right default here: this file is a private
	// meeting's audio and transcript, and the whole point of the surface is that
	// Nextcloud decides who may read it. Publishing it to every account on a
	// shared host would undo that, and forcing a mode would override a caller who
	// set a restrictive umask on purpose. A caller who wants it wider can chmod.
	if err := os.Rename(tmpName, outPath); err != nil {
		return 0, fmt.Errorf("move the downloaded meeting into place at %s: %w", outPath, err)
	}
	committed = true
	return written, nil
}

// redactMeetingsArgs returns args with any app-password value replaced.
//
// Surplus arguments get echoed back so the caller can see what was
// misinterpreted — but a flag placed after the meeting id is exactly what lands
// there, and `--app-password <secret>` is the likeliest such flag. Echoing it
// verbatim would print the credential to the terminal and into any captured
// output, which is the one thing this surface must never do.
func redactMeetingsArgs(args []string) []string {
	const flagName = "app-password"
	redacted := make([]string, 0, len(args))
	hideNext := false
	for _, arg := range args {
		if hideNext {
			redacted = append(redacted, "<redacted>")
			hideNext = false
			continue
		}
		trimmed := strings.TrimLeft(arg, "-")
		switch {
		case trimmed == flagName:
			// The value is the next argument.
			redacted = append(redacted, arg)
			hideNext = true
		case strings.HasPrefix(trimmed, flagName+"="):
			redacted = append(redacted, arg[:len(arg)-len(trimmed)]+flagName+"=<redacted>")
		default:
			redacted = append(redacted, arg)
		}
	}
	return redacted
}

// checkMeetingOutPath rejects a destination that cannot receive a file, naming
// what is wrong with it rather than letting the failure surface later as a rename
// error about a temp path.
func checkMeetingOutPath(outPath string) error {
	if strings.HasSuffix(outPath, string(os.PathSeparator)) {
		return fmt.Errorf("--out %s names a directory; give the path of the .opus file to write", outPath)
	}
	info, err := os.Stat(outPath)
	if err == nil && info.IsDir() {
		return fmt.Errorf("--out %s is an existing directory; give the path of the .opus file to write", outPath)
	}
	return nil
}

// meetingsParseArgs moves a leading meeting id to the end so both
// `fetch <id> --out x` and `fetch --out x <id>` parse. Go's flag package stops
// at the first non-flag argument, so the id has to come last; mirrors build,
// pack and publish, which accept their one positional argument either way.
//
// Only a leading argument is moved. Scanning for the first non-flag anywhere
// would mistake a flag's value ("--out x") for the positional.
func meetingsParseArgs(args []string) []string {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return append(append([]string{}, args[1:]...), args[0])
	}
	return args
}

// meetingsArgsLookLikeFlagsAfterPositional reports whether the leftover
// arguments contain something flag-shaped, which means the caller put a flag
// after the meeting id and flag parsing stopped early.
func meetingsArgsLookLikeFlagsAfterPositional(leftover []string) bool {
	for _, arg := range leftover {
		if strings.HasPrefix(arg, "-") {
			return true
		}
	}
	return false
}
