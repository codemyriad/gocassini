### Fixed
- When transcription is on but its saved model's files are missing, Settings says so: the transcript step shows a warning instead of "In use", and the status strip's Transcribe step turns amber with "model not installed". Recordings published in that state have no transcript.
- Downloading the voice separation model no longer shows "until the model is ready" on the Publish step, and that note no longer appears while transcription is on.
- Voice separation's "(optional)" is lighter and smaller than its heading.
- A model installed but not yet checked against the current build reads "Installed · checked before the next recording" instead of "runtime check needed".

### Changed
- The hardware details under "Change" are one box: GPU, CPU cores, memory and the device choice as label and value, then one tip. The device choice warns only when it names a GPU this server doesn't have.
- The line above Quality reads as one sentence ("Transcribing with Parakeet TDT 0.6B v3 — fp32 on the CPU (no GPU found).") with a Hardware button that opens the details.
- Browsers revalidate the app's script and stylesheet on every load, so an update that keeps the version number no longer leaves an old stylesheet in place.
- A model download says what happens after it finishes: "Downloaded · checks once no meeting is being processed", then "Checking it runs on this server", instead of sitting at "installed · 100%". The progress bar shows only while downloading.
