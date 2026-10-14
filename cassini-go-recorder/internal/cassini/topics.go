package cassini

import (
	"bufio"
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
	"strings"
	"unicode/utf8"

	"gocassini/internal/modelstore"
	"gocassini/internal/topics"
	"gocassini/internal/transcribe"
)

// `cassini topics` embeds text for topic suggestions
// (docs/proposals/automatic-tagging). embed chunks and embeds a whole meeting
// (topics_embed.go); embed-texts embeds short texts such as topic
// definitions.
//
// The operator runs this binary rather than importing it, so the exit codes,
// the stdout events and the output file are a contract with cassini-operator.
// Change both sides together.

const (
	topicsExitRuntime     = 1
	topicsExitUsage       = 2 // also: an input line that is not valid, or a text over the token cap
	topicsExitUnavailable = 3 // no installed embedding model, or an ONNX Runtime this binary cannot use
	topicsExitModelFault  = 4 // a vector that is not finite, not 768 long or not unit-norm
)

// DefaultEmbeddingModelID is the catalogue embedding model topic suggestions
// use.
const DefaultEmbeddingModelID = "embeddinggemma-2-text-int8"

// topicsVectorsFormat names the embed-texts output file.
const topicsVectorsFormat = "cassini.topic-text-vectors.v1"

// maxTopicsTextBytes bounds one input text before tokenizing; the token cap
// refuses long ones anyway, this only keeps a bad input from costing memory.
const maxTopicsTextBytes = 64 << 10

// textEmbedder is the part of *topics.Embedder the command uses.
type textEmbedder interface {
	EmbedTexts(ctx context.Context, texts []string) ([]topics.Embedding, error)
	Close() error
}

// Seams so the command can be tested without the model.
var (
	loadTopicsTokenizerFn = func(path string) (topics.TokenCounter, error) { return topics.LoadTokenizer(path) }
	topicsRuntimeCheckFn  = topics.RuntimeCheck
	openTopicsEmbedderFn  = func(files modelstore.EmbeddingFiles, opts topics.Options) (textEmbedder, error) {
		return topics.Open(files, opts)
	}
)

func runTopics(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printTopicsUsage(stderr)
		return topicsExitUsage
	}
	switch args[0] {
	case "help", "-h", "--help":
		printTopicsUsage(stdout)
		return 0
	case "embed":
		return runTopicsEmbed(ctx, args[1:], stdout, stderr)
	case "embed-texts":
		return runTopicsEmbedTexts(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown topics subcommand %q\n\n", args[0])
		printTopicsUsage(stderr)
		return topicsExitUsage
	}
}

func printTopicsUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  cassini topics embed (--input <file.opus|bundle.meeting> | --words <words.json>) --out <passages.jsonl>
      [--known-key KEY] [--threads N] [--model-dir DIR]
  cassini topics embed-texts --in <texts.jsonl> --out <vectors.jsonl> [--threads N] [--model-dir DIR]

Embed text for topic suggestions with the embedding model installed in the
model store (Settings, or cassini models install `+DefaultEmbeddingModelID+`).

embed reads the default words transcript of a meeting, splits it into
natural passages (`+topics.ChunkerVersion+`), detects its language and embeds every
passage. Its key is sha256(payload sha256, model revision, chunker, recipe);
with --known-key equal to it, embed prints {"unchanged":true} and stops
before it loads or locks anything. It writes a header line {"format",
"embedKey", "payloadSha256", "modelRevision", "chunkerVersion",
"recipeVersion", "dimensions", "count", "language", "languageConfidence",
"detectorVersion", "truncatedNone"} and then one {"ordinal", "startMs",
"endMs", "wordCount", "tokenCount", "boundary", "vector"} line per passage.
No transcript text is written. --words reads {"words": [{"text", "startMs",
"endMs", "speaker"}]} instead of a meeting, for evaluation.

embed-texts reads one JSON object per line, {"id": "...", "text": "..."}, and
writes a header line {"format", "modelRevision", "recipeVersion",
"chunkerVersion", "dimensions", "count"} and then one {"id", "tokenCount", "vector"} line per
input, in input order. Each vector is 768 float32 values, unit-norm. Text is
never truncated: a text over 512 tokens, the prompt prefix included, is
refused. Once it holds the model store's inference lock it prints
{"locked":true}; at the end {"count":N}.

The model runs on the CPU with --threads threads, else $CASSINI_TOPICS_THREADS,
else min(4, CPUs). --model-dir names a directory with the decompressed model
files instead of the model store, for development.

Exit codes: 0 ok, 1 runtime, 2 usage or a refused input, 3 no usable
embedding model or runtime, 4 model fault, 5 no usable transcript (embed;
stderr names no-transcript, transcript-has-no-words or transcript-integrity).
`)
}

type topicsTextInput struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

func runTopicsEmbedTexts(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cassini topics embed-texts", flag.ContinueOnError)
	fs.SetOutput(stderr)
	in := fs.String("in", "", "input JSON lines, one {\"id\",\"text\"} per line")
	out := fs.String("out", "", "output JSON lines file")
	threads := fs.Int("threads", 0, "CPU threads (default $"+topics.ThreadsEnv+", else min(4, CPUs))")
	modelDir := fs.String("model-dir", "", "directory with the decompressed model files (development)")
	root := fs.String("cache-root", defaultCassiniCacheRoot(), "persistent operator model cache root")
	fs.Usage = func() {
		printTopicsUsage(fs.Output())
		fs.PrintDefaults()
	}
	if _, code := parseAnnotateArgs(fs, args, 0); code >= 0 {
		return code
	}
	fail := func(code int, format string, a ...any) int {
		fmt.Fprintf(stderr, "cassini topics embed-texts: "+format+"\n", a...)
		return code
	}
	if *in == "" || *out == "" {
		return fail(topicsExitUsage, "--in and --out are required")
	}
	inputs, err := readTopicsTextInputs(*in)
	if err != nil {
		return fail(topicsExitUsage, "%v", err)
	}
	files, revision, err := resolveTopicsModel(*root, *modelDir)
	if err != nil {
		return fail(topicsExitUnavailable, "%v", err)
	}
	if err := topicsRuntimeCheckFn(); err != nil {
		return fail(topicsExitUnavailable, "%v", err)
	}
	// Refuse what would fail after the model ran, before waiting for the lock:
	// a text over the token cap, and an output nobody can write.
	tok, err := loadTopicsTokenizerFn(files.Tokenizer)
	if err != nil {
		return fail(topicsExitUnavailable, "%v", err)
	}
	for _, input := range inputs {
		if n := len(tok.Encode(input.Text)); n > topics.MaxTokens {
			return fail(topicsExitUsage, "%v: text %q has %d tokens; the limit is %d", topics.ErrTooLong, input.ID, n, topics.MaxTokens)
		}
	}
	if err := checkWritableDir(filepath.Dir(*out)); err != nil {
		return fail(topicsExitUsage, "--out: %v", err)
	}
	n := topics.ClampThreads(*threads)
	if *threads <= 0 {
		var warn error
		n, warn = topics.Threads(transcribe.DefaultNumThreads())
		if warn != nil {
			fmt.Fprintf(stderr, "cassini topics embed-texts: %v\n", warn)
		}
	}
	// Embedding is inference: it waits for a build, a model check or a
	// diarization using the same store, and they wait for it.
	unlock, err := lockModelRuntimeFn(ctx, *root)
	if err != nil {
		return fail(topicsExitRuntime, "model runtime lock: %v", err)
	}
	defer unlock()
	enc := json.NewEncoder(stdout)
	_ = enc.Encode(map[string]bool{"locked": true})
	embedder, err := openTopicsEmbedderFn(files, topics.Options{Threads: n, Batch: true})
	if err != nil {
		// A runtime that cannot run it, or installed files that do not load:
		// retrying will not help until an administrator acts.
		return fail(topicsExitUnavailable, "%v", err)
	}
	defer embedder.Close()
	texts := make([]string, len(inputs))
	for i, input := range inputs {
		texts[i] = input.Text
	}
	embeddings, err := embedder.EmbedTexts(ctx, texts)
	switch {
	case errors.Is(err, topics.ErrTooLong):
		return fail(topicsExitUsage, "%v", err)
	case errors.Is(err, topics.ErrModelFault):
		return fail(topicsExitModelFault, "%v", err)
	case err != nil:
		return fail(topicsExitRuntime, "%v", err)
	}
	if err := writeTopicsVectors(*out, revision, inputs, embeddings); err != nil {
		if errors.Is(err, topics.ErrModelFault) {
			return fail(topicsExitModelFault, "%v", err)
		}
		return fail(topicsExitRuntime, "write %s: %v", *out, err)
	}
	_ = enc.Encode(map[string]int{"count": len(inputs)})
	return 0
}

// checkWritableDir makes sure the output can be written before an embed
// spends the model's time on it.
func checkWritableDir(dir string) error {
	f, err := os.CreateTemp(dir, ".topic-vectors-check-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

func readTopicsTextInputs(path string) ([]topicsTextInput, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var inputs []topicsTextInput
	seen := map[string]bool{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 4*maxTopicsTextBytes)
	for line := 1; scanner.Scan(); line++ {
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		var input topicsTextInput
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			return nil, fmt.Errorf("%s line %d: %v", path, line, err)
		}
		switch {
		case input.ID == "" || len(input.ID) > 256:
			return nil, fmt.Errorf("%s line %d: id must be 1 to 256 bytes", path, line)
		case seen[input.ID]:
			return nil, fmt.Errorf("%s line %d: id %q repeats", path, line, input.ID)
		case strings.TrimSpace(input.Text) == "":
			return nil, fmt.Errorf("%s line %d: text is empty", path, line)
		case len(input.Text) > maxTopicsTextBytes || !utf8.ValidString(input.Text):
			return nil, fmt.Errorf("%s line %d: text must be valid UTF-8 of at most %d bytes", path, line, maxTopicsTextBytes)
		}
		seen[input.ID] = true
		inputs = append(inputs, input)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	if len(inputs) == 0 {
		return nil, fmt.Errorf("%s has no texts", path)
	}
	return inputs, nil
}

// resolveTopicsModel finds the files to load and the revision to report:
// "<id>@<source_sha256>" for an installed catalogue model. A --model-dir is
// hashed like the catalogue computes a revision, so a copy of the shipped
// files reports the shipped revision and anything else reports "local@...".
func resolveTopicsModel(root, modelDir string) (modelstore.EmbeddingFiles, string, error) {
	s := modelstore.New(root)
	if modelDir == "" {
		m, err := s.Catalogue.Model(DefaultEmbeddingModelID, "")
		if err != nil {
			return modelstore.EmbeddingFiles{}, "", err
		}
		files, err := s.InstalledEmbedder(m)
		if err != nil {
			state := "is not installed"
			if !errors.Is(err, os.ErrNotExist) {
				state = fmt.Sprintf("is not installed intact (%v)", err)
			}
			return modelstore.EmbeddingFiles{}, "", fmt.Errorf("model-unavailable: the topic suggestions model %s %s; an administrator can download it in Cassini's Settings, or run `cassini models install %s` (offline: `cassini models import`)", m.ID, state, m.ID)
		}
		return files, m.ID + "@" + m.Revision, nil
	}
	files := modelstore.EmbeddingFiles{
		Graph:     filepath.Join(modelDir, "model_quantized.onnx"),
		Data:      filepath.Join(modelDir, "model_quantized.onnx_data"),
		Tokenizer: filepath.Join(modelDir, modelstore.EmbeddingTokenizerFile),
	}
	var sums strings.Builder
	for _, path := range []string{files.Graph, files.Data, files.Tokenizer} {
		sum, err := fileSHA256(path)
		if err != nil {
			return modelstore.EmbeddingFiles{}, "", err
		}
		fmt.Fprintf(&sums, "%s  %s\n", sum, filepath.Base(path))
	}
	digest := sha256.Sum256([]byte(sums.String()))
	revision := hex.EncodeToString(digest[:])
	for _, m := range s.Catalogue.Models {
		if m.Kind == modelstore.KindEmbedding && m.Revision == revision {
			return files, m.ID + "@" + revision, nil
		}
	}
	return files, "local@" + revision, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type topicsVectorsHeader struct {
	Format        string `json:"format"`
	ModelRevision string `json:"modelRevision"`
	RecipeVersion string `json:"recipeVersion"`
	// ChunkerVersion is the chunker this binary would use for a meeting, so
	// the operator can tell a chunker change without embedding one.
	ChunkerVersion string `json:"chunkerVersion"`
	Dimensions     int    `json:"dimensions"`
	Count          int    `json:"count"`
}

type topicsVectorLine struct {
	ID         string    `json:"id"`
	TokenCount int       `json:"tokenCount"`
	Vector     []float32 `json:"vector"`
}

// writeTopicsVectors writes the output file whole or not at all: a temporary
// file in the same directory, renamed into place.
func writeTopicsVectors(path, revision string, inputs []topicsTextInput, embeddings []topics.Embedding) error {
	if len(embeddings) != len(inputs) {
		return fmt.Errorf("%w: %d vectors for %d texts", topics.ErrModelFault, len(embeddings), len(inputs))
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".topic-vectors-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	w := bufio.NewWriter(tmp)
	enc := json.NewEncoder(w)
	err = enc.Encode(topicsVectorsHeader{Format: topicsVectorsFormat, ModelRevision: revision, RecipeVersion: topics.EmbedRecipeVersion, ChunkerVersion: topics.ChunkerVersion, Dimensions: topics.Dimensions, Count: len(inputs)})
	for i := 0; err == nil && i < len(inputs); i++ {
		if guard := topics.CheckVector(embeddings[i].Vector); guard != nil {
			err = fmt.Errorf("%w: vector %d: %v", topics.ErrModelFault, i, guard)
			break
		}
		err = enc.Encode(topicsVectorLine{ID: inputs[i].ID, TokenCount: embeddings[i].TokenCount, Vector: embeddings[i].Vector})
	}
	if err == nil {
		err = w.Flush()
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
