package cassini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"gocassini/internal/portable"
)

// Round trips through real packed .opus files. The op semantics in isolation
// are in annotate_ops_test.go; these prove what only a real file can: that the
// marks land in the manifest, that nothing else in the file moves, and that the
// result document and exit codes are the ones the operator reads.

// operatorAnnotateResult mirrors cassini-operator's annotateResult field for
// field (internal/operator/annotate_cli.go). The operator decodes this
// command's stdout into that struct; decoding into a copy here pins the
// contract from this side.
type operatorAnnotateResult struct {
	Format          string          `json:"format"`
	Annotations     json.RawMessage `json:"annotations"`
	Revision        int             `json:"revision"`
	OperationID     string          `json:"operationId,omitempty"`
	Added           []string        `json:"added,omitempty"`
	Removed         []string        `json:"removed,omitempty"`
	NotFound        []string        `json:"notFound,omitempty"`
	Carried         int             `json:"carried,omitempty"`
	Resolved        *bool           `json:"resolved"`
	AudioOpusSHA256 string          `json:"audioOpusSha256"`
	ContainerSHA256 string          `json:"containerSha256"`
}

// annotateResultMembers is the result document's exact member set.
var annotateResultMembers = []string{
	"added", "annotations", "audioOpusSha256", "carried", "containerSha256",
	"format", "notFound", "operationId", "removed", "resolved", "revision",
}

// decodeAnnotateResult decodes a --json result, first checking it has exactly
// the contract's members and that the three lists are lists, never null.
func decodeAnnotateResult(t *testing.T, stdout string) operatorAnnotateResult {
	t.Helper()
	members := annotateResultRawMembers(t, stdout)
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)
	if !slices.Equal(names, annotateResultMembers) {
		t.Fatalf("result members = %v, want exactly %v", names, annotateResultMembers)
	}
	for _, list := range []string{"added", "removed", "notFound"} {
		if !bytes.HasPrefix(bytes.TrimSpace(members[list]), []byte("[")) {
			t.Errorf("%s = %s, want a list (empty rather than null)", list, members[list])
		}
	}
	var result operatorAnnotateResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode result into the operator's shape: %v\n%s", err, stdout)
	}
	if result.Format != "cassini.annotate.result.v1" {
		t.Fatalf("format = %q", result.Format)
	}
	return result
}

func annotateResultRawMembers(t *testing.T, stdout string) map[string]json.RawMessage {
	t.Helper()
	var members map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &members); err != nil {
		t.Fatalf("result is not a JSON object: %v\n%s", err, stdout)
	}
	return members
}

// writeAnnotateBundle builds a meeting bundle carrying everything a careless
// manifest rewrite could lose: a words transcript, a display transcript (a
// chunk set of its own) and a summary.md attachment. A non-zero sineHz
// replaces the audio, so two bundles can hold different recordings.
func writeAnnotateBundle(t *testing.T, dir, name string, sineHz int) string {
	t.Helper()
	bundleDir := filepath.Join(dir, name+".meeting")
	if err := writeReadyMeetingBundleFixture(bundleDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if sineHz != 0 {
		if output, err := exec.Command("ffmpeg", "-y", "-v", "error",
			"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=%d:sample_rate=48000:duration=0.25", sineHz),
			"-c:a", "libopus", "-application", "voip",
			filepath.Join(bundleDir, "meeting.webm"),
		).CombinedOutput(); err != nil {
			t.Fatalf("write replacement audio: %v: %s", err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "summary.md"), []byte("# Meeting Summary\n\nHiring was discussed.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	display := `{"version":"transcript.display.v1","blocks":[{"id":"display-1","text":"Hello team."}]}`
	if err := os.WriteFile(filepath.Join(bundleDir, "transcript.display.v1.json"), []byte(display), 0o644); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(bundleDir, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["files"].(map[string]any)["summary"] = "summary.md"
	updated, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, updated, 0o644); err != nil {
		t.Fatal(err)
	}
	return bundleDir
}

func packAnnotateBundle(t *testing.T, bundleDir, outPath string, packArgs ...string) string {
	t.Helper()
	args := append([]string{"pack", bundleDir, "--out", outPath}, packArgs...)
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), args, &stdout, &stderr); code != 0 {
		t.Fatalf("pack fixture failed code=%d stderr=%q", code, stderr.String())
	}
	return outPath
}

func packAnnotateFixture(t *testing.T, dir, name string) string {
	t.Helper()
	return packAnnotateBundle(t, writeAnnotateBundle(t, dir, name, 0), filepath.Join(dir, name+".opus"))
}

func runAnnotateForTest(stdin string, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := runAnnotate(context.Background(), args, strings.NewReader(stdin), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func annotateOK(t *testing.T, stdin string, args ...string) operatorAnnotateResult {
	t.Helper()
	code, stdout, stderr := runAnnotateForTest(stdin, args...)
	if code != 0 {
		t.Fatalf("annotate %v: exit %d, stderr=%q", args, code, stderr)
	}
	return decodeAnnotateResult(t, stdout)
}

// applyInPlace runs apply as alice, ops on stdin, rewriting path in place.
func applyInPlace(t *testing.T, path, ops string, extra ...string) operatorAnnotateResult {
	t.Helper()
	return annotateOK(t, ops, append([]string{"apply", path, "--ops", "-", "--actor-id", "alice", "--json"}, extra...)...)
}

func annotationsIn(t *testing.T, path string) *portable.Annotations {
	t.Helper()
	doc, err := portable.ParseAnnotations(decodePortableManifestFromOpus(t, path).Annotations)
	if err != nil {
		t.Fatalf("parse annotations of %s: %v", path, err)
	}
	return doc
}

// manifestWithoutAnnotations is the generic manifest document minus its
// annotations member — the thing an annotation write must leave alone.
func manifestWithoutAnnotations(t *testing.T, path string) map[string]any {
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
	delete(document, "annotations")
	return document
}

func fileDigestForTest(t *testing.T, path string) string {
	t.Helper()
	digest, err := annotateFileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func sameAnnotationsJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	left, err := decodePortableMeetingDocument(a)
	if err != nil {
		t.Fatalf("decode %s: %v", a, err)
	}
	right, err := decodePortableMeetingDocument(b)
	if err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return reflect.DeepEqual(left, right)
}

func assertNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s exists (err=%v); a refused write must not create its output", path, err)
	}
}

const annotateTwoMarks = `{"ops":[
	{"op":"mark","tag":{"label":"hiring"},"target":{"kind":"meeting"}},
	{"op":"mark","tag":{"label":"budget"},"target":{"kind":"time-range","startMs":50,"endMs":150}}
]}`

func TestAnnotateApplyWritesMarksIntoTheFile(t *testing.T) {
	requireFFMediaTools(t)
	path := packAnnotateFixture(t, t.TempDir(), "meeting")
	manifest := decodePortableManifestFromOpus(t, path)
	if manifest.Audio.DurationMS < 150 {
		t.Fatalf("the fixture is %d ms long; the ranges below need 150", manifest.Audio.DurationMS)
	}
	const namespace = "urn:uuid:07e4eab6-4f5f-4cc4-8913-46432c5cd726"

	result := applyInPlace(t, path, `{"ops":[
		{"op":"mark","tag":{"label":"  Hiring "},"target":{"kind":"meeting"}},
		{"op":"mark","tag":{"label":"budget"},"target":{"kind":"time-range","startMs":50,"endMs":150}}
	]}`, "--actor-kind", "agent", "--tag-namespace", namespace)

	doc := annotationsIn(t, path)
	if doc == nil {
		t.Fatal("the file carries no annotations after apply")
	}
	if doc.Revision != 1 || result.Revision != 1 {
		t.Errorf("revision = %d (result %d), want 1 on a first write", doc.Revision, result.Revision)
	}
	// The binding is the audio digest, taken from the file on its first write.
	if doc.AudioOpusSHA256 != manifest.Integrity.OpusSHA256 {
		t.Errorf("binding = %s, want the file's audio digest %s", doc.AudioOpusSHA256, manifest.Integrity.OpusSHA256)
	}
	if doc.TagNamespace != namespace {
		t.Errorf("namespace = %q, want the flag's %q", doc.TagNamespace, namespace)
	}
	if len(doc.Tags) != 2 || doc.Tags[0].Label != "budget" || doc.Tags[1].Label != "Hiring" {
		t.Errorf("tags = %+v, want budget then Hiring (canonical order, label trimmed)", doc.Tags)
	}
	if len(doc.Items) != 2 || doc.Items[0].Target.Kind != portable.AnnotationTargetMeeting ||
		doc.Items[1].Target.Kind != portable.AnnotationTargetTimeRange ||
		*doc.Items[1].Target.StartMS != 50 || *doc.Items[1].Target.EndMS != 150 {
		t.Fatalf("items = %+v, want the meeting mark then [50, 150)", doc.Items)
	}
	createdAt := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)
	for _, item := range doc.Items {
		if item.Actor != (portable.AnnotationActor{Kind: "agent", ID: "alice"}) {
			t.Errorf("actor = %+v, want agent alice", item.Actor)
		}
		if item.OperationID != result.OperationID {
			t.Errorf("item operation = %q, want the batch's %q", item.OperationID, result.OperationID)
		}
		if !createdAt.MatchString(item.CreatedAtUTC) {
			t.Errorf("createdAtUtc = %q, want whole seconds in UTC ending Z", item.CreatedAtUTC)
		}
	}
	if !regexp.MustCompile(`^op_[a-z2-7]{26}$`).MatchString(result.OperationID) {
		t.Errorf("operationId = %q, want a minted op_ id", result.OperationID)
	}
	if !slices.Equal(result.Added, []string{doc.Items[0].ID, doc.Items[1].ID}) {
		t.Errorf("added = %v, want the two new item ids", result.Added)
	}
	if len(result.Removed) != 0 || len(result.NotFound) != 0 || result.Carried != 0 {
		t.Errorf("removed=%v notFound=%v carried=%d, want none", result.Removed, result.NotFound, result.Carried)
	}
	if result.Resolved == nil || !*result.Resolved {
		t.Errorf("resolved = %v, want true", result.Resolved)
	}
	if result.AudioOpusSHA256 != manifest.Integrity.OpusSHA256 {
		t.Errorf("audioOpusSha256 = %s, want the file's %s", result.AudioOpusSHA256, manifest.Integrity.OpusSHA256)
	}
	if result.ContainerSHA256 != fileDigestForTest(t, path) {
		t.Error("containerSha256 is not the digest of the file as written")
	}
	if !sameAnnotationsJSON(t, result.Annotations, decodePortableManifestFromOpus(t, path).Annotations) {
		t.Errorf("the result's annotations are not what the file carries:\n%s", result.Annotations)
	}
}

func TestAnnotateApplyChangesNothingButTheAnnotations(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	path := packAnnotateFixture(t, tmp, "meeting")
	out := filepath.Join(tmp, "annotated.opus")

	beforeManifest := decodePortableManifestFromOpus(t, path)
	// Proof the fixture carries what a careless rewrite would lose.
	if len(beforeManifest.Attachments) == 0 || len(beforeManifest.Summary) == 0 || len(beforeManifest.ReadableTranscripts) == 0 {
		t.Fatalf("the fixture must carry a summary and a display transcript for this to prove anything: %+v", beforeManifest)
	}
	before := manifestWithoutAnnotations(t, path)
	beforeTags, err := portableMeetingTags(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeAudio, err := computePortableAudioIntegrity(path)
	if err != nil {
		t.Fatal(err)
	}
	inputDigest := fileDigestForTest(t, path)

	result := annotateOK(t, annotateTwoMarks, "apply", path, "--ops", "-", "--actor-id", "alice", "--out", out, "--json")

	if fileDigestForTest(t, path) != inputDigest {
		t.Error("apply --out modified its input")
	}
	if after := manifestWithoutAnnotations(t, out); !reflect.DeepEqual(after, before) {
		t.Errorf("the manifest changed outside annotations:\n before %v\n after  %v", before, after)
	}
	afterTags, err := portableMeetingTags(out)
	if err != nil {
		t.Fatal(err)
	}
	transcriptTags := 0
	for key, value := range beforeTags {
		if !strings.HasPrefix(strings.ToUpper(key), "CASSINI_TX_") {
			continue
		}
		transcriptTags++
		if got := portableTagValue(afterTags, key); got != strings.TrimSpace(value) {
			t.Errorf("transcript tag %s changed", key)
		}
	}
	if transcriptTags == 0 {
		t.Fatal("the fixture has no CASSINI_TX_* tags, so this proves nothing about transcripts")
	}
	// The audio digest is recomputed from the output's audio, not read from
	// its manifest.
	afterAudio, err := computePortableAudioIntegrity(out)
	if err != nil {
		t.Fatal(err)
	}
	if !portableAudioIntegrityEqual(afterAudio, beforeAudio) {
		t.Errorf("the audio changed:\n before %+v\n after  %+v", beforeAudio, afterAudio)
	}
	if result.ContainerSHA256 != fileDigestForTest(t, out) || result.ContainerSHA256 == inputDigest {
		t.Error("containerSha256 must be the new file's digest, which differs from the input's")
	}
}

func TestAnnotateApplySameBatchTwiceMarksOnce(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	path := packAnnotateFixture(t, tmp, "meeting")
	opsPath := filepath.Join(tmp, "ops.json")
	if err := os.WriteFile(opsPath, []byte(`{"ops":[{"op":"mark","tag":{"label":"hiring"},"target":{"kind":"meeting"}}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{"apply", path, "--ops", opsPath, "--actor-id", "alice", "--json"}

	first := annotateOK(t, "", args...)
	written := fileDigestForTest(t, path)
	// The retry after a lost response.
	second := annotateOK(t, "", args...)

	if len(second.Added) != 0 {
		t.Errorf("the retry added %v", second.Added)
	}
	if doc := annotationsIn(t, path); len(doc.Items) != 1 {
		t.Errorf("items = %d after the same batch twice, want 1", len(doc.Items))
	}
	// A batch that changes nothing is not a commit: no new revision, and the
	// file is not rewritten.
	if second.Revision != 1 {
		t.Errorf("revision = %d after a no-op batch, want it to stay 1", second.Revision)
	}
	if fileDigestForTest(t, path) != written || second.ContainerSHA256 != first.ContainerSHA256 {
		t.Error("a batch that changed nothing rewrote the file")
	}

	// A caller that asked for an output still finds one there.
	copyPath := filepath.Join(tmp, "copy.opus")
	third := annotateOK(t, "", append(args, "--out", copyPath)...)
	got, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatalf("no output at --out for a no-op batch: %v", err)
	}
	if want, _ := os.ReadFile(path); !bytes.Equal(got, want) || third.ContainerSHA256 != written {
		t.Error("the no-op output is not a byte-for-byte copy of the input")
	}
}

func TestAnnotateApplyUndoUnmarkRelabelAndTheNamespace(t *testing.T) {
	requireFFMediaTools(t)
	path := packAnnotateFixture(t, t.TempDir(), "meeting")

	first := applyInPlace(t, path, `{"ops":[
		{"op":"mark","tag":{"id":"tag_alpha","label":"alpha"},"target":{"kind":"meeting"}},
		{"op":"mark","tag":{"id":"tag_beta","label":"beta"},"target":{"kind":"time-range","startMs":10,"endMs":20}}
	]}`, "--operation-id", "op_first")
	doc := annotationsIn(t, path)
	if first.OperationID != "op_first" {
		t.Errorf("operationId = %q, want the flag's", first.OperationID)
	}
	uuidV4 := regexp.MustCompile(`^urn:uuid:[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !uuidV4.MatchString(doc.TagNamespace) {
		t.Fatalf("namespace = %q; a first write with no --tag-namespace must mint a random uuid", doc.TagNamespace)
	}
	namespace := doc.TagNamespace

	// A mark by a defined id ignores a differing label; a mark by label finds
	// the tag case-insensitively; a namespace flag on a file that already has
	// one is ignored.
	second := applyInPlace(t, path, `{"ops":[
		{"op":"mark","tag":{"id":"tag_alpha","label":"not a rename"},"target":{"kind":"time-range","startMs":30,"endMs":40}},
		{"op":"mark","tag":{"label":"ALPHA"},"target":{"kind":"time-range","startMs":50,"endMs":60}}
	]}`, "--operation-id", "op_second", "--tag-namespace", "urn:uuid:00000000-0000-4000-8000-000000000000")
	doc = annotationsIn(t, path)
	if doc.TagNamespace != namespace {
		t.Errorf("namespace moved from %q to %q; it is set once and never changed", namespace, doc.TagNamespace)
	}
	if second.Revision != 2 || len(doc.Items) != 4 || len(doc.Tags) != 2 || doc.Tags[0].Label != "alpha" {
		t.Fatalf("after the second batch: revision %d, tags %+v, %d items", second.Revision, doc.Tags, len(doc.Items))
	}

	// Undo takes out the first batch; beta, left with no marks, goes with it.
	third := applyInPlace(t, path, `{"ops":[{"op":"undo-operation","operationId":"op_first"}]}`)
	doc = annotationsIn(t, path)
	if len(third.Removed) != 2 || len(doc.Items) != 2 || len(doc.Tags) != 1 || doc.Tags[0].ID != "tag_alpha" {
		t.Fatalf("after undo: removed %v, tags %+v, %d items", third.Removed, doc.Tags, len(doc.Items))
	}
	for _, item := range doc.Items {
		if item.OperationID != "op_second" {
			t.Errorf("item %s from %s survived the undo of op_first", item.ID, item.OperationID)
		}
	}

	// unmark-tag with a target takes out that mark only.
	fourth := applyInPlace(t, path, `{"ops":[{"op":"unmark-tag","tagId":"tag_alpha","target":{"kind":"time-range","startMs":30,"endMs":40}}]}`)
	doc = annotationsIn(t, path)
	if len(fourth.Removed) != 1 || len(doc.Items) != 1 || *doc.Items[0].Target.StartMS != 50 {
		t.Fatalf("after unmark-tag with a target: removed %v, items %+v", fourth.Removed, doc.Items)
	}

	fifth := applyInPlace(t, path, `{"ops":[{"op":"relabel","tagId":"tag_alpha","label":" Renamed "}]}`)
	doc = annotationsIn(t, path)
	if fifth.Revision != 5 || doc.Tags[0].Label != "Renamed" {
		t.Fatalf("after relabel: revision %d, tags %+v", fifth.Revision, doc.Tags)
	}

	last := doc.Items[0].ID
	sixth := applyInPlace(t, path, fmt.Sprintf(`{"ops":[{"op":"unmark","itemId":%q},{"op":"unmark","itemId":"mk_gone"}]}`, last))
	if !slices.Equal(sixth.Removed, []string{last}) || !slices.Equal(sixth.NotFound, []string{"mk_gone"}) {
		t.Fatalf("unmark: removed %v notFound %v", sixth.Removed, sixth.NotFound)
	}
	// Emptied, not deleted: the revision keeps counting and the namespace stays
	// the file's.
	doc = annotationsIn(t, path)
	if doc == nil || doc.Revision != 6 || len(doc.Tags) != 0 || len(doc.Items) != 0 || doc.TagNamespace != namespace {
		t.Fatalf("after the last unmark: %+v", doc)
	}
	raw := string(decodePortableManifestFromOpus(t, path).Annotations)
	if !strings.Contains(raw, `"tags":[]`) || !strings.Contains(raw, `"items":[]`) {
		t.Errorf("an empty document must write empty lists, not null: %s", raw)
	}
}

func TestAnnotateApplyExpectRevisionMismatchWritesNothing(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	path := packAnnotateFixture(t, tmp, "meeting")
	out := filepath.Join(tmp, "out.opus")
	digest := fileDigestForTest(t, path)

	code, _, stderr := runAnnotateForTest(annotateTwoMarks, "apply", path, "--ops", "-", "--actor-id", "alice",
		"--expect-revision", "1", "--out", out, "--json")
	if code != annotateExitRevision {
		t.Fatalf("exit = %d, want %d; stderr=%q", code, annotateExitRevision, stderr)
	}
	if !strings.Contains(stderr, "revision 0") {
		t.Errorf("stderr = %q, want it to name the file's actual revision", stderr)
	}
	if fileDigestForTest(t, path) != digest {
		t.Error("a revision mismatch changed the file")
	}
	assertNotExist(t, out)

	// 0 means "carries none", which a fresh file does.
	applyInPlace(t, path, annotateTwoMarks, "--expect-revision", "0")
	digest = fileDigestForTest(t, path)
	if code, _, _ := runAnnotateForTest(annotateTwoMarks, "apply", path, "--ops", "-", "--actor-id", "alice", "--expect-revision", "0"); code != annotateExitRevision {
		t.Fatalf("expect 0 on a file at revision 1: exit %d, want %d", code, annotateExitRevision)
	}
	if fileDigestForTest(t, path) != digest {
		t.Error("a revision mismatch changed the file")
	}
}

func TestAnnotateApplyRefusesAnInvalidRangeAndWritesNothing(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	path := packAnnotateFixture(t, tmp, "meeting")
	duration := decodePortableManifestFromOpus(t, path).Audio.DurationMS
	digest := fileDigestForTest(t, path)
	out := filepath.Join(tmp, "out.opus")

	for reason, target := range map[string]string{
		"past the end":      fmt.Sprintf(`{"kind":"time-range","startMs":100,"endMs":%d}`, duration+1000),
		"empty or reversed": `{"kind":"time-range","startMs":100,"endMs":100}`,
	} {
		ops := `{"ops":[{"op":"mark","tag":{"label":"hiring"},"target":` + target + `}]}`
		code, stdout, stderr := runAnnotateForTest(ops, "apply", path, "--ops", "-", "--actor-id", "alice", "--out", out, "--json")
		if code != annotateExitInvalid {
			t.Fatalf("%s: exit = %d, want %d; stderr=%q", reason, code, annotateExitInvalid, stderr)
		}
		// The operator relays stderr to the caller for exit 4.
		if !strings.Contains(stderr, reason) || !strings.Contains(stderr, "ops[0] (mark)") {
			t.Errorf("%s: stderr = %q, want the op and the reason", reason, stderr)
		}
		if stdout != "" {
			t.Errorf("%s: a refusal printed a result: %q", reason, stdout)
		}
		if fileDigestForTest(t, path) != digest {
			t.Errorf("%s: a refused batch changed the file", reason)
		}
		assertNotExist(t, out)
	}
}

func TestAnnotateCarryResolved(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	// Two packs of one bundle: the same audio in two different containers —
	// what a rerun that did not change the audio produces.
	bundle := writeAnnotateBundle(t, tmp, "meeting", 0)
	delivered := packAnnotateBundle(t, bundle, filepath.Join(tmp, "delivered.opus"))
	sealed := packAnnotateBundle(t, bundle, filepath.Join(tmp, "sealed.opus"), "--title", "Rerun")
	sealedManifest := decodePortableManifestFromOpus(t, sealed)
	if decodePortableManifestFromOpus(t, delivered).Integrity.OpusSHA256 != sealedManifest.Integrity.OpusSHA256 {
		t.Fatal("the fixture assumes two packs of one bundle share their audio digest")
	}
	applyInPlace(t, delivered, annotateTwoMarks)
	sealedDigest := fileDigestForTest(t, sealed)
	out := filepath.Join(tmp, "outgoing.opus")

	result := annotateOK(t, "", "carry", delivered, sealed, "--out", out, "--json")

	if result.Carried != 2 || result.Resolved == nil || !*result.Resolved || result.Revision != 1 {
		t.Fatalf("carried=%d resolved=%v revision=%d, want 2 resolved marks at the delivered revision", result.Carried, result.Resolved, result.Revision)
	}
	if result.OperationID != "" || len(result.Added) != 0 || len(result.Removed) != 0 || len(result.NotFound) != 0 {
		t.Errorf("carry is not a batch, but reported %+v", result)
	}
	if !sameAnnotationsJSON(t, decodePortableManifestFromOpus(t, out).Annotations, decodePortableManifestFromOpus(t, delivered).Annotations) {
		t.Error("the carried annotations are not the delivered copy's")
	}
	// Everything else is the sealed file's — its new title included.
	if !reflect.DeepEqual(manifestWithoutAnnotations(t, out), manifestWithoutAnnotations(t, sealed)) {
		t.Error("the output's manifest outside annotations is not the sealed file's")
	}
	if got := decodePortableManifestFromOpus(t, out).Meeting.Title; got != "Rerun" {
		t.Errorf("title = %q, want the sealed file's", got)
	}
	if fileDigestForTest(t, sealed) != sealedDigest {
		t.Error("carry modified the sealed file")
	}
	if result.AudioOpusSHA256 != sealedManifest.Integrity.OpusSHA256 || result.ContainerSHA256 != fileDigestForTest(t, out) {
		t.Errorf("digests: audio %s container %s", result.AudioOpusSHA256, result.ContainerSHA256)
	}
	// The revision travels, so a client that read it before the rerun can
	// still write against it after.
	if next := applyInPlace(t, out, `{"ops":[{"op":"unmark-tag","tagId":"`+annotationsIn(t, out).Tags[0].ID+`"}]}`, "--expect-revision", "1"); next.Revision != 2 {
		t.Errorf("apply after carry: revision %d, want 2", next.Revision)
	}
}

func TestAnnotateCarryAcrossDifferentAudioIsUnresolved(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	delivered := packAnnotateFixture(t, tmp, "delivered")
	sealed := packAnnotateBundle(t, writeAnnotateBundle(t, tmp, "rerun", 440), filepath.Join(tmp, "sealed.opus"))
	deliveredDigest := decodePortableManifestFromOpus(t, delivered).Integrity.OpusSHA256
	if deliveredDigest == decodePortableManifestFromOpus(t, sealed).Integrity.OpusSHA256 {
		t.Fatal("the fixture needs two different recordings")
	}
	applyInPlace(t, delivered, annotateTwoMarks)
	out := filepath.Join(tmp, "outgoing.opus")

	result := annotateOK(t, "", "carry", delivered, sealed, "--out", out, "--json")

	if result.Carried != 2 || result.Resolved == nil || *result.Resolved {
		t.Fatalf("carried=%d resolved=%v, want 2 marks carried unresolved", result.Carried, result.Resolved)
	}
	// Kept with their original binding, not re-pinned to audio they were never
	// drawn on.
	if got := annotationsIn(t, out).AudioOpusSHA256; got != deliveredDigest {
		t.Errorf("binding = %s, want the delivered audio %s", got, deliveredDigest)
	}
	if shown := annotateOK(t, "", "show", out, "--json"); shown.Resolved == nil || *shown.Resolved || shown.Revision != 1 {
		t.Errorf("show on the carried file: resolved=%v revision=%d", shown.Resolved, shown.Revision)
	}

	// And apply refuses to add to them.
	digest := fileDigestForTest(t, out)
	code, _, stderr := runAnnotateForTest(annotateTwoMarks, "apply", out, "--ops", "-", "--actor-id", "alice", "--json")
	if code != annotateExitUnresolved || !strings.Contains(stderr, "different audio") {
		t.Fatalf("apply on unresolved marks: exit %d stderr %q, want %d", code, stderr, annotateExitUnresolved)
	}
	if fileDigestForTest(t, out) != digest {
		t.Error("a refused apply changed the file")
	}

	// But they can still be cleaned up: relabel and removals work, and keep the
	// binding while marks from the other audio remain.
	doc := annotationsIn(t, out)
	cleaned := applyInPlace(t, out, `{"ops":[{"op":"relabel","tagId":"`+doc.Tags[0].ID+`","label":"renamed"},{"op":"unmark-tag","tagId":"`+doc.Tags[1].ID+`"}]}`)
	if cleaned.Resolved == nil || *cleaned.Resolved || len(cleaned.Removed) != 1 {
		t.Fatalf("relabel and unmark-tag on unresolved marks: resolved=%v removed=%v", cleaned.Resolved, cleaned.Removed)
	}
	if got := annotationsIn(t, out).AudioOpusSHA256; got != deliveredDigest {
		t.Errorf("binding = %s while marks from other audio remain, want %s", got, deliveredDigest)
	}
	// A document with no marks left binds to this audio, and marking works again.
	last := annotationsIn(t, out).Items[0].ID
	if emptied := applyInPlace(t, out, `{"ops":[{"op":"unmark","itemId":"`+last+`"}]}`); emptied.Resolved == nil || !*emptied.Resolved {
		t.Errorf("an emptied document should bind to this audio: resolved=%v", emptied.Resolved)
	}
	if marked := applyInPlace(t, out, annotateTwoMarks); len(marked.Added) != 2 {
		t.Errorf("marking after the cleanup added %v", marked.Added)
	}
}

func TestAnnotateCarryRebindsADocumentWithNoMarks(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	delivered := packAnnotateFixture(t, tmp, "delivered")
	sealed := packAnnotateBundle(t, writeAnnotateBundle(t, tmp, "rerun", 440), filepath.Join(tmp, "sealed.opus"))
	applyInPlace(t, delivered, annotateTwoMarks, "--operation-id", "op_gone")
	applyInPlace(t, delivered, `{"ops":[{"op":"undo-operation","operationId":"op_gone"}]}`)
	out := filepath.Join(tmp, "outgoing.opus")

	result := annotateOK(t, "", "carry", delivered, sealed, "--out", out, "--json")

	if result.Carried != 0 || result.Revision != 2 || result.Resolved == nil || !*result.Resolved {
		t.Fatalf("carried=%d revision=%d resolved=%v, want an empty document at revision 2, bound to the sealed audio",
			result.Carried, result.Revision, result.Resolved)
	}
	if got := annotationsIn(t, out).AudioOpusSHA256; got != result.AudioOpusSHA256 {
		t.Errorf("binding = %s, want the sealed audio %s", got, result.AudioOpusSHA256)
	}
}

func TestAnnotateCarryWithNothingToCarryCopiesTheSealedFile(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	bundle := writeAnnotateBundle(t, tmp, "meeting", 0)
	delivered := packAnnotateBundle(t, bundle, filepath.Join(tmp, "delivered.opus"))
	sealed := packAnnotateBundle(t, bundle, filepath.Join(tmp, "sealed.opus"), "--title", "Rerun")
	out := filepath.Join(tmp, "outgoing.opus")

	code, stdout, stderr := runAnnotateForTest("", "carry", delivered, sealed, "--out", out, "--json")
	if code != 0 {
		t.Fatalf("carry: exit %d stderr %q", code, stderr)
	}
	result := decodeAnnotateResult(t, stdout)
	members := annotateResultRawMembers(t, stdout)
	if string(members["annotations"]) != "null" || string(members["resolved"]) != "null" || result.Carried != 0 || result.Revision != 0 {
		t.Errorf("annotations=%s resolved=%s carried=%d revision=%d, want null, null, 0, 0",
			members["annotations"], members["resolved"], result.Carried, result.Revision)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := os.ReadFile(sealed); !bytes.Equal(got, want) {
		t.Error("with nothing to carry, the output must be a byte-for-byte copy of the sealed file")
	}
	if result.ContainerSHA256 != fileDigestForTest(t, sealed) {
		t.Error("containerSha256 is not the copy's digest")
	}
}

func TestAnnotateCarryRefusesToWriteOverTheSealedFile(t *testing.T) {
	tmp := t.TempDir()
	delivered := filepath.Join(tmp, "delivered.opus")
	sealed := filepath.Join(tmp, "sealed.opus")
	for _, path := range []string{delivered, sealed} {
		if err := os.WriteFile(path, []byte("stub"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, out := range []string{sealed, filepath.Join(tmp, ".", "sealed.opus")} {
		code, _, stderr := runAnnotateForTest("", "carry", delivered, sealed, "--out", out)
		if code != annotateExitUsage || !strings.Contains(stderr, "never modifies") {
			t.Errorf("carry --out %s: exit %d stderr %q, want %d", out, code, stderr, annotateExitUsage)
		}
	}
	if got, _ := os.ReadFile(sealed); string(got) != "stub" {
		t.Error("the sealed file was touched")
	}
}

func TestAnnotateShowOnAFileWithoutAnnotations(t *testing.T) {
	requireFFMediaTools(t)
	path := packFixtureOpus(t, t.TempDir(), "meeting")
	manifest := decodePortableManifestFromOpus(t, path)

	// Through the root dispatch, as the operator runs it.
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"annotate", "show", path, "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("show: exit %d stderr %q", code, stderr.String())
	}
	result := decodeAnnotateResult(t, stdout.String())
	members := annotateResultRawMembers(t, stdout.String())
	if string(members["annotations"]) != "null" || string(members["resolved"]) != "null" {
		t.Errorf("annotations=%s resolved=%s, want null for a file with none", members["annotations"], members["resolved"])
	}
	if result.Revision != 0 || result.Carried != 0 || result.OperationID != "" {
		t.Errorf("revision=%d carried=%d operationId=%q, want zeros", result.Revision, result.Carried, result.OperationID)
	}
	if result.AudioOpusSHA256 != manifest.Integrity.OpusSHA256 || result.ContainerSHA256 != fileDigestForTest(t, path) {
		t.Errorf("digests: audio %s container %s", result.AudioOpusSHA256, result.ContainerSHA256)
	}

	stdout.Reset()
	if code := Run(context.Background(), []string{"annotate", "show", path}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "no annotations") {
		t.Errorf("human show: exit %d stdout %q", code, stdout.String())
	}
}

func TestAnnotateApplyOnAFileThatIsNotAPortableMeeting(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	in := filepath.Join(tmp, "in.opus")
	if err := os.WriteFile(in, []byte("this is not an ogg container"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(tmp, "out.opus")
	code, _, _ := runAnnotateForTest(annotateTwoMarks, "apply", in, "--ops", "-", "--actor-id", "alice", "--out", out)
	if code != annotateExitRuntime {
		t.Fatalf("exit = %d, want %d for an unreadable recording", code, annotateExitRuntime)
	}
	assertNotExist(t, out)
}

// Bad ops and bad request flags are refused with exit 4 before the recording
// is opened — which the stub input proves: reading it would fail with exit 1.
func TestAnnotateApplyRefusesBadOpsBeforeOpeningTheFile(t *testing.T) {
	tmp := t.TempDir()
	in := filepath.Join(tmp, "in.opus")
	if err := os.WriteFile(in, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		ops   string
		extra []string
		want  string
	}{
		{"not JSON", `not json`, nil, `is not {"ops"`},
		{"an unknown op", `{"ops":[{"op":"tag"}]}`, nil, "unknown op"},
		{"another op's member", `{"ops":[{"op":"unmark","itemId":"mk_1","tagId":"tag_a"}]}`, nil, "not a member"},
		{"an unknown actor kind", `{"ops":[]}`, []string{"--actor-kind", "robot"}, "--actor-kind"},
		{"a malformed operation id", `{"ops":[]}`, []string{"--operation-id", "not an id"}, "--operation-id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"apply", in, "--ops", "-", "--actor-id", "alice"}, tc.extra...)
			code, _, stderr := runAnnotateForTest(tc.ops, args...)
			if code != annotateExitInvalid || !strings.Contains(stderr, tc.want) {
				t.Fatalf("exit %d stderr %q, want %d containing %q", code, stderr, annotateExitInvalid, tc.want)
			}
		})
	}
	if got, _ := os.ReadFile(in); string(got) != "stub" {
		t.Error("the input was touched")
	}
}

func TestAnnotateUsageErrors(t *testing.T) {
	tmp := t.TempDir()
	stub := filepath.Join(tmp, "in.opus")
	if err := os.WriteFile(stub, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	apply := func(extra ...string) []string {
		return append([]string{"apply", stub, "--ops", "-", "--actor-id", "alice"}, extra...)
	}
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no subcommand", nil, "Usage"},
		{"an unknown subcommand", []string{"stamp", stub}, "unknown annotate subcommand"},
		{"apply without --ops", []string{"apply", stub, "--actor-id", "alice"}, "--ops is required"},
		{"apply without --actor-id", []string{"apply", stub, "--ops", "-"}, "--actor-id is required"},
		{"apply with a blank --actor-id", []string{"apply", stub, "--ops", "-", "--actor-id", "  "}, "--actor-id is required"},
		{"apply to a file that is not .opus", apply("--out", filepath.Join(tmp, "out.txt")), "--out must be a .opus"},
		{"a malformed namespace", apply("--tag-namespace", "urn:uuid:NOT-A-UUID"), "--tag-namespace"},
		{"a negative revision", apply("--expect-revision", "-1"), "--expect-revision"},
		{"show with two files", []string{"show", stub, stub}, "takes 1 file"},
		{"carry with one file", []string{"carry", stub, "--out", filepath.Join(tmp, "o.opus")}, "takes 2 file"},
		{"carry without --out", []string{"carry", stub, stub}, "--out is required"},
		{"an unknown flag", []string{"show", stub, "--jsn"}, "flag provided but not defined"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runAnnotateForTest("", tc.args...)
			if code != annotateExitUsage || !strings.Contains(stderr, tc.want) {
				t.Fatalf("exit %d stderr %q, want %d containing %q", code, stderr, annotateExitUsage, tc.want)
			}
		})
	}

	if code, stdout, _ := runAnnotateForTest("", "--help"); code != 0 || !strings.Contains(stdout, "cassini annotate apply") {
		t.Errorf("annotate --help: exit %d stdout %q", code, stdout)
	}
	if code, _, stderr := runAnnotateForTest("", "apply", "--help"); code != 0 || !strings.Contains(stderr, "cassini annotate apply") {
		t.Errorf("annotate apply --help: exit %d stderr %q", code, stderr)
	}
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), nil, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "annotate") {
		t.Errorf("root usage does not mention annotate:\n%s", stdout.String())
	}
}
