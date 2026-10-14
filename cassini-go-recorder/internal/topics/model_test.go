package topics

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gocassini/internal/modelstore"
)

// udhrArticle1 is Article 1 of the Universal Declaration of Human Rights, a
// public text, in a few languages (Japanese with spaces between words, as a
// transcript has them).
var udhrArticle1 = map[string]string{
	"en": "All human beings are born free and equal in dignity and rights. They are endowed with reason and conscience and should act towards one another in a spirit of brotherhood.",
	"it": "Tutti gli esseri umani nascono liberi ed eguali in dignità e diritti. Essi sono dotati di ragione e di coscienza e devono agire gli uni verso gli altri in spirito di fratellanza.",
	"de": "Alle Menschen sind frei und gleich an Würde und Rechten geboren. Sie sind mit Vernunft und Gewissen begabt und sollen einander im Geist der Brüderlichkeit begegnen.",
	"fr": "Tous les êtres humains naissent libres et égaux en dignité et en droits. Ils sont doués de raison et de conscience et doivent agir les uns envers les autres dans un esprit de fraternité.",
	"es": "Todos los seres humanos nacen libres e iguales en dignidad y derechos y, dotados como están de razón y conciencia, deben comportarse fraternalmente los unos con los otros.",
	"ru": "Все люди рождаются свободными и равными в своем достоинстве и правах. Они наделены разумом и совестью и должны поступать в отношении друг друга в духе братства.",
	"ja": "すべての 人間 は 、 生まれ ながら に して 自由 で あり 、 かつ 、 尊厳 と 権利 と に ついて 平等 で ある 。 人間 は 、 理性 と 良心 と を 授け られて おり 、 互い に 同胞 の 精神 を もって 行動 しなければ ならない 。",
}

// The tests in this file need the real model. Point CASSINI_TOPICS_MODEL_DIR
// at a directory holding the decompressed model_quantized.onnx,
// model_quantized.onnx_data and tokenizer.model (for example an installed
// models/embeddinggemma-2-text-int8/<revision> directory); without it they
// skip. The reference data in testdata/ comes from testdata/reference/gen.py,
// which runs the PyTorch model on public and synthetic text only.
const modelDirEnv = "CASSINI_TOPICS_MODEL_DIR"

func modelFiles(t *testing.T) modelstore.EmbeddingFiles {
	t.Helper()
	dir := os.Getenv(modelDirEnv)
	if dir == "" {
		t.Skipf("%s is not set; skipping a test that needs the EmbeddingGemma 2 files", modelDirEnv)
	}
	files := modelstore.EmbeddingFiles{
		Graph:     filepath.Join(dir, "model_quantized.onnx"),
		Data:      filepath.Join(dir, "model_quantized.onnx_data"),
		Tokenizer: filepath.Join(dir, modelstore.EmbeddingTokenizerFile),
	}
	for _, path := range []string{files.Graph, files.Data, files.Tokenizer} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s: %v", modelDirEnv, err)
		}
	}
	return files
}

func openEmbedder(t *testing.T, batch bool) *Embedder {
	t.Helper()
	files := modelFiles(t)
	if err := RuntimeCheck(); err != nil {
		t.Fatalf("ONNX Runtime: %v", err)
	}
	e, err := Open(files, Options{Threads: 4, Batch: batch})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

type referenceVectors struct {
	Prompt  string      `json:"prompt"`
	Texts   []string    `json:"texts"`
	IDs     [][]int64   `json:"ids"`
	Vectors [][]float32 `json:"vectors"`
}

func readReference(t *testing.T, path string) referenceVectors {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ref referenceVectors
	if err := json.Unmarshal(raw, &ref); err != nil {
		t.Fatal(err)
	}
	if ref.Prompt != PromptPrefix || len(ref.Texts) == 0 || len(ref.Texts) != len(ref.Vectors) || len(ref.Texts) != len(ref.IDs) {
		t.Fatalf("%s: malformed reference", path)
	}
	return ref
}

// The Go tokenizer gives the ids the Hugging Face tokenizer gives, prompt,
// BOS and EOS included, on public multilingual text and synthetic edge cases.
func TestTokenizerMatchesHuggingFace(t *testing.T) {
	files := modelFiles(t)
	tok, err := LoadTokenizer(files.Tokenizer)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/tokenizer_parity.json")
	if err != nil {
		t.Fatal(err)
	}
	var parity struct {
		Prompt string `json:"prompt"`
		Cases  []struct {
			Name string  `json:"name"`
			Text string  `json:"text"`
			IDs  []int64 `json:"ids"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &parity); err != nil {
		t.Fatal(err)
	}
	if parity.Prompt != PromptPrefix || len(parity.Cases) < 40 {
		t.Fatalf("parity file: prompt %q, %d cases", parity.Prompt, len(parity.Cases))
	}
	for _, c := range parity.Cases {
		if got := tok.Encode(c.Text); !slices.Equal(got, c.IDs) {
			t.Errorf("%s: %d ids, Hugging Face gives %d\n go: %v\n hf: %v", c.Name, len(got), len(c.IDs), got, c.IDs)
		}
	}
}

// Text that spells a control token stays text. The Hugging Face tokenizer
// turns a literal "<eos>" inside a passage into the EOS token; this one
// encodes the characters, so a transcript can never end its own input early.
// It is the one known difference from the reference, and only for these four
// strings.
func TestControlTokenTextStaysText(t *testing.T) {
	files := modelFiles(t)
	tok, err := LoadTokenizer(files.Tokenizer)
	if err != nil {
		t.Fatal(err)
	}
	ids := tok.Encode("<bos> <eos> <pad> <unk>")
	if ids[0] != bosID || ids[len(ids)-1] != eosID {
		t.Fatalf("ids = %v, want BOS ... EOS", ids)
	}
	for _, id := range ids[1 : len(ids)-1] {
		if id <= 3 {
			t.Fatalf("ids = %v carry control token %d inside the text", ids, id)
		}
	}
}

// The int8 graph run from Go stays within cosine 0.9995 of the PyTorch fp32
// model on public multilingual text.
func TestGoldenVectors(t *testing.T) {
	e := openEmbedder(t, false)
	ref := readReference(t, "testdata/golden_vectors.json")
	got, err := e.EmbedTexts(context.Background(), ref.Texts)
	if err != nil {
		t.Fatal(err)
	}
	for i := range ref.Texts {
		if !slices.Equal(e.Tokenizer().Encode(ref.Texts[i]), ref.IDs[i]) {
			t.Errorf("text %d: ids differ from the reference", i)
		}
		if c := Cosine(got[i].Vector, ref.Vectors[i]); c < ProbeMinCosine {
			t.Errorf("text %d: cosine to the fp32 reference %.6f < %.4f", i, c, ProbeMinCosine)
		}
	}
}

// A padded batch embeds every row to what the row embeds to alone. Batching
// is on only because this holds.
func TestPaddedBatchEqualsSingle(t *testing.T) {
	single := openEmbedder(t, false)
	ref := readReference(t, "testdata/golden_vectors.json")
	texts := append(slices.Clone(ref.Texts), "Short.", "Two words", "A slightly longer sentence about the release plan.")
	// And long texts of mixed lengths up to the cap, so rows are padded by
	// hundreds of tokens: public UDHR sentences, concatenated.
	var udhr []string
	for _, code := range []string{"en", "it", "de", "fr", "es", "ru", "ja"} {
		udhr = append(udhr, udhrArticle1[code])
	}
	for n := 3; n <= 16; n++ {
		var parts []string
		for i := range n {
			parts = append(parts, udhr[i%len(udhr)])
		}
		if text := strings.Join(parts, " "); len(single.Tokenizer().Encode(text)) <= MaxTokens {
			texts = append(texts, text)
		}
	}
	want, err := single.EmbedTexts(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	batched := openEmbedder(t, true)
	if groups := batches(tokenize(batched, texts), true); len(groups) >= len(texts) {
		t.Fatalf("batching formed %d groups for %d texts", len(groups), len(texts))
	}
	got, err := batched.EmbedTexts(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	longest := 0
	for _, w := range want {
		longest = max(longest, w.TokenCount)
	}
	if longest < 400 {
		t.Fatalf("the longest text has %d tokens; the test needs texts near the cap", longest)
	}
	for i := range texts {
		if c := Cosine(got[i].Vector, want[i].Vector); c < 0.99999 {
			t.Errorf("text %d (%d tokens): padded cosine %.7f < 0.99999", i, want[i].TokenCount, c)
		}
	}
}

func tokenize(e *Embedder, texts []string) [][]int64 {
	ids := make([][]int64, len(texts))
	for i, text := range texts {
		ids[i] = e.Tokenizer().Encode(text)
	}
	return ids
}

func TestEmbedTextsRefusesTooLongText(t *testing.T) {
	e := openEmbedder(t, false)
	long := ""
	for range 600 {
		long += "word "
	}
	if _, err := e.EmbedTexts(context.Background(), []string{"fine", long}); err == nil {
		t.Fatal("a text over the token cap was embedded")
	}
}

func TestProbeGolden(t *testing.T) {
	e := openEmbedder(t, false)
	if err := Probe(context.Background(), e, ShippedModelRevision); err != nil {
		t.Fatal(err)
	}
}

// The runtime check `cassini models install|probe` runs, on a model store
// that imported the real files.
func TestProbeInstalledWithTheModel(t *testing.T) {
	files := modelFiles(t)
	s := modelstore.New(t.TempDir())
	m, err := s.Catalogue.Model("embeddinggemma-2-text-int8", ShippedModelRevision)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Import(context.Background(), m, modelstore.ImportOptions{From: filepath.Dir(files.Graph)}); err != nil {
		t.Fatal(err)
	}
	if err := ProbeInstalled(context.Background(), s, m, 2); err != nil {
		t.Fatal(err)
	}
}
