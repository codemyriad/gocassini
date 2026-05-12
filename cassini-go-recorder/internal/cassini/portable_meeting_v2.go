package cassini

import (
	"fmt"
	"os"
	"strings"

	"gocassini/internal/portable"
)

// portableMeetingV2Enabled reports whether the producer should emit the v2
// multi-transcription format. The flag stays off by default; flip it on with
// CASSINI_FORMAT_V2=1 once the v2-aware viewer is live in production.
//
// Strict-profile rollout (see docs/proposals/multi-transcription-format-plan.md):
// the viewer must ship to production before flipping this flag.
func portableMeetingV2Enabled() bool {
	value := strings.TrimSpace(os.Getenv("CASSINI_FORMAT_V2"))
	switch strings.ToLower(value) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// buildPortableMeetingV2Tags derives the v2 OpusTag map from a v1-shaped
// Manifest. Step 3 of the rollout emits a single transcript entry (id
// "raw-asr") from the bundle's existing transcript.words.v1.json file.
// Multi-transcript bundle input arrives in step 4.
func buildPortableMeetingV2Tags(manifest portable.Manifest) (map[string]string, error) {
	transcripts := []portable.TranscriptInput{
		{
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
		},
	}

	encoded, err := portable.EncodeManifestV2(manifest, transcripts, portable.DefaultPayloadChunkSize)
	if err != nil {
		return nil, fmt.Errorf("encode portable v2 manifest: %w", err)
	}
	return portable.BuildOpusTagsV2(manifest, encoded, portable.RoleRawASR), nil
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
