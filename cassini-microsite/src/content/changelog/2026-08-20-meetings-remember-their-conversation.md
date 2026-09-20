---
title: Meetings remember which conversation they came from
date: 2026-08-20
version: 0.2.0-beta.2
---

Until now a recording's room name was flattened into its title and the conversation's identity was thrown away. Meetings now carry the room itself, and the meeting list groups and filters by it.

The room published on a meeting is a one way derivation of the conversation's identity, never the conversation's own token, so a public conversation's token cannot be recovered from a published file. `CASSINI_ROOM_ID_PEPPER` controls that derivation.

Recordings published before this update can be given their room back: run `scripts/backfill-catalog-rooms.sh` once, on the host where the app container runs. Until you do, one conversation can appear as two rooms, one identified from its token for recordings made since the update and one from its name for everything older. Running it again merges them.
