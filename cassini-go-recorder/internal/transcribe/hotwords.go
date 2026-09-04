package transcribe

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The operator-configured vocabulary ("Participant and project vocabulary")
// biases the decoder towards spellings it would otherwise never produce.
//
// This is contextual biasing, not correction. sherpa-onnx builds a context
// graph from the terms and adds a per-token bonus while the beam search is
// running, so a term is only ever emitted where the acoustics already support
// it: no pass rewrites finished text, and a term nobody said cannot appear.
// That is the whole reason the vocabulary moved here from the deleted LLM
// cleanup step, which could and did invent text.
//
// Two hard requirements come from sherpa-onnx itself
// (offline-recognizer-transducer-nemo-impl.h):
//
//  1. hotwords are read only under decoding_method=modified_beam_search;
//     greedy search ignores the file entirely.
//  2. modeling_unit must be "bpe" AND bpe_vocab must name a real file. With
//     bpe_vocab empty the recognizer fails to construct; with modeling_unit
//     left unset the terms are tokenised as whole words, fail to encode, and
//     the biasing is silently a no-op while everything looks healthy. A model
//     bundle without bpe.vocab therefore cannot take hints at all.
//
// A build that cannot meet both records that in provenance rather than
// pretending the vocabulary was applied.
const (
	// maxVocabularyTerms bounds what an operator can configure. The operator
	// enforces the same limit on the way in; this is the recorder's own guard
	// against a hand-edited environment.
	maxVocabularyTerms = 100
	// maxVocabularyTermRunes bounds a single term.
	maxVocabularyTermRunes = 100

	// defaultHotwordsScore is the per-token bonus added along a hotword path.
	// The bonus fights the acoustic score, so it stays modest: a large boost is
	// how contextual biasing starts inventing its own vocabulary.
	defaultHotwordsScore = 2.0

	// hotwordsMaxActivePaths is the beam width used when hints are on. Beam
	// search is the price of hotwords, and a wider beam costs decode time for
	// diminishing returns.
	hotwordsMaxActivePaths = 4

	// hotwordsFileName is written into the build's working directory, next to
	// the artifacts, so a failed build leaves the exact file behind for
	// inspection.
	hotwordsFileName = "hotwords.txt"

	envHintsDisabled = "CASSINI_STT_HINTS_DISABLED"
	envHintsScore    = "CASSINI_STT_HINTS_SCORE"
)

// DecoderHints is the resolved biasing configuration for one recognizer.
// A nil *DecoderHints means the pass runs unbiased on greedy search.
type DecoderHints struct {
	// File is the path to the hotwords file sherpa-onnx reads.
	File string
	// Score is the per-token bonus.
	Score float32
	// TermCount is how many terms File holds, for provenance.
	TermCount int
	// BpeVocabFile is the vocabulary the terms are encoded with: the bundle's
	// own, or one derived from tokens.txt. Held here rather than read back off
	// ModelPaths so the recognizer uses the vocabulary these hints were
	// actually resolved against.
	BpeVocabFile string
}

// ParseVocabulary reads the JSON array the operator passes in
// CASSINI_TRANSCRIPTION_TERMS and returns bounded, de-duplicated terms.
// Anything unparseable yields no vocabulary: a malformed setting must leave
// transcription exactly as it was, not fail the build.
func ParseVocabulary(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var terms []string
	if err := json.Unmarshal([]byte(raw), &terms); err != nil {
		return nil
	}
	return NormalizeVocabulary(terms)
}

// NormalizeVocabulary collapses whitespace, drops empty and over-long entries,
// removes case-insensitive duplicates keeping the first spelling, and caps the
// list. The first spelling wins so an operator who lists both "Nextcloud" and
// "nextcloud" gets the one they wrote first rather than an arbitrary choice.
func NormalizeVocabulary(terms []string) []string {
	out := make([]string, 0, min(len(terms), maxVocabularyTerms))
	seen := make(map[string]struct{}, len(terms))
	for _, term := range terms {
		term = strings.Join(strings.Fields(term), " ")
		if term == "" || len([]rune(term)) > maxVocabularyTermRunes {
			continue
		}
		key := strings.ToLower(term)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, term)
		if len(out) == maxVocabularyTerms {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// vocabularyForBuild combines the operator's terms with the participant display
// names this recording actually carries. Names are the terms most likely to be
// mangled and the ones an operator should not have to retype, but they are
// appended after the configured list so an explicit spelling always wins the
// de-duplication.
func vocabularyForBuild(configured []string, streams []AudioStream) []string {
	combined := make([]string, 0, len(configured)+len(streams))
	combined = append(combined, configured...)
	for _, stream := range streams {
		combined = append(combined, stream.SpeakerLabel)
	}
	return NormalizeVocabulary(combined)
}

// writeHotwordsFile writes one term per line in the format sherpa-onnx expects.
// Terms are written verbatim: the models we ship emit cased, punctuated text,
// so the spelling an operator wants to see is the spelling to bias towards.
func writeHotwordsFile(dir string, terms []string) (string, error) {
	if len(terms) == 0 {
		return "", nil
	}
	path := filepath.Join(dir, hotwordsFileName)
	var sb strings.Builder
	for _, term := range terms {
		sb.WriteString(term)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		return "", fmt.Errorf("write hotwords file: %w", err)
	}
	return path, nil
}

// resolveHints turns a configured vocabulary into either a usable DecoderHints
// or a provenance record explaining why the vocabulary could not be applied.
//
// It never returns an error for an unusable model: a vocabulary the decoder
// cannot take is an operator-visible fact, not a build failure. Only a genuine
// I/O failure writing the file is an error.
func resolveHints(workDir string, terms []string, paths ModelPaths) (*DecoderHints, *HintsProvenance, error) {
	if len(terms) == 0 {
		return nil, nil, nil
	}
	if envBool(envHintsDisabled) {
		return nil, &HintsProvenance{
			TermCount: len(terms),
			Applied:   false,
			Reason:    "disabled by configuration (" + envHintsDisabled + ")",
		}, nil
	}
	if paths.EncoderFile == "" {
		return nil, &HintsProvenance{
			TermCount: len(terms),
			Applied:   false,
			Reason:    "this model is CTC; decoder biasing needs a transducer model",
		}, nil
	}
	if paths.BpeVocabFile == "" {
		return nil, &HintsProvenance{
			TermCount: len(terms),
			Applied:   false,
			Reason: "this model bundle ships no bpe.vocab, which the decoder needs to encode the terms; " +
				"rebuild the bundle with upstream scripts/nemo/generate_bpe_vocab.py",
		}, nil
	}

	path, err := writeHotwordsFile(workDir, terms)
	if err != nil {
		return nil, nil, err
	}
	score := hintsScore()
	return &DecoderHints{File: path, Score: score, TermCount: len(terms), BpeVocabFile: paths.BpeVocabFile},
		&HintsProvenance{
			TermCount:      len(terms),
			Score:          score,
			DecodingMethod: decodingModifiedBeamSearch,
			Applied:        true,
		}, nil
}

// hintsScore reads the operator override, falling back to the default. A score
// at or below zero would disable the bonus while still paying for beam search,
// so it is rejected in favour of the default.
func hintsScore() float32 {
	raw := strings.TrimSpace(os.Getenv(envHintsScore))
	if raw == "" {
		return defaultHotwordsScore
	}
	var v float32
	if _, err := fmt.Sscanf(raw, "%f", &v); err != nil || v <= 0 {
		return defaultHotwordsScore
	}
	return v
}
