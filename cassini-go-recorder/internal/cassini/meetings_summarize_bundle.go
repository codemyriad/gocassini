package cassini

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gocassini/internal/portable"
	"gocassini/internal/transcribe"
)

// `cassini meetings summarize` on a meeting BUNDLE writes the summary where
// the operator keeps the canonical copy of a published meeting.
//
// A summary added only to the sealed .opus does not last: speaker edits and
// republishes seal again from the bundle, and `speakers apply` gives a meeting
// without a summary none (planSpeakerSummary). So the operator's summarize
// attempt (D-814) writes into a copy of the published bundle, and the existing
// seal and publish carry the result into the delivered file.
//
// The bundle ends up as a build with summaries on would have left it:
// summary.md, files.summary, provenance.meetingSummary (source "backfill",
// as for a backfilled .opus) and processing.meetingSummary "completed". The
// manifest is edited member by member, so keys this build has never heard of
// survive.

// summarizeReport is one input's outcome, as --json prints it.
type summarizeReport struct {
	Path   string `json:"path"`
	Status string `json:"status"` // summarized, skipped, failed, estimated
	// Reason says why a file was skipped: has_summary or no_transcript.
	Reason string `json:"reason,omitempty"`
	Model  string `json:"model,omitempty"`
	// Words and EstimatedInputTokens size the model call, made or not.
	Words                int    `json:"words,omitempty"`
	EstimatedInputTokens int    `json:"estimatedInputTokens,omitempty"`
	Error                string `json:"error,omitempty"`
}

const (
	summarizeSummarized = "summarized"
	summarizeSkipped    = "skipped"
	summarizeFailed     = "failed"
	summarizeEstimated  = "estimated"

	summarizeReasonHasSummary   = "has_summary"
	summarizeReasonNoTranscript = "no_transcript"
)

// isMeetingBundleInput reports whether path is a ready meeting bundle.
func isMeetingBundleInput(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false, nil
	}
	bundle, ok, err := LoadMeetingBundle(path)
	if err != nil || !ok {
		return false, err
	}
	if !bundleIsReady(bundle.Manifest.State) {
		return false, fmt.Errorf("%s is not a ready meeting bundle (%s)", path, bundleStateSummary(bundle.Manifest.State, bundle.Manifest.Stage))
	}
	return true, nil
}

// summarizeMeetingBundle summarizes one bundle, or with estimateOnly only
// sizes the call. A meeting with no words, or one that already has a summary
// (unless force), is skipped, and nothing is sent. The model call happens
// before anything is written, so a failed call leaves the bundle as it was.
func summarizeMeetingBundle(root string, cfg transcribe.LLMConfig, force, estimateOnly bool) (summarizeReport, error) {
	report := summarizeReport{Path: root, Model: strings.TrimSpace(cfg.Model)}
	manifestPath := filepath.Join(root, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return report, fmt.Errorf("read artifact manifest: %w", err)
	}
	manifest, err := parseOrderedJSONObject(raw)
	if err != nil {
		return report, fmt.Errorf("parse artifact manifest: %w", err)
	}
	processing, err := manifest.object("processing")
	if err != nil {
		return report, fmt.Errorf("parse processing: %w", err)
	}
	if !bundleTranscriptionCompleted(processing) {
		report.Status, report.Reason = summarizeSkipped, summarizeReasonNoTranscript
		return report, nil
	}
	files, err := manifest.object("files")
	if err != nil {
		return report, fmt.Errorf("parse files: %w", err)
	}
	if !force && orderedString(files, "summary", "") != "" && fileExists(filepath.Join(root, speakersSummaryFile)) {
		report.Status, report.Reason = summarizeSkipped, summarizeReasonHasSummary
		return report, nil
	}

	// The default transcript, which carries any speaker edits already
	// applied, so the summary names people as the meeting now does.
	transcript, err := transcribe.ReadTranscriptSpeakers(filepath.Join(root, speakersDefaultTranscript))
	if err != nil {
		return report, fmt.Errorf("read transcript: %w", err)
	}
	report.Words, report.EstimatedInputTokens = transcript.SummaryEstimate()
	if report.Words == 0 {
		report.Status, report.Reason = summarizeSkipped, summarizeReasonNoTranscript
		return report, nil
	}
	if estimateOnly {
		report.Status = summarizeEstimated
		return report, nil
	}

	body, err := transcript.Summarize(cfg)
	if err != nil {
		report.Status = summarizeFailed
		return report, fmt.Errorf("generate summary: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(root, speakersSummaryFile), []byte(body), 0o644); err != nil {
		report.Status = summarizeFailed
		return report, fmt.Errorf("write summary: %w", err)
	}
	if err := recordBundleSummary(manifestPath, manifest, files, processing, report.Model); err != nil {
		report.Status = summarizeFailed
		return report, err
	}
	report.Status = summarizeSummarized
	return report, nil
}

// bundleTranscriptionCompleted is false only when the bundle says its
// transcription did not complete. A bundle built before processing was
// recorded says nothing, and its words decide.
func bundleTranscriptionCompleted(processing *orderedJSONObject) bool {
	if processing == nil {
		return true
	}
	transcription, err := processing.object("transcription")
	if err != nil || transcription == nil {
		return true
	}
	return orderedString(transcription, "status", "completed") == "completed"
}

// recordBundleSummary names summary.md in the manifest, written last so the
// manifest never lists a summary that is not on disk.
func recordBundleSummary(manifestPath string, manifest, files, processing *orderedJSONObject, model string) error {
	if files == nil {
		files = &orderedJSONObject{}
	}
	files.set("summary", mustJSON(speakersSummaryFile))
	if err := manifest.setObject("files", files); err != nil {
		return err
	}

	provenance, err := manifest.object("provenance")
	if err != nil {
		return fmt.Errorf("parse provenance: %w", err)
	}
	if provenance == nil {
		provenance = &orderedJSONObject{}
	}
	step := map[string]string{"backend": "openai-compatible", "source": "backfill"}
	if model != "" {
		step["model"] = model
	}
	provenance.set("meetingSummary", mustJSON(step))
	if err := manifest.setObject("provenance", provenance); err != nil {
		return err
	}

	// As for a backfilled .opus: only where processing is already recorded.
	if processing != nil {
		outcome := portable.SummaryStatus{Status: portable.SummaryCompleted, Model: model}
		processing.set("meetingSummary", mustJSON(outcome))
		if err := manifest.setObject("processing", processing); err != nil {
			return err
		}
	}
	if err := writeOrderedJSON(manifestPath, manifest); err != nil {
		return fmt.Errorf("write artifact manifest: %w", err)
	}
	return nil
}
