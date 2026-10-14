package topics

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"

	ort "github.com/yalue/onnxruntime_go"

	"gocassini/internal/modelstore"
)

var (
	// ErrModelFault marks a vector that is not finite, has the wrong length
	// or is not unit-norm. The model or the runtime is broken; retrying the
	// same text gives the same answer.
	ErrModelFault = errors.New("model fault")
	// ErrUnsupportedRuntime marks an ONNX Runtime library this binding cannot
	// use: missing, or older than the C API version it was built against.
	ErrUnsupportedRuntime = errors.New("unsupported-runtime")
	// ErrTooLong marks a text whose ids exceed MaxTokens.
	ErrTooLong = errors.New("text too long")
)

// normTolerance bounds how far a vector's length may be from 1.
const normTolerance = 1e-3

// LibraryPath is the ONNX Runtime library the embedder loads:
// $CASSINI_LIB_DIR/libonnxruntime.so in the image. Without CASSINI_LIB_DIR it
// is the bare soname, which resolves to the copy the cassini binary already
// links for sherpa-onnx.
func LibraryPath() string {
	if dir := os.Getenv("CASSINI_LIB_DIR"); dir != "" {
		return filepath.Join(dir, "libonnxruntime.so")
	}
	return "libonnxruntime.so"
}

var (
	runtimeOnce sync.Once
	runtimeErr  error
)

// RuntimeCheck loads the ONNX Runtime library once per process and asks it
// for the C API version the binding needs. It is cheap and depends on
// neither the boot nor the model, so `models list` runs it on every listing.
func RuntimeCheck() error {
	runtimeOnce.Do(func() {
		ort.SetSharedLibraryPath(LibraryPath())
		if err := ort.InitializeEnvironment(ort.WithLogLevelError()); err != nil {
			runtimeErr = fmt.Errorf("%w: %v", ErrUnsupportedRuntime, err)
		}
	})
	return runtimeErr
}

// RuntimeVersion is the loaded ONNX Runtime version, or "" before a
// successful RuntimeCheck.
func RuntimeVersion() string {
	if RuntimeCheck() != nil {
		return ""
	}
	return ort.GetVersion()
}

// Options configure an Embedder.
type Options struct {
	// Threads is the intra-op thread count; below 1 means 1.
	Threads int
	// Batch turns on padded batching (MaxBatchRows, MaxBatchTokens). Off,
	// every text runs alone.
	Batch bool
}

// Embedder runs the text graph of an installed embedding model. It is not
// safe for concurrent use.
type Embedder struct {
	tok     *Tokenizer
	session *ort.DynamicAdvancedSession
	batch   bool
	// runRows replaces run in tests.
	runRows func(rows [][]int64) ([][]float32, error)
}

var (
	inputNames  = []string{"input_ids", "attention_mask", "image_features", "video_features", "audio_features"}
	outputNames = []string{"sentence_embedding"}
)

// Open loads the tokenizer and the graph. Model load takes well under a
// second, so callers open one Embedder per process.
func Open(files modelstore.EmbeddingFiles, opts Options) (*Embedder, error) {
	if err := RuntimeCheck(); err != nil {
		return nil, err
	}
	tok, err := LoadTokenizer(files.Tokenizer)
	if err != nil {
		return nil, err
	}
	so, err := ort.NewSessionOptions()
	if err != nil {
		return nil, err
	}
	defer so.Destroy()
	if err := so.SetIntraOpNumThreads(max(opts.Threads, 1)); err != nil {
		return nil, err
	}
	if err := so.SetInterOpNumThreads(1); err != nil {
		return nil, err
	}
	// The graph names its weights file; ONNX Runtime resolves it next to the
	// graph, so files.Data is only checked to exist.
	if _, err := os.Stat(files.Data); err != nil {
		return nil, err
	}
	session, err := ort.NewDynamicAdvancedSession(files.Graph, inputNames, outputNames, so)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", filepath.Base(files.Graph), err)
	}
	return &Embedder{tok: tok, session: session, batch: opts.Batch}, nil
}

// Close releases the session.
func (e *Embedder) Close() error {
	if e == nil || e.session == nil {
		return nil
	}
	err := e.session.Destroy()
	e.session = nil
	return err
}

// Tokenizer is the embedder's tokenizer.
func (e *Embedder) Tokenizer() *Tokenizer { return e.tok }

// Embedding is one text's vector and how many ids it took.
type Embedding struct {
	Vector     []float32
	TokenCount int
}

// EmbedTexts embeds texts in order. A text over MaxTokens fails the call with
// ErrTooLong before anything runs; a vector that fails the guards fails it
// with ErrModelFault.
func (e *Embedder) EmbedTexts(ctx context.Context, texts []string) ([]Embedding, error) {
	ids := make([][]int64, len(texts))
	for i, text := range texts {
		ids[i] = e.tok.Encode(text)
		if len(ids[i]) > MaxTokens {
			return nil, fmt.Errorf("%w: text %d has %d tokens; the limit is %d", ErrTooLong, i, len(ids[i]), MaxTokens)
		}
	}
	return e.EmbedIDs(ctx, ids)
}

// EmbedIDs embeds already tokenized inputs in order.
func (e *Embedder) EmbedIDs(ctx context.Context, ids [][]int64) ([]Embedding, error) {
	out := make([]Embedding, len(ids))
	for _, group := range batches(ids, e.batch) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rows := make([][]int64, len(group))
		for i, index := range group {
			rows[i] = ids[index]
		}
		run := e.run
		if e.runRows != nil {
			run = e.runRows
		}
		vectors, err := run(rows)
		if err != nil {
			return nil, err
		}
		for i, index := range group {
			if err := CheckVector(vectors[i]); err != nil {
				return nil, fmt.Errorf("%w: input %d: %v", ErrModelFault, index, err)
			}
			out[index] = Embedding{Vector: vectors[i], TokenCount: len(ids[index])}
		}
	}
	// A native run cannot be interrupted: a cancellation that arrived during
	// the last one still fails the call, so no caller reports a run it was
	// asked to stop as a success.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// batches groups input indexes. Without batching every input is its own
// group. With it, inputs are sorted by length (stable, so the grouping is
// deterministic) and packed while the group stays within MaxBatchRows rows
// and MaxBatchTokens padded tokens.
func batches(ids [][]int64, batch bool) [][]int {
	order := make([]int, len(ids))
	for i := range order {
		order[i] = i
	}
	if !batch {
		groups := make([][]int, len(order))
		for i, index := range order {
			groups[i] = []int{index}
		}
		return groups
	}
	sort.SliceStable(order, func(a, b int) bool { return len(ids[order[a]]) < len(ids[order[b]]) })
	var groups [][]int
	var current []int
	for _, index := range order {
		// Sorted ascending, so this input is the group's longest.
		if len(current) > 0 && (len(current) == MaxBatchRows || (len(current)+1)*len(ids[index]) > MaxBatchTokens) {
			groups = append(groups, current)
			current = nil
		}
		current = append(current, index)
	}
	if len(current) > 0 {
		groups = append(groups, current)
	}
	return groups
}

// run embeds one group, right-padding every row to the longest with the pad
// id and a zero attention mask.
func (e *Embedder) run(rows [][]int64) ([][]float32, error) {
	width := 0
	for _, row := range rows {
		width = max(width, len(row))
	}
	n := int64(len(rows))
	ids := make([]int64, 0, len(rows)*width)
	mask := make([]int64, 0, len(rows)*width)
	for _, row := range rows {
		for j := 0; j < width; j++ {
			if j < len(row) {
				ids = append(ids, row[j])
				mask = append(mask, 1)
			} else {
				ids = append(ids, padID)
				mask = append(mask, 0)
			}
		}
	}
	var values []ort.Value
	defer func() {
		for _, v := range values {
			if v != nil {
				_ = v.Destroy()
			}
		}
	}()
	idsTensor, err := ort.NewTensor(ort.NewShape(n, int64(width)), ids)
	if err != nil {
		return nil, err
	}
	values = append(values, idsTensor)
	maskTensor, err := ort.NewTensor(ort.NewShape(n, int64(width)), mask)
	if err != nil {
		return nil, err
	}
	values = append(values, maskTensor)
	// The graph also takes image, video and audio features; text alone is
	// three empty [0,512] tensors.
	for range 3 {
		empty, err := ort.NewTensor(ort.NewShape(0, 512), []float32{})
		if err != nil {
			return nil, err
		}
		values = append(values, empty)
	}
	outputs := []ort.Value{nil}
	if err := e.session.Run(values, outputs); err != nil {
		return nil, fmt.Errorf("run embedding graph: %w", err)
	}
	values = append(values, outputs[0])
	tensor, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		return nil, fmt.Errorf("%w: sentence_embedding is not a float32 tensor", ErrModelFault)
	}
	shape := tensor.GetShape()
	if len(shape) != 2 || shape[0] != n || shape[1] != Dimensions {
		return nil, fmt.Errorf("%w: sentence_embedding has shape %v, want [%d %d]", ErrModelFault, shape, n, Dimensions)
	}
	data := tensor.GetData()
	vectors := make([][]float32, len(rows))
	for i := range vectors {
		vectors[i] = append([]float32(nil), data[i*Dimensions:(i+1)*Dimensions]...)
	}
	return vectors, nil
}

// CheckVector is the model-fault guard: finite, Dimensions long, unit norm.
// EmbedIDs applies it to every vector; callers that receive vectors from
// elsewhere apply it again before writing them.
func CheckVector(v []float32) error {
	if len(v) != Dimensions {
		return fmt.Errorf("vector has %d dimensions, want %d", len(v), Dimensions)
	}
	var sum float64
	for _, x := range v {
		f := float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return errors.New("vector is not finite")
		}
		sum += f * f
	}
	if norm := math.Sqrt(sum); math.Abs(norm-1) > normTolerance {
		return fmt.Errorf("vector norm %.6f is not 1", norm)
	}
	return nil
}

// Cosine is the cosine similarity of two vectors of equal length.
func Cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / math.Sqrt(na*nb)
}
