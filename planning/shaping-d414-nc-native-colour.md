---
shaping: true
---

# D-414 — Nextcloud Native Colour/Accessibility

## Frame

**Source:** D-414 — "Primary colour and base colour accessibility preferences can be set by a Nextcloud user, we should adhere to these by default in the Nextcloud viewer. Fallback to defined theme on external viewer."

**Problem:** Cassini's viewer and control-panel use two hardcoded DaisyUI themes (`forrest-light` / `forrest-dark`). When embedded in Nextcloud, they ignore the user's Nextcloud colour and accessibility preferences entirely — making the app look out of place and inaccessible for users who rely on high-contrast or custom primary colours.

**Outcome:** When embedded in Nextcloud, both the viewer and control-panel adopt the Nextcloud user's colour/accessibility settings. In the external (standalone) viewer, the existing forrest themes are preserved.

---

## Requirements (R)

| ID | Requirement | Status |
|----|-------------|--------|
| R0 | When embedded in Nextcloud, both viewer and control-panel use Nextcloud colour/accessibility preferences | Core goal |
| R1 | When in external viewer (standalone), fall back to forrest-light/forrest-dark themes | Must-have |
| R2 | Respect Nextcloud dark/light mode selection | Must-have |
| R3 | Respect Nextcloud primary colour selection | Must-have |
| R4 | Respect Nextcloud high-contrast / accessibility mode | Must-have |
| R5 | Colour preferences apply on next load (no live update required) | Must-have |
| R6 | Theme toggle hidden in Nextcloud context; default to Nextcloud user preference | Must-have |

---

## Technical Constraints

**Shadow root naming conflict.** Nextcloud exposes `--color-primary` on `:root`. DaisyUI also uses `--color-primary` on `:host` inside the shadow root — overriding anything inherited. A `var()` reference to itself would be circular.

**Resolved by spike:** Use `OCA.Theming.primaryColor` (available on `window`) to read the NC primary colour as a hex string, set it on the shadow host element as `--nc-primary` via inline style, then reference it in CSS without a circular dependency.

**NC vars update correctly per theme.** NC sets the same CSS var names for light, dark, and high-contrast modes — but changes their values. A single CSS override block reading the NC vars covers all three states. No per-theme CSS branching needed.

**Dark/high-contrast detection.** Signalled via `document.body.dataset.themes` (e.g. `"dark"`, `"highcontrast"`, `"dark-highcontrast"`). Needed only to set `color-scheme: dark` on the shadow host so browser-native elements (scrollbars etc.) render correctly.

---

## Selected Shape: A — Attribute-gated CSS Override

| Part | Mechanism |
|------|-----------|
| **A1** | **NC var reader** |
| A1.1 | `embedded.ts` (viewer + control-panel): on mount, read `OCA.Theming.primaryColor` for primary colour; read `getComputedStyle(document.documentElement)` for all other NC vars |
| A1.2 | Set `--nc-primary: <value>` as inline style on the shadow host element (alias to break circular `var()` reference) |
| **A2** | **Theme state detection** |
| A2.1 | Read `document.body.dataset.themes` to detect dark mode (`includes('dark')`) and high-contrast mode (`includes('highcontrast')`) |
| A2.2 | Set `data-nc-theme="light|dark|highcontrast|dark-highcontrast"` attribute on the shadow host element |
| **A3** | **CSS override block** |
| A3.1 | `app.css` (viewer + control-panel): single `:host([data-nc-theme])` block overrides DaisyUI tokens with NC vars |
| A3.2 | `color-scheme: dark` applied via `:host([data-nc-theme="dark"]), :host([data-nc-theme="dark-highcontrast"])` |
| **A4** | **Theme toggle removal** |
| A4.1 | `embedded.ts` (viewer): pass `ncMode: true` prop to App |
| A4.2 | `App.svelte` (viewer): hide sun/moon toggle when `ncMode` is true |

### DaisyUI token mapping (from spike)

| NC source | DaisyUI token |
|-----------|--------------|
| `OCA.Theming.primaryColor` → `--nc-primary` (inline) | `--color-primary: var(--nc-primary)` |
| `--color-primary-text` | `--color-primary-content` |
| `--color-main-background` | `--color-base-100` |
| `--color-background-dark` | `--color-base-200` |
| `--color-background-darker` | `--color-base-300` |
| `--color-main-text` | `--color-base-content` |

### CSS override block (sketch)

```css
:host([data-nc-theme]) {
  --color-primary:         var(--nc-primary);
  --color-primary-content: var(--color-primary-text);
  --color-base-100:        var(--color-main-background);
  --color-base-200:        var(--color-background-dark);
  --color-base-300:        var(--color-background-darker);
  --color-base-content:    var(--color-main-text);
}

:host([data-nc-theme="dark"]),
:host([data-nc-theme="dark-highcontrast"]) {
  color-scheme: dark;
}
```

---

## Fit Check (R × A)

| Req | Requirement | Status | A |
|-----|-------------|--------|---|
| R0 | Both viewer + control-panel use NC prefs when embedded | Core goal | ✅ |
| R1 | Falls back to forrest-light/dark in standalone | Must-have | ✅ |
| R2 | Respect NC dark/light mode | Must-have | ✅ |
| R3 | Respect NC primary colour | Must-have | ✅ |
| R4 | Respect NC high-contrast mode | Must-have | ✅ |
| R5 | Applies on next load | Must-have | ✅ |
| R6 | Hide toggle, default to NC preference | Must-have | ✅ |

All requirements satisfied. Flags resolved by spike (see [spike-d414-nc-theming.md](spike-d414-nc-theming.md)).

---

## Files to Change

| File | Change |
|------|--------|
| `cassini-viewer/src/embedded.ts` | Add NC var reader + `data-nc-theme` setter on shadow host |
| `cassini-control-panel/src/embedded.ts` | Same |
| `cassini-viewer/src/app.css` | Add `:host([data-nc-theme])` override block |
| `cassini-control-panel/src/app.css` | Same |
| `cassini-viewer/src/App.svelte` | Accept `ncMode` prop; hide theme toggle |
