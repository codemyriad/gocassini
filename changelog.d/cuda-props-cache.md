### Fixed
- GPU (`-cuda`) builds are back to their earlier speed. The move to the sherpa-onnx 2.0.1 runtime had made them 22-42% slower, because its ONNX Runtime asks CUDA for the GPU's properties (about 1 ms each) 77 times for every speech segment of a new length. Cassini's CUDA runtime now answers those repeat calls from a cache, which brings a 21-minute meeting from 35 s back to 28 s on an RTX 5060. Transcripts are byte-identical to before.

### Changed
- The native speech runtime is now built from upstream sherpa-onnx's development branch (2.0.1 plus 16 later commits) instead of the 2.0.1 release. The version stays 2.0.1. Transcripts with the default greedy decoding are unchanged. The later commits include an upstream fix to beam search for Parakeet models, which affects only setups that turn name hints back on.
