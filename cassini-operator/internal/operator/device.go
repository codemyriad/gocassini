package operator

import (
	"strconv"
	"strings"
)

// Device and model identifiers shared by the resource governor, the settings
// API and /status.
//
// The operator and the recorder are separate Go modules, so the tier -> model
// mapping below is a deliberate copy of transcribe.ModelForQuality
// (cassini-go-recorder/internal/transcribe/policy.go). Keep the two in step:
// the operator uses it to size a build's RAM floor and to report the model an
// admitted build will actually load, while the recorder remains the code that
// selects it.

const (
	deviceCPU  = "cpu"
	deviceCUDA = "cuda"

	// modelParakeetUltraFp32 is Moondream's post-trained Parakeet v3 in fp32:
	// every CUDA tier and the CPU "best" tier. fp32 is the only precision the
	// onnxruntime CUDA EP runs without fragmenting back to CPU.
	modelParakeetUltraFp32 = "parakeet-ultra-0.6b"
	// modelParakeetUltraInt8 is its int8 quantization, the balanced CPU tier.
	modelParakeetUltraInt8 = "parakeet-ultra-0.6b-int8"
	// modelParakeet110M is the small CTC model: fastest on CPU, least accurate.
	modelParakeet110M = "parakeet-tdt-ctc-110m-en-int8"
	// modelParakeetV3Int8 and modelParakeetV3Fp32 are NVIDIA's Parakeet v3,
	// which Ultra replaced at the same precision. Retired: an install that
	// still has their files transcribes with them while Ultra is not ready.
	modelParakeetV3Int8 = "parakeet-tdt-0.6b-v3-int8"
	modelParakeetV3Fp32 = "parakeet-tdt-0.6b-v3"
)

// probeNVIDIADevice reports whether an NVIDIA device node is visible. It is a
// package var so a test can exercise both sides of the device decision on any
// host: the CPU-only path matters on a GPU-equipped CI runner just as much as
// the CUDA path matters on a laptop without one.
var probeNVIDIADevice = detectGPU

// isCUDA reports whether a resolved device string names the GPU path.
func isCUDA(device string) bool {
	return strings.EqualFold(strings.TrimSpace(device), deviceCUDA)
}

// cudaCapableHost reports whether this image can run CUDA inference AND an
// NVIDIA device is visible. Raw device visibility alone is not enough: AppAPI
// can install the plain image on a GPU daemon, which then sees /dev/nvidia*
// while carrying no CUDA execution provider. Every decision that depends on
// "will this build actually use the GPU" must ask this, not detectGPU.
func cudaCapableHost() bool {
	capable, _ := imageCUDACapability()
	return capable && probeNVIDIADevice()
}

// speechModelSpec is what the operator needs to know about a speech model. The
// gates below read it instead of comparing ids, so a model shares another one's
// treatment by declaration.
type speechModelSpec struct {
	// cuda: an fp32 model the CUDA path installs and runs.
	cuda bool
	// runtimeMarkers are the native runtime markers the recorder needs before
	// it loads this model; empty for a model without any. They mirror
	// RuntimeMarkers in the recorder's models.go.
	runtimeMarkers []string
	// buildPeakMB and downloadMB: see modelBuildPeakMB and modelDownloadMB.
	buildPeakMB, downloadMB int
}

// parakeetTDTJoinerMarker is in the version of a runtime that detects a NeMo
// TDT joiner from its output size, which plain-url Parakeet Ultra exports need
// to load at all (sherpa-onnx-go-*/v2 v2.0.1-cassini.1).
const parakeetTDTJoinerMarker = ".parakeet-tdt-joiner-v1"

// minRuntimeBase is the oldest sherpa-onnx release whose C API the recorder's
// Go wrapper matches; it mirrors minRuntimeBase in the recorder.
var minRuntimeBase = [3]int{2, 0, 1}

var speechModelSpecs = map[string]speechModelSpec{
	modelParakeet110M: {buildPeakMB: 512, downloadMB: 100},
	// Both Ultra precisions load only where the joiner-size TDT detection is.
	modelParakeetUltraInt8: {runtimeMarkers: []string{parakeetTDTJoinerMarker}, buildPeakMB: 2048, downloadMB: 465},
	modelParakeetUltraFp32: {cuda: true, runtimeMarkers: []string{parakeetTDTJoinerMarker}, buildPeakMB: 3584, downloadMB: 1485},
	// The retired v3 models are never selected or downloaded; a build runs one
	// only as a stand-in for the Ultra model replacing it (admitStandIn). Their
	// tdt url loads on any 2.0.1 runtime, so they need no marker.
	modelParakeetV3Int8: {buildPeakMB: 2048},
	modelParakeetV3Fp32: {cuda: true, buildPeakMB: 3584},
}

// modelSupportsCUDA reports whether the CUDA path installs and runs a model.
func modelSupportsCUDA(model string) bool {
	return speechModelSpecs[strings.TrimSpace(model)].cuda
}

// isSpeechModel reports whether model is one this operator knows how to size
// and check: every one of them needs the 2.0.1 C API, so /status checks the
// runtime for each.
func isSpeechModel(model string) bool {
	_, ok := speechModelSpecs[strings.TrimSpace(model)]
	return ok
}

// modelNeedsRuntimeMarkers reports whether a model loads only on a runtime
// carrying specific markers beyond the 2.0.1 base (Parakeet Ultra), so a stock
// 2.0.1 runtime cannot run it either.
func modelNeedsRuntimeMarkers(model string) bool {
	return len(speechModelSpecs[strings.TrimSpace(model)].runtimeMarkers) > 0
}

// runtimeRunsModel reports whether a native runtime version string, or a
// cassini-native-buildinfo.txt document carrying one, has the 2.0.1 C API and
// every marker this model needs. A model with no markers needs only the base,
// as the recorder's CheckNativeRuntime does; an unknown model is never run.
func runtimeRunsModel(model, info string) bool {
	if !isSpeechModel(model) {
		return false
	}
	version := runtimeVersionFromBuildInfo(info)
	if !runtimeBaseAtLeast(version, minRuntimeBase) {
		return false
	}
	for _, marker := range speechModelSpecs[strings.TrimSpace(model)].runtimeMarkers {
		if !strings.Contains(version, marker) {
			return false
		}
	}
	return true
}

// runtimeVersionFromBuildInfo returns the sherpa= (or sherpa_version=) value
// of a buildinfo document, or the trimmed text when it is a bare version.
func runtimeVersionFromBuildInfo(info string) string {
	for _, line := range strings.Split(info, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && (key == "sherpa" || key == "sherpa_version") {
			return strings.TrimSpace(value)
		}
	}
	return strings.TrimSpace(info)
}

// runtimeBaseAtLeast compares the release part of a sherpa version
// ("2.0.1" of "2.0.1+cassini...") with min.
func runtimeBaseAtLeast(version string, min [3]int) bool {
	if i := strings.IndexAny(version, "+-"); i >= 0 {
		version = version[:i]
	}
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return false
		}
		if n != min[i] {
			return n > min[i]
		}
	}
	return true
}

// modelServesQuality reports whether a prepared model may transcribe for a
// tier on a device: only the tier's own model does.
func modelServesQuality(quality, device, model string) bool {
	return modelForQuality(quality, device) == model
}

// modelForQuality returns the model an admitted build will load for a quality
// tier on a resolved device. Every CUDA tier is fp32 Parakeet Ultra; the CPU
// tiers trade accuracy for speed.
func modelForQuality(quality, device string) string {
	if isCUDA(device) {
		return modelParakeetUltraFp32
	}
	switch normalizeQuality(quality) {
	case sttQualityBest:
		return modelParakeetUltraFp32
	case sttQualityFast:
		return modelParakeet110M
	default:
		return modelParakeetUltraInt8
	}
}

// modelBuildPeakMB is the measured peak resident set of a whole CPU build for
// each model, in MiB, rounded up. Only the CPU path consults it — a CUDA build
// keeps its weights in VRAM and is governed by gpuMinFreeMB instead.
//
// Measured 2026-09-01 (D-702) over 6 minutes of two-speaker audio in the
// operator's own configuration (one recognizer, one stream), which is what
// makes these comparable to a real build:
//
//	parakeet-tdt-ctc-110m-en-int8   320 MiB   0.10x realtime
//	parakeet-tdt-0.6b-v3-int8      1740 MiB   0.09x realtime
//	parakeet-tdt-0.6b-v3 (fp32)    3446 MiB   1.41x realtime
//
// Parakeet Ultra replaced v3 in 2026-10. Over 19 real `cassini build` runs
// (6 threads) Ultra int8 peaked at 1,893 MiB decoding greedily (1,933 in an
// earlier batch), against v3 int8's 1,807; it is charged 2048. Ultra fp32's
// host RSS in CUDA builds was within 2% of v3 fp32's (2,105 vs 2,146 MiB); its
// CPU build was not measured, so it is charged v3 fp32's CPU figure above,
// rounded up to 3584. v3 is still charged for the builds it stands in for
// while Ultra is not ready: int8 2048 (its 1,807 rounded up like the rest;
// before Ultra it was charged 1792 from the D-702 figure), fp32 3584. A
// stand-in thus never needs less than the model it stands in for, which
// admission relies on when that model's check waits for memory.
//
// The fp32 figure is why "best" is not the CPU default: it is the only tier
// that transcribes slower than the meeting it is transcribing.
//
// An unknown model (an operator-side override this table has not caught up
// with) is charged the largest known footprint rather than the smallest:
// refusing to start a build that would have fitted is recoverable, admitting
// one that OOMs the host running Nextcloud and Talk is not.
func modelBuildPeakMB(model string) int {
	if spec, ok := speechModelSpecs[strings.TrimSpace(model)]; ok {
		return spec.buildPeakMB
	}
	switch strings.TrimSpace(model) {
	case defaultDiarizationModel:
		// Its runtime check, the only "build" a diarizer has: about 200 MB
		// measured, with room for the native runtime.
		return 1024
	case "nemotron-3-diarization":
		return 1536
	case defaultEmbeddingModel:
		// Topic suggestions: a measured peak RSS of 0.58 GB embedding
		// batches of texts near the token cap, with room for the native
		// runtime; the same floor the recorder's probe admission uses.
		return 1536
	default:
		return 3584
	}
}

// modelDownloadMB is the approximate download size of each model, in MB, from
// the compressed archive each one ships as. The panel shows it before the first
// build of a tier the image does not bake, so an administrator knows whether
// the wait is seconds or minutes (D-704).
func modelDownloadMB(model string) int {
	return speechModelSpecs[strings.TrimSpace(model)].downloadMB
}
