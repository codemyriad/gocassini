package operator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// Save only the public policy, not the operator's model settings, in the
// published provenance. This is a policy declaration, not a deletion receipt.
func stampMediaPolicy(meeting string, job Job) error {
	var req TriggerRequest
	if err := json.Unmarshal([]byte(job.RequestJSON), &req); err != nil {
		return err
	}
	path := filepath.Join(meeting, "manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var manifest map[string]any
	if err = json.Unmarshal(raw, &manifest); err != nil {
		return err
	}
	provenance, ok := manifest["provenance"].(map[string]any)
	if !ok {
		provenance = map[string]any{}
	}
	provenance["recording"] = map[string]string{"captureMode": req.CaptureMode, "sourceRetention": req.ProcessingPolicy.SourceRetention}
	manifest["provenance"] = provenance
	raw, err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0600)
}
