package inspect

import (
	"path/filepath"
	"strings"
	"testing"

	"gocassini/internal/portable"
)

func TestExtractMeetingPublishedV1RecoversChunkedTranscriptAndSummary(t *testing.T) {
	requireFFMediaTools(t)
	words := []string{"Hello", "team", "lantern", "festival", "tonight"}
	path := createPortableOpusFixture(t, filepath.Join(t.TempDir(), "meeting.opus"), portableFixtureOptions{
		words: words, withSummary: true,
	})

	meeting, err := ExtractMeeting(path)
	if err != nil {
		t.Fatalf("ExtractMeeting: %v", err)
	}
	if meeting.FormatTag != portable.Format {
		t.Errorf("FormatTag = %q, want %q", meeting.FormatTag, portable.Format)
	}
	if meeting.Manifest.Version != portable.WireVersion || meeting.Manifest.Profile != portable.Profile {
		t.Errorf("manifest contract = %d/%q", meeting.Manifest.Version, meeting.Manifest.Profile)
	}
	if len(meeting.Manifest.Transcripts) != 1 {
		t.Fatalf("manifest transcript descriptors = %d, want 1", len(meeting.Manifest.Transcripts))
	}
	if meeting.Transcript.TranscriptID != portable.RoleRawASR || meeting.Transcript.Role != portable.RoleRawASR {
		t.Errorf("default transcript = %q/%q", meeting.Transcript.TranscriptID, meeting.Transcript.Role)
	}
	texts := make([]string, 0, len(meeting.Transcript.Words))
	for _, word := range meeting.Transcript.Words {
		texts = append(texts, word.Text)
	}
	if got, want := strings.Join(texts, " "), strings.Join(words, " "); got != want {
		t.Errorf("recovered words = %q, want %q", got, want)
	}
	if got, want := string(meeting.SummaryMarkdown), "# Meeting Summary\n"; got != want {
		t.Errorf("SummaryMarkdown = %q, want %q", got, want)
	}
	if meeting.SummaryFormat() != "markdown" || meeting.SummaryModel() != "summary-model" {
		t.Errorf("summary metadata = %q/%q", meeting.SummaryFormat(), meeting.SummaryModel())
	}
	if got := meeting.SpeakerLabels()["spk1"]; got != "Silvio" {
		t.Errorf("speaker label = %q, want Silvio", got)
	}
}

func TestExtractMeetingWithoutSummaryIsNotAnError(t *testing.T) {
	requireFFMediaTools(t)
	path := createPortableOpusFixture(t, filepath.Join(t.TempDir(), "meeting.opus"), portableFixtureOptions{
		words: []string{"Hello", "team"},
	})

	meeting, err := ExtractMeeting(path)
	if err != nil {
		t.Fatalf("ExtractMeeting: %v", err)
	}
	if meeting.SummaryMarkdown != nil || meeting.SummaryFormat() != "" || meeting.SummaryModel() != "" {
		t.Errorf("unexpected summary: body=%q format=%q model=%q", meeting.SummaryMarkdown, meeting.SummaryFormat(), meeting.SummaryModel())
	}
	if len(meeting.Transcript.Words) != 2 {
		t.Errorf("recovered %d words, want 2", len(meeting.Transcript.Words))
	}
}

func TestExtractMeetingRejectsPlainOpus(t *testing.T) {
	requireFFMediaTools(t)
	path := createTestOpus(t, filepath.Join(t.TempDir(), "plain.opus"))

	_, err := ExtractMeeting(path)
	if err == nil || !strings.Contains(err.Error(), "not a portable meeting") {
		t.Fatalf("error = %v, want not-a-portable-meeting error", err)
	}
}

func TestSummaryMarkdownFromAttachmentsIgnoresOtherAttachments(t *testing.T) {
	got := summaryMarkdownFromAttachments([]map[string]any{
		{"name": "captions.vtt", "mime": "text/vtt", "contentBase64": "V0VCVlRU"},
		{"name": "summary.md", "mime": "text/markdown", "contentBase64": "IyBTdW1tYXJ5Cg=="},
	})
	if want := "# Summary\n"; string(got) != want {
		t.Errorf("summaryMarkdownFromAttachments = %q, want %q", got, want)
	}
	if got := summaryMarkdownFromAttachments(nil); got != nil {
		t.Errorf("summaryMarkdownFromAttachments(nil) = %q, want nil", got)
	}
	if got := summaryMarkdownFromAttachments([]map[string]any{{
		"name": "summary.md", "contentBase64": "!!!not base64!!!",
	}}); got != nil {
		t.Errorf("corrupt summary attachment = %q, want nil", got)
	}
}
