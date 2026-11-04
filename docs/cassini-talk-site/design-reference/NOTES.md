# Design notes

These notes come from the *Cassini Apps Site Mocks* canvas (https://claude.ai/artifact/Nozj3Lxyu8Gxuxxu8xugsL), page "Elevated site". They were exported on 4 Nov 2026 from canvas version 1793809446-03c6, after the copy pass.

- `pages/` holds the exact files behind the boards.
- `shots/` holds each page rendered at 1440 and 390 wide, with reduced motion turned on. The demos show the still frame the build should render under reduced motion; in motion they loop.
- The phone boards on the canvas wrap the same pages at 390, so they aren't exported separately.

The canvas notes follow word for word.

## Home

Home, elevated: the same simple demos, without the left/right zig-zag. The rhythm borrows from the references: a split hero (a two-line headline across the top that sells the app specifically, “Every word written down. None of it uploaded.”: what it does, then why it’s different, with the 0 uploads in the numbers row right below to back it up; under it the live demo on the right, with its three steps as the only copy on the left: Record the conversation, Read it back word for word, Find the part that mattered. Each step has a rail that fills while its stage plays, and clicking a step jumps the demo there. One Download button for every platform sits under the steps and opens the download page. The whole demo is in the first screen at 1440 × 900 and on phones; on tablets and phones the hero stacks, with the steps as three short tabs over the demo. Earlier heroes are kept on the Explorations page under Hero versions: the first centred stack, and this split with “Be a little more present.”), a row of big true numbers (SaySo) counting down 25, 5, 1, 0, where each number acts out its fact underneath (greetings in turn, the platform icons lighting up, a waveform becoming one .opus file, a timer that sends 0 B), a bento grid instead of alternating sections (Granola), where each tile plays its own loop (queries typed into search with the results easing in, Speaker 2 becoming Chris? with a lavender Confirm button that a cursor comes in and clicks, the way the hero’s cursor presses Record, bookmarks dropped on a timeline and jumped back to, two voices streaming on their own lanes), then the three plain facts (works offline, no account, export) as a quiet strip of icons and one-line answers instead of more cards, the 25 languages as a ring of greetings orbiting a “Detected” label like Saturn’s rings, kept quiet: only the front arc reads, the greeting passing the front sits in a solid teal capsule and the label rolls to its language. Long and short greetings alternate around the ring so neighbours never touch; on phones the ring is smaller so the whole curve shows (the other options are on the Explorations page), then a full-width privacy statement with the five platform tiles right under it, no heading of their own (nothing leaves your device, whichever device that is), the .opus file as a left/right section: the copy on the left, and on the right the file as a small tree of its three parts (audio, transcript, speakers) playing back in a loop, the playhead crossing the waveform, the transcript reading along word by word and the speaker who is talking lit up, a single-column FAQ and a closing call to action.

Ground: saturn-night, a proposed deeper blue-black theme now in the design system. Only the surfaces change, and cards read raised instead of recessed. Switch the Theme tweak to saturn-dark to compare with the grey. The other speaker uses the theme's own accent, the lavender color-accent that gocassini.com declares but never shows, instead of amber, as a cool pair with your teal: their name, the Confirm button that accepts it, the check, their words and their wave. Bookmarks are ink markers on the teal timeline.

New to the system: a display headline at clamp(1.75rem, 7.6vw, 6.25rem) and 4rem numerals, both inline. Everything else is existing classes.

Closing band: the “close” option, a compact live two-voice wave directly under “Keep your conversations.”, with the Download button just below. The button takes the colour of whoever is speaking at the centre of the wave, teal or lavender. New audio is generated at the centre line every frame and spreads outward; teal is one speaker, lavender the other. The other two options (bottom, behind) are on the Explorations page, with the numbers row options. Still with reduced motion.

## Download, Help and the help article

Download and help, elevated: the same pages and the same words as the first versions on the Explorations page, rebuilt to match the elevated home. The saturn-night ground with the same Theme tweak, a centred display headline, section headings at Home's sizes, bordered cards on the raised surface like the bento and the platform tiles, and ink store buttons.

- Download: Home's Download button lands here. All five apps sit in one bento with no section headings and no recommended device: the phone and tablet apps across the top at half width, the three computer apps below at a third. Buttons and version lines line up across each row. Home's platform tiles link straight to each card. The Nextcloud cross-link carries the Nextcloud logo in its own blue.
- Help centre: search and the platform filter under the headline, then the six topics as cards with teal icons, each ending in All guides. On phones the filter is one row that scrolls sideways.
- Help article: the gocassini.com docs layout on the night ground. The warning is a tinted card instead of a solid amber block.

The nav and footer link these four pages to each other; the first versions on Explorations still link among themselves.

Logo: the nav on all four pages carries the mark as Saturn in true colour (B on the Explorations page, under Logo, Saturn options), drawn inline so it works on the web only. The first versions and the hero records keep the flat mark, and so does the favicon.

## Motion

Motion for the build, first pass (4 Nov 2026). The canvas can’t show these, because each board renders its whole page without scrolling, so they are rules for the real site.

- Demos start when seen: each looping demo starts from its beginning when it scrolls into view and pauses off screen. Nobody lands halfway through a loop, and the dozen or so loops on Home never all run at once.
- One quiet reveal: section headings and cards fade in with an 8px rise over about 450ms, once. The demos get no entrance, since they already move. Hide with visibility and reveal with an animation, not an opacity transition, which flashes on a hard refresh.
- Page transitions: a short crossfade of the page content, about 200ms, through the browser’s View Transitions, with the nav held still. No slides or zooms. Browsers without View Transitions load the page normally.
- Reduced motion: the demos freeze on a still frame, as they do here, and the reveals and transitions switch off.
- Tokens: cubic-bezier(0.32, 0.72, 0, 1) for movement, cubic-bezier(0.34, 1.56, 0.64, 1) for presses and pops, and 0.35 to 0.6 seconds for most transitions.

The orbit load state is left out for now. It stays on the Explorations page under Logo in motion.

## Decisions made later in the design session

### Logo
- The mark is **Saturn**. The nav uses the true-colour version at nav detail: `logo/saturn-nav.svg`.
- Detail follows size:

  | File | Size | What it keeps |
  |---|---|---|
  | `saturn-nav.svg` | nav, 25.6px tall | No ring gaps, a smooth ring, one cloud belt, the polar haze, the ring's shading and shadow |
  | `saturn-mid.svg` | around 64px | The Cassini Division only |
  | `saturn-full.svg` | large | Every ringlet and gap |

  Gaps thinner than a pixel break into grain, so don't scale `saturn-full.svg` down.
- The light comes from the upper left everywhere. Shadows are tinted to the palette (warm browns on the gold), never blue-black.
- The favicon and app icons keep the flat mark: `logo/mark-flat.svg`, and the microsite's existing `public/favicon.svg`.
- Out of scope for now: the moon's orbit on hover, and the orbit load state. Both are explored on the canvas's Explorations page, "Logo in motion".
- **SVG IDs:** each SVG uses its own ID prefix (`sn`, `sm`, `sf`). Inline only one copy per prefix on a page, or re-prefix.

### Colour
- Teal is you and your actions.
- Lavender is the other speaker, including the controls about them. The voice tile's Confirm button and its click ring are lavender.
- Ink is markers.
- Static label icons are white (base-content). Colour appears only in the animated parts, such as the Audio, Transcript and Speakers icons in the .opus tree.
- The closing band's Download button takes the colour of whoever is speaking at the centre of the wave. It fades over 0.4s.

### Copy and names
- **Hero headline:** "Every word written down." / "None of it uploaded." The second line is at /40.
- **Hero steps:** "Record the conversation.", "Read it back, word for word.", "Find the part that mattered."
- **Names:** Silvio speaks in the hero; Chris is the other speaker; "You" is you. First names only.
- **Copy pass:** the user's copy pass is in `pages/Home-elevated.dc.html`. Take copy from there, not from these notes.

### Layout
- **Footer:** sits directly after the last section; `<main>` doesn't stretch.
- **Home spacing:** the .opus section has 64px bottom padding; Questions has 64px top and 96px bottom. The closing band has 96px top and bottom on desktop, and 48px on phones.
- **Voice tile:** the "Speaker 2" / "Chris?" slot takes the width of the visible label, so Confirm sits right after the name.
