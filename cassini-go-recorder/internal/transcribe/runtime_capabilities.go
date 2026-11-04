package transcribe

import (
	"fmt"
	"strconv"
	"strings"
)

// The native runtime is sherpa-onnx 2.0.1 from codemyriad/sherpa-onnx tag
// v2.0.1-cassini-2 (upstream master past 2.0.1; -1 was the 2.0.1 release),
// shipped through the sherpa-onnx-go-{linux,macos,windows}/v2
// v2.0.1-cassini.3 forks (.1/.2 carried -1). Both report the version string
//
//	2.0.1+cassini.parakeet-tdt-joiner-v1.nemotron-diarization-v2
//
// and each capability below is read from that string, never from a module
// version, because LD_LIBRARY_PATH or a stale image can put other libraries
// under this binary.

// minRuntimeBase is the oldest sherpa-onnx release whose C API matches the Go
// wrapper this binary is built with. 2.0.1 widened SpeechSegment.start to
// int64 (every VAD pass reads it) and #4032 appended the Sortformer member to
// the diarization config, so a 1.13.7 library (cassini.1 to .8) would be
// handed structures it lays out differently. Every 2.0.1 build, stock arm32
// included, also has upstream #3999: NeMo models skip the log floor and use a
// FLT_EPSILON/2 guard, which is what made quiet audio decode.
var minRuntimeBase = [3]int{2, 0, 1}

// parakeetTDTJoinerRuntimeMarker is in the version of a runtime that detects a
// NeMo TDT joiner from its output size (output_size > vocab_size) rather than
// from "tdt" in the encoder url. Moondream Parakeet Ultra exports keep the
// plain https://huggingface.co/moondream/parakeet-ultra url, so stock 2.0.1
// exits the process while loading them ("vocab_size: 8193 != output_size:
// 8198"); the marker is what lets them load at all.
const parakeetTDTJoinerRuntimeMarker = ".parakeet-tdt-joiner-v1"

// nemotronDiarizationRuntimeMarker is in the version of a runtime that runs
// the Nemotron-3-Diarization v2 ONNX interface. On a 2.0.1 runtime that means
// the Sortformer segmentation member of k2-fsa/sherpa-onnx#4032; the 1.13.7
// Cassini runtimes carried the same marker for a Pyannote-slot shim this
// binary no longer drives, which is why HasDiarizationRuntime checks the base
// too. Stock libraries (Linux arm32) lack it and cannot diarize.
const nemotronDiarizationRuntimeMarker = ".nemotron-diarization-v2"

// legacyReferenceRuntimeMarker is the frontend marker of the 1.13.7 Cassini
// runtimes (cassini.1 to .8). Nothing selects on it any more; it is named only
// so an error can say what it found.
const legacyReferenceRuntimeMarker = "+cassini-parakeet-v3-reference-v1"

// NativeFrontendNeMoLogGuard names the native feature frontend NeMo models
// get on every supported runtime: sherpa's NeMo fbank with upstream #3999's
// additive log guard. It replaced Cassini's Parakeet reference frontend
// ("parakeet-reference-v1", 1.13.7 runtimes), which provenance written by
// those builds still records as referenceFrontend.
const NativeFrontendNeMoLogGuard = "nemo-log-guard"

// runtimeBase parses the release part of a sherpa version ("2.0.1" of
// "2.0.1+cassini...").
func runtimeBase(version string) ([3]int, bool) {
	version = strings.TrimSpace(version)
	if i := strings.IndexAny(version, "+-"); i >= 0 {
		version = version[:i]
	}
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// runtimeABICompatible reports whether the runtime's C API is the one this
// binary's wrapper was built for (minRuntimeBase or later).
func runtimeABICompatible(version string) bool {
	base, ok := runtimeBase(version)
	if !ok {
		return false
	}
	for i := range base {
		if base[i] != minRuntimeBase[i] {
			return base[i] > minRuntimeBase[i]
		}
	}
	return true
}

// NativeFrontend names the feature frontend a NeMo model runs on with this
// runtime, or "" for a runtime this binary cannot drive.
func NativeFrontend(version string) string {
	if runtimeABICompatible(version) {
		return NativeFrontendNeMoLogGuard
	}
	return ""
}

// CheckNativeRuntime says why this runtime cannot run the model, or nil. A
// runtime older than the wrapper's C API is refused for every model; a model
// that needs runtime markers (Parakeet Ultra: the joiner-size TDT detection)
// is refused where they are missing, because the fallback is not a slower
// decode but a process exit at load.
func CheckNativeRuntime(id ModelID, version string) error {
	if !runtimeABICompatible(version) {
		hint := ""
		if strings.Contains(version, legacyReferenceRuntimeMarker) {
			hint = " (a 1.13.7 Cassini runtime from before the 2.0.1 move)"
		}
		return fmt.Errorf("native sherpa runtime %q%s is older than the sherpa-onnx %d.%d.%d C API this build links: install the libraries shipped with it (sherpa-onnx-go-*/v2 v2.0.1-cassini.1) and check that no older libsherpa-onnx-c-api is on the library path",
			version, hint, minRuntimeBase[0], minRuntimeBase[1], minRuntimeBase[2])
	}
	var missing []string
	for _, marker := range knownModels[id].RuntimeMarkers {
		if !strings.Contains(version, marker) {
			missing = append(missing, marker)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("model %s needs a Cassini sherpa runtime with %s (sherpa-onnx-go-*/v2 v2.0.1-cassini.1 or later); runtime %q would exit while loading it",
			id, strings.Join(missing, ", "), version)
	}
	return nil
}
