# Optional Parakeet frontend diagnostic

This research tool compares two audio frontends through the **same local ONNX
weights and greedy TDT decoder**. It does not change Cassini's production
recognizer, install dependencies, download models, call an API, or publish data.

Use a separate Python environment with `numpy`, `kaldi-native-fbank==1.22.3`,
and `onnxruntime`. The initial diagnostic used ONNX Runtime 1.30.0 on CPU. Supply
the FP32 Parakeet TDT v3 model directory containing `encoder.onnx`, its external
weights, `decoder.onnx`, `joiner.onnx`, and `tokens.txt`.

```sh
python harness/bin/check-parakeet-frontend.py \
  --model-dir /private/parakeet-tdt-0.6b-v3 \
  --audio /private/exact-participant-crop.wav \
  --output /private/frontend-results.jsonl
```

Input must be mono, 16 kHz, signed 16-bit PCM WAV. Repeat `--audio` for multiple
clips. Default tail conditions are 0 and 500 ms; repeat `--tail-ms` to override.
`--head-ms` defaults to zero. Output creation is exclusive to preserve previous
experiments. JSONL contains recognized text and source paths; keep it private.

The model-reference condition implements centered zero-padded frames, symmetric
Hann windows, Nyquist upper frequency, additive log guard, and sample variance.
Those settings numerically matched NVIDIA's published Transformers frontend on
the investigated clips. The other condition models Sherpa's NeMo frontend
settings; it is **not a byte-exact invocation of the native production runtime**.
Both use stable centered variance. Native versions with different normalization
arithmetic may differ.

This isolates frontend effects under greedy decoding. Production beam search,
hotword bias, VAD, overlap merging, and the energy gate are deliberately absent.
A recovered phrase establishes a useful diagnostic result, not general quality
improvement or a deployable replacement. Test held-out recordings and native
runtime parity before adopting a frontend change.
