package meetingcontext

import (
	"strings"
	"testing"
)

func windowFixture() Bundle {
	meeting := Build(BuildInput{ID: "M1", Title: "Standup", WordCount: 40, Summary: Summary{Present: true, Markdown: "We shipped."}, Segments: []Segment{
		{Speaker: "s1", StartMS: 0, EndMS: 10_000, Text: "Good morning."},
		{Speaker: "s2", StartMS: 10_000, EndMS: 70_000, Text: "Straddles the start."},
		{Speaker: "s1", StartMS: 70_000, EndMS: 80_000, Text: "Inside."},
		{Speaker: "s2", StartMS: 115_000, EndMS: 130_000, Text: "Straddles the end."},
		{Speaker: "s1", StartMS: 120_000, EndMS: 125_000, Text: "Starts exactly at the end."},
	}})
	return Bundle{Meetings: []MeetingContext{meeting}}
}

func segmentTexts(m MeetingContext) string {
	texts := make([]string, 0, len(m.Segments))
	for _, segment := range m.Segments {
		texts = append(texts, segment.Text)
	}
	return strings.Join(texts, "|")
}

// A window keeps every passage overlapping it, whole — a passage straddling an
// edge is not cut, because half of it would say something the whole does not —
// and it says that it is a window. Everything else still describes the whole
// meeting.
func TestWithWindowKeepsOverlappingPassagesWhole(t *testing.T) {
	to := int64(120_000)
	original := windowFixture()
	got := original.WithWindow(Window{FromMS: 60_000, ToMS: &to}).Meetings[0]

	if texts := segmentTexts(got); texts != "Straddles the start.|Inside.|Straddles the end." {
		t.Fatalf("segments = %s", texts)
	}
	if got.Window == nil || got.Window.FromMS != 60_000 || *got.Window.ToMS != 120_000 {
		t.Fatalf("window = %+v", got.Window)
	}
	if got.WordCount != 40 || !got.Summary.Present {
		t.Fatalf("the meeting-wide fields changed: words=%d summary=%v", got.WordCount, got.Summary.Present)
	}
	if len(original.Meetings[0].Segments) != 5 || original.Meetings[0].Window != nil {
		t.Fatal("WithWindow changed the bundle it was called on")
	}

	rendered, err := RenderMarkdown(Bundle{Meetings: []MeetingContext{got}}, RenderOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "- Window: 01:00 to 02:00 — only the passages overlapping it are below") {
		t.Fatalf("the markdown does not say it is a window:\n%s", rendered)
	}
}

// A window open at the end runs to the end of the meeting; a passage ending
// exactly where the window starts is not in it.
func TestWithWindowOpenEnded(t *testing.T) {
	got := windowFixture().WithWindow(Window{FromMS: 10_000}).Meetings[0]
	if texts := segmentTexts(got); strings.Contains(texts, "Good morning.") || !strings.HasSuffix(texts, "Starts exactly at the end.") {
		t.Fatalf("segments = %s", texts)
	}
}

func TestWindowValidate(t *testing.T) {
	same, before := int64(5_000), int64(1_000)
	for name, window := range map[string]Window{
		"negative start": {FromMS: -1},
		"empty":          {FromMS: 5_000, ToMS: &same},
		"backwards":      {FromMS: 5_000, ToMS: &before},
	} {
		if window.Validate() == nil {
			t.Errorf("%s window was accepted", name)
		}
	}
	if (Window{FromMS: 0}).Validate() != nil {
		t.Error("a window from the start was refused")
	}
}
