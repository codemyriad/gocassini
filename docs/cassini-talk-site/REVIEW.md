# cassini.talk site: review notes

Branch `feat/cassini-talk-site`, local only (not pushed, no PR). The plan was built in one pass, with one commit per phase:

| Commit | What |
|---|---|
| `dac8aa43` | The plan and `design-reference/`, as their own commit |
| `9e9c172a` | Phase 1: scaffold |
| `44e93566` | Phase 2: Download, Help, help article |
| `b8afd49b` | Phase 3: Home, static |
| `af5085c1` | Phase 4: Home, in motion |
| `0f8adee4` | Phase 5: QA fixes |
| `85f8e0b8` | Phase 6: deploy workflow |

`cassini-microsite/` and `microsite.yml` are unchanged (`git diff main..HEAD -- cassini-microsite .github/workflows/microsite.yml` is empty).

## Design pass after the build

After the six phases were built, a review session in the browser refined the design. These changes are deliberate, so the site no longer matches `design-reference/shots/` in the places listed here. The phase sections below still describe the build as first done.

**Surfaces**
- The hero demo card, the bento tiles, the Download cards, the Help cards, the FAQ items and the .opus tree boxes have no border; their fill alone defines them.
- The Home platform tiles have no border. On hover or keyboard focus they lighten, lift 3px and gain a soft shadow (no lift with reduced motion).
- The Nextcloud cross-link cards on Download and Help are outline with no fill, like the numbers row: they point off the site, so they read as asides.
- Borders stay on controls: search fields, the hero search pill, icon tiles, the Mark button, the footer chips and the filter tabs.
- The facts strip under the bento has no dividers, and has more space above it.
- The closing band sits on the page ground instead of the raised surface, with no top rule, so the page descends straight into the footer.

**The footer is deep space**
- A band that darkens from the page ground to `oklch(11% 0.018 258)` along an eased curve, with a seeded, static starfield (`Footer.astro`) that fades in with it. No planet, no motion.
- The fade and the star mask are sized from the space area's height (`--space-h`), not the whole footer, so on phones they settle above the stacked links.
- The space area is `clamp(120px, min(15vw, 22vh), 210px)` on desktop and `clamp(96px, 16vh, 128px)` on phones, so short screens get less of it.
- Layout: links left and chips right on the first row; the Nextcloud line left and © right on the second. Links stay at the nav's 14px; the line below and the © are 13px. On phones it stacks into links, the Nextcloud line, then a sign-off of chips and © under a faint hairline.

**Copy (Home)**
- Bento heading: "Better than taking notes", with no subline.
- The FAQ has no "Questions" heading; the section keeps it as a hidden label.
- Numbers row captions, all balanced to two lines:
  - 5: "Apps available on iPhone, iPad, Android, Mac, Windows, Linux"
  - 1: "File per recording, with the transcript and speakers inside"
  - 0: "Bytes leave your device. Everything stays in local storage"
- Download: "Build it yourself" description uses the full width (one line from 1024 up).

**Motion**
- Reveal: 0.75s per item (the plan said about 450ms), and items revealed together are staggered 110ms apart in reading order, capped at 660ms.
- The nav no longer crossfades during page transitions; it swaps instantly, so its semi-transparent GitHub icon doesn't flash brighter.

**Spacing**
- The closing band's padding is 48px top and 32px bottom on desktop (was 96 and 96), and 32 and 24 on phones. At 1270 × 779 and up, the whole closing section fits under the nav at the end of the page.

**Performance**
- The hero's 44 waveform bars are clipped (`clip-path: inset(… round 2px)`) instead of resized, so they no longer trigger layout 30 times a second. They are pixel-identical to the old bars at desktop widths; on phones, where bars are 2.58px wide, a few cap pixels antialias a shade differently.
- `--p` (the step rail fill) is a registered, non-inherited `@property`.
- The ticker sleeps until the next 30fps frame instead of waking on every display refresh.
- Over one full hero loop at 1440: layout ~50ms → ~5ms, style ~479ms → ~370ms, all main-thread work ~2,630ms → ~2,180ms. With no demo in view, still 0.

**Typeface: the system stack stays.** Nine web fonts and eleven serifs were compared on the real Home sections. The decision was to keep `ui-sans-serif, system-ui, sans-serif`, as gocassini.com does. On macOS, every greeting on Home (Latin Extended, Cyrillic including Ukrainian, Greek, Maltese) was verified to draw in SF Pro with no fallback. Segoe UI and Roboto cover the same scripts; Linux depends on the distro and may substitute Noto or DejaVu for some words. Not verified on Windows, Android or Linux.

**Page weight** (gzip, first visit): Home ≈ 55 KB, Download ≈ 33 KB, Help ≈ 30 KB, article ≈ 30 KB. No images or web fonts.

**Still open from the review**
- A privacy policy page: the footer's Privacy link goes to the help centre's Privacy and storage card, but the App Store and Google Play both require a privacy policy URL.
- A 404 page: a bad URL currently gets Cloudflare's bare 404.
- The `#` store buttons jump to the top of the page; until the real links exist they could show as disabled ("Coming soon").
- Unwritten help guides look like links but aren't; dim them or list only written ones.
- At 1280 × 720 and on small phones (390 × 664), "Keep your conversations." still slides about 3–21px under the nav at the very end of the page.

## What was built

### Phase 1: scaffold
- `cassini-talk-site/` is a copy of `cassini-microsite/` with the same versions and lockfile (Astro 4.16, Tailwind 4, DaisyUI 5). `npm ci` installed from the copied lockfile with no release-age or install-script problems, and no package was added in any phase.
- Removed, as the plan lists: the embed, `sync-embed.mjs`, the conference page, the changelog, the gocassini docs, `hero-screenshot.webp`, `og-card.png` and `_redirects`. Also removed, because nothing uses them any more: `docs-nav.ts`, the old logo assets and `nc-logo.svg`.
- Renamed `package.json` to `cassini-talk-site` and set `site` to `https://cassini.talk`. In `wrangler.jsonc` the Worker is named `cassini-talk`, with `workers_dev` on and no routes.
- One `saturn-night` theme in `src/tailwind.css`, as both the default and the `prefersdark` theme. That file also holds:
  - all the mock `<helmet>` CSS, once, with its prefixes kept;
  - the motion tokens;
  - the reveal;
  - the cross-document View Transitions rules.
- New components:
  - `Nav` and `Footer`, ported from AppsNav and AppsFooter;
  - `SaturnMark`, the inline true-colour SVG at `nav`, `mid` or `full` detail, with a `prefix` prop that re-prefixes its IDs;
  - `Head`, `Glyph`, `NextcloudLogo` and `DownloadIcon`.
- `SocialMeta.astro` and `Card.astro` were kept, as the plan says. `Card` is not used by any page.
- `.claude/launch.json` was recreated on port 4323. It isn't committed, because `.claude/` is in your global gitignore. That's also why the microsite's copy isn't tracked.

### Phase 2: Download, Help and the help article
- **Download** reads `src/data/apps.ts`. Placeholder facts are flagged in the data (see "Open follow-ups").
- **Help** reads `src/data/help.ts`. It has the search field (with a working `/` shortcut) and the platform filter as keyboard tabs.
- **Help article:** "Transcribe on your device" is the first entry in a `help` content collection, at `src/content/help/transcribe-on-your-device.md`. The adapted `DocsLayout.astro` renders it. Its warning uses the existing GitHub-alert remark plugin, restyled as the mock's tinted card.

### Phase 3: Home, static
- Each section is its own component in `src/components/home/`.
- The mock's `renderVals()` maths is ported to pure modules in `src/demos/`: `hero`, `nums`, `search`, `voice`, `mark`, `file` and `voices`. Each module returns the bound style, text and attributes per element, keyed by `data-k`.
- The components render the reduced-motion still frame from those modules at build time. The page is therefore complete and readable without JS.

### Phase 4: Home, in motion
- `src/scripts/ticker.ts` is one shared 30fps clock. It runs only the demos that are in view. Each demo restarts at frame 0 whenever it enters the viewport, and the restart snaps there with transitions off.
- `src/scripts/home.ts` applies the same Phase 3 modules every frame, through a `Binder` that writes only the values that changed.
- **Hero steps:** they work as tabs, by click or with the arrow keys, Home and End.
- **Search tile:** if you type in it, it searches your text, as in the mock.
- **Language ring:** it stays CSS-only. Its animations restart on entry and pause when off screen.
- **Reveal:** scroll-triggered, with `visibility` and an animation, never an opacity transition.
- **Page transitions:** a native cross-document crossfade with the nav held still. Astro's `<ViewTransitions />` router is not used.
- **Reduced motion:** the demos stay on their still frames, and the reveal and page transitions are off.

### Phase 5: QA
Two fixes came out of QA (see "Decisions" below):
- **Keyboard reach:** content still waiting to reveal was out of the tab order.
- **320px nav:** the nav overflowed below 360px.

### Phase 6: deploy
- `.github/workflows/cassini-talk-site.yml` is modelled on `microsite.yml`, without the embed job. It runs `npm ci`, lint and build on PRs, and on pushes to `main` it deploys that exact `dist` with `wrangler deploy`.
- Nothing was deployed: wrangler was not run and nothing on Cloudflare changed.

## How it was checked

All screenshots come from headless Google Chrome. A small script in the session scratchpad drives it over the DevTools protocol using Node's built-in WebSocket; nothing was added to the repo. Diffs are per pixel, with a threshold of 24 out of 255.

| Check | Widths | Result |
|---|---|---|
| Download, Help, article first screen vs `shots/`, reduced motion | 1440, 390 | Match within 2–7 antialiased pixels (the nav's Download button corner) |
| Download and Help vs `shots/`, motion on, after the reveal | 1440 | Match within 2–7 pixels |
| Home vs `shots/`, reduced motion | 1440, 390 | Same page height (5355 and 6858 px). The only differing areas are listed under "Differences" |
| Home with JS disabled | 1440 | Same as reduced motion, except the language ring, whose CSS animation keeps running |
| All four pages, by eye | 1024, 768 | Layouts follow the mock's own breakpoints. No reference shots exist at these widths |
| Horizontal overflow, console errors and warnings, motion on | 1440, 1280, 1024, 900, 768, 600, 390, 360, 320 | None, on all four pages |
| Keyboard, real key events | 1440, 390 | See below |
| Demos pause off screen (DevTools Performance metrics over 3 s) | 1440 | See below |
| Lint, build, `tsc --noEmit` | n/a | Pass |

**Keyboard results:**
- Tab order is the nav, then the selected hero step (roving tabindex), then the rest of the page.
- The arrow keys, Home and End move the hero steps, and the demo jumps with them.
- Focus rings are visible.
- The article's guide list opens from its toggle and closes on Escape.

**CPU results:**
- With no demo in view, script, style and layout time are 0 ms, the same as the static Download page.
- With only the hero in view, the page spends about 160 ms on style and about 630 ms on main-thread tasks per 3 s, in software rendering.

**Not verified:**
- The page-transition crossfade itself. The rule is present, Chrome reports `CSSViewTransitionRule`, and the nav has its `view-transition-name`, but no navigation was captured mid-transition.
- Safari and Firefox.
- Real phones and tablets.
- How the reveal animation and the demo motion feel in a real browser. Frames were sampled, not watched.
- GPU-composited CPU cost on a real machine.

## Differences from the mocks I couldn't resolve

- **Two-voice lanes and closing wave:** they use the mock's random conversation generator. For the static still it is seeded so builds are reproducible, which means their bars can't match the shot, which caught one random run. Layout, colours and counts match.
- **One transcript line in the .opus tree:** sub-pixel glyph antialiasing differs ("Chris The best ideas…"). It's not a layout difference.
- **Nav below 360px:** the mock stops at 390, so I added a fix: the GitHub icon is hidden below 360px so the Download button fits. It's unchanged from 390 up.
- **Unwritten help guides:** the mock links every guide title to the one article. These now render as plain text with the same styling. "All guides →" is a link only where its category has a written guide (Transcription).
- **Footer Privacy link:** the mock points it at the help article, which is about transcription. It now goes to the help centre's Privacy and storage card (`/help#privacy-and-storage`).
- **Find-in-page:** content not yet revealed is `visibility: hidden`, so the browser's find can't reach it until it has scrolled into view or Tab has been pressed. This is a consequence of the agreed hiding method; I've left it as it is.

## Decisions I made on my own

1. **Home's mobile spacing:** the mock's `[data-theme] main>section` rules are scoped to `main.home>section`. Otherwise they would also restyle Download and Help on phones.
2. **One source for the demo maths:** the Phase 3 build-time still frames and the Phase 4 motion use the same modules, so they can't drift apart.
3. **Snapping restarts:** a demo entering the viewport snaps to frame 0 instead of animating back from the still frame.
4. **Revealing everything for the keyboard:** the first Tab press reveals all hidden content, and so does printing. Hidden elements can't take focus, so Tab was skipping the platform tiles, the .opus links and the FAQ.
5. **Hero steps under reduced motion:** each step shows a still of its own stage. In the mock, clicking a step does nothing under reduced motion.
6. **Search under reduced motion:** the search tile stays usable, so typing still searches.
7. **The demo's Record button is out of the tab order:** the three hero steps are the keyboard way into the demo.
8. **Decorative demos are `aria-hidden`:** the tile headings carry the meaning. The search results area is not a live region, because the looping demo would announce a result every few seconds.
9. **No share image:** `SocialMeta` emits no `og:image` until cassini.talk has a share image, and uses a `summary` Twitter card meanwhile.
10. **Caching:** `public/_headers` gives `/_astro/*` (hashed build assets) an immutable year-long cache. The old embed and conference rules are gone.
11. **The Linux card spans both columns at tablet width** (`pg-wide`), as the mock's CSS intends for the odd fifth card.
12. **The deploy environment has no URL:** it's named `cassini.talk`, without a URL until the workers.dev or custom domain is known.

## Open follow-ups

From the plan, still open:
1. **Domain:** the exact domain and its DNS on Cloudflare. After that, add the route that's written out in a comment in `wrangler.jsonc`, and the environment URL in the workflow.
2. **Store links, versions and Windows:** all in `src/data/apps.ts`.
   - Store links are `PLACEHOLDER` (`"#"`) and carry `data-placeholder="true"` in the HTML.
   - The ink store buttons are marked `badgePending` until the official badges replace them.
   - Windows is `placeholder: true`, with the mock's bracketed requirement, version and date, and a `#` source link.
   - It's still open whether Windows ships at launch.
3. **Help content:** only "Transcribe on your device" is written. The other guides are titles only, in `src/data/help.ts`. Give a guide a `slug` once its article exists in `src/content/help/` and it becomes a link everywhere.
4. **Share image:** the OG card for cassini.talk. Pass it to `SocialMeta` as `image`.
5. **Analytics:** none were added.

New from the build:
- **Changelog gate:** `changelog-check.yml` treats `cassini-talk-site/` as shipping code. The PR needs the `skip-changelog` label, as the plan says.
- **CI cost:** `ci.yml` runs its full suite on PRs that touch `cassini-talk-site/`, as it does for the microsite. I didn't change `ci.yml`.
- **Deploy secret:** the deploy reuses the `CLOUDFLARE_API_TOKEN` secret. Check that it can create a new Worker called `cassini-talk`.

## Running it locally

```sh
cd cassini-talk-site
npm ci
npm run dev -- --port 4323        # or the "cassini-talk-site" entry in .claude/launch.json
npm run lint && npm run build     # what CI runs
npm run preview -- --port 4323    # serve the built dist
```

The routes are `/`, `/download`, `/help` and `/help/transcribe-on-your-device`. To see the still frames, turn on reduced motion in the OS, or emulate `prefers-reduced-motion` in DevTools.
