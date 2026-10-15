package cassini

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gocassini/internal/inspect"
	"gocassini/internal/portable"
)

// `cassini upgrade-format` rewrites a portable meeting written in one of the
// pre-publication draft shapes into the published version 1 format.
//
// Readers stopped accepting the drafts when the format was published (commit
// 218c3f6b), but recordings in those shapes exist. This is the one-off way to
// bring them forward, and it holds the same properties as `cassini retag`:
//
//   - THE AUDIO IS NOT RE-ENCODED. The output is a -c:a copy, and its canonical
//     compressed-Opus digest must equal the input's before anything is
//     committed.
//   - NOTHING IS GUESSED. A file is recognised by its (CASSINI_FORMAT,
//     CASSINI_PAYLOAD_SCHEMA) pair AND its manifest's shape; a mismatch is a
//     refusal, not a best effort.
//   - NOTHING IS SILENTLY DROPPED. The manifest is edited as a generic JSON
//     document, so every member v1 has room for is carried as written. The few
//     members v1 cannot hold (the draft exact-PCM integrity record, whose
//     object v1 closes) move under the manifest's open root as
//     `x-upgradedFrom`, and the report names each one.
//
// The shapes it reads, as they were written (see docs/portable-meeting-format.md):
//
//	draft 1   CASSINI_FORMAT=.../1  schema cassini.local/...-v1  one inline `transcript`, exact-pcm
//	draft 2   CASSINI_FORMAT=.../2  schema cassini.local/...-v2  `transcripts` index, exact-pcm
//	draft 3   CASSINI_FORMAT=.../3  schema cassini.local/...-v3  `transcripts` index, exact-opus-audio-v1
//	v1, old schema URL  CASSINI_FORMAT=.../1  schema cassini-format.codemyriad.io  (published v1 before the
//	          schema moved to format.gocassini.com; identical content)
const (
	upgradeFormatDraft2 = "org.cassini.portable-meeting/2"
	upgradeFormatDraft3 = "org.cassini.portable-meeting/3"

	upgradeSchemaDraft1          = "https://cassini.local/spec/cassini-portable-meeting-manifest-v1.schema.json"
	upgradeSchemaDraft2          = "https://cassini.local/spec/cassini-portable-meeting-manifest-v2.schema.json"
	upgradeSchemaDraft3          = "https://cassini.local/spec/cassini-portable-meeting-manifest-v3.schema.json"
	upgradeSchemaPublishedLegacy = "https://cassini-format.codemyriad.io/schema/cassini-portable-meeting-manifest-v1.schema.json"

	upgradeLegacyPCMPolicy = "exact-pcm"
	upgradeLegacyPCMFormat = "s16le"

	// Draft-1 content gets these transcript ids when it moves out of the main
	// payload into chunk sets: the ids the draft-2 packer gave the same
	// content when it synthesised an index from an inline transcript.
	upgradeDraft1WordsID    = portable.DefaultWordsTranscriptID
	upgradeDraft1ReadableID = "readable-cleanup"
	upgradeDraft1DisplayID  = portable.RoleDisplay

	upgradeRecordKey = "x-upgradedFrom"
)

// Shapes, as reported.
const (
	upgradeShapePublished          = "published-v1"
	upgradeShapePublishedOldSchema = "published-v1-old-schema-url"
	upgradeShapeDraft1             = "draft-1"
	upgradeShapeDraft2             = "draft-2"
	upgradeShapeDraft3             = "draft-3"
)

// Statuses, as reported.
const (
	upgradeStatusCurrent    = "current"
	upgradeStatusUpgradable = "upgradable"
	upgradeStatusUpgraded   = "upgraded"
	upgradeStatusRefused    = "refused"
)

// How the source audio was tied to the source metadata before the new digest
// was stamped.
const (
	upgradeAudioOpusDigest = "opus-digest"
	upgradeAudioPCMDigest  = "pcm-digest"
	upgradeAudioPCMShape   = "pcm-shape-only"
)

type upgradeShapeRules struct {
	version int
	inline  bool
	policy  string
}

var upgradeShapes = map[string]upgradeShapeRules{
	upgradeShapePublishedOldSchema: {version: 1, policy: portable.AudioMatchPolicy},
	upgradeShapeDraft1:             {version: 1, inline: true, policy: upgradeLegacyPCMPolicy},
	upgradeShapeDraft2:             {version: 2, policy: upgradeLegacyPCMPolicy},
	upgradeShapeDraft3:             {version: 3, policy: portable.AudioMatchPolicy},
}

// UpgradeFormatChange is one thing the upgrade moved.
type UpgradeFormatChange struct {
	Field string `json:"field"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
}

// UpgradeFormatTranscript is one transcript body as the output carries it.
type UpgradeFormatTranscript struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"` // words | display | readable (a role v1 readers skip)
	Role      string `json:"role,omitempty"`
	Default   bool   `json:"default,omitempty"`
	WordCount int    `json:"wordCount"`
	SHA256    string `json:"sha256"`
	// Origin is "carried" (chunk set copied unchanged) or "moved-inline"
	// (a draft-1 inline body, now in its own chunk set).
	Origin string `json:"origin"`
}

// UpgradeFormatReport is the --json contract and the source of the prose
// report.
type UpgradeFormatReport struct {
	Input             string                    `json:"input"`
	Output            string                    `json:"output,omitempty"`
	Status            string                    `json:"status"`
	Shape             string                    `json:"shape,omitempty"`
	FormatTag         string                    `json:"formatTag,omitempty"`
	SchemaTag         string                    `json:"schemaTag,omitempty"`
	MeetingID         string                    `json:"meetingId,omitempty"`
	AudioVerification string                    `json:"audioVerification,omitempty"`
	OpusAudioSHA256   string                    `json:"opusAudioSha256,omitempty"`
	DefaultWordCount  int                       `json:"defaultWordCount"`
	Transcripts       []UpgradeFormatTranscript `json:"transcripts,omitempty"`
	Changes           []UpgradeFormatChange     `json:"changes,omitempty"`
	// Relocated names every source member v1 has no place for, and where in
	// the output it now lives.
	Relocated []UpgradeFormatChange `json:"relocated,omitempty"`
	Notes     []string              `json:"notes,omitempty"`
	Refusal   string                `json:"refusal,omitempty"`
}

type upgradeFormatOptions struct {
	acceptPCMShape bool
}

func runUpgradeFormat(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var (
		outPath        string
		check          bool
		emitJSON       bool
		acceptPCMShape bool
	)
	fs := flag.NewFlagSet("cassini upgrade-format", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&outPath, "out", "", "output portable .opus file (required unless --check; must differ from the input)")
	fs.BoolVar(&check, "check", false, "report what would change and write nothing")
	fs.BoolVar(&emitJSON, "json", false, "print a machine-readable report")
	fs.BoolVar(&acceptPCMShape, "accept-pcm-shape-match", false,
		"for exact-pcm drafts whose PCM digest this FFmpeg cannot reproduce, accept an exact sample-count match as the audio check")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini upgrade-format ./meeting.opus --check
  cassini upgrade-format ./meeting.opus --out ./meeting.v1.opus
  cassini upgrade-format ./meeting.opus --out ./meeting.v1.opus --json

Rewrite a portable meeting written in a pre-publication draft shape (CASSINI_FORMAT
org.cassini.portable-meeting/2 or /3, draft 1, or /1 under the old
cassini-format.codemyriad.io schema URL) as the published version 1 format.

The audio is copied, never re-encoded, and the output is checked with the current
reader before it is written: same compressed-Opus digest, every transcript body
unchanged, the default transcript readable. A file already in the published format
is reported as current and nothing is written. Anything not recognised exactly is
refused.

Drafts 1 and 2 bound their metadata to the audio with a digest of decoded PCM,
which depends on the decoder. When this FFmpeg reproduces it, the upgrade proceeds.
When it does not, the upgrade is refused unless --accept-pcm-shape-match is given,
in which case an exact decoded sample count stands in for the digest and the report
says so.

`+"\n")
		fs.PrintDefaults()
	}

	parseArgs := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		parseArgs = append(append([]string{}, args[1:]...), args[0])
	}
	if err := fs.Parse(parseArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	inputPath := fs.Arg(0)
	if !strings.EqualFold(filepath.Ext(inputPath), ".opus") {
		fmt.Fprintf(stderr, "upgrade-format configuration error: input must be a .opus file, got %s\n", inputPath)
		return 2
	}
	if !check {
		if strings.TrimSpace(outPath) == "" {
			fmt.Fprintln(stderr, "upgrade-format configuration error: --out is required (or use --check)")
			return 2
		}
		if !strings.EqualFold(filepath.Ext(outPath), ".opus") {
			fmt.Fprintf(stderr, "upgrade-format configuration error: --out must be a .opus file, got %s\n", outPath)
			return 2
		}
		if same, err := sameFilePath(inputPath, outPath); err != nil {
			fmt.Fprintf(stderr, "upgrade-format configuration error: %v\n", err)
			return 2
		} else if same {
			fmt.Fprintln(stderr, "upgrade-format configuration error: --out must differ from the input; upgrade-format never writes in place")
			return 2
		}
	} else if strings.TrimSpace(outPath) != "" {
		fmt.Fprintln(stderr, "upgrade-format configuration error: --check writes nothing; drop --out")
		return 2
	}

	opts := upgradeFormatOptions{acceptPCMShape: acceptPCMShape}
	var (
		report UpgradeFormatReport
		err    error
	)
	if check {
		report, err = checkPortableUpgrade(ctx, inputPath, opts)
	} else {
		report, err = upgradePortableMeeting(ctx, inputPath, outPath, opts)
	}
	if err != nil {
		report.Input = inputPath
		report.Output = ""
		report.Status = upgradeStatusRefused
		report.Refusal = err.Error()
	}

	if emitJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if encodeErr := encoder.Encode(report); encodeErr != nil {
			fmt.Fprintf(stderr, "upgrade-format: write report: %v\n", encodeErr)
			return 1
		}
	} else {
		printUpgradeFormatReport(stdout, report)
	}
	if err != nil {
		if !emitJSON {
			fmt.Fprintf(stderr, "upgrade-format: %v\n", err)
		}
		return 1
	}
	return 0
}

func printUpgradeFormatReport(out io.Writer, report UpgradeFormatReport) {
	switch report.Status {
	case upgradeStatusCurrent:
		fmt.Fprintf(out, "%s: already the published format (%s); nothing written\n", report.Input, report.FormatTag)
		return
	case upgradeStatusRefused:
		fmt.Fprintf(out, "%s: refused\n", report.Input)
		return
	case upgradeStatusUpgradable:
		fmt.Fprintf(out, "%s: %s -> published v1 (check only, nothing written)\n", report.Input, report.Shape)
	case upgradeStatusUpgraded:
		fmt.Fprintf(out, "%s: %s -> published v1 -> %s\n", report.Input, report.Shape, report.Output)
	}
	fmt.Fprintf(out, "  audio: copied, %s check, opus sha256 %s\n", report.AudioVerification, report.OpusAudioSHA256)
	for _, transcript := range report.Transcripts {
		marker := ""
		if transcript.Default {
			marker = " (default)"
		}
		fmt.Fprintf(out, "  transcript %s%s: %s, %d words, %s\n", transcript.ID, marker, transcript.Kind, transcript.WordCount, transcript.Origin)
	}
	for _, change := range report.Changes {
		fmt.Fprintf(out, "  change %s: %s -> %s\n", change.Field, upgradeDisplay(change.From), upgradeDisplay(change.To))
	}
	for _, moved := range report.Relocated {
		fmt.Fprintf(out, "  relocated %s -> %s\n", moved.Field, moved.To)
	}
	for _, note := range report.Notes {
		fmt.Fprintf(out, "  note: %s\n", note)
	}
}

func upgradeDisplay(value string) string {
	if value == "" {
		return "(absent)"
	}
	return value
}

// portableUpgradePlan is everything the write needs, computed and checked
// before anything is written.
type portableUpgradePlan struct {
	report   UpgradeFormatReport
	document map[string]any
	tags     map[string]string
	// bodies maps every transcript id the output indexes to the SHA-256 of
	// its body JSON. Every one must decode out of the written file unchanged.
	bodies map[string]string
	source portableAudioIntegrity
}

func checkPortableUpgrade(ctx context.Context, inputPath string, opts upgradeFormatOptions) (UpgradeFormatReport, error) {
	plan, err := planPortableUpgrade(ctx, inputPath, opts)
	if err != nil {
		return plan.report, err
	}
	if plan.report.Status == upgradeStatusCurrent {
		return plan.report, nil
	}
	plan.report.Status = upgradeStatusUpgradable
	return plan.report, nil
}

func upgradePortableMeeting(ctx context.Context, inputPath, outPath string, opts upgradeFormatOptions) (UpgradeFormatReport, error) {
	plan, err := planPortableUpgrade(ctx, inputPath, opts)
	if err != nil {
		return plan.report, err
	}
	if plan.report.Status == upgradeStatusCurrent {
		return plan.report, nil
	}
	resolvedOut, err := preparePortableMeetingOutput(outPath)
	if err != nil {
		return plan.report, err
	}
	if err := commitPortableUpgrade(ctx, inputPath, resolvedOut, plan); err != nil {
		return plan.report, err
	}
	plan.report.Status = upgradeStatusUpgraded
	plan.report.Output = resolvedOut
	return plan.report, nil
}

// planPortableUpgrade always returns a plan; on a refusal its report holds
// what was learnt about the file before the refusal (shape, tags, meeting id).
func planPortableUpgrade(ctx context.Context, inputPath string, opts upgradeFormatOptions) (*portableUpgradePlan, error) {
	report := &UpgradeFormatReport{Input: inputPath}
	plan, err := buildPortableUpgradePlan(ctx, inputPath, opts, report)
	if err != nil {
		return &portableUpgradePlan{report: *report}, err
	}
	return plan, nil
}

func buildPortableUpgradePlan(ctx context.Context, inputPath string, opts upgradeFormatOptions, report *UpgradeFormatReport) (*portableUpgradePlan, error) {
	tags, err := portableMeetingTags(inputPath)
	if err != nil {
		return nil, err
	}
	formatTag := portableTagValue(tags, "CASSINI_FORMAT")
	schemaTag := portableTagValue(tags, "CASSINI_PAYLOAD_SCHEMA")
	report.FormatTag, report.SchemaTag = formatTag, schemaTag

	shape, err := classifyPortableShape(formatTag, schemaTag)
	if err != nil {
		return nil, err
	}
	report.Shape = shape

	if shape == upgradeShapePublished {
		// Current already. It must also READ as current: a file that claims the
		// published format and fails the reader is damaged, not upgradable,
		// and saying "current" about it would hide that.
		manifest, _, err := readPortableMeetingManifest(inputPath)
		if err != nil {
			return nil, fmt.Errorf("claims the published format but the reader refuses it: %w", err)
		}
		if err := verifyPortableMeetingFile(inputPath, manifest); err != nil {
			return nil, fmt.Errorf("claims the published format but its audio does not verify: %w", err)
		}
		extracted, err := inspect.ExtractMeeting(inputPath)
		if err != nil {
			return nil, fmt.Errorf("claims the published format but the reader refuses it: %w", err)
		}
		report.Status = upgradeStatusCurrent
		report.MeetingID = manifest.Meeting.ID
		report.OpusAudioSHA256 = manifest.Integrity.OpusSHA256
		report.AudioVerification = upgradeAudioOpusDigest
		report.DefaultWordCount = len(extracted.Transcript.Words)
		return &portableUpgradePlan{report: *report}, nil
	}
	rules := upgradeShapes[shape]

	for _, required := range []struct{ name, want string }{
		{"CASSINI_PROFILE", portable.Profile},
		{"CASSINI_PAYLOAD_MIME", portable.PayloadMIME},
		{"CASSINI_PAYLOAD_ENCODING", portable.PayloadEncoding},
	} {
		if got := portableTagValue(tags, required.name); got != required.want {
			return nil, fmt.Errorf("unexpected %s=%q for a %s file", required.name, got, shape)
		}
	}
	if repeated := repeatedPortableTag(tags); repeated != "" {
		return nil, fmt.Errorf("tag %s appears more than once; refusing a file whose load-bearing tags were edited", repeated)
	}

	rawJSON, err := decodeUpgradeMainPayload(tags)
	if err != nil {
		return nil, err
	}
	document, err := decodePortableMeetingDocument(rawJSON)
	if err != nil {
		return nil, err
	}
	if _, exists := document[upgradeRecordKey]; exists {
		return nil, fmt.Errorf("the manifest already carries %s; refusing to stack a second upgrade record", upgradeRecordKey)
	}
	if kind, _ := document["kind"].(string); kind != "cassini-portable-meeting" {
		return nil, fmt.Errorf("unexpected manifest kind %q", kind)
	}
	if profile, _ := document["profile"].(string); profile != portable.Profile {
		return nil, fmt.Errorf("unexpected manifest profile %q", profile)
	}
	if version, ok := upgradeJSONInt(document["version"]); !ok || int(version) != rules.version {
		return nil, fmt.Errorf("%s file has manifest version %v, expected %d", shape, document["version"], rules.version)
	}
	_, hasInline := document["transcript"]
	_, hasIndex := document["transcripts"]
	_, hasReadableIndex := document["readableTranscripts"]
	if rules.inline {
		if !hasInline || hasIndex || hasReadableIndex {
			return nil, fmt.Errorf("%s file must carry one inline transcript and no transcripts index; this one does not", shape)
		}
	} else if hasInline || !hasIndex {
		return nil, fmt.Errorf("%s file must carry a transcripts index and no inline transcript; this one does not", shape)
	}
	meeting, ok := document["meeting"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("the manifest has no meeting object")
	}
	report.MeetingID, _ = meeting["id"].(string)

	integrity, ok := document["integrity"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("the manifest has no integrity object")
	}
	policy, _ := integrity["matchPolicy"].(string)
	if tagPolicy := portableTagValue(tags, "CASSINI_AUDIO_MATCH_POLICY"); policy != rules.policy || tagPolicy != rules.policy {
		return nil, fmt.Errorf("%s file must declare matchPolicy %s; manifest says %q, tag says %q", shape, rules.policy, policy, tagPolicy)
	}

	plan := &portableUpgradePlan{document: document, bodies: map[string]string{}}
	record := map[string]any{
		"format":          formatTag,
		"schema":          schemaTag,
		"manifestVersion": json.Number(strconv.Itoa(rules.version)),
	}

	audio, verification, err := verifyUpgradeSourceAudio(ctx, inputPath, integrity, tags, rules.policy, opts)
	if err != nil {
		return nil, err
	}
	plan.source = audio
	report.AudioVerification = verification
	report.OpusAudioSHA256 = audio.OpusSHA256
	record["audioVerification"] = verification

	newTags := make(map[string]string, len(tags))
	for key, value := range tags {
		newTags[key] = value
	}

	if rules.policy == upgradeLegacyPCMPolicy {
		record["integrity"] = integrity
		document["integrity"] = map[string]any{
			"matchPolicy":     portable.AudioMatchPolicy,
			"opusAudioSha256": audio.OpusSHA256,
			"sampleRate":      json.Number(strconv.Itoa(audio.SampleRate)),
			"channels":        json.Number(strconv.Itoa(audio.Channels)),
			"sampleCount":     json.Number(strconv.FormatInt(audio.SampleCount, 10)),
			"durationMs":      json.Number(strconv.FormatInt(audio.DurationMS, 10)),
		}
		pcmDigest, _ := integrity["pcmSha256"].(string)
		report.Changes = append(report.Changes, UpgradeFormatChange{
			Field: "integrity",
			From:  upgradeLegacyPCMPolicy + " pcmSha256=" + pcmDigest,
			To:    portable.AudioMatchPolicy + " opusAudioSha256=" + audio.OpusSHA256,
		})
		report.Relocated = append(report.Relocated, UpgradeFormatChange{
			Field: "integrity (exact-pcm record: " + strings.Join(upgradeSortedKeys(integrity), ", ") + ")",
			To:    upgradeRecordKey + ".integrity",
		})
		for _, name := range []string{"CASSINI_AUDIO_PCM_FORMAT", "CASSINI_AUDIO_PCM_SHA256"} {
			if value := portableTagValue(newTags, name); value != "" {
				deleteTagAnyCase(newTags, name)
				report.Changes = append(report.Changes, UpgradeFormatChange{Field: "tag " + name, From: value})
			}
		}
		setTagPreservingCase(newTags, "CASSINI_AUDIO_MATCH_POLICY", portable.AudioMatchPolicy)
		setTagPreservingCase(newTags, "CASSINI_AUDIO_OPUS_SHA256", audio.OpusSHA256)
		report.Changes = append(report.Changes,
			UpgradeFormatChange{Field: "tag CASSINI_AUDIO_MATCH_POLICY", From: upgradeLegacyPCMPolicy, To: portable.AudioMatchPolicy},
			UpgradeFormatChange{Field: "tag CASSINI_AUDIO_OPUS_SHA256", To: audio.OpusSHA256},
		)
		if strings.HasPrefix(report.MeetingID, "mtg_") && strings.TrimPrefix(report.MeetingID, "mtg_") == pcmDigest {
			report.Notes = append(report.Notes, "meeting.id is kept as written; it derives from the draft's PCM digest, not the new compressed-audio digest")
		}
	}

	if rules.inline {
		if err := planDraft1Transcripts(document, newTags, record, plan, report); err != nil {
			return nil, err
		}
	} else {
		if err := planIndexedTranscripts(document, tags, plan, report); err != nil {
			return nil, err
		}
	}

	if rules.version != portable.WireVersion {
		document["version"] = json.Number(strconv.Itoa(portable.WireVersion))
		report.Changes = append(report.Changes, UpgradeFormatChange{Field: "version", From: strconv.Itoa(rules.version), To: strconv.Itoa(portable.WireVersion)})
	}
	if _, legacy := meeting["roomName"]; legacy {
		report.Notes = append(report.Notes, "meeting.roomName (legacy, no longer written) is carried unchanged; v1 meeting is open")
	}
	record["tool"] = "cassini upgrade-format"
	document[upgradeRecordKey] = record
	report.Changes = append(report.Changes, UpgradeFormatChange{Field: upgradeRecordKey, To: "added (records the source format, schema and audio check)"})

	for _, tag := range []struct{ name, value string }{
		{"CASSINI_FORMAT", portable.Format},
		{"CASSINI_PAYLOAD_SCHEMA", portable.PayloadSchema},
		{"CASSINI_DECODE_HINT", portable.DecodeHint},
	} {
		before := portableTagValue(newTags, tag.name)
		if before == tag.value {
			continue
		}
		setTagPreservingCase(newTags, tag.name, tag.value)
		report.Changes = append(report.Changes, UpgradeFormatChange{Field: "tag " + tag.name, From: before, To: tag.value})
	}

	// The index decides; the discoverability tags follow it.
	ids := make([]string, 0, len(report.Transcripts))
	defaultID := ""
	for _, transcript := range report.Transcripts {
		ids = append(ids, transcript.ID)
		if transcript.Kind == "words" && transcript.Default && defaultID == "" {
			defaultID = transcript.ID
		}
	}
	if defaultID == "" {
		for _, transcript := range report.Transcripts {
			if transcript.Kind == "words" {
				defaultID = transcript.ID
				break
			}
		}
	}
	sort.Strings(ids)
	for _, tag := range []struct{ name, value string }{
		{"CASSINI_TRANSCRIPT_IDS", strings.Join(ids, ",")},
		{"CASSINI_TRANSCRIPT_DEFAULT", defaultID},
	} {
		before := portableTagValue(newTags, tag.name)
		if before == tag.value {
			continue
		}
		setTagPreservingCase(newTags, tag.name, tag.value)
		report.Changes = append(report.Changes, UpgradeFormatChange{Field: "tag " + tag.name, From: before, To: tag.value})
	}
	for _, transcript := range report.Transcripts {
		if transcript.ID == defaultID {
			report.DefaultWordCount = transcript.WordCount
		}
	}

	// The target must be a manifest the current reader accepts before any
	// byte is written.
	targetJSON, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode upgraded manifest: %w", err)
	}
	if _, err := portable.DecodePublishedManifest(targetJSON); err != nil {
		return nil, fmt.Errorf("the upgraded manifest would not read as published v1: %w", err)
	}
	if _, present := document["annotations"]; present {
		// Carried as written. Readers never fail a recording over its marks,
		// and the marks bind to the compressed-Opus digest, which an upgrade
		// records but never changes.
		report.Notes = append(report.Notes, "annotations are carried unchanged")
	}

	plan.tags = newTags
	plan.report = *report
	return plan, nil
}

func classifyPortableShape(formatTag, schemaTag string) (string, error) {
	switch {
	case formatTag == "":
		return "", fmt.Errorf("not a portable meeting file: missing CASSINI_FORMAT")
	case formatTag == portable.Format && schemaTag == portable.PayloadSchema:
		return upgradeShapePublished, nil
	case formatTag == portable.Format && schemaTag == upgradeSchemaPublishedLegacy:
		return upgradeShapePublishedOldSchema, nil
	case formatTag == portable.Format && schemaTag == upgradeSchemaDraft1:
		return upgradeShapeDraft1, nil
	case formatTag == upgradeFormatDraft2 && schemaTag == upgradeSchemaDraft2:
		return upgradeShapeDraft2, nil
	case formatTag == upgradeFormatDraft3 && schemaTag == upgradeSchemaDraft3:
		return upgradeShapeDraft3, nil
	}
	return "", fmt.Errorf("unrecognised CASSINI_FORMAT=%q with CASSINI_PAYLOAD_SCHEMA=%q; not a shape this command knows", formatTag, schemaTag)
}

// repeatedPortableTag names a load-bearing tag ffprobe reported more than
// once (it joins repeats with ';'), which the current reader treats as an
// edited file.
func repeatedPortableTag(tags map[string]string) string {
	for key, value := range tags {
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "CASSINI_") && strings.Contains(value, ";") &&
			(isPayloadChunkTag(upper) || strings.Contains(upper, "_PAYLOAD_")) {
			return key
		}
	}
	return ""
}

func decodeUpgradeMainPayload(tags map[string]string) ([]byte, error) {
	chunkCount, err := positivePortableTagInt(tags, "CASSINI_PAYLOAD_CHUNK_COUNT")
	if err != nil {
		return nil, err
	}
	raw, gzipLen, err := decodeUpgradeChunkSet(tags, "CASSINI_PAYLOAD_", chunkCount)
	if err != nil {
		return nil, fmt.Errorf("manifest payload: %w", err)
	}
	if declared, err := positivePortableTagInt(tags, "CASSINI_PAYLOAD_GZIP_BYTES"); err != nil || declared != gzipLen {
		return nil, fmt.Errorf("manifest payload gzip byte count mismatch")
	}
	if declared, err := positivePortableTagInt(tags, "CASSINI_PAYLOAD_RAW_BYTES"); err != nil || declared != len(raw) {
		return nil, fmt.Errorf("manifest payload raw byte count mismatch")
	}
	if declared := portableTagValue(tags, "CASSINI_PAYLOAD_SHA256"); declared != upgradeSHA256(raw) {
		return nil, fmt.Errorf("manifest payload sha256 mismatch")
	}
	return raw, nil
}

// decodeUpgradeChunkSet concatenates <prefix>000..N-1, base64url-decodes and
// gunzips them, returning the body and the compressed length.
func decodeUpgradeChunkSet(tags map[string]string, prefix string, chunkCount int) ([]byte, int, error) {
	var encoded strings.Builder
	for idx := 0; idx < chunkCount; idx++ {
		key := fmt.Sprintf("%s%03d", prefix, idx)
		chunk := portableTagValue(tags, key)
		if chunk == "" {
			return nil, 0, fmt.Errorf("%s is missing", key)
		}
		encoded.WriteString(chunk)
	}
	if extra := portableTagValue(tags, fmt.Sprintf("%s%03d", prefix, chunkCount)); extra != "" {
		return nil, 0, fmt.Errorf("%s%03d exists beyond the declared %d chunks", prefix, chunkCount, chunkCount)
	}
	compressed, err := base64.RawURLEncoding.DecodeString(encoded.String())
	if err != nil {
		return nil, 0, fmt.Errorf("decode base64url: %w", err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, 0, fmt.Errorf("open gzip: %w", err)
	}
	defer func() { _ = reader.Close() }()
	raw, err := io.ReadAll(reader)
	if err != nil {
		return nil, 0, fmt.Errorf("decompress gzip: %w", err)
	}
	return raw, len(compressed), nil
}

func upgradeSHA256(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// verifyUpgradeSourceAudio ties the source metadata to the source audio before
// a new digest is stamped onto it: an upgrade must never be the step that
// declares a stale transcript verified.
func verifyUpgradeSourceAudio(ctx context.Context, path string, integrity map[string]any, tags map[string]string, policy string, opts upgradeFormatOptions) (portableAudioIntegrity, string, error) {
	audio, err := computePortableAudioIntegrity(path)
	if err != nil {
		return portableAudioIntegrity{}, "", err
	}
	sampleRate, _ := upgradeJSONInt(integrity["sampleRate"])
	channels, _ := upgradeJSONInt(integrity["channels"])
	sampleCount, _ := upgradeJSONInt(integrity["sampleCount"])
	durationMS, _ := upgradeJSONInt(integrity["durationMs"])
	if int(sampleRate) != audio.SampleRate || int(channels) != audio.Channels {
		return portableAudioIntegrity{}, "", fmt.Errorf("audio shape disagrees with the manifest: manifest %d Hz x%d, file %d Hz x%d",
			sampleRate, channels, audio.SampleRate, audio.Channels)
	}
	if sampleCount != audio.SampleCount {
		return portableAudioIntegrity{}, "", fmt.Errorf("audio sample count disagrees with the manifest: manifest=%d compressed-Opus=%d", sampleCount, audio.SampleCount)
	}

	if policy == portable.AudioMatchPolicy {
		digest, _ := integrity["opusAudioSha256"].(string)
		if tagged := portableTagValue(tags, "CASSINI_AUDIO_OPUS_SHA256"); tagged != digest {
			return portableAudioIntegrity{}, "", fmt.Errorf("compressed Opus sha256 disagrees between manifest (%s) and tag (%s)", digest, tagged)
		}
		if digest != audio.OpusSHA256 {
			return portableAudioIntegrity{}, "", fmt.Errorf("compressed Opus sha256 mismatch: manifest=%s actual=%s; the metadata does not belong to this audio", digest, audio.OpusSHA256)
		}
		if durationMS != audio.DurationMS {
			return portableAudioIntegrity{}, "", fmt.Errorf("duration disagrees with the manifest: manifest=%d actual=%d", durationMS, audio.DurationMS)
		}
		return audio, upgradeAudioOpusDigest, nil
	}

	if deltaMS := durationMS - audio.DurationMS; deltaMS > 1 || deltaMS < -1 {
		return portableAudioIntegrity{}, "", fmt.Errorf("duration disagrees with the manifest: manifest=%d actual=%d", durationMS, audio.DurationMS)
	}
	if format, _ := integrity["pcmFormat"].(string); format != "" && !strings.EqualFold(format, upgradeLegacyPCMFormat) {
		return portableAudioIntegrity{}, "", fmt.Errorf("unsupported integrity pcmFormat %q", format)
	}
	digest, _ := integrity["pcmSha256"].(string)
	digest = strings.ToLower(strings.TrimSpace(digest))
	if !validPortableSHA256(digest) {
		return portableAudioIntegrity{}, "", fmt.Errorf("exact-pcm draft without a valid pcmSha256")
	}
	if tagged := strings.ToLower(portableTagValue(tags, "CASSINI_AUDIO_PCM_SHA256")); tagged != "" && tagged != digest {
		return portableAudioIntegrity{}, "", fmt.Errorf("PCM sha256 disagrees between manifest (%s) and tag (%s)", digest, tagged)
	}
	pcmDigest, pcmSamples, err := hashUpgradeDecodedPCM(ctx, path, audio.SampleRate, audio.Channels)
	if err != nil {
		return portableAudioIntegrity{}, "", err
	}
	if pcmDigest == digest {
		return audio, upgradeAudioPCMDigest, nil
	}
	if pcmSamples != sampleCount {
		return portableAudioIntegrity{}, "", fmt.Errorf("decoded PCM sha256 and sample count both disagree with the manifest (samples manifest=%d decoded=%d); the metadata does not belong to this audio", sampleCount, pcmSamples)
	}
	if !opts.acceptPCMShape {
		return portableAudioIntegrity{}, "", fmt.Errorf("this FFmpeg's decode does not reproduce the draft's PCM sha256 (manifest=%s decoded=%s), though the decoded sample count matches exactly; the PCM digest depends on the decoder, so pass --accept-pcm-shape-match to accept the sample-count match as the audio check", digest, pcmDigest)
	}
	return audio, upgradeAudioPCMShape, nil
}

// hashUpgradeDecodedPCM decodes the audio the way the draft-1/draft-2 writer
// hashed it (s16le at the stream's rate and channel count) and returns the
// digest and the decoded sample count.
func hashUpgradeDecodedPCM(ctx context.Context, path string, sampleRate, channels int) (string, int64, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-v", "error",
		"-i", path,
		"-map", "0:a:0",
		"-f", "s16le",
		"-acodec", "pcm_s16le",
		"-ar", strconv.Itoa(sampleRate),
		"-ac", strconv.Itoa(channels),
		"-",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", 0, fmt.Errorf("open ffmpeg PCM output: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", 0, fmt.Errorf("start ffmpeg PCM decode: %w", err)
	}
	digest := sha256.New()
	byteCount, copyErr := io.Copy(digest, stdout)
	waitErr := cmd.Wait()
	if copyErr != nil {
		return "", 0, fmt.Errorf("read ffmpeg PCM output: %w", copyErr)
	}
	if waitErr != nil {
		return "", 0, fmt.Errorf("ffmpeg PCM decode: %w: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	frame := int64(2 * channels)
	if frame <= 0 || byteCount%frame != 0 {
		return "", 0, fmt.Errorf("ffmpeg produced %d PCM bytes, not a whole number of %d-byte frames", byteCount, frame)
	}
	return hex.EncodeToString(digest.Sum(nil)), byteCount / frame, nil
}

// planIndexedTranscripts checks every chunk set a draft-2, draft-3 or
// old-schema file indexes. The bodies are carried unchanged, so each must
// already be one the current reader accepts.
func planIndexedTranscripts(document map[string]any, tags map[string]string, plan *portableUpgradePlan, report *UpgradeFormatReport) error {
	words, err := upgradeEntryList(document, "transcripts")
	if err != nil {
		return err
	}
	readable, err := upgradeEntryList(document, "readableTranscripts")
	if err != nil {
		return err
	}
	defaults := 0
	rolesOnWords := false
	for _, entry := range words {
		transcript, raw, err := checkUpgradeChunkSet(tags, entry)
		if err != nil {
			return err
		}
		var body portable.TranscriptBody
		if err := json.Unmarshal(raw, &body); err != nil {
			return fmt.Errorf("transcript %q body: %w", transcript.ID, err)
		}
		if err := portable.ValidateTranscriptBody(body); err != nil {
			return fmt.Errorf("transcript %q body would not read as published v1: %w", transcript.ID, err)
		}
		if _, ok := entry["role"]; ok {
			rolesOnWords = true
		}
		transcript.Kind = "words"
		transcript.WordCount = body.WordCount
		transcript.Default, _ = entry["default"].(bool)
		if transcript.Default {
			defaults++
		}
		transcript.Origin = "carried"
		plan.bodies[transcript.ID] = transcript.SHA256
		report.Transcripts = append(report.Transcripts, transcript)
	}
	if defaults > 1 {
		return fmt.Errorf("%d word transcripts are flagged default; refusing to choose one", defaults)
	}
	if rolesOnWords {
		report.Notes = append(report.Notes, "word transcripts keep their draft role members; v1 readers ignore role on word transcripts")
	}
	for _, entry := range readable {
		transcript, raw, err := checkUpgradeChunkSet(tags, entry)
		if err != nil {
			return err
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			return fmt.Errorf("readable transcript %q body is not a JSON object: %w", transcript.ID, err)
		}
		if transcript.Role == portable.RoleDisplay {
			transcript.Kind = "display"
		} else {
			transcript.Kind = "readable"
			report.Notes = append(report.Notes, fmt.Sprintf("readable transcript %q (role %s) is carried; v1 readers skip readable roles other than display", transcript.ID, transcript.Role))
		}
		if count, ok := upgradeJSONInt(entry["wordCount"]); ok {
			transcript.WordCount = int(count)
		}
		transcript.Origin = "carried"
		plan.bodies[transcript.ID] = transcript.SHA256
		report.Transcripts = append(report.Transcripts, transcript)
	}
	return nil
}

func upgradeEntryList(document map[string]any, key string) ([]map[string]any, error) {
	value, ok := document[key]
	if !ok {
		return nil, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("manifest %s is not an array", key)
	}
	out := make([]map[string]any, 0, len(list))
	for index, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("manifest %s[%d] is not an object", key, index)
		}
		out = append(out, entry)
	}
	return out, nil
}

// checkUpgradeChunkSet decodes one indexed body and checks it against its
// payloadRef, exactly as the current reader will.
func checkUpgradeChunkSet(tags map[string]string, entry map[string]any) (UpgradeFormatTranscript, []byte, error) {
	id, _ := entry["id"].(string)
	if err := portable.ValidateTranscriptID(id); err != nil {
		return UpgradeFormatTranscript{}, nil, fmt.Errorf("transcript id is not valid in published v1: %w", err)
	}
	role, _ := entry["role"].(string)
	refJSON, err := json.Marshal(entry["payloadRef"])
	if err != nil {
		return UpgradeFormatTranscript{}, nil, fmt.Errorf("transcript %q payloadRef: %w", id, err)
	}
	var ref portable.PayloadRef
	decoder := json.NewDecoder(bytes.NewReader(refJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ref); err != nil {
		return UpgradeFormatTranscript{}, nil, fmt.Errorf("transcript %q payloadRef is not one published v1 reads: %w", id, err)
	}
	if ref.Encoding != portable.PayloadEncoding {
		return UpgradeFormatTranscript{}, nil, fmt.Errorf("transcript %q has payload encoding %q", id, ref.Encoding)
	}
	if ref.ChunkCount < 1 {
		return UpgradeFormatTranscript{}, nil, fmt.Errorf("transcript %q has no chunks", id)
	}
	raw, gzipLen, err := decodeUpgradeChunkSet(tags, ref.Prefix, ref.ChunkCount)
	if err != nil {
		return UpgradeFormatTranscript{}, nil, fmt.Errorf("transcript %q: %w", id, err)
	}
	digest := upgradeSHA256(raw)
	if digest != ref.SHA256 || len(raw) != ref.RawBytes || gzipLen != ref.GzipBytes {
		return UpgradeFormatTranscript{}, nil, fmt.Errorf("transcript %q body does not match its payloadRef", id)
	}
	return UpgradeFormatTranscript{ID: id, Role: role, SHA256: digest}, raw, nil
}

// planDraft1Transcripts moves draft 1's inline bodies into their own chunk
// sets and the matching single-step provenance into per-transcript maps,
// which is how the draft-2 packer represented the same content.
func planDraft1Transcripts(document map[string]any, tags map[string]string, record map[string]any, plan *portableUpgradePlan, report *UpgradeFormatReport) error {
	for key := range tags {
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "CASSINI_TX_") || upper == "CASSINI_TRANSCRIPT_IDS" || upper == "CASSINI_TRANSCRIPT_DEFAULT" {
			return fmt.Errorf("a draft-1 file carries transcript chunk-set tag %s; refusing a mixed shape", key)
		}
	}
	meeting, _ := document["meeting"].(map[string]any)
	processedAt, _ := meeting["processedAtUtc"].(string)
	meetingLanguage, _ := meeting["language"].(string)

	inline, ok := document["transcript"].(map[string]any)
	if !ok {
		return fmt.Errorf("draft-1 inline transcript is not an object")
	}
	inlineJSON, err := json.Marshal(inline)
	if err != nil {
		return err
	}
	var body portable.TranscriptBody
	if err := json.Unmarshal(inlineJSON, &body); err != nil {
		return fmt.Errorf("draft-1 inline transcript: %w", err)
	}
	if err := portable.ValidateTranscriptBody(body); err != nil {
		return fmt.Errorf("draft-1 inline transcript would not read as published v1: %w", err)
	}

	type moved struct {
		id, role, source string
		body             map[string]any
	}
	bodies := []moved{{id: upgradeDraft1WordsID, body: inline}}
	if readable, present := document["readableTranscript"]; present {
		readableBody, ok := readable.(map[string]any)
		if !ok {
			return fmt.Errorf("draft-1 readableTranscript is not an object")
		}
		bodies = append(bodies, moved{id: upgradeDraft1ReadableID, role: "readable-cleanup", source: upgradeDraft1WordsID, body: readableBody})
	}
	if display, present := document["displayTranscript"]; present {
		displayBody, ok := display.(map[string]any)
		if !ok {
			return fmt.Errorf("draft-1 displayTranscript is not an object")
		}
		bodies = append(bodies, moved{id: upgradeDraft1DisplayID, role: portable.RoleDisplay, source: upgradeDraft1WordsID, body: displayBody})
	}

	var wordEntries, readableEntries []any
	for _, item := range bodies {
		format := ""
		if value, ok := item.body["format"].(string); ok {
			format = strings.TrimSpace(value)
		} else if value, ok := item.body["version"].(string); ok {
			format = strings.TrimSpace(value)
		}
		if format == "" {
			return fmt.Errorf("draft-1 %s body names no format or version", item.id)
		}
		mimeRole := ""
		if item.role != "" {
			mimeRole = portable.RoleDisplay // readable MIME for every readable body
		}
		payload, ref, err := portable.EncodeTranscriptBody(item.body, item.id, mimeRole, portable.DefaultPayloadChunkSize)
		if err != nil {
			return err
		}
		entry := map[string]any{
			"id":         item.id,
			"format":     format,
			"payloadRef": upgradePayloadRefDocument(ref),
		}
		transcript := UpgradeFormatTranscript{ID: item.id, Role: item.role, SHA256: payload.SHA256, Origin: "moved-inline"}
		if item.role == "" {
			entry["default"] = true
			entry["wordCount"] = json.Number(strconv.Itoa(body.WordCount))
			language := strings.TrimSpace(body.Language)
			if language == "" {
				language = strings.TrimSpace(meetingLanguage)
			}
			if language != "" {
				entry["language"] = language
			}
			if processedAt != "" {
				entry["createdAtUtc"] = processedAt
			}
			transcript.Kind = "words"
			transcript.Default = true
			transcript.WordCount = body.WordCount
			wordEntries = append(wordEntries, entry)
		} else {
			entry["role"] = item.role
			entry["sourceTranscriptId"] = item.source
			if language, ok := item.body["language"].(string); ok && language != "" {
				entry["language"] = language
			}
			if processedAt != "" {
				entry["createdAtUtc"] = processedAt
			}
			if item.role == portable.RoleDisplay {
				transcript.Kind = "display"
			} else {
				transcript.Kind = "readable"
				report.Notes = append(report.Notes, fmt.Sprintf("draft-1 readableTranscript becomes readable transcript %q (role %s); v1 readers skip readable roles other than display", item.id, item.role))
			}
			readableEntries = append(readableEntries, entry)
		}
		writeUpgradeChunkSetTags(tags, ref, payload)
		plan.bodies[item.id] = payload.SHA256
		report.Transcripts = append(report.Transcripts, transcript)
		source := "transcript"
		if item.id == upgradeDraft1ReadableID {
			source = "readableTranscript"
		} else if item.id == upgradeDraft1DisplayID {
			source = "displayTranscript"
		}
		report.Changes = append(report.Changes, UpgradeFormatChange{Field: source, From: "inline in the main payload", To: "chunk set " + ref.Prefix + " (id " + item.id + ")"})
	}
	delete(document, "transcript")
	delete(document, "readableTranscript")
	delete(document, "displayTranscript")
	document["transcripts"] = wordEntries
	if len(readableEntries) > 0 {
		document["readableTranscripts"] = readableEntries
	}

	// Provenance: draft 1 held one step per stage; v1 keys steps by
	// transcript id.
	if provenance, present := document["provenance"]; present {
		steps, ok := provenance.(map[string]any)
		if !ok {
			return fmt.Errorf("draft-1 provenance is not an object")
		}
		hasBody := map[string]bool{}
		for _, item := range bodies {
			hasBody[item.id] = true
		}
		for _, stage := range []struct{ key, id string }{
			{"speechToText", upgradeDraft1WordsID},
			{"readableCleanup", upgradeDraft1ReadableID},
			{"displayTranscript", upgradeDraft1DisplayID},
		} {
			value, present := steps[stage.key]
			if !present {
				continue
			}
			step, ok := value.(map[string]any)
			if !ok || !upgradeLooksLikeSingleStep(step) {
				return fmt.Errorf("draft-1 provenance.%s is not a single processing step; refusing to guess its keys", stage.key)
			}
			if hasBody[stage.id] {
				steps[stage.key] = map[string]any{stage.id: step}
				report.Changes = append(report.Changes, UpgradeFormatChange{Field: "provenance." + stage.key, From: "one step", To: "keyed by transcript id " + stage.id})
				continue
			}
			if stage.key == "readableCleanup" {
				// Not a v1 member, so a single step is still valid there.
				continue
			}
			delete(steps, stage.key)
			movedProvenance, _ := record["provenance"].(map[string]any)
			if movedProvenance == nil {
				movedProvenance = map[string]any{}
				record["provenance"] = movedProvenance
			}
			movedProvenance[stage.key] = step
			report.Relocated = append(report.Relocated, UpgradeFormatChange{Field: "provenance." + stage.key + " (no matching transcript body)", To: upgradeRecordKey + ".provenance." + stage.key})
		}
	}
	return nil
}

// upgradeLooksLikeSingleStep reports whether a provenance member is one
// processing step (scalar members) rather than a map of steps.
func upgradeLooksLikeSingleStep(step map[string]any) bool {
	for _, value := range step {
		if _, nested := value.(map[string]any); nested {
			return false
		}
	}
	return true
}

func upgradePayloadRefDocument(ref portable.PayloadRef) map[string]any {
	return map[string]any{
		"prefix":     ref.Prefix,
		"chunkCount": json.Number(strconv.Itoa(ref.ChunkCount)),
		"sha256":     ref.SHA256,
		"rawBytes":   json.Number(strconv.Itoa(ref.RawBytes)),
		"gzipBytes":  json.Number(strconv.Itoa(ref.GzipBytes)),
		"mime":       ref.MIME,
		"encoding":   ref.Encoding,
	}
}

// writeUpgradeChunkSetTags writes one chunk set and its descriptor mirrors
// the way the published writer does (buildMultiTranscriptOpusTags).
func writeUpgradeChunkSetTags(tags map[string]string, ref portable.PayloadRef, payload portable.EncodedPayload) {
	prefix := ref.Prefix
	tags[prefix+"MIME"] = ref.MIME
	tags[prefix+"ENCODING"] = portable.PayloadEncoding
	tags[prefix+"CHUNK_COUNT"] = strconv.Itoa(len(payload.Chunks))
	tags[prefix+"SHA256"] = payload.SHA256
	tags[prefix+"RAW_BYTES"] = strconv.Itoa(payload.RawBytes)
	tags[prefix+"GZIP_BYTES"] = strconv.Itoa(payload.CompressedBytes)
	for idx, chunk := range payload.Chunks {
		tags[fmt.Sprintf("%s%03d", prefix, idx)] = chunk
	}
}

// commitPortableUpgrade writes the upgraded file next to the output, proves
// it with the current reader, and only then renames it into place.
func commitPortableUpgrade(ctx context.Context, inputPath, resolvedOut string, plan *portableUpgradePlan) error {
	payloadJSON, err := json.Marshal(plan.document)
	if err != nil {
		return fmt.Errorf("encode upgraded manifest: %w", err)
	}
	manifest, err := portable.DecodePublishedManifest(payloadJSON)
	if err != nil {
		return err
	}
	if manifest.Integrity.OpusSHA256 != plan.source.OpusSHA256 {
		return fmt.Errorf("upgraded manifest digest %s is not the source audio's %s", manifest.Integrity.OpusSHA256, plan.source.OpusSHA256)
	}
	tags, err := retagOpusTags(plan.tags, payloadJSON, manifest)
	if err != nil {
		return err
	}
	stagePath, err := createPortableStagePath(resolvedOut)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(stagePath) }()
	if err := writePortableMeetingFile(ctx, inputPath, stagePath, tags); err != nil {
		return err
	}
	// The audio: the copy's canonical compressed-Opus digest and shape must be
	// the source's.
	if err := verifyPortableMeetingFile(stagePath, manifest); err != nil {
		return err
	}
	if err := verifyUpgradedFile(stagePath, payloadJSON, plan); err != nil {
		return fmt.Errorf("verify upgraded file: %w", err)
	}
	return commitPortableMeetingOutput(stagePath, resolvedOut)
}

// verifyUpgradedFile reads the staged output back with the current reader:
// the manifest is the one planned, every body decodes to the bytes it had,
// `cassini inspect` finds nothing to warn about, and the default transcript
// extracts with the word count the source had.
func verifyUpgradedFile(stagePath string, payloadJSON []byte, plan *portableUpgradePlan) error {
	written, writtenTags, err := readPortableMeetingManifest(stagePath)
	if err != nil {
		return err
	}
	writtenJSON, err := decodePortableMeetingPayload(writtenTags)
	if err != nil {
		return err
	}
	if !bytes.Equal(writtenJSON, payloadJSON) {
		return fmt.Errorf("the written manifest is not the planned one")
	}
	document, err := decodePortableMeetingDocument(writtenJSON)
	if err != nil {
		return err
	}
	seen := 0
	for _, key := range []string{"transcripts", "readableTranscripts"} {
		entries, err := upgradeEntryList(document, key)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			transcript, _, err := checkUpgradeChunkSet(writtenTags, entry)
			if err != nil {
				return err
			}
			want, ok := plan.bodies[transcript.ID]
			if !ok || want != transcript.SHA256 {
				return fmt.Errorf("transcript %q body changed in the write", transcript.ID)
			}
			seen++
		}
	}
	if seen != len(plan.bodies) {
		return fmt.Errorf("the written file indexes %d transcript bodies, planned %d", seen, len(plan.bodies))
	}

	var inspected bytes.Buffer
	if err := inspect.InspectPath(&inspected, stagePath); err != nil {
		return fmt.Errorf("cassini inspect refuses the output: %w", err)
	}
	for _, line := range strings.Split(inspected.String(), "\n") {
		if strings.HasPrefix(line, "warning=") || strings.HasPrefix(line, "fallback=") {
			return fmt.Errorf("cassini inspect reports %s", line)
		}
		if strings.HasPrefix(line, "portable_meeting=") && !strings.HasSuffix(strings.TrimSpace(line), " cassini=ok") {
			return fmt.Errorf("cassini inspect does not report the output as ok: %s", line)
		}
	}
	extracted, err := inspect.ExtractMeeting(stagePath)
	if err != nil {
		return err
	}
	if len(extracted.Transcript.Words) != plan.report.DefaultWordCount {
		return fmt.Errorf("default transcript reads back with %d words, source had %d", len(extracted.Transcript.Words), plan.report.DefaultWordCount)
	}
	if written.Integrity.OpusSHA256 != plan.source.OpusSHA256 {
		return fmt.Errorf("written digest %s is not the source audio's %s", written.Integrity.OpusSHA256, plan.source.OpusSHA256)
	}
	return nil
}

func upgradeJSONInt(value any) (int64, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	case float64:
		return int64(typed), float64(int64(typed)) == typed
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	}
	return 0, false
}

func upgradeSortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
