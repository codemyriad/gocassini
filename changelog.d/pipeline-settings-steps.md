### Changed

- The Publish pipeline's "After each meeting" card reads as four numbered steps, each in its own panel, with the status strip's chips numbered after them.
- The Transcript step is one list: Off, or a quality tier. Each tier shows its model, its size and whether it is installed, with the Download button on the tier itself; a tier whose model is not ready is greyed. Picking a tier turns transcription on, and so does a download started from the page once the model is ready. The transcription switch and the separate model row are gone.
- The Summary step greys out while there is no transcript, with an amber "needs a transcript" note instead of a switch that could not be used. Its description now says where each transcript goes and that the provider may bill one request per meeting.
- What participants receive is a dropdown that describes each option; "Transcript only" says in place that it needs transcription on. The step, and "What media stays on the server" (formerly "What stays on this server"; its chip reads "Media retention"), stay editable while transcription is off.
