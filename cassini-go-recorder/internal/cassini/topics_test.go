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
	"time"

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
}

func (f *fakeEmbedder) EmbedTexts(ctx context.Context, texts []string) ([]topics.Embedding, error) {
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
	prevCheck, prevOpen := topicsRuntimeCheckFn, openTopicsEmbedderFn
	topicsRuntimeCheckFn = func() error { return nil }
	openTopicsEmbedderFn = func(modelstore.EmbeddingFiles, topics.Options) (textEmbedder, error) { return embedder, nil }
	t.Cleanup(func() { topicsRuntimeCheckFn, openTopicsEmbedderFn = prevCheck, prevOpen })
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
	stubTopicsEmbedder(t, &fakeEmbedder{})
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
	want := topicsVectorsHeader{Format: topicsVectorsFormat, ModelRevision: m.ID + "@" + m.Revision, RecipeVersion: topics.EmbedRecipeVersion, Dimensions: 768, Count: 2}
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
		{"text over the cap", &fakeEmbedder{err: fmt.Errorf("%w: text 1", topics.ErrTooLong)}, good, root, topicsExitUsage},
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
}

// A second embed waits for the first: both take the model store's inference
// lock, as builds, probes and diarization do.
func TestTopicsEmbedTextsWaitsForTheInferenceLock(t *testing.T) {
	first := &fakeEmbedder{started: make(chan struct{}), release: make(chan struct{})}
	second := &fakeEmbedder{started: make(chan struct{})}
	root := t.TempDir()
	fakeInstallShipped(t, root, DefaultEmbeddingModelID)
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
		return second, nil
	}
	t.Cleanup(func() { topicsRuntimeCheckFn, openTopicsEmbedderFn = prevCheck, prevOpen })
	in := writeTopicsInput(t, `{"id":"a","text":"one"}`)
	done := make(chan int, 2)
	go func() { code, _, _ := runEmbedTexts(root, "--in", in, "--out", filepath.Join(t.TempDir(), "1")); done <- code }()
	<-first.started
	go func() { code, _, _ := runEmbedTexts(root, "--in", in, "--out", filepath.Join(t.TempDir(), "2")); done <- code }()
	select {
	case <-second.started:
		t.Fatal("the second embed ran while the first held the lock")
	case <-time.After(400 * time.Millisecond):
	}
	close(first.release)
	for range 2 {
		if code := <-done; code != 0 {
			t.Fatalf("code %d", code)
		}
	}
	select {
	case <-second.started:
	default:
		t.Fatal("the second embed never ran")
	}
	// And a build holding the lock keeps it waiting too.
	unlock, err := transcribe.LockModelRuntime(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	third := &fakeEmbedder{started: make(chan struct{})}
	stubTopicsEmbedder(t, third)
	go func() { code, _, _ := runEmbedTexts(root, "--in", in, "--out", filepath.Join(t.TempDir(), "3")); done <- code }()
	select {
	case <-third.started:
		t.Fatal("embed ran while a build held the lock")
	case <-time.After(400 * time.Millisecond):
	}
	unlock()
	if code := <-done; code != 0 {
		t.Fatalf("code %d", code)
	}
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
