package inspect

import (
	"path/filepath"
	"strings"
	"testing"

	"gocassini/internal/portable"
)

// TestExtractMeetingDraft1RecoversManifestTranscriptAndSummary proves the
// one-pass reader returns everything a context bundle needs out of a draft-1
// file: the manifest fields the agent shows the user, the inline words, and
// the summary markdown decoded from the attachment.
func TestExtractMeetingDraft1RecoversManifestTranscriptAndSummary(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	path := createPortableOpusFixture(t, filepath.Join(tmp, "draft1.opus"), portableFixtureOptions{withSummary: true})

	meeting, err := ExtractMeeting(path)
	if err != nil {
		t.Fatalf("ExtractMeeting: %v", err)
	}

	if meeting.FormatTag != portable.Format {
		t.Errorf("FormatTag = %q, want %q", meeting.FormatTag, portable.Format)
	}
	if meeting.Manifest.Version != portable.Draft1WireVersion {
		t.Errorf("Manifest.Version = %d, want %d", meeting.Manifest.Version, portable.Draft1WireVersion)
	}
	if got, want := meeting.Manifest.Meeting.Title, "Weekly Sync"; got != want {
		t.Errorf("Meeting.Title = %q, want %q", got, want)
	}
	if got, want := meeting.Manifest.Meeting.ID, "meeting-20260311-weekly-sync"; got != want {
		t.Errorf("Meeting.ID = %q, want %q", got, want)
	}
	if got, want := meeting.Manifest.Meeting.CreatedAtUTC, "2026-03-11T08:30:00Z"; got != want {
		t.Errorf("Meeting.CreatedAtUTC = %q, want %q", got, want)
	}
	if meeting.Manifest.Meeting.DurationMS <= 0 {
		t.Errorf("Meeting.DurationMS = %d, want > 0", meeting.Manifest.Meeting.DurationMS)
	}

	// The summary text lives in attachments[], base64.StdEncoding — decoding it
	// as base64url would silently yield garbage rather than fail.
	if got, want := string(meeting.SummaryMarkdown), "# Meeting Summary\n"; got != want {
		t.Errorf("SummaryMarkdown = %q, want %q", got, want)
	}
	if got, want := meeting.SummaryFormat(), "markdown"; got != want {
		t.Errorf("SummaryFormat() = %q, want %q", got, want)
	}
	if got, want := meeting.SummaryModel(), "summary-model"; got != want {
		t.Errorf("SummaryModel() = %q, want %q", got, want)
	}

	if got, want := meeting.Transcript.WordCount, 2; got != want {
		t.Errorf("Transcript.WordCount = %d, want %d", got, want)
	}
	texts := make([]string, 0, len(meeting.Transcript.Words))
	for _, word := range meeting.Transcript.Words {
		texts = append(texts, word.Text)
	}
	if got, want := strings.Join(texts, " "), "Hello team"; got != want {
		t.Errorf("recovered words = %q, want %q", got, want)
	}

	if got, want := meeting.SpeakerLabels()["spk1"], "Silvio"; got != want {
		t.Errorf("SpeakerLabels()[spk1] = %q, want %q", got, want)
	}
}

// TestExtractMeetingTellsADraft1FileFromAPublishedOneByShape is the regression
// test for the version-number collision. The published format is version 1;
// so was the first, never-published draft. A reader that decides what a file
// contains from `version` therefore looks at a draft-1 file, concludes it has
// a `transcripts` index, finds none and returns a meeting with no words in it.
//
// The fixture below is a genuine draft-1 file: one inline transcript,
// matchPolicy exact-pcm, format tag org.cassini.portable-meeting/1, version 1
// — the same format tag and the same version number a file this producer
// writes today carries. What separates them is the shape, and the words this
// test recovers came out of the inline object.
func TestExtractMeetingTellsADraft1FileFromAPublishedOneByShape(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	path := createPortableOpusFixture(t, filepath.Join(tmp, "draft1.opus"), portableFixtureOptions{})

	meeting, err := ExtractMeeting(path)
	if err != nil {
		t.Fatalf("ExtractMeeting: %v", err)
	}

	// The collision itself: tag and version are the published ones.
	if meeting.FormatTag != portable.Format {
		t.Fatalf("FormatTag = %q, want the published tag %q", meeting.FormatTag, portable.Format)
	}
	if meeting.Manifest.Version != portable.WireVersion {
		t.Fatalf("Manifest.Version = %d, want %d", meeting.Manifest.Version, portable.WireVersion)
	}

	// And the shape, which is what actually decides.
	if meeting.Manifest.IsMultiTranscript() {
		t.Fatalf("draft-1 manifest reported a transcripts index: %+v", meeting.Manifest.Transcripts)
	}
	if got, want := meeting.Manifest.Integrity.MatchPolicy, portable.LegacyAudioMatchPolicyPCM; got != want {
		t.Errorf("Integrity.MatchPolicy = %q, want %q", got, want)
	}

	if got, want := meeting.Transcript.WordCount, 2; got != want {
		t.Fatalf("Transcript.WordCount = %d, want %d", got, want)
	}
	texts := make([]string, 0, len(meeting.Transcript.Words))
	for _, word := range meeting.Transcript.Words {
		texts = append(texts, word.Text)
	}
	if got, want := strings.Join(texts, " "), "Hello team"; got != want {
		t.Errorf("recovered words = %q, want %q", got, want)
	}
}

// TestExtractMeetingDraft2RecoversChunkedTranscriptAndSummary covers the
// multi-transcript shape: a draft-2 file whose transcript body lives in its
// own CASSINI_TX_<ID>_PAYLOAD_* chunk set.
func TestExtractMeetingDraft2RecoversChunkedTranscriptAndSummary(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	words := []string{"Hello", "team", "lantern", "festival", "tonight"}
	path := createPortableDraft2OpusFixtureWith(t, filepath.Join(tmp, "v2.opus"), portableDraft2FixtureOptions{
		words:       words,
		withSummary: true,
		summaryBody: "# Lantern Festival\n\n- Decided the date.\n",
	})

	meeting, err := ExtractMeeting(path)
	if err != nil {
		t.Fatalf("ExtractMeeting: %v", err)
	}

	if meeting.FormatTag != portable.FormatDraft2 {
		t.Errorf("FormatTag = %q, want %q", meeting.FormatTag, portable.FormatDraft2)
	}
	if meeting.Manifest.Version != portable.Draft2WireVersion {
		t.Errorf("Manifest.Version = %d, want %d", meeting.Manifest.Version, portable.Draft2WireVersion)
	}
	if got, want := meeting.Manifest.Meeting.Title, "Lantern Festival"; got != want {
		t.Errorf("Meeting.Title = %q, want %q", got, want)
	}
	if got, want := meeting.Transcript.TranscriptID, portable.RoleRawASR; got != want {
		t.Errorf("Transcript.TranscriptID = %q, want %q", got, want)
	}
	if got, want := meeting.Transcript.Role, portable.RoleRawASR; got != want {
		t.Errorf("Transcript.Role = %q, want %q", got, want)
	}
	texts := make([]string, 0, len(meeting.Transcript.Words))
	for _, word := range meeting.Transcript.Words {
		texts = append(texts, word.Text)
	}
	if got, want := strings.Join(texts, " "), strings.Join(words, " "); got != want {
		t.Errorf("recovered words = %q, want %q", got, want)
	}
	if got, want := string(meeting.SummaryMarkdown), "# Lantern Festival\n\n- Decided the date.\n"; got != want {
		t.Errorf("SummaryMarkdown = %q, want %q", got, want)
	}
}

// TestExtractMeetingWithoutSummaryIsNotAnError pins that a meeting published by
// a deployment with no summariser configured still extracts cleanly. Summaries
// are LLM-gated, so this is the common case, not a degraded one.
func TestExtractMeetingWithoutSummaryIsNotAnError(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	path := createPortableDraft2OpusFixture(t, filepath.Join(tmp, "no-summary.opus"), []string{"Hello", "team"})

	meeting, err := ExtractMeeting(path)
	if err != nil {
		t.Fatalf("ExtractMeeting: %v", err)
	}
	if meeting.SummaryMarkdown != nil {
		t.Errorf("SummaryMarkdown = %q, want nil", meeting.SummaryMarkdown)
	}
	if got := meeting.SummaryFormat(); got != "" {
		t.Errorf("SummaryFormat() = %q, want empty", got)
	}
	if got := meeting.SummaryModel(); got != "" {
		t.Errorf("SummaryModel() = %q, want empty", got)
	}
	if len(meeting.Transcript.Words) != 2 {
		t.Errorf("recovered %d words, want 2", len(meeting.Transcript.Words))
	}
}

// TestExtractMeetingRejectsPlainOpus keeps the not-a-portable-meeting error
// distinguishable from a decode failure: an agent pointed at ordinary audio
// should be told that, not handed an empty bundle.
func TestExtractMeetingRejectsPlainOpus(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	path := createTestOpus(t, filepath.Join(tmp, "plain.opus"))

	_, err := ExtractMeeting(path)
	if err == nil {
		t.Fatal("expected an error for a plain .opus with no CASSINI_FORMAT")
	}
	if !strings.Contains(err.Error(), "not a portable meeting") {
		t.Errorf("error = %v, want it to mention 'not a portable meeting'", err)
	}
}

// TestSummaryMarkdownFromAttachmentsIgnoresOtherAttachments guards the lookup
// against a future file carrying more than one attachment.
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

	// A corrupt attachment must not masquerade as an empty summary.
	if got := summaryMarkdownFromAttachments([]map[string]any{
		{"name": "summary.md", "contentBase64": "!!!not base64!!!"},
	}); got != nil {
		t.Errorf("summaryMarkdownFromAttachments(corrupt) = %q, want nil", got)
	}
}
