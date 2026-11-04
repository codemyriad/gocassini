# Build the cassini.talk site

A plan for a Claude Code session in the terminal. Work through it phase by phase, and stop for review at the end of each phase.

## What we're building

cassini.talk is the marketing site for the native cassini.talk apps (iPhone and iPad, Android, Mac, Windows, Linux). It lives on its own domain, next to gocassini.com, which covers Cassini for Nextcloud.

It has four pages: **Home**, **Download**, **Help** and a **help article**. They're designed in full on a claude.ai Design canvas, the "Elevated site" page of *Cassini Apps Site Mocks* (https://claude.ai/artifact/Nozj3Lxyu8Gxuxxu8xugsL).

**The mocks are the spec.** Match them; don't redesign.

We start by duplicating the gocassini.com site (`cassini-microsite/`), then reshape the copy to match the mocks. That way we keep the same design system, theme machinery, docs layout and deploy path.

## Ground rules

- **Branch and PR:** work on a branch (`feat/cassini-talk-site`) and open a PR. Never push to `main`.
- **No AI attribution:** no `Co-Authored-By` trailer, no "Generated with Claude Code" line, no session links in commits or PRs.
- **Comments:** write the reasoning in the commit message, not in comments above the code. Keep code comments for what a reader can't infer.
- **Installs:** npm, bun and pnpm enforce a minimum release age of 7 days. If an install fails on release age, stop and ask; don't retry or bump versions. npm has `ignore-scripts=true`; if a package needs install scripts, say so.
- **Leave the existing site alone:** don't change `cassini-microsite/` or its workflow. The new site is a copy.
- **Versions:** keep the microsite's versions (Astro 4.16, Tailwind 4, DaisyUI 5). No framework upgrades in this work.
- **Changelog gate:** PRs that touch shipping paths need a `changelog.d` fragment. That changelog is for Cassini for Nextcloud, so ask the user to add the `skip-changelog` label instead.

## Source of truth

`docs/cassini-talk-site/design-reference/` holds the exported mock, from canvas version 1793809446-03c6 (4 Nov 2026, after the copy pass).

| Path | What it is |
|---|---|
| `pages/*.dc.html` | Home, Download, Help and the help article, plus the nav and footer, as HTML with the real Tailwind/DaisyUI classes. The page-specific CSS is in each file's `<helmet>`, and the logic class at the bottom drives the demos. **Take copy from here**, since it includes the user's copy pass. |
| `shots/*.png` | Each page at 1440 and 390, rendered with reduced motion on, so Home's demos show their still frames. The help article is the first screen only, because its content scrolls inside the layout. **For checking only, never as the source.** |
| `logo/` | `saturn-nav.svg`, `saturn-mid.svg` and `saturn-full.svg` (true colour, by size), plus `mark-flat.svg`. |
| `NOTES.md` | The canvas notes word for word, plus the decisions made after them: logo, colour, names, layout. |

**Reading a `.dc.html` file:**
- `{{value}}` is a binding that `renderVals()` fills in each frame.
- `<sc-for>` repeats an element over a list.
- `<sc-if>` shows an element conditionally.
- `<dc-import name="AppsNav">` embeds the nav component.

Ignore the runtime itself. The markup and the `renderVals()` maths are what to port.

### Port the HTML; don't redraw it

The structure, classes, copy, SVG icons, page CSS and animation maths in `pages/` are final, so most of the build is already written. They can't ship as they are, because they depend on the canvas runtime:
- the template bindings;
- one 30fps loop that re-renders the whole page;
- the design system's prebuilt CSS bundle instead of the site's own Tailwind build;
- CSS repeated in every file.

Port each section by copying its markup into an `.astro` component nearly unchanged, then make only these changes:
1. **Static bindings become literals or props:** theme, hrefs and labels.
2. **`<sc-for>` lists become `.map()`** over data in the component's frontmatter. `<dc-import>` becomes the `Nav` and `Footer` components.
3. **Each file's `<helmet>` CSS moves into `src/tailwind.css`**, once, not per page.
4. **Animated bindings become client scripts.** These are `{{hero.*}}`, `{{voice.*}}`, `{{mark.*}}`, `{{file.*}}`, `{{wave}}` and the like. Their still-frame values go into the static HTML, and a small script ports the matching `renderVals()` maths to update them (see "Porting the demos").

If a section looks off afterwards, diff your component against the `.dc.html` markup before changing anything by eye.

## Decisions already made (don't reopen)

- **Ground:** a dark site on the `saturn-night` theme, the `saturn-dark` tokens with deeper surfaces:
  - `--color-base-100: oklch(15% 0.014 255)`
  - `--color-base-200: oklch(19.5% 0.016 255)`
  - `--color-base-300: oklch(28% 0.018 255)`
  - `--color-neutral-content` = base-100, `--color-info` = base-300

  Dark only; no light theme.
- **Colour meanings:**
  - teal `primary` = you and your actions;
  - lavender `accent` = the other speaker, including the controls about them, like Confirm;
  - ink `base-content` = markers;
  - static label icons are white, and colour appears only in the animations.
- **Hero:** "Every word written down. / None of it uploaded." The second line is at /40.
  - **Layout:** split. The steps are on the left with progress rails ("Record the conversation." / "Read it back, word for word." / "Find the part that mattered."), the live demo card is on the right, and Download plus one caption sit under the steps.
  - **Phones:** the hero stacks, with the steps as three short tabs over the demo.
- **Names in demos:** Silvio speaks in the hero; Chris is the other speaker; "You" is you. First names only.
- **Logo:** the nav uses the true-colour Saturn mark at **nav detail** (smooth ring, one belt, no ring gaps). The favicon and app icons keep the flat mark. The animated logo and the orbit loader are **out of scope**.
- **Download:** one bento of five apps with no section headings and no "recommended for your device":
  - iPhone and iPad and Android at half width;
  - Mac, Windows and Linux at a third.

  Store buttons are ink placeholders, to be replaced by the official badges. The Nextcloud logo is blue.

## Motion rules (agreed first pass)

1. **Demos start when they're seen.** Each looping demo starts from its beginning when it scrolls into view and pauses off screen. Use `IntersectionObserver`.
2. **One quiet reveal.** Section headings and cards fade in with an 8px rise over about 450ms, once. Demos get no entrance.
   - Hide with `visibility` and reveal with a CSS animation, never an opacity transition, which flashes on hard refresh.
   - Content must be visible without JS.
3. **Page transitions.** A ~200ms crossfade of the page content between pages, with the nav held still.
   - Use native cross-document View Transitions: `@view-transition { navigation: auto; }`, with `view-transition-name` on the nav.
   - Drop Astro's `<ViewTransitions />` client router from the copied docs layout, so the demo scripts run on normal page loads.
   - Unsupported browsers just load the page.
4. **Reduced motion.** Demos render their still frame (the mock's `still` state), and reveals and transitions switch off.
5. **Tokens.**
   - `cubic-bezier(0.32, 0.72, 0, 1)` for movement;
   - `cubic-bezier(0.34, 1.56, 0.64, 1)` for presses and pops;
   - 0.35 to 0.6s for most transitions.

   Define them once, as CSS custom properties.

## Architecture

**Folder:** `cassini-talk-site/`, a sibling of `cassini-microsite/`. It's not an npm workspace; like the microsite, it has its own `package.json` and lockfile.

**Start from a copy:** `cp -R cassini-microsite cassini-talk-site`, excluding `node_modules`, `dist` and `.astro`. Then:

- **Delete:**
  - the embed: `public/embed`, `scripts/sync-embed.mjs`, and the `/embed` rules in `public/_headers`;
  - `src/pages/nextcloud-conf-2026.astro` and `public/nextcloud-conf-2026/`;
  - the changelog collection, its page and `ChangelogEntry.astro`;
  - every gocassini doc in `src/content/docs`;
  - `hero-screenshot.webp` and `og-card.png`;
  - the old `_redirects` rules;
  - `.claude/launch.json` (recreate it with the new path and a different port, say 4323).
- **Rename:**
  - `package.json` `name` → `cassini-talk-site`;
  - `astro.config.mjs` `site` → `https://cassini.talk`;
  - `wrangler.jsonc` `name` → `cassini-talk`, with routes for the new domain only.
- **Keep:**
  - the MDX setup and the GitHub-alert remark plugin;
  - sitemap, ESLint config, `.nvmrc`, `tsconfig.json`;
  - `SocialMeta.astro`, `Card.astro`;
  - `DocsLayout.astro`, which becomes the help-article layout.

**Theme:** in `src/tailwind.css`, replace the two themes with one `saturn-night` theme as default and `prefersdark`, using the values above. Move into this file any CSS a mock defines in its `<helmet>` that the site needs, such as the hero layout, the language ring, the facts strip and the file tree. The mock CSS is prefixed (`lead-`, `pg-`, `lang-`, `mo-`); keep the prefixes.

**Don't name a class `hero`:** DaisyUI already has a `.hero` component.

**Routes:**

| Route | Source |
|---|---|
| `/` | Home (`Home-elevated`) |
| `/download` | Download (`Download-elevated`) |
| `/help` | Help centre (`Help-elevated`) |
| `/help/[...slug]` | Help articles, from a `help` content collection rendered by the adapted `DocsLayout` (`Help-article-elevated`) |

**Components:**
- `Nav.astro`: Saturn mark plus "cassini.talk", then Help, GitHub and an ink Download button. Keep the blurred, fixed bar from the microsite.
- `Footer.astro`: Download, Help, Privacy and Support; © 2026 Codemyriad; the codemyriad.io and Open source chips; and the "Running Nextcloud? Cassini for Nextcloud ↗" line. **The footer sits directly after the last section**, so `<main>` must not stretch.
- `SaturnMark.astro`: inline SVG with a `detail` prop (`nav`, `mid` or `full`), from `design-reference/logo/`. IDs inside the SVG must be unique per instance; prefix them.
- **Home sections**, one component each:
  - the hero (split, steps with progress rails, demo card);
  - the numbers row (25 / 5 / 1 / 0, each acting out its fact);
  - the bento of four looping tiles (search, naming a voice, marking a moment, two voices on lanes);
  - the facts strip;
  - the language ring (CSS 3D, from the mock's `lang-*` keyframes);
  - the privacy statement and platform tiles;
  - the .opus file section;
  - the FAQ (`<details>`);
  - the closing band: the live two-voice wave, with a Download button that takes the colour of whoever is speaking.
- **Download data:** `src/data/apps.ts` holds each platform's name, requirement line, version and date, buttons and links. Windows facts are placeholders in the mock; keep them clearly marked.

**Porting the demos:** in the mock, one component ticks at 30fps, and `renderVals()` computes every demo's state from time `t` (10 ticks per second). Keep that model:
- **Shape:** each demo is a small TS module, `render(t)`, that sets styles and attributes on its own elements.
- **Ticking:** one shared ticker drives only the demos that are in view. Each demo's own `t` starts at 0 when it enters the viewport, so it plays from its beginning.
- **Timings:** copy the timings, easing and transitions exactly from the mock's markup and `renderVals()`. Don't approximate.
- **Structure:** markup is static Astro. Scripts are plain `<script>` modules, as the microsite already does.

## Phases

1. **Scaffold.** Copy, trim and rename; set up the theme, nav, footer and Saturn mark; stub the four routes. `npm ci && npm run lint && npm run build` pass, and `npm run dev` serves it.
2. **Download, Help, article.** All three static pages to match the mocks at 1440, 1024, 768 and 390. Move the help article in the mock into the collection as the first entry.
3. **Home, static.** Every section in place, showing the demos' still frames, readable with JS off.
4. **Home, in motion.** Port the demos one at a time (hero first). Add the in-view start and pause, the reveal, the page transitions, reduced motion and the motion tokens.
5. **QA.**
   - Compare each page with `design-reference/shots/` at 1440 and 390, and check 1024 and 768 too.
   - No horizontal overflow.
   - Keyboard and focus work, including the hero steps as tabs.
   - Reduced motion shows the still frames.
   - No console errors.
   - Demos pause off screen: check that CPU drops when scrolled away.
6. **Deploy.**
   - Add `.github/workflows/cassini-talk-site.yml`, modelled on `microsite.yml` without the embed job: build and lint on PRs, and deploy from `main` with `wrangler deploy` in `cassini-talk-site/`.
   - Deploy to the Worker's `workers.dev` URL first. Add the custom domain only after the user confirms DNS.

## Checking your work

- **Build and preview:** `npm run build` and `npm run preview` from `cassini-talk-site/`.
- **Screenshots:** use the browser at 1440×900 and 390×844 and compare against `design-reference/shots/`. For full-page checks, capture beyond the viewport; for Home, wait for the demos to settle, or check reduced motion.
- **Reporting:** say what you could not verify, rather than calling something matched.

## Open follow-ups (the user handles these later)

These come later, as separate actions by the user. Don't block on them and don't ask about them during the build. Use the mock's placeholders, keep each one clearly marked in code (for example a `PLACEHOLDER` flag in `src/data/apps.ts`), and list what's still open in the PR description.

1. **Domain:** the exact domain and its DNS on Cloudflare. Until then, deploy to `workers.dev` only and leave out the custom-domain route.
2. **Store links and versions** for each app, and whether Windows ships at launch.
3. **Help content** beyond the one written article and the six category cards.
4. **Share image:** the OG card for cassini.talk.
5. **Analytics:** add none for now.

## Hand-off prompt

Paste this into Claude Code in the terminal, from the repo root:

> Read docs/cassini-talk-site/PLAN.md and do Phase 1. Work on a branch, stop at the end of the phase, and show me what you built and what you couldn't verify.
