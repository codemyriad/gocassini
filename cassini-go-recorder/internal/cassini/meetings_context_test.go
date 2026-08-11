package cassini

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	inspectpkg "gocassini/internal/inspect"
	"gocassini/internal/portable"
)

// extractedMeetingFixture builds an ExtractedMeeting in memory, so the bundle
// assembly and rendering can be tested without ffprobe or a packed file.
func extractedMeetingFixture(words []inspectpkg.TranscriptWord, summary string) inspectpkg.ExtractedMeeting {
	meeting := inspectpkg.ExtractedMeeting{
		FormatTag: portable.FormatV2,
		Manifest: portable.Manifest{
			Version: 2,
			Meeting: portable.Meeting{
				ID:           "mtg_contenthash",
				Title:        "Daily Standup",
				CreatedAtUTC: "2026-08-11T10:32:00Z",
				DurationMS:   3_725_000,
			},
			Speakers: []portable.Speaker{
				{ID: "spk1", Label: "Erlich"},
				{ID: "spk2", Label: "Monica"},
			},
			Summary: map[string]any{"format": "markdown", "model": "summary-model"},
		},
		Transcript: inspectpkg.ExtractedTranscript{
			TranscriptID: portable.RoleRawASR,
			Role:         portable.RoleRawASR,
			Format:       "cassini.words.v1",
			Language:     "en",
			WordCount:    len(words),
			Words:        words,
		},
	}
	if summary != "" {
		meeting.SummaryMarkdown = []byte(summary)
	}
	return meeting
}

func standupWords() []inspectpkg.TranscriptWord {
	words := wordsAt("spk1", 0, 200, "we", "should", "ship", "the", "agent", "surface")
	return append(words, wordsAt("spk2", 1400, 200, "agreed", "let's", "do", "it")...)
}

// The catalog id is what a caller can correlate; the manifest's own id is a
// content hash and is not what the published catalog indexes by.
func TestBuildMeetingContextUsesTheCatalogIDNotTheManifestID(t *testing.T) {
	bundle := buildMeetingContext("CATALOG-ID-1", extractedMeetingFixture(standupWords(), ""))

	if bundle.Meeting.ID != "CATALOG-ID-1" {
		t.Errorf("Meeting.ID = %q, want the catalog id", bundle.Meeting.ID)
	}
	if bundle.Version != meetingContextVersion {
		t.Errorf("Version = %q, want %q", bundle.Version, meetingContextVersion)
	}
}

// The provenance marker is the guard against an agent presenting derived prose
// as an edited transcript, so it is asserted explicitly.
func TestBuildMeetingContextMarksTheTranscriptAsDerived(t *testing.T) {
	bundle := buildMeetingContext("M1", extractedMeetingFixture(standupWords(), ""))

	if bundle.TranscriptSource != "derived-from-words" {
		t.Errorf("TranscriptSource = %q, want derived-from-words", bundle.TranscriptSource)
	}
	if bundle.SourceTranscriptID != portable.RoleRawASR {
		t.Errorf("SourceTranscriptID = %q, want %q", bundle.SourceTranscriptID, portable.RoleRawASR)
	}
	if bundle.SourceTranscriptFormat != "cassini.words.v1" {
		t.Errorf("SourceTranscriptFormat = %q, want cassini.words.v1", bundle.SourceTranscriptFormat)
	}
}

func TestBuildMeetingContextAttributesSegmentsToSpeakers(t *testing.T) {
	bundle := buildMeetingContext("M1", extractedMeetingFixture(standupWords(), "# Summary\n"))

	if len(bundle.Segments) != 2 {
		t.Fatalf("got %d segments, want 2: %+v", len(bundle.Segments), bundle.Segments)
	}
	if bundle.Segments[0].SpeakerLabel != "Erlich" || bundle.Segments[0].Text != "we should ship the agent surface" {
		t.Errorf("segment 0 = %+v", bundle.Segments[0])
	}
	if bundle.Segments[1].SpeakerLabel != "Monica" || bundle.Segments[1].Text != "agreed let's do it" {
		t.Errorf("segment 1 = %+v", bundle.Segments[1])
	}
	if len(bundle.Speakers) != 2 {
		t.Errorf("got %d speakers, want 2", len(bundle.Speakers))
	}
	if !bundle.Summary.Present || bundle.Summary.Markdown != "# Summary\n" {
		t.Errorf("Summary = %+v, want the markdown present", bundle.Summary)
	}
	if bundle.Summary.Model != "summary-model" {
		t.Errorf("Summary.Model = %q, want summary-model", bundle.Summary.Model)
	}
}

// A missing summary must be a distinguishable state, not an empty string that
// reads like an empty summary.
func TestBuildMeetingContextMarksAMissingSummaryAbsent(t *testing.T) {
	bundle := buildMeetingContext("M1", extractedMeetingFixture(standupWords(), ""))

	if bundle.Summary.Present {
		t.Errorf("Summary.Present = true, want false: %+v", bundle.Summary)
	}
	if bundle.Summary.Markdown != "" {
		t.Errorf("Summary.Markdown = %q, want empty", bundle.Summary.Markdown)
	}
}

func TestWriteMeetingContextMarkdown(t *testing.T) {
	bundle := buildMeetingContext("M1", extractedMeetingFixture(standupWords(), "# Summary\n\n- Ship it.\n"))

	var out bytes.Buffer
	if err := writeMeetingContext(&out, bundle, false); err != nil {
		t.Fatalf("writeMeetingContext: %v", err)
	}
	got := out.String()

	for _, want := range []string{
		"# Daily Standup",
		"- Meeting id: `M1`",
		"- Recorded (UTC): 2026-08-11T10:32:00Z",
		"- Duration: 1:02:05",
		"- Speakers: Erlich, Monica",
		"- Transcript source: `derived-from-words`",
		"## Summary",
		"- Ship it.",
		"## Transcript",
		"not from an edited transcript",
		"**Erlich:** we should ship the agent surface",
		"**Monica:** agreed let's do it",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("markdown missing %q:\n%s", want, got)
		}
	}
}

func TestWriteMeetingContextMarkdownSaysWhenThereIsNoSummary(t *testing.T) {
	bundle := buildMeetingContext("M1", extractedMeetingFixture(standupWords(), ""))

	var out bytes.Buffer
	if err := writeMeetingContext(&out, bundle, false); err != nil {
		t.Fatalf("writeMeetingContext: %v", err)
	}
	if !strings.Contains(out.String(), "_No summary was generated for this meeting._") {
		t.Errorf("expected the no-summary line:\n%s", out.String())
	}
}

func TestWriteMeetingContextMarkdownHandlesASilentMeeting(t *testing.T) {
	bundle := buildMeetingContext("M1", extractedMeetingFixture(nil, ""))

	var out bytes.Buffer
	if err := writeMeetingContext(&out, bundle, false); err != nil {
		t.Fatalf("writeMeetingContext: %v", err)
	}
	if !strings.Contains(out.String(), "_This meeting has no transcribed speech._") {
		t.Errorf("expected the no-speech line:\n%s", out.String())
	}
}

func TestWriteMeetingContextJSON(t *testing.T) {
	bundle := buildMeetingContext("M1", extractedMeetingFixture(standupWords(), "# Summary\n"))

	var out bytes.Buffer
	if err := writeMeetingContext(&out, bundle, true); err != nil {
		t.Fatalf("writeMeetingContext: %v", err)
	}

	var got meetingContext
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("parse JSON: %v\n%s", err, out.String())
	}
	if got.Version != meetingContextVersion {
		t.Errorf("version = %q, want %q", got.Version, meetingContextVersion)
	}
	if got.TranscriptSource != "derived-from-words" {
		t.Errorf("transcriptSource = %q", got.TranscriptSource)
	}
	if len(got.Segments) != 2 {
		t.Fatalf("got %d segments, want 2", len(got.Segments))
	}
	if got.Segments[0].SpeakerLabel != "Erlich" {
		t.Errorf("segment 0 speakerLabel = %q", got.Segments[0].SpeakerLabel)
	}
	if !got.Summary.Present || got.Summary.Markdown != "# Summary\n" {
		t.Errorf("summary = %+v", got.Summary)
	}
}

// The summary's own headings must nest under "## Summary", or a consumer
// splitting the document on h2 reads them as top-level sections of the context.
func TestWriteMeetingContextMarkdownNestsTheSummarysOwnHeadings(t *testing.T) {
	summary := "## Decisions\n\n- Ship it.\n\n## Actions\n\n- Erlich: open the PR.\n"
	bundle := buildMeetingContext("M1", extractedMeetingFixture(standupWords(), summary))

	var out bytes.Buffer
	if err := writeMeetingContext(&out, bundle, false); err != nil {
		t.Fatalf("writeMeetingContext: %v", err)
	}
	got := out.String()

	for _, want := range []string{"### Decisions", "### Actions"} {
		if !strings.Contains(got, want) {
			t.Errorf("markdown missing demoted heading %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\n## Decisions") {
		t.Errorf("summary heading should not stay at h2:\n%s", got)
	}
	// The JSON bundle carries the summary exactly as authored — only the
	// markdown rendering nests it.
	if bundle.Summary.Markdown != summary {
		t.Errorf("Summary.Markdown = %q, want it unmodified", bundle.Summary.Markdown)
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

func TestFormatMeetingDuration(t *testing.T) {
	cases := map[int64]string{
		0:         "0:00",
		9_000:     "0:09",
		65_000:    "1:05",
		600_000:   "10:00",
		3_725_000: "1:02:05",
	}
	for ms, want := range cases {
		if got := formatMeetingDuration(ms); got != want {
			t.Errorf("formatMeetingDuration(%d) = %q, want %q", ms, got, want)
		}
	}
}

// End to end against a fake Nextcloud serving a real packed .opus: fetch,
// extract, derive and render, with no temp file left behind.
func TestMeetingsContextEndToEndAgainstAPackedOpus(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()

	meetingDir := filepath.Join(tmp, "good.meeting")
	if err := writeReadyMeetingBundleFixture(meetingDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write ready meeting bundle: %v", err)
	}
	sourceOpus := filepath.Join(tmp, "published.opus")
	if err := packMeetingBundle(context.Background(), meetingDir, sourceOpus, portablePackOptions{Title: "Daily Standup"}); err != nil {
		t.Fatalf("pack meeting bundle: %v", err)
	}
	published, err := os.ReadFile(sourceOpus)
	if err != nil {
		t.Fatalf("read packed opus: %v", err)
	}
	fake := newMeetingsFakeNextcloud(t, serveCatalogAndOpus(oneMeetingCatalog, published))

	t.Run("markdown", func(t *testing.T) {
		code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "context", "MEETING1")
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		for _, want := range []string{"# Daily Standup", "## Summary", "## Transcript", "- Transcript source: `derived-from-words`"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("stdout missing %q:\n%s", want, stdout)
			}
		}
	})

	t.Run("json", func(t *testing.T) {
		code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "context", "MEETING1", "--json")
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		var got meetingContext
		if err := json.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatalf("parse JSON: %v\n%s", err, stdout)
		}
		if got.Meeting.ID != "MEETING1" {
			t.Errorf("meeting.id = %q, want the catalog id MEETING1", got.Meeting.ID)
		}
		if got.TranscriptSource != "derived-from-words" {
			t.Errorf("transcriptSource = %q", got.TranscriptSource)
		}
		if got.SourceTranscriptID == "" {
			t.Error("sourceTranscriptId should name the transcript the prose came from")
		}
		if got.WordCount == 0 || len(got.Segments) == 0 {
			t.Errorf("expected transcribed speech, got wordCount=%d segments=%d", got.WordCount, len(got.Segments))
		}
	})

	t.Run("--out writes a file and reports the path", func(t *testing.T) {
		outPath := filepath.Join(t.TempDir(), "context.md")
		code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "context", "MEETING1", "--out", outPath)
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		if !strings.Contains(stdout, "meeting_context -> "+outPath) {
			t.Errorf("expected the arrow line, got %q", stdout)
		}
		body, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatalf("read %s: %v", outPath, err)
		}
		if !strings.Contains(string(body), "# Daily Standup") {
			t.Errorf("file content unexpected:\n%s", body)
		}
	})

	t.Run("--keep-opus keeps a parseable download", func(t *testing.T) {
		keptPath := filepath.Join(t.TempDir(), "kept.opus")
		code, _, stderr := runMeetingsCLI(t, fake.server.URL, "context", "MEETING1", "--keep-opus", keptPath)
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		kept, err := os.ReadFile(keptPath)
		if err != nil {
			t.Fatalf("read %s: %v", keptPath, err)
		}
		if !bytes.Equal(kept, published) {
			t.Errorf("kept %d bytes, want the %d published bytes", len(kept), len(published))
		}
	})
}

// Staging the download must not leave temp files behind — a leak here would
// accumulate whole meetings, and cassini has leaked temp state before.
func TestMeetingsContextLeavesNoTempFiles(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	meetingDir := filepath.Join(t.TempDir(), "good.meeting")
	if err := writeReadyMeetingBundleFixture(meetingDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write ready meeting bundle: %v", err)
	}
	sourceOpus := filepath.Join(t.TempDir(), "published.opus")
	if err := packMeetingBundle(context.Background(), meetingDir, sourceOpus, portablePackOptions{Title: "Daily Standup"}); err != nil {
		t.Fatalf("pack meeting bundle: %v", err)
	}
	published, err := os.ReadFile(sourceOpus)
	if err != nil {
		t.Fatalf("read packed opus: %v", err)
	}

	fake := newMeetingsFakeNextcloud(t, serveCatalogAndOpus(oneMeetingCatalog, published))
	if code, _, stderr := runMeetingsCLI(t, fake.server.URL, "context", "MEETING1"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	assertNoStrayFiles(t, tmp)

	// And on the failure path too: an id the caller cannot read.
	if code, _, _ := runMeetingsCLI(t, fake.server.URL, "context", "SOMEONE-ELSES"); code != 1 {
		t.Fatalf("expected exit 1 for an unreadable id, got %d", code)
	}
	assertNoStrayFiles(t, tmp)
}

// A file that is not a portable meeting must be reported as such rather than
// producing an empty bundle that reads like a silent meeting.
func TestMeetingsContextReportsANonPortableDownload(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalogAndOpus(oneMeetingCatalog, []byte("not an ogg file at all")))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "context", "MEETING1")

	if code != 1 {
		t.Fatalf("exit=%d, want 1 (stderr=%q)", code, stderr)
	}
	if !strings.Contains(stderr, "read the downloaded meeting") {
		t.Errorf("stderr=%q, want it to say reading the meeting failed", stderr)
	}
	if strings.Contains(stdout, meetingContextVersion) {
		t.Errorf("no bundle should be emitted on a read failure: %q", stdout)
	}
}

func TestMeetingsContextUsageErrors(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantStderr string
	}{
		{"no id", []string{"context"}, "exactly one meeting id"},
		{"two ids", []string{"context", "A", "B"}, "exactly one meeting id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newMeetingsFakeNextcloud(t, serveCatalogAndOpus(oneMeetingCatalog, []byte("opus")))
			code, _, stderr := runMeetingsCLI(t, fake.server.URL, tc.args...)
			if code != 2 {
				t.Fatalf("exit=%d, want 2 (stderr=%q)", code, stderr)
			}
			if !strings.Contains(stderr, tc.wantStderr) {
				t.Errorf("stderr=%q, want %q", stderr, tc.wantStderr)
			}
		})
	}
}
