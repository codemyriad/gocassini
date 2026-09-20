---
title: Recordings live in Nextcloud Files, locked to the people in the call
date: 2026-08-14
version: 0.2.0-beta.1
---

A recording is readable by everyone who had access to the conversation, including people who were invited and never joined. Making a conversation public afterwards does not widen recordings made while it was private. Recordings belong to a `cassini` account rather than to whichever administrator installed the app, so they no longer depend on one person's account surviving.

Enabling the app provisions all of it: the account, the Team folder, and the permissions. No `occ` commands. If a step fails, the app reports an unfinished install and says what went wrong, instead of looking healthy while showing nobody their recordings. Registering Cassini as Talk's recording backend is easier too: the signalling secret is generated on first start if you do not supply one, and an administrator endpoint hands you the exact `recording_servers` value to paste into Talk.

Access control is no longer optional. It used to be a deploy option that defaulted to off, so a stock install shipped without it. If you set `CASSINI_NC_ACCESS_CONTROL`, drop it; nothing replaces it. Installations that published recordings before this update can migrate them in one shot, and enabling or updating the app no longer re-uploads the whole archive.
