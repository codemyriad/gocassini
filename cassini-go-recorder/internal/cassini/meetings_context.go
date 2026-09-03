package cassini

import (
	"context"
	"encoding/json"
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
	local := fs.Bool("local", false, "read portable .opus files named on the command line instead of fetching meeting ids from Nextcloud")
	catalogPath := fs.String("catalog", "", "with --local, the catalog.json to read each meeting's room from (matched on the meeting id)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini meetings context <meeting-id>
  cassini meetings context <meeting-id> <meeting-id> [<meeting-id> ...]
  cassini meetings context <meeting-id> --json
  cassini meetings context <meeting-id> --out ./context.md
  cassini meetings context --local ./meetings/<meeting-id>.opus [...]
  cassini meetings context --local --catalog ./catalog.json ./meetings/*.opus

Print one or more meetings as context an agent can read: the transcript as
speaker-attributed prose, plus the generated summary when the meeting has one.

Several ids produce ONE document, holding the meetings in the order you named
them, separated by a --- rule. An id you cannot read fails the whole run: a
bundle that quietly dropped a meeting would answer a question asked of all of
them using only some of them, and look right doing it.

--local reads portable .opus files already on disk instead of fetching ids from
Nextcloud, the way `+"`cassini meetings summarize`"+` does, and needs none of the
connection flags. Each meeting's id is its file's basename without the .opus,
which is how a published archive names one (meetings/<meeting-id>.opus). The
room is not in the file — the catalog owns it — so pass --catalog to read it
from a catalog.json; without one a meeting renders with whatever room id its
file carries and no room name.

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
	// Anything flag-shaped left over is a flag the caller put after a positional
	// argument, where Go's flag package stops parsing. Refusing it is not
	// pedantry: taken as an id it would be echoed back in a failure line, and the
	// likeliest such flag is --app-password.
	if meetingsArgsLookLikeFlagsAfterPositional(fs.Args()) {
		fmt.Fprintf(stderr, "context takes meeting ids, but %d argument(s) after the ids could not be read as ids: %v\n",
			fs.NArg(), redactMeetingsArgs(fs.Args()))
		fmt.Fprintf(stderr, "hint=flags must come before the meeting ids, or the ids must come last\n")
		fs.Usage()
		return 2
	}
	positional := append(leading, fs.Args()...)

	// Trim once and use the trimmed values everywhere: gating the file open on a
	// trimmed path but the "wrote it" line on the raw one made `--out " "` print
	// the payload to stdout AND claim a file had been written.
	outFile := strings.TrimSpace(*outPath)
	keepOpusPath := strings.TrimSpace(*keepOpus)
	catalogFile := strings.TrimSpace(*catalogPath)

	var bundle meetingcontext.Bundle
	var code int
	if *local {
		bundle, code = localMeetingContexts(fs, positional, catalogFile, keepOpusPath, stderr)
	} else {
		bundle, code = remoteMeetingContexts(ctx, fs, &cfg, positional, catalogFile, keepOpusPath, stderr)
	}
	if code != 0 {
		return code
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

// remoteMeetingContexts builds the bundle by fetching each meeting id from
// Nextcloud as the calling user, which is what every other `cassini meetings`
// verb does.
//
// The non-zero int is the process exit code, already reported to stderr.
func remoteMeetingContexts(ctx context.Context, fs *flag.FlagSet, cfg *meetingsConfig, args []string, catalogFile, keepOpusPath string, stderr io.Writer) (meetingcontext.Bundle, int) {
	// A catalog on disk is meaningless here: the caller's own filtered catalog is
	// what an id is resolved against, and reading the room out of a second one
	// would let a stale file contradict it.
	if catalogFile != "" {
		fmt.Fprintf(stderr, "context configuration error: --catalog reads the room for meetings named as local files; without --local the room comes from the catalog Nextcloud serves you\n")
		return meetingcontext.Bundle{}, 2
	}
	ids, err := meetingContextIDs(args)
	if err != nil {
		fmt.Fprintf(stderr, "context configuration error: %v\n", err)
		fs.Usage()
		return meetingcontext.Bundle{}, 2
	}
	if len(ids) == 0 {
		fmt.Fprintf(stderr, "context takes at least one meeting id, got none\n")
		fs.Usage()
		return meetingcontext.Bundle{}, 2
	}
	if err := resolveMeetingsConfig(fs, cfg); err != nil {
		fmt.Fprintf(stderr, "context configuration error: %v\n", err)
		return meetingcontext.Bundle{}, 2
	}
	warnAboutInsecureTLS(stderr, *cfg)
	if keepOpusPath != "" && len(ids) > 1 {
		fmt.Fprintf(stderr, "context configuration error: --keep-opus names one file and cannot hold %d meetings; ask for one meeting id, or use `cassini meetings fetch`\n", len(ids))
		return meetingcontext.Bundle{}, 2
	}

	// One catalog fetch for the whole bundle, not one per id. That is the
	// semantics as much as the saving: a bundle is a set of meetings resolved
	// against a single view of what this caller may read, so it cannot be
	// assembled half out of one catalog and half out of a later one.
	client := newMeetingsClient(*cfg)
	listing, err := client.fetchCatalog(ctx)
	warnAboutMeetingsSource(stderr, listing)
	if err != nil {
		return meetingcontext.Bundle{}, reportMeetingsError(stderr, "context", *cfg, err)
	}

	bundle := meetingcontext.Bundle{Meetings: make([]meetingContext, 0, len(ids))}
	for _, meetingID := range ids {
		audioURL, entry, err := client.resolveMeetingIn(listing, meetingID)
		if err != nil {
			noteMeetingContextFailure(stderr, ids, meetingID)
			return meetingcontext.Bundle{}, reportMeetingsError(stderr, "context", *cfg, err)
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
			return meetingcontext.Bundle{}, reportMeetingsError(stderr, "context", *cfg, err)
		}
		meeting, extractErr := inspectpkg.ExtractMeeting(opusPath)
		cleanup()
		if extractErr != nil {
			noteMeetingContextFailure(stderr, ids, meetingID)
			fmt.Fprintf(stderr, "context failed: read the downloaded meeting: %v\n", extractErr)
			return meetingcontext.Bundle{}, 1
		}

		bundle.Meetings = append(bundle.Meetings, buildMeetingContext(meetingID, meeting, entry))
	}
	return bundle, 0
}

// localMeetingContexts builds the bundle from portable .opus files already on
// disk, reading no Nextcloud at all — the same shape `cassini meetings
// summarize` has, and for a related reason: the file is the input, and getting
// it there is somebody else's job.
//
// It exists because the operator serves the app a context bundle over
// `published/meetings-context` (D-717). The operator can fetch a recording from
// Nextcloud Files as the caller — that is how the read proxy already works —
// but it holds no app password of the caller's, so the id path is closed to it.
// This is the seam that keeps one implementation of the document instead of a
// second one written in the operator's module, which could not import this one.
//
// A meeting's id is its file's basename without the .opus, because that is how
// a published archive names a recording (meetings/<id>.opus), so a caller that
// downloaded the archive's files keeps the archive's ids for free.
//
// The room is deliberately NOT read from the file: a catalog entry's room id is
// kept current by the operator and a room's mutable display name lives only in
// the catalog (D-640), which is why an id run reads both from there. --catalog
// hands this run the same entry, so a local run over an archive's files
// produces the same bytes as an id run over the same meetings. Without it a
// meeting renders with the room id its file was tagged with and no room name —
// exactly what an id run produces for a meeting whose catalog entry says
// nothing about the room.
func localMeetingContexts(fs *flag.FlagSet, paths []string, catalogFile, keepOpusPath string, stderr io.Writer) (meetingcontext.Bundle, int) {
	if keepOpusPath != "" {
		fmt.Fprintf(stderr, "context configuration error: --keep-opus keeps a downloaded meeting, and --local downloads nothing\n")
		return meetingcontext.Bundle{}, 2
	}
	if len(paths) == 0 {
		fmt.Fprintf(stderr, "context --local takes at least one portable .opus meeting file, got none\n")
		fs.Usage()
		return meetingcontext.Bundle{}, 2
	}
	for _, path := range paths {
		if !isPortableMeetingOutput(strings.TrimSpace(path)) {
			fmt.Fprintf(stderr, "context configuration error: %s is not a .opus file\n", path)
			return meetingcontext.Bundle{}, 2
		}
	}
	ids, err := meetingContextFileIDs(paths)
	if err != nil {
		fmt.Fprintf(stderr, "context configuration error: %v\n", err)
		return meetingcontext.Bundle{}, 2
	}

	entries := map[string]meetingsCatalogEntry{}
	if catalogFile != "" {
		entries, err = loadMeetingsCatalogFile(catalogFile)
		if err != nil {
			fmt.Fprintf(stderr, "context failed: %v\n", err)
			return meetingcontext.Bundle{}, 1
		}
	}

	bundle := meetingcontext.Bundle{Meetings: make([]meetingContext, 0, len(paths))}
	for i, path := range paths {
		meetingID := ids[i]
		entry, known := entries[meetingID]
		// A --catalog that does not name a meeting is a mismatched pair of
		// inputs, not a meeting without a room: the run would silently produce a
		// document that differs from the same meeting read by id, and nothing in
		// it would say so.
		if catalogFile != "" && !known {
			fmt.Fprintf(stderr, "context failed: %s has no entry for meeting %q, so its room cannot be read; pass the catalog that lists it, or drop --catalog\n", catalogFile, meetingID)
			return meetingcontext.Bundle{}, 1
		}
		meeting, extractErr := inspectpkg.ExtractMeeting(strings.TrimSpace(path))
		if extractErr != nil {
			noteMeetingContextFailure(stderr, ids, meetingID)
			fmt.Fprintf(stderr, "context failed: read %s: %v\n", path, extractErr)
			return meetingcontext.Bundle{}, 1
		}
		bundle.Meetings = append(bundle.Meetings, buildMeetingContext(meetingID, meeting, entry))
	}
	return bundle, 0
}

// meetingContextFileIDs derives each local file's meeting id from its name, in
// the order the caller named the files.
//
// The same id twice is refused for the reason meetingContextIDs refuses it —
// the same transcript twice is context spent twice on one meeting while reading
// as coverage of two — and here it also catches the likelier mistake of naming
// the same recording out of two directories.
func meetingContextFileIDs(paths []string) ([]string, error) {
	ids := make([]string, 0, len(paths))
	seen := make(map[string]string, len(paths))
	for _, raw := range paths {
		path := strings.TrimSpace(raw)
		id := strings.TrimSpace(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
		if id == "" {
			return nil, fmt.Errorf("%q has no meeting id in its name; a meeting file is named <meeting-id>.opus", raw)
		}
		if first, repeated := seen[id]; repeated {
			return nil, fmt.Errorf("%s and %s are both meeting %q; a bundle carries each meeting once", first, path, id)
		}
		seen[id] = path
		ids = append(ids, id)
	}
	return ids, nil
}

// loadMeetingsCatalogFile reads a catalog.json off disk and indexes it by
// meeting id, so a --local run can read each meeting's room from the same place
// an id run reads it from.
//
// It goes through the same version gate and the same entry decoding as a
// fetched catalog: a file this cannot read is refused rather than treated as a
// catalog with nothing in it, which would render every meeting roomless and say
// nothing about why.
func loadMeetingsCatalogFile(path string) (map[string]meetingsCatalogEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read catalog %s: %w", path, err)
	}
	defer file.Close()

	// Bounded while reading, not measured afterwards: a --catalog is a path
	// somebody handed this process, and a cap applied to bytes already in memory
	// is not a cap at all. One byte past it, like the fetched catalog, so hitting
	// the limit stays detectable instead of surfacing as truncated JSON.
	body, err := io.ReadAll(io.LimitReader(file, maxCatalogBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read catalog %s: %w", path, err)
	}
	if len(body) > maxCatalogBytes {
		return nil, fmt.Errorf("catalog %s is larger than %d MiB, which no real meeting list is", path, maxCatalogBytes>>20)
	}
	var catalog meetingsCatalog
	if err := json.Unmarshal(body, &catalog); err != nil {
		return nil, fmt.Errorf("parse catalog %s: %w", path, err)
	}
	if catalog.Version != meetingsCatalogVersion {
		return nil, fmt.Errorf("catalog %s declares version %q (this build reads %q)", path, catalog.Version, meetingsCatalogVersion)
	}
	entries := make(map[string]meetingsCatalogEntry, len(catalog.Meetings))
	for i, raw := range catalog.Meetings {
		var entry meetingsCatalogEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			return nil, fmt.Errorf("parse catalog entry %d of %s: %w", i, path, err)
		}
		entry.ID = strings.TrimSpace(entry.ID)
		if entry.ID == "" {
			continue
		}
		// First wins, matching the fetched catalog's find(): ids are producer
		// data and nothing guarantees they are unique.
		if _, taken := entries[entry.ID]; !taken {
			entries[entry.ID] = entry
		}
	}
	return entries, nil
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
