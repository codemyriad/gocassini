// Package topics turns transcript text into EmbeddingGemma 2 vectors for
// topic suggestions (docs/proposals/automatic-tagging). It runs the int8
// ONNX export through the ONNX Runtime library the image already ships and
// tokenizes with a pure-Go SentencePiece reader. It knows nothing about
// meetings: callers hand it plain text.
package topics

import (
	"crypto/sha256"
	"encoding/hex"
)

// EmbedRecipeVersion names everything outside the model and the chunker that
// changes a vector: the prompt prefix, the token cap and the batching policy.
// A change to any of them must bump it, because stored vectors made under an
// older recipe can no longer be compared with new ones.
const EmbedRecipeVersion = "r1"

// PromptPrefix is EmbeddingGemma's "Clustering" prompt, the one the
// prototype's thresholds were measured with. Every text, passage or topic
// definition, is embedded behind it.
const PromptPrefix = "task: clustering | query: "

// MaxTokens caps one input, prefix and the BOS and EOS tokens included. A
// longer text is refused, never truncated: truncation drops words silently.
const MaxTokens = 512

// Dimensions is the length of every vector.
const Dimensions = 768

// The batching policy. Texts are sorted by token count and grouped into
// batches of at most MaxBatchRows rows and MaxBatchTokens padded tokens; a
// padded row embeds to the same vector as the text alone (the padding parity
// test), so batching changes only the speed and the memory. Measured at 4
// threads on 600 natural passages (about 66 tokens each): 1.7 times as fast
// as one text at a time, with the same speed at 1024 and 4096 tokens per
// batch. On texts near the cap the peak RSS is 0.58 GB at 1024 and 1.78 GB at
// 4096, so 1024 it is.
const (
	MaxBatchRows   = 16
	MaxBatchTokens = 1024
)

// EmbedKey identifies one meeting's passage vectors: the transcript body, the
// model, the chunker and this recipe. Equal keys mean equal vectors, so an
// embed whose key is unchanged is skipped. It is the lowercase hex sha256 of
// the four values joined by newlines:
//
//	payloadSHA256 "\n" modelRevision "\n" ChunkerVersion "\n" EmbedRecipeVersion
//
// cassini-operator computes the same key from stored values; change both
// together.
func EmbedKey(payloadSHA256, modelRevision string) string {
	sum := sha256.Sum256([]byte(payloadSHA256 + "\n" + modelRevision + "\n" + ChunkerVersion + "\n" + EmbedRecipeVersion))
	return hex.EncodeToString(sum[:])
}
