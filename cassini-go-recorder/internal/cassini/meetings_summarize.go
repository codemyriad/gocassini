package cassini

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	inspectpkg "gocassini/internal/inspect"
	"gocassini/internal/portable"
	"gocassini/internal/transcribe"
)

// `cassini meetings summarize` backfills a meeting summary into an
// already-sealed portable .opus.
//
// It exists because summaries arrived after the first recordings were
// published (D-621): a file sealed before the feature carries its transcript
// but no summary.md attachment, and the only other way to get one is to re-run
// the whole build — hours of GPU transcription recomputing words the file
// already carries. The cheap path is the one this command takes: read the
// transcript back out of the file, make the one LLM call `cassini build` would
// have made, and rewrite the OpusTags through the same stage+verify+rename
// plumbing retag uses. The audio is never re-encoded, and the manifest's own
// integrity numbers must still verify before the original is replaced.
//
// Unlike its siblings under `cassini meetings`, this command reads LOCAL
// files, not the Nextcloud catalog: it is the middle of a
// fetch → summarize → re-upload loop, and the uploading stays with the
// operator's own tooling.

// summaryAttachmentName matches the attachment the pack step writes
// (internal/cassini/portable_meeting.go) and the reader in
// internal/inspect/extract_meeting.go looks for.
const summaryAttachmentName = "summary.md"

// errSummaryAlreadyPresent marks the skip outcome so the caller can report it
// as a skip line rather than a failure.
var errSummaryAlreadyPresent = errors.New("the file already carries a summary")

func runMeetingsSummarize(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var force, estimateOnly, asJSON bool
	var outPath string
	fs := flag.NewFlagSet("cassini meetings summarize", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&force, "force", false, "regenerate and replace a summary the file already carries")
	fs.StringVar(&outPath, "out", "", "write the result to this .opus instead of in place (single .opus input only)")
	fs.BoolVar(&estimateOnly, "estimate", false, "only size each summary (words, estimated input tokens); no model is called and nothing is written")
	fs.BoolVar(&asJSON, "json", false, "print the reports as one JSON array instead of status lines")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini meetings summarize ./Meeting.opus
  cassini meetings summarize --force ./Meeting.opus
  cassini meetings summarize --out ./Summarized.opus ./Meeting.opus
  cassini meetings summarize ./archive/*.opus
  cassini meetings summarize ./current/Meeting.meeting
  cassini meetings summarize --estimate --json ./current/*.meeting

Add a meeting summary to already-sealed portable .opus files, or to meeting
bundles, without re-running transcription. The transcript is read back out of
each one and one LLM call generates the summary.

A .opus file is rewritten in place through the same stage-verify-rename path
the packer uses: the audio bytes are copied untouched and the integrity numbers
must still verify before the original is replaced.

A meeting bundle gets summary.md and its manifest entries, as a build with
summaries on would have left it, so sealing the bundle again carries the
summary into the meeting file. A bundle whose transcription did not complete
is skipped.

--estimate calls no model and writes nothing: it reports the words each
summary would read and roughly how many input tokens that is, so the cost can
be shown before anything is sent.

The LLM endpoint comes from the environment exactly as for `+"`cassini build`"+`:
LLM_BASE_URL (or OPENROUTER_API_KEY for OpenRouter), with SUMMARY_BASE_URL /
SUMMARY_MODEL overriding the summary step alone. A file that already carries a
summary is skipped unless --force replaces it.

Flags must come before the files. One status line is printed per file (or one
JSON array with --json), every file is processed even when one fails, and the
exit code is non-zero when any failed.

`+"\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(stderr, "summarize takes at least one portable .opus meeting file or meeting bundle")
		fs.Usage()
		return 2
	}
	inputs := fs.Args()
	// Go's flag package stops at the first non-flag argument, so a flag placed
	// after the first file lands here as a "file". Refuse rather than read
	// `--force` as the name of a meeting to summarize.
	if meetingsArgsLookLikeFlagsAfterPositional(inputs) {
		fmt.Fprintf(stderr, "summarize configuration error: flags must come before the meeting files, got: %v\n", inputs)
		return 2
	}
	bundles := map[string]bool{}
	for _, input := range inputs {
		isBundle, err := isMeetingBundleInput(input)
		if err != nil {
			fmt.Fprintf(stderr, "summarize configuration error: %v\n", err)
			return 2
		}
		if isBundle {
			bundles[input] = true
			continue
		}
		if !isPortableMeetingOutput(input) {
			fmt.Fprintf(stderr, "summarize configuration error: %s is not a .opus file or a meeting bundle\n", input)
			return 2
		}
	}
	outPath = strings.TrimSpace(outPath)
	if outPath != "" {
		if len(inputs) != 1 {
			fmt.Fprintf(stderr, "summarize configuration error: --out only works with a single input, got %d files\n", len(inputs))
			return 2
		}
		if bundles[inputs[0]] {
			fmt.Fprintln(stderr, "summarize configuration error: --out only works with a .opus input; a meeting bundle is summarized in place")
			return 2
		}
		if !isPortableMeetingOutput(outPath) {
			fmt.Fprintf(stderr, "summarize configuration error: --out must be a .opus file, got %s\n", outPath)
			return 2
		}
	}

	// The same environment `cassini build` reads, resolved the same way —
	// including the SUMMARY_* per-step overrides and the kill-switch — so a
	// host that builds with summaries backfills with the identical
	// configuration. Checked before any file is touched: a missing endpoint is
	// an operator mistake, not a per-file failure. An estimate sends nothing,
	// so it needs no endpoint.
	cfg := transcribe.DefaultBuildConfig().SummaryLLM
	if !estimateOnly && !cfg.IsConfigured() {
		fmt.Fprintln(stderr, "summarize configuration error: no summary LLM endpoint is configured")
		fmt.Fprintln(stderr, "hint=set LLM_BASE_URL to an OpenAI-compatible endpoint (or OPENROUTER_API_KEY for OpenRouter), optionally SUMMARY_BASE_URL / SUMMARY_MODEL for the summary step alone — the same variables `cassini build` reads — and make sure CASSINI_SUMMARY_DISABLED is not set")
		return 2
	}

	failed := 0
	reports := make([]summarizeReport, 0, len(inputs))
	for _, input := range inputs {
		var report summarizeReport
		var err error
		switch {
		case bundles[input]:
			report, err = summarizeMeetingBundle(input, cfg, force, estimateOnly)
		case estimateOnly:
			report, err = estimatePortableMeeting(input, cfg.Model, force)
		default:
			report = summarizeReport{Path: input, Model: strings.TrimSpace(cfg.Model)}
			target := input
			if outPath != "" {
				target = outPath
			}
			switch err = summarizePortableMeeting(ctx, input, target, cfg, force); {
			case err == nil:
				report.Status = summarizeSummarized
			case errors.Is(err, errSummaryAlreadyPresent):
				report.Status, report.Reason, err = summarizeSkipped, summarizeReasonHasSummary, nil
			}
		}
		if err != nil {
			failed++
			report.Status, report.Error = summarizeFailed, err.Error()
		}
		reports = append(reports, report)
		if !asJSON {
			printSummarizeLine(stdout, report)
		}
	}
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(reports); err != nil {
			fmt.Fprintf(stderr, "summarize: write report: %v\n", err)
			return 1
		}
	}
	if failed > 0 {
		fmt.Fprintf(stderr, "summarize: %d of %d file(s) failed\n", failed, len(inputs))
		return 1
	}
	return 0
}

func printSummarizeLine(w io.Writer, report summarizeReport) {
	switch report.Status {
	case summarizeSummarized:
		fmt.Fprintf(w, "%s: summarized\n", report.Path)
	case summarizeSkipped:
		why := "has summary"
		if report.Reason == summarizeReasonNoTranscript {
			why = "no transcript"
		}
		fmt.Fprintf(w, "%s: skip (%s)\n", report.Path, why)
	case summarizeEstimated:
		fmt.Fprintf(w, "%s: would summarize %d words (~%d input tokens)\n", report.Path, report.Words, report.EstimatedInputTokens)
	default:
		fmt.Fprintf(w, "%s: failed: %s\n", report.Path, report.Error)
	}
}

// estimatePortableMeeting sizes the summary of one sealed file without
// calling the model.
func estimatePortableMeeting(path, model string, force bool) (summarizeReport, error) {
	report := summarizeReport{Path: path, Model: strings.TrimSpace(model)}
	meeting, err := inspectpkg.ExtractMeeting(path)
	if err != nil {
		return report, err
	}
	if len(meeting.SummaryMarkdown) > 0 && !force {
		report.Status, report.Reason = summarizeSkipped, summarizeReasonHasSummary
		return report, nil
	}
	streams, segments := summaryInputFromMeeting(meeting)
	for _, segment := range segments {
		report.Words += len(strings.Fields(segment.Text))
	}
	if report.Words == 0 {
		report.Status, report.Reason = summarizeSkipped, summarizeReasonNoTranscript
		return report, nil
	}
	report.EstimatedInputTokens = transcribe.EstimateSummaryInputTokens(streams, segments)
	report.Status = summarizeEstimated
	return report, nil
}

// summarizePortableMeeting adds a generated summary to one sealed file.
//
// Reads first, writes second, strictly: the LLM call happens before the write
// path is even prepared, so a failed or garbage LLM response leaves the file
// byte-identical. The write itself goes through commitPortableManifestRewrite
// (retag's plumbing), which stages next to the output, verifies the audio
// against the manifest's declared integrity policy, re-reads the staged
// manifest, and only then renames over the target.
func summarizePortableMeeting(ctx context.Context, inputPath, outPath string, cfg transcribe.LLMConfig, force bool) error {
	meeting, err := inspectpkg.ExtractMeeting(inputPath)
	if err != nil {
		return err
	}
	if len(meeting.SummaryMarkdown) > 0 && !force {
		return errSummaryAlreadyPresent
	}

	streams, segments := summaryInputFromMeeting(meeting)
	if len(segments) == 0 {
		return fmt.Errorf("meeting has no transcript text to summarize")
	}
	body, err := transcribe.BuildMeetingSummary(cfg, streams, segments)
	if err != nil {
		return fmt.Errorf("generate summary: %w", err)
	}

	resolvedOut, err := preparePortableMeetingOutput(outPath)
	if err != nil {
		return err
	}
	tags, err := portableMeetingTags(inputPath)
	if err != nil {
		return err
	}
	rawJSON, err := decodePortableMeetingPayload(tags)
	if err != nil {
		return err
	}
	document, err := decodePortableMeetingDocument(rawJSON)
	if err != nil {
		return err
	}
	applySummaryToPortableDocument(document, body, cfg.Model)

	return commitPortableManifestRewrite(ctx, inputPath, resolvedOut, document, tags,
		func(_, written portable.Manifest, _ map[string]string) error {
			// The audio is verified by the shared plumbing; what this command
			// changes is the metadata, so prove the summary actually landed in
			// the staged file before it replaces the only copy.
			if got := summaryAttachmentContent(written.Attachments); string(got) != body {
				return fmt.Errorf("verify summarized file: the written summary.md attachment does not carry the generated summary")
			}
			if len(written.Summary) == 0 {
				return fmt.Errorf("verify summarized file: the written manifest carries no summary metadata")
			}
			return nil
		})
}

// summaryInputFromMeeting rebuilds the two inputs BuildMeetingSummary takes
// from what a sealed file carries. The build pipeline hands it per-speaker
// segments and the stream roster; a portable file carries only word-level
// items and the speaker roster, so the words are regrouped with the same
// gap/length rule the `meetings context` prose uses (deriveProseSegments) and
// the roster becomes the minimal AudioStream list labelForSpeaker needs.
func summaryInputFromMeeting(meeting inspectpkg.ExtractedMeeting) ([]transcribe.AudioStream, []transcribe.Segment) {
	streams := make([]transcribe.AudioStream, 0, len(meeting.Manifest.Speakers))
	for _, speaker := range meeting.Manifest.Speakers {
		label := strings.TrimSpace(speaker.Label)
		if label == "" {
			// An unlabelled speaker still gets attributed by id rather than as
			// an empty name in the prompt.
			label = strings.TrimSpace(speaker.ID)
		}
		streams = append(streams, transcribe.AudioStream{
			SpeakerID:    strings.TrimSpace(speaker.ID),
			SpeakerLabel: label,
		})
	}

	prose := deriveProseSegments(meeting.Transcript.Words, meeting.SpeakerLabels())
	segments := make([]transcribe.Segment, 0, len(prose))
	for _, segment := range prose {
		segments = append(segments, transcribe.Segment{
			SpeakerID: segment.Speaker,
			StartMS:   segment.StartMS,
			EndMS:     segment.EndMS,
			Text:      segment.Text,
		})
	}
	return streams, segments
}

// applySummaryToPortableDocument seals a generated summary into the decoded
// manifest document exactly the way the pack step would have — the same
// metadata buildPortableSummaryMetadata writes and the same summary.md
// attachment shape (internal/cassini/portable_meeting.go) — so a backfilled
// file is indistinguishable in shape from one built with summaries on.
//
// The one deliberate difference is provenance.meetingSummary.source =
// "backfill". `source` is the manifest schema's existing ProcessingStep field
// for where a step's output came from (rendered by `cassini inspect`), and it
// records that this summary was generated after the file was sealed rather
// than by the build that produced it. No new schema field is invented.
//
// The document is edited as generic JSON, not through portable.Manifest, for
// the same reason retag edits it that way: re-marshalling the struct would
// silently drop every key this build has never heard of.
func applySummaryToPortableDocument(document map[string]any, body string, model string) {
	trimmedModel := strings.TrimSpace(model)

	meta := map[string]any{
		"format":          "markdown",
		"templateVersion": "v0",
	}
	if trimmedModel != "" {
		meta["model"] = trimmedModel
	}
	document["summary"] = meta

	step := map[string]any{
		"backend": "openai-compatible",
		"source":  "backfill",
	}
	if trimmedModel != "" {
		step["model"] = trimmedModel
	}
	provenance, _ := document["provenance"].(map[string]any)
	if provenance == nil {
		provenance = map[string]any{}
		document["provenance"] = provenance
	}
	provenance["meetingSummary"] = step

	// The summary step's outcome now says completed. Only where the file
	// already records processing: a file sealed before that existed asserts
	// nothing about its transcription, and inventing a processing block here
	// would put a summary status beside a transcription status nobody wrote.
	if processing, ok := document["processing"].(map[string]any); ok {
		outcome := map[string]any{"status": portable.SummaryCompleted}
		if trimmedModel != "" {
			outcome["model"] = trimmedModel
		}
		processing["meetingSummary"] = outcome
	}

	attachment := map[string]any{
		"name":          summaryAttachmentName,
		"mime":          "text/markdown",
		"contentBase64": base64.StdEncoding.EncodeToString([]byte(body)),
	}
	existing, _ := document["attachments"].([]any)
	// Filtered rather than replaced in place: --force must not leave a second
	// summary.md behind if a producer somehow wrote duplicates, because the
	// reader takes the first one it finds.
	kept := make([]any, 0, len(existing)+1)
	for _, entry := range existing {
		if attachmentMap, ok := entry.(map[string]any); ok {
			if name, _ := attachmentMap["name"].(string); strings.EqualFold(strings.TrimSpace(name), summaryAttachmentName) {
				continue
			}
		}
		kept = append(kept, entry)
	}
	document["attachments"] = append(kept, attachment)
}

// summaryAttachmentContent returns the decoded summary.md attachment from a
// re-read manifest, mirroring the reader in internal/inspect — StdEncoding,
// matching the writer, NOT the RawURLEncoding the payload chunks use.
func summaryAttachmentContent(attachments []map[string]any) []byte {
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
