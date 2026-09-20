---
title: Embed a recording in any page
date: 2026-09-20
version: 0.2.0-beta.7
---

`<cassini-meeting src="…">` puts one recording in any page from a single script tag, served from `gocassini.com/embed/v1/viewer.js`.

A recording published on its own, in a static export or embedded somewhere, now shows the tags and marked stretches that are inside it, read from the file rather than from a server. The public viewer is read only by construction: the marks are drawn, and no control that would add or remove one appears. It used to hide tags altogether when there was no server behind it to write them.

An embed carries a "Recorded with Cassini" footer by default, linking to gocassini.com. The `hide-badge` attribute removes it.
