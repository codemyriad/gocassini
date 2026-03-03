package talk

import (
	"math"
	"testing"
	"time"
)

func TestPlanFinalMergeInputsUsesEarliestSessionAndSourceStart(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	s1 := &sessionCapture{MKVPath: "/tmp/s1.mkv", StartedAt: base.Add(5 * time.Second)}
	s2 := &sessionCapture{MKVPath: "/tmp/s2.mkv", StartedAt: base}
	s3 := &sessionCapture{MKVPath: "/tmp/s3.mkv", StartedAt: base.Add(12500 * time.Millisecond)}

	probe := func(path string) (float64, bool) {
		switch path {
		case "/tmp/s1.mkv":
			return 1.25, true
		case "/tmp/s2.mkv":
			return 0.1, true
		default:
			return 0, false
		}
	}

	got := planFinalMergeInputs([]*sessionCapture{s1, s2, s3}, probe)
	if len(got) != 3 {
		t.Fatalf("expected 3 merge inputs, got %d", len(got))
	}

	if got[0].Session != s1 || got[1].Session != s2 || got[2].Session != s3 {
		t.Fatalf("session order changed")
	}

	assertClose(t, got[0].OffsetSeconds, 3.75)
	assertClose(t, got[1].OffsetSeconds, -0.1)
	assertClose(t, got[2].OffsetSeconds, 12.5)
}

func TestPlanFinalMergeInputsWithNilProbeDefaultsToZeroSourceStart(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	s1 := &sessionCapture{MKVPath: "/tmp/s1.mkv", StartedAt: base}
	s2 := &sessionCapture{MKVPath: "/tmp/s2.mkv", StartedAt: base.Add(1500 * time.Millisecond)}

	got := planFinalMergeInputs([]*sessionCapture{s1, s2}, nil)
	if len(got) != 2 {
		t.Fatalf("expected 2 merge inputs, got %d", len(got))
	}

	assertClose(t, got[0].OffsetSeconds, 0.0)
	assertClose(t, got[1].OffsetSeconds, 1.5)
}

func TestPlanFinalMergeInputsEmpty(t *testing.T) {
	got := planFinalMergeInputs(nil, nil)
	if len(got) != 0 {
		t.Fatalf("expected no merge inputs, got %d", len(got))
	}
}

func assertClose(t *testing.T, got float64, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("unexpected float value: got=%.12f want=%.12f", got, want)
	}
}
