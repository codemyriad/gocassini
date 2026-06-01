# D-288 Play Commands — Follow-ups

Source: PR #43 body (`https://github.com/codemyriad/gocassini/pull/43`) said the initial result worked but left two next steps:

> I'm not too happy, the next steps are to:
> - perhaps find a better meeting (this plays weird sounds after 5secs)
> - find a way to simulate a private 1:1 meeting

## 1. Replace the Lantern Festival fixture with a better synthetic meeting

**Status:** not done in the initial D-288 cut.

Current state:

- `cassini dev play` defaults to the committed Lantern Festival showcase fixture.
- The PR notes that this fixture "plays weird sounds after 5secs," making it a poor default for manual recorder testing.

Follow-up work:

- choose or generate a better speech-like synthetic meeting fixture
- prefer the existing Pied Piper synthetic meeting path if available
- preserve fixture caching so normal playback does not regenerate media unnecessarily
- update `cassini dev play` full/single mode defaults and launch output to identify the new fixture
- update tests and validation docs around the new default media

## 2. Simulate a private 1:1 meeting

**Status:** not done in the initial D-288 cut.

Current state:

- `cassini dev play` plays into a Talk room resolved by display name.
- The initial flow is suitable for public/group-room style harness playback.
- It does not scaffold Nextcloud users, private conversations, or private-call setup.

Follow-up work:

- create a scaffold flow for two synthetic users from the selected meeting fixture
- create private Talk conversations between those users and between `admin` and the first synthetic user
- let the existing play command start/play into one of those private conversations
- ensure the recording path exercises Nextcloud's integrated recorder end-to-end rather than using the operator API to start recording
- keep the workflow scriptable enough for repeatable manual validation
