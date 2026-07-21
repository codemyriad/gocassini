### Changed
- Raised the meeting filter's text from 12px to 14px, which the small-input size had pinned to its minimum.

### Fixed
- Fixed the meeting filter drawing a second, squared-off focus ring inside its own. The field is a daisyUI wrapper that shows focus on the wrapper, and daisyUI suppresses the inner control's ring — but the app's global focus ring is unlayered, so it overrode that reset regardless of specificity.
