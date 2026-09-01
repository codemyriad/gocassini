# Cassini source-capture companion

This tiny native Nextcloud app loads Cassini's source-audio payload on
authenticated Talk call pages through `LoadAdditionalScriptsEvent`. It does not
contain capture or storage logic; those stay in the `gocassini` ExApp.

Install it only on a Nextcloud that also has Talk, alongside a compatible
`gocassini` version. The ExApp synchronizes its default-off
`CASSINI_SOURCE_CAPTURE` switch into
AppAPI's ExApp config store, which the listener injects as initial state before Talk's
own JavaScript runs. Participant consent remains a separate, required browser
opt-in. Joining a call does not start local audio collection: capture follows
Talk's confirmed recording-active/recording-off lifecycle.

Anonymous guest calls are intentionally unsupported. Talk does not currently
dispatch `LoadAdditionalScriptsEvent` from its guest page, and Cassini's upload
endpoint requires an authenticated Nextcloud user.
