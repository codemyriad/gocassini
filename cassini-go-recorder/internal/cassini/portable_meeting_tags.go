package cassini

import (
	"fmt"

	"gocassini/internal/portable"
)

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
	var inputs []portable.TranscriptInput
	if len(additional) == 0 {
		// A single-transcript bundle uses the same published indexed layout as a
		// bundle carrying several raw transcripts.
		items := flattenPortableTranscriptItems(source.Transcript)
		inputs = append(inputs, portable.TranscriptInput{
			ID:           portable.RoleRawASR,
			Role:         portable.RoleRawASR,
			Default:      true,
			Language:     manifest.Meeting.Language,
			CreatedAtUTC: manifest.Meeting.ProcessedAtUTC,
			Body: portable.TranscriptBody{
				Format:    "cassini.words.v1",
				Language:  manifest.Meeting.Language,
				WordCount: len(items),
				Items:     items,
			},
			Provenance: provenanceSpeechToText(manifest),
		})
	} else {
		inputs = make([]portable.TranscriptInput, 0, len(additional)+2)
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
	}

	defaultID := pickDefaultRawTranscriptID(inputs)
	if defaultID == "" {
		return nil, "", fmt.Errorf("portable meeting bundle has no raw-ASR entry to use as default")
	}
	if source.ReadableTranscript != nil {
		inputs = append(inputs, portable.TranscriptInput{
			ID:                 "readable",
			Role:               portable.RoleReadableCleanup,
			Default:            true,
			Format:             portableDocumentFormat(source.ReadableTranscript),
			Language:           manifest.Meeting.Language,
			CreatedAtUTC:       manifest.Meeting.ProcessedAtUTC,
			SourceTranscriptID: defaultID,
			Body:               source.ReadableTranscript,
			Provenance:         provenanceReadableCleanup(manifest),
		})
	}
	if source.DisplayTranscript != nil {
		inputs = append(inputs, portable.TranscriptInput{
			ID:                 "display",
			Role:               portable.RoleDisplay,
			Default:            true,
			Format:             portableDocumentFormat(source.DisplayTranscript),
			Language:           manifest.Meeting.Language,
			CreatedAtUTC:       manifest.Meeting.ProcessedAtUTC,
			SourceTranscriptID: defaultID,
			Body:               source.DisplayTranscript,
			Provenance:         provenanceDisplayTranscript(manifest),
		})
	}
	return inputs, defaultID, nil
}

func portableDocumentFormat(document map[string]any) string {
	if value, ok := document["format"].(string); ok {
		return value
	}
	if value, ok := document["version"].(string); ok {
		return value
	}
	return ""
}

func pickDefaultRawTranscriptID(inputs []portable.TranscriptInput) string {
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
	case portable.RoleRawASR, portable.RoleHumanCorrected, portable.RoleTranslation:
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

func provenanceReadableCleanup(manifest portable.Manifest) *portable.ProcessingStep {
	if manifest.Provenance == nil {
		return nil
	}
	return manifest.Provenance.ReadableCleanup
}

func provenanceDisplayTranscript(manifest portable.Manifest) *portable.ProcessingStep {
	if manifest.Provenance == nil {
		return nil
	}
	return manifest.Provenance.DisplayTranscript
}
