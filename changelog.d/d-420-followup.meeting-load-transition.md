### Fixed
- Fixed the meeting load-in fade stalling and then snapping into place. The content and the loading overlay were animating the same reveal at once, on different clocks, so their handoff showed as a visible step. The overlay now covers instantly and fades only when revealing, making it the single transition; switching meetings behaves the same as opening the first one.
