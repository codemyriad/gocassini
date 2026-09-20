---
title: Tag a meeting, or a stretch of one
date: 2026-09-20
version: 0.2.0-beta.7
---

A tag such as `hiring` can go on a whole meeting or on a stretch of it, and the marks live inside the recording's `.opus` file. A meeting keeps its tags when it is downloaded or shared, and a reader that does not understand them still opens the recording normally.

Tag a meeting from its row in the list, or select several and tag them together. Inside a meeting, select the words and choose Tag selection, then set exactly where it begins and ends with the handles in the text or the arrow keys. Tagged sections are underlined in their tag's colour and arrows step from one to the next. A tag has a colour and, if you want, an icon; Manage tags is where they are renamed, merged and deleted.

The meeting list is organised by room now, with a rooms nav down the left, month headings, and each row showing its room, its date, its speaker count and how long it ran. The tag filter sits beside the rooms, and the search box finds meetings by the names of their tags as well as by what was said in them.

Anyone who can read a meeting can tag it, and every mark is attributed to the Nextcloud account that made it. Two people tagging the same meeting at once no longer overwrite each other. Re-running a meeting keeps the tags added since it was published, and marks made against audio the rerun changed are kept and flagged rather than dropped. From outside the app, `cassini meetings tags`, `annotations` and `annotate` do the same work, and `--tag` narrows a list or a search.
