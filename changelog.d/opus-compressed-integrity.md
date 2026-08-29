### Changed
- New portable meetings identify their recording from canonical compressed Opus packets instead of FFmpeg-decoded PCM, making integrity checks decoder-version independent while retaining exact-PCM verification for existing v1/v2 files.
