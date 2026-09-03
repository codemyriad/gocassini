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

	inspectpkg "gocassini/internal/inspect"
	"gocassini/internal/meetingcontext"
)

// The cassini.meetings.context.v1 contract lives in internal/meetingcontext so
// that consumers outside this CLI — the published read surface, the insight run
// seam — can name the type they are specified to consume (D-715). These aliases
// keep this package's own code and tests reading the way they did.
type meetingContext = meetingcontext.MeetingContext

const meetingContextVersion = meetingcontext.Version

func runMeetingsContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var cfg meetingsConfig
	fs := flag.NewFlagSet("cassini meetings context", flag.ContinueOnError)
	fs.SetOutput(stderr)
	registerMeetingsConnectionFlags(fs, &cfg)
	outPath := fs.String("out", "", "write to this file instead of stdout")
	asJSON := fs.Bool("json", false, "emit JSON instead of markdown")
	keepOpus := fs.String("keep-opus", "", "also keep the downloaded portable .opus at this path")
	timestamps := fs.Bool("timestamps", false, "cite each passage's start time in the markdown as MM:SS, or H:MM:SS past an hour (the JSON always carries the raw timings)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini meetings context <meeting-id>
  cassini meetings context <meeting-id> <meeting-id> [<meeting-id> ...]
  cassini meetings context <meeting-id> --json
  cassini meetings context <meeting-id> --out ./context.md

Print one or more meetings as context an agent can read: the transcript as
speaker-attributed prose, plus the generated summary when the meeting has one.

Several ids produce ONE document, holding the meetings in the order you named
them, separated by a --- rule. An id you cannot read fails the whole run: a
bundle that quietly dropped a meeting would answer a question asked of all of
them using only some of them, and look right doing it.

The transcript is derived from the recording's word timings — a published
meeting carries no separately cleaned-up transcript — so it is labelled
derived-from-words and must not be presented as edited text.

Requires ffprobe on PATH to read each meeting file's metadata.

`+"\n")
		fs.PrintDefaults()
	}

	leading, rest := splitLeadingMeetingIDs(args)
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	// Anything flag-shaped left over is a flag the caller put after an id, where
	// Go's flag package stops parsing. Refusing it is not pedantry: taken as an
	// id it would be echoed back in a failure line, and the likeliest such flag
	// is --app-password.
	if meetingsArgsLookLikeFlagsAfterPositional(fs.Args()) {
		fmt.Fprintf(stderr, "context takes meeting ids, but %d argument(s) after the ids could not be read as ids: %v\n",
			fs.NArg(), redactMeetingsArgs(fs.Args()))
		fmt.Fprintf(stderr, "hint=flags must come before the meeting ids, or the ids must come last\n")
		fs.Usage()
		return 2
	}
	ids, err := meetingContextIDs(append(leading, fs.Args()...))
	if err != nil {
		fmt.Fprintf(stderr, "context configuration error: %v\n", err)
		fs.Usage()
		return 2
	}
	if len(ids) == 0 {
		fmt.Fprintf(stderr, "context takes at least one meeting id, got none\n")
		fs.Usage()
		return 2
	}
	if err := resolveMeetingsConfig(fs, &cfg); err != nil {
		fmt.Fprintf(stderr, "context configuration error: %v\n", err)
		return 2
	}
	warnAboutInsecureTLS(stderr, cfg)

	// Trim once and use the trimmed values everywhere: gating the file open on a
	// trimmed path but the "wrote it" line on the raw one made `--out " "` print
	// the payload to stdout AND claim a file had been written.
	outFile := strings.TrimSpace(*outPath)
	keepOpusPath := strings.TrimSpace(*keepOpus)
	if keepOpusPath != "" && len(ids) > 1 {
		fmt.Fprintf(stderr, "context configuration error: --keep-opus names one file and cannot hold %d meetings; ask for one meeting id, or use `cassini meetings fetch`\n", len(ids))
		return 2
	}

	// One catalog fetch for the whole bundle, not one per id. That is the
	// semantics as much as the saving: a bundle is a set of meetings resolved
	// against a single view of what this caller may read, so it cannot be
	// assembled half out of one catalog and half out of a later one.
	client := newMeetingsClient(cfg)
	listing, err := client.fetchCatalog(ctx)
	warnAboutMeetingsSource(stderr, listing)
	if err != nil {
		return reportMeetingsError(stderr, "context", cfg, err)
	}

	bundle := meetingcontext.Bundle{Meetings: make([]meetingContext, 0, len(ids))}
	for _, meetingID := range ids {
		audioURL, entry, err := client.resolveMeetingIn(listing, meetingID)
		if err != nil {
			noteMeetingContextFailure(stderr, ids, meetingID)
			return reportMeetingsError(stderr, "context", cfg, err)
		}

		// The portable reader needs a filesystem path: it shells out to ffprobe to
		// read the OpusTags. Stage the download in a temp file — and remove it on
		// every path, since cassini has leaked temp files before. The cleanup is
		// called rather than deferred because a bundle stages its meetings in
		// turn, and deferring would keep every download on disk until the whole
		// command finished — N whole meetings, for a command whose output is text.
		opusPath, cleanup, err := client.stageMeetingOpus(ctx, audioURL, keepOpusPath)
		if err != nil {
			noteMeetingContextFailure(stderr, ids, meetingID)
			return reportMeetingsError(stderr, "context", cfg, err)
		}
		meeting, extractErr := inspectpkg.ExtractMeeting(opusPath)
		cleanup()
		if extractErr != nil {
			noteMeetingContextFailure(stderr, ids, meetingID)
			fmt.Fprintf(stderr, "context failed: read the downloaded meeting: %v\n", extractErr)
			return 1
		}

		bundle.Meetings = append(bundle.Meetings, buildMeetingContext(meetingID, meeting, entry))
	}

	out, closeOut, err := openMeetingsOutput(outFile, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "context failed: %v\n", err)
		return 1
	}
	writeErr := writeMeetingContext(out, bundle, *asJSON, meetingcontext.RenderOpts{Timestamps: *timestamps})
	if closeErr := closeOut(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		fmt.Fprintf(stderr, "context failed: write output: %v\n", writeErr)
		return 1
	}

	noteMissingSummaries(stderr, bundle)
	if outFile != "" {
		fmt.Fprintf(stdout, "meeting_context -> %s\n", outFile)
	}
	return 0
}

// splitLeadingMeetingIDs takes the run of meeting ids off the front of the
// arguments, so `context A B --json` and `context --json A B` both parse.
//
// It replaces meetingsParseArgs for this one command. That helper rotates a
// single leading positional to the end, which is right for the verbs that take
// one — but rotating a run of them past the flags reverses `context A --json B`
// into B, A, and this command's document order is the caller's order. Splitting
// instead keeps every id where the caller put it.
func splitLeadingMeetingIDs(args []string) (leading, rest []string) {
	for i, arg := range args {
		if strings.HasPrefix(arg, "-") {
			return args[:i:i], args[i:]
		}
	}
	return args[:len(args):len(args)], nil
}

// meetingContextIDs validates the ids the caller asked for and returns them in
// the order they were given, which is the order the document holds them in.
//
// A repeated id is refused rather than fetched twice: a bundle is a set of
// distinct meetings, and the same transcript twice is context spent twice on
// one meeting while reading as coverage of two.
func meetingContextIDs(args []string) ([]string, error) {
	ids := make([]string, 0, len(args))
	seen := make(map[string]struct{}, len(args))
	for _, arg := range args {
		id := strings.TrimSpace(arg)
		if id == "" {
			return nil, errors.New("the meeting id must not be empty")
		}
		if _, repeated := seen[id]; repeated {
			return nil, fmt.Errorf("meeting id %q was given more than once; a bundle carries each meeting once", id)
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

// noteMeetingContextFailure names which meeting of a bundle failed, before the
// error itself is reported.
//
// A bundle is all of its meetings or none. Without this line the 404 wording —
// deliberately identical for "absent" and "you may not read it" — does not say
// which of the ids it is about. Silent for a single id, where the id is already
// the whole command line.
func noteMeetingContextFailure(stderr io.Writer, ids []string, meetingID string) {
	if len(ids) < 2 {
		return
	}
	fmt.Fprintf(stderr, "context failed on meeting id %q; a bundle is all %d of its meetings or none\n", meetingID, len(ids))
}

// noteMissingSummaries says, per meeting, that the transcript arrived without
// one — summaries are LLM-gated, so their absence is an ordinary state a caller
// should still be told about rather than left to infer.
func noteMissingSummaries(stderr io.Writer, bundle meetingcontext.Bundle) {
	for _, meeting := range bundle.Meetings {
		if meeting.Summary.Present {
			continue
		}
		if len(bundle.Meetings) == 1 {
			fmt.Fprintf(stderr, "note=this meeting has no generated summary; the transcript is included on its own\n")
			continue
		}
		fmt.Fprintf(stderr, "note=meeting %s has no generated summary; its transcript is included on its own\n", meeting.Meeting.ID)
	}
}

// resolveMeetingIn resolves one id against a catalog listing already fetched,
// and hands back the entry as well as the audio URL.
//
// The entry is returned rather than looked up a second time because the bundle
// needs it: the room reaches the document from the catalog, not from the file
// (D-640) — the artifact carries a room id and no name, since a display name is
// editable and a sealed recording is not.
func (c *meetingsClient) resolveMeetingIn(listing meetingsListing, meetingID string) (*url.URL, meetingsCatalogEntry, error) {
	entry, err := listing.find(meetingID)
	if err != nil {
		return nil, meetingsCatalogEntry{}, err
	}
	catalogURL, err := c.catalogURL()
	if err != nil {
		return nil, entry, err
	}
	audioURL, err := resolveMeetingAudioURL(catalogURL, entry)
	return audioURL, entry, err
}

// stageMeetingOpus downloads the meeting to a path the portable reader can open.
//
// When keepPath is set the file is kept there — useful for feeding the same
// download to `cassini inspect`. Otherwise it goes to a temp file that the
// returned cleanup removes, on success and on failure alike.
func (c *meetingsClient) stageMeetingOpus(ctx context.Context, audioURL *url.URL, keepPath string) (string, func(), error) {
	if strings.TrimSpace(keepPath) != "" {
		if _, err := c.downloadMeeting(ctx, audioURL, keepPath); err != nil {
			return "", func() {}, err
		}
		return keepPath, func() {}, nil
	}

	tmpDir, err := os.MkdirTemp("", "cassini-meeting-context-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("create temp directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }
	opusPath := filepath.Join(tmpDir, "meeting.opus")
	if _, err := c.downloadMeeting(ctx, audioURL, opusPath); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return opusPath, cleanup, nil
}

// buildMeetingContext adapts an extracted meeting and its catalog entry into
// the published contract.
//
// The adaptation lives here, not in internal/meetingcontext, because both
// inputs are things only this CLI has: inspect.ExtractedMeeting is the product
// of shelling out to ffprobe, and the catalog entry is a shape of the read
// surface. Keeping them out of the contract package is what lets a consumer
// decode and render a bundle with no media tooling at all.
//
// catalogID is the id the caller asked for; it wins over the manifest's own
// meeting id so the bundle can be correlated with the catalog entry it came
// from. The manifest id is content-derived (a hash of the audio) and is not the
// id the published catalog indexes by.
func buildMeetingContext(catalogID string, meeting inspectpkg.ExtractedMeeting, entry meetingsCatalogEntry) meetingContext {
	labels := meeting.SpeakerLabels()
	segments := deriveProseSegments(meeting.Transcript.Words, labels)

	// The catalog is authoritative for the room. A catalog entry's room id is
	// kept current by the operator, while the file's is whatever it was last
	// tagged with, so the file is only the id fallback. Mutable room names live
	// exclusively in the catalog.
	roomID := strings.TrimSpace(entry.RoomID)
	if roomID == "" {
		roomID = strings.TrimSpace(meeting.Manifest.Meeting.RoomID)
	}

	input := meetingcontext.BuildInput{
		ID:                     catalogID,
		Title:                  meeting.Manifest.Meeting.Title,
		CreatedAtUTC:           meeting.Manifest.Meeting.CreatedAtUTC,
		DurationMS:             meeting.Manifest.Meeting.DurationMS,
		RoomID:                 roomID,
		RoomName:               entry.RoomName,
		SourceTranscriptID:     meeting.Transcript.TranscriptID,
		SourceTranscriptFormat: meeting.Transcript.Format,
		Language:               meeting.Transcript.Language,
		WordCount:              meeting.Transcript.WordCount,
		Speakers:               make([]meetingcontext.Speaker, 0, len(meeting.Manifest.Speakers)),
		Segments:               make([]meetingcontext.Segment, 0, len(segments)),
	}
	for _, speaker := range meeting.Manifest.Speakers {
		input.Speakers = append(input.Speakers, meetingcontext.Speaker{
			ID:    speaker.ID,
			Label: speakerLabel(speaker.ID, labels),
		})
	}
	for _, segment := range segments {
		input.Segments = append(input.Segments, meetingcontext.Segment{
			Speaker:      segment.Speaker,
			SpeakerLabel: segment.SpeakerLabel,
			StartMS:      segment.StartMS,
			EndMS:        segment.EndMS,
			Text:         segment.Text,
		})
	}
	if len(meeting.SummaryMarkdown) > 0 {
		input.Summary = meetingcontext.Summary{
			Present:  true,
			Format:   meeting.SummaryFormat(),
			Model:    meeting.SummaryModel(),
			Markdown: string(meeting.SummaryMarkdown),
		}
	}
	return meetingcontext.Build(input)
}

func writeMeetingContext(out io.Writer, bundle meetingcontext.Bundle, asJSON bool, opts meetingcontext.RenderOpts) error {
	if asJSON {
		return meetingcontext.EncodeJSON(out, bundle)
	}
	rendered, err := meetingcontext.RenderMarkdown(bundle, opts)
	if err != nil {
		return err
	}
	_, err = io.WriteString(out, rendered)
	return err
}
