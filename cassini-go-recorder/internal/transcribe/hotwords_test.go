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
	if len(got.Terms) != 2 || got.Terms[0] != "Silvio Tomatis" || got.Terms[1] != "Chris" {
		t.Fatalf("got %v, want [Silvio Tomatis Chris]", got)
	}
	if got.ParticipantTermCount != 1 {
		t.Fatalf("participant term count = %d, want 1 (the configured duplicate is not automatic)", got.ParticipantTermCount)
	}
}

// Participant names come from metadata rather than an operator choosing a
// spelling. They therefore carry a smaller phrase-specific score: a score of 2
// made the one-word name "Silvio" repeat hundreds of times on uncertain audio.
func TestResolveBuildVocabularyCapsAutomaticParticipantScore(t *testing.T) {
	dir := t.TempDir()
	vocabulary := vocabularyForBuild(
		[]string{"Librocco"},
		[]AudioStream{{SpeakerLabel: "Silvio"}, {SpeakerLabel: "Chris"}},
	)
	dec, prov, err := resolveDecoderVocabulary(dir, vocabulary, transducerPaths(t))
	if err != nil {
		t.Fatalf("resolveDecoderVocabulary: %v", err)
	}
	body, err := os.ReadFile(dec.HotwordsFile)
	if err != nil {
		t.Fatalf("read hotwords file: %v", err)
	}
	if got := string(body); got != "Librocco\nSilvio :0.5\nChris :0.5\n" {
		t.Fatalf("hotwords file = %q, want configured score implicit and participant score capped", got)
	}
	if prov == nil || !prov.Applied || prov.TermCount != 3 || prov.Score != 2 ||
		prov.ParticipantTermCount != 2 || prov.ParticipantScore != 0.5 || prov.OwnNameExcluded {
		t.Fatalf("provenance does not describe both scores: %+v", prov)
	}
}

func TestAutomaticParticipantScoreHonoursLowerOperatorScore(t *testing.T) {
	t.Setenv(envHintsScore, "0.25")
	vocabulary := vocabularyForBuild(nil, []AudioStream{{SpeakerLabel: "Silvio"}})
	dec, prov, err := resolveDecoderVocabulary(t.TempDir(), vocabulary, transducerPaths(t))
	if err != nil {
		t.Fatalf("resolveDecoderVocabulary: %v", err)
	}
	body, err := os.ReadFile(dec.HotwordsFile)
	if err != nil {
		t.Fatalf("read hotwords file: %v", err)
	}
	if got := string(body); got != "Silvio :0.25\n" {
		t.Fatalf("hotwords file = %q, want the lower operator score", got)
	}
	if prov.ParticipantScore != 0.25 || prov.Score != 0.25 {
		t.Fatalf("provenance scores = base %v, participant %v; want 0.25 for both", prov.Score, prov.ParticipantScore)
	}
}

func TestSpeakerDecodersOmitTheTracksOwnName(t *testing.T) {
	dir := t.TempDir()
	streams := []AudioStream{
		{Index: 1, SpeakerLabel: "Silvio"},
		{Index: 3, SpeakerLabel: "Chris"},
	}
	// Silvio is explicit as well as present in participant metadata. It keeps
	// the operator's stronger score on Chris's track but is still omitted from
	// Silvio's own track.
	vocabulary := vocabularyForBuild([]string{"Librocco", "Silvio"}, streams)
	base, _, err := resolveDecoderVocabulary(dir, vocabulary, transducerPaths(t))
	if err != nil {
		t.Fatalf("resolveDecoderVocabulary: %v", err)
	}
	decoders, err := speakerDecoders(dir, vocabulary, streams, base)
	if err != nil {
		t.Fatalf("speakerDecoders: %v", err)
	}

	body, err := os.ReadFile(decoders[1].HotwordsFile)
	if err != nil {
		t.Fatalf("read Silvio-track hotwords: %v", err)
	}
	if got, want := string(body), "Librocco\nChris :0.5\n"; got != want {
		t.Errorf("Silvio-track hotwords = %q, want %q", got, want)
	}
	body, err = os.ReadFile(decoders[3].HotwordsFile)
	if err != nil {
		t.Fatalf("read Chris-track hotwords: %v", err)
	}
	if got, want := string(body), "Librocco\nSilvio\n"; got != want {
		t.Errorf("Chris-track hotwords = %q, want %q", got, want)
	}

	// With no explicit duplicate, the owner disappears while the other name
	// remains available as the useful spelling hint.
	vocabulary = vocabularyForBuild(nil, streams)
	base, _, err = resolveDecoderVocabulary(dir, vocabulary, transducerPaths(t))
	if err != nil {
		t.Fatalf("resolveDecoderVocabulary without explicit duplicate: %v", err)
	}
	decoders, err = speakerDecoders(dir, vocabulary, streams, base)
	if err != nil {
		t.Fatalf("speakerDecoders without explicit duplicate: %v", err)
	}
	body, err = os.ReadFile(decoders[1].HotwordsFile)
	if err != nil {
		t.Fatalf("read Silvio-track hotwords: %v", err)
	}
	if got := string(body); got != "Chris :0.5\n" {
		t.Fatalf("Silvio-track hotwords = %q, want only the other participant", got)
	}
}

func TestSpeakerDecodersOmitOwnNamesWhenAllWereConfiguredExplicitly(t *testing.T) {
	dir := t.TempDir()
	streams := []AudioStream{{Index: 1, SpeakerLabel: "Silvio"}, {Index: 2, SpeakerLabel: "Chris"}}
	vocabulary := vocabularyForBuild([]string{"Silvio", "Chris", "Librocco"}, streams)
	if vocabulary.ParticipantTermCount != 0 {
		t.Fatalf("participant term count = %d, want every name classified as configured", vocabulary.ParticipantTermCount)
	}
	base, _, err := resolveDecoderVocabulary(dir, vocabulary, transducerPaths(t))
	if err != nil {
		t.Fatalf("resolveDecoderVocabulary: %v", err)
	}
	decoders, err := speakerDecoders(dir, vocabulary, streams, base)
	if err != nil {
		t.Fatalf("speakerDecoders: %v", err)
	}
	for index, want := range map[int]string{1: "Chris\nLibrocco\n", 2: "Silvio\nLibrocco\n"} {
		body, err := os.ReadFile(decoders[index].HotwordsFile)
		if err != nil {
			t.Fatalf("read stream %d hotwords: %v", index, err)
		}
		if got := string(body); got != want {
			t.Errorf("stream %d hotwords = %q, want %q", index, got, want)
		}
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
	if prov.TermCount != 1 || !strings.Contains(prov.Reason, "CTC") || !strings.Contains(prov.Reason, "cannot take decoder hints") {
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
