# Experimental native Parakeet v3 reference frontend

`parakeet-v3-reference-frontend-sherpa-v1.13.7.patch` applies to upstream
sherpa-onnx **v1.13.7**. It is a research patch for an isolated native build;
it does not modify Cassini's installed dependency or production defaults.

```sh
# From a clean v1.13.7 source checkout:
git apply /path/to/parakeet-v3-reference-frontend-sherpa-v1.13.7.patch
# Build the native runtime separately, then opt in for the experiment:
CASSINI_PARAKEET_REFERENCE_FRONTEND=1 your-isolated-recognizer-command
```

The flag activates only when the encoder metadata URL is exactly
`https://huggingface.co/nvidia/parakeet-tdt-0.6b-v3`, feature dimension is 128,
sample rate is 16 kHz, and normalization is `per_feature`. Unset, zero, and
other values leave the original path unchanged. Other model exports also
retain their existing preprocessing.

The alternate offline frontend buffers the supplied waveform, applies global
preemphasis, and computes centered zero-padded symmetric Hann windows. It uses
Nyquist as the mel upper frequency, `log(mel + 2**-24)`, valid frame count
`floor(samples / 160)`, and centered sample-variance normalization. This matches
the mathematical reference settings tested by `check-parakeet-frontend.py`;
native floating-point and decoder equivalence still require validation.

It does not add decoder silence, alter VAD boundaries, switch search strategy,
rewrite recognized words, or implement stateful streaming. Padding outside the
real waveform exists only for feature extraction. The optional frontend needs
at least two feature frames; shorter inputs return no features.

Touched upstream files:

- `features.h`: internal opt-in flag, false by default.
- `offline-transducer-nemo-model.{h,cc}`: exact metadata identification.
- `offline-recognizer-transducer-nemo-impl.h`: gated activation.
- `offline-stream.cc`: isolated reference preprocessing path.

Before deployment, compare opt-in/off outputs on held-out recordings, confirm
that the disabled build matches the original native runtime, verify timestamp
alignment, and measure CPU/GPU cost. No claim of overall quality improvement is
implied by this patch alone.
