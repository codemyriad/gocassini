---
shaping: true
---

# D-412 — Fix Nextcloud Viewer Bugs

## Source

> 1. Viewer width when meeting unselected
> 2. Player inaccessible below fold
> 3. viewer max width, constrained on wider screens

— D-412, Alex

---

## Problem

The Cassini viewer has layout bugs when running inside Nextcloud via the AppAPI embedded page (shadow root in `#content`). All three bugs trace to the same root cause: the viewer's layout uses **viewport-relative sizing** (`100vh` via `h-screen` / `min-h-screen`) but the embedded container is shorter and potentially narrower than the full browser viewport.

- In standalone, `h-screen` = full browser window = correct
- In Nextcloud embedded, `h-screen` = full browser window ≠ available content area

The three bugs:

| # | Symptom | Cause |
|---|---------|-------|
| 1 | Viewer width wrong when no meeting selected | Layout grid assumes `100vh` container not available in embedded context |
| 2 | Player bar below fold | `meeting-viewer` section is `h-screen` tall; `absolute bottom-0` player lands below visible area |
| 3 | Viewer content constrained on wider screens | Shadow host not explicitly sized to fill `#content`; width doesn't expand |

---

## Requirements (R)

| ID | Requirement | Status |
|----|-------------|--------|
| R0 | Player is always visible without scrolling when a meeting is loaded | Must-have |
| R1 | Meeting list and viewer panes fill the correct area in all states (no meeting, meeting selected) | Must-have |
| R2 | Viewer fills the full available width on wider screens inside Nextcloud | Must-have |
| R3 | Standalone viewer (non-embedded) continues to work correctly | Must-have |
| R4 | Fix applies only to layout/sizing — no changes to visual design, colours, or interaction behaviour | Must-have |

---

## CURRENT

| Part | Mechanism |
|------|-----------|
| C1 | Outer grid: `min-h-screen` (= `min-height: 100vh`) |
| C2 | `.meeting-list` and `.meeting-viewer` sections: `h-screen` (= `height: 100vh`) |
| C3 | Player footer: `absolute bottom-0` inside the `h-screen` meeting-viewer |
| C4 | Shadow host in `ensureShadowAppRoot` (`embedded.ts`): unstyled `<div>` appended into `#content` |
| C5 | `app.css` height chain: `html, body, #app, .cassini-root` use `min-height: 100%` / `min-height: 100vh` |

**Why CURRENT fails in Nextcloud:**
- Nextcloud chrome (top bar, sidebar) consumes ~60–100px of vertical space. `100vh` inside `#content` overflows the available area, pushing `absolute bottom-0` below fold.
- The shadow host has no explicit `width: 100%; height: 100%`, so its dimensions depend on content rather than filling `#content`.

---

## A: Container-relative layout

Replace viewport units with container-relative units throughout; make the shadow host fill its container.

| Part | Mechanism |
|------|-----------|
| A1 | `app.css`: add `height: 100%` to `html, body, #app` (supplements existing `min-height`) to establish the `h-full` chain from root to `.cassini-root` |
| A2 | `App.svelte`: outer grid `min-h-screen` → `min-h-full`; section `h-screen` → `h-full` (both sections) |
| A3 | `embedded.ts` `ensureShadowAppRoot`: set `host.style.cssText = "display:block; width:100%; height:100%"` so the host fills `#content` |
| A4 | `app.css` `:host`: upgrade `min-height: 100%` → `height: 100%` so the shadow host height is definite and `h-full` resolves correctly inside it |

**How it solves each bug:**
- **Bug 1** (wrong width unselected): With `h-full` chain established, the outer grid fills exactly the available height — no overflow, correct column widths.
- **Bug 2** (player below fold): `meeting-viewer` section is now `h-full` (height of `#content`), so `absolute bottom-0` player anchors to the bottom of the visible content area.
- **Bug 3** (constrained width): Shadow host gets explicit `width: 100%`, expanding to fill `#content`'s available width on any screen size.

---

## Fit Check

| Req | Requirement | Status | A |
|-----|-------------|--------|---|
| R0 | Player always visible without scrolling | Must-have | ✅ |
| R1 | Panes fill correct area in all states | Must-have | ✅ |
| R2 | Viewer fills full available width on wider screens | Must-have | ✅ |
| R3 | Standalone viewer continues to work correctly | Must-have | ✅ |
| R4 | Layout/sizing only — no visual design changes | Must-have | ✅ |

**Notes:**
- A passes R3: `height: 100%` on `html/body` supplements (not replaces) existing `min-height: 100vh`; standalone dimensions unchanged. `h-full` on sections resolves to `100vh` chain in standalone.
- A passes R2: `width: 100%` on the shadow host is a no-op in standalone (no shadow host present).

---

## Selected shape: A

All four parts are small, contained changes across three files:

| File | Parts |
|------|-------|
| `cassini-viewer/src/app.css` | A1, A4 |
| `cassini-viewer/src/App.svelte` | A2 |
| `cassini-viewer/src/embedded.ts` | A3 |
