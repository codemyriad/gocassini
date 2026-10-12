### Fixed
- Search results keep the meeting's title and details readable at any width: the matched transcript lines sit under them, and beside them only when the list is wide. The meeting's length no longer runs into the first timestamp.
- Clicking a matched transcript line opens that moment again; the row's click area had been covering it.

### Changed
- Opening a meeting from a search that matched what was said fills the meeting's transcript search with the same words, as opening one of its moments already did.
- The app asks for its stylesheet by build, not only by app version. An update that keeps the version number (a rebuilt image, a hot-deploy) could leave a browser pairing the new script with an older stylesheet, which unstyled every screen built from components.
- On wide lists the matched transcript lines take more room and start further left, while the meeting's title, date and tags keep about 34rem.
- "Who can see recordings" in Settings and the "Shared with participants" note in the rooms rail say the same three points, as a list. The note's last line no longer wraps early.
