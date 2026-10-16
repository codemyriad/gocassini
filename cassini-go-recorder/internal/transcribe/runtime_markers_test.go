package transcribe

import (
	"strings"
	"testing"
)

// cassiniRuntimeVersion is the native version of sherpa-onnx-go-linux
// v1.13.7-cassini.7 and .8 (codemyriad/sherpa-onnx v1.13.7-cassini).
const cassiniRuntimeVersion = "1.13.7+cassini-parakeet-v3-reference-v1.nemotron-diarization-v2.parakeet-ultra-v1"

// A Cassini runtime must select the reference frontend for Parakeet Ultra
// exports that keep their honest model URL. Runtimes before .7 lack the marker
// and run them on the legacy frontend (or exit at load for the plain URL).
func TestLinkedCassiniRuntimeSelectsParakeetUltra(t *testing.T) {
	version := RuntimeVersion()
	if !HasReferenceRuntime() {
		t.Skipf("stock sherpa runtime %q (no Cassini build on this platform)", version)
	}
	if !HasParakeetUltraRuntime() {
		t.Fatalf("Cassini runtime %q lacks %s; use sherpa-onnx-go-linux v1.13.7-cassini.7 or later", version, parakeetUltraRuntimeMarker)
	}
	if !HasDiarizationRuntime() {
		t.Fatalf("Cassini runtime %q lost %s", version, nemotronDiarizationRuntimeMarker)
	}
}

// The Ultra marker is appended after the v3 marker: both Ultra precisions get
// the reference boundary policy, and the diarization check still passes.
func TestUltraRuntimeSelectsReferencePolicy(t *testing.T) {
	for _, marker := range []string{parakeetReferenceRuntimeMarker, nemotronDiarizationRuntimeMarker, parakeetUltraRuntimeMarker} {
		if !strings.Contains(cassiniRuntimeVersion, marker) {
			t.Fatalf("%q lacks %s", cassiniRuntimeVersion, marker)
		}
	}
	for _, id := range []ModelID{ModelParakeetUltra, ModelParakeetUltraInt8} {
		policy, isRef, warn := effectiveVADPolicy(id, cassiniRuntimeVersion, true)
		if !isRef || warn != "" || policy != vadDecodePolicyForModel(id) {
			t.Fatalf("%s on %q: isRef=%v warn=%q policy=%+v", id, cassiniRuntimeVersion, isRef, warn, policy)
		}
	}
	if policy, isRef, warn := effectiveVADPolicy("other", cassiniRuntimeVersion, true); isRef || warn != "" || policy != defaultVADDecodePolicy() {
		t.Fatalf("unrelated model changed: isRef=%v warn=%q policy=%+v", isRef, warn, policy)
	}
}
