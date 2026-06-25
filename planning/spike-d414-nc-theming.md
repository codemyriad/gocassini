---
shaping: true
---

# D-414 Spike: Nextcloud Theming & Accessibility Signal Detection

## Context

Cassini's viewer and control-panel are embedded in Nextcloud inside shadow roots. Nextcloud users can set a primary colour, a light/dark theme, and a high-contrast mode via Nextcloud's accessibility settings. We need to bridge these into the shadow root to override DaisyUI's hardcoded tokens.

## Goal

Identify exactly how Nextcloud 32–35 exposes: primary colour, dark/light mode, and high-contrast mode — and map them to the signals we read in `embedded.ts`.

## Questions & Answers

Inspected against a live NC 32 instance at `demo.nextcloud.codemyriad.io` with dark + high-contrast toggled on.

| # | Question | Answer |
|---|----------|--------|
| **S1-Q1** | What CSS custom properties does NC set on `:root` for primary colour? | `--color-primary`, `--color-primary-text`, `--color-primary-element`, `--color-primary-element-light`, `--color-primary-element-hover`, `--color-primary-element-text`, `--color-primary-light` |
| **S1-Q2** | How is dark mode signalled? | `document.body.dataset.themes` contains `"dark"` or `"dark-highcontrast"`. Check `includes('dark')`. Also available as `OCA.Theming.enabledThemes`. |
| **S1-Q3** | How is high-contrast mode signalled? | Same `data-themes` attribute on `<body>`: `"highcontrast"` or `"dark-highcontrast"`. Check `includes('highcontrast')`. NC also adds a boolean attribute `data-theme-dark-highcontrast=""`. |
| **S1-Q4** | In high-contrast mode, do the existing NC CSS vars update, or are there separate vars? | **The existing vars update.** NC sets `--color-main-background`, `--color-main-text`, etc. to their high-contrast values. No new var names are introduced. `getComputedStyle` on `:root` always gives the correct current-theme values. |
| **S1-Q5** | What CSS var covers the base/background colour? | `--color-main-background` (confirmed). Also `--color-background-dark` and `--color-background-darker` for hierarchy. |
| **S1-Q6** | Does NC fire JS events for runtime theme changes? | No explicit events observed. `OCA.Theming.enabledThemes` is the canonical state. Since R5 = next load, no listener needed. |
| **S1-Q7** | Is `OCA.Theming` more reliable than `getComputedStyle`? | Both work. `OCA.Theming.primaryColor` gives the primary colour as a hex string directly (avoids the naming conflict with DaisyUI). For background/text/border colours use `getComputedStyle(document.documentElement)`. |

## Full NC CSS var list (dark-highcontrast state, observed)

```
--color-primary:                  #00679e
--color-primary-text:             #ffffff
--color-primary-element:          #0091f2
--color-primary-element-hover:    #17a2ff
--color-primary-element-light:    #14232c
--color-primary-element-text:     #000000
--color-primary-light:            #14232c
--color-main-background:          #171717
--color-main-background-translucent: rgba(23,23,23,.97)
--color-main-text:                #EBEBEB
--color-background-dark:          #292929
--color-background-darker:        #3b3b3b
--color-background-hover:         #212121
--color-text-maxcontrast:         #999999
--color-border:                   #292929
--color-border-dark:              #3b3b3b
--color-error:                    #552121
--color-warning:                  #3D3010
--color-success:                  #11321A
--color-info:                     #003553
```

## OCA.Theming (observed)

```js
OCA.Theming.primaryColor    // "#00679e" — user's selected primary
OCA.Theming.enabledThemes   // ["dark-highcontrast"] — active themes
OCA.Theming.inverted        // false
```

`OCA.Theming.backgroundColor` is confusingly named — it returns the primary colour, not the page background. Ignore it; use `--color-main-background` from CSS vars instead.

## DaisyUI token mapping (resolved)

| NC source | DaisyUI token | Notes |
|-----------|--------------|-------|
| `OCA.Theming.primaryColor` | `--color-primary` | Read via OCA (avoids circular `var()` ref); set as `--nc-primary` inline on host, referenced in CSS |
| `--color-primary-text` | `--color-primary-content` | Text on primary background |
| `--color-primary-element` | — | NC's interactive element colour; closer to DaisyUI `--color-primary` than `--color-primary` itself, but primary is already mapped |
| `--color-main-background` | `--color-base-100` | Page background |
| `--color-background-dark` | `--color-base-200` | Elevated surface |
| `--color-background-darker` | `--color-base-300` | Even more elevated |
| `--color-main-text` | `--color-base-content` | Main text |
| `--color-border` | `--color-base-200` border | Could also inform custom border vars |

## Acceptance

✅ Spike complete. We can describe:
- The exact CSS vars NC sets (and that they update correctly for each theme)
- The DOM signal for dark and high-contrast: `document.body.dataset.themes` (check `includes('dark')` / `includes('highcontrast')`)
- No JS events needed (next-load approach)
- `OCA.Theming.primaryColor` + `getComputedStyle` covers all data we need
- A single CSS override block suffices (no per-variant CSS branching needed) — NC does the var-value switching; we just bridge them in
