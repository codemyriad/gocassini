### Added
- Bundle a pinned Ling-3.0-tiny Q4 model and managed llama.cpp runtime alongside
  STT in the CPU/CUDA ExApp images. Local meeting summaries are an opt-in pilot
  (`CASSINI_SUMMARY_BACKEND=local`), need no API key, release memory after use,
  and reject context overflow and incomplete output without blocking transcripts.
  Existing remote summary configuration remains the default. The CUDA base moves
  to CUDA 12.8.1 for Blackwell support; model weights add approximately 4.8 GB.
