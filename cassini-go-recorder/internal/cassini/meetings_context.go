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
)

// meetingContextVersion identifies the context bundle's shape. Bumping it is a
// breaking change for every consumer, so it is versioned from the start.
const meetingContextVersion = "cassini.meetings.context.v1"

// meetingContext is one meeting rendered as agent-readable context: what the
// meeting was, who spoke, the transcript as prose, and the summary if one was
// generated.
type meetingContext struct {
	Version  string                  `json:"version"`
	Meeting  meetingContextMeeting   `json:"meeting"`
	Speakers []meetingContextSpeaker `json:"speakers,omitempty"`

	// TranscriptSource says how the prose was produced. Always
	// "derived-from-words" today, and deliberately explicit: an
	// operator-published .opus carries no LLM-cleaned readable transcript at
	// all — only raw-ASR words — so a consumer must not present this text as
	// one. (Re-exported copies in the static-site export tree do carry a
	// readable transcript; this command reads the default raw-ASR entry
	// regardless.)
	TranscriptSource string `json:"transcriptSource"`
	// SourceTranscriptID and SourceTranscriptFormat identify the transcript the
	// prose was derived from, so a claim can be traced back to the file.
	SourceTranscriptID     string `json:"sourceTranscriptId,omitempty"`
	SourceTranscriptFormat string `json:"sourceTranscriptFormat,omitempty"`
	Language               string `json:"language,omitempty"`
	WordCount              int    `json:"wordCount"`

	Segments []meetingContextSegment `json:"segments"`
	Summary  meetingContextSummary   `json:"summary"`
}

type meetingContextMeeting struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	CreatedAtUTC string `json:"createdAtUtc,omitempty"`
	DurationMS   int64  `json:"durationMs,omitempty"`
	// RoomID and RoomName say which conversation this meeting came out of
	// (D-640). Both optional — a meeting with no room is an ordinary state —
	// and both read from the CATALOG entry rather than from the file, because
	// the room's current name lives only in the catalog.
	//
	// They are here because "which room was this?" is a question an agent
	// summarising a meeting routinely needs answered, and the command the docs
	// point it at for reading a meeting used to hand back a document that could
	// not answer it.
	RoomID   string `json:"roomId,omitempty"`
	RoomName string `json:"roomName,omitempty"`
}

type meetingContextSpeaker struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type meetingContextSegment struct {
	Speaker      string `json:"speaker,omitempty"`
	SpeakerLabel string `json:"speakerLabel,omitempty"`
	StartMS      int64  `json:"startMs"`
	EndMS        int64  `json:"endMs"`
	Text         string `json:"text"`
}

// meetingContextSummary carries the generated summary. Present is explicit so a
// consumer can tell "no summary was generated" from "the summary was empty" —
// summaries are LLM-gated, and a deployment with no summariser configured
// publishes transcripts without them.
type meetingContextSummary struct {
	Present  bool   `json:"present"`
	Format   string `json:"format,omitempty"`
	Model    string `json:"model,omitempty"`
	Markdown string `json:"markdown,omitempty"`
}

func runMeetingsContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var cfg meetingsConfig
	fs := flag.NewFlagSet("cassini meetings context", flag.ContinueOnError)
	fs.SetOutput(stderr)
	registerMeetingsConnectionFlags(fs, &cfg)
	outPath := fs.String("out", "", "write to this file instead of stdout")
	asJSON := fs.Bool("json", false, "emit JSON instead of markdown")
	keepOpus := fs.String("keep-opus", "", "also keep the downloaded portable .opus at this path")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini meetings context <meeting-id>
  cassini meetings context <meeting-id> --json
  cassini meetings context <meeting-id> --out ./context.md

Print one meeting as context an agent can read: the transcript as
speaker-attributed prose, plus the generated summary when the meeting has one.

The transcript is derived from the recording's word timings — a published
meeting carries no separately cleaned-up transcript — so it is labelled
derived-from-words and must not be presented as edited text.

Requires ffprobe on PATH to read the meeting file's metadata.

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
		fmt.Fprintf(stderr, "context takes exactly one meeting id, got %d arguments: %v\n", fs.NArg(), redactMeetingsArgs(fs.Args()))
		if meetingsArgsLookLikeFlagsAfterPositional(fs.Args()) {
			fmt.Fprintf(stderr, "hint=flags must come before the meeting id, or the id must come last\n")
		}
		fs.Usage()
		return 2
	}
	meetingID := strings.TrimSpace(fs.Arg(0))
	if meetingID == "" {
		fmt.Fprintf(stderr, "context configuration error: the meeting id must not be empty\n")
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

	client := newMeetingsClient(cfg)
	audioURL, listing, err := client.resolveMeeting(ctx, meetingID)
	warnAboutMeetingsSource(stderr, listing)
	if err != nil {
		return reportMeetingsError(stderr, "context", cfg, err)
	}

	// The portable reader needs a filesystem path: it shells out to ffprobe to
	// read the OpusTags. Stage the download in a temp file — and remove it on
	// every path, since cassini has leaked temp files before.
	opusPath, cleanup, err := client.stageMeetingOpus(ctx, audioURL, keepOpusPath)
	if err != nil {
		return reportMeetingsError(stderr, "context", cfg, err)
	}
	defer cleanup()

	meeting, err := inspectpkg.ExtractMeeting(opusPath)
	if err != nil {
		fmt.Fprintf(stderr, "context failed: read the downloaded meeting: %v\n", err)
		return 1
	}

	// The room reaches the bundle from the CATALOG entry, not from the file
	// (D-640). The artifact carries a room id and no name — a display name is
	// editable and a sealed recording is not — so the entry is the only place
	// both halves exist. resolveMeeting already found it; look it up again
	// rather than widen its signature, and treat a miss as "no room": it cannot
	// happen after a successful resolve, and an agent reading a transcript is
	// the wrong caller to fail over a label.
	var entry meetingsCatalogEntry
	if found, err := listing.find(meetingID); err == nil {
		entry = found
	}
	bundle := buildMeetingContext(meetingID, meeting, entry)

	out, closeOut, err := openMeetingsOutput(outFile, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "context failed: %v\n", err)
		return 1
	}
	writeErr := writeMeetingContext(out, bundle, *asJSON)
	if closeErr := closeOut(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		fmt.Fprintf(stderr, "context failed: write output: %v\n", writeErr)
		return 1
	}

	if !bundle.Summary.Present {
		fmt.Fprintf(stderr, "note=this meeting has no generated summary; the transcript is included on its own\n")
	}
	if outFile != "" {
		fmt.Fprintf(stdout, "meeting_context -> %s\n", outFile)
	}
	return 0
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

// buildMeetingContext assembles the context bundle from an extracted meeting.
//
// catalogID is the id the caller asked for; it wins over the manifest's own
// meeting id so the bundle can be correlated with the catalog entry it came
// from. The manifest id is content-derived (a hash of the audio) and is not the
// id the published catalog indexes by.
func buildMeetingContext(catalogID string, meeting inspectpkg.ExtractedMeeting, entry meetingsCatalogEntry) meetingContext {
	labels := meeting.SpeakerLabels()
	segments := deriveProseSegments(meeting.Transcript.Words, labels)

	// The catalog is authoritative for the room, and the file is the fallback.
	// A catalog entry's room id is kept current by the backfill and by any
	// reattribution an operator has made, while the file's is whatever it was
	// last tagged with — so preferring the entry means a merged room reads as
	// merged here too. The file's legacy room name is used only when the
	// catalog has none, which is what a pre-D-640 archive looks like.
	roomID := strings.TrimSpace(entry.RoomID)
	if roomID == "" {
		roomID = strings.TrimSpace(meeting.Manifest.Meeting.RoomID)
	}
	roomName := strings.TrimSpace(entry.RoomName)
	if roomName == "" {
		roomName = strings.TrimSpace(meeting.Manifest.Meeting.RoomName)
	}

	bundle := meetingContext{
		Version: meetingContextVersion,
		Meeting: meetingContextMeeting{
			ID:           catalogID,
			Title:        strings.TrimSpace(meeting.Manifest.Meeting.Title),
			CreatedAtUTC: strings.TrimSpace(meeting.Manifest.Meeting.CreatedAtUTC),
			DurationMS:   meeting.Manifest.Meeting.DurationMS,
			RoomID:       roomID,
			RoomName:     roomName,
		},
		TranscriptSource:       transcriptSourceDerived,
		SourceTranscriptID:     meeting.Transcript.TranscriptID,
		SourceTranscriptFormat: meeting.Transcript.Format,
		Language:               meeting.Transcript.Language,
		WordCount:              meeting.Transcript.WordCount,
		Segments:               make([]meetingContextSegment, 0, len(segments)),
	}
	for _, speaker := range meeting.Manifest.Speakers {
		bundle.Speakers = append(bundle.Speakers, meetingContextSpeaker{
			ID:    speaker.ID,
			Label: speakerLabel(speaker.ID, labels),
		})
	}
	for _, segment := range segments {
		bundle.Segments = append(bundle.Segments, meetingContextSegment{
			Speaker:      segment.Speaker,
			SpeakerLabel: segment.SpeakerLabel,
			StartMS:      segment.StartMS,
			EndMS:        segment.EndMS,
			Text:         segment.Text,
		})
	}
	if len(meeting.SummaryMarkdown) > 0 {
		bundle.Summary = meetingContextSummary{
			Present:  true,
			Format:   meeting.SummaryFormat(),
			Model:    meeting.SummaryModel(),
			Markdown: string(meeting.SummaryMarkdown),
		}
	}
	return bundle
}

func writeMeetingContext(out io.Writer, bundle meetingContext, asJSON bool) error {
	if asJSON {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(bundle)
	}
	return writeMeetingContextMarkdown(out, bundle)
}

// writeMeetingContextMarkdown renders the bundle as markdown, the shape an agent
// reads directly.
func writeMeetingContextMarkdown(out io.Writer, bundle meetingContext) error {
	title := bundle.Meeting.Title
	if title == "" {
		title = bundle.Meeting.ID
	}
	buf := &strings.Builder{}
	fmt.Fprintf(buf, "# %s\n\n", title)

	fmt.Fprintf(buf, "- Meeting id: `%s`\n", bundle.Meeting.ID)
	// The room, when there is one. Named as "Room" with the id in backticks
	// beside it, because the id is what `meetings list --room` takes and the
	// name is not — an agent that copies the label instead of the id gets an
	// empty list and no explanation.
	if bundle.Meeting.RoomID != "" || bundle.Meeting.RoomName != "" {
		switch {
		case bundle.Meeting.RoomName != "" && bundle.Meeting.RoomID != "":
			fmt.Fprintf(buf, "- Room: %s (`%s`)\n", bundle.Meeting.RoomName, bundle.Meeting.RoomID)
		case bundle.Meeting.RoomID != "":
			fmt.Fprintf(buf, "- Room: `%s`\n", bundle.Meeting.RoomID)
		default:
			fmt.Fprintf(buf, "- Room: %s\n", bundle.Meeting.RoomName)
		}
	}
	if bundle.Meeting.CreatedAtUTC != "" {
		fmt.Fprintf(buf, "- Recorded (UTC): %s\n", bundle.Meeting.CreatedAtUTC)
	}
	if bundle.Meeting.DurationMS > 0 {
		fmt.Fprintf(buf, "- Duration: %s\n", formatMeetingDuration(bundle.Meeting.DurationMS))
	}
	if len(bundle.Speakers) > 0 {
		labels := make([]string, 0, len(bundle.Speakers))
		for _, speaker := range bundle.Speakers {
			labels = append(labels, speaker.Label)
		}
		fmt.Fprintf(buf, "- Speakers: %s\n", strings.Join(labels, ", "))
	}
	fmt.Fprintf(buf, "- Words: %d\n", bundle.WordCount)
	fmt.Fprintf(buf, "- Transcript source: `%s`\n\n", bundle.TranscriptSource)

	fmt.Fprint(buf, "## Summary\n\n")
	if bundle.Summary.Present {
		// The summary is authored markdown with its own headings. Demote them so
		// they nest under this section instead of reading as siblings of
		// "## Summary" and "## Transcript" — a consumer splitting the document on
		// h2 would otherwise take the summary's own sections for top-level ones.
		fmt.Fprintf(buf, "%s\n", demoteMarkdownHeadings(strings.TrimRight(bundle.Summary.Markdown, "\n"), 2))
	} else {
		fmt.Fprint(buf, "_No summary was generated for this meeting._\n")
	}

	fmt.Fprint(buf, "\n## Transcript\n\n")
	fmt.Fprint(buf, "_Assembled from the recording's word timings, not from an edited transcript._\n\n")
	if len(bundle.Segments) == 0 {
		fmt.Fprint(buf, "_This meeting has no transcribed speech._\n")
	}
	for _, segment := range bundle.Segments {
		if segment.SpeakerLabel != "" {
			fmt.Fprintf(buf, "**%s:** %s\n\n", segment.SpeakerLabel, segment.Text)
			continue
		}
		fmt.Fprintf(buf, "%s\n\n", segment.Text)
	}

	_, err := io.WriteString(out, buf.String())
	return err
}

// maxMarkdownHeadingLevel is the deepest heading markdown defines; a heading
// already this deep is left alone rather than gaining a seventh "#", which
// renders as literal text.
const maxMarkdownHeadingLevel = 6

// demoteMarkdownHeadings pushes every ATX heading in body down so the shallowest
// one sits just below under. Relative depth is preserved, and text inside fenced
// code blocks is left untouched.
func demoteMarkdownHeadings(body string, under int) string {
	lines := strings.Split(body, "\n")

	shallowest, deepest := 0, 0
	fence := markdownFence{}
	for _, line := range lines {
		if fence.toggles(line) {
			continue
		}
		if fence.open() {
			continue
		}
		if level := markdownHeadingLevel(line); level > 0 {
			if shallowest == 0 || level < shallowest {
				shallowest = level
			}
			if level > deepest {
				deepest = level
			}
		}
	}
	if shallowest == 0 {
		return body
	}
	// One shift for every heading, clamped so the deepest still fits. Clamping
	// per line instead would leave a child shallower than its parent, inverting
	// the nesting this exists to preserve.
	shift := under + 1 - shallowest
	if headroom := maxMarkdownHeadingLevel - deepest; shift > headroom {
		shift = headroom
	}
	if shift <= 0 {
		return body
	}

	fence = markdownFence{}
	for i, line := range lines {
		if fence.toggles(line) {
			continue
		}
		if fence.open() {
			continue
		}
		if markdownHeadingLevel(line) == 0 {
			continue
		}
		lines[i] = strings.Repeat("#", shift) + line
	}
	return strings.Join(lines, "\n")
}

// markdownFence tracks whether the current line sits inside a fenced code block.
//
// It records the opening fence's marker and length, because per CommonMark only a
// run of the same character at least as long closes it. Treating any ``` or ~~~
// as a toggle got this wrong twice: a nested shorter fence inside a longer one
// closed the block early, and a ~~~ was accepted as the closer of a ``` block —
// either way the code inside was then rewritten as if it were prose.
type markdownFence struct {
	marker rune
	length int
}

func (f *markdownFence) open() bool { return f.length > 0 }

// toggles reports whether line is a fence delimiter, updating the state if so.
func (f *markdownFence) toggles(line string) bool {
	marker, length := markdownFenceRun(line)
	if length == 0 {
		return false
	}
	if !f.open() {
		f.marker, f.length = marker, length
		return true
	}
	if marker == f.marker && length >= f.length {
		f.marker, f.length = 0, 0
		return true
	}
	// A different or shorter run inside an open block is content, not a closer.
	return false
}

// markdownFenceRun returns the fence character and run length of a fence line,
// or a zero length when the line is not one.
func markdownFenceRun(line string) (rune, int) {
	trimmed := strings.TrimLeft(line, " ")
	for _, marker := range []rune{'`', '~'} {
		length := 0
		for _, r := range trimmed {
			if r != marker {
				break
			}
			length++
		}
		if length >= 3 {
			return marker, length
		}
	}
	return 0, 0
}

// markdownHeadingLevel returns the ATX heading level of line, or 0 when it is
// not a heading. A run of "#" must be followed by a space to be a heading.
func markdownHeadingLevel(line string) int {
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level == 0 || level > maxMarkdownHeadingLevel {
		return 0
	}
	if level == len(line) || line[level] != ' ' {
		return 0
	}
	return level
}

// formatMeetingDuration renders milliseconds as h:mm:ss or m:ss, whichever fits.
func formatMeetingDuration(durationMS int64) string {
	if durationMS < 0 {
		durationMS = 0
	}
	totalSeconds := durationMS / 1000
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%d:%02d", minutes, seconds)
}
