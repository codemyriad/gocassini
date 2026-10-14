package topics

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"

	"gocassini/internal/modelstore"
	"gocassini/internal/transcribe"
)

// probe_golden.json holds four pinned public sentences in four languages, the
// ids the Hugging Face tokenizer gives them and the vectors the PyTorch fp32
// model gives them (testdata/reference/gen.py). The runtime check embeds the
// sentences and compares.
//
//go:embed probe_golden.json
var probeGoldenJSON []byte

// ProbeMinCosine is how close the int8 runtime must come to the fp32
// reference. Measured: at least 0.99994.
const ProbeMinCosine = 0.9995

// ShippedModelRevision is the catalogue revision the embedded probe vectors
// were made for.
const ShippedModelRevision = "89cef0454bc6e723f9f7fc53df0e2c881ba672fd00d8678c02d677f0b1ebee2c"

type probeGolden struct {
	Model    string      `json:"model"`
	Revision string      `json:"revision"`
	Prompt   string      `json:"prompt"`
	Texts    []string    `json:"texts"`
	IDs      [][]int64   `json:"ids"`
	Vectors  [][]float32 `json:"vectors"`
}

func loadProbeGolden() (probeGolden, error) {
	var g probeGolden
	if err := json.Unmarshal(probeGoldenJSON, &g); err != nil {
		return g, err
	}
	if g.Prompt != PromptPrefix || len(g.Texts) == 0 || len(g.IDs) != len(g.Texts) || len(g.Vectors) != len(g.Texts) {
		return g, fmt.Errorf("malformed probe vectors")
	}
	return g, nil
}

// Probe embeds the pinned sentences and compares them with the reference: the
// ids must match exactly and every vector must reach ProbeMinCosine. A
// revision with no reference vectors fails, so a catalogue update cannot ship
// a model nobody checked.
func Probe(ctx context.Context, e *Embedder, revision string) error {
	g, err := loadProbeGolden()
	if err != nil {
		return err
	}
	if g.Revision != revision {
		return fmt.Errorf("no probe vectors for model revision %s", revision)
	}
	for i, text := range g.Texts {
		if !slices.Equal(e.Tokenizer().Encode(text), g.IDs[i]) {
			return fmt.Errorf("%w: tokenizer ids for probe text %d differ from the reference", ErrModelFault, i)
		}
	}
	got, err := e.EmbedTexts(ctx, g.Texts)
	if err != nil {
		return err
	}
	for i := range g.Texts {
		if c := Cosine(got[i].Vector, g.Vectors[i]); c < ProbeMinCosine {
			return fmt.Errorf("%w: probe text %d has cosine %.6f to the reference, below %.4f", ErrModelFault, i, c, ProbeMinCosine)
		}
	}
	return nil
}

// ProbeInstalled is the runtime check of an installed embedding model, the
// one `cassini models install|probe` runs. It writes no readiness receipt:
// `models list` reports an embedding model ready when its files are
// installed and RuntimeCheck passes, which survives a reboot or an image
// upgrade. Like every inference it holds the model store's inference lock,
// and it refuses to start without the memory its probe floor asks for.
func ProbeInstalled(ctx context.Context, s *modelstore.Store, m modelstore.Model, threads int) error {
	if m.Kind != modelstore.KindEmbedding {
		return fmt.Errorf("%s is not an embedding model", m.ID)
	}
	if err := RuntimeCheck(); err != nil {
		return err
	}
	unlock, err := transcribe.LockModelRuntime(ctx, s.Root)
	if err != nil {
		return err
	}
	defer unlock()
	files, err := s.InstalledEmbedder(m)
	if err != nil {
		return err
	}
	if err := transcribe.AdmitModelProbe(ctx, m.ID, "cpu"); err != nil {
		return err
	}
	e, err := Open(files, Options{Threads: threads})
	if err != nil {
		return err
	}
	defer e.Close()
	if err := Probe(ctx, e, m.Revision); err != nil {
		return err
	}
	return ctx.Err()
}
