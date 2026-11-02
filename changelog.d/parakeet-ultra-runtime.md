### Changed
- The native speech runtime loads Moondream Parakeet Ultra exports that keep their own model URL, including the plain Ultra URL, instead of exiting. Nemotron diarization is unchanged.
- macOS builds (Apple silicon and Intel) now use Cassini's own sherpa-onnx libraries, built and tested on GitHub runners, instead of the stock ones, so Parakeet Ultra and Nemotron diarization work there too; before, only the fast tier's 110M model transcribed. The Windows libraries are pinned to the matching Cassini release as well. Linux arm32 still uses the stock library.
