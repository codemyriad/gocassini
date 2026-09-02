package cassini

import (
	"fmt"
	"strings"

	"gocassini/internal/portable"
)

// buildPortableMeetingDraft2Tags is the simple case: derive a draft-2 OpusTag
// map from a Manifest's single inline transcript. Used by tests and kept as a
// thin wrapper over the full source-aware builder below.
func buildPortableMeetingDraft2Tags(manifest portable.Manifest) (map[string]string, error) {
	return buildPortableMeetingDraft2TagsFromSource(manifest, portableMeetingSource{})
}

// buildPortableMeetingDraft2TagsFromSource builds the draft-2 OpusTag map for
// a portable meeting bundle. Draft 2 is not written any more; this survives
// for the tests that cover reading a file it produced.
func buildPortableMeetingDraft2TagsFromSource(manifest portable.Manifest, source portableMeetingSource) (map[string]string, error) {
	transcripts, defaultID, err := assembleTranscriptInputs(manifest, source)
	if err != nil {
		return nil, err
	}

	encoded, err := portable.EncodeDraft2Manifest(manifest, transcripts, portable.DefaultPayloadChunkSize)
	if err != nil {
		return nil, fmt.Errorf("encode draft-2 portable manifest: %w", err)
	}
	return portable.BuildDraft2OpusTags(manifest, encoded, defaultID), nil
}

// buildPortableMeetingTagsFromSource builds the OpusTag map every packed file
// gets: the published format, version 1. If the bundle has
// source.AdditionalTranscripts (a multi-transcript manifest.json), each entry
// becomes a separate transcript with its own provenance. Otherwise the
// bundle's single transcript.words.v1.json is synthesized as one raw-asr
// entry.
func buildPortableMeetingTagsFromSource(manifest portable.Manifest, source portableMeetingSource) (map[string]string, error) {
	transcripts, defaultID, err := assembleTranscriptInputs(manifest, source)
	if err != nil {
		return nil, err
	}

	encoded, err := portable.EncodePublishedManifest(manifest, transcripts, portable.DefaultPayloadChunkSize)
	if err != nil {
		return nil, fmt.Errorf("encode portable meeting manifest: %w", err)
	}
	return portable.BuildPublishedOpusTags(manifest, encoded, defaultID), nil
}

// assembleTranscriptInputs returns the list of transcript inputs plus the
// id of the default raw-ASR entry. The default rule: if any input declares
// Default=true, that's the default; otherwise the first raw-ASR input wins.
func assembleTranscriptInputs(manifest portable.Manifest, source portableMeetingSource) ([]portable.TranscriptInput, string, error) {
	additional := source.AdditionalTranscripts
	if len(additional) == 0 {
		// Single-transcript bundle: synthesize one raw-asr entry from the
		// inline manifest.Transcript the caller already populated.
		input := portable.TranscriptInput{
			ID:           portable.RoleRawASR,
			Role:         portable.RoleRawASR,
			Default:      true,
			Language:     firstNonEmptyV2(manifest.Transcript.Language, manifest.Meeting.Language),
			CreatedAtUTC: manifest.Meeting.ProcessedAtUTC,
			Body: portable.TranscriptBody{
				Format:    manifest.Transcript.Format,
				Language:  manifest.Transcript.Language,
				WordCount: manifest.Transcript.WordCount,
				Items:     manifest.Transcript.Items,
			},
			Provenance: provenanceSpeechToText(manifest),
		}
		return []portable.TranscriptInput{input}, portable.RoleRawASR, nil
	}

	inputs := make([]portable.TranscriptInput, 0, len(additional))
	for _, entry := range additional {
		role := entry.Role
		if role == "" {
			role = portable.RoleRawASR
		}
		items := flattenPortableTranscriptItems(entry.Transcript)
		body := portable.TranscriptBody{
			Format:    "cassini.words.v1",
			Language:  entry.Language,
			WordCount: len(items),
			Items:     items,
		}
		inputs = append(inputs, portable.TranscriptInput{
			ID:           entry.ID,
			Role:         role,
			Default:      entry.Default,
			Language:     entry.Language,
			CreatedAtUTC: manifest.Meeting.ProcessedAtUTC,
			Body:         body,
			Provenance:   entry.Provenance,
		})
	}

	defaultID := pickV2DefaultRawID(inputs)
	if defaultID == "" {
		return nil, "", fmt.Errorf("v2 multi-transcript bundle has no raw-ASR entry to use as default")
	}
	return inputs, defaultID, nil
}

func pickV2DefaultRawID(inputs []portable.TranscriptInput) string {
	for _, input := range inputs {
		if input.Default && isRawRole(input.Role) {
			return input.ID
		}
	}
	for _, input := range inputs {
		if isRawRole(input.Role) {
			return input.ID
		}
	}
	return ""
}

func isRawRole(role string) bool {
	switch role {
	case portable.RoleRawASR, portable.RoleHumanCorrected, portable.RoleTranslation, portable.RoleScripted:
		return true
	default:
		return false
	}
}

func provenanceSpeechToText(manifest portable.Manifest) *portable.ProcessingStep {
	if manifest.Provenance == nil {
		return nil
	}
	return manifest.Provenance.SpeechToText
}

func firstNonEmptyV2(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
