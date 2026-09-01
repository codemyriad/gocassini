package operator

import "strings"

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

	// modelParakeetV3Fp32 is the accuracy ceiling on both devices, and the only
	// precision the onnxruntime CUDA EP runs without fragmenting back to CPU.
	modelParakeetV3Fp32 = "parakeet-tdt-0.6b-v3"
	// modelParakeetV3Int8 is the balanced CPU model.
	modelParakeetV3Int8 = "parakeet-tdt-0.6b-v3-int8"
	// modelParakeet110M is the small CTC model: fastest on CPU, least accurate.
	modelParakeet110M = "parakeet-tdt-ctc-110m-en-int8"
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

// modelForQuality returns the model an admitted build will load for a quality
// tier on a resolved device. Every CUDA tier is fp32 v3; the CPU tiers trade
// accuracy for speed.
func modelForQuality(quality, device string) string {
	if isCUDA(device) {
		return modelParakeetV3Fp32
	}
	switch normalizeQuality(quality) {
	case sttQualityBest:
		return modelParakeetV3Fp32
	case sttQualityFast:
		return modelParakeet110M
	default:
		return modelParakeetV3Int8
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
// The fp32 figure is why "best" is not the CPU default: it is the only tier
// that transcribes slower than the meeting it is transcribing.
//
// An unknown model (an operator-side override this table has not caught up
// with) is charged the largest known footprint rather than the smallest:
// refusing to start a build that would have fitted is recoverable, admitting
// one that OOMs the host running Nextcloud and Talk is not.
func modelBuildPeakMB(model string) int {
	switch strings.TrimSpace(model) {
	case modelParakeet110M:
		return 512
	case modelParakeetV3Int8:
		return 1792
	case modelParakeetV3Fp32:
		return 3584
	default:
		return 3584
	}
}
