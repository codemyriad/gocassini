### Changed
- Retention settings use plainer names on every screen: Recordings, Published meetings (on this server), Previous attempts, Processing logs and Published meetings in Nextcloud. The reminder banner is one sentence with Review and Dismiss.
- The Storage report shows two totals, On this server and In Nextcloud Files, then space by type and by date. Previous attempts opens onto its four kinds, and each date chart picks its own bar width.
- The retention editor shows each category as a heading, one line and its control. Previous attempts splits into its four kinds with a link, the daily cleanup is one row with a timezone list, and Discard changes appears only when there are unsaved edits.
- New installations run the daily cleanup at 03:42 UTC instead of 02:00 UTC. A saved cleanup time is kept.
- Publishing asks what people receive first, then what stays on the server. Keeping nothing is unavailable while audio is published.
- Transcription has its on/off switch in the heading, and the model shows as one row with its size, state and one action.

### Fixed
- The "Who can see recordings" card and, in high-contrast themes, the "Create an insight" card now have the same inner padding as the cards around them.
- The dot or icon in a tag chip sits on the centre of its label in every browser, including the tag filter in the rooms rail and the tag picker.
