package inspect

import (
	"encoding/base64"
	"fmt"
	"strings"

	"gocassini/internal/portable"
)

// summaryAttachmentName is the attachment the pack step writes the generated
// meeting summary under (internal/cassini/portable_meeting.go).
const summaryAttachmentName = "summary.md"

// ExtractedMeeting is everything a consumer needs from one portable .opus,
// recovered in a single ffprobe pass: the decoded manifest (title, id, timing,
// speaker labels, summary metadata, provenance), the default words transcript,
// and the summary markdown.
//
// This is the read side of the portable format and the only one in Go —
// internal/portable is encode-only. Adapters that need meeting content (the
// `cassini meetings` CLI, and anything wrapping it later) go through here
// rather than re-deriving the OpusTag layout.
type ExtractedMeeting struct {
	// Manifest is the decoded CASSINI_PAYLOAD_* manifest. Its Version field
	// tells v1 (transcript inline) from v2/v3 (transcript bodies in their own
	// chunk sets) — trust it only on a decoded manifest, never on one built
	// locally, since the normalizers select a wire version explicitly.
	Manifest portable.Manifest

	// FormatTag is CASSINI_FORMAT exactly as the file carries it.
	FormatTag string

	// Transcript is the default raw-words transcript: the same value
	// ExtractTranscriptWords returns.
	Transcript ExtractedTranscript

	// SummaryMarkdown is the decoded summary.md attachment, or nil when the
	// file carries none. Absence is normal, not an error: summaries are
	// LLM-gated and a deployment with no summariser configured publishes
	// transcripts without them.
	SummaryMarkdown []byte
}

// ExtractMeeting reads a published portable .opus back into its manifest, its
// default words transcript, and its summary markdown.
//
// It reuses the same decode path as inspect: probePortableAudio +
// decodePortableMeeting verify the payload's sha256 and byte counts. It
// deliberately does NOT verify audio integrity. Legacy v1/v2 verification
// decodes the whole recording to PCM, while v3 streams the compressed Opus
// packets; callers that need either gate use `cassini inspect` explicitly.
func ExtractMeeting(path string) (ExtractedMeeting, error) {
	meta, err := probePortableAudio(path)
	if err != nil {
		return ExtractedMeeting{}, err
	}
	stream, err := firstAudioStream(meta)
	if err != nil {
		return ExtractedMeeting{}, err
	}
	// For Ogg/Opus ffmpeg reports the CASSINI_* tags as stream tags and leaves
	// the format tag map empty, so both sets must be merged before any lookup.
	tags := mergePortableTags(meta.Format.Tags, stream.Tags)

	formatTag := metadataTag(tags, "CASSINI_FORMAT")
	if formatTag == "" {
		return ExtractedMeeting{}, fmt.Errorf("%s carries no CASSINI_FORMAT metadata (not a portable meeting)", path)
	}
	if !strings.EqualFold(formatTag, portable.Format) && !strings.EqualFold(formatTag, portable.FormatV2) && !strings.EqualFold(formatTag, portable.FormatV3) {
		return ExtractedMeeting{}, fmt.Errorf("unsupported CASSINI_FORMAT=%s", formatTag)
	}

	_, manifest, err := decodePortableMeeting(tags)
	if err != nil {
		return ExtractedMeeting{}, err
	}

	extracted := ExtractedMeeting{
		Manifest:        manifest,
		FormatTag:       formatTag,
		SummaryMarkdown: summaryMarkdownFromAttachments(manifest.Attachments),
	}

	// v1 files carry the words inline in the main manifest.
	if manifest.Version == 1 {
		extracted.Transcript = extractedFromTranscriptBody(
			"transcript",
			manifest.Transcript.Format,
			firstNonEmpty(manifest.Transcript.Language, manifest.Meeting.Language),
			manifest.Transcript.WordCount,
			manifest.Transcript.Items,
		)
		return extracted, nil
	}

	entry, _, ok := defaultWordsTranscriptEntry(tags, manifest)
	if !ok {
		return ExtractedMeeting{}, fmt.Errorf("portable meeting %s declares no default words transcript", path)
	}
	body, _, err := decodeTranscriptBody(tags, entry)
	if err != nil {
		return ExtractedMeeting{}, fmt.Errorf("decode transcript %q body: %w", entry.ID, err)
	}
	transcript := extractedFromTranscriptBody(entry.ID, body.Format, body.Language, body.WordCount, body.Items)
	transcript.Role = entry.Role
	if transcript.Language == "" {
		transcript.Language = entry.Language
	}
	if transcript.Format == "" {
		transcript.Format = entry.Format
	}
	extracted.Transcript = transcript
	return extracted, nil
}

// summaryMarkdownFromAttachments returns the decoded summary.md attachment, or
// nil when the file carries none.
//
// The encoding here is base64.StdEncoding, matching the writer in
// internal/cassini/portable_meeting.go — NOT the base64.RawURLEncoding every
// CASSINI_PAYLOAD_* chunk uses. Decoding one with the other silently yields
// garbage, so writer and reader are kept symmetric on purpose.
func summaryMarkdownFromAttachments(attachments []map[string]any) []byte {
	for _, attachment := range attachments {
		name, _ := attachment["name"].(string)
		if !strings.EqualFold(strings.TrimSpace(name), summaryAttachmentName) {
			continue
		}
		encoded, ok := attachment["contentBase64"].(string)
		if !ok {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			continue
		}
		return raw
	}
	return nil
}

// SummaryFormat reports the summary artifact's declared format (currently
// always "markdown") from the manifest's summary metadata, which is metadata
// only — the summary text itself lives in the attachments, not here.
func (m ExtractedMeeting) SummaryFormat() string {
	return summaryMetadataString(m.Manifest.Summary, "format")
}

// SummaryModel reports the model credited with generating the summary, or ""
// when the manifest does not name one.
func (m ExtractedMeeting) SummaryModel() string {
	if model := summaryMetadataString(m.Manifest.Summary, "model"); model != "" {
		return model
	}
	if m.Manifest.Provenance != nil && m.Manifest.Provenance.MeetingSummary != nil {
		return strings.TrimSpace(m.Manifest.Provenance.MeetingSummary.Model)
	}
	return ""
}

// summaryMetadataString reads one string key out of the open-schema summary
// metadata map, tolerating a non-string value rather than panicking.
func summaryMetadataString(summary map[string]any, key string) string {
	value, ok := summary[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

// SpeakerLabels maps speaker id to display label from the manifest. Ids with no
// declared label are absent from the map; callers fall back to the raw id.
func (m ExtractedMeeting) SpeakerLabels() map[string]string {
	labels := make(map[string]string, len(m.Manifest.Speakers))
	for _, speaker := range m.Manifest.Speakers {
		id := strings.TrimSpace(speaker.ID)
		label := strings.TrimSpace(speaker.Label)
		if id == "" || label == "" {
			continue
		}
		labels[id] = label
	}
	return labels
}
