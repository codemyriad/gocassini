package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Step outcomes: what an attempt's transcription and summary steps did
// (D-854, D-814).
//
// The built bundle says it in manifest.json (processing.transcription,
// processing.meetingSummary). Once an attempt is built the operator copies
// that onto the attempt, so a run that published without a transcript or a
// summary says so instead of reading "done". What the bundle cannot say, the
// operator adds as the detail: the last words of a build that crashed, or the
// resource that ran out when transcription was dropped at the retry ceiling.

// stepOutcomes is the manifest's processing block, as far as the Operator
// shows it.
type stepOutcomes struct {
	TranscriptionStatus string
	TranscriptionReason string
	SummaryStatus       string
	SummaryReason       string
}

// Why the operator dropped transcription from a build, as the recorder's
// CASSINI_TRANSCRIPTION_REASON carries it into processing.transcription.
const (
	// transcriptionReasonFailed: the build crashed after media preparation
	// and was rebuilt without transcription.
	transcriptionReasonFailed = "transcription_failed"
	// transcriptionReasonInsufficientResources: at the retry ceiling there
	// was still not enough memory or GPU for the model, so the audio was
	// published without waiting longer.
	transcriptionReasonInsufficientResources = "insufficient_resources"
	// transcriptionReasonModelUnavailable: the model cannot run at all (not
	// installed, failed its check), which waiting would not change.
	transcriptionReasonModelUnavailable = "model_unavailable"
)

// readBundleStepOutcomes reads the outcomes from a built bundle. A bundle
// built before they were recorded says nothing about its summary; one with a
// summary on disk is still known to have one.
func readBundleStepOutcomes(meetingPath string) (stepOutcomes, error) {
	raw, err := os.ReadFile(filepath.Join(meetingPath, "manifest.json"))
	if err != nil {
		return stepOutcomes{}, fmt.Errorf("read artifact manifest: %w", err)
	}
	var manifest struct {
		Files struct {
			Summary string `json:"summary"`
		} `json:"files"`
		Processing *struct {
			Transcription *struct {
				Status string `json:"status"`
				Reason string `json:"reason"`
			} `json:"transcription"`
			MeetingSummary *struct {
				Status string `json:"status"`
				Reason string `json:"reason"`
			} `json:"meetingSummary"`
		} `json:"processing"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return stepOutcomes{}, fmt.Errorf("parse artifact manifest: %w", err)
	}
	var out stepOutcomes
	if p := manifest.Processing; p != nil {
		if t := p.Transcription; t != nil {
			out.TranscriptionStatus, out.TranscriptionReason = strings.TrimSpace(t.Status), strings.TrimSpace(t.Reason)
		}
		if s := p.MeetingSummary; s != nil {
			out.SummaryStatus, out.SummaryReason = strings.TrimSpace(s.Status), strings.TrimSpace(s.Reason)
		}
	}
	if out.SummaryStatus == "" && strings.TrimSpace(manifest.Files.Summary) != "" {
		out.SummaryStatus = "completed"
	}
	return out, nil
}

// RecordAttemptStepOutcomes copies a built bundle's outcomes onto its attempt.
// The detail is left alone: it is the operator's, written when it happened.
func (s *Store) RecordAttemptStepOutcomes(ctx context.Context, jobID string, attemptNumber int, o stepOutcomes) error {
	if _, err := s.db.ExecContext(ctx, `
UPDATE job_attempts
SET transcription_status = ?, transcription_reason = ?, summary_status = ?, summary_reason = ?
WHERE job_id = ? AND attempt_number = ?`,
		o.TranscriptionStatus, o.TranscriptionReason, o.SummaryStatus, o.SummaryReason, jobID, attemptNumber); err != nil {
		return fmt.Errorf("record step outcomes: %w", err)
	}
	return nil
}

// SetAttemptTranscriptionDetail keeps why the operator dropped transcription
// from an attempt, in its own words.
func (s *Store) SetAttemptTranscriptionDetail(ctx context.Context, jobID string, attemptNumber int, detail string) error {
	if _, err := s.db.ExecContext(ctx, `
UPDATE job_attempts SET transcription_detail = ? WHERE job_id = ? AND attempt_number = ?`,
		boundTranscriptionDetail(detail), jobID, attemptNumber); err != nil {
		return fmt.Errorf("record transcription detail: %w", err)
	}
	return nil
}

// maxTranscriptionDetail bounds the detail kept on an attempt: enough for the
// line that names the cause, never a whole log.
const maxTranscriptionDetail = 500

func boundTranscriptionDetail(detail string) string {
	detail = strings.TrimSpace(detail)
	if len(detail) <= maxTranscriptionDetail {
		return detail
	}
	return "…" + detail[len(detail)-maxTranscriptionDetail:]
}

// tailBuffer keeps the last bytes written to it: a crashed build's final
// output, without holding the whole log in memory.
type tailBuffer struct {
	mu   sync.Mutex
	max  int
	data []byte
}

func newTailBuffer(max int) *tailBuffer { return &tailBuffer{max: max} }

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if over := len(b.data) - b.max; over > 0 {
		b.data = append(b.data[:0], b.data[over:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

// crashDetail picks, from a crashed build's last output, the line most likely
// to name the cause: one that mentions running out of memory, else the last
// non-empty line, else the process error.
func crashDetail(tail string, runErr error) string {
	lines := strings.Split(strings.ReplaceAll(tail, "\r", "\n"), "\n")
	last := ""
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if last == "" {
			last = line
		}
		lower := strings.ToLower(line)
		if strings.Contains(lower, "out of memory") || strings.Contains(lower, "cudnn") || strings.Contains(lower, "cuda error") {
			return line
		}
	}
	if last != "" {
		return last
	}
	if runErr != nil {
		return runErr.Error()
	}
	return "the build stopped without saying why"
}
