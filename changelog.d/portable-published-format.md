### Added
- The `scripted` transcript role, for authored text a recording is a performance of: a read script, a song's words. It is not a transcription, so it never names a source transcript.

### Changed
- Portable meeting producers and readers now implement only the published version 1 contract. Transcript bodies contain word items, the media digest comes from `integrity.opusAudioSha256`, and unsupported shapes are not repaired or upgraded.

### Fixed
- A portable `.opus` whose transcript body cannot be read is no longer called a broken file. That transcript is unavailable, named in a warning, and the meeting, the speakers and the other transcripts still read. `cassini inspect` still exits non-zero.
- A repeated load-bearing tag now stops `cassini inspect` from printing any part of the manifest. It used to declare the metadata invalid and then print the title, the meeting id, the speakers and every descriptor anyway.
- Transcript bodies are verified against the manifest's `payloadRef` rather than the `CASSINI_TX_*` tags that copy it. A corrupted body whose tag happened to agree with it was accepted, and a good body whose copied tag had drifted was rejected; a disagreement is now a warning.
- The transcript a reader opens is the one `cassini inspect` marks `default=yes`, including in a file where no entry carries the flag and array order decides.
- `words=` is the number of items read out of the chunk set, not the count the body declares.
- Model ids containing `_` no longer produce a transcript id the packer rejects, which let a bundle build and then fail at packing.
- The exported transcript JSON keeps one segment per speaker turn. It used to collapse a multi-speaker transcript into a single segment with no speaker at all.
- `cassini inspect` exits non-zero on unusable manifest metadata instead of printing a summary and reporting success.
