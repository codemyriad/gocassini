package operator

// The catalogue kind of the topic suggestions embedding model in `cassini
// models list`, and the one Settings installs (docs/proposals/automatic-tagging).
// It always runs on the CPU, like the speaker separation model.
const (
	modelKindEmbedding    = "embedding"
	defaultEmbeddingModel = "embeddinggemma-2-text-int8"
)
