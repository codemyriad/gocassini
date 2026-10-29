package topics

import (
	"context"
	"errors"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"gocassini/internal/modelstore"
	"gocassini/internal/transcribe"
)

func idsOfLengths(lengths ...int) [][]int64 {
	out := make([][]int64, len(lengths))
	for i, n := range lengths {
		out[i] = make([]int64, n)
	}
	return out
}

func TestBatchesWithoutBatchingKeepEveryInputAlone(t *testing.T) {
	groups := batches(idsOfLengths(30, 10, 20), false)
	if !slices.EqualFunc(groups, [][]int{{0}, {1}, {2}}, slices.Equal) {
		t.Fatalf("groups = %v", groups)
	}
}

func TestBatchesSortByLengthAndRespectBothCaps(t *testing.T) {
	lengths := []int{300, 12, 40, 12, 500, 41}
	for range 20 {
		lengths = append(lengths, 30)
	}
	ids := idsOfLengths(lengths...)
	groups := batches(ids, true)
	seen := map[int]int{}
	last := 0
	for _, g := range groups {
		width := 0
		for _, index := range g {
			seen[index]++
			if len(ids[index]) < last {
				t.Fatalf("groups %v are not in length order", groups)
			}
			last = len(ids[index])
			width = max(width, len(ids[index]))
		}
		if len(g) > MaxBatchRows || len(g)*width > MaxBatchTokens {
			t.Fatalf("group %v breaks a cap (%d rows of %d tokens)", g, len(g), width)
		}
	}
	for i := range ids {
		if seen[i] != 1 {
			t.Fatalf("input %d is in %d groups", i, seen[i])
		}
	}
	if !slices.EqualFunc(groups, batches(ids, true), slices.Equal) {
		t.Fatal("grouping is not deterministic")
	}
}

func TestCheckVectorIsTheModelFaultGuard(t *testing.T) {
	unit := make([]float32, Dimensions)
	unit[0] = 1
	if err := CheckVector(unit); err != nil {
		t.Fatal(err)
	}
	nan := slices.Clone(unit)
	nan[3] = float32(math.NaN())
	inf := slices.Clone(unit)
	inf[0] = float32(math.Inf(1))
	long := slices.Clone(unit)
	long[0] = 1.01
	for name, v := range map[string][]float32{"short": unit[:767], "nan": nan, "inf": inf, "norm": long, "zero": make([]float32, Dimensions)} {
		if CheckVector(v) == nil {
			t.Errorf("%s vector passed the guard", name)
		}
	}
}

// The probe vectors are for the revision the shipped catalogue installs, so a
// catalogue update that changes the model must regenerate them.
func TestProbeVectorsMatchTheShippedCatalogue(t *testing.T) {
	g, err := loadProbeGolden()
	if err != nil {
		t.Fatal(err)
	}
	m, err := modelstore.Shipped().Model(g.Model, g.Revision)
	if err != nil {
		t.Fatalf("probe vectors are for %s@%s: %v", g.Model, g.Revision, err)
	}
	if m.Kind != modelstore.KindEmbedding || g.Revision != ShippedModelRevision || len(g.Texts) != 4 {
		t.Fatalf("probe vectors = %s@%s, %d texts", g.Model, g.Revision, len(g.Texts))
	}
	for i, v := range g.Vectors {
		if err := CheckVector(v); err != nil {
			t.Fatalf("reference vector %d: %v", i, err)
		}
	}
}

func TestProbeRefusesARevisionWithoutVectors(t *testing.T) {
	err := Probe(context.Background(), &Embedder{}, "0000")
	if err == nil || errors.Is(err, ErrModelFault) {
		t.Fatalf("err = %v", err)
	}
}

func TestThreads(t *testing.T) {
	for _, c := range []struct {
		env  string
		cpus int
		want int
		bad  bool
	}{
		{"", 12, 4, false},
		{"", 2, 2, false},
		{"0", 12, 4, false},
		{"6", 2, 6, false},
		{"99", 2, 16, false},
		{"-3", 8, 1, false},
		{"many", 8, 4, true},
	} {
		t.Setenv(ThreadsEnv, c.env)
		got, err := Threads(c.cpus)
		if got != c.want || (err != nil) != c.bad {
			t.Errorf("%s=%q cpus=%d: %d, %v; want %d", ThreadsEnv, c.env, c.cpus, got, err, c.want)
		}
	}
}

// unitRows returns one valid unit vector per row.
func unitRows(rows [][]int64) ([][]float32, error) {
	out := make([][]float32, len(rows))
	for i := range out {
		out[i] = make([]float32, Dimensions)
		out[i][0] = 1
	}
	return out, nil
}

// A cancellation during the last native run fails the call: the run cannot be
// interrupted, but its caller asked to stop.
func TestEmbedIDsCancelledDuringTheLastBatchFails(t *testing.T) {
	for _, batch := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		ids := idsOfLengths(3, 5)
		runs := 0
		e := &Embedder{batch: batch, runRows: func(rows [][]int64) ([][]float32, error) {
			runs++
			if runs == len(batches(ids, batch)) {
				cancel()
			}
			return unitRows(rows)
		}}
		if got, err := e.EmbedIDs(ctx, ids); !errors.Is(err, context.Canceled) || got != nil {
			t.Fatalf("batch=%v: got %d vectors, err %v; want context.Canceled and none", batch, len(got), err)
		}
		cancel()
	}
	// Not cancelled: every input embeds.
	e := &Embedder{batch: true, runRows: unitRows}
	if got, err := e.EmbedIDs(context.Background(), idsOfLengths(3, 5)); err != nil || len(got) != 2 {
		t.Fatalf("got %d vectors, err %v", len(got), err)
	}
}

// The probe's result is recorded while the inference lock is still held, so
// probes record in the order they ran: a pass cannot land after a later
// failure and make a failing model ready again.
func TestProbeInstalledRecordsUnderTheLock(t *testing.T) {
	s := modelstore.New(t.TempDir())
	m := modelstore.Model{ID: "embedder", Kind: modelstore.KindEmbedding}
	fault := errors.New("probe failed")
	started, release := make(chan struct{}), make(chan struct{})
	prev := probeLockedFn
	t.Cleanup(func() { probeLockedFn = prev })
	// The file lock orders the two probes; the race detector cannot see
	// that, so the shared state is guarded as well.
	var calls atomic.Int32
	var mu sync.Mutex
	probeLockedFn = func(context.Context, *modelstore.Store, modelstore.Model, int) error {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return nil
		}
		return fault
	}
	var recorded []error
	record := func(err error) error {
		// Someone else trying the lock now must be refused.
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		if unlock, lockErr := transcribe.LockModelRuntime(cancelled, s.Root); lockErr == nil {
			unlock()
			t.Error("record ran without the inference lock held")
		}
		mu.Lock()
		recorded = append(recorded, err)
		mu.Unlock()
		return nil
	}
	first := make(chan error, 1)
	go func() { first <- ProbeInstalled(context.Background(), s, m, 1, record) }()
	<-started
	second := make(chan error, 1)
	go func() { second <- ProbeInstalled(context.Background(), s, m, 1, record) }()
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; !errors.Is(err, fault) {
		t.Fatalf("second probe: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(recorded) != 2 || recorded[0] != nil || !errors.Is(recorded[1], fault) {
		t.Fatalf("recorded %v, want the pass then the failure", recorded)
	}
}
