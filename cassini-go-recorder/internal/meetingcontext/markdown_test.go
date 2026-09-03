package meetingcontext

import (
	"strings"
	"testing"
)

// oneMeeting is the smallest bundle the renderer accepts, so the assertions
// below can be about the markdown rather than about bundle plumbing.
func oneMeeting(segments ...Segment) Bundle {
	return Bundle{Meetings: []MeetingContext{Build(BuildInput{
		ID:       "M1",
		Title:    "Daily Standup",
		Segments: segments,
	})}}
}

func TestRenderMarkdownRefusesAnEmptyBundle(t *testing.T) {
	// An empty bundle is a caller mistake, and rendering it as an empty
	// document would hand an agent a question with no evidence attached and no
	// sign that anything went wrong.
	if _, err := RenderMarkdown(Bundle{}, RenderOpts{}); err == nil {
		t.Error("rendering a bundle with no meetings should fail")
	}
}

func TestRenderMarkdownSeparatesMeetingsWithARule(t *testing.T) {
	bundle := Bundle{Meetings: []MeetingContext{
		Build(BuildInput{ID: "M1", Title: "First"}),
		Build(BuildInput{ID: "M2", Title: "Second"}),
	}}

	got, err := RenderMarkdown(bundle, RenderOpts{})
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	if strings.Count(got, "\n\n---\n\n") != 1 {
		t.Errorf("expected exactly one separator:\n%s", got)
	}
	// The rule must sit under a blank line. Directly beneath a line of text,
	// "---" is a setext heading, and it would promote the last line of the
	// previous meeting instead of dividing the two.
	if strings.Contains(got, "_\n---") {
		t.Errorf("the separator follows a line of text, making it a setext heading:\n%s", got)
	}
	if first, second := strings.Index(got, "# First"), strings.Index(got, "# Second"); first > second {
		t.Errorf("meetings are not in the bundle's own order:\n%s", got)
	}
}

// A one-meeting bundle renders as it always did, with no separator and no
// container of any kind — the property the golden fixtures in internal/cassini
// pin byte for byte.
func TestRenderMarkdownLeavesAOneMeetingDocumentAlone(t *testing.T) {
	got, err := RenderMarkdown(oneMeeting(Segment{SpeakerLabel: "Erlich", Text: "we should ship it"}), RenderOpts{})
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	if strings.Contains(got, "---") {
		t.Errorf("a single meeting must carry no separator:\n%s", got)
	}
	if !strings.Contains(got, "**Erlich:** we should ship it") {
		t.Errorf("the un-cited speaker form changed:\n%s", got)
	}
}

func TestRenderMarkdownCitesSegmentStartsWhenAsked(t *testing.T) {
	bundle := oneMeeting(
		Segment{SpeakerLabel: "Erlich", StartMS: 5_000, Text: "we should ship it"},
		Segment{StartMS: 3_642_000, Text: "then we are done"},
	)

	got, err := RenderMarkdown(bundle, RenderOpts{Timestamps: true})
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	// The citation goes between the label and the colon, matching the
	// "<label> at MM:SS" grammar the workflow prompts are graded against.
	if !strings.Contains(got, "**Erlich** [00:05]: we should ship it") {
		t.Errorf("expected an attributed citation:\n%s", got)
	}
	// An unattributed passage still gets its citation, or a workflow could cite
	// only the speech that happened to be attributed.
	if !strings.Contains(got, "[1:00:42] then we are done") {
		t.Errorf("expected an unattributed citation:\n%s", got)
	}
}

func TestFormatTimestamp(t *testing.T) {
	cases := map[int64]string{
		0:         "00:00",
		-1_000:    "00:00",
		5_000:     "00:05",
		65_000:    "01:05",
		600_000:   "10:00",
		3_600_000: "1:00:00",
		3_725_000: "1:02:05",
	}
	for ms, want := range cases {
		if got := formatTimestamp(ms); got != want {
			t.Errorf("formatTimestamp(%d) = %q, want %q", ms, got, want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[int64]string{
		0:         "0:00",
		9_000:     "0:09",
		65_000:    "1:05",
		600_000:   "10:00",
		3_725_000: "1:02:05",
	}
	for ms, want := range cases {
		if got := formatDuration(ms); got != want {
			t.Errorf("formatDuration(%d) = %q, want %q", ms, got, want)
		}
	}
}

func TestFormatDurationClampsNegatives(t *testing.T) {
	if got := formatDuration(-3661000); got != "0:00" {
		t.Errorf("formatDuration(-3661000) = %q, want 0:00", got)
	}
}

func TestDemoteMarkdownHeadings(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		under int
		want  string
	}{
		{"h2 under h2 becomes h3", "## Decisions", 2, "### Decisions"},
		{"relative depth is preserved", "## A\n### B", 2, "### A\n#### B"},
		{"an h1 summary is demoted from its own level", "# Title\n## Sub", 2, "### Title\n#### Sub"},
		{"already deep enough is untouched", "#### Deep", 2, "#### Deep"},
		{"no headings is untouched", "just prose\n- a bullet", 2, "just prose\n- a bullet"},
		{"a hash without a space is not a heading", "#hashtag", 2, "#hashtag"},
		{"h6 is not pushed past the maximum", "###### Six\n###### Also six", 5, "###### Six\n###### Also six"},
		{"hashes inside a fence are left alone", "## Real\n\n```sh\n# a comment\n```", 2, "### Real\n\n```sh\n# a comment\n```"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := demoteMarkdownHeadings(tc.in, tc.under); got != tc.want {
				t.Errorf("demoteMarkdownHeadings(%q, %d) =\n%q\nwant\n%q", tc.in, tc.under, got, tc.want)
			}
		})
	}
}

// Content inside a fenced code block must never be rewritten, whichever fence
// marker and length the summary uses.
func TestDemoteMarkdownHeadingsLeavesFencedContentAlone(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "a shorter inner fence does not close a longer one",
			in:   "# Title\n\n````\n```\n# not a heading\n```\n````\n\n## After",
			want: "### Title\n\n````\n```\n# not a heading\n```\n````\n\n#### After",
		},
		{
			name: "a tilde run does not close a backtick block",
			in:   "# Title\n```\n# inside\n~~~\n## still inside\n```\n## after",
			want: "### Title\n```\n# inside\n~~~\n## still inside\n```\n#### after",
		},
		{
			name: "indented fence still counts",
			in:   "## Real\n\n  ```sh\n  # a comment\n  ```",
			want: "### Real\n\n  ```sh\n  # a comment\n  ```",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := demoteMarkdownHeadings(tc.in, 2); got != tc.want {
				t.Errorf("got:\n%q\nwant:\n%q", got, tc.want)
			}
		})
	}
}

// Clamping at h6 must apply one shift to every heading. Clamping per line left a
// child shallower than its parent, inverting the nesting.
func TestDemoteMarkdownHeadingsNeverInvertsNesting(t *testing.T) {
	got := demoteMarkdownHeadings("# Title\n#### Deep\n##### Deeper", 2)

	levels := make([]int, 0, 3)
	for _, line := range strings.Split(got, "\n") {
		levels = append(levels, markdownHeadingLevel(line))
	}
	for i := 1; i < len(levels); i++ {
		if levels[i] < levels[i-1] && i == 2 {
			t.Errorf("nesting inverted: %v\n%s", levels, got)
		}
	}
	if levels[1] > levels[2] {
		t.Errorf("child %d is shallower than parent %d: %v\n%s", levels[2], levels[1], levels, got)
	}
}
