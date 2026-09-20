---
title: Cassini sets itself up, and names anything it cannot do
date: 2026-09-20
version: 0.2.0-beta.7
---

Installing used to mean running `occ` commands by hand. Cassini now offers to make those changes itself: the `cassini` service account and its group, the Team folder, its group mappings, advanced permissions, and the permission manager delegation. It lists every change before it makes any of them and performs them as you, with Nextcloud asking you to confirm your own password in its own dialog. Cassini never sees it, stores it or sends it on. The `occ` recipe is still there if you would rather run it yourself, generated from the same plan, so the two cannot disagree.

Anything Cassini cannot do for itself is named, together with the command that fixes it: the service account, each Nextcloud app, the Team folder, its group mappings, its permissions setting and its permissions manager. Installing the two Nextcloud apps is the one step that stays yours, and it says so.

The Setup tab and its wizard are gone. Cassini no longer opens on a question that blocks every recording until somebody answers it. Where storage is genuinely broken you get one sentence saying recordings cannot be saved right now, with Try again and Show details; the steps, paths and commands live inside that disclosure. Anyone who is not an administrator gets the same sentence and no buttons.
