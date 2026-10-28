### Changed
- Parakeet Ultra replaces Parakeet v3; installs download it on upgrade. Moondream's Parakeet Ultra 0.6B (CC-BY-4.0), a post-training of NVIDIA Parakeet 0.6B v3, is now the model of every quality tier on a GPU (fp32) and of Balanced (int8) and Best (fp32) on a CPU; Fast stays the 110M model. An install set to a v3 model moves to the Ultra model of its tier on its device, downloads it (Settings shows the progress), and new recordings wait for it before they are transcribed; v3's files are deleted once Ultra has passed its check. A recording made before the upgrade under "delete audio after processing" is transcribed with Ultra too, and keeps its audio for as long as Ultra is still being installed. If the download could not be queued, or the operator stopped before it was, the next start queues it again. Existing transcripts are unchanged.
- The App Store listing rates speech-to-text with Parakeet Ultra 🟠, because Moondream has not published its post-training code or data; the CPU's Fast tier (NVIDIA Parakeet 110M) stays 🟡.
- A CPU build of the balanced tier now waits for 3 GiB of free memory instead of 2.75 GiB, after new measurements of its peak.

### Added
- Transcripts record the url and comment of the encoder that ran.

### Fixed
- The `<unk>` placeholder a speech model emits for an unrecognised sound no longer appears in transcripts.
