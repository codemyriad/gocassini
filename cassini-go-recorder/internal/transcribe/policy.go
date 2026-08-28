package transcribe

import (
	"os"
	"strings"
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
// the dynamic-int8 ops back to CPU, so every GPU tier uses fp32 — it is both the
// accuracy ceiling and the fastest real option on a GPU. On CPU the tiers trade
// accuracy for speed: best=fp32, balanced=int8 0.6B, fast=110M CTC.
func ModelForQuality(q STTQuality, device string) ModelID {
	if strings.EqualFold(device, "cuda") {
		return ModelParakeet06BV3 // fp32 v3
	}
	switch NormalizeQuality(string(q)) {
	case QualityBest:
		return ModelParakeet06BV3 // fp32 on CPU = highest accuracy
	case QualityFast:
		return ModelParakeet110M // small CTC = fastest on CPU
	default: // balanced
		return ModelParakeet06BV3Int8
	}
}

// ResolveModelID returns the model to use: an explicit CASSINI_STT_MODEL value
// always wins; otherwise it is derived from the quality tier and device.
func ResolveModelID(explicitModel, quality, device string) ModelID {
	if m := ModelID(strings.TrimSpace(explicitModel)); m != "" {
		return m
	}
	return ModelForQuality(NormalizeQuality(quality), device)
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
