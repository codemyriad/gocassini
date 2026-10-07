# Source media disposal

- 🔄 Persist independent publication and source-retention policy at admission.
- ⬜ Enforce transcription and contain temporary media; durable terminal cleanup.
- ⬜ Settings and operator cleanup/rerun UI.
- ⬜ Integration verification, documentation, changelog and draft PR.

The approved behavior keeps capture, publication and source retention independent.
JSON publication can retain source media for reruns. Explicit disposal requires
JSON publication and audio-only capture, deletes media after success or terminal
failure, and blocks source-based reruns. Existing meetings are unchanged.

    saved settings -> admission snapshot -> capture -> build -> seal -> publish
                            |                failure at any stage      |
                            +----------------------> terminal result <-+
                                                          |
                                                   journaled cleanup
                                                          |
                                                   JSON without media
