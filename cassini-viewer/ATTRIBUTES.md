# `<cassini-meeting>` — the public embed contract

Show one Cassini recording — its audio, transcript, speakers, summary and the
tags inside it — in any page, from a URL.

```html
<script src="https://dist.gocassini.com/embed/v1/viewer.js"></script>
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
| `layout` | no | `inline` for a page that already names the recording and shows its details: the viewer drops its title, date line and file details, and the player sits above the transcript instead of floating over it | the full viewer |

Attributes are read when the element enters the page. Changing one afterwards
has no effect; replace the element instead. `theme` is the exception: set it
again and the viewer follows, so a page with its own light/dark switch can keep
the recording in step with it.

## Events

| Event | `detail` | When |
|-------|----------|------|
| `playbackerror` | `{ message }`, a sentence you can show a reader | The browser would not start or could not decode the audio. The viewer shows nothing for this itself. |

The event bubbles from the `<cassini-meeting>` element:

```js
document.querySelector("cassini-meeting")
  .addEventListener("playbackerror", (event) => showNotice(event.detail.message));
```

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

Every Cassini release is also published at its own version,
`/embed/<version>/` (for example `/embed/0.2.0/`), and never changes after that.
Pin one when you need the viewer to stay exactly as you tested it.

Each release attaches the pair to its GitHub release as
`cassini-embed-<version>.tar.gz` (`viewer.js`, `viewer.css`, `SHA256SUMS`). To
host the embed yourself, unpack it into one directory and serve it as described
above.

The embed was first served from `gocassini.com/embed/v1/`. That address now
redirects to `dist.gocassini.com/embed/v1/`, so snippets pasted against it keep
working.

## Styling and isolation

The viewer mounts inside an open shadow root with its stylesheet injected into
that shadow. It does not restyle the embedding page, and the embedding page's
CSS does not reach into it. Size it from the outside:

```css
cassini-meeting { display: block; height: 40rem; }
```

To match the viewer's colours and type to your page, set any of these custom
properties on the element. They reach inside the shadow root. Whatever you
leave unset keeps the value from `theme`.

| Property | What it colours |
|----------|-----------------|
| `--cassini-color-base-100` | The player and raised surfaces, and the playing word's text |
| `--cassini-color-base-200` | The viewer's background |
| `--cassini-color-base-300` | Borders and rules |
| `--cassini-color-base-content` | Text, and playback itself: the playing word and the play button are drawn in the text colour |
| `--cassini-color-primary` | Links, the loading spinner and the selected transcript |
| `--cassini-color-primary-content` | Text on the primary colour |
| `--cassini-font-sans` | The viewer's font stack |

```css
cassini-meeting {
  --cassini-color-base-200: var(--my-page-background);
  --cassini-color-base-content: var(--my-ink);
}
```

These properties are the whole styling contract. Class names inside the shadow
root are not, and can change in any release.

The viewer stays inside its box. Space plays and pauses only when the reader
pressed it inside the viewer; anywhere else on the page, it scrolls the page.
Following playback scrolls the transcript inside the viewer, never the page
around it. The viewer is not a `<main>` landmark, which belongs to the embedding
page. It never reads or changes the page's URL fragment, so the page's
own `#anchors` keep working.
