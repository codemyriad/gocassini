package operator

import (
	"encoding/json"
	"fmt"
)

const (
	sourceStoragePolicy         = "storage-policy"
	sourceDeleteAfterProcessing = "delete-after-processing"
)

// Persisted at admission, independently of capture provenance. Legacy jobs
// without this snapshot keep their existing publication and retention behavior.
type recordingProcessingPolicy struct {
	MeetingFormat   string `json:"meeting_format"`
	SourceRetention string `json:"source_retention"`
	// Disposal jobs must not lose transcription when global settings change.
	Transcription *STTSettings `json:"transcription,omitempty"`
}

func normalizeSourceRetention(s *STTSettings) error {
	if s.SourceRetention == "" {
		s.SourceRetention = sourceStoragePolicy
	}
	switch s.SourceRetention {
	case sourceStoragePolicy:
		return nil
	case sourceDeleteAfterProcessing:
		if s.MeetingFormat != "json" {
			return fmt.Errorf("deleting source media requires transcription-only publication")
		}
		if s.RetainVideo {
			return fmt.Errorf("deleting source media requires audio-only capture")
		}
		if !s.TranscriptionEnabled || s.ActiveModel == "" || s.ActiveRevision == "" {
			return fmt.Errorf("deleting source media requires enabled, prepared transcription")
		}
		return nil
	default:
		return fmt.Errorf("source_retention must be storage-policy or delete-after-processing")
	}
}

func processingPolicy(job Job) *recordingProcessingPolicy {
	var req TriggerRequest
	if json.Unmarshal([]byte(job.RequestJSON), &req) != nil {
		return nil
	}
	return req.ProcessingPolicy
}

func deletesSourceMedia(job Job) bool {
	p := processingPolicy(job)
	return p != nil && p.SourceRetention == sourceDeleteAfterProcessing
}
