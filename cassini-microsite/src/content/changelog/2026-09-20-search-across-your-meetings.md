---
title: Search across your meetings, and the names transcription gets wrong
date: 2026-09-20
version: 0.2.0-beta.7
---

Cassini indexes each transcript as it is published and answers with the moments that match: the meeting, the speaker, and where in the recording they said it. You only ever see meetings you can already open. There is a search box above the transcript as well, for finding words inside the meeting you are reading.

Search knows the names transcription gets wrong. Looking for "cassini" also finds the recordings where it was written down as "casino", and each result says whether it matched your words or a known mis-hearing of them. Administrators keep that list in the app's settings, one name per line with its spellings after it, and Cassini ships a starting list.

Every answer says how much of your archive it covered: how many of the meetings you can read were actually searched. A meeting that could not be indexed is reported as outside that coverage rather than passing as one with no matches. Recordings published before the index existed can be added to it with `cassini-operator backfill-search`.
