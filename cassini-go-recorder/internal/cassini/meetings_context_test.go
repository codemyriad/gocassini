package cassini

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	inspectpkg "gocassini/internal/inspect"
	"gocassini/internal/meetingcontext"
	"gocassini/internal/portable"
)

// extractedMeetingFixture builds an ExtractedMeeting in memory, so the bundle
// assembly and rendering can be tested without ffprobe or a packed file.
func extractedMeetingFixture(words []inspectpkg.TranscriptWord, summary string) inspectpkg.ExtractedMeeting {
	meeting := inspectpkg.ExtractedMeeting{
		FormatTag: portable.Format,
		Manifest: portable.Manifest{
			Kind:    "cassini-portable-meeting",
			Version: portable.WireVersion,
			Profile: portable.Profile,
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
	bundle := buildMeetingContext("CATALOG-ID-1", extractedMeetingFixture(standupWords(), ""), meetingsCatalogEntry{})

	if bundle.Meeting.ID != "CATALOG-ID-1" {
		t.Errorf("Meeting.ID = %q, want the catalog id", bundle.Meeting.ID)
	}
	if bundle.Version != meetingContextVersion {
		t.Errorf("Version = %q, want %q", bundle.Version, meetingContextVersion)
	}
}

// "Which room was this?" is a question an agent summarising a meeting routinely
// needs answered, and until D-640 the command the docs point it at handed back a
// document that could not answer it.
func TestBuildMeetingContextCarriesTheRoomFromTheCatalogEntry(t *testing.T) {
	bundle := buildMeetingContext("M1", extractedMeetingFixture(standupWords(), ""), meetingsCatalogEntry{
		RoomID: "rm_9f2a1c3d4e5b6a70", RoomName: "Weekly Sync",
	})

	if bundle.Meeting.RoomID != "rm_9f2a1c3d4e5b6a70" || bundle.Meeting.RoomName != "Weekly Sync" {
		t.Errorf("room = %q/%q, want the catalog entry's", bundle.Meeting.RoomID, bundle.Meeting.RoomName)
	}

	var buf bytes.Buffer
	if err := writeOneMeetingContext(&buf, bundle, false); err != nil {
		t.Fatalf("writeMeetingContext: %v", err)
	}
	// The id is what `meetings list --room` accepts and the name is not, so the
	// markdown shows both — an agent that copies the label instead of the id
	// gets an empty list and no explanation.
	if !strings.Contains(buf.String(), "- Room: Weekly Sync (`rm_9f2a1c3d4e5b6a70`)") {
		t.Errorf("markdown does not carry the room:\n%s", buf.String())
	}
}

// The catalog is authoritative because it is what a backfill and a
// reattribution keep current; the file's id is whatever it was last tagged with.
func TestBuildMeetingContextPrefersTheCatalogRoomOverTheFilesOwn(t *testing.T) {
	meeting := extractedMeetingFixture(standupWords(), "")
	meeting.Manifest.Meeting.RoomID = "rm_1111111111111111"

	bundle := buildMeetingContext("M1", meeting, meetingsCatalogEntry{
		RoomID: "rm_2222222222222222", RoomName: "Merged Room",
	})
	if bundle.Meeting.RoomID != "rm_2222222222222222" || bundle.Meeting.RoomName != "Merged Room" {
		t.Errorf("room = %q/%q, want the catalog's", bundle.Meeting.RoomID, bundle.Meeting.RoomName)
	}

	// The file remains the id fallback, while the mutable name is catalog-only.
	fallback := buildMeetingContext("M1", meeting, meetingsCatalogEntry{})
	if fallback.Meeting.RoomID != "rm_1111111111111111" || fallback.Meeting.RoomName != "" {
		t.Errorf("fallback room = %q/%q, want file id and no name", fallback.Meeting.RoomID, fallback.Meeting.RoomName)
	}
}

func TestBuildMeetingContextOmitsTheRoomLineWhenThereIsNoRoom(t *testing.T) {
	// A non-Talk job, a --simulate run, an import: no room is an ordinary state
	// and must not render as an empty or dashed field.
	bundle := buildMeetingContext("M1", extractedMeetingFixture(standupWords(), ""), meetingsCatalogEntry{})
	var buf bytes.Buffer
	if err := writeOneMeetingContext(&buf, bundle, false); err != nil {
		t.Fatalf("writeMeetingContext: %v", err)
	}
	if strings.Contains(buf.String(), "- Room:") {
		t.Errorf("markdown carries a room line for a meeting with no room:\n%s", buf.String())
	}
}

// The provenance marker is the guard against an agent presenting derived prose
// as an edited transcript, so it is asserted explicitly.
func TestBuildMeetingContextMarksTheTranscriptAsDerived(t *testing.T) {
	bundle := buildMeetingContext("M1", extractedMeetingFixture(standupWords(), ""), meetingsCatalogEntry{})

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
	bundle := buildMeetingContext("M1", extractedMeetingFixture(standupWords(), "# Summary\n"), meetingsCatalogEntry{})

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
	bundle := buildMeetingContext("M1", extractedMeetingFixture(standupWords(), ""), meetingsCatalogEntry{})

	if bundle.Summary.Present {
		t.Errorf("Summary.Present = true, want false: %+v", bundle.Summary)
	}
	if bundle.Summary.Markdown != "" {
		t.Errorf("Summary.Markdown = %q, want empty", bundle.Summary.Markdown)
	}
}

func TestWriteMeetingContextMarkdown(t *testing.T) {
	bundle := buildMeetingContext("M1", extractedMeetingFixture(standupWords(), "# Summary\n\n- Ship it.\n"), meetingsCatalogEntry{})

	var out bytes.Buffer
	if err := writeOneMeetingContext(&out, bundle, false); err != nil {
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
	bundle := buildMeetingContext("M1", extractedMeetingFixture(standupWords(), ""), meetingsCatalogEntry{})

	var out bytes.Buffer
	if err := writeOneMeetingContext(&out, bundle, false); err != nil {
		t.Fatalf("writeMeetingContext: %v", err)
	}
	if !strings.Contains(out.String(), "_No summary was generated for this meeting._") {
		t.Errorf("expected the no-summary line:\n%s", out.String())
	}
}

func TestWriteMeetingContextMarkdownHandlesASilentMeeting(t *testing.T) {
	bundle := buildMeetingContext("M1", extractedMeetingFixture(nil, ""), meetingsCatalogEntry{})

	var out bytes.Buffer
	if err := writeOneMeetingContext(&out, bundle, false); err != nil {
		t.Fatalf("writeMeetingContext: %v", err)
	}
	if !strings.Contains(out.String(), "_This meeting has no transcribed speech._") {
		t.Errorf("expected the no-speech line:\n%s", out.String())
	}
}

func TestWriteMeetingContextJSON(t *testing.T) {
	bundle := buildMeetingContext("M1", extractedMeetingFixture(standupWords(), "# Summary\n"), meetingsCatalogEntry{})

	var out bytes.Buffer
	if err := writeOneMeetingContext(&out, bundle, true); err != nil {
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
	bundle := buildMeetingContext("M1", extractedMeetingFixture(standupWords(), summary), meetingsCatalogEntry{})

	var out bytes.Buffer
	if err := writeOneMeetingContext(&out, bundle, false); err != nil {
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
		{"no id", []string{"context"}, "at least one meeting id"},
		{"an empty id", []string{"context", "   "}, "must not be empty"},
		// A repeated id is a mistake worth refusing: the same transcript twice
		// spends the context budget on one meeting while reading as two.
		{"the same id twice", []string{"context", "A", "A"}, "was given more than once"},
		// --keep-opus names one file, so it cannot hold a bundle.
		{"--keep-opus with several ids", []string{"context", "A", "B", "--keep-opus", "/tmp/x.opus"}, "cannot hold 2 meetings"},
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

// --out " " must not quietly fall back to stdout while also claiming a file was
// written — that emitted two documents on stdout and lost the flag silently.
func TestMeetingsContextRejectsAWhitespaceOutPath(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalogAndOpus(oneMeetingCatalog, []byte("opus")))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "context", "MEETING1", "--json", "--out", "   ")

	if strings.Contains(stdout, "meeting_context ->") {
		t.Errorf("must not claim a file was written:\n%s", stdout)
	}
	// The download is not a real .opus, so this fails at extraction — the point is
	// that it does not report having written a file it did not write.
	if code == 0 && strings.Count(stdout, meetingContextVersion) > 1 {
		t.Errorf("emitted more than one document on stdout:\n%s", stdout)
	}
	_ = stderr
}

// A speaker id padded with whitespace must still find its declared label.
func TestBuildMeetingContextMatchesSpeakerLabelsDespiteWhitespace(t *testing.T) {
	meeting := extractedMeetingFixture(wordsAt(" spk1 ", 0, 200, "hello", "there"), "")
	meeting.Manifest.Speakers = []portable.Speaker{{ID: " spk1 ", Label: "Erlich"}}

	bundle := buildMeetingContext("M1", meeting, meetingsCatalogEntry{})

	if len(bundle.Segments) != 1 {
		t.Fatalf("got %d segments, want 1", len(bundle.Segments))
	}
	if bundle.Segments[0].SpeakerLabel != "Erlich" {
		t.Errorf("SpeakerLabel = %q, want Erlich", bundle.Segments[0].SpeakerLabel)
	}
}

// updateContextGolden rewrites the fixtures under testdata/context instead of
// comparing against them. Regenerate with:
//
//	go test ./internal/cassini -run Golden -update-golden
//
// A diff in those files is the review signal that the published
// cassini.meetings.context.v1 bytes changed — the version string is what
// consumers pin, and until these fixtures existed nothing made a change to the
// shape visible.
var updateContextGolden = flag.Bool("update-golden", false, "rewrite the context golden fixtures from the current output")

// goldenContextFixture is the representative meeting the golden files pin.
//
// It is its own fixture rather than a reuse of standupWords(): the goldens
// define "the v1 bytes did not change", so what they cover must not drift when
// an unrelated test wants different words. It carries every optional field the
// render can emit — room, created-at, duration, a speaker roster, a summary
// with its own headings to demote — plus a segment past the hour mark, which is
// the only way the H:MM:SS citation form gets exercised.
func goldenContextFixture() meetingContext {
	words := wordsAt("spk1", 5_000, 200, "we", "should", "ship", "the", "agent", "surface")
	words = append(words, wordsAt("spk2", 12_400, 200, "agreed", "let's", "do", "it")...)
	words = append(words, wordsAt("spk1", 3_642_000, 200, "then", "we", "are", "done")...)
	summary := "## Decisions\n\n- Ship the agent surface.\n\n## Actions\n\n- Erlich: open the PR.\n"
	return buildMeetingContext("MEETING-GOLDEN-1", extractedMeetingFixture(words, summary), meetingsCatalogEntry{
		RoomID: "rm_9f2a1c3d4e5b6a70", RoomName: "Weekly Sync",
	})
}

func TestMeetingContextGoldenMarkdown(t *testing.T) {
	var out bytes.Buffer
	if err := writeOneMeetingContext(&out, goldenContextFixture(), false); err != nil {
		t.Fatalf("writeMeetingContext: %v", err)
	}
	assertContextGolden(t, "context/single-meeting.md", out.Bytes())
}

func TestMeetingContextGoldenJSON(t *testing.T) {
	var out bytes.Buffer
	if err := writeOneMeetingContext(&out, goldenContextFixture(), true); err != nil {
		t.Fatalf("writeMeetingContext: %v", err)
	}
	assertContextGolden(t, "context/single-meeting.json", out.Bytes())
}

func assertContextGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateContextGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		t.Logf("wrote %s (%d bytes)", path, len(got))
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v\nregenerate with `go test ./internal/cassini -run Golden -update-golden`", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s no longer matches the published bytes.\n--- pinned ---\n%s\n--- produced ---\n%s", path, want, got)
	}
}

// writeOneMeetingContext renders a single meeting the way the command does when
// it is given one id, so the assertions above stay about the bytes rather than
// about bundle plumbing.
func writeOneMeetingContext(out io.Writer, bundle meetingContext, asJSON bool) error {
	return writeMeetingContext(out, meetingcontext.Bundle{Meetings: []meetingContext{bundle}}, asJSON, meetingcontext.RenderOpts{})
}

// goldenContextBundle is the many-meeting document the bundle goldens pin.
//
// The silent meeting sits in the middle on purpose: its rendering ends on a
// line of text rather than a blank line, and "---" written directly beneath a
// line of text is a setext heading, not a rule. Putting it last would never
// exercise the separator that mistake lives in.
func goldenContextBundle() meetingcontext.Bundle {
	silent := extractedMeetingFixture(nil, "")
	silent.Manifest.Meeting.Title = "Silent Retro"
	silent.Manifest.Meeting.CreatedAtUTC = "2026-08-18T09:00:00Z"
	silent.Manifest.Meeting.DurationMS = 60_000

	review := extractedMeetingFixture(wordsAt("spk2", 900, 200, "different", "meeting", "same", "room"), "# Backlog\n\n- Cut the stale cards.\n")
	review.Manifest.Meeting.Title = "Backlog Review"
	review.Manifest.Meeting.CreatedAtUTC = "2026-08-25T14:05:00Z"
	review.Manifest.Meeting.DurationMS = 1_500_000

	return meetingcontext.Bundle{Meetings: []meetingContext{
		goldenContextFixture(),
		buildMeetingContext("MEETING-GOLDEN-2", silent, meetingsCatalogEntry{}),
		buildMeetingContext("MEETING-GOLDEN-3", review, meetingsCatalogEntry{RoomID: "rm_9f2a1c3d4e5b6a70", RoomName: "Weekly Sync"}),
	}}
}

// The citation form is off by default because it changes bytes consumers have
// pinned; this golden is what --timestamps produces when it is on.
func TestMeetingContextGoldenMarkdownWithTimestamps(t *testing.T) {
	rendered, err := meetingcontext.RenderMarkdown(
		meetingcontext.Bundle{Meetings: []meetingContext{goldenContextFixture()}},
		meetingcontext.RenderOpts{Timestamps: true},
	)
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	assertContextGolden(t, "context/single-meeting-timestamps.md", []byte(rendered))
}

func TestMeetingContextBundleGoldenMarkdown(t *testing.T) {
	var out bytes.Buffer
	if err := writeMeetingContext(&out, goldenContextBundle(), false, meetingcontext.RenderOpts{}); err != nil {
		t.Fatalf("writeMeetingContext: %v", err)
	}
	assertContextGolden(t, "context/three-meetings.md", out.Bytes())
}

func TestMeetingContextBundleGoldenJSON(t *testing.T) {
	var out bytes.Buffer
	if err := writeMeetingContext(&out, goldenContextBundle(), true, meetingcontext.RenderOpts{}); err != nil {
		t.Fatalf("writeMeetingContext: %v", err)
	}
	assertContextGolden(t, "context/three-meetings.json", out.Bytes())
}

// A one-meeting bundle must still be the bare v1 document, not a one-element
// container: that is the shape every existing consumer parses, and the golden
// above is only meaningful while this holds.
func TestMeetingContextSingleMeetingJSONIsNotWrapped(t *testing.T) {
	var out bytes.Buffer
	if err := writeOneMeetingContext(&out, goldenContextFixture(), true); err != nil {
		t.Fatalf("writeOneMeetingContext: %v", err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatalf("parse JSON: %v\n%s", err, out.String())
	}
	if _, wrapped := document["meetings"]; wrapped {
		t.Errorf("a single meeting must not gain a meetings array:\n%s", out.String())
	}
	if _, ok := document["meeting"]; !ok {
		t.Errorf("a single meeting must stay the bare v1 document:\n%s", out.String())
	}
}

// TestContextBundleKeysAreDeclaredBySchema is the guard against the one drift
// this format cannot absorb: adding a field to the bundle and forgetting the
// published schema, which is declared with "additionalProperties": false and so
// is invalidated by any key it does not name.
//
// It is the same guard the portable manifest has, for the same reason: the
// schema is a document nothing else in CI validates, so drift would be found by
// a consumer rather than by us. It reads the real schema file and a really
// produced bundle rather than comparing against a list, which would be a third
// thing to keep in step.
func TestContextBundleKeysAreDeclaredBySchema(t *testing.T) {
	schemaPath := "../../../spec/cassini-meetings-context-v1.schema.json"
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read %s: %v", schemaPath, err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("parse %s: %v", schemaPath, err)
	}

	var out bytes.Buffer
	if err := writeMeetingContext(&out, goldenContextBundle(), true, meetingcontext.RenderOpts{}); err != nil {
		t.Fatalf("writeMeetingContext: %v", err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatalf("parse the produced bundle: %v", err)
	}
	assertSchemaDeclares(t, schema, schemaPath, "multiMeetingDocument", document)

	var container struct {
		Meetings []map[string]json.RawMessage `json:"meetings"`
	}
	if err := json.Unmarshal(out.Bytes(), &container); err != nil {
		t.Fatalf("parse the produced meetings: %v", err)
	}
	if len(container.Meetings) == 0 {
		t.Fatal("the produced bundle has no meetings")
	}
	for _, meeting := range container.Meetings {
		assertSchemaDeclares(t, schema, schemaPath, "meetingContext", meeting)
		assertNestedSchemaDeclares(t, schema, schemaPath, "meeting", meeting["meeting"])
		assertNestedSchemaDeclares(t, schema, schemaPath, "summary", meeting["summary"])
		for _, key := range []struct{ field, def string }{{"speakers", "speaker"}, {"segments", "segment"}} {
			var items []json.RawMessage
			if body, ok := meeting[key.field]; ok {
				if err := json.Unmarshal(body, &items); err != nil {
					t.Fatalf("parse %s: %v", key.field, err)
				}
			}
			for _, item := range items {
				assertNestedSchemaDeclares(t, schema, schemaPath, key.def, item)
			}
		}
	}
}

func assertNestedSchemaDeclares(t *testing.T, schema map[string]any, schemaPath, def string, body json.RawMessage) {
	t.Helper()
	if len(body) == 0 {
		return
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil {
		t.Fatalf("parse the produced %s: %v", def, err)
	}
	assertSchemaDeclares(t, schema, schemaPath, def, object)
}

func assertSchemaDeclares(t *testing.T, schema map[string]any, schemaPath, def string, object map[string]json.RawMessage) {
	t.Helper()
	declaredBy := nested(schema, "$defs", def)
	if declaredBy == nil {
		t.Fatalf("%s has no $defs/%s", schemaPath, def)
	}
	// The check only means anything while this is false; if it is ever relaxed,
	// this test should be reconsidered rather than silently pass.
	if additional, ok := declaredBy["additionalProperties"].(bool); !ok || additional {
		t.Fatalf("%s: %s.additionalProperties is not false; this test assumes it is", schemaPath, def)
	}
	declared, _ := declaredBy["properties"].(map[string]any)
	var undeclared []string
	for key := range object {
		if _, ok := declared[key]; !ok {
			undeclared = append(undeclared, key)
		}
	}
	sort.Strings(undeclared)
	if len(undeclared) > 0 {
		t.Errorf("%s does not declare %s field(s) a produced bundle emits: %v", schemaPath, def, undeclared)
	}
}

const bundlableMeetingCatalog = `{
  "version": "cassini.viewer.catalog.v1",
  "meetings": [
    {"id": "MEETING1", "title": "Daily Standup", "dateLabel": "2026-08-11 10:32",
     "audioPath": "./meetings/MEETING1.opus", "speakerCount": 1, "segmentCount": 2},
    {"id": "MEETING2", "title": "Backlog Review", "dateLabel": "2026-08-18 09:00",
     "audioPath": "./meetings/MEETING2.opus", "speakerCount": 1, "segmentCount": 2}
  ]
}`

// serveTwoMeetings answers both meetings' asset routes with the same published
// bytes. What these tests exercise is the bundling, not the audio, and the two
// stay distinguishable because the bundle takes each meeting's id from the
// catalog rather than from the file.
func serveTwoMeetings(opusBody []byte) func(w http.ResponseWriter, r *http.Request) {
	const assetPrefix = "/index.php/apps/app_api/proxy/gocassini/published/meetings/"
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case meetingsTestCatalogPath:
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Cassini-Meeting-Source", "nextcloud-files")
			fmt.Fprint(w, bundlableMeetingCatalog)
		case assetPrefix + "MEETING1.opus", assetPrefix + "MEETING2.opus":
			w.Header().Set("Content-Type", "audio/ogg")
			w.Header().Set("X-Cassini-Meeting-Source", "nextcloud-files")
			_, _ = w.Write(opusBody)
		default:
			http.NotFound(w, r)
		}
	}
}

// packedOpusForContext packs the shared meeting fixture once and returns its
// published bytes.
func packedOpusForContext(t *testing.T) []byte {
	t.Helper()
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
	return published
}

// The bundle is the artifact the insight feature is built on: several meetings,
// one document, one question asked of all of them.
func TestMeetingsContextBundlesSeveralMeetingsIntoOneDocument(t *testing.T) {
	requireFFMediaTools(t)
	fake := newMeetingsFakeNextcloud(t, serveTwoMeetings(packedOpusForContext(t)))

	// Asked for in reverse catalog order, because the document's order is the
	// caller's order — that is what makes a bundle reproducible from the command
	// line that produced it.
	t.Run("markdown", func(t *testing.T) {
		code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "context", "MEETING2", "MEETING1")
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		if want := 1; strings.Count(stdout, "\n\n---\n\n") != want {
			t.Errorf("expected %d separator between two meetings:\n%s", want, stdout)
		}
		second, first := strings.Index(stdout, "- Meeting id: `MEETING2`"), strings.Index(stdout, "- Meeting id: `MEETING1`")
		if second < 0 || first < 0 {
			t.Fatalf("both meetings should be in the document:\n%s", stdout)
		}
		if second > first {
			t.Errorf("the document is not in the order the ids were given:\n%s", stdout)
		}
	})

	t.Run("json", func(t *testing.T) {
		code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "context", "MEETING2", "MEETING1", "--json")
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		// Decoding through the published contract rather than a local struct is
		// the point: what the command writes is what a consumer package reads.
		bundle, err := meetingcontext.DecodeJSON([]byte(stdout))
		if err != nil {
			t.Fatalf("DecodeJSON: %v\n%s", err, stdout)
		}
		if len(bundle.Meetings) != 2 {
			t.Fatalf("got %d meetings, want 2", len(bundle.Meetings))
		}
		if bundle.Meetings[0].Meeting.ID != "MEETING2" || bundle.Meetings[1].Meeting.ID != "MEETING1" {
			t.Errorf("ids = %q, %q; want MEETING2, MEETING1", bundle.Meetings[0].Meeting.ID, bundle.Meetings[1].Meeting.ID)
		}
	})

	t.Run("--timestamps cites each passage", func(t *testing.T) {
		cited := regexp.MustCompile(`\[\d\d:\d\d\]`)

		_, plain, _ := runMeetingsCLI(t, fake.server.URL, "context", "MEETING1")
		if cited.MatchString(plain) {
			t.Errorf("citations must be off by default:\n%s", plain)
		}

		code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "context", "MEETING1", "--timestamps")
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		if !cited.MatchString(stdout) {
			t.Errorf("--timestamps produced no MM:SS citation:\n%s", stdout)
		}
	})
}

// A bundle is all of its meetings or none. Dropping the one id the caller
// cannot read would answer a question asked of two meetings using one, and look
// right doing it.
func TestMeetingsContextFailsTheWholeRunWhenOneIDIsUnreadable(t *testing.T) {
	requireFFMediaTools(t)
	fake := newMeetingsFakeNextcloud(t, serveTwoMeetings(packedOpusForContext(t)))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "context", "MEETING1", "SOMEONE-ELSES", "MEETING2")

	if code != 1 {
		t.Fatalf("exit=%d, want 1 (stderr=%q)", code, stderr)
	}
	if strings.Contains(stdout, meetingContextVersion) || strings.Contains(stdout, "# ") {
		t.Errorf("no document may be emitted when the set is incomplete:\n%s", stdout)
	}
	// Which id failed has to be said out loud: the 404 wording is deliberately
	// the same for "absent" and "you may not read it", so it names no id itself.
	if !strings.Contains(stderr, `"SOMEONE-ELSES"`) {
		t.Errorf("stderr does not name the failing id: %q", stderr)
	}
}

// The document's order is the caller's order, whichever side of the flags each
// id was written on. Rotating a run of ids past the flags — what the shared
// one-positional helper does — would silently reverse them, and a bundle whose
// meetings came back in a different order than they were asked for is not
// reproducible from the command line that produced it.
func TestSplitLeadingMeetingIDsKeepsTheCallersOrder(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"ids first", []string{"A", "B", "--json"}, []string{"A", "B"}},
		{"flags first", []string{"--json", "A", "B"}, []string{"A", "B"}},
		{"ids on both sides of a flag", []string{"A", "--json", "B"}, []string{"A", "B"}},
		{"no ids at all", []string{"--json"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leading, rest := splitLeadingMeetingIDs(tc.args)

			fs := flag.NewFlagSet("context", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			fs.Bool("json", false, "")
			if err := fs.Parse(rest); err != nil {
				t.Fatalf("parse %v: %v", rest, err)
			}
			got, err := meetingContextIDs(append(leading, fs.Args()...))
			if err != nil {
				t.Fatalf("meetingContextIDs: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}
