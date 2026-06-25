package transcribe

import "testing"

func TestNormalizeQuality(t *testing.T) {
	cases := map[string]STTQuality{
		"":         QualityBalanced,
		"balanced": QualityBalanced,
		"FAST":     QualityFast,
		"  best  ": QualityBest,
		"nonsense": QualityBalanced,
	}
	for in, want := range cases {
		if got := NormalizeQuality(in); got != want {
			t.Errorf("NormalizeQuality(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestModelForQuality(t *testing.T) {
	cases := []struct {
		q      STTQuality
		device string
		want   ModelID
	}{
		// GPU always uses fp32 (int8 fragments under the CUDA EP).
		{QualityBest, "cuda", ModelParakeet06BV3},
		{QualityBalanced, "cuda", ModelParakeet06BV3},
		{QualityFast, "cuda", ModelParakeet06BV3},
		// CPU trades accuracy for speed across tiers.
		{QualityBest, "cpu", ModelParakeet06BV3},
		{QualityBalanced, "cpu", ModelParakeet06BV3Int8},
		{QualityFast, "cpu", ModelParakeet110M},
		// Unset quality behaves as balanced.
		{"", "cpu", ModelParakeet06BV3Int8},
	}
	for _, c := range cases {
		if got := ModelForQuality(c.q, c.device); got != c.want {
			t.Errorf("ModelForQuality(%q,%q) = %q, want %q", c.q, c.device, got, c.want)
		}
	}
}

func TestResolveDevice(t *testing.T) {
	if got := ResolveDevice("cpu"); got != "cpu" {
		t.Errorf("ResolveDevice(cpu) = %q", got)
	}
	if got := ResolveDevice("CUDA"); got != "cuda" {
		t.Errorf("ResolveDevice(CUDA) = %q", got)
	}
	// "" / "auto" auto-detect; on any host the result must be a concrete device.
	for _, in := range []string{"", "auto", "weird"} {
		if got := ResolveDevice(in); got != "cpu" && got != "cuda" {
			t.Errorf("ResolveDevice(%q) = %q, want cpu|cuda", in, got)
		}
	}
}

func TestResolveModelID(t *testing.T) {
	// Explicit model always wins, even against the device default.
	if got := ResolveModelID("my-custom-model", "best", "cuda"); got != ModelID("my-custom-model") {
		t.Errorf("explicit model not honoured: got %q", got)
	}
	// Derived from quality+device when unset.
	if got := ResolveModelID("", "fast", "cpu"); got != ModelParakeet110M {
		t.Errorf("ResolveModelID(fast,cpu) = %q", got)
	}
	if got := ResolveModelID("", "fast", "cuda"); got != ModelParakeet06BV3 {
		t.Errorf("ResolveModelID(fast,cuda) = %q, want fp32 (cuda overrides tier)", got)
	}
}

func TestDefaultNumThreads(t *testing.T) {
	if n := DefaultNumThreads(); n < 1 || n > 16 {
		t.Errorf("DefaultNumThreads() = %d, want 1..16", n)
	}
}
