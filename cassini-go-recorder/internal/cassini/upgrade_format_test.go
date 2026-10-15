package cassini

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"gocassini/internal/inspect"
	"gocassini/internal/portable"
)

// The fixtures below are synthetic: a meeting packed by today's writer, then
// rewritten into each pre-publication shape the way the draft writers emitted
// it (internal/portable/manifest.go and manifest_v2.go as of 218c3f6b^:
// BuildDraft1OpusTags, EncodeDraft2Manifest, the draft-3 constants). No real
// recording is involved.

type draftFixtureOptions struct {
	// pcmDigest overrides the exact-pcm digest a draft 1 or 2 records. Empty
	// means the digest this host's FFmpeg reproduces.
	pcmDigest string
	// sampleCount overrides integrity.sampleCount (stale-metadata cases).
	sampleCount int64
	// opusDigest overrides integrity.opusAudioSha256 in a draft 3.
	opusDigest string
	// mutate edits the draft manifest just before it is encoded.
	mutate func(document map[string]any, tags map[string]string)
}

type draftFixture struct {
	path string
	// bodies holds every transcript body the draft carries, by the id it has
	// (or, for a draft-1 inline body, will have) in the published file.
	bodies map[string][]byte
	words  []portable.TranscriptItem
	// document is the draft manifest as written.
	document map[string]any
}

func writeDraftFixture(t *testing.T, dir, shape string, opts draftFixtureOptions) draftFixture {
	t.Helper()
	ctx := context.Background()
	v1Path := packFixtureOpus(t, dir, "source-"+shape,
		"--title", "Weekly Sync", "--room-token", "a7bc3k9x",
		"--job-id", "01K3Q7W8ZC9F0MJXQ2NB8V4RTD", "--attempt-number", "2")
	tags, err := portableMeetingTags(v1Path)
	if err != nil {
		t.Fatalf("read fixture tags: %v", err)
	}
	rawJSON, err := decodePortableMeetingPayload(tags)
	if err != nil {
		t.Fatalf("decode fixture payload: %v", err)
	}
	document, err := decodePortableMeetingDocument(rawJSON)
	if err != nil {
		t.Fatalf("parse fixture payload: %v", err)
	}
	audio, err := computePortableAudioIntegrity(v1Path)
	if err != nil {
		t.Fatalf("hash fixture audio: %v", err)
	}

	words, err := upgradeEntryList(document, "transcripts")
	if err != nil || len(words) != 1 {
		t.Fatalf("fixture should index one words transcript, got %d (%v)", len(words), err)
	}
	_, wordsJSON, err := checkUpgradeChunkSet(tags, words[0])
	if err != nil {
		t.Fatalf("decode fixture words: %v", err)
	}
	var wordsBody portable.TranscriptBody
	if err := json.Unmarshal(wordsJSON, &wordsBody); err != nil {
		t.Fatalf("parse fixture words: %v", err)
	}
	fixture := draftFixture{bodies: map[string][]byte{"raw-asr": wordsJSON}, words: wordsBody.Items}

	// Provenance the draft writers recorded, keyed the multi-transcript way.
	document["provenance"] = map[string]any{
		"speechToText": map[string]any{"raw-asr": map[string]any{"backend": "sherpa-onnx", "model": "parakeet", "device": "cpu"}},
	}
	// A summary, as the packer attaches one.
	document["summary"] = map[string]any{"format": "markdown", "templateVersion": "v0"}
	document["attachments"] = []any{map[string]any{
		"name": "summary.md", "mime": "text/markdown",
		"contentBase64": base64.StdEncoding.EncodeToString([]byte("# Summary\n\nWe said hello.\n")),
	}}

	setPCM := func() {
		digest := opts.pcmDigest
		if digest == "" {
			digest, _, err = hashUpgradeDecodedPCM(ctx, v1Path, audio.SampleRate, audio.Channels)
			if err != nil {
				t.Fatalf("hash fixture PCM: %v", err)
			}
		}
		sampleCount := audio.SampleCount
		if opts.sampleCount != 0 {
			sampleCount = opts.sampleCount
		}
		document["integrity"] = map[string]any{
			"matchPolicy": "exact-pcm",
			"pcmFormat":   "s16le",
			"pcmSha256":   digest,
			"sampleRate":  json.Number(strconv.Itoa(audio.SampleRate)),
			"channels":    json.Number(strconv.Itoa(audio.Channels)),
			"sampleCount": json.Number(strconv.FormatInt(sampleCount, 10)),
			"durationMs":  json.Number(strconv.FormatInt(audio.DurationMS, 10)),
		}
		meeting := document["meeting"].(map[string]any)
		meeting["id"] = "mtg_" + digest
		deleteTagAnyCase(tags, "CASSINI_AUDIO_OPUS_SHA256")
		tags["CASSINI_AUDIO_MATCH_POLICY"] = "exact-pcm"
		tags["CASSINI_AUDIO_PCM_FORMAT"] = "s16le"
		tags["CASSINI_AUDIO_PCM_SHA256"] = digest
		tags["CASSINI_MEETING_ID"] = "mtg_" + digest
	}
	addChunkSet := func(id, role string, body any) map[string]any {
		mimeRole := ""
		if role == portable.RoleDisplay || role == "readable-cleanup" {
			mimeRole = portable.RoleDisplay
		}
		payload, ref, err := portable.EncodeTranscriptBody(body, id, mimeRole, portable.DefaultPayloadChunkSize)
		if err != nil {
			t.Fatalf("encode %s: %v", id, err)
		}
		writeUpgradeChunkSetTags(tags, ref, payload)
		fixture.bodies[id] = payload.JSON
		return map[string]any{"id": id, "role": role, "format": "x", "payloadRef": upgradePayloadRefDocument(ref)}
	}
	// Drafts 2 and 3 carried a role on every entry, and could hold more than
	// one words transcript and readable documents beside them.
	multiTranscriptExtras := func() {
		words[0]["role"] = "raw-asr"
		alt := portable.TranscriptBody{Format: "cassini.words.v1", Language: "en", WordCount: 1,
			Items: []portable.TranscriptItem{{Speaker: "spk_host", StartMS: 0, EndMS: 90, Text: "hullo"}}}
		corrected := addChunkSet("canary", "human-corrected", alt)
		corrected["format"] = "cassini.words.v1"
		corrected["wordCount"] = json.Number("1")
		display := addChunkSet("display", portable.RoleDisplay, map[string]any{
			"version": "transcript.display.v1",
			"blocks":  []any{map[string]any{"speaker": "spk_host", "startMs": json.Number("0"), "text": "Hello team."}},
		})
		display["format"] = "transcript.display.v1"
		display["sourceTranscriptId"] = "raw-asr"
		cleanup := addChunkSet("cleanup", "readable-cleanup", map[string]any{"version": "transcript.readable.v1", "segments": []any{}})
		cleanup["format"] = "transcript.readable.v1"
		cleanup["sourceTranscriptId"] = "raw-asr"
		document["transcripts"] = []any{words[0], corrected}
		document["readableTranscripts"] = []any{display, cleanup}
		document["chapters"] = []any{map[string]any{"title": "Opening", "startMs": json.Number("0"), "endMs": json.Number("200")}}
		provenance := document["provenance"].(map[string]any)
		provenance["readableCleanup"] = map[string]any{"cleanup": map[string]any{"backend": "llm"}}
		provenance["displayTranscript"] = map[string]any{"display": map[string]any{"backend": "rules"}}
	}

	switch shape {
	case upgradeShapePublishedOldSchema:
		words[0]["role"] = "raw-asr"
		tags["CASSINI_PAYLOAD_SCHEMA"] = upgradeSchemaPublishedLegacy
	case upgradeShapeDraft3:
		multiTranscriptExtras()
		document["version"] = json.Number("3")
		document["annotations"] = json.RawMessage(upgradeFixtureAnnotations(t, audio.OpusSHA256))
		if opts.opusDigest != "" {
			document["integrity"].(map[string]any)["opusAudioSha256"] = opts.opusDigest
			tags["CASSINI_AUDIO_OPUS_SHA256"] = opts.opusDigest
		}
		tags["CASSINI_FORMAT"] = upgradeFormatDraft3
		tags["CASSINI_PAYLOAD_SCHEMA"] = upgradeSchemaDraft3
	case upgradeShapeDraft2:
		multiTranscriptExtras()
		document["version"] = json.Number("2")
		setPCM()
		document["meeting"].(map[string]any)["roomName"] = "Weekly Sync"
		tags["CASSINI_ROOM_NAME"] = "Weekly Sync"
		tags["CASSINI_FORMAT"] = upgradeFormatDraft2
		tags["CASSINI_PAYLOAD_SCHEMA"] = upgradeSchemaDraft2
	case upgradeShapeDraft1:
		setPCM()
		document["integrity"].(map[string]any)["containerSha256"] = strings.Repeat("c", 64)
		var inline map[string]any
		if err := json.Unmarshal(wordsJSON, &inline); err != nil {
			t.Fatalf("parse words: %v", err)
		}
		document["transcript"] = inline
		readable := map[string]any{"version": "transcript.readable.v1", "language": "en", "segments": []any{map[string]any{"text": "Hello team."}}}
		document["readableTranscript"] = readable
		readableJSON, _ := json.Marshal(readable)
		fixture.bodies[upgradeDraft1ReadableID] = readableJSON
		delete(document, "transcripts")
		delete(document, "readableTranscripts")
		document["chapters"] = []any{map[string]any{"title": "Opening", "startMs": json.Number("0"), "endMs": json.Number("200")}}
		meeting := document["meeting"].(map[string]any)
		meeting["language"] = "en"
		provenance := document["provenance"].(map[string]any)
		stt := provenance["speechToText"].(map[string]any)
		provenance["speechToText"] = stt["raw-asr"]
		provenance["readableCleanup"] = map[string]any{"backend": "llm", "model": "small"}
		for key := range tags {
			upper := strings.ToUpper(key)
			if strings.HasPrefix(upper, "CASSINI_TX_") || upper == "CASSINI_TRANSCRIPT_IDS" || upper == "CASSINI_TRANSCRIPT_DEFAULT" {
				delete(tags, key)
			}
		}
		tags["CASSINI_DECODE_HINT"] = "Concatenate CASSINI_PAYLOAD_000..N, base64url decode, gzip decompress, parse UTF-8 JSON."
		tags["CASSINI_WORD_COUNT"] = strconv.Itoa(wordsBody.WordCount)
		tags["CASSINI_TRANSCRIPT_LANGUAGE"] = "en"
		tags["CASSINI_FORMAT"] = portable.Format
		tags["CASSINI_PAYLOAD_SCHEMA"] = upgradeSchemaDraft1
	default:
		t.Fatalf("unknown draft shape %q", shape)
	}
	if shape != upgradeShapeDraft1 {
		// Re-key the IDS mirror to the transcripts the draft now indexes.
		ids := []string{}
		for id := range fixture.bodies {
			ids = append(ids, id)
		}
		sortStrings(ids)
		tags["CASSINI_TRANSCRIPT_IDS"] = strings.Join(ids, ",")
	}
	if opts.mutate != nil {
		opts.mutate(document, tags)
	}

	draftJSON, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encode draft manifest: %v", err)
	}
	encoded, err := portable.EncodePayloadBytes(draftJSON, portable.DefaultPayloadChunkSize)
	if err != nil {
		t.Fatalf("encode draft payload: %v", err)
	}
	for key := range tags {
		if isPayloadChunkTag(key) {
			delete(tags, key)
		}
	}
	tags["CASSINI_PAYLOAD_CHUNK_COUNT"] = strconv.Itoa(len(encoded.Chunks))
	tags["CASSINI_PAYLOAD_SHA256"] = encoded.SHA256
	tags["CASSINI_PAYLOAD_RAW_BYTES"] = strconv.Itoa(encoded.RawBytes)
	tags["CASSINI_PAYLOAD_GZIP_BYTES"] = strconv.Itoa(encoded.CompressedBytes)
	for idx, chunk := range encoded.Chunks {
		tags["CASSINI_PAYLOAD_"+leftPad3(idx)] = chunk
	}
	fixture.path = filepath.Join(dir, shape+".opus")
	if err := writePortableMeetingFile(ctx, v1Path, fixture.path, tags); err != nil {
		t.Fatalf("write draft fixture: %v", err)
	}
	fixture.document, err = decodePortableMeetingDocument(draftJSON)
	if err != nil {
		t.Fatalf("reparse draft: %v", err)
	}
	return fixture
}

func leftPad3(idx int) string {
	value := strconv.Itoa(idx)
	for len(value) < 3 {
		value = "0" + value
	}
	return value
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func upgradeFixtureAnnotations(t *testing.T, audioSHA string) []byte {
	t.Helper()
	doc := portable.Annotations{
		Format:          portable.AnnotationsFormatV1,
		Revision:        1,
		AudioOpusSHA256: audioSHA,
		TagNamespace:    "urn:uuid:07e4eab6-4f5f-4cc4-8913-46432c5cd726",
		Tags:            []portable.AnnotationTag{{ID: "tag_a", Label: "budget"}},
		Items: []portable.AnnotationItem{{ID: "mk_1", TagID: "tag_a",
			Target:       portable.AnnotationTarget{Kind: portable.AnnotationTargetMeeting},
			CreatedAtUTC: "2026-09-10T11:23:54Z", OperationID: "op_1",
			Actor: portable.AnnotationActor{Kind: portable.AnnotationActorPerson, ID: "alice"}}},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal annotations: %v", err)
	}
	return raw
}

func runUpgradeFormatCLI(t *testing.T, args ...string) (int, UpgradeFormatReport, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), append([]string{"upgrade-format"}, append(args, "--json")...), &stdout, &stderr)
	var report UpgradeFormatReport
	if stdout.Len() > 0 {
		if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
			t.Fatalf("parse report: %v\n%s", err, stdout.String())
		}
	}
	return code, report, stderr.String()
}

// readAllTranscriptBodies decodes every body a published file indexes.
func readAllTranscriptBodies(t *testing.T, path string) map[string][]byte {
	t.Helper()
	tags, err := portableMeetingTags(path)
	if err != nil {
		t.Fatalf("read tags: %v", err)
	}
	rawJSON, err := decodePortableMeetingPayload(tags)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	document, err := decodePortableMeetingDocument(rawJSON)
	if err != nil {
		t.Fatalf("parse payload: %v", err)
	}
	out := map[string][]byte{}
	for _, key := range []string{"transcripts", "readableTranscripts"} {
		entries, err := upgradeEntryList(document, key)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		for _, entry := range entries {
			transcript, raw, err := checkUpgradeChunkSet(tags, entry)
			if err != nil {
				t.Fatalf("decode %v: %v", entry["id"], err)
			}
			out[transcript.ID] = raw
		}
	}
	return out
}

func sameJSON(t *testing.T, left, right []byte) bool {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(left, &a); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := json.Unmarshal(right, &b); err != nil {
		t.Fatalf("parse: %v", err)
	}
	return reflect.DeepEqual(a, b)
}

func TestUpgradeFormatRoundTripsEveryDraftShape(t *testing.T) {
	requireFFMediaTools(t)
	for _, shape := range []string{upgradeShapeDraft1, upgradeShapeDraft2, upgradeShapeDraft3, upgradeShapePublishedOldSchema} {
		t.Run(shape, func(t *testing.T) {
			tmp := t.TempDir()
			draft := writeDraftFixture(t, tmp, shape, draftFixtureOptions{})
			draftBytes, err := os.ReadFile(draft.path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := inspect.ExtractMeeting(draft.path); err == nil {
				t.Fatalf("the current reader should refuse the %s fixture, or the fixture is not a draft", shape)
			}
			outPath := filepath.Join(tmp, "upgraded.opus")

			code, report, stderr := runUpgradeFormatCLI(t, draft.path, "--out", outPath)
			if code != 0 {
				t.Fatalf("upgrade failed code=%d report=%+v stderr=%s", code, report, stderr)
			}
			if report.Status != upgradeStatusUpgraded || report.Shape != shape {
				t.Fatalf("report status=%q shape=%q, want upgraded %s", report.Status, report.Shape, shape)
			}
			if after, _ := os.ReadFile(draft.path); !bytes.Equal(after, draftBytes) {
				t.Fatal("the input file changed")
			}

			// Audio: the same compressed Opus stream, packet for packet.
			sourceAudio, err := computePortableAudioIntegrity(draft.path)
			if err != nil {
				t.Fatal(err)
			}
			outAudio, err := computePortableAudioIntegrity(outPath)
			if err != nil {
				t.Fatal(err)
			}
			if outAudio != sourceAudio {
				t.Fatalf("audio changed: before %+v after %+v", sourceAudio, outAudio)
			}

			// The current reader opens it, and inspect finds nothing to flag.
			var inspected bytes.Buffer
			if err := inspect.InspectPath(&inspected, outPath); err != nil {
				t.Fatalf("inspect: %v\n%s", err, inspected.String())
			}
			if !strings.Contains(inspected.String(), " cassini=ok\n") || strings.Contains(inspected.String(), "warning=") {
				t.Fatalf("inspect is not clean:\n%s", inspected.String())
			}
			extracted, err := inspect.ExtractMeeting(outPath)
			if err != nil {
				t.Fatalf("extract: %v", err)
			}
			if extracted.FormatTag != portable.Format {
				t.Fatalf("format tag %q", extracted.FormatTag)
			}
			if len(extracted.Transcript.Words) != len(draft.words) {
				t.Fatalf("default transcript has %d words, draft had %d", len(extracted.Transcript.Words), len(draft.words))
			}
			for i, word := range extracted.Transcript.Words {
				want := draft.words[i]
				if word.Text != want.Text || word.StartMS != want.StartMS || word.EndMS != want.EndMS || word.Speaker != want.Speaker {
					t.Fatalf("word %d = %+v, draft had %+v", i, word, want)
				}
			}
			if got := string(extracted.SummaryMarkdown); got != "# Summary\n\nWe said hello.\n" {
				t.Fatalf("summary = %q", got)
			}

			// Every body the draft carried, byte for byte.
			bodies := readAllTranscriptBodies(t, outPath)
			if len(bodies) != len(draft.bodies) {
				t.Fatalf("output carries %d transcript bodies, draft had %d", len(bodies), len(draft.bodies))
			}
			for id, want := range draft.bodies {
				got, ok := bodies[id]
				if !ok {
					t.Fatalf("transcript %q missing from the output", id)
				}
				if !bytes.Equal(got, want) && !sameJSON(t, got, want) {
					t.Fatalf("transcript %q body changed", id)
				}
			}

			// Everything else in the manifest, member by member.
			written := decodeUpgradeOutputDocument(t, outPath)
			record, ok := written[upgradeRecordKey].(map[string]any)
			if !ok || record["format"] != portableTagValueOf(t, draft.document, shape) {
				t.Fatalf("x-upgradedFrom = %#v", written[upgradeRecordKey])
			}
			for _, key := range []string{"meeting", "speakers", "summary", "attachments", "audio", "chapters", "processing", "annotations"} {
				if !reflect.DeepEqual(written[key], draft.document[key]) {
					t.Fatalf("%s changed:\n draft   %#v\n written %#v", key, draft.document[key], written[key])
				}
			}
			manifest := extracted.Manifest
			if manifest.Meeting.RoomID == "" || manifest.Meeting.JobID == "" || manifest.Meeting.AttemptNumber != 2 {
				t.Fatalf("meeting origin lost: %+v", manifest.Meeting)
			}
			if manifest.Provenance == nil || manifest.Provenance.SpeechToText == nil {
				t.Fatal("speech-to-text provenance lost")
			}
			if manifest.Integrity.OpusSHA256 != sourceAudio.OpusSHA256 {
				t.Fatalf("integrity digest %s, audio is %s", manifest.Integrity.OpusSHA256, sourceAudio.OpusSHA256)
			}
			if draftIntegrity := draft.document["integrity"].(map[string]any); draftIntegrity["matchPolicy"] == "exact-pcm" {
				if !reflect.DeepEqual(record["integrity"], draftIntegrity) {
					t.Fatalf("the exact-pcm record was not kept: %#v", record["integrity"])
				}
			}
			for _, tag := range []string{"CASSINI_AUDIO_PCM_SHA256", "CASSINI_AUDIO_PCM_FORMAT"} {
				if got := readOpusTag(t, outPath, tag); got != "" {
					t.Fatalf("%s survived: %q", tag, got)
				}
			}
			if got := readOpusTag(t, outPath, "CASSINI_PAYLOAD_SCHEMA"); got != portable.PayloadSchema {
				t.Fatalf("schema tag %q", got)
			}
			if got := readOpusTag(t, outPath, "CASSINI_ROOM_ID"); got != manifest.Meeting.RoomID {
				t.Fatalf("room tag %q, manifest %q", got, manifest.Meeting.RoomID)
			}

			// An upgraded file is current.
			code, again, _ := runUpgradeFormatCLI(t, outPath, "--check")
			if code != 0 || again.Status != upgradeStatusCurrent {
				t.Fatalf("re-check: code=%d status=%q refusal=%q", code, again.Status, again.Refusal)
			}
		})
	}
}

func decodeUpgradeOutputDocument(t *testing.T, path string) map[string]any {
	t.Helper()
	tags, err := portableMeetingTags(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := decodePortableMeetingPayload(tags)
	if err != nil {
		t.Fatal(err)
	}
	document, err := decodePortableMeetingDocument(raw)
	if err != nil {
		t.Fatal(err)
	}
	return document
}

// portableTagValueOf returns the CASSINI_FORMAT the draft fixture was given.
func portableTagValueOf(t *testing.T, _ map[string]any, shape string) string {
	t.Helper()
	switch shape {
	case upgradeShapeDraft2:
		return upgradeFormatDraft2
	case upgradeShapeDraft3:
		return upgradeFormatDraft3
	}
	return portable.Format
}

func TestUpgradeFormatMovesDraft1ProvenanceToTranscriptIDs(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	draft := writeDraftFixture(t, tmp, upgradeShapeDraft1, draftFixtureOptions{})
	outPath := filepath.Join(tmp, "upgraded.opus")
	if code, report, stderr := runUpgradeFormatCLI(t, draft.path, "--out", outPath); code != 0 {
		t.Fatalf("upgrade failed: %+v %s", report, stderr)
	}
	written := decodeUpgradeOutputDocument(t, outPath)
	provenance := written["provenance"].(map[string]any)
	stt, ok := provenance["speechToText"].(map[string]any)["raw-asr"].(map[string]any)
	if !ok || !reflect.DeepEqual(stt, draft.document["provenance"].(map[string]any)["speechToText"]) {
		t.Fatalf("speechToText = %#v", provenance["speechToText"])
	}
	if _, ok := provenance["readableCleanup"].(map[string]any)[upgradeDraft1ReadableID]; !ok {
		t.Fatalf("readableCleanup = %#v", provenance["readableCleanup"])
	}
	entries := written["readableTranscripts"].([]any)
	readable := entries[0].(map[string]any)
	if readable["role"] != "readable-cleanup" || readable["sourceTranscriptId"] != "raw-asr" {
		t.Fatalf("readable entry = %#v", readable)
	}
	for _, key := range []string{"transcript", "readableTranscript", "displayTranscript"} {
		if _, present := written[key]; present {
			t.Fatalf("%s is still inline", key)
		}
	}
	if got := readOpusTag(t, outPath, "CASSINI_TRANSCRIPT_DEFAULT"); got != "raw-asr" {
		t.Fatalf("CASSINI_TRANSCRIPT_DEFAULT = %q", got)
	}
	record := written[upgradeRecordKey].(map[string]any)
	if record["integrity"].(map[string]any)["containerSha256"] != strings.Repeat("c", 64) {
		t.Fatalf("containerSha256 not kept: %#v", record["integrity"])
	}
}

func TestUpgradeFormatLeavesAPublishedFileAlone(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	inPath := packFixtureOpus(t, tmp, "current")
	before, err := os.ReadFile(inPath)
	if err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(tmp, "out.opus")
	code, report, stderr := runUpgradeFormatCLI(t, inPath, "--out", outPath)
	if code != 0 || report.Status != upgradeStatusCurrent {
		t.Fatalf("code=%d status=%q stderr=%s", code, report.Status, stderr)
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Fatalf("a current file must not produce an output, stat err=%v", err)
	}
	if after, _ := os.ReadFile(inPath); !bytes.Equal(after, before) {
		t.Fatal("the input changed")
	}
}

func TestUpgradeFormatCheckWritesNothing(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	draft := writeDraftFixture(t, tmp, upgradeShapeDraft2, draftFixtureOptions{})
	entriesBefore, _ := os.ReadDir(tmp)
	code, report, stderr := runUpgradeFormatCLI(t, draft.path, "--check")
	if code != 0 || report.Status != upgradeStatusUpgradable {
		t.Fatalf("code=%d report=%+v stderr=%s", code, report, stderr)
	}
	if report.AudioVerification != upgradeAudioPCMDigest {
		t.Fatalf("audio check %q", report.AudioVerification)
	}
	if len(report.Changes) == 0 || len(report.Relocated) == 0 {
		t.Fatalf("check should list changes and relocations: %+v", report)
	}
	entriesAfter, _ := os.ReadDir(tmp)
	if len(entriesAfter) != len(entriesBefore) {
		t.Fatalf("--check wrote files: %d -> %d entries", len(entriesBefore), len(entriesAfter))
	}
}

func TestUpgradeFormatPCMDigestTheDecoderCannotReproduce(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	draft := writeDraftFixture(t, tmp, upgradeShapeDraft2, draftFixtureOptions{pcmDigest: strings.Repeat("a", 64)})
	outPath := filepath.Join(tmp, "out.opus")

	code, report, _ := runUpgradeFormatCLI(t, draft.path, "--out", outPath)
	if code != 1 || report.Status != upgradeStatusRefused || !strings.Contains(report.Refusal, "--accept-pcm-shape-match") {
		t.Fatalf("without the flag: code=%d report=%+v", code, report)
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Fatal("a refusal wrote an output")
	}

	code, report, stderr := runUpgradeFormatCLI(t, draft.path, "--out", outPath, "--accept-pcm-shape-match")
	if code != 0 || report.AudioVerification != upgradeAudioPCMShape {
		t.Fatalf("with the flag: code=%d report=%+v stderr=%s", code, report, stderr)
	}
	record := decodeUpgradeOutputDocument(t, outPath)[upgradeRecordKey].(map[string]any)
	if record["audioVerification"] != upgradeAudioPCMShape {
		t.Fatalf("the file does not record how weak its audio check was: %#v", record)
	}
}

func TestUpgradeFormatRefusals(t *testing.T) {
	requireFFMediaTools(t)
	cases := []struct {
		name  string
		shape string
		opts  draftFixtureOptions
		flags []string
		want  string
	}{
		{
			name: "pcm sample count disagrees", shape: upgradeShapeDraft2,
			opts:  draftFixtureOptions{pcmDigest: strings.Repeat("a", 64), sampleCount: 7},
			flags: []string{"--accept-pcm-shape-match"}, want: "sample count",
		},
		{
			name: "draft 3 metadata for other audio", shape: upgradeShapeDraft3,
			opts: draftFixtureOptions{opusDigest: strings.Repeat("b", 64)}, want: "does not belong to this audio",
		},
		{
			name: "unknown format and schema pair", shape: upgradeShapeDraft3,
			opts: draftFixtureOptions{mutate: func(_ map[string]any, tags map[string]string) {
				tags["CASSINI_PAYLOAD_SCHEMA"] = upgradeSchemaDraft2
			}},
			want: "unrecognised CASSINI_FORMAT",
		},
		{
			name: "manifest version disagrees with the format tag", shape: upgradeShapeDraft3,
			opts: draftFixtureOptions{mutate: func(document map[string]any, _ map[string]string) {
				document["version"] = json.Number("2")
			}},
			want: "manifest version",
		},
		{
			name: "inline transcript under a multi-transcript tag", shape: upgradeShapeDraft2,
			opts: draftFixtureOptions{mutate: func(document map[string]any, _ map[string]string) {
				document["transcript"] = map[string]any{"format": "cassini.words.v1", "wordCount": json.Number("0"), "items": []any{}}
			}},
			want: "no inline transcript",
		},
		{
			name: "two default word transcripts", shape: upgradeShapeDraft3,
			opts: draftFixtureOptions{mutate: func(document map[string]any, _ map[string]string) {
				for _, entry := range document["transcripts"].([]any) {
					entry.(map[string]any)["default"] = true
				}
			}},
			want: "flagged default",
		},
		{
			name: "transcript id the published format rejects", shape: upgradeShapeDraft3,
			opts: draftFixtureOptions{mutate: func(document map[string]any, _ map[string]string) {
				document["transcripts"].([]any)[1].(map[string]any)["id"] = "human_corrected"
			}},
			want: "not valid in published v1",
		},
		{
			name: "body that does not match its payloadRef", shape: upgradeShapeDraft3,
			opts: draftFixtureOptions{mutate: func(document map[string]any, _ map[string]string) {
				ref := document["transcripts"].([]any)[1].(map[string]any)["payloadRef"].(map[string]any)
				ref["sha256"] = strings.Repeat("d", 64)
			}},
			want: "does not match its payloadRef",
		},
		{
			name: "words body the published reader rejects", shape: upgradeShapeDraft1,
			opts: draftFixtureOptions{mutate: func(document map[string]any, _ map[string]string) {
				inline := document["transcript"].(map[string]any)
				inline["items"].([]any)[0].(map[string]any)["text"] = "two words"
			}},
			want: "exactly one word",
		},
		{
			name: "already carries an upgrade record", shape: upgradeShapeDraft3,
			opts: draftFixtureOptions{mutate: func(document map[string]any, _ map[string]string) {
				document[upgradeRecordKey] = map[string]any{}
			}},
			want: "upgrade record",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			draft := writeDraftFixture(t, tmp, tc.shape, tc.opts)
			outPath := filepath.Join(tmp, "out.opus")
			code, report, _ := runUpgradeFormatCLI(t, append([]string{draft.path, "--out", outPath}, tc.flags...)...)
			if code != 1 || report.Status != upgradeStatusRefused {
				t.Fatalf("code=%d status=%q, want a refusal", code, report.Status)
			}
			if !strings.Contains(report.Refusal, tc.want) {
				t.Fatalf("refusal %q does not mention %q", report.Refusal, tc.want)
			}
			if _, err := os.Stat(outPath); !os.IsNotExist(err) {
				t.Fatal("a refusal wrote an output")
			}
		})
	}
}

func TestUpgradeFormatRefusesPlainAudio(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	bundle := filepath.Join(tmp, "plain.meeting")
	if err := writeReadyMeetingBundleFixture(bundle, "/tmp/source.mkv"); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(tmp, "plain.opus")
	if err := writePortableMeetingFile(context.Background(), filepath.Join(bundle, "meeting.webm"), plain, map[string]string{"TITLE": "x"}); err != nil {
		t.Fatal(err)
	}
	code, report, _ := runUpgradeFormatCLI(t, plain, "--check")
	if code != 1 || !strings.Contains(report.Refusal, "CASSINI_FORMAT") {
		t.Fatalf("code=%d report=%+v", code, report)
	}
}

func TestUpgradeFormatUsageErrors(t *testing.T) {
	tmp := t.TempDir()
	in := filepath.Join(tmp, "in.opus")
	if err := os.WriteFile(in, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][]string{
		"same path":        {in, "--out", in},
		"no out":           {in},
		"check with out":   {in, "--check", "--out", filepath.Join(tmp, "o.opus")},
		"not an opus file": {filepath.Join(tmp, "in.json"), "--check"},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(context.Background(), append([]string{"upgrade-format"}, args...), &stdout, &stderr); code != 2 {
				t.Fatalf("code=%d stderr=%s", code, stderr.String())
			}
		})
	}
}

func TestUpgradeFormatAppearsInRootUsage(t *testing.T) {
	var stdout bytes.Buffer
	Run(context.Background(), []string{"help"}, &stdout, &stdout)
	if !strings.Contains(stdout.String(), "upgrade-format") {
		t.Fatal("root usage does not list upgrade-format")
	}
}
