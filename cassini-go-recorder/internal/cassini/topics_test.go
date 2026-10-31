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
	"sync"
	"testing"

	"gocassini/internal/modelstore"
	"gocassini/internal/topics"
	"gocassini/internal/transcribe"
)

// fakeEmbedder returns a unit vector per text, or what vector says.
type fakeEmbedder struct {
	vector  func(i int) []float32
	err     error
	started chan struct{}
	release chan struct{}
	// prefixes are the prompt prefixes it was called with.
	prefixes []string
}

func (f *fakeEmbedder) EmbedTexts(ctx context.Context, prefix string, texts []string) ([]topics.Embedding, error) {
	f.prefixes = append(f.prefixes, prefix)
	if f.started != nil {
		close(f.started)
	}
	if f.release != nil {
		<-f.release
	}
	if f.err != nil {
		return nil, f.err
	}
	out := make([]topics.Embedding, len(texts))
	for i := range texts {
		v := make([]float32, topics.Dimensions)
		v[i%topics.Dimensions] = 1
		if f.vector != nil {
			v = f.vector(i)
		}
		out[i] = topics.Embedding{Vector: v, TokenCount: 10 + i}
	}
	return out, nil
}
func (f *fakeEmbedder) Close() error { return nil }

func stubTopicsEmbedder(t *testing.T, embedder textEmbedder) {
	t.Helper()
	stubTopicsTokenizer(t)
	prevCheck, prevOpen := topicsRuntimeCheckFn, openTopicsEmbedderFn
	topicsRuntimeCheckFn = func() error { return nil }
	openTopicsEmbedderFn = func(modelstore.EmbeddingFiles, topics.Options) (textEmbedder, error) { return embedder, nil }
	t.Cleanup(func() { topicsRuntimeCheckFn, openTopicsEmbedderFn = prevCheck, prevOpen })
}

// fakeTokenCounter counts one token per byte plus the prompt: a text over
// topics.MaxTokens bytes is over the cap.
type fakeTokenCounter struct{}

func (fakeTokenCounter) Encode(prefix, text string) []int64 { return make([]int64, len(text)) }

func stubTopicsTokenizer(t *testing.T) {
	t.Helper()
	prev := loadTopicsTokenizerFn
	loadTopicsTokenizerFn = func(string) (topics.TokenCounter, error) { return fakeTokenCounter{}, nil }
	t.Cleanup(func() { loadTopicsTokenizerFn = prev })
}

func writeTopicsInput(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "texts.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runEmbedTexts(root string, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), append([]string{"topics", "embed-texts", "--cache-root", root}, args...), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

type topicsVectorsFile struct {
	header topicsVectorsHeader
	lines  []topicsVectorLine
}

func readTopicsVectors(t *testing.T, path string) topicsVectorsFile {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out topicsVectorsFile
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		if out.header.Format == "" {
			if err := json.Unmarshal(scanner.Bytes(), &out.header); err != nil {
				t.Fatal(err)
			}
			continue
		}
		var line topicsVectorLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		out.lines = append(out.lines, line)
	}
	return out
}

func TestTopicsEmbedTextsWritesVectorsInInputOrder(t *testing.T) {
	embedder := &fakeEmbedder{}
	stubTopicsEmbedder(t, embedder)
	t.Cleanup(func() {
		// Definitions are queries: the passages they look for are documents.
		if len(embedder.prefixes) != 1 || embedder.prefixes[0] != topics.DefinitionPrefix {
			t.Errorf("embed-texts prompts: %q", embedder.prefixes)
		}
	})
	root := t.TempDir()
	m := fakeInstallShipped(t, root, DefaultEmbeddingModelID)
	in := writeTopicsInput(t, `{"id":"budget","text":"Planning the yearly budget."}`, "", `{"id":"hiring","text":"Interviews and new hires."}`)
	out := filepath.Join(t.TempDir(), "vectors.jsonl")
	code, stdout, stderr := runEmbedTexts(root, "--in", in, "--out", out)
	if code != 0 {
		t.Fatalf("code %d: %s", code, stderr)
	}
	if stdout != "{\"locked\":true}\n{\"count\":2}\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	got := readTopicsVectors(t, out)
	want := topicsVectorsHeader{Format: topicsVectorsFormat, ModelRevision: m.ID + "@" + m.Revision, RecipeVersion: topics.EmbedRecipeVersion, ChunkerVersion: topics.ChunkerVersion, Dimensions: 768, Count: 2}
	if got.header != want {
		t.Fatalf("header = %+v, want %+v", got.header, want)
	}
	if len(got.lines) != 2 || got.lines[0].ID != "budget" || got.lines[1].ID != "hiring" || got.lines[1].TokenCount != 11 || len(got.lines[0].Vector) != 768 {
		t.Fatalf("lines = %+v", got.lines)
	}
}

func TestTopicsEmbedTextsExitCodes(t *testing.T) {
	root := t.TempDir()
	fakeInstallShipped(t, root, DefaultEmbeddingModelID)
	good := writeTopicsInput(t, `{"id":"a","text":"one"}`, `{"id":"b","text":"two"}`)
	halfNorm := func(int) []float32 { v := make([]float32, 768); v[0] = 0.5; return v }
	nan := func(int) []float32 { v := make([]float32, 768); v[0] = float32(math.NaN()); return v }
	short := func(int) []float32 { v := make([]float32, 767); v[0] = 1; return v }
	cases := []struct {
		name     string
		embedder *fakeEmbedder
		input    string
		root     string
		want     int
	}{
		{"not unit norm", &fakeEmbedder{vector: halfNorm}, good, root, topicsExitModelFault},
		{"not finite", &fakeEmbedder{vector: nan}, good, root, topicsExitModelFault},
		{"767 dimensions", &fakeEmbedder{vector: short}, good, root, topicsExitModelFault},
		{"guard in the runtime", &fakeEmbedder{err: fmt.Errorf("%w: input 0", topics.ErrModelFault)}, good, root, topicsExitModelFault},
		{"text over the cap in the runtime", &fakeEmbedder{err: fmt.Errorf("%w: text 1", topics.ErrTooLong)}, good, root, topicsExitUsage},
		{"runtime error", &fakeEmbedder{err: fmt.Errorf("boom")}, good, root, topicsExitRuntime},
		{"model not installed", &fakeEmbedder{}, good, t.TempDir(), topicsExitUnavailable},
		{"repeated id", &fakeEmbedder{}, writeTopicsInput(t, `{"id":"a","text":"one"}`, `{"id":"a","text":"two"}`), root, topicsExitUsage},
		{"empty text", &fakeEmbedder{}, writeTopicsInput(t, `{"id":"a","text":"  "}`), root, topicsExitUsage},
		{"unknown field", &fakeEmbedder{}, writeTopicsInput(t, `{"id":"a","text":"one","lang":"en"}`), root, topicsExitUsage},
		{"no texts", &fakeEmbedder{}, writeTopicsInput(t, ""), root, topicsExitUsage},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubTopicsEmbedder(t, c.embedder)
			out := filepath.Join(t.TempDir(), "vectors.jsonl")
			code, _, stderr := runEmbedTexts(c.root, "--in", c.input, "--out", out)
			if code != c.want {
				t.Fatalf("code %d, want %d: %s", code, c.want, stderr)
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatalf("a failed run left %s (%v)", out, err)
			}
		})
	}
	stubTopicsEmbedder(t, &fakeEmbedder{})
	topicsRuntimeCheckFn = func() error { return topics.ErrUnsupportedRuntime }
	if code, _, _ := runEmbedTexts(root, "--in", good, "--out", filepath.Join(t.TempDir(), "v")); code != topicsExitUnavailable {
		t.Fatalf("unsupported runtime: code %d", code)
	}
	if code, _, _ := runEmbedTexts(root, "--in", good); code != topicsExitUsage {
		t.Fatalf("no --out: code %d", code)
	}
	topicsRuntimeCheckFn = func() error { return nil }
	openTopicsEmbedderFn = func(modelstore.EmbeddingFiles, topics.Options) (textEmbedder, error) {
		return nil, fmt.Errorf("load model_quantized.onnx: protobuf parsing failed")
	}
	if code, _, _ := runEmbedTexts(root, "--in", good, "--out", filepath.Join(t.TempDir(), "v")); code != topicsExitUnavailable {
		t.Fatalf("model files that do not load: code %d, want unavailable", code)
	}
}

// What would only fail after the model ran is refused first, without taking
// the inference lock or loading the model: a text over the token cap and an
// output directory that does not exist.
func TestTopicsEmbedTextsRefusesBeforeTheLock(t *testing.T) {
	root := t.TempDir()
	fakeInstallShipped(t, root, DefaultEmbeddingModelID)
	stubTopicsTokenizer(t)
	prevCheck, prevOpen := topicsRuntimeCheckFn, openTopicsEmbedderFn
	t.Cleanup(func() { topicsRuntimeCheckFn, openTopicsEmbedderFn = prevCheck, prevOpen })
	topicsRuntimeCheckFn = func() error { return nil }
	openTopicsEmbedderFn = func(modelstore.EmbeddingFiles, topics.Options) (textEmbedder, error) {
		t.Fatal("loaded the model for a request it should have refused")
		return nil, nil
	}
	// The lock is held: a refusal that waited for it would hang here.
	unlock, err := transcribe.LockModelRuntime(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	long := writeTopicsInput(t, `{"id":"a","text":"one"}`, fmt.Sprintf(`{"id":"b","text":%q}`, strings.Repeat("x", topics.MaxTokens+1)))
	code, stdout, stderr := runEmbedTexts(root, "--in", long, "--out", filepath.Join(t.TempDir(), "v"))
	if code != topicsExitUsage || !strings.Contains(stderr, `"b"`) || strings.Contains(stdout, "locked") {
		t.Fatalf("over the cap: code %d stdout %q stderr %q", code, stdout, stderr)
	}
	good := writeTopicsInput(t, `{"id":"a","text":"one"}`)
	code, stdout, stderr = runEmbedTexts(root, "--in", good, "--out", filepath.Join(t.TempDir(), "missing", "v"))
	if code != topicsExitUsage || !strings.Contains(stderr, "--out") || strings.Contains(stdout, "locked") {
		t.Fatalf("unwritable output: code %d stdout %q stderr %q", code, stdout, stderr)
	}
}

func TestTopicsEmbedTextsClampsThreads(t *testing.T) {
	root := t.TempDir()
	fakeInstallShipped(t, root, DefaultEmbeddingModelID)
	stubTopicsTokenizer(t)
	var got []int
	prevCheck, prevOpen := topicsRuntimeCheckFn, openTopicsEmbedderFn
	t.Cleanup(func() { topicsRuntimeCheckFn, openTopicsEmbedderFn = prevCheck, prevOpen })
	topicsRuntimeCheckFn = func() error { return nil }
	openTopicsEmbedderFn = func(_ modelstore.EmbeddingFiles, opts topics.Options) (textEmbedder, error) {
		got = append(got, opts.Threads)
		return &fakeEmbedder{}, nil
	}
	in := writeTopicsInput(t, `{"id":"a","text":"one"}`)
	// A negative value is clamped too; only 0 means the environment.
	t.Setenv(topics.ThreadsEnv, "16")
	for _, threads := range []string{"100", "3", "-1"} {
		if code, _, stderr := runEmbedTexts(root, "--in", in, "--out", filepath.Join(t.TempDir(), "v"), "--threads", threads); code != 0 {
			t.Fatalf("--threads %s: code %d: %s", threads, code, stderr)
		}
	}
	if fmt.Sprint(got) != "[16 3 1]" {
		t.Fatalf("threads %v, want [16 3 1]", got)
	}
}

// A second embed waits for the first: both take the model store's inference
// lock, as builds, probes and diarization do.
func TestTopicsEmbedTextsWaitsForTheInferenceLock(t *testing.T) {
	first := &fakeEmbedder{started: make(chan struct{}), release: make(chan struct{})}
	root := t.TempDir()
	fakeInstallShipped(t, root, DefaultEmbeddingModelID)
	stubTopicsTokenizer(t)
	var mu sync.Mutex
	opened := 0
	prevCheck, prevOpen := topicsRuntimeCheckFn, openTopicsEmbedderFn
	topicsRuntimeCheckFn = func() error { return nil }
	openTopicsEmbedderFn = func(modelstore.EmbeddingFiles, topics.Options) (textEmbedder, error) {
		mu.Lock()
		defer mu.Unlock()
		opened++
		if opened == 1 {
			return first, nil
		}
		return &fakeEmbedder{}, nil
	}
	t.Cleanup(func() { topicsRuntimeCheckFn, openTopicsEmbedderFn = prevCheck, prevOpen })
	openedCount := func() int { mu.Lock(); defer mu.Unlock(); return opened }
	in := writeTopicsInput(t, `{"id":"a","text":"one"}`)
	// A run whose context is already given up takes the lock only if it is
	// free: the lock tries once before it looks at the context. So it fails
	// exactly when someone else holds the lock, and never needs a timer.
	givenUp := func(name string) (int, string, string, string) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		out := filepath.Join(t.TempDir(), name)
		var stdout, stderr bytes.Buffer
		code := Run(ctx, []string{"topics", "embed-texts", "--cache-root", root, "--in", in, "--out", out}, &stdout, &stderr)
		return code, stdout.String(), stderr.String(), out
	}
	assertWaited := func(holder string) {
		t.Helper()
		before := openedCount()
		code, stdout, stderr, out := givenUp("waited")
		if code != topicsExitRuntime || !strings.Contains(stderr, "model runtime lock") {
			t.Fatalf("while %s held the lock: exit %d stderr %q, want a runtime failure waiting for it", holder, code, stderr)
		}
		if strings.Contains(stdout, "locked") || openedCount() != before {
			t.Fatalf("while %s held the lock the embed loaded the model: stdout %q", holder, stdout)
		}
		assertNotExist(t, out)
	}

	// Another embed holds the lock while it runs.
	done := make(chan int, 1)
	go func() {
		code, _, _ := runEmbedTexts(root, "--in", in, "--out", filepath.Join(t.TempDir(), "1"))
		done <- code
	}()
	<-first.started
	assertWaited("another embed")
	close(first.release)
	if code := <-done; code != 0 {
		t.Fatalf("first embed: code %d", code)
	}
	// Free again: the same given-up run now goes through, so the failure
	// above was the lock and not the cancelled context.
	if code, stdout, stderr, out := givenUp("free"); code != 0 || !strings.Contains(stdout, `{"locked":true}`) {
		t.Fatalf("with the lock free: exit %d stdout %q stderr %q", code, stdout, stderr)
	} else if _, err := os.Stat(out); err != nil {
		t.Fatal(err)
	}

	// A build, a model check or a diarization holding it keeps it waiting too.
	unlock, err := transcribe.LockModelRuntime(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	assertWaited("a build")
	unlock()
}

// The real model, from a directory of its files (CASSINI_TOPICS_MODEL_DIR).
func TestTopicsEmbedTextsWithTheModel(t *testing.T) {
	dir := os.Getenv("CASSINI_TOPICS_MODEL_DIR")
	if dir == "" {
		t.Skip("CASSINI_TOPICS_MODEL_DIR is not set")
	}
	in := writeTopicsInput(t,
		`{"id":"en","text":"The team reviewed the release schedule."}`,
		`{"id":"it","text":"La squadra ha rivisto il calendario dei rilasci."}`,
		`{"id":"unrelated","text":"A recipe for bread with flour, water and salt."}`)
	out := filepath.Join(t.TempDir(), "vectors.jsonl")
	code, _, stderr := runEmbedTexts(t.TempDir(), "--in", in, "--out", out, "--model-dir", dir, "--threads", "2")
	if code != 0 {
		t.Fatalf("code %d: %s", code, stderr)
	}
	got := readTopicsVectors(t, out)
	if got.header.ModelRevision != DefaultEmbeddingModelID+"@"+topics.ShippedModelRevision || len(got.lines) != 3 {
		t.Fatalf("header = %+v, %d lines", got.header, len(got.lines))
	}
	for _, line := range got.lines {
		if err := topics.CheckVector(line.Vector); err != nil {
			t.Fatalf("%s: %v", line.ID, err)
		}
	}
	same := topics.Cosine(got.lines[0].Vector, got.lines[1].Vector)
	other := topics.Cosine(got.lines[0].Vector, got.lines[2].Vector)
	if same <= other {
		t.Fatalf("translation cosine %.3f is not above the unrelated text's %.3f", same, other)
	}
}

func TestTopicsVersionsNamesTheInstalledModelWithoutLoadingIt(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := runTopics(context.Background(), []string{"versions", "--cache-root", root}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
	want := fmt.Sprintf(`{"chunkerVersion":%q,"detectorVersion":%q,"modelRevision":"","recipeVersion":%q}`+"\n", topics.ChunkerVersion, topics.DetectorVersion, topics.EmbedRecipeVersion)
	if stdout.String() != want {
		t.Fatalf("without a model: %s", stdout.String())
	}
	m := fakeInstallShipped(t, root, DefaultEmbeddingModelID)
	stdout.Reset()
	if code := runTopics(context.Background(), []string{"versions", "--cache-root", root}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
	var got map[string]string
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil || got["modelRevision"] != m.ID+"@"+m.Revision {
		t.Fatalf("installed: %s %v", stdout.String(), err)
	}
}

// With --skip-too-long, a text over the cap is named on stdout and left out;
// the others are embedded. When none is left, nothing is locked or loaded.
func TestTopicsEmbedTextsSkipsTextsOverTheCap(t *testing.T) {
	root := t.TempDir()
	fakeInstallShipped(t, root, DefaultEmbeddingModelID)
	stubTopicsEmbedder(t, &fakeEmbedder{})
	stubTopicsTokenizer(t)
	long := fmt.Sprintf(`{"id":"b","text":%q}`, strings.Repeat("x", topics.MaxTokens+1))
	out := filepath.Join(t.TempDir(), "vectors.jsonl")
	code, stdout, stderr := runEmbedTexts(root, "--in", writeTopicsInput(t, `{"id":"a","text":"one"}`, long), "--out", out, "--skip-too-long")
	if code != 0 || !strings.Contains(stdout, `{"tokens":`) || !strings.Contains(stdout, `"tooLong":"b"`) || !strings.Contains(stdout, `{"count":1}`) {
		t.Fatalf("code %d stdout %q stderr %q", code, stdout, stderr)
	}
	if got := readTopicsVectors(t, out); got.header.Count != 1 || len(got.lines) != 1 || got.lines[0].ID != "a" {
		t.Fatalf("vectors %+v", got)
	}

	openTopicsEmbedderFn = func(modelstore.EmbeddingFiles, topics.Options) (textEmbedder, error) {
		t.Fatal("loaded the model with nothing to embed")
		return nil, nil
	}
	unlock, err := transcribe.LockModelRuntime(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	code, stdout, stderr = runEmbedTexts(root, "--in", writeTopicsInput(t, long), "--out", out, "--skip-too-long")
	if code != 0 || strings.Contains(stdout, "locked") || !strings.Contains(stdout, `{"count":0}`) {
		t.Fatalf("all over the cap: code %d stdout %q stderr %q", code, stdout, stderr)
	}
	if got := readTopicsVectors(t, out); got.header.Count != 0 || len(got.lines) != 0 {
		t.Fatalf("vectors %+v", got)
	}
}
