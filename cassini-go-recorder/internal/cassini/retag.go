package cassini

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gocassini/internal/portable"
)

// runRetag rewrites the identity fields of an already-packed portable `.opus`
// without re-encoding its audio.
//
// It exists because a room can be recovered after the fact (D-640). The
// operator's database still holds the Talk token for every meeting it produced,
// so the true room id of a recording published before the field existed is
// reachable — but writing it into `catalog.json` alone does not last. The
// exporter re-derives a catalog entry's room from the FILE on every republish,
// so a recovery that never touched the file is reverted by the next re-seal.
//
// The obvious shortcut does not work. The room lives in the gzipped
// CASSINI_PAYLOAD_* manifest, and the plain CASSINI_ROOM_ID tag is a mirror of
// it for shell readers; `ffmpeg -metadata CASSINI_ROOM_ID=…` produces a file
// that looks correct to `ffprobe`, passes a spot check, and still republishes
// as roomless. Both have to move together, which means decoding the payload,
// editing it and re-encoding it.
//
// Three properties this command holds onto:
//
//   - THE AUDIO IS NOT RE-ENCODED. `-c:a copy`, and the PCM SHA-256 of the
//     output is compared against the manifest's before anything is renamed into
//     place — the same contract `pack` holds. Under D-612 a published recording
//     cannot be deleted, so a corrupt overwrite is permanent; the only place to
//     catch it is before it is sent.
//   - NOTHING ELSE IN THE MANIFEST CHANGES. The payload is edited as a generic
//     JSON document rather than round-tripped through portable.Manifest, so a v2
//     file's per-transcript descriptors, a provenance map, chapters,
//     attachments and any key a future producer added all survive byte-exact in
//     meaning. Only the named fields move.
//   - IT DERIVES NOTHING. --room-id takes an already-derived id. A flag that
//     accepted a Talk token would be a flag that has to be HANDED one, and the
//     entire point of a derived id is that the token does not travel. The
//     callers derive; this writes.
func runRetag(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var (
		outPath         string
		roomID          string
		clearRoomID     bool
		roomName        string
		clearRoomName   bool
		jobID           string
		attemptNumber   int
		clearAttempt    bool
		clearJobID      bool
		emitJSONSummary bool
	)

	fs := flag.NewFlagSet("cassini retag", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&outPath, "out", "", "output portable .opus file (required, must differ from the input)")
	fs.StringVar(&roomID, "room-id", "", "set the derived room id (rm_<16 hex>)")
	fs.BoolVar(&clearRoomID, "clear-room-id", false, "remove the room id")
	fs.StringVar(&roomName, "room-name", "", "set the legacy record-time room name")
	fs.BoolVar(&clearRoomName, "clear-room-name", false, "remove a legacy record-time room name")
	fs.StringVar(&jobID, "job-id", "", "set the id of the operator job that produced this file")
	fs.BoolVar(&clearJobID, "clear-job-id", false, "remove the job id")
	fs.IntVar(&attemptNumber, "attempt-number", 0, "set the 1-based attempt of that job")
	fs.BoolVar(&clearAttempt, "clear-attempt-number", false, "remove the attempt number")
	fs.BoolVar(&emitJSONSummary, "json", false, "print a machine-readable before/after summary instead of a line of prose")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini retag ./meeting.opus --out ./retagged.opus --room-id rm_9f2a1c3d4e5b6a70
  cassini retag ./meeting.opus --out ./retagged.opus \
    --job-id 01K3Q7W8ZC9F0MJXQ2NB8V4RTD --attempt-number 1
  cassini retag ./meeting.opus --out ./retagged.opus --clear-room-id --json

Rewrite the identity fields of an already-packed portable .opus file. The audio
is copied, not re-encoded, and the result is integrity-checked against the
manifest it carries before it is written.

Both the embedded manifest and the plain OpusTags are updated together. Editing
only the tags would produce a file that looks right to ffprobe and still loses
its room the next time it is published, because the exporter reads the manifest.

--room-id takes an ALREADY-DERIVED id, never a Talk conversation token. The id
is a one-way derivation precisely so the token does not travel with the
recording; derive it where the token already is, and pass the result here.

--room-name writes the legacy record-time name that producers no longer emit.
It is here to correct or clear one on an old file, not to start adding them: the
room's current name belongs in the catalog entry.

Every field is left exactly as it was unless a flag names it. Setting and
clearing the same field is refused rather than resolved.

`+"\n")
		fs.PrintDefaults()
	}

	// Mirror build/pack/publish: the input path may come before or after flags.
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

	edits, code := retagEditsFromFlags(retagFlagValues{
		roomID:        roomID,
		clearRoomID:   clearRoomID,
		roomName:      roomName,
		clearRoomName: clearRoomName,
		jobID:         jobID,
		clearJobID:    clearJobID,
		attemptNumber: attemptNumber,
		clearAttempt:  clearAttempt,
	}, fs, stderr)
	if code != 0 {
		return code
	}

	if strings.TrimSpace(outPath) == "" {
		fmt.Fprintln(stderr, "retag configuration error: --out is required")
		return 2
	}
	if !isPortableMeetingOutput(outPath) {
		fmt.Fprintf(stderr, "retag configuration error: --out must be a .opus file, got %s\n", outPath)
		return 2
	}
	// Refused rather than handled. Writing in place would mean staging inside
	// this command and renaming over the only copy, which hides the failure
	// window from the caller — and every caller is a script working on a
	// downloaded temp copy that has an obvious second path.
	if same, err := sameFilePath(inputPath, outPath); err != nil {
		fmt.Fprintf(stderr, "retag configuration error: %v\n", err)
		return 2
	} else if same {
		fmt.Fprintln(stderr, "retag configuration error: --out must differ from the input; retag never writes in place")
		return 2
	}

	summary, err := retagPortableMeeting(ctx, inputPath, outPath, edits)
	if err != nil {
		fmt.Fprintf(stderr, "retag: %v\n", err)
		return 1
	}

	if emitJSONSummary {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(summary); err != nil {
			fmt.Fprintf(stderr, "retag: write summary: %v\n", err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "retagged -> %s\n", outPath)
	for _, change := range summary.Changes {
		fmt.Fprintf(stdout, "  %s: %s -> %s\n", change.Field, retagDisplayValue(change.From), retagDisplayValue(change.To))
	}
	if len(summary.Changes) == 0 {
		fmt.Fprintln(stdout, "  (no field changed; the file was rewritten unchanged)")
	}
	return 0
}

type retagFlagValues struct {
	roomID        string
	clearRoomID   bool
	roomName      string
	clearRoomName bool
	jobID         string
	clearJobID    bool
	attemptNumber int
	clearAttempt  bool
}

// retagEdit is one requested field change. A nil Value means "remove the field".
type retagEdit struct {
	Field string
	Value any
}

// retagEditsFromFlags turns the flag pairs into an ordered edit list, rejecting
// every combination that has more than one reading.
//
// Setting and clearing the same field is an error rather than a precedence
// rule. Any precedence we picked would be a coin flip a caller has to memorise,
// and the caller that produced both flags is a caller with a bug.
func retagEditsFromFlags(values retagFlagValues, fs *flag.FlagSet, stderr io.Writer) ([]retagEdit, int) {
	provided := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { provided[f.Name] = true })

	var edits []retagEdit
	for _, pair := range []struct {
		setFlag   string
		clearFlag string
		field     string
		clearSet  bool
	}{
		{"room-id", "clear-room-id", "roomId", values.clearRoomID},
		{"room-name", "clear-room-name", "roomName", values.clearRoomName},
		{"job-id", "clear-job-id", "jobId", values.clearJobID},
		{"attempt-number", "clear-attempt-number", "attemptNumber", values.clearAttempt},
	} {
		// Presence AND value. Go's flag package accepts `--clear-room-id=false`
		// for a bool, so deciding from fs.Visit alone made the explicit
		// "do NOT clear this" spelling clear the field — the exact opposite of
		// what it says, on a tool that edits published recordings.
		set, clear := provided[pair.setFlag], provided[pair.clearFlag] && pair.clearSet
		if set && clear {
			fmt.Fprintf(stderr, "retag configuration error: --%s and --%s contradict each other\n", pair.setFlag, pair.clearFlag)
			return nil, 2
		}
		if clear {
			edits = append(edits, retagEdit{Field: pair.field})
			continue
		}
		if !set {
			continue
		}
		switch pair.field {
		case "roomId":
			trimmed := strings.TrimSpace(values.roomID)
			// An empty --room-id is refused rather than read as a clear: it is
			// indistinguishable from a shell variable that failed to expand,
			// which is the dangerous kind of ambiguity in a tool that edits
			// published recordings. --clear-room-id says it out loud.
			if trimmed == "" {
				fmt.Fprintln(stderr, "retag configuration error: --room-id is empty; use --clear-room-id to remove it")
				return nil, 2
			}
			if !portable.IsRoomID(trimmed) {
				fmt.Fprintf(stderr, "retag configuration error: --room-id %q is not a derived room id (want rm_ followed by 16 hex characters)\n", trimmed)
				return nil, 2
			}
			edits = append(edits, retagEdit{Field: "roomId", Value: trimmed})
		case "roomName":
			trimmed := strings.TrimSpace(values.roomName)
			if trimmed == "" {
				fmt.Fprintln(stderr, "retag configuration error: --room-name is empty; use --clear-room-name to remove it")
				return nil, 2
			}
			edits = append(edits, retagEdit{Field: "roomName", Value: trimmed})
		case "jobId":
			trimmed := strings.TrimSpace(values.jobID)
			if trimmed == "" {
				fmt.Fprintln(stderr, "retag configuration error: --job-id is empty; use --clear-job-id to remove it")
				return nil, 2
			}
			edits = append(edits, retagEdit{Field: "jobId", Value: trimmed})
		case "attemptNumber":
			if values.attemptNumber < 1 {
				fmt.Fprintf(stderr, "retag configuration error: --attempt-number must be 1 or greater, got %d; use --clear-attempt-number to remove it\n", values.attemptNumber)
				return nil, 2
			}
			edits = append(edits, retagEdit{Field: "attemptNumber", Value: values.attemptNumber})
		}
	}
	return edits, 0
}

// RetagChange is one field that actually moved. A field set to the value it
// already had is not a change and does not appear.
type RetagChange struct {
	Field string `json:"field"`
	From  any    `json:"from"`
	To    any    `json:"to"`
}

// RetagSummary is the --json contract: enough for a caller to assert that the
// edit it asked for is the edit that happened, without re-probing the file.
type RetagSummary struct {
	Input     string        `json:"input"`
	Output    string        `json:"output"`
	MeetingID string        `json:"meetingId"`
	Changes   []RetagChange `json:"changes"`
}

func retagPortableMeeting(ctx context.Context, inputPath, outPath string, edits []retagEdit) (RetagSummary, error) {
	resolvedOut, err := preparePortableMeetingOutput(outPath)
	if err != nil {
		return RetagSummary{}, err
	}
	tags, err := portableMeetingTags(inputPath)
	if err != nil {
		return RetagSummary{}, err
	}
	rawJSON, err := decodePortableMeetingPayload(tags)
	if err != nil {
		return RetagSummary{}, err
	}
	document, err := decodePortableMeetingDocument(rawJSON)
	if err != nil {
		return RetagSummary{}, err
	}

	meeting, ok := document["meeting"].(map[string]any)
	if !ok {
		return RetagSummary{}, fmt.Errorf("not a portable meeting file: the manifest has no meeting object")
	}

	summary := RetagSummary{
		Input:     inputPath,
		Output:    resolvedOut,
		MeetingID: fmt.Sprintf("%v", meeting["id"]),
	}
	for _, edit := range edits {
		before, had := meeting[edit.Field]
		if edit.Value == nil {
			if !had {
				continue
			}
			delete(meeting, edit.Field)
			summary.Changes = append(summary.Changes, RetagChange{Field: edit.Field, From: retagJSONValue(before), To: nil})
			continue
		}
		if had && retagValuesEqual(before, edit.Value) {
			continue
		}
		meeting[edit.Field] = edit.Value
		summary.Changes = append(summary.Changes, RetagChange{Field: edit.Field, From: retagJSONValue(before), To: edit.Value})
	}

	updatedJSON, err := json.Marshal(document)
	if err != nil {
		return RetagSummary{}, fmt.Errorf("encode portable meeting manifest: %w", err)
	}

	// The manifest is re-parsed into the struct AFTER the edit rather than
	// carried alongside it, so the integrity numbers verified below are the
	// ones the output file actually claims — not the ones the input claimed.
	var manifest portable.Manifest
	if err := json.Unmarshal(updatedJSON, &manifest); err != nil {
		return RetagSummary{}, fmt.Errorf("re-read portable meeting manifest: %w", err)
	}

	updatedTags, err := retagOpusTags(tags, updatedJSON, manifest)
	if err != nil {
		return RetagSummary{}, err
	}

	stagePath, err := createPortableStagePath(resolvedOut)
	if err != nil {
		return RetagSummary{}, err
	}
	defer func() { _ = os.Remove(stagePath) }()
	if err := writePortableMeetingFile(ctx, inputPath, stagePath, updatedTags); err != nil {
		return RetagSummary{}, err
	}
	if err := verifyPortableMeetingFile(stagePath, manifest); err != nil {
		return RetagSummary{}, err
	}
	// Read the manifest back OUT of the staged file before committing it.
	//
	// verifyPortableMeetingFile checks the audio, which -c:a copy makes almost
	// impossible to break; what can actually go wrong here is the metadata,
	// which is the only thing this command changes. A chunk set that did not
	// survive the muxer, a payload digest that does not match what was written,
	// a room id that landed in the tags and not the manifest — all of them
	// produce a file that plays perfectly and is unreadable as a meeting.
	//
	// The callers upload the result over a recording that, under D-612, cannot
	// be deleted. This is the last point at which that is preventable.
	written, writtenTags, err := readPortableMeetingManifest(stagePath)
	if err != nil {
		return RetagSummary{}, fmt.Errorf("verify retagged file: %w", err)
	}
	if written.Meeting.RoomID != manifest.Meeting.RoomID ||
		written.Meeting.RoomName != manifest.Meeting.RoomName ||
		written.Meeting.JobID != manifest.Meeting.JobID ||
		written.Meeting.AttemptNumber != manifest.Meeting.AttemptNumber {
		return RetagSummary{}, fmt.Errorf("verify retagged file: the written manifest does not carry the requested fields")
	}
	// And that the mirrors agree with it, since a shell reader believes them.
	if got := portableTagValue(writtenTags, "CASSINI_ROOM_ID"); got != written.Meeting.RoomID {
		return RetagSummary{}, fmt.Errorf("verify retagged file: CASSINI_ROOM_ID is %q, manifest says %q", got, written.Meeting.RoomID)
	}
	if err := commitPortableMeetingOutput(stagePath, resolvedOut); err != nil {
		return RetagSummary{}, err
	}
	return summary, nil
}

// retagOpusTags rebuilds the tag set: every tag the file already had, with the
// payload chunk set replaced and the mirrored fields brought back into line.
//
// Starting from the file's own tags rather than from BuildOpusTags is what
// makes this safe on a file this build did not write. A v2 file carries a
// CASSINI_TX_<ID>_PAYLOAD_* chunk set per transcript, and a future producer may
// carry tags nothing here has heard of; ffmpeg runs with -map_metadata -1, so
// anything not in this map is deleted from the output.
func retagOpusTags(existing map[string]string, payloadJSON []byte, manifest portable.Manifest) (map[string]string, error) {
	chunkSize := portable.DefaultPayloadChunkSize
	encoded, err := portable.EncodePayloadBytes(payloadJSON, chunkSize)
	if err != nil {
		return nil, err
	}

	tags := make(map[string]string, len(existing)+len(encoded.Chunks))
	for key, value := range existing {
		// Drop the OLD payload CHUNKS wholesale. Copying them forward and
		// overwriting the ones that happen to exist in the new set would leave a
		// stale tail behind when the payload shrinks — and a stale
		// CASSINI_PAYLOAD_009 is invisible until a reader trusts a chunk count
		// that no longer matches.
		//
		// Matched on the numbered form specifically, NOT on the
		// `CASSINI_PAYLOAD_` prefix: that prefix also covers
		// CASSINI_PAYLOAD_MIME, _ENCODING and _SCHEMA, which the format spec
		// lists as REQUIRED and which have nothing to do with the chunk set. A
		// blanket prefix drop deleted all three from every re-tagged file —
		// invisibly, because nothing this repo ships needs them to decode.
		if isPayloadChunkTag(key) {
			continue
		}
		tags[key] = value
	}

	// Rewritten rather than deleted-and-re-added, so a file whose producer
	// spelled them in another case ends up with one of each rather than two.
	setTagPreservingCase(tags, "CASSINI_PAYLOAD_CHUNK_COUNT", fmt.Sprintf("%d", len(encoded.Chunks)))
	setTagPreservingCase(tags, "CASSINI_PAYLOAD_SHA256", encoded.SHA256)
	setTagPreservingCase(tags, "CASSINI_PAYLOAD_RAW_BYTES", fmt.Sprintf("%d", encoded.RawBytes))
	setTagPreservingCase(tags, "CASSINI_PAYLOAD_GZIP_BYTES", fmt.Sprintf("%d", encoded.CompressedBytes))
	for idx, chunk := range encoded.Chunks {
		tags[fmt.Sprintf("CASSINI_PAYLOAD_%03d", idx)] = chunk
	}

	// The mirrors. Deleted when the field is gone rather than left stale — a
	// CASSINI_ROOM_ID that disagrees with the manifest is worse than no tag,
	// because the shell readers these exist for would believe it.
	//
	// Case-insensitively, because ffprobe reports Vorbis comment keys in
	// whatever case the muxer wrote them and builds disagree. An exact-case
	// delete on a file tagged `cassini_room_id` would leave the old value in
	// place AND add the new one under a second key, so the file would name two
	// different rooms and which one a reader saw would depend on its iteration
	// order.
	attempt := ""
	if manifest.Meeting.AttemptNumber > 0 {
		attempt = fmt.Sprintf("%d", manifest.Meeting.AttemptNumber)
	}
	for tag, value := range map[string]string{
		"CASSINI_ROOM_ID":        manifest.Meeting.RoomID,
		"CASSINI_ROOM_NAME":      manifest.Meeting.RoomName,
		"CASSINI_JOB_ID":         manifest.Meeting.JobID,
		"CASSINI_ATTEMPT_NUMBER": attempt,
	} {
		if value == "" {
			deleteTagAnyCase(tags, tag)
			continue
		}
		setTagPreservingCase(tags, tag, value)
	}
	return tags, nil
}

// isPayloadChunkTag reports whether a tag is one of the numbered payload chunks
// — CASSINI_PAYLOAD_000 and friends — and not one of the descriptor tags that
// merely share the prefix.
func isPayloadChunkTag(key string) bool {
	const prefix = "CASSINI_PAYLOAD_"
	upper := strings.ToUpper(key)
	if !strings.HasPrefix(upper, prefix) {
		return false
	}
	suffix := upper[len(prefix):]
	if suffix == "" {
		return false
	}
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// setTagPreservingCase writes a tag under the spelling the file already used,
// falling back to the canonical one. See the note on the mirrors above for why
// the case matters.
func setTagPreservingCase(tags map[string]string, canonical, value string) {
	for key := range tags {
		if strings.EqualFold(key, canonical) {
			tags[key] = value
			return
		}
	}
	tags[canonical] = value
}

func deleteTagAnyCase(tags map[string]string, canonical string) {
	for key := range tags {
		if strings.EqualFold(key, canonical) {
			delete(tags, key)
		}
	}
}

// retagValuesEqual compares an existing decoded value against a requested one.
//
// The existing side comes from a UseNumber decode, so an attempt number is a
// json.Number holding the literal text and never an int. Comparing their string
// forms is the whole of it, and it keeps "set it to what it already is" from
// being reported as a change.
func retagValuesEqual(existing any, requested any) bool {
	return fmt.Sprintf("%v", existing) == fmt.Sprintf("%v", requested)
}

// retagJSONValue normalises a decoded value for the summary, so --json emits a
// number as a number rather than as the string a json.Number would marshal to.
func retagJSONValue(value any) any {
	number, ok := value.(json.Number)
	if !ok {
		return value
	}
	if parsed, err := number.Int64(); err == nil {
		return parsed
	}
	return number.String()
}

func retagDisplayValue(value any) string {
	if value == nil {
		return "(absent)"
	}
	return fmt.Sprintf("%v", value)
}

// sameFilePath answers "would writing to out overwrite in?" for paths that may
// name the same file by different routes.
//
// Comparing cleaned strings is not enough: `./a.opus` and `a.opus` compare
// equal after cleaning, but a symlink or a bind mount does not, and the caller
// is a script assembling paths from variables. os.SameFile is the answer where
// both exist; the string comparison is the fallback for an output that does not
// exist yet, which is the normal case.
func sameFilePath(inputPath, outPath string) (bool, error) {
	inAbs, err := filepath.Abs(inputPath)
	if err != nil {
		return false, fmt.Errorf("resolve input path: %w", err)
	}
	outAbs, err := filepath.Abs(outPath)
	if err != nil {
		return false, fmt.Errorf("resolve --out path: %w", err)
	}
	if inAbs == outAbs {
		return true, nil
	}
	inInfo, err := os.Stat(inAbs)
	if err != nil {
		return false, nil
	}
	outInfo, err := os.Stat(outAbs)
	if err != nil {
		return false, nil
	}
	return os.SameFile(inInfo, outInfo), nil
}
