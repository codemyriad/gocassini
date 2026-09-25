# `<cassini-meeting>` — the public embed contract

Show one Cassini recording — its audio, transcript, speakers, summary and the
tags inside it — in any page, from a URL.

```html
<script src="https://gocassini.com/embed/v1/viewer.js"></script>
<cassini-meeting src="https://gocassini.com/nextcloud-conf-2026/talk.opus"></cassini-meeting>
```

This file is the published surface. The implementation is
`cassini-viewer/src/public.ts`.

## Attributes

| Attribute | Required | Value | Default |
|-----------|----------|-------|---------|
| `src` | yes | URL of a Cassini portable `.opus`. Absolute or relative to the embedding page. | — |
| `title` | no | A name for the recording, shown as the heading. | Read from the file name, e.g. `Daily-Standup--2026-03-13--12-00-00.opus` → "Daily Standup", 2026-03-13 12:00 |
| `theme` | no | `light`, `dark`, or `auto` | `auto` — follows the reader's `prefers-color-scheme` |
| `hide-badge` | no | Present to remove the "Recorded with Cassini" footer | absent — the badge is shown |

Attributes are read when the element enters the page. Changing one afterwards
has no effect; replace the element instead.

## The badge

An embed carries a small "Recorded with Cassini" footer linking to
`gocassini.com`, shown by default. It is the only thing on the page that says
what produced the recording, to readers who have no other way to find out.

Remove it with `hide-badge`:

```html
<cassini-meeting src="…" hide-badge></cassini-meeting>
```

## What it shows

Everything the recording carries, and nothing else. There is no server behind a
public embed, so it is **read-only by construction**: the tags and marked
stretches in the file are drawn, and no control is offered that would change
one. This is not a restricted mode of the app — it is the whole of what a file
on its own can honestly support.

Readers can copy or download the transcript they are viewing and download the
whole meeting file. That file plays as audio and can also carry the transcript,
summary and tags. These controls are also available in the Nextcloud meeting
view.

Tag colours are **not** in the file (the format carries a tag's id and label
only, tracked as D-774), so the embed deals each tag a colour from its palette
in the file's own tag order. Colours are stable for a given file and will not
match the same tags seen inside Nextcloud.

## What you must serve

- **`viewer.js` and `viewer.css` together, in one directory.** The script finds
  its stylesheet as a sibling of its own URL. Serving them apart, or mixing
  versions, gives you an unstyled viewer.
- **`Access-Control-Allow-Origin` on the recording.** The script tag itself
  needs no CORS, but the recording is fetched by script, so it is a cross-origin
  read whenever the embedding page is not on the recording's origin.
- **Range requests, ideally.** The viewer asks for the first bytes to read the
  file's manifest and falls back to fetching the whole file when the host
  answers `200` instead of `206`. A host without range support works; a reader
  on a slow connection waits for the whole recording first.

## Versions

`/embed/v1/` is the contract channel. A snippet pasted against it keeps working
and picks up every compatible release without being re-pasted. A breaking change
to the attributes above ships as `/embed/v2/` alongside it, and `/embed/v1/`
keeps answering.

Exact-version pins (`/embed/v1.2.3/`) are not published yet. They are additive —
a new directory beside this one — so they can be introduced without moving
anything, once somebody needs to freeze a version.

## Styling and isolation

The viewer mounts inside an open shadow root with its stylesheet injected into
that shadow. It does not restyle the embedding page, and the embedding page's
CSS does not reach into it. Size it from the outside:

```css
cassini-meeting { display: block; height: 40rem; }
```
