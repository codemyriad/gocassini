### Changed

- Linux amd64/arm64 CPU builds now use prebuilt Parakeet v3 frontend libraries
  from `codemyriad/sherpa-onnx-go-linux v1.13.7-cassini.4`, with matching upstream
  v1.13.7 Go bindings. Normal developer, Docker and CI builds no longer compile
  sherpa-onnx. CUDA source builds consume a checksum-verified fork commit instead
  of applying a local patch. Other platforms retain stock libraries and the
  graceful fallback. Operator runtime diagnostics coalesce concurrent probes
  and refresh cached results, including unavailable runtimes, after 30 seconds.
