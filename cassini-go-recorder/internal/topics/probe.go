package topics

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
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

// ReadyFingerprint is what an embedding model's readiness receipt is bound
// to: its probe passed, with these probe sentences and vectors. Unlike a
// speech model's receipt it names neither the boot nor the image, so the
// model stays ready across a reboot and an app upgrade
// (docs/proposals/automatic-tagging, "Model catalogue"), and only a failing
// probe takes it away. Bump it when probe_golden.json or the probe changes.
const ReadyFingerprint = "embedding-probe-1"

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
// one `cassini models install|probe` runs. Like every inference it holds the
// model store's inference lock, and it refuses to start without the memory
// its probe floor asks for.
//
// record, when not nil, gets the probe's result while the lock is still held,
// so the caller can update the readiness receipt (bound to ReadyFingerprint)
// before another probe can run: a pass recorded after a later failure would
// otherwise make a failing model ready again. A probe that never got the lock
// records nothing. ProbeInstalled returns the probe's result joined with
// record's error.
func ProbeInstalled(ctx context.Context, s *modelstore.Store, m modelstore.Model, threads int, record func(error) error) error {
	if m.Kind != modelstore.KindEmbedding {
		return fmt.Errorf("%s is not an embedding model", m.ID)
	}
	unlock, err := transcribe.LockModelRuntime(ctx, s.Root)
	if err != nil {
		return err
	}
	defer unlock()
	err = probeLockedFn(ctx, s, m, threads)
	if record != nil {
		err = errors.Join(err, record(err))
	}
	return err
}

// probeLockedFn is a seam for tests of the locking around the probe.
var probeLockedFn = probeLocked

func probeLocked(ctx context.Context, s *modelstore.Store, m modelstore.Model, threads int) error {
	if err := RuntimeCheck(); err != nil {
		return err
	}
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
