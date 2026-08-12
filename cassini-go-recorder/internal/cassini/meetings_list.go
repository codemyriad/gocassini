package cassini

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"unicode"
)

func runMeetingsList(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var cfg meetingsConfig
	fs := flag.NewFlagSet("cassini meetings list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	registerMeetingsConnectionFlags(fs, &cfg)
	asJSON := fs.Bool("json", false, "emit the catalog as JSON instead of one line per meeting")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini meetings list
  cassini meetings list --json

List the meetings your Nextcloud account may read. Prints nothing but a count
when the account may read none — which is also what a mis-provisioned
recordings folder looks like.

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
		fmt.Fprintf(stderr, "list does not accept positional arguments: %v\n", redactMeetingsArgs(fs.Args()))
		fs.Usage()
		return 2
	}
	if err := resolveMeetingsConfig(fs, &cfg); err != nil {
		fmt.Fprintf(stderr, "list configuration error: %v\n", err)
		return 2
	}

	client := newMeetingsClient(cfg)
	listing, err := client.fetchCatalog(ctx)
	if err != nil {
		return reportMeetingsError(stderr, "list", cfg, err)
	}

	if *asJSON {
		if err := writeMeetingsCatalogJSON(stdout, listing); err != nil {
			fmt.Fprintf(stderr, "list failed: write JSON: %v\n", err)
			return 1
		}
		return 0
	}

	entries := listing.Entries()
	fmt.Fprintf(stdout, "meetings=%d caller=%s source=%s\n", len(entries), cfg.user, listing.Source)
	if listing.Source == "unknown" {
		fmt.Fprintf(stderr, "warning=response carried no %s header, so these bytes did not come from Nextcloud Files; per-caller access control may not be in effect\n", meetingsSourceHeader)
	}
	if listing.Skipped > 0 {
		fmt.Fprintf(stderr, "warning=%d catalog entr(y/ies) had no id and were skipped, so this list may be incomplete\n", listing.Skipped)
	}
	if len(entries) == 0 {
		// The app answers 200 with an empty list both when the caller really
		// has no readable recordings and when the recordings substrate is
		// mis-provisioned or unreachable. It cannot tell the caller which,
		// by design — so neither can we, and this is not a failure.
		fmt.Fprintln(stdout, "note=no recordings are visible to this account; this is also what a mis-provisioned recordings folder looks like")
		return 0
	}
	for _, entry := range entries {
		fmt.Fprintf(stdout, "meeting=%s date=%s title=%s speakers=%s segments=%s duration_ms=%s fetchable=%s\n",
			blankMeetingsDash(entry.ID),
			blankMeetingsDash(entry.DateLabel),
			blankMeetingsDash(entry.Title),
			meetingsCount(entry.SpeakerCount),
			meetingsCount(entry.SegmentCount),
			meetingsCount64(entry.DigestDurationMS),
			meetingsYesNo(strings.TrimSpace(entry.AudioPath) != ""),
		)
	}
	return 0
}

// blankMeetingsDash renders a field for the one-record-per-line output: empty
// becomes "-", matching how inspect prints key=value records, and control
// characters are flattened to spaces.
//
// The flattening is not cosmetic. Titles come from the server, and a title
// containing a newline would let a catalog forge additional "meeting=" lines
// that a caller parsing this output would read as real recordings; an escape
// sequence could rewrite the terminal. Neither can survive into a line-oriented
// format.
func blankMeetingsDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return oneLineField(value)
}

// oneLineField replaces every control character in value with a space, so the
// result cannot break out of the line it is printed on.
func oneLineField(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' || unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
}

// meetingsCount renders an absent count as "-" rather than a misleading 0.
func meetingsCount(value int) string {
	if value <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d", value)
}

func meetingsCount64(value int64) string {
	if value <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d", value)
}

func meetingsYesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
