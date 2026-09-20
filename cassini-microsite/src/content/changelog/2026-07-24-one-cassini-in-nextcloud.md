---
title: One Cassini in Nextcloud, with the recording controls inside it
date: 2026-07-24
version: 0.2.0-alpha.4
---

Cassini used to add two entries to the Nextcloud menu, one to browse recordings and one to control them. Now there is one. Everyone who is logged in gets the meeting browser, and administrators get a Browse and Operator switch inside it. Recording controls stay administrator only; that boundary has not moved.

You can link straight to a recording, the back button returns you to the list, and the panel tells you in one word whether it is connected.

The other half of this release is calls that finished empty. A participant whose handshake completed but whose audio never arrived could leave a short call with nothing to transcribe, and turning a camera on mid call could silently lose the rest of that person's video. Both are fixed.
