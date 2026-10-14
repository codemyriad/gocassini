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

	"gocassini/internal/inspect"
	"gocassini/internal/topics"
	"gocassini/internal/transcribe"
)

// topicsExitTranscript: the meeting has no transcript to embed, or none with
// words, or one whose bytes do not match their digest. Stderr starts with the
// reason: no-transcript, transcript-has-no-words or transcript-integrity.
const topicsExitTranscript = 5

// topicsPassagesFormat names the `cassini topics embed` output file.
const topicsPassagesFormat = "cassini.topic-passages.v1"

// topicsPassagesHeader is the first line of the output. No transcript text
// leaves the CLI: passages are time ranges, counts and vectors.
type topicsPassagesHeader struct {
	Format             string  `json:"format"`
	EmbedKey           string  `json:"embedKey"`
	PayloadSHA256      string  `json:"payloadSha256"`
	ModelRevision      string  `json:"modelRevision"`
	ChunkerVersion     string  `json:"chunkerVersion"`
	RecipeVersion      string  `json:"recipeVersion"`
	Dimensions         int     `json:"dimensions"`
	Count              int     `json:"count"`
	Language           string  `json:"language"`
	LanguageConfidence float64 `json:"languageConfidence"`
	DetectorVersion    string  `json:"detectorVersion"`
	// RecordedLanguage is the language the transcript itself records, when
	// it records one. It is reported, never used instead of Language.
	RecordedLanguage string `json:"recordedLanguage,omitempty"`
	// TruncatedNone says no passage was cut: over-long text is split.
	TruncatedNone bool `json:"truncatedNone"`
}

type topicsPassageLine struct {
	Ordinal    int       `json:"ordinal"`
	StartMS    int64     `json:"startMs"`
	EndMS      int64     `json:"endMs"`
	WordCount  int       `json:"wordCount"`
	TokenCount int       `json:"tokenCount"`
	Boundary   string    `json:"boundary"`
	Vector     []float32 `json:"vector"`
}

func runTopicsEmbed(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cassini topics embed", flag.ContinueOnError)
	fs.SetOutput(stderr)
	input := fs.String("input", "", "a delivered .opus or a .meeting bundle directory")
	wordsFile := fs.String("words", "", "a JSON word list instead of --input (development and evaluation)")
	knownKey := fs.String("known-key", "", "the embed key of the vectors the caller already holds")
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
		fmt.Fprintf(stderr, "cassini topics embed: "+format+"\n", a...)
		return code
	}
	if (*input == "") == (*wordsFile == "") || *out == "" {
		return fail(topicsExitUsage, "one of --input or --words, and --out, are required")
	}
	var transcript topicsTranscript
	var err error
	if *input != "" {
		transcript, err = readTopicsTranscript(*input)
	} else {
		transcript, err = readTopicsWordsFile(*wordsFile)
	}
	if err != nil {
		if reason := topicsTranscriptReason(err); reason != "" {
			return fail(topicsExitTranscript, "%s: %v", reason, err)
		}
		// Could not read it (I/O, a tool failing): worth retrying.
		return fail(topicsExitRuntime, "%v", err)
	}
	if !topicsHasWords(transcript.Words) {
		return fail(topicsExitTranscript, "transcript-has-no-words: the default transcript has no words")
	}
	files, revision, err := resolveTopicsModel(*root, *modelDir)
	if err != nil {
		return fail(topicsExitUnavailable, "%v", err)
	}
	key := topics.EmbedKey(transcript.PayloadSHA256, revision)
	enc := json.NewEncoder(stdout)
	// The caller already holds these vectors: nothing to load or lock.
	if *knownKey != "" && *knownKey == key {
		_ = enc.Encode(map[string]any{"unchanged": true, "embedKey": key})
		return 0
	}
	if err := topicsRuntimeCheckFn(); err != nil {
		return fail(topicsExitUnavailable, "%v", err)
	}
	tok, err := loadTopicsTokenizerFn(files.Tokenizer)
	if err != nil {
		return fail(topicsExitUnavailable, "%v", err)
	}
	passages := topics.Chunk(transcript.Words, tok)
	if len(passages) == 0 {
		return fail(topicsExitTranscript, "transcript-has-no-words: the default transcript has no words")
	}
	language := topics.DetectLanguage(passages)
	if err := checkWritableDir(filepath.Dir(*out)); err != nil {
		return fail(topicsExitUsage, "--out: %v", err)
	}
	n := topics.ClampThreads(*threads)
	if *threads <= 0 {
		var warn error
		n, warn = topics.Threads(transcribe.DefaultNumThreads())
		if warn != nil {
			fmt.Fprintf(stderr, "cassini topics embed: %v\n", warn)
		}
	}
	unlock, err := lockModelRuntimeFn(ctx, *root)
	if err != nil {
		return fail(topicsExitRuntime, "model runtime lock: %v", err)
	}
	defer unlock()
	// The operator arms its timeout from here: waiting for a build is not
	// embedding.
	_ = enc.Encode(map[string]bool{"locked": true})
	embedder, err := openTopicsEmbedderFn(files, topics.Options{Threads: n, Batch: true})
	if err != nil {
		return fail(topicsExitUnavailable, "%v", err)
	}
	defer embedder.Close()
	texts := make([]string, len(passages))
	for i, p := range passages {
		texts[i] = p.Text
	}
	embeddings, err := embedder.EmbedTexts(ctx, texts)
	switch {
	case errors.Is(err, topics.ErrModelFault), errors.Is(err, topics.ErrTooLong):
		// The chunker keeps every passage within the cap, so a refusal here
		// is the runtime disagreeing with it: a fault, not bad input.
		return fail(topicsExitModelFault, "%v", err)
	case err != nil:
		return fail(topicsExitRuntime, "%v", err)
	}
	header := topicsPassagesHeader{
		Format: topicsPassagesFormat, EmbedKey: key, PayloadSHA256: transcript.PayloadSHA256,
		ModelRevision: revision, ChunkerVersion: topics.ChunkerVersion, RecipeVersion: topics.EmbedRecipeVersion,
		Dimensions: topics.Dimensions, Count: len(passages),
		Language: language.Code, LanguageConfidence: language.Confidence, DetectorVersion: topics.DetectorVersion,
		RecordedLanguage: transcript.Language, TruncatedNone: true,
	}
	if err := writeTopicsPassages(*out, header, passages, embeddings); err != nil {
		if errors.Is(err, topics.ErrModelFault) {
			return fail(topicsExitModelFault, "%v", err)
		}
		return fail(topicsExitRuntime, "write %s: %v", *out, err)
	}
	_ = enc.Encode(map[string]int{"count": len(passages)})
	return 0
}

// topicsTranscriptReason names a transcript that will never embed, with the
// reasons search indexing uses: the meeting has none, or its body does not
// match its manifest. Anything else ("" here) may succeed on a retry.
func topicsTranscriptReason(err error) string {
	switch {
	case errors.Is(err, inspect.ErrTranscriptIntegrity):
		return "transcript-integrity"
	case errors.Is(err, inspect.ErrNoDefaultTranscript), errors.Is(err, inspect.ErrNotPortableMeeting), errors.Is(err, errTopicsWordsFile):
		return "no-transcript"
	}
	return ""
}

func topicsHasWords(words []topics.Word) bool {
	for _, w := range words {
		if strings.TrimSpace(w.Text) != "" {
			return true
		}
	}
	return false
}

// errTopicsWordsFile marks a --words file that is not a word list.
var errTopicsWordsFile = errors.New("not a word list")

// topicsWordsFile is the --words input: a plain word list, for evaluating the
// method on transcripts that are not packed meetings. Its payload digest is
// the file's sha256.
type topicsWordsFile struct {
	Language string `json:"language,omitempty"`
	Words    []struct {
		Text    string `json:"text"`
		StartMS int64  `json:"startMs"`
		EndMS   int64  `json:"endMs"`
		Speaker string `json:"speaker"`
	} `json:"words"`
}

func readTopicsWordsFile(path string) (topicsTranscript, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return topicsTranscript{}, err
	}
	var file topicsWordsFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return topicsTranscript{}, fmt.Errorf("%s: %w: %v", path, errTopicsWordsFile, err)
	}
	sum := sha256.Sum256(raw)
	out := topicsTranscript{PayloadSHA256: hex.EncodeToString(sum[:]), Language: file.Language}
	for _, w := range file.Words {
		out.Words = append(out.Words, topics.Word{Text: w.Text, StartMS: w.StartMS, EndMS: w.EndMS, Speaker: w.Speaker})
	}
	return out, nil
}

// writeTopicsPassages writes the output whole or not at all, after checking
// every vector.
func writeTopicsPassages(path string, header topicsPassagesHeader, passages []topics.Passage, embeddings []topics.Embedding) error {
	if len(embeddings) != len(passages) {
		return fmt.Errorf("%w: %d vectors for %d passages", topics.ErrModelFault, len(embeddings), len(passages))
	}
	for i := range embeddings {
		if err := topics.CheckVector(embeddings[i].Vector); err != nil {
			return fmt.Errorf("%w: passage %d: %v", topics.ErrModelFault, i, err)
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".topic-passages-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	w := bufio.NewWriter(tmp)
	enc := json.NewEncoder(w)
	err = enc.Encode(header)
	for i := 0; err == nil && i < len(passages); i++ {
		p := passages[i]
		err = enc.Encode(topicsPassageLine{Ordinal: p.Ordinal, StartMS: p.StartMS, EndMS: p.EndMS, WordCount: p.WordCount, TokenCount: p.TokenCount, Boundary: p.Boundary, Vector: embeddings[i].Vector})
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
