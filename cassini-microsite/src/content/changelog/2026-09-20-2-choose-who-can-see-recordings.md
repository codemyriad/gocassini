---
title: Choose who can see recordings
date: 2026-09-20
version: 0.2.0-beta.7
---

Recordings can be visible to anyone with a Nextcloud account, or only to the people who were in the call. Until now there was one model, meeting participants, and it needs two Nextcloud apps that an external app cannot install for itself, so an instance without them recorded meetings happily and then could not publish any of them. The choice lives in Operator, Settings, "Who can see recordings", and the meeting list carries a chip saying which is in force.

Switching says what it would do before it does it: how many recordings would move, between which folders, whether the destination already holds any, and whether an earlier switch left a copy behind. Working that out changes nothing, and nothing happens until you confirm. The switch copies the archive, checks that every recording arrived, records the new audience, and only then empties the old folder, so whichever audience is in force that folder holds a complete archive.

A meeting's audience is captured while it runs, when the recording starts and again when it stops, and then frozen. A room that gains or loses members afterwards does not change who can open a recording made before. That also means recordings a switch left visible to everyone can now be narrowed to the people who were in the call, which Cassini previously had no way of working out.
