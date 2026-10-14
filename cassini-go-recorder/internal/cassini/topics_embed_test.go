package cassini

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gocassini/internal/inspect"
	"gocassini/internal/modelstore"
	"gocassini/internal/topics"
	"gocassini/internal/transcribe"
)

type topicsPassagesFile struct {
	header topicsPassagesHeader
	lines  []topicsPassageLine
}

func readTopicsPassages(t *testing.T, path string) topicsPassagesFile {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out topicsPassagesFile
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for i := 0; scanner.Scan(); i++ {
		if i == 0 {
			if err := json.Unmarshal(scanner.Bytes(), &out.header); err != nil {
				t.Fatal(err)
			}
			continue
		}
		var line topicsPassageLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		out.lines = append(out.lines, line)
	}
	return out
}

func runTopicsEmbedCmd(ctx context.Context, root string, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := Run(ctx, append([]string{"topics", "embed", "--cache-root", root}, args...), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// writeTopicsWords writes a --words file of n synthetic words: sentences of
// public text, one speaker, 100 ms apart.
func writeTopicsWords(t *testing.T, text string, n int) string {
	t.Helper()
	fields := strings.Fields(text)
	type word struct {
		Text    string `json:"text"`
		StartMS int64  `json:"startMs"`
		EndMS   int64  `json:"endMs"`
		Speaker string `json:"speaker"`
	}
	var words []word
	for i := range n {
		words = append(words, word{Text: fields[i%len(fields)], StartMS: int64(i) * 100, EndMS: int64(i)*100 + 90, Speaker: "spk_a"})
	}
	raw, _ := json.Marshal(map[string]any{"words": words})
	path := filepath.Join(t.TempDir(), "words.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const udhrEnglish = "All human beings are born free and equal in dignity and rights. They are endowed with reason and conscience and should act towards one another in a spirit of brotherhood."

// The bundle the operator keeps and the .opus it delivered embed under the
// same payload digest and key, and the output carries no transcript text.
func TestTopicsEmbedBundleAndOpusGiveTheSameKey(t *testing.T) {
	requireFFMediaTools(t)
	bundleDir := filepath.Join(t.TempDir(), "01KWEKPZVEJWP9BYBPBX9ZRNDQ.meeting")
	if err := writeReadyMeetingBundleFixture(bundleDir, "/tmp/source.mkv"); err != nil {
		t.Fatal(err)
	}
	opus, packed := packTopicsFixture(t, bundleDir)
	root := t.TempDir()
	m := fakeInstallShipped(t, root, DefaultEmbeddingModelID)
	stubTopicsEmbedder(t, &fakeEmbedder{})
	var headers []topicsPassagesHeader
	for _, input := range []string{bundleDir, opus} {
		out := filepath.Join(t.TempDir(), "passages.jsonl")
		code, stdout, stderr := runTopicsEmbedCmd(context.Background(), root, "--input", input, "--out", out)
		if code != 0 || !strings.Contains(stdout, `{"locked":true}`) || !strings.Contains(stdout, `{"count":1}`) {
			t.Fatalf("%s: code %d stdout %q stderr %q", input, code, stdout, stderr)
		}
		got := readTopicsPassages(t, out)
		raw, _ := os.ReadFile(out)
		if bytes.Contains(raw, []byte("hello")) || bytes.Contains(raw, []byte("team")) {
			t.Fatal("the output carries transcript text")
		}
		if len(got.lines) != 1 || got.lines[0].WordCount != 2 || got.lines[0].StartMS != 0 || got.lines[0].EndMS != 200 || got.lines[0].Boundary != "end" || len(got.lines[0].Vector) != topics.Dimensions {
			t.Fatalf("%s: passages %+v", input, got.lines)
		}
		headers = append(headers, got.header)
	}
	want := topics.EmbedKey(packed, m.ID+"@"+m.Revision)
	for _, h := range headers {
		if h.PayloadSHA256 != packed || h.EmbedKey != want || h.ModelRevision != m.ID+"@"+m.Revision || h.ChunkerVersion != topics.ChunkerVersion ||
			h.RecipeVersion != topics.EmbedRecipeVersion || h.Format != topicsPassagesFormat || h.Count != 1 || !h.TruncatedNone ||
			h.DetectorVersion != topics.DetectorVersion || h.Language != topics.LanguageUnknown {
			t.Fatalf("header %+v, want payload %s key %s", h, packed, want)
		}
	}
}

// EmbedKey is the documented sha256 of the four values, so the operator can
// compute it from stored values.
func TestTopicsEmbedKeyIsTheDocumentedDigest(t *testing.T) {
	got := topics.EmbedKey("p", "m@r")
	if got != sha256Hex([]byte("p\nm@r\n"+topics.ChunkerVersion+"\n"+topics.EmbedRecipeVersion)) {
		t.Fatal("EmbedKey is not sha256(payload \\n revision \\n chunker \\n recipe)")
	}
	// A golden value: cassini-operator recomputes keys from stored values and
	// tests against the same one.
	shipped := "embeddinggemma-2-text-int8@" + topics.ShippedModelRevision
	if topics.ChunkerVersion == "natural-1" && topics.EmbedRecipeVersion == "r1" && topics.EmbedKey(strings.Repeat("0", 64), shipped) != "414248845d38953f659282ff86a8c0d6bb6fd345275827fc4a72f0158e763ea3" {
		t.Fatal("EmbedKey changed for natural-1/r1")
	}
	if topics.EmbedKey("p", "m@s") == got || topics.EmbedKey("q", "m@r") == got {
		t.Fatal("the key ignores an input")
	}
}

// With --known-key equal to the computed key, embed answers unchanged before
// it takes the inference lock or loads anything, even with the lock held.
// With the lock held and a different key, it waits for the lock and says
// "locked" only once it holds it.
func TestTopicsEmbedKnownKeyStopsBeforeTheLock(t *testing.T) {
	root := t.TempDir()
	m := fakeInstallShipped(t, root, DefaultEmbeddingModelID)
	words := writeTopicsWords(t, udhrEnglish, 60)
	prevCheck, prevOpen, prevTok := topicsRuntimeCheckFn, openTopicsEmbedderFn, loadTopicsTokenizerFn
	t.Cleanup(func() {
		topicsRuntimeCheckFn, openTopicsEmbedderFn, loadTopicsTokenizerFn = prevCheck, prevOpen, prevTok
	})
	topicsRuntimeCheckFn = func() error { t.Error("checked the runtime for an unchanged meeting"); return nil }
	loadTopicsTokenizerFn = func(string) (topics.TokenCounter, error) {
		t.Error("loaded the tokenizer for an unchanged meeting")
		return fakeTokenCounter{}, nil
	}
	openTopicsEmbedderFn = func(modelstore.EmbeddingFiles, topics.Options) (textEmbedder, error) {
		t.Error("loaded the model for an unchanged meeting")
		return &fakeEmbedder{}, nil
	}
	unlock, err := transcribe.LockModelRuntime(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(words)
	key := topics.EmbedKey(sha256Hex(raw), m.ID+"@"+m.Revision)
	out := filepath.Join(t.TempDir(), "passages.jsonl")
	// Given up from the start: a regression that took the lock before
	// comparing keys fails at once instead of waiting for it.
	givenUp, giveUp := context.WithCancel(context.Background())
	giveUp()
	code, stdout, stderr := runTopicsEmbedCmd(givenUp, root, "--words", words, "--known-key", key, "--out", out)
	if code != 0 || !strings.Contains(stdout, `"unchanged":true`) || strings.Contains(stdout, "locked") {
		t.Fatalf("known key: code %d stdout %q stderr %q", code, stdout, stderr)
	}
	assertNotExist(t, out)

	// A different key embeds, so it must wait for the lock: given up while
	// waiting, it never says locked and loads nothing.
	topicsRuntimeCheckFn = func() error { return nil }
	loadTopicsTokenizerFn = func(string) (topics.TokenCounter, error) { return fakeTokenCounter{}, nil }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	code, stdout, stderr = runTopicsEmbedCmd(ctx, root, "--words", words, "--known-key", strings.Repeat("0", 64), "--out", out)
	if code != topicsExitRuntime || strings.Contains(stdout, "locked") || !strings.Contains(stderr, "model runtime lock") {
		t.Fatalf("lock held: code %d stdout %q stderr %q", code, stdout, stderr)
	}
	assertNotExist(t, out)
	unlock()
	openTopicsEmbedderFn = func(modelstore.EmbeddingFiles, topics.Options) (textEmbedder, error) { return &fakeEmbedder{}, nil }
	code, stdout, stderr = runTopicsEmbedCmd(ctx, root, "--words", words, "--out", out)
	if code != 0 || !strings.HasPrefix(stdout, `{"locked":true}`) {
		t.Fatalf("lock free: code %d stdout %q stderr %q", code, stdout, stderr)
	}
	if h := readTopicsPassages(t, out).header; h.EmbedKey != key || h.Language != "en" || h.LanguageConfidence < topics.LanguageConfidenceFloor {
		t.Fatalf("header %+v, want key %s and English", h, key)
	}
}

func TestTopicsEmbedExitCodes(t *testing.T) {
	root := t.TempDir()
	fakeInstallShipped(t, root, DefaultEmbeddingModelID)
	words := writeTopicsWords(t, udhrEnglish, 60)
	empty := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(empty, []byte(`{"words":[{"text":" ","startMs":0,"endMs":10,"speaker":"a"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	notWords := filepath.Join(t.TempDir(), "notes.json")
	if err := os.WriteFile(notWords, []byte(`["not", "a", "word", "list"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	nan := func(int) []float32 { v := make([]float32, 768); v[0] = float32(math.NaN()); return v }
	cases := []struct {
		name     string
		embedder *fakeEmbedder
		args     []string
		want     int
		reason   string
	}{
		{"not finite", &fakeEmbedder{vector: nan}, []string{"--words", words}, topicsExitModelFault, ""},
		{"guard in the runtime", &fakeEmbedder{err: fmt.Errorf("%w: input 0", topics.ErrModelFault)}, []string{"--words", words}, topicsExitModelFault, ""},
		{"runtime refuses a passage", &fakeEmbedder{err: fmt.Errorf("%w: text 0", topics.ErrTooLong)}, []string{"--words", words}, topicsExitModelFault, ""},
		{"runtime error", &fakeEmbedder{err: fmt.Errorf("boom")}, []string{"--words", words}, topicsExitRuntime, ""},
		{"no words", &fakeEmbedder{}, []string{"--words", empty}, topicsExitTranscript, "transcript-has-no-words"},
		{"not a word list", &fakeEmbedder{}, []string{"--words", notWords}, topicsExitTranscript, "no-transcript"},
		// A file that cannot be read now may be readable on a retry.
		{"no file", &fakeEmbedder{}, []string{"--input", filepath.Join(t.TempDir(), "gone.opus")}, topicsExitRuntime, "gone.opus"},
		{"both inputs", &fakeEmbedder{}, []string{"--words", words, "--input", words}, topicsExitUsage, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubTopicsEmbedder(t, c.embedder)
			out := filepath.Join(t.TempDir(), "passages.jsonl")
			code, _, stderr := runTopicsEmbedCmd(context.Background(), root, append(c.args, "--out", out)...)
			if code != c.want || !strings.Contains(stderr, c.reason) {
				t.Fatalf("code %d, want %d (%s): %s", code, c.want, c.reason, stderr)
			}
			assertNotExist(t, out)
		})
	}
	if code, _, _ := runTopicsEmbedCmd(context.Background(), t.TempDir(), "--words", words, "--out", filepath.Join(t.TempDir(), "v")); code != topicsExitUnavailable {
		t.Fatalf("model not installed: code %d", code)
	}
	// A transcript with no words says so even where no model is installed.
	if code, _, stderr := runTopicsEmbedCmd(context.Background(), t.TempDir(), "--words", empty, "--out", filepath.Join(t.TempDir(), "v")); code != topicsExitTranscript || !strings.Contains(stderr, "transcript-has-no-words") {
		t.Fatalf("no words, no model: code %d %s", code, stderr)
	}
}

// The reasons a transcript can never embed come from typed errors, not from
// error text, so an unreadable file is never mistaken for one.
func TestTopicsTranscriptReason(t *testing.T) {
	cases := map[string]error{
		"transcript-integrity": fmt.Errorf("decode transcript body: %w", inspect.ErrTranscriptIntegrity),
		"no-transcript":        fmt.Errorf("bundle x has %w", inspect.ErrNoDefaultTranscript),
		"":                     fmt.Errorf("compressed Opus sha256 mismatch between manifest and tag"),
	}
	for want, err := range cases {
		if got := topicsTranscriptReason(err); got != want {
			t.Errorf("%v: reason %q, want %q", err, got, want)
		}
	}
	if got := topicsTranscriptReason(fmt.Errorf("x: %w", inspect.ErrNotPortableMeeting)); got != "no-transcript" {
		t.Errorf("not a meeting: %q", got)
	}
}

// The real model on a public transcript: finite, unit-norm, 768-long vectors.
func TestTopicsEmbedWithTheModel(t *testing.T) {
	dir := os.Getenv("CASSINI_TOPICS_MODEL_DIR")
	if dir == "" {
		t.Skip("CASSINI_TOPICS_MODEL_DIR is not set")
	}
	words := writeTopicsWords(t, udhrEnglish, 400)
	out := filepath.Join(t.TempDir(), "passages.jsonl")
	code, stdout, stderr := runTopicsEmbedCmd(context.Background(), t.TempDir(), "--words", words, "--model-dir", dir, "--out", out)
	if code != 0 {
		t.Fatalf("code %d stdout %q stderr %q", code, stdout, stderr)
	}
	got := readTopicsPassages(t, out)
	if got.header.Count != len(got.lines) || got.header.Count < 3 || got.header.Language != "en" {
		t.Fatalf("header %+v with %d lines", got.header, len(got.lines))
	}
	for _, line := range got.lines {
		if err := topics.CheckVector(line.Vector); err != nil || line.TokenCount > topics.MaxTokens {
			t.Fatalf("passage %d: %v, %d tokens", line.Ordinal, err, line.TokenCount)
		}
	}
}
