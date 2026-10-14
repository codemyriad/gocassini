### Added
- Moondream's Parakeet Ultra 0.6B (fp32, CC-BY-4.0), a post-training of NVIDIA Parakeet 0.6B v3, is a new speech model. Transcripts record the url and comment of the encoder that ran.

### Changed
- On a GPU every quality tier now uses Parakeet Ultra. The CPU tiers are unchanged. A GPU install already using Parakeet 0.6B v3 keeps it until an administrator installs and selects Ultra in Settings, and can go back to v3 at any time.
- A CPU build of the balanced tier now waits for 3 GiB of free memory instead of 2.75 GiB, after new measurements of its peak.
- The App Store listing rates speech-to-text on a GPU separately: 🟠, because Moondream has not published its post-training code or data.

### Fixed
- The `<unk>` placeholder a speech model emits for an unrecognised sound no longer appears in transcripts.
