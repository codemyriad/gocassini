package transcribe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseVocabularyNormalisesAndDeduplicates(t *testing.T) {
	got := ParseVocabulary(`["  Nextcloud  ", "nextcloud", "Aire   Spaces", "", "Librocco"]`)
	want := []string{"Nextcloud", "Aire Spaces", "Librocco"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("term %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// A malformed setting must leave transcription exactly as it was. Failing the
// build would turn a typo in a text box into a recording that never publishes.
func TestParseVocabularyIgnoresMalformedInput(t *testing.T) {
	for _, raw := range []string{"", "   ", "not json", `{"a":1}`, `["ok"`} {
		if got := ParseVocabulary(raw); got != nil {
			t.Errorf("ParseVocabulary(%q) = %v, want nil", raw, got)
		}
	}
}

func TestNormalizeVocabularyEnforcesBounds(t *testing.T) {
	long := strings.Repeat("x", maxVocabularyTermRunes+1)
	if got := NormalizeVocabulary([]string{long}); got != nil {
		t.Errorf("an over-long term must be dropped, got %v", got)
	}

	many := make([]string, maxVocabularyTerms+20)
	for i := range many {
		many[i] = string(rune('a'+i%26)) + strings.Repeat("y", i%7+1)
	}
	if got := NormalizeVocabulary(many); len(got) > maxVocabularyTerms {
		t.Errorf("got %d terms, want at most %d", len(got), maxVocabularyTerms)
	}
}

// Speaker labels are appended after the configured terms so an operator's
// explicit spelling wins the case-insensitive de-duplication.
func TestVocabularyForBuildPrefersConfiguredSpelling(t *testing.T) {
	got := vocabularyForBuild(
		[]string{"Silvio Tomatis"},
		[]AudioStream{{SpeakerLabel: "silvio tomatis"}, {SpeakerLabel: "Chris"}},
	)
	if len(got) != 2 || got[0] != "Silvio Tomatis" || got[1] != "Chris" {
		t.Fatalf("got %v, want [Silvio Tomatis Chris]", got)
	}
}

// Beam search is unconditional on a transducer: the decoder must not change
// under the operator depending on whether the vocabulary box happens to be
// empty. An empty vocabulary leaves no hints provenance, because there was
// nothing to apply and nothing to explain.
func TestResolveDecoderWithoutVocabularyStillBeamSearches(t *testing.T) {
	dec, prov, err := resolveDecoder(t.TempDir(), nil, transducerPaths(t))
	if err != nil {
		t.Fatalf("resolveDecoder: %v", err)
	}
	if dec == nil || dec.Method != decodingModifiedBeamSearch {
		t.Fatalf("a transducer must beam-search regardless of vocabulary, got %+v", dec)
	}
	if dec.Biased() {
		t.Error("an empty vocabulary must not produce hotwords")
	}
	if prov != nil {
		t.Errorf("no vocabulary means nothing to explain, got %+v", prov)
	}
}

// A CTC model has no hotword support in sherpa, so it keeps greedy search. The
// wider beam would buy nothing and cost decode time.
func TestResolveDecoderKeepsGreedySearchForCTC(t *testing.T) {
	dir := t.TempDir()
	ctc := ModelPaths{ModelFile: filepath.Join(dir, "model.onnx"), TokensFile: filepath.Join(dir, "tokens.txt")}
	dec, prov, err := resolveDecoder(dir, nil, ctc)
	if err != nil {
		t.Fatalf("resolveDecoder: %v", err)
	}
	if dec == nil || dec.Method != decodingGreedySearch {
		t.Fatalf("CTC must stay on greedy search, got %+v", dec)
	}
	if prov != nil {
		t.Errorf("no vocabulary means nothing to explain, got %+v", prov)
	}
}

// A CTC model cannot be biased. The build must say so rather than decode
// unbiased while the operator believes their vocabulary was applied.
func TestResolveHintsReportsCTCModelAsUnapplied(t *testing.T) {
	dir := t.TempDir()
	ctc := ModelPaths{ModelFile: filepath.Join(dir, "model.onnx"), TokensFile: filepath.Join(dir, "tokens.txt")}
	hints, prov, err := resolveDecoder(dir, []string{"Librocco"}, ctc)
	if err != nil {
		t.Fatalf("resolveDecoder: %v", err)
	}
	if hints.Biased() {
		t.Error("a CTC model must not produce usable hints")
	}
	if prov == nil || prov.Applied {
		t.Fatalf("expected an unapplied provenance record, got %+v", prov)
	}
	if prov.TermCount != 1 || !strings.Contains(prov.Reason, "CTC") || !strings.Contains(prov.Reason, "balanced") {
		t.Errorf("provenance must name the reason, got %+v", prov)
	}
}

func TestResolveHintsWritesHotwordsAndRecordsProvenance(t *testing.T) {
	dir := t.TempDir()
	paths := transducerPaths(t)
	hints, prov, err := resolveDecoder(dir, []string{"Librocco", "Aire Spaces"}, paths)
	if err != nil {
		t.Fatalf("resolveDecoder: %v", err)
	}
	if !hints.Biased() || prov == nil || !prov.Applied {
		t.Fatalf("expected applied hints, got hints=%+v prov=%+v", hints, prov)
	}
	if prov.DecodingMethod != decodingModifiedBeamSearch {
		t.Errorf("hotwords require %s, provenance says %q", decodingModifiedBeamSearch, prov.DecodingMethod)
	}
	if prov.TermCount != 2 || hints.TermCount != 2 {
		t.Errorf("term count mismatch: prov=%d hints=%d", prov.TermCount, hints.TermCount)
	}
	body, err := os.ReadFile(hints.HotwordsFile)
	if err != nil {
		t.Fatalf("read hotwords file: %v", err)
	}
	if got := string(body); got != "Librocco\nAire Spaces\n" {
		t.Errorf("hotwords file = %q, want one verbatim term per line", got)
	}
}

// The kill switch has to leave a trace: a build that ignored the vocabulary
// because an operator disabled biasing must not look like one that applied it.
func TestResolveHintsRecordsTheDisableSwitch(t *testing.T) {
	t.Setenv(envHintsDisabled, "1")
	hints, prov, err := resolveDecoder(t.TempDir(), []string{"Librocco"}, transducerPaths(t))
	if err != nil {
		t.Fatalf("resolveDecoder: %v", err)
	}
	if hints.Biased() {
		t.Error("the disable switch must produce no hints")
	}
	if prov == nil || prov.Applied || !strings.Contains(prov.Reason, envHintsDisabled) {
		t.Fatalf("expected a disabled provenance record naming the env var, got %+v", prov)
	}
}

func TestHintsScoreRejectsUnusableOverrides(t *testing.T) {
	for _, raw := range []string{"", "0", "-1", "not-a-number"} {
		t.Setenv(envHintsScore, raw)
		if got := hintsScore(); got != defaultHotwordsScore {
			t.Errorf("hintsScore() with %q = %v, want the default %v", raw, got, defaultHotwordsScore)
		}
	}
	t.Setenv(envHintsScore, "3.5")
	if got := hintsScore(); got != 3.5 {
		t.Errorf("hintsScore() = %v, want 3.5", got)
	}
}

// A bundle with no BPE vocabulary cannot be biased. The build must say so, and
// name the fix, rather than decode unbiased while the operator believes their
// vocabulary was applied.
func TestResolveHintsReportsMissingBpeVocabAsUnapplied(t *testing.T) {
	dir := t.TempDir()
	paths := ModelPaths{EncoderFile: filepath.Join(dir, "encoder.onnx"), TokensFile: filepath.Join(dir, "tokens.txt")}
	hints, prov, err := resolveDecoder(dir, []string{"Librocco"}, paths)
	if err != nil {
		t.Fatalf("resolveDecoder: %v", err)
	}
	if hints.Biased() {
		t.Error("a bundle without bpe.vocab must not produce usable hints")
	}
	if prov == nil || prov.Applied || !strings.Contains(prov.Reason, "bpe.vocab") {
		t.Fatalf("expected an unapplied record naming bpe.vocab, got %+v", prov)
	}
}

// transducerPaths builds a model layout that can take hints: an encoder plus a
// tokens file and a BPE vocabulary.
func transducerPaths(t *testing.T) ModelPaths {
	t.Helper()
	dir := t.TempDir()
	tokens := filepath.Join(dir, "tokens.txt")
	if err := os.WriteFile(tokens, []byte("<unk> 0\n▁a 1\n"), 0o644); err != nil {
		t.Fatalf("write tokens: %v", err)
	}
	vocab := filepath.Join(dir, "bpe.vocab")
	if err := os.WriteFile(vocab, []byte("<unk>\t0\n"), 0o644); err != nil {
		t.Fatalf("write bpe vocab: %v", err)
	}
	return ModelPaths{EncoderFile: filepath.Join(dir, "encoder.onnx"), TokensFile: tokens, BpeVocabFile: vocab}
}

// sherpa parses a trailing ":n" token as a per-phrase boost, so glossary text
// must not be able to reach that grammar. A term ending in one is dropped: it
// is not a spelling, and honouring it would let the vocabulary silently
// override the score this build recorded in provenance.
func TestNormalizeVocabularyRejectsHotwordScoreSyntax(t *testing.T) {
	got := NormalizeVocabulary([]string{"Alice :100000", ":2", "Librocco", "Aire Spaces"})
	if len(got) != 2 || got[0] != "Librocco" || got[1] != "Aire Spaces" {
		t.Fatalf("got %v, want the two ordinary terms only", got)
	}
}

// The kill switch has to be able to restore the output an operator had before
// this feature existed. Dropping the hotwords but leaving beam search on would
// leave them with a third behaviour and no way back.
func TestHintsDisabledSwitchAlsoRestoresGreedySearch(t *testing.T) {
	t.Setenv(envHintsDisabled, "1")
	dec, prov, err := resolveDecoder(t.TempDir(), []string{"Librocco"}, transducerPaths(t))
	if err != nil {
		t.Fatalf("resolveDecoder: %v", err)
	}
	if dec.Method != decodingGreedySearch {
		t.Errorf("decoder = %q, want the previous greedy search back", dec.Method)
	}
	if dec.Biased() || prov == nil || prov.Applied {
		t.Errorf("hints must be off and recorded as such, got dec=%+v prov=%+v", dec, prov)
	}
}
