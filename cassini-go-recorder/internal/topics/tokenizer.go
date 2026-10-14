package topics

import (
	"fmt"

	sentencepiece "github.com/eliben/go-sentencepiece"
)

// Gemma's special token ids. The HF tokenizer wraps every input in BOS and
// EOS; SentencePiece does not, so Encode adds them.
const (
	padID = 0
	eosID = 1
	bosID = 2
)

// Tokenizer turns text into EmbeddingGemma input ids, matching the Hugging
// Face tokenizer the model was published with (the parity test).
type Tokenizer struct {
	proc *sentencepiece.Processor
}

// LoadTokenizer reads the model's tokenizer.model.
func LoadTokenizer(path string) (*Tokenizer, error) {
	proc, err := sentencepiece.NewProcessorFromPath(path)
	if err != nil {
		return nil, fmt.Errorf("load tokenizer %s: %w", path, err)
	}
	return &Tokenizer{proc: proc}, nil
}

// Encode returns the ids the model reads for text: BOS, the prompt prefix and
// the text, EOS.
func (t *Tokenizer) Encode(text string) []int64 {
	tokens := t.proc.Encode(PromptPrefix + text)
	ids := make([]int64, 0, len(tokens)+2)
	ids = append(ids, bosID)
	for _, tok := range tokens {
		ids = append(ids, int64(tok.ID))
	}
	return append(ids, eosID)
}
