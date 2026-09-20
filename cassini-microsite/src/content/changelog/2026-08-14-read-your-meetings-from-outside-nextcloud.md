---
title: Read your meetings from outside Nextcloud
date: 2026-08-14
version: 0.2.0-beta.1
---

`cassini meetings` runs on a laptop or in a container. It signs in with an app password, lists what that account may see, and either fetches a meeting's `.opus` file or prints its transcript and summary in a form a script or an agent can read.

The account's permissions are the only permissions. There is no separate key and no second access path, so nothing is exposed that the person could not already open in Nextcloud.
