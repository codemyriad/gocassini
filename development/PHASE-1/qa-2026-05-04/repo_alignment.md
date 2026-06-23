# MVP repo-alignment QA

## Scope and method

- Baseline chosen from the oldest MVP initiative commit touching `planning/initiatives/mvp/`: `2ac2243` (2026-04-27).
- Reviewed mainline work since its parent (`1159f92`) across the MVP-related code and planning changes.
- Spot-checked runtime quality with:
  - `cd cassini-operator && go test ./...`
  - `cd cassini-go-recorder && go test ./internal/cassini ./internal/transcribe`
  - `cd cassini-viewer && npm test`
  - `cd cassini-viewer && npm run build`

Below are the repo-alignment issues I would flag aggressively.

---

## QA-01 — Viewer design-system refactor reintroduced a bespoke styling layer

- **Severity:** High
- **Misalignment:** The V7 work was supposed to move the viewer onto a stock DaisyUI/Tailwind foundation, but the current result recreates a local theme/CSS system instead of staying close to repo-standard primitives.
- **Evidence:**
  - `cassini-viewer/src/app.css` grew from a tiny entrypoint to a 216-line theme/custom-style file.
  - It now contains custom `@theme` tokens, two `@plugin "daisyui/theme"` blocks (`forrest-light`, `forrest-dark`), plus bespoke selectors like `.scroll-stable`, `.cassini-spinner`, `html.theme-switching`, and a global `:focus-visible` rule.
  - `cassini-viewer/src/App.svelte` persists custom theme names via `ThemeMode = "forrest-light" | "forrest-dark"`.
- **Why this is a repo-alignment issue:** The repo was cleaner when styling decisions were either stock or clearly isolated. This change creates another custom design layer that future work has to learn and maintain.
- **Suggestion:** Either collapse back to stock DaisyUI `light`/`dark` + utilities, or explicitly treat the custom theme as a first-class maintained exception with its own minimal theme file and docs.
- **User feedback:** 

This is perfectly fine. It allows us to utilise Daisy UI for our custom styling using style tokens, rather than rewrite CSS every time -- this is a good thing.

---

## QA-02 — Viewer nested-route asset resolution is currently broken

- **Severity:** High
- **Misalignment:** The viewer now leans harder on URL/history behavior, but its default asset resolution is still not robust under nested routes.
- **Evidence:**
  - `cd cassini-viewer && npm test` currently fails two tests:
    - `src/viewer/catalog.test.ts` → `loads the default catalog from the app base on nested routes`
    - `src/viewer/loadArtifact.test.ts` → `loads bundled artifacts from the app base when opened on a nested route`
  - `cassini-viewer/src/viewer/catalog.ts` and `cassini-viewer/src/viewer/loadArtifact.ts` both resolve app-relative defaults from `window.location.href`, which is exactly the failure mode those tests exercise.
- **Why this is a repo-alignment issue:** A clean repo should not ship URL-driven viewer work while its own route-resolution tests are red.
- **Suggestion:** Introduce one app-root URL helper (preferably based on `document.baseURI` or a normalized app base), use it everywhere for default artifact/catalog paths, and keep the nested-route tests as guardrails.
- **User feedback:** 

This is a good idea. Please add this to the followups file.

---

## QA-03 — CI still does not cover the viewer, so frontend regressions land green

- **Severity:** High
- **Misalignment:** The initiative added major viewer changes, but CI still only exercises Go modules.
- **Evidence:**
  - `.github/workflows/ci.yml` runs unit tests for:
    - `cassini-go-recorder`
    - `cassini-operator`
    - `harness/go-talk-rotator`
  - There is no `cassini-viewer` job for `npm test` or `npm run build`.
  - The current viewer test suite is red locally, so CI is no longer a trustworthy repo-wide signal.
- **Why this is a repo-alignment issue:** Repo cleanliness is not just code layout; it is also whether the automation actually protects the surfaces being changed.
- **Suggestion:** Add a viewer CI job that runs `npm ci`, `npm test`, and `npm run build` under `cassini-viewer`.
- **User feedback:** 

This is a good idea. Please add this to the followups file.

---

## QA-04 — A committed opaque operator runtime DB artifact is stale and should not live in the repo

- **Severity:** High
- **Misalignment:** The repo contains a checked-in SQLite runtime database with unclear ownership and no obvious consumer.
- **Evidence:**
  - A tracked operator `jobs.sqlite3` runtime artifact was added during D-233 slice 1.
  - Repo search does not show code or docs using it as a supported fixture.
  - Its schema is stale relative to current operator code: it only contains the original `jobs` table + indexes, and lacks `schema_migrations` plus the later stop-metadata migration columns.
- **Why this is a repo-alignment issue:** This is the exact kind of opaque generated state that makes a once-clean repo feel messy: binary, mutable, undocumented, and already behind the current schema.
- **Suggestion:** Remove it from git. If a DB fixture is genuinely needed, replace it with SQL seed files in a `testdata/`-style location or a documented generator script.
- **User feedback:** 

We don't want this - any DB files should be removed from the repo, we need to:
    - delete this file (and commit delete -- no rewriting history though)
    - make sure the DB file is ignored (at least the default path)

---

## QA-05 — Operator runtime state defaults into the source tree

- **Severity:** Medium
- **Misalignment:** The operator writes mutable runtime-owned state under `cassini-operator/runtime/`, which turns a source directory into a working-data directory.
- **Evidence:**
  - `cassini-operator/internal/operator/run.go` defaults:
    - DB: `cassini-operator/runtime/jobs.sqlite3`
    - work root: `cassini-operator/runtime/jobs`
    - site root: `cassini-operator/runtime/site`
  - `.gitignore` has to cover `/cassini-operator/runtime/*` to hide the churn.
  - Operator runtime artifacts should stay consolidated into that one directory.
- **Why this is a repo-alignment issue:** The repo already had cleaner patterns for generated/runtime data (`harness/runtime/`, `test/runtime/`). This move makes the component directory less source-only and less predictable.
- **Suggestion:** If the repo keeps the harness-style convention, standardize fully on `cassini-operator/runtime/` and ensure all operator-owned runtime artifacts stay there.
- **User feedback:** 

Yeah, this is less than ideal. I would want to further explore the conventions though. It would seem that `harness/*` includes both the src code as well as gitignored `/runtime`:
    - if so, I would apply the same for `cassini-operator` (just rename it from `.runtime` -> `runtime`), however if that's not the case, we can move it to top level `/runtime/operator/*`
    - we definitely need to make sure all runtime artefacts (incl. DB and such) are stored into the same directory
    - we definitely need to make the runtime directory is gitignored

---

## QA-06 — MVP planning docs are not portable after the doc move

- **Severity:** Medium
- **Misalignment:** The initiative reorganized planning docs, but the move was not completed cleanly.
- **Evidence:**
  - `planning/initiatives/mvp/tickets.md` still references removed paths such as:
    - `cassini-viewer/docs/viewer-refactor-shaping.md`
    - `cassini-viewer/docs/viewer-refactor-slices.md`
    - `cassini-viewer/docs/viewer-ux-restructure-shaping.md`
    - `cassini-viewer/docs/viewer-ux-restructure-slices.md`
  - Moved V7 docs still contain workstation-local absolute paths like `/home/silvio/dev/gocassini/...` in:
    - `planning/initiatives/mvp/slices/V7-viewer-design-system/cleaned-transcript-alignment-plan.md`
    - `planning/initiatives/mvp/slices/V7-viewer-design-system/mechanical-timing-audit.md`
  - Some moved docs still use now-broken relative links such as `../src/App.svelte`, `../src/core`, `../src/viewer`, and `../vite.config.ts` from inside the planning tree.
- **Why this is a repo-alignment issue:** Planning docs are now half-portable and half-machine-local. That is exactly the kind of documentation drift that makes repo archaeology harder.
- **Suggestion:** Normalize all moved-doc links to repo-relative paths, remove machine-local paths, and standardize naming/link targets inside `planning/initiatives/mvp/slices/`.
- **User feedback:** 

This needs to be realigned

---

## QA-07 — `.envrc.example` still advertises dead fixture-era config

- **Severity:** Low-Medium
- **Misalignment:** The shared env example still exposes a V1 fixture knob that the current operator runtime no longer uses.
- **Evidence:**
  - `.envrc.example` exports `FIXTURE_URL`.
  - Repo search shows `FIXTURE_URL` only in `.envrc.example` and V1 planning docs; it is not part of the live V2 operator implementation.
- **Why this is a repo-alignment issue:** Shared examples should bias contributors toward current, supported knobs. This one mixes current setup with slice-history baggage.
- **Suggestion:** Remove `FIXTURE_URL` from `.envrc.example`, or move it behind a clearly labeled historical/V1 note if it still matters for archived slice docs.
- **User feedback:** 

This should be removed

---

## Overall assessment

The MVP work is not a structural disaster, but it **did** introduce several repo-alignment regressions:

1. the viewer foundation is less stock than advertised,
2. the viewer test suite is currently red,
3. CI does not protect the viewer,
4. runtime/data placement around the operator is inconsistent, and
5. the planning-doc move left behind broken and machine-local references.

If I were triaging this strictly, I would treat **QA-01 through QA-04** as the priority fixes before calling the initiative fully repo-aligned.
