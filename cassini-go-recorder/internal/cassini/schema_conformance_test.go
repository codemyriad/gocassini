package cassini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gocassini/internal/portable"
)

// TestPackedMeetingKeysAreDeclaredBySchema is the guard against the one drift
// this format cannot absorb: adding a field to portable.Meeting and forgetting
// the schema.
//
// The public schema accepts extension keys, but the reference writer must
// still document the meeting fields it produces. This catches writer/schema
// drift without closing the format to another producer's extensions.
//
// It reads the real schema file and a really-packed file, rather than comparing
// the struct against a list: a list would be a third thing to keep in step.
func TestPackedMeetingKeysAreDeclaredBySchema(t *testing.T) {
	requireFFMediaTools(t)

	tmp := t.TempDir()
	bundleDir := filepath.Join(tmp, "conformance.meeting")
	if err := writeReadyMeetingBundleFixture(bundleDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	outPath := filepath.Join(tmp, "conformance.opus")
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"pack", bundleDir, "--out", outPath,
		// Everything optional, so the packed file exercises the widest key set
		// a producer can emit.
		"--title", "Weekly Sync", "--room-token", "a7bc3k9x", "--room-name", "Weekly Sync",
		"--job-id", "01K3Q7W8ZC9F0MJXQ2NB8V4RTD", "--attempt-number", "2",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("pack failed code=%d stderr=%q", code, stderr.String())
	}

	tags, err := portableMeetingTags(outPath)
	if err != nil {
		t.Fatalf("read tags: %v", err)
	}
	rawJSON, err := decodePortableMeetingPayload(tags)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var document struct {
		Meeting map[string]json.RawMessage `json:"meeting"`
	}
	if err := json.Unmarshal(rawJSON, &document); err != nil {
		t.Fatalf("parse payload: %v", err)
	}
	if len(document.Meeting) == 0 {
		t.Fatal("the packed manifest has no meeting object")
	}

	schemaPath := manifestSchemaPath
	doc := readManifestSchema(t)
	meeting := nested(doc, "$defs", "meeting")
	if meeting == nil {
		t.Fatalf("%s has no meeting object", schemaPath)
	}
	declared, _ := meeting["properties"].(map[string]any)
	var undeclared []string
	for key := range document.Meeting {
		if _, ok := declared[key]; !ok {
			undeclared = append(undeclared, key)
		}
	}
	sort.Strings(undeclared)
	if len(undeclared) > 0 {
		t.Errorf("%s does not declare meeting field(s) a packed file emits: %v", schemaPath, undeclared)
	}
}

// TestAnnotationsSchemaMatchesTheWriter is the same guard for the annotations
// member (D-737), which has more to drift: its format string, its limits and its
// target kinds are constants in internal/portable as well as values in the
// schema. It checks a document the real writer encoded rather than a
// hand-written one, and reads every limit back out of the schema file rather
// than restating it here.
//
// It is not a JSON Schema validator — the module carries none, and adding one
// for a test is not worth a dependency. It checks what actually drifts: every
// member the writer emits is declared, every member the schema requires is
// emitted, the declared patterns accept the writer's values, and the schema's
// constants are the Go ones.
func TestAnnotationsSchemaMatchesTheWriter(t *testing.T) {
	schema := readManifestSchema(t)
	document := encodeAnnotatedManifestFixture(t)

	topLevel, _ := schema["properties"].(map[string]any)
	if _, ok := topLevel["annotations"]; !ok {
		t.Fatal("the schema does not declare the top-level annotations member")
	}
	// The member is held to the v1 shape only when it claims v1: a reader
	// ignores a format it does not know, so the schema must not reject one.
	if got := nested(schema, "$defs", "annotations", "if", "properties", "format")["const"]; got != portable.AnnotationsFormatV1 {
		t.Errorf("annotations applies the v1 shape when format is %v, want %q", got, portable.AnnotationsFormatV1)
	}
	v1 := nested(schema, "$defs", "annotationsV1")
	if v1 == nil {
		t.Fatal("the schema has no annotationsV1 definition")
	}
	if got := nested(v1, "properties", "format")["const"]; got != portable.AnnotationsFormatV1 {
		t.Errorf("annotationsV1.format const = %v, want %q", got, portable.AnnotationsFormatV1)
	}
	for _, limit := range []struct {
		name string
		got  any
		want int
	}{
		{"annotationsV1.tags.maxItems", nested(v1, "properties", "tags")["maxItems"], portable.MaxAnnotationTags},
		{"annotationsV1.items.maxItems", nested(v1, "properties", "items")["maxItems"], portable.MaxAnnotationItems},
		{"annotationTag.label.maxLength", nested(schema, "$defs", "annotationTag", "properties", "label")["maxLength"], portable.MaxAnnotationLabelRunes},
	} {
		if limit.got != float64(limit.want) {
			t.Errorf("%s = %v, but internal/portable enforces %d", limit.name, limit.got, limit.want)
		}
	}

	annotations, ok := document["annotations"].(map[string]any)
	if !ok {
		t.Fatalf("the encoded manifest carries no annotations object: %v", document["annotations"])
	}
	checkAgainstSchemaObject(t, schema, "annotations", annotations, v1)
	for i, tag := range annotations["tags"].([]any) {
		checkAgainstSchemaObject(t, schema, fmt.Sprintf("annotations.tags[%d]", i),
			tag.(map[string]any), nested(schema, "$defs", "annotationTag"))
	}

	// The target is a oneOf keyed by kind. Each branch must name exactly one of
	// the kinds the Go validator accepts, and the fixture must exercise every
	// branch, or this test would prove half of the claim.
	branches := map[string]map[string]any{}
	oneOf, _ := nested(schema, "$defs", "annotationTarget")["oneOf"].([]any)
	for _, raw := range oneOf {
		branch, _ := raw.(map[string]any)
		kind, _ := nested(branch, "properties", "kind")["const"].(string)
		branches[kind] = branch
	}
	for _, want := range []string{portable.AnnotationTargetMeeting, portable.AnnotationTargetTimeRange} {
		if branches[want] == nil {
			t.Errorf("annotationTarget has no oneOf branch for kind %q", want)
		}
	}
	if len(branches) != 2 {
		t.Errorf("annotationTarget declares kinds %v; internal/portable accepts exactly meeting and time-range", sortedKeys(branches))
	}
	seen := map[string]bool{}
	for i, raw := range annotations["items"].([]any) {
		at := fmt.Sprintf("annotations.items[%d]", i)
		item := raw.(map[string]any)
		checkAgainstSchemaObject(t, schema, at, item, nested(schema, "$defs", "annotationItem"))
		checkAgainstSchemaObject(t, schema, at+".actor", item["actor"].(map[string]any), nested(schema, "$defs", "annotationActor"))
		target := item["target"].(map[string]any)
		kind, _ := target["kind"].(string)
		branch := branches[kind]
		if branch == nil {
			t.Errorf("%s.target.kind %q matches no oneOf branch", at, kind)
			continue
		}
		seen[kind] = true
		checkAgainstSchemaObject(t, schema, at+".target", target, branch)
	}
	for kind := range branches {
		if !seen[kind] {
			t.Errorf("the fixture exercises no %q target, so its branch is unchecked", kind)
		}
	}
}

// encodeAnnotatedManifestFixture returns the main payload, as JSON, of a
// manifest carrying a v1 annotations document with one mark of each target
// kind — encoded by the real writer, so what is checked is what ships.
func encodeAnnotatedManifestFixture(t *testing.T) map[string]any {
	t.Helper()
	const durationMS = 3_600_000
	audioSHA := strings.Repeat("a", 64)
	start, end := int64(869_000), int64(884_000)
	doc := &portable.Annotations{
		Format:          portable.AnnotationsFormatV1,
		Revision:        4,
		AudioOpusSHA256: audioSHA,
		TagNamespace:    "urn:uuid:07e4eab6-4f5f-4cc4-8913-46432c5cd726",
		Tags:            []portable.AnnotationTag{{ID: "tag_k3v9q2m7x4d8w1pz", Label: "hiring"}},
		Items: []portable.AnnotationItem{
			{
				ID: "mk_01J9ZB6Q4H7T2N8K3M5P0R1S2V", TagID: "tag_k3v9q2m7x4d8w1pz",
				Target:       portable.AnnotationTarget{Kind: portable.AnnotationTargetMeeting},
				CreatedAtUTC: "2026-09-10T11:23:54Z",
				Actor:        portable.AnnotationActor{Kind: portable.AnnotationActorPerson, ID: "alice"},
				OperationID:  "op_01J9ZB6Q4H7T2N8K3M5P0R1S2W",
			},
			{
				ID: "mk_01J9ZB6Q4H7T2N8K3M5P0R1S2X", TagID: "tag_k3v9q2m7x4d8w1pz",
				Target:       portable.AnnotationTarget{Kind: portable.AnnotationTargetTimeRange, StartMS: &start, EndMS: &end},
				CreatedAtUTC: "2026-09-10T11:24:10Z",
				Actor:        portable.AnnotationActor{Kind: portable.AnnotationActorAgent, ID: "alice"},
				OperationID:  "op_01J9ZB6Q4H7T2N8K3M5P0R1S2Y",
			},
		},
	}
	doc.Canonicalize()
	if err := portable.ValidateAnnotations(doc, durationMS); err != nil {
		t.Fatalf("the fixture is not a valid v1 document, so checking it proves nothing: %v", err)
	}
	member, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal annotations: %v", err)
	}
	manifest := portable.NormalizePublishedManifest(portable.Manifest{
		Meeting: portable.Meeting{
			ID: "mtg_" + strings.Repeat("c", 64), Title: "Weekly Sync",
			CreatedAtUTC: "2026-09-10T11:00:00Z", DurationMS: durationMS,
		},
		Audio: portable.Audio{
			Container: "ogg", Codec: "opus", SampleRate: 48000, Channels: 1,
			SampleCount: durationMS * 48, DurationMS: durationMS,
		},
		Integrity: portable.Integrity{
			MatchPolicy: portable.AudioMatchPolicy, OpusSHA256: audioSHA,
			SampleRate: 48000, Channels: 1, SampleCount: durationMS * 48, DurationMS: durationMS,
		},
		Speakers: []portable.Speaker{{ID: "spk1", Label: "Silvio"}},
	})
	manifest.Annotations = member
	encoded, err := portable.EncodePublishedManifest(manifest, []portable.TranscriptInput{{
		ID: portable.DefaultWordsTranscriptID, Default: true,
		Body: portable.TranscriptBody{
			Format: "cassini.words.v1", WordCount: 1,
			Items: []portable.TranscriptItem{{Speaker: "spk1", StartMS: 0, EndMS: 100, Text: "hello"}},
		},
	}}, 0)
	if err != nil {
		t.Fatalf("encode annotated manifest: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded.Main.JSON, &document); err != nil {
		t.Fatalf("parse encoded manifest: %v", err)
	}
	return document
}

// checkAgainstSchemaObject checks one emitted object against the schema object
// that declares it: nothing undeclared, nothing required missing, and every
// declared pattern and constant satisfied.
func checkAgainstSchemaObject(t *testing.T, schema map[string]any, where string, value, def map[string]any) {
	t.Helper()
	if def == nil {
		t.Errorf("%s: the schema has no definition for this object", where)
		return
	}
	declared, _ := def["properties"].(map[string]any)
	for key, member := range value {
		property, ok := declared[key].(map[string]any)
		if !ok {
			t.Errorf("%s: the writer emits %q, which the schema does not declare", where, key)
			continue
		}
		checkAgainstSchemaProperty(t, schema, where+"."+key, member, resolveSchemaRef(schema, property))
	}
	required, _ := def["required"].([]any)
	for _, key := range required {
		if _, ok := value[key.(string)]; !ok {
			t.Errorf("%s: the schema requires %q, which the writer does not emit", where, key)
		}
	}
}

func checkAgainstSchemaProperty(t *testing.T, schema map[string]any, where string, value any, property map[string]any) {
	t.Helper()
	if want, ok := property["const"]; ok && value != want {
		t.Errorf("%s = %v, but the schema's const is %v", where, value, want)
	}
	text, isString := value.(string)
	if pattern, ok := property["pattern"].(string); ok {
		if !isString {
			t.Errorf("%s: the schema declares a pattern but the writer emits %T", where, value)
		} else if !compileSchemaPattern(t, pattern).MatchString(text) {
			t.Errorf("%s = %q, which the schema's pattern %s rejects", where, text, pattern)
		}
	}
	if forbidden, ok := nested(property, "not")["pattern"].(string); ok && isString {
		if compileSchemaPattern(t, forbidden).MatchString(text) {
			t.Errorf("%s = %q, which matches the pattern the schema forbids (%s)", where, text, forbidden)
		}
	}
}

// resolveSchemaRef follows a local "#/$defs/<name>" reference. The schema's
// references are all of that one shape.
func resolveSchemaRef(schema, property map[string]any) map[string]any {
	ref, ok := property["$ref"].(string)
	if !ok {
		return property
	}
	name, found := strings.CutPrefix(ref, "#/$defs/")
	if !found {
		return property
	}
	if def := nested(schema, "$defs", name); def != nil {
		return def
	}
	return property
}

// ecmaUnicodeEscape rewrites a JSON Schema (ECMA-262) \uXXXX escape into the
// \x{XXXX} form Go's RE2 accepts. The schema's patterns use nothing else RE2
// lacks.
var ecmaUnicodeEscape = regexp.MustCompile(`\\u([0-9A-Fa-f]{4})`)

func compileSchemaPattern(t *testing.T, pattern string) *regexp.Regexp {
	t.Helper()
	compiled, err := regexp.Compile(ecmaUnicodeEscape.ReplaceAllString(pattern, `\x{$1}`))
	if err != nil {
		t.Fatalf("schema pattern %q does not compile: %v", pattern, err)
	}
	return compiled
}

const manifestSchemaPath = "../../../spec/cassini-portable-meeting-manifest-v1.schema.json"

func readManifestSchema(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(manifestSchemaPath)
	if err != nil {
		t.Fatalf("read %s: %v", manifestSchemaPath, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", manifestSchemaPath, err)
	}
	return doc
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func nested(doc map[string]any, keys ...string) map[string]any {
	current := doc
	for _, key := range keys {
		next, ok := current[key].(map[string]any)
		if !ok {
			return nil
		}
		current = next
	}
	return current
}
