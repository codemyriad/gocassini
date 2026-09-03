# Cassini source-capture companion

This tiny native Nextcloud app loads Cassini's source-audio payload on
authenticated Talk call pages through `LoadAdditionalScriptsEvent`. It does not
contain capture or storage logic; those stay in the `gocassini` ExApp.

Install it only on a Nextcloud that also has Talk, alongside a compatible
`gocassini` version. The ExApp synchronizes its `CASSINI_SOURCE_CAPTURE` switch
— on unless the deployment sets it to `0` — into AppAPI's ExApp config store,
which the listener injects as initial state before Talk's own JavaScript runs.
The listener itself still fails closed: with no value in that store it injects
`enabled: false` and nothing is captured. Joining a call does not start local audio collection:
capture follows Talk's confirmed recording-active/recording-off lifecycle, and
that is the only per-call gate. Telling participants a call is recorded is
Talk's own job, through its recording indicator and its `recording_consent`
setting; neither this app nor the ExApp asks them anything or records
an answer from them.

Anonymous guest calls are intentionally unsupported. Talk does not currently
dispatch `LoadAdditionalScriptsEvent` from its guest page, and Cassini's upload
endpoint requires an authenticated Nextcloud user.
