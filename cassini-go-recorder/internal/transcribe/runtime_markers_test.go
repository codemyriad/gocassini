package transcribe

import (
	"strings"
	"testing"
)

// cassiniRuntimeVersion is the native version of sherpa-onnx-go-*/v2
// v2.0.1-cassini.1 to .3 (codemyriad/sherpa-onnx tags v2.0.1-cassini-1 and -2).
const cassiniRuntimeVersion = "2.0.1+cassini.parakeet-tdt-joiner-v1.nemotron-diarization-v2"

// Older and partial runtimes this binary may still meet.
const (
	// The 1.13.7 Cassini runtimes, cassini.7 and .8: reference frontend and
	// Ultra url selection, on a C API this binary no longer links.
	legacyCassiniRuntimeVersion = "1.13.7+cassini-parakeet-v3-reference-v1.nemotron-diarization-v2.parakeet-ultra-v1"
	// Stock 2.0.1 (Linux arm32 ships it): #3999 log guard, no joiner
	// detection, no Sortformer.
	stockRuntimeVersion       = "2.0.1"
	stockLegacyRuntimeVersion = "1.13.7"
)

// The linked runtime must be the Cassini 2.0.1 build wherever this binary has
// one: it loads plain-url Ultra exports and runs Nemotron through Sortformer.
func TestLinkedCassiniRuntimeRunsParakeetUltra(t *testing.T) {
	version := RuntimeVersion()
	if !runtimeABICompatible(version) {
		t.Fatalf("linked runtime %q is older than the %v C API this wrapper uses", version, minRuntimeBase)
	}
	if !strings.Contains(version, "+cassini") {
		t.Skipf("stock sherpa runtime %q (no Cassini build on this platform)", version)
	}
	for _, id := range []ModelID{ModelParakeetUltra, ModelParakeetUltraInt8, ModelParakeet110M} {
		if err := CheckNativeRuntime(id, version); err != nil {
			t.Fatalf("Cassini runtime cannot run %s: %v", id, err)
		}
	}
	if !HasDiarizationRuntime() {
		t.Fatalf("Cassini runtime %q lost %s", version, nemotronDiarizationRuntimeMarker)
	}
	if strings.Contains(version, legacyReferenceRuntimeMarker) {
		t.Fatalf("Cassini runtime %q still claims the 1.13.7 reference frontend", version)
	}
}

func TestRuntimeBase(t *testing.T) {
	cases := map[string]bool{
		cassiniRuntimeVersion:       true,
		stockRuntimeVersion:         true,
		"2.0.2":                     true,
		"2.1.0+cassini.x":           true,
		"10.0.0":                    true,
		legacyCassiniRuntimeVersion: false,
		stockLegacyRuntimeVersion:   false,
		"1.99.99":                   false,
		"2.0.0":                     false,
		"":                          false,
		"garbage":                   false,
		"2.0":                       false,
	}
	for version, want := range cases {
		if got := runtimeABICompatible(version); got != want {
			t.Errorf("runtimeABICompatible(%q) = %v, want %v", version, got, want)
		}
	}
}

// Ultra loads only where the joiner-size TDT detection is; every model needs
// the 2.0.1 C API. The boundary policy and its frontend come with the runtime.
func TestNativeRuntimeGates(t *testing.T) {
	cases := []struct {
		id      ModelID
		version string
		refuse  string // substring of the refusal, "" for none
	}{
		{ModelParakeetUltra, cassiniRuntimeVersion, ""},
		{ModelParakeetUltraInt8, cassiniRuntimeVersion, ""},
		{ModelParakeet110M, cassiniRuntimeVersion, ""},
		{ModelParakeetUltra, stockRuntimeVersion, parakeetTDTJoinerRuntimeMarker},
		{ModelParakeetUltraInt8, stockRuntimeVersion, parakeetTDTJoinerRuntimeMarker},
		{ModelParakeet110M, stockRuntimeVersion, ""},
		{ModelParakeetUltra, legacyCassiniRuntimeVersion, "1.13.7 Cassini runtime"},
		{ModelParakeetUltraInt8, legacyCassiniRuntimeVersion, "older than the sherpa-onnx 2.0.1 C API"},
		{ModelParakeet110M, legacyCassiniRuntimeVersion, "older than the sherpa-onnx 2.0.1 C API"},
		{ModelParakeet110M, stockLegacyRuntimeVersion, "older than the sherpa-onnx 2.0.1 C API"},
	}
	for _, c := range cases {
		err := CheckNativeRuntime(c.id, c.version)
		switch {
		case c.refuse == "" && err != nil:
			t.Errorf("%s on %q refused: %v", c.id, c.version, err)
		case c.refuse != "" && (err == nil || !strings.Contains(err.Error(), c.refuse)):
			t.Errorf("%s on %q: refusal %v, want one naming %q", c.id, c.version, err, c.refuse)
		}
	}
	for _, id := range []ModelID{ModelParakeetUltra, ModelParakeetUltraInt8} {
		policy, frontend := effectiveVADPolicy(id, cassiniRuntimeVersion, true)
		if frontend != NativeFrontendNeMoLogGuard || policy != vadDecodePolicyForModel(id) {
			t.Fatalf("%s on %q: frontend=%q policy=%+v", id, cassiniRuntimeVersion, frontend, policy)
		}
		if policy, frontend := effectiveVADPolicy(id, cassiniRuntimeVersion, false); frontend != "" || policy != defaultVADDecodePolicy() {
			t.Fatalf("%s legacy profile: frontend=%q policy=%+v", id, frontend, policy)
		}
	}
	if policy, frontend := effectiveVADPolicy(ModelParakeet110M, cassiniRuntimeVersion, true); frontend != "" || policy != defaultVADDecodePolicy() {
		t.Fatalf("110M changed: frontend=%q policy=%+v", frontend, policy)
	}
}

// Nemotron goes through #4032's Sortformer member, which only a 2.0.1 runtime
// with the marker has. The 1.13.7 runtimes carried the marker for a different
// route, and stock arm32 has neither.
func TestDiarizationRuntimeGate(t *testing.T) {
	for version, want := range map[string]bool{
		cassiniRuntimeVersion:       true,
		legacyCassiniRuntimeVersion: false,
		stockRuntimeVersion:         false,
		stockLegacyRuntimeVersion:   false,
	} {
		if got := diarizationRuntime(version); got != want {
			t.Errorf("diarizationRuntime(%q) = %v, want %v", version, got, want)
		}
	}
}

func TestNativeFrontendNeverClaimsTheReferenceFrontend(t *testing.T) {
	for version, want := range map[string]string{
		cassiniRuntimeVersion:       NativeFrontendNeMoLogGuard,
		stockRuntimeVersion:         NativeFrontendNeMoLogGuard,
		legacyCassiniRuntimeVersion: "",
	} {
		if got := NativeFrontend(version); got != want {
			t.Errorf("NativeFrontend(%q) = %q, want %q", version, got, want)
		}
	}
}
