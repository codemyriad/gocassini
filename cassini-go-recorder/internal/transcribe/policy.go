package transcribe

import (
	"os"
	"strings"

	"gocassini/internal/modelstore"
)

// STTQuality selects a hardware-adaptive (device, model, threads) policy. It
// lets a deployment ask for an outcome ("best accuracy", "fastest") and have the
// recorder pick the concrete model/device for the host it is actually running
// on, instead of hard-wiring a single model that is wrong on half the hardware.
type STTQuality string

const (
	// QualityBest maximises accuracy: fp32 on every device (fp32 is the quality
	// ceiling and is byte-identical on CPU and GPU).
	QualityBest STTQuality = "best"
	// QualityBalanced is the default: best-quality-that-is-also-fast per device.
	// On a GPU that is fp32 (better AND faster than int8, which fragments to
	// fp32 under the CUDA EP anyway); on CPU it keeps int8 0.6B for speed.
	QualityBalanced STTQuality = "balanced"
	// QualityFast favours speed: fp32 on GPU (the fast option there); the small
	// 110M CTC model on CPU (non-autoregressive, much faster, lower accuracy).
	QualityFast STTQuality = "fast"

	// DefaultQuality is used when CASSINI_STT_QUALITY is unset. Balanced keeps
	// the historical CPU default (int8) while upgrading GPU boxes to fp32.
	DefaultQuality = QualityBalanced
)

// NormalizeQuality maps a raw (possibly empty / mixed-case) value to a known
// tier, defaulting to balanced for unset or unrecognised input.
func NormalizeQuality(s string) STTQuality {
	switch STTQuality(strings.ToLower(strings.TrimSpace(s))) {
	case QualityFast:
		return QualityFast
	case QualityBest:
		return QualityBest
	default:
		return QualityBalanced
	}
}

// DetectDevice reports the best STT device available on this host: "cuda" when
// an NVIDIA GPU is present, otherwise "cpu". The device-node check is a cheap,
// reliable proxy — the CPU container image has no /dev/nvidia* nodes, while a
// CUDA-enabled deployment binds them in. doctor / EnsureModel still validate
// that the chosen model is actually usable, so a false positive fails fast
// rather than silently mis-transcribing.
func DetectDevice() string {
	if hasNVIDIAGPU() {
		return "cuda"
	}
	return "cpu"
}

func hasNVIDIAGPU() bool {
	for _, node := range []string{"/dev/nvidia0", "/dev/nvidiactl"} {
		if _, err := os.Stat(node); err == nil {
			return true
		}
	}
	return false
}

// ResolveDevice turns an explicit device value ("", "auto", "cpu", "cuda") into
// a concrete device, auto-detecting a GPU when unspecified. An explicit "cpu"
// or "cuda" always wins.
func ResolveDevice(explicit string) string {
	switch strings.ToLower(strings.TrimSpace(explicit)) {
	case "cpu":
		return "cpu"
	case "cuda":
		return "cuda"
	default: // "", "auto", anything unrecognised
		return DetectDevice()
	}
}

// ModelForQuality picks the STT model for a quality tier on the resolved device.
//
// fp32 is the only precision the onnxruntime CUDA EP runs without fragmenting
// the dynamic-int8 ops back to CPU, so every GPU tier uses fp32 Parakeet Ultra:
// it is both the accuracy ceiling and the fastest real option on a GPU. On CPU
// the tiers trade accuracy for speed: best=Ultra fp32, balanced=Ultra int8,
// fast=110M CTC.
//
// The operator (cassini-operator/internal/operator/device.go) and the app
// (cassini-app src/speechModels.ts, src/operator/pipelineStatus.ts) keep copies
// of this map; change all four together.
func ModelForQuality(q STTQuality, device string) ModelID {
	if strings.EqualFold(device, "cuda") {
		return ModelParakeetUltra
	}
	switch NormalizeQuality(string(q)) {
	case QualityBest:
		return ModelParakeetUltra
	case QualityFast:
		return ModelParakeet110M
	default: // balanced
		return ModelParakeetUltraInt8
	}
}

// ResolvedModel is the model a build or a check uses.
type ResolvedModel struct {
	ID ModelID
	// Revision is the pinned revision, or empty for the catalogue's current one.
	Revision string
	// Replaces is the retired id the configuration named, when ID stands in
	// for it; empty otherwise.
	Replaces ModelID
}

// ResolveModel returns the model to use: an explicit CASSINI_STT_MODEL value
// always wins; otherwise it is derived from the quality tier and device.
//
// An explicit id that an earlier version shipped and the catalogue has since
// retired (Parakeet v3) resolves to the model that replaced it, at the same
// precision, and its revision pin is dropped: it named the retired model's
// files. An id the catalogue never knew stays as it is, so the build reports it
// as unavailable instead of quietly transcribing with something else.
func ResolveModel(explicitModel, explicitRevision, quality, device string) ResolvedModel {
	id := ModelID(strings.TrimSpace(explicitModel))
	revision := strings.TrimSpace(explicitRevision)
	if id == "" {
		return ResolvedModel{ID: ModelForQuality(NormalizeQuality(quality), device), Revision: revision}
	}
	if replacement, ok := ReplacementModelID(id); ok {
		return ResolvedModel{ID: replacement, Replaces: id}
	}
	return ResolvedModel{ID: id, Revision: revision}
}

// ResolveModelID is ResolveModel without a revision pin.
func ResolveModelID(explicitModel, quality, device string) ModelID {
	return ResolveModel(explicitModel, "", quality, device).ID
}

// ReplacementModelID returns the shipped model that replaced a retired id, and
// whether id is retired. A model this binary knows is never retired.
func ReplacementModelID(id ModelID) (ModelID, bool) {
	if _, known := knownModels[id]; known {
		return id, false
	}
	if m, ok := modelstore.Shipped().Replacement(string(id)); ok {
		return ModelID(m.ID), true
	}
	return id, false
}

// DefaultNumThreads returns a sensible intra-op thread count for sequential
// transcription: all cores, capped to avoid oversubscription. (Replaces the
// historical hard-coded 4, which left most cores idle on CPU-only boxes.)
func DefaultNumThreads() int {
	n := detectOnlineCPUs()
	switch {
	case n < 1:
		return 1
	case n > 16:
		return 16
	default:
		return n
	}
}

// DefaultNumThreadsForDevice keeps CUDA inference from creating a large CPU
// thread pool alongside the GPU execution provider. A single host thread was
// sufficient for the CUDA path in the transcription audit and keeps CPU/RAM
// pressure predictable; CPU inference retains the core-count-derived default.
func DefaultNumThreadsForDevice(device string) int {
	if strings.EqualFold(strings.TrimSpace(device), "cuda") {
		return 1
	}
	return DefaultNumThreads()
}

// vadProvider returns the execution provider for Silero VAD. It defaults to CPU
// regardless of the recogniser device: VAD runs a tiny model per 32 ms window,
// which is latency-bound and pathologically slow on a GPU (millions of micro
// kernel launches — measured ~3x slower on sparse/long streams). Override with
// CASSINI_VAD_DEVICE only for experimentation.
func vadProvider() string {
	if v := strings.TrimSpace(os.Getenv("CASSINI_VAD_DEVICE")); v != "" {
		return v
	}
	return "cpu"
}
