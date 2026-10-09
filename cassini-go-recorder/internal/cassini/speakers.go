package cassini

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gocassini/internal/portable"
	"gocassini/internal/transcribe"
)

// `cassini speakers` separates the voices of people who shared one device.
// diarize measures who spoke when on one participant's own track and stores
// the turns; apply rewrites a built .meeting bundle from its original
// transcript, an edits document and those stored turns. apply never runs a
// model and never touches the audio, so a meeting republished from it keeps
// its audio identity and every mark stays resolved.
//
// The operator runs this binary rather than importing it, so the exit codes,
// the stderr prefixes and the --json report are a contract with
// cassini-operator. Change both sides together.

const (
	speakersExitRuntime      = 1
	speakersExitUsage        = 2
	speakersExitUnavailable  = 3 // no diarization model, or a runtime without Nemotron
	speakersExitNotFound     = 4 // the recording has no stream for that participant
	speakersExitTurnsMissing = 5 // a split has no stored turns file
)

const (
	speakersRawASRTranscript   = "transcript.raw-asr.words.v1.json"
	speakersEditsFile          = "speaker-edits.json"
	speakersDefaultTranscript  = "transcript.words.v1.json"
	speakersSeparatedID        = "separated-voices"
	speakersRawASRID           = portable.DefaultWordsTranscriptID
	speakersDiarizationKey     = "x-speakerDiarization"
	speakersCaptionsFile       = "captions.vtt"
	speakersSummaryFile        = "summary.md"
	speakersBaseSummary        = "summary.raw-asr.md"
	speakersDisplayTranscript  = "transcript.display.v1.json"
	speakersReadableTranscript = "transcript.readable.v1.json"
)

// speakerFileIDRE is the shape of a participant id (speakerIDFromLabel), and
// so of a turns file name. Anything else is refused before it reaches a path.
var speakerFileIDRE = regexp.MustCompile(`^[a-z0-9_]{1,128}$`)

// Seams so the command can be tested without a model or a recording.
var (
	diarizeSpeakerFn          = transcribe.DiarizeSpeaker
	resolveDiarizationModelFn = transcribe.ResolveDiarizationModel
	loadDiarizationModelFn    = transcribe.LoadDiarizationModel
	lockModelRuntimeFn        = transcribe.LockModelRuntime
	diarizationThreadsFn      = transcribe.DiarizationThreads
	// speakersBeforeAudioCheck runs between apply's writes and its audio
	// check; tests use it to change the audio underneath.
	speakersBeforeAudioCheck = func(string) {}
)

type speakersFailure struct {
	code int
	err  error
}

func (f *speakersFailure) Error() string { return f.err.Error() }
func (f *speakersFailure) Unwrap() error { return f.err }

func speakersFail(code int, format string, args ...any) error {
	return &speakersFailure{code: code, err: fmt.Errorf(format, args...)}
}

func speakersExitCodeFor(err error) int {
	var failure *speakersFailure
	if errors.As(err, &failure) {
		return failure.code
	}
	return speakersExitRuntime
}

func runSpeakers(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printSpeakersUsage(stderr)
		return speakersExitUsage
	}
	switch args[0] {
	case "help", "-h", "--help":
		printSpeakersUsage(stdout)
		return 0
	case "diarize":
		return runSpeakersDiarize(ctx, args[1:], stdout, stderr)
	case "apply":
		return runSpeakersApply(args[1:], stdout, stderr)
	case "show":
		return runSpeakersShow(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown speakers subcommand %q\n\n", args[0])
		printSpeakersUsage(stderr)
		return speakersExitUsage
	}
}

func printSpeakersUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  cassini speakers diarize <x.run|x.mkv> --speaker <speakerId> --out <turns.json> [--model <path>]
  cassini speakers apply   <dir.meeting> --edits <edits.json> --turns-dir <dir> [--recording <x.run|x.mkv>] [--json]
  cassini speakers show    <x.meeting|x.opus> [--json]

Separate the voices of people who shared one device.

diarize finds who spoke when on one participant's own audio and writes the
turns (times and voice numbers only, no voice data). The model is --model,
else $CASSINI_DIARIZATION_MODEL, else the speaker separation model installed
in the model store (Settings, or cassini models install
nemotron-3-diarization-int8). It runs on the CPU with
$CASSINI_DIARIZATION_THREADS threads (default 2, at most 16).

apply rewrites a .meeting bundle in place from its original transcript, the
edits document (cassini.speaker-edits.v1) and <turns-dir>/<speakerId>.json for
each split. It runs no model and never touches the audio. An edits document
that changes nothing restores the bundle as it was built. With --recording,
turns measured on any other recording are refused.

show prints the speakers a meeting has, the default transcript and the edits
revision applied.

Exit codes: 0 ok, 1 runtime, 2 usage, 3 diarization unavailable, 4 speaker not
found, 5 a split has no turns file.
`)
}

// ---------------------------------------------------------------- diarize

func runSpeakersDiarize(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cassini speakers diarize", flag.ContinueOnError)
	fs.SetOutput(stderr)
	speaker := fs.String("speaker", "", "participant id whose audio to diarize")
	out := fs.String("out", "", "where to write the speaker turns (JSON)")
	modelPath := fs.String("model", "", "Nemotron diarization ONNX model (default: $CASSINI_DIARIZATION_MODEL or the installed model)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini speakers diarize ./runs/meeting.run --speaker spk_... --out ./turns/spk_....json [--model ./model.int8.onnx]

`)
		fs.PrintDefaults()
	}
	files, code := parseAnnotateArgs(fs, args, 1)
	if code >= 0 {
		return code
	}
	if strings.TrimSpace(*speaker) == "" || strings.TrimSpace(*out) == "" {
		fmt.Fprintln(stderr, "cassini speakers diarize: --speaker and --out are required")
		return speakersExitUsage
	}
	input, err := resolveBuildInput(files[0])
	if err != nil {
		fmt.Fprintf(stderr, "cassini speakers diarize: %v\n", err)
		return speakersExitRuntime
	}
	var model transcribe.DiarizationModel
	if *modelPath != "" {
		model, err = loadDiarizationModelFn(*modelPath)
	} else {
		model, err = resolveDiarizationModelFn(defaultCassiniCacheRoot())
	}
	if err != nil {
		if errors.Is(err, transcribe.ErrDiarizationUnavailable) {
			fmt.Fprintf(stderr, "diarization-unavailable: %v\n", err)
			return speakersExitUnavailable
		}
		fmt.Fprintf(stderr, "cassini speakers diarize: %v\n", err)
		return speakersExitRuntime
	}
	// Diarizing is inference: it waits for a build or a model check using the
	// same store to finish, and they wait for it, as they do for each other.
	unlock, err := lockModelRuntimeFn(ctx, defaultCassiniCacheRoot())
	if err != nil {
		fmt.Fprintf(stderr, "cassini speakers diarize: model runtime lock: %v\n", err)
		return speakersExitRuntime
	}
	defer unlock()
	// The operator sets the threads from its own budget; only the speed
	// depends on them, and the turn set records how many ran.
	threads, err := diarizationThreadsFn()
	if err != nil {
		fmt.Fprintf(stderr, "cassini speakers diarize: %v\n", err)
	}
	set, err := diarizeSpeakerFn(ctx, input.RecordingPath, *speaker, model, threads)
	switch {
	case errors.Is(err, transcribe.ErrSpeakerNotFound):
		fmt.Fprintf(stderr, "speaker-not-found: %s\n", *speaker)
		return speakersExitNotFound
	case errors.Is(err, transcribe.ErrDiarizationUnavailable):
		fmt.Fprintf(stderr, "diarization-unavailable: %v\n", err)
		return speakersExitUnavailable
	case err != nil:
		fmt.Fprintf(stderr, "cassini speakers diarize: %v\n", err)
		return speakersExitRuntime
	}
	if err := transcribe.WriteSpeakerTurnSet(*out, set); err != nil {
		fmt.Fprintf(stderr, "cassini speakers diarize: write turns: %v\n", err)
		return speakersExitRuntime
	}
	voices := map[int]bool{}
	for _, t := range set.Turns {
		voices[t.Speaker] = true
	}
	fmt.Fprintf(stdout, "speaker-turns -> %s (%d turns, %d voices, %d ms)\n", *out, len(set.Turns), len(voices), set.ElapsedMS)
	return 0
}

// ---------------------------------------------------------------- apply

// speakersApplyReport is what apply prints with --json. Lists are never null.
type speakersApplyReport struct {
	Revision     int                   `json:"revision"`
	Splits       []speakersSplitReport `json:"splits"`
	Missing      []string              `json:"missing"`
	Inconclusive []string              `json:"inconclusive"`
	Merged       int                   `json:"merged"`
	SpeakerCount int                   `json:"speakerCount"`
	// Summary says what happened to summary.md: "none" (the meeting has no
	// summary), "unchanged" (nothing to do, or already written for these
	// exact edits), "regenerated" (rewritten for the new speakers),
	// "restored" (the build's own summary is back), or "stale" (no summary
	// model, or it failed: the summary still credits the speakers it was
	// written for; the reason is on stderr, and applying the same edits again
	// tries again).
	Summary string `json:"summary"`
	// SummarySHA256 says which summary the meeting now has: the SHA-256 of
	// the summary.md apply wrote, as x-speakerDiarization.summary records it
	// on the default transcript, or "" for the build's own (or none). A
	// reader compares it with the recording it shows: the last apply's
	// Summary alone does not say whether an earlier one rewrote it.
	SummarySHA256 string `json:"summarySha256"`
}

type speakersSplitReport struct {
	SpeakerID    string   `json:"speakerId"`
	Voices       []string `json:"voices"`
	Inconclusive bool     `json:"inconclusive"`
}

// speakerDiarizationRecord is manifest.json's x-speakerDiarization, and (less
// Base) the separated-voices transcript's provenance. Counts and ids only:
// nothing in it is voice data.
type speakerDiarizationRecord struct {
	Backend          string                    `json:"backend,omitempty"`
	Model            string                    `json:"model,omitempty"`
	ModelSHA256      string                    `json:"modelSha256,omitempty"`
	MinDurationOn    float64                   `json:"minDurationOn,omitempty"`
	MinDurationOff   float64                   `json:"minDurationOff,omitempty"`
	Assignment       string                    `json:"assignment,omitempty"`
	SourceSeparation bool                      `json:"sourceSeparation"`
	EditsRevision    int                       `json:"editsRevision"`
	EditsSHA256      string                    `json:"editsSha256"`
	Splits           []speakerDiarizationSplit `json:"splits"`
	// Summary is present once apply has rewritten summary.md, and says for
	// which edits and with which model; provenance.meetingSummary still
	// describes the build's summary, now kept as summary.raw-asr.md.
	Summary *speakerSummaryRecord `json:"summary,omitempty"`
	// Base holds the manifest members the build wrote and apply rewrites, so
	// an edits document that changes nothing restores them exactly. Only on
	// the bundle's own manifest; never packed.
	Base *speakerDiarizationBase `json:"base,omitempty"`
}

type speakerDiarizationSplit struct {
	SpeakerID    string   `json:"speakerId"`
	Voices       []string `json:"voices"`
	TurnCount    int      `json:"turnCount"`
	SpeakerCount int      `json:"speakerCount"`
	Inconclusive bool     `json:"inconclusive"`
}

// speakerSummaryRecord is the summary member of x-speakerDiarization: the
// summary.md apply wrote (by SHA-256), the model that wrote it, the edits it
// was written for and the edited transcript it was written from (both by
// SHA-256). Apply skips the summary model while summary.md and the edited
// transcript are still those, so a retried or replayed apply, or a new
// revision that changes nobody's words or name, is not billed again.
type speakerSummaryRecord struct {
	Rewritten        bool   `json:"rewritten"`
	Model            string `json:"model,omitempty"`
	SHA256           string `json:"sha256"`
	EditsSHA256      string `json:"editsSha256"`
	TranscriptSHA256 string `json:"transcriptSha256,omitempty"`
}

type speakerDiarizationBase struct {
	SpeakerCount json.RawMessage `json:"speakerCount,omitempty"`
	SegmentCount json.RawMessage `json:"segmentCount,omitempty"`
	Transcripts  json.RawMessage `json:"transcripts,omitempty"`
}

func runSpeakersApply(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cassini speakers apply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	editsPath := fs.String("edits", "", "speaker edits document (cassini.speaker-edits.v1)")
	turnsDir := fs.String("turns-dir", "", "directory holding <speakerId>.json turns for each split")
	emitJSON := fs.Bool("json", false, "print the report as JSON")
	recording := fs.String("recording", "", "the capture (x.run or x.mkv) the turns were measured on; turns measured on any other recording are refused")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini speakers apply ./meetings/meeting.meeting --edits ./edits.json --turns-dir ./turns [--recording ./runs/meeting.run] [--json]

`)
		fs.PrintDefaults()
	}
	files, code := parseAnnotateArgs(fs, args, 1)
	if code >= 0 {
		return code
	}
	if *editsPath == "" || *turnsDir == "" {
		fmt.Fprintln(stderr, "cassini speakers apply: --edits and --turns-dir are required")
		return speakersExitUsage
	}
	editsRaw, err := os.ReadFile(*editsPath)
	if err != nil {
		fmt.Fprintf(stderr, "cassini speakers apply: %v\n", err)
		return speakersExitRuntime
	}
	recordingSHA256 := ""
	if *recording != "" {
		input, err := resolveBuildInput(*recording)
		if err != nil {
			fmt.Fprintf(stderr, "cassini speakers apply: %v\n", err)
			return speakersExitRuntime
		}
		if recordingSHA256, err = annotateFileSHA256(input.RecordingPath); err != nil {
			fmt.Fprintf(stderr, "cassini speakers apply: hash recording: %v\n", err)
			return speakersExitRuntime
		}
	}
	report, err := applySpeakerEditsToBundle(files[0], editsRaw, *turnsDir, recordingSHA256, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return speakersExitCodeFor(err)
	}
	if *emitJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			fmt.Fprintf(stderr, "cassini speakers apply: write report: %v\n", err)
			return speakersExitRuntime
		}
		return 0
	}
	fmt.Fprintf(stdout, "revision %d: %d speakers", report.Revision, report.SpeakerCount)
	for _, split := range report.Splits {
		if split.Inconclusive {
			fmt.Fprintf(stdout, "; %s: only one voice found", split.SpeakerID)
			continue
		}
		fmt.Fprintf(stdout, "; %s: %d voices", split.SpeakerID, len(split.Voices))
	}
	if report.Merged > 0 {
		fmt.Fprintf(stdout, "; %d segments merged", report.Merged)
	}
	fmt.Fprintln(stdout)
	return 0
}

// applySpeakerEditsToBundle applies an edits document to a .meeting bundle in
// place. The original transcript is kept byte for byte as the raw-asr
// transcript and is the base of every apply, so applying is idempotent and an
// edits document that changes nothing leaves no trace. Warnings (a summary
// that could not be rewritten) go to stderr. With recordingSHA256, every turn
// set must have been measured on that recording.
func applySpeakerEditsToBundle(meetingDir string, editsRaw []byte, turnsDir, recordingSHA256 string, stderr io.Writer) (speakersApplyReport, error) {
	doc, err := transcribe.ParseSpeakerEdits(editsRaw)
	if err != nil {
		return speakersApplyReport{}, speakersFail(speakersExitRuntime, "invalid: %v", err)
	}
	turnSets := map[string]transcribe.SpeakerTurnSet{}
	for _, split := range doc.Splits {
		if !speakerFileIDRE.MatchString(split.SpeakerID) {
			return speakersApplyReport{}, speakersFail(speakersExitRuntime, "invalid: cannot split %q", split.SpeakerID)
		}
		path := filepath.Join(turnsDir, split.SpeakerID+".json")
		set, err := transcribe.ReadSpeakerTurnSet(path)
		if errors.Is(err, os.ErrNotExist) {
			return speakersApplyReport{}, speakersFail(speakersExitTurnsMissing, "turns-missing: %s", split.SpeakerID)
		}
		if err != nil {
			return speakersApplyReport{}, speakersFail(speakersExitRuntime, "read turns: %v", err)
		}
		if err := set.Check(); err != nil {
			return speakersApplyReport{}, speakersFail(speakersExitRuntime, "read turns: %v", err)
		}
		// Turns name times in one recording; on another they would credit
		// words to voices that never said them.
		if recordingSHA256 != "" && set.Source.SHA256 != recordingSHA256 {
			return speakersApplyReport{}, speakersFail(speakersExitRuntime, "turns-source-mismatch: %s was measured on recording %s, not on %s", split.SpeakerID, set.Source.SHA256, recordingSHA256)
		}
		turnSets[split.SpeakerID] = set
	}

	root, err := filepath.Abs(meetingDir)
	if err != nil {
		return speakersApplyReport{}, err
	}
	manifestPath := filepath.Join(root, "manifest.json")
	manifestRaw, err := os.ReadFile(manifestPath)
	if err != nil {
		return speakersApplyReport{}, fmt.Errorf("read meeting artifact manifest: %w", err)
	}
	manifest, err := parseOrderedJSONObject(manifestRaw)
	if err != nil {
		return speakersApplyReport{}, fmt.Errorf("parse meeting artifact manifest: %w", err)
	}
	files, err := manifest.object("files")
	if err != nil {
		return speakersApplyReport{}, fmt.Errorf("meeting artifact manifest files: %w", err)
	}
	if files == nil {
		files = &orderedJSONObject{}
	}
	audioPath := filepath.Join(root, orderedString(files, "audio", "meeting.webm"))
	primaryName := orderedString(files, "transcript", speakersDefaultTranscript)
	primaryPath := filepath.Join(root, primaryName)
	rawASRPath := filepath.Join(root, speakersRawASRTranscript)

	audioBefore, err := annotateFileSHA256(audioPath)
	if err != nil {
		return speakersApplyReport{}, fmt.Errorf("hash meeting audio: %w", err)
	}

	applied := fileExists(rawASRPath)
	basePath := primaryPath
	if applied {
		basePath = rawASRPath
	}
	baseRaw, err := os.ReadFile(basePath)
	if err != nil {
		return speakersApplyReport{}, fmt.Errorf("read base transcript: %w", err)
	}
	base, err := transcribe.ReadTranscriptSpeakers(basePath)
	if err != nil {
		return speakersApplyReport{}, err
	}
	segments, roster, result, err := transcribe.ApplySpeakerEdits(base.Segments(), base.Roster(), doc, turnSets)
	if err != nil {
		return speakersApplyReport{}, speakersFail(speakersExitRuntime, "invalid: %v", err)
	}
	if len(result.Missing) > 0 {
		return speakersApplyReport{}, speakersFail(speakersExitTurnsMissing, "turns-missing: %s", result.Missing[0])
	}
	edited, err := base.WithSpeakers(segments, roster)
	if err != nil {
		return speakersApplyReport{}, err
	}

	// The record of what the build wrote: the stash, while the manifest holds
	// one; otherwise the manifest still is the build's. That is so before the
	// first apply, and also after a crash part way through one (the raw-asr
	// copy is written before the manifest) or through an undo (the manifest is
	// restored before the raw-asr copy is removed).
	var stash speakerDiarizationBase
	var previous speakerDiarizationRecord
	if raw, ok := manifest.get(speakersDiarizationKey); ok {
		if err := json.Unmarshal(raw, &previous); err != nil {
			return speakersApplyReport{}, fmt.Errorf("parse %s: %w", speakersDiarizationKey, err)
		}
	}
	if previous.Base != nil {
		stash = *previous.Base
	} else {
		stash.SpeakerCount, _ = manifest.get("speakerCount")
		stash.SegmentCount, _ = manifest.get("segmentCount")
		stash.Transcripts, _ = files.get("transcripts")
	}

	report := speakersApplyReport{
		Revision:     doc.Revision,
		Splits:       []speakersSplitReport{},
		Missing:      []string{},
		Inconclusive: append([]string{}, result.Inconclusive...),
		Merged:       result.Merged,
	}
	appliedSplits := 0
	for _, split := range result.Splits {
		inconclusive := len(split.SubSpeakerIDs) < 2
		voices := []string{}
		if !inconclusive {
			voices = append(voices, split.SubSpeakerIDs...)
			appliedSplits++
		}
		report.Splits = append(report.Splits, speakersSplitReport{SpeakerID: split.ParentID, Voices: voices, Inconclusive: inconclusive})
	}
	// Effective means a split found voices, a merge moved words, or a speaker
	// who is in both rosters got a new name. Not "the roster changed": the
	// result roster lists only speakers with words, so a bundle built without
	// a transcript (every participant listed, no words) would otherwise lose
	// its participants and its untranscribed marker to any edits document. It
	// stays as built; a rerun that transcribes it replays the edits then.
	effective := appliedSplits > 0 || result.Merged > 0 || renamesSpeaker(base.Roster(), roster)

	baseCount, err := rawInt(stash.SpeakerCount)
	if err != nil {
		return speakersApplyReport{}, fmt.Errorf("manifest speakerCount: %w", err)
	}
	report.SpeakerCount = baseCount
	report.Summary = "unchanged"
	if !fileExists(filepath.Join(root, speakersSummaryFile)) && !fileExists(filepath.Join(root, speakersBaseSummary)) {
		report.Summary = "none"
	}

	if !effective {
		// The kept copy of the build's summary goes last in an undo, so while
		// it is there the summary still has to be put back, even when an
		// interrupted undo already removed the raw-asr copy.
		if fileExists(filepath.Join(root, speakersBaseSummary)) {
			report.Summary = "restored"
		}
		if applied {
			if err := restoreSpeakerBase(root, manifest, files, stash, primaryPath, baseRaw, base); err != nil {
				return speakersApplyReport{}, err
			}
		} else if err := restoreSpeakerSummary(root); err != nil {
			return speakersApplyReport{}, err
		}
	} else {
		report.SpeakerCount = baseCount + edited.LogicalSpeakerCount() - base.LogicalSpeakerCount()
		record := speakerDiarizationRecord{
			SourceSeparation: false,
			EditsRevision:    doc.Revision,
			EditsSHA256:      sha256Hex(editsRaw),
			Splits:           []speakerDiarizationSplit{},
		}
		for i, split := range result.Splits {
			set := turnSets[split.ParentID]
			if record.Model == "" {
				record.Backend = fmt.Sprintf("sherpa-onnx %s Nemotron diarization, %s, %d threads", set.Model.Runtime, strings.ToUpper(set.Params.Provider), set.Params.Threads)
				record.Model = set.Model.Name
				record.ModelSHA256 = set.Model.SHA256
				record.MinDurationOn = set.Params.MinDurationOn
				record.MinDurationOff = set.Params.MinDurationOff
				record.Assignment = set.Assignment
			}
			record.Splits = append(record.Splits, speakerDiarizationSplit{
				SpeakerID:    split.ParentID,
				Voices:       report.Splits[i].Voices,
				TurnCount:    split.TurnCount,
				SpeakerCount: split.SpeakerCount,
				Inconclusive: report.Splits[i].Inconclusive,
			})
		}
		// The summary model runs before the first write: it is the slow,
		// fallible step, and the bundle stays as it was while it runs.
		summary, err := planSpeakerSummary(root, previous.Summary, edited, record.EditsSHA256, stderr)
		if err != nil {
			return speakersApplyReport{}, err
		}
		report.Summary = summary.status
		record.Summary = summary.record
		if summary.record != nil {
			report.SummarySHA256 = summary.record.SHA256
		}
		if !applied {
			// Kept byte for byte: this is the base of every later apply, and
			// the only copy of it once the primary transcript is replaced, so
			// it is on disk before that happens.
			if err := writeFileDurable(rawASRPath, baseRaw, 0o644); err != nil {
				return speakersApplyReport{}, fmt.Errorf("keep original transcript: %w", err)
			}
		}
		if err := edited.Write(primaryPath); err != nil {
			return speakersApplyReport{}, fmt.Errorf("write transcript: %w", err)
		}
		if err := writeFileAtomic(filepath.Join(root, speakersEditsFile), editsRaw, 0o644); err != nil {
			return speakersApplyReport{}, fmt.Errorf("write speaker edits: %w", err)
		}
		if err := regenerateSpeakerCaptions(root, edited); err != nil {
			return speakersApplyReport{}, err
		}
		if err := writeSpeakerSummary(root, summary); err != nil {
			return speakersApplyReport{}, err
		}
		// Display and readable documents carry their own copies of speaker
		// labels; the viewer derives both from the words when they are absent.
		for key, name := range map[string]string{"displayTranscript": speakersDisplayTranscript, "readableTranscript": speakersReadableTranscript} {
			if err := removeIfExists(filepath.Join(root, orderedString(files, key, name))); err != nil {
				return speakersApplyReport{}, err
			}
			files.delete(key)
		}
		if err := writeSeparatedVoicesManifest(manifest, files, stash, record, primaryName, report.SpeakerCount, len(segments)); err != nil {
			return speakersApplyReport{}, err
		}
		if err := writeOrderedJSON(manifestPath, manifest); err != nil {
			return speakersApplyReport{}, fmt.Errorf("write meeting artifact manifest: %w", err)
		}
	}

	speakersBeforeAudioCheck(root)
	audioAfter, err := annotateFileSHA256(audioPath)
	if err != nil {
		return speakersApplyReport{}, fmt.Errorf("hash meeting audio: %w", err)
	}
	if audioAfter != audioBefore {
		return speakersApplyReport{}, fmt.Errorf("meeting audio changed during apply (sha256 %s -> %s); a speaker edit must never re-encode the audio", audioBefore, audioAfter)
	}
	return report, nil
}

// writeSeparatedVoicesManifest points the manifest at the separated transcript
// as the default and the original as raw-asr, and records the split.
func writeSeparatedVoicesManifest(manifest, files *orderedJSONObject, stash speakerDiarizationBase, record speakerDiarizationRecord, primaryName string, speakerCount, segmentCount int) error {
	var speechToText *orderedJSONObject
	if prov, err := manifest.object("provenance"); err == nil && prov != nil {
		if step, err := prov.object("speechToText"); err == nil && step != nil {
			speechToText = step
		}
	}
	recordRaw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	separatedProv := speechToText.clone()
	separatedProv.set(speakersDiarizationKey, recordRaw)
	transcripts := []json.RawMessage{}
	add := func(entry *orderedJSONObject) error {
		raw, err := entry.MarshalJSON()
		if err != nil {
			return err
		}
		transcripts = append(transcripts, raw)
		return nil
	}
	separated := &orderedJSONObject{}
	separated.set("id", mustJSON(speakersSeparatedID))
	separated.set("path", mustJSON(primaryName))
	separated.set("default", mustJSON(true))
	if err := separated.setObject("provenance", separatedProv); err != nil {
		return err
	}
	if err := add(separated); err != nil {
		return err
	}
	rawASR := &orderedJSONObject{}
	rawASR.set("id", mustJSON(speakersRawASRID))
	rawASR.set("path", mustJSON(speakersRawASRTranscript))
	if speechToText != nil {
		if err := rawASR.setObject("provenance", speechToText.clone()); err != nil {
			return err
		}
	}
	if err := add(rawASR); err != nil {
		return err
	}
	// Further transcripts (additional models) follow. The build's entry for
	// the primary file is the one raw-asr now stands for.
	if len(stash.Transcripts) > 0 {
		var existing []json.RawMessage
		if err := json.Unmarshal(stash.Transcripts, &existing); err != nil {
			return fmt.Errorf("manifest files.transcripts: %w", err)
		}
		for _, raw := range existing {
			var ref struct {
				ID   string `json:"id"`
				Path string `json:"path"`
			}
			if err := json.Unmarshal(raw, &ref); err != nil {
				return fmt.Errorf("manifest files.transcripts: %w", err)
			}
			if ref.Path == primaryName || ref.ID == speakersSeparatedID || ref.ID == speakersRawASRID {
				continue
			}
			transcripts = append(transcripts, raw)
		}
	}
	transcriptsRaw, err := json.Marshal(transcripts)
	if err != nil {
		return err
	}
	files.set("transcripts", transcriptsRaw)
	if err := manifest.setObject("files", files); err != nil {
		return err
	}
	manifest.set("speakerCount", mustJSON(speakerCount))
	if len(stash.SegmentCount) > 0 {
		manifest.set("segmentCount", mustJSON(segmentCount))
	}
	stashed := record
	stashed.Base = &stash
	stashedRaw, err := json.Marshal(stashed)
	if err != nil {
		return err
	}
	manifest.set(speakersDiarizationKey, stashedRaw)
	return nil
}

// restoreSpeakerBase undoes every edit: the original transcript, captions and
// manifest members come back as the build wrote them.
func restoreSpeakerBase(root string, manifest, files *orderedJSONObject, stash speakerDiarizationBase, primaryPath string, baseRaw []byte, base transcribe.TranscriptSpeakers) error {
	if err := writeFileAtomic(primaryPath, baseRaw, 0o644); err != nil {
		return fmt.Errorf("restore transcript: %w", err)
	}
	if err := regenerateSpeakerCaptions(root, base); err != nil {
		return err
	}
	if err := putBackSpeakerSummary(root); err != nil {
		return err
	}
	if len(stash.Transcripts) > 0 {
		files.set("transcripts", stash.Transcripts)
	} else {
		files.delete("transcripts")
	}
	if err := manifest.setObject("files", files); err != nil {
		return err
	}
	for key, raw := range map[string]json.RawMessage{"speakerCount": stash.SpeakerCount, "segmentCount": stash.SegmentCount} {
		if len(raw) > 0 {
			manifest.set(key, raw)
		} else {
			manifest.delete(key)
		}
	}
	manifest.delete(speakersDiarizationKey)
	if err := writeOrderedJSON(filepath.Join(root, "manifest.json"), manifest); err != nil {
		return fmt.Errorf("write meeting artifact manifest: %w", err)
	}
	// Last, so a crash before this point leaves the base where the next
	// apply looks for it; the manifest is the build's again, which is where
	// that apply then reads the build's values.
	if err := removeIfExists(filepath.Join(root, speakersEditsFile)); err != nil {
		return err
	}
	if err := removeIfExists(filepath.Join(root, speakersRawASRTranscript)); err != nil {
		return err
	}
	// After the raw-asr copy: while the build's summary is kept, the next
	// apply knows the summary was rewritten and reports it restored.
	return removeIfExists(filepath.Join(root, speakersBaseSummary))
}

// summarizeSpeakersFn writes a new summary for an edited transcript and names
// the model that wrote it; tests replace it so no LLM is called.
var summarizeSpeakersFn = func(t transcribe.TranscriptSpeakers) (body, model string, err error) {
	cfg := transcribe.DefaultBuildConfig().SummaryLLM
	if !cfg.IsConfigured() {
		return "", "", errSummaryModelUnavailable
	}
	body, err = t.Summarize(cfg)
	return body, cfg.Model, err
}

var errSummaryModelUnavailable = errors.New("no summary model configured")

// speakerSummaryPlan is what an apply does to summary.md, decided before the
// bundle is touched.
type speakerSummaryPlan struct {
	status string // the report's summary value
	body   []byte // the new summary.md, nil to leave it as it is
	record *speakerSummaryRecord
}

// planSpeakerSummary rewrites the summary for the edited speakers, so its
// action items name the people the transcript now names. It calls the
// summary model only when summary.md is not already the one written for
// these exact edits: a retried apply, or a replay onto a bundle this apply
// already wrote, costs nothing, while a rebuilt bundle (whose fresh summary
// has no record) is rewritten. A meeting without a summary gets none.
func planSpeakerSummary(root string, previous *speakerSummaryRecord, edited transcribe.TranscriptSpeakers, editsSHA256 string, stderr io.Writer) (speakerSummaryPlan, error) {
	path := filepath.Join(root, speakersSummaryFile)
	if !fileExists(path) && !fileExists(filepath.Join(root, speakersBaseSummary)) {
		return speakerSummaryPlan{status: "none"}, nil
	}
	// The record is kept only while summary.md is still the file it names.
	if previous != nil {
		current, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return speakerSummaryPlan{}, fmt.Errorf("read summary: %w", err)
		}
		if err != nil || sha256Hex(current) != previous.SHA256 {
			previous = nil
		}
	}
	// Keyed on what the model reads, not on the edits document: a new
	// revision with the same effect (a label for a voice that has no words,
	// the same edits saved again) gives the same transcript.
	transcriptSHA256, err := edited.SHA256()
	if err != nil {
		return speakerSummaryPlan{}, fmt.Errorf("hash edited transcript: %w", err)
	}
	if previous != nil && previous.TranscriptSHA256 == transcriptSHA256 {
		kept := *previous
		kept.EditsSHA256 = editsSHA256
		return speakerSummaryPlan{status: "unchanged", record: &kept}, nil
	}
	body, model, err := summarizeSpeakersFn(edited)
	if err != nil {
		// The transcript is still right; only the prose lags behind it.
		// No record for these edits, so applying them again retries.
		fmt.Fprintf(stderr, "cassini speakers apply: summary not rewritten, it still credits the earlier speakers: %v\n", err)
		return speakerSummaryPlan{status: "stale", record: previous}, nil
	}
	return speakerSummaryPlan{
		status: "regenerated",
		body:   []byte(body),
		record: &speakerSummaryRecord{Rewritten: true, Model: model, SHA256: sha256Hex([]byte(body)), EditsSHA256: editsSHA256, TranscriptSHA256: transcriptSHA256},
	}, nil
}

// writeSpeakerSummary writes a planned summary. The build's own summary is
// kept byte for byte beside it, the first time, so undoing every edit
// restores it.
func writeSpeakerSummary(root string, plan speakerSummaryPlan) error {
	if plan.body == nil {
		return nil
	}
	path := filepath.Join(root, speakersSummaryFile)
	basePath := filepath.Join(root, speakersBaseSummary)
	if !fileExists(basePath) && fileExists(path) {
		original, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read summary: %w", err)
		}
		if err := writeFileDurable(basePath, original, 0o644); err != nil {
			return fmt.Errorf("keep original summary: %w", err)
		}
	}
	if err := writeFileAtomic(path, plan.body, 0o644); err != nil {
		return fmt.Errorf("write summary: %w", err)
	}
	return nil
}

// putBackSpeakerSummary writes the build's own summary back over summary.md,
// keeping the copy; restoreSpeakerBase removes the copy last.
func putBackSpeakerSummary(root string) error {
	basePath := filepath.Join(root, speakersBaseSummary)
	if !fileExists(basePath) {
		return nil
	}
	original, err := os.ReadFile(basePath)
	if err != nil {
		return fmt.Errorf("read original summary: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(root, speakersSummaryFile), original, 0o644); err != nil {
		return fmt.Errorf("restore summary: %w", err)
	}
	return nil
}

// restoreSpeakerSummary puts the build's own summary back and drops the copy.
func restoreSpeakerSummary(root string) error {
	if err := putBackSpeakerSummary(root); err != nil {
		return err
	}
	return removeIfExists(filepath.Join(root, speakersBaseSummary))
}

func regenerateSpeakerCaptions(root string, transcript transcribe.TranscriptSpeakers) error {
	path := filepath.Join(root, speakersCaptionsFile)
	if !fileExists(path) {
		return nil
	}
	if err := transcript.WriteCaptionsVTT(path); err != nil {
		return fmt.Errorf("write captions: %w", err)
	}
	return nil
}

// renamesSpeaker reports whether a speaker in both rosters has a new label.
// Speakers that come or go are a split's or a merge's doing, counted apart.
func renamesSpeaker(base, result []transcribe.RosterEntry) bool {
	labels := make(map[string]string, len(base))
	for _, r := range base {
		labels[r.ID] = r.Label
	}
	for _, r := range result {
		if label, ok := labels[r.ID]; ok && label != r.Label {
			return true
		}
	}
	return false
}

func rawInt(raw json.RawMessage) (int, error) {
	if len(raw) == 0 {
		return 0, nil
	}
	var n int
	err := json.Unmarshal(raw, &n)
	return n, err
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

// ---------------------------------------------------------------- show

type speakersShowResult struct {
	Speakers          []speakersShowEntry `json:"speakers"`
	DefaultTranscript string              `json:"defaultTranscript"`
	Transcripts       []string            `json:"transcripts"`
	EditsRevision     *int                `json:"editsRevision"`
}

type speakersShowEntry struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Device string `json:"device,omitempty"`
}

func runSpeakersShow(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cassini speakers show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	emitJSON := fs.Bool("json", false, "print JSON")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini speakers show ./meetings/meeting.meeting [--json]
  cassini speakers show ./Meeting.opus [--json]

`)
		fs.PrintDefaults()
	}
	files, code := parseAnnotateArgs(fs, args, 1)
	if code >= 0 {
		return code
	}
	var result speakersShowResult
	var err error
	if info, statErr := os.Stat(files[0]); statErr == nil && info.IsDir() {
		result, err = showMeetingBundleSpeakers(files[0])
	} else {
		result, err = showPortableSpeakers(files[0])
	}
	if err != nil {
		fmt.Fprintf(stderr, "cassini speakers show: %v\n", err)
		return speakersExitRuntime
	}
	if *emitJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(result); err != nil {
			fmt.Fprintf(stderr, "cassini speakers show: %v\n", err)
			return speakersExitRuntime
		}
		return 0
	}
	fmt.Fprintf(stdout, "default transcript: %s (of %s)\n", result.DefaultTranscript, strings.Join(result.Transcripts, ", "))
	if result.EditsRevision != nil {
		fmt.Fprintf(stdout, "speaker edits revision: %d\n", *result.EditsRevision)
	}
	for _, s := range result.Speakers {
		if s.Device != "" {
			fmt.Fprintf(stdout, "  %s  %s  (voice on %s)\n", s.ID, s.Label, s.Device)
			continue
		}
		fmt.Fprintf(stdout, "  %s  %s\n", s.ID, s.Label)
	}
	return 0
}

func showMeetingBundleSpeakers(dir string) (speakersShowResult, error) {
	var manifest struct {
		Files struct {
			Transcript  string `json:"transcript"`
			Transcripts []struct {
				ID      string `json:"id"`
				Default bool   `json:"default"`
			} `json:"transcripts"`
		} `json:"files"`
		SpeakerDiarization *speakerDiarizationRecord `json:"x-speakerDiarization"`
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return speakersShowResult{}, err
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return speakersShowResult{}, fmt.Errorf("parse manifest.json: %w", err)
	}
	primary := manifest.Files.Transcript
	if primary == "" {
		primary = speakersDefaultTranscript
	}
	transcript, err := transcribe.ReadTranscriptSpeakers(filepath.Join(dir, primary))
	if err != nil {
		return speakersShowResult{}, err
	}
	// Devices are named as the build named them.
	deviceLabels := map[string]string{}
	if original, err := transcribe.ReadTranscriptSpeakers(filepath.Join(dir, speakersRawASRTranscript)); err == nil {
		for _, s := range original.Roster() {
			deviceLabels[s.ID] = s.Label
		}
	}
	result := speakersShowResult{Speakers: []speakersShowEntry{}, Transcripts: []string{}}
	for _, s := range transcript.Roster() {
		result.Speakers = append(result.Speakers, showEntry(s.ID, s.Label, deviceLabels))
	}
	for _, t := range manifest.Files.Transcripts {
		result.Transcripts = append(result.Transcripts, t.ID)
		if t.Default && result.DefaultTranscript == "" {
			result.DefaultTranscript = t.ID
		}
	}
	if len(result.Transcripts) == 0 {
		// What pack calls a bundle's only transcript.
		result.Transcripts = []string{portable.DefaultWordsTranscriptID}
	}
	if result.DefaultTranscript == "" {
		result.DefaultTranscript = result.Transcripts[0]
	}
	if manifest.SpeakerDiarization != nil {
		rev := manifest.SpeakerDiarization.EditsRevision
		result.EditsRevision = &rev
	}
	return result, nil
}

func showPortableSpeakers(path string) (speakersShowResult, error) {
	manifest, _, err := readPortableMeetingManifest(path)
	if err != nil {
		return speakersShowResult{}, err
	}
	result := speakersShowResult{Speakers: []speakersShowEntry{}, Transcripts: []string{}}
	for _, s := range manifest.Speakers {
		result.Speakers = append(result.Speakers, showEntry(s.ID, s.Label, nil))
	}
	for _, t := range manifest.Transcripts {
		if t.Role != "" {
			continue
		}
		result.Transcripts = append(result.Transcripts, t.ID)
		if t.Default && result.DefaultTranscript == "" {
			result.DefaultTranscript = t.ID
		}
	}
	if result.DefaultTranscript == "" && len(result.Transcripts) > 0 {
		result.DefaultTranscript = result.Transcripts[0]
	}
	if manifest.Provenance != nil && manifest.Provenance.SpeechToText != nil && len(manifest.Provenance.SpeechToText.SpeakerDiarization) > 0 {
		var record speakerDiarizationRecord
		if err := json.Unmarshal(manifest.Provenance.SpeechToText.SpeakerDiarization, &record); err == nil {
			result.EditsRevision = &record.EditsRevision
		}
	}
	return result, nil
}

func showEntry(id, label string, deviceLabels map[string]string) speakersShowEntry {
	entry := speakersShowEntry{ID: id, Label: label}
	if parent := transcribe.VoiceParent(id); parent != "" {
		entry.Device = parent
		if l := deviceLabels[parent]; l != "" {
			entry.Device = l
		}
	}
	return entry
}

// ---------------------------------------------------------------- ordered JSON

// orderedJSONObject is a JSON object whose members keep their order and their
// exact values, so a rewrite changes only what it sets and an unchanged
// document re-serialises byte for byte (the build writes two-space indented
// JSON, which writeOrderedJSON reproduces).
type orderedJSONObject struct {
	members []orderedJSONMember
}

type orderedJSONMember struct {
	key   string
	value json.RawMessage
}

func parseOrderedJSONObject(raw []byte) (*orderedJSONObject, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("not a JSON object")
	}
	obj := &orderedJSONObject{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("unexpected token %v", tok)
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		obj.set(key, value)
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return obj, nil
}

func (o *orderedJSONObject) get(key string) (json.RawMessage, bool) {
	if o == nil {
		return nil, false
	}
	for _, m := range o.members {
		if m.key == key {
			return m.value, true
		}
	}
	return nil, false
}

// object returns the member as an object, nil when absent or null.
func (o *orderedJSONObject) object(key string) (*orderedJSONObject, error) {
	raw, ok := o.get(key)
	if !ok || string(bytes.TrimSpace(raw)) == "null" {
		return nil, nil
	}
	return parseOrderedJSONObject(raw)
}

func (o *orderedJSONObject) set(key string, value json.RawMessage) {
	for i := range o.members {
		if o.members[i].key == key {
			o.members[i].value = value
			return
		}
	}
	o.members = append(o.members, orderedJSONMember{key: key, value: value})
}

func (o *orderedJSONObject) setObject(key string, value *orderedJSONObject) error {
	raw, err := value.MarshalJSON()
	if err != nil {
		return err
	}
	o.set(key, raw)
	return nil
}

func (o *orderedJSONObject) delete(key string) {
	if o == nil {
		return
	}
	for i := range o.members {
		if o.members[i].key == key {
			o.members = append(o.members[:i], o.members[i+1:]...)
			return
		}
	}
}

func (o *orderedJSONObject) clone() *orderedJSONObject {
	out := &orderedJSONObject{}
	if o != nil {
		out.members = append(out.members, o.members...)
	}
	return out
}

func (o *orderedJSONObject) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	if o != nil {
		for i, m := range o.members {
			if i > 0 {
				buf.WriteByte(',')
			}
			key, err := json.Marshal(m.key)
			if err != nil {
				return nil, err
			}
			buf.Write(key)
			buf.WriteByte(':')
			if err := json.Compact(&buf, m.value); err != nil {
				return nil, err
			}
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func orderedString(o *orderedJSONObject, key, fallback string) string {
	raw, ok := o.get(key)
	if !ok {
		return fallback
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil || strings.TrimSpace(s) == "" {
		return fallback
	}
	return strings.TrimSpace(s)
}

func writeOrderedJSON(path string, o *orderedJSONObject) error {
	compact, err := o.MarshalJSON()
	if err != nil {
		return err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", "  "); err != nil {
		return err
	}
	out.WriteByte('\n')
	return writeFileAtomic(path, out.Bytes(), 0o644)
}
