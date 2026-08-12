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
		fmt.Fprintf(stderr, "fetch takes exactly one meeting id, got %d arguments: %v\n", fs.NArg(), fs.Args())
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

	client := newMeetingsClient(cfg)
	audioURL, err := client.resolveMeeting(ctx, meetingID)
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
func (c *meetingsClient) resolveMeeting(ctx context.Context, meetingID string) (*url.URL, error) {
	listing, err := c.fetchCatalog(ctx)
	if err != nil {
		return nil, err
	}
	entry, err := listing.find(meetingID)
	if err != nil {
		return nil, err
	}
	catalogURL, err := c.catalogURL()
	if err != nil {
		return nil, err
	}
	return resolveMeetingAudioURL(catalogURL, entry)
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
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return 0, fmt.Errorf("set permissions on %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, outPath); err != nil {
		return 0, fmt.Errorf("move the downloaded meeting into place at %s: %w", outPath, err)
	}
	committed = true
	return written, nil
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
