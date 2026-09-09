package meetingcontext

import (
	"fmt"
	"strings"
)

// RenderOpts controls what the markdown rendering includes.
type RenderOpts struct {
	// Timestamps writes each passage's start time beside the speaker, as MM:SS
	// or H:MM:SS once a meeting passes an hour.
	//
	// Off by default, because it changes bytes existing consumers have already
	// pinned. It exists because the insight workflows ask the model to cite
	// where a claim was made, and the markdown carried no time axis at all —
	// the timings reached the JSON form and stopped there. Asking a model to
	// cite an axis it cannot see does not measure grounding; it measures which
	// model invents the more plausible number.
	Timestamps bool
}

// meetingSeparator divides consecutive meetings in a many-meeting document.
//
// A thematic break rather than a heading level: every meeting already opens
// with its own h1, and demoting those to nest under a shared title would
// change the bytes a one-meeting document produces. The blank line before the
// rule is required — directly beneath a line of text, "---" is a setext
// heading, which would silently promote the last line of the previous meeting.
const meetingSeparator = "\n\n---\n\n"

// RenderMarkdown renders the bundle as the markdown an agent reads directly.
//
// A one-meeting bundle renders exactly as it always has, to the byte. Many
// meetings render as those same documents joined by meetingSeparator, in the
// order the bundle holds them, so what a consumer learned to parse for one
// meeting keeps working for N.
func RenderMarkdown(b Bundle, opts RenderOpts) (string, error) {
	if len(b.Meetings) == 0 {
		return "", fmt.Errorf("render %s: a bundle must carry at least one meeting", Version)
	}
	if len(b.Meetings) == 1 {
		return renderMeetingMarkdown(b.Meetings[0], opts), nil
	}
	parts := make([]string, 0, len(b.Meetings))
	for _, meeting := range b.Meetings {
		parts = append(parts, strings.TrimRight(renderMeetingMarkdown(meeting, opts), "\n"))
	}
	return strings.Join(parts, meetingSeparator) + "\n", nil
}

func renderMeetingMarkdown(bundle MeetingContext, opts RenderOpts) string {
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
		fmt.Fprintf(buf, "- Duration: %s\n", formatDuration(bundle.Meeting.DurationMS))
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
		// Without timestamps the colon stays inside the bold: that is the byte
		// every already-published context document carries, and this rendering
		// is pinned by a golden fixture for exactly that reason. The cited form
		// puts the citation between the speaker and the colon, matching the
		// `<label> at MM:SS` grammar the workflow prompts are graded against.
		switch {
		case segment.SpeakerLabel != "" && opts.Timestamps:
			fmt.Fprintf(buf, "**%s** [%s]: %s\n\n", segment.SpeakerLabel, formatTimestamp(segment.StartMS), segment.Text)
		case segment.SpeakerLabel != "":
			fmt.Fprintf(buf, "**%s:** %s\n\n", segment.SpeakerLabel, segment.Text)
		case opts.Timestamps:
			fmt.Fprintf(buf, "[%s] %s\n\n", formatTimestamp(segment.StartMS), segment.Text)
		default:
			fmt.Fprintf(buf, "%s\n\n", segment.Text)
		}
	}

	return buf.String()
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

// formatDuration renders milliseconds as h:mm:ss or m:ss, whichever fits. It is
// how the "- Duration:" line reads.
func formatDuration(durationMS int64) string {
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

// formatTimestamp renders a passage's start offset as MM:SS, or H:MM:SS once a
// meeting passes an hour.
//
// Minutes are zero-padded even under ten, unlike formatDuration: this value is
// a citation that a workflow's output grammar is matched against, and a
// citation that is sometimes "4:12" and sometimes "04:12" is two formats to
// whatever has to check it.
func formatTimestamp(offsetMS int64) string {
	if offsetMS < 0 {
		offsetMS = 0
	}
	totalSeconds := offsetMS / 1000
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}
