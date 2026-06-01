---
shaping: true
---

# D-289 — Skill-based Go coding standard system — Shaping

This shapes a repo-level system for keeping durable repo instructions in `AGENTS.md`, detailed coding standards in shared skills, and reusable refactor prompts separate from the standards themselves. The first instantiation is Go.

## Working position

**Working shape: A — use `AGENTS.md` as a light repo scaffold, keep detailed Go standards in one Agent Skills-compatible skill, and keep a separate generic `refactor-to-standard` prompt that works against whichever standard artifact is in force.**

Why this is the current working position:

- it matches the desired split between durable scaffold (`AGENTS.md`) and detailed language policy (skill)
- it keeps the Go skill focused on standards and examples instead of mixing in workflow modes
- it creates one reusable refactor mechanism that can later be paired with other standards, not just Go
- it stays Pi-friendly while keeping the shared artifacts in a harness-neutral home first

---

## Requirements (R)

| ID | Requirement | Status |
|----|-------------|--------|
| R0 | Keep `AGENTS.md` as the core repo prompt scaffold while moving detailed Go coding standards into a separate skill. | Core goal |
| R1 | Shared skill/layout portability | |
| R1.1 | The Go skill should live in a harness-friendly project location that Pi can load directly. | Must-have |
| R1.2 | Pi should work natively, while other harnesses should need only small and explicit adaptation. | Must-have |
| R2 | MVP simplicity and scope | |
| R2.1 | Updating the standard should be a manual edit to the skill. | Must-have |
| R2.2 | Self-improving or history-mining automation is out of scope for this MVP. | Must-have |
| R2.3 | The MVP should not require a custom Pi extension, daemon, or background automation. | Must-have |
| 🟡 R3 | 🟡 Skill and prompt responsibilities | |
| 🟡 R3.1 | 🟡 The first Go skill should define the Go coding standard clearly enough to guide new code and edits. | Must-have |
| 🟡 R3.2 | 🟡 The Go skill should stay focused on the Go standard itself, including examples where useful, rather than bundling multi-mode operational workflows. | Must-have |
| 🟡 R3.3 | 🟡 A separate generic prompt should drive standards-alignment refactors and be reusable with other standards or skills later. | Must-have |
| 🟡 R4 | 🟡 `AGENTS.md` should only lightly mention the Go skill and the refactor prompt; detailed Go rules should stay in the skill. | Must-have |
| 🟡 R5 | 🟡 The system should define a repeatable “refactor to current standard” flow after the skill changes, using the separate prompt. | Must-have |
| 🟡 R6 | 🟡 The first Go standard should be explicit enough to apply ex ante, include examples where useful, and evolve incrementally over time. | Must-have |
| R7 | The system should fit the current repo reality: multiple Go modules, mixed tooling, and no shared root Go lint config today. | Must-have |
| 🟡 R8 | 🟡 Scope stays narrow around Pi-first usage and shared skill/shared prompt structure; broader harness-specific integrations are follow-up work. | Must-have |

---

## CURRENT: repo and harness baseline

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **CURRENT1** | Pi loads project context from `AGENTS.md` and can discover project skills from `.agents/skills/` and `.pi/skills/`. | |
| **CURRENT2** | Pi registers skills as `/skill:name` commands, so a skill can already act like a named command without extension work. | |
| **🟡 CURRENT3** | 🟡 Pi prompt templates auto-load from `.pi/prompts/`, and Pi can also load extra prompt directories via `.pi/settings.json`. | |
| **CURRENT4** | This repo currently has Go modules in `cassini-go-recorder/`, `cassini-operator/`, and `harness/go-talk-rotator/`. | |
| **CURRENT5** | This repo does not currently have a root `AGENTS.md`. | |
| **CURRENT6** | This repo does not currently have a project skill directory under `.agents/skills/` or `.pi/skills/`. | |
| **🟡 CURRENT7** | 🟡 This repo does not currently have a shared prompt directory or Pi prompt settings such as `.agents/prompts/` plus `.pi/settings.json`. | |
| **CURRENT8** | This repo does not currently have a shared root Go lint/config policy file such as `.golangci.yml`. | |
| **CURRENT9** | The repo has a `.claude/` directory, but it does not currently expose shared project instructions or shared prompt/skill artifacts for other harnesses. | |

---

## A: Shared Go standard skill + separate generic refactor prompt

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **🟡 A1** | 🟡 Put the Go standard in `.agents/skills/go-coding-standard/SKILL.md` and keep that file as the source of truth for detailed Go policy. | |
| **🟡 A2** | 🟡 Put a generic prompt in `.agents/prompts/refactor-to-standard.md`; for Pi, load it via `.pi/settings.json` so it becomes a named prompt command without moving the canonical artifact into a Pi-only directory. | |
| **🟡 A3** | 🟡 Add a root `AGENTS.md` that briefly tells agents to use the Go skill for Go work and the refactor prompt for standards-alignment refactors. | |
| **🟡 A4** | 🟡 Evolve the Go standard by manually editing the skill, then explicitly run the generic refactor prompt against a chosen scope to align code to the updated standard. | |
| **🟡 A5** | 🟡 Keep repo-specific validation commands and Go examples in the Go skill; the refactor prompt consumes that guidance instead of duplicating it. | |

## 🟡 B: Shared Go skill that also owns refactor workflow

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **🟡 B1** | 🟡 Put the Go standard in one shared skill, but also put refactor-to-standard workflow instructions in that same skill. | |
| **🟡 B2** | 🟡 Invoke standards-alignment refactors through the Go skill itself rather than a separate generic prompt. | |
| **🟡 B3** | 🟡 Keep `AGENTS.md` light, but accept a multi-mode Go skill as the main execution surface. | |

## 🟡 C: Pi-specific skill and prompt layout

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **🟡 C1** | 🟡 Put the Go standard in `.pi/skills/go-coding-standard/`. | |
| **🟡 C2** | 🟡 Put the generic refactor prompt in `.pi/prompts/refactor-to-standard.md`. | |
| **🟡 C3** | 🟡 Keep `AGENTS.md` light, but rely on Pi-specific discovery for both artifacts. | |

---

## Fit Check

| Req | Requirement | Status | A | B | C |
|-----|-------------|--------|---|---|---|
| R0 | Keep `AGENTS.md` as the core repo prompt scaffold while moving detailed Go coding standards into a separate skill. | Core goal | ✅ | ✅ | ✅ |
| R1.1 | The Go skill should live in a harness-friendly project location that Pi can load directly. | Must-have | ✅ | ✅ | ❌ |
| R1.2 | Pi should work natively, while other harnesses should need only small and explicit adaptation. | Must-have | ✅ | ✅ | ❌ |
| R2.1 | Updating the standard should be a manual edit to the skill. | Must-have | ✅ | ✅ | ✅ |
| R2.2 | Self-improving or history-mining automation is out of scope for this MVP. | Must-have | ✅ | ✅ | ✅ |
| R2.3 | The MVP should not require a custom Pi extension, daemon, or background automation. | Must-have | ✅ | ✅ | ✅ |
| R3.1 | The first Go skill should define the Go coding standard clearly enough to guide new code and edits. | Must-have | ✅ | ✅ | ✅ |
| R3.2 | The Go skill should stay focused on the Go standard itself, including examples where useful, rather than bundling multi-mode operational workflows. | Must-have | ✅ | ❌ | ✅ |
| R3.3 | A separate generic prompt should drive standards-alignment refactors and be reusable with other standards or skills later. | Must-have | ✅ | ❌ | ✅ |
| R4 | `AGENTS.md` should only lightly mention the Go skill and the refactor prompt; detailed Go rules should stay in the skill. | Must-have | ✅ | ✅ | ✅ |
| R5 | The system should define a repeatable “refactor to current standard” flow after the skill changes, using the separate prompt. | Must-have | ✅ | ❌ | ✅ |
| R6 | The first Go standard should be explicit enough to apply ex ante, include examples where useful, and evolve incrementally over time. | Must-have | ✅ | ✅ | ✅ |
| R7 | The system should fit the current repo reality: multiple Go modules, mixed tooling, and no shared root Go lint config today. | Must-have | ✅ | ✅ | ✅ |
| R8 | Scope stays narrow around Pi-first usage and shared skill/shared prompt structure; broader harness-specific integrations are follow-up work. | Must-have | ✅ | ✅ | ❌ |

**Notes:**

- **A** is the best current fit because it preserves the scaffold/skill split, keeps the Go skill focused, and gives us one reusable refactor prompt for standards-driven cleanup.
- **B** fails because it mixes the standard artifact with operational refactor workflow, which is exactly the coupling we now want to avoid.
- **C** keeps the separation of concerns, but it gives up the cross-harness-friendly project layout too early.

---

## Working shape: A

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **🟡 A1** | **🟡 Canonical Go standard artifact** | |
| 🟡 A1.1 | 🟡 Keep detailed Go coding standards in `.agents/skills/go-coding-standard/SKILL.md`. | |
| 🟡 A1.2 | 🟡 Treat that skill as a standards artifact first: rules, examples, repo-local validation guidance. | |
| **🟡 A2** | **🟡 Separate generic refactor prompt** | |
| 🟡 A2.1 | 🟡 Keep `refactor-to-standard` separate from the Go skill so it can be reused with later standards and skills. | |
| 🟡 A2.2 | 🟡 Canonical shared location: `.agents/prompts/refactor-to-standard.md`. | |
| 🟡 A2.3 | 🟡 For Pi, wire that shared prompt in through `.pi/settings.json` rather than relocating it into a Pi-only prompt directory. | |
| 🟡 A2.4 | 🟡 Pi command contract: `/refactor-to-standard <standard> <scope>`. | |
| 🟡 A2.5 | 🟡 `<scope>` is repo-root-relative, and `*` means the whole repo. | |
| **🟡 A3** | **🟡 Lightweight scaffold only** | |
| 🟡 A3.1 | 🟡 Root `AGENTS.md` should briefly route Go work to the Go skill. | |
| 🟡 A3.2 | 🟡 Root `AGENTS.md` should briefly route standards-alignment refactors to the generic refactor prompt. | |
| **🟡 A4** | **🟡 Manual evolution loop** | |
| 🟡 A4.1 | 🟡 Update the Go skill manually when the Go standard changes. | |
| 🟡 A4.2 | 🟡 Run `/refactor-to-standard <standard> <scope>` against the chosen repo-root-relative scope after the standard update. | |
| 🟡 A4.3 | 🟡 The refactor prompt should inspect the standard, restate the intended change briefly, refactor, validate, and report remaining gaps. | |
| 🟡 A4.4 | 🟡 Validate with repo-appropriate formatting and tests before considering the refactor done. | |
| **🟡 A5** | **🟡 Artifact boundaries stay clean** | |
| 🟡 A5.1 | 🟡 The Go skill should not become a multi-mode workflow script. | |
| 🟡 A5.2 | 🟡 The refactor prompt should not redefine the Go standard; it should consume the selected standard artifact. | |
| 🟡 A5.3 | 🟡 Future language skills can plug into the same system shape without changing `AGENTS.md` fundamentally. | |

---

## X2 spike update — generic refactor prompt packaging and invocation

Confirmed for the MVP:

- canonical prompt path: `.agents/prompts/refactor-to-standard.md`
- Pi wiring: `.pi/settings.json` loads `../.agents/prompts`
- Pi command: `/refactor-to-standard <standard> <scope>`
- scope semantics: repo-root-relative paths; `*` means whole repo
- prompt flow: inspect standard and scope, restate intent briefly, refactor, validate, report remaining gaps

## X3 spike update — initial Go standards

X3 is resolved.

The user accepted the full proposed initial convention set, so the repo's first written Go standard is now:

- `gofmt` is mandatory
- validation uses the narrowest sensible repo-root-relative test scope
- Go tests use the standard `testing` package
- test helpers with `*testing.T` use `t.Helper()`
- table-driven tests are used when there is a real case matrix
- `t.Parallel()` is opt-in and only for clearly isolated tests
- `context.Context` goes first on cancelable, I/O-heavy, request-scoped, or long-running operations
- errors are wrapped with `%w`, inspected with `errors.Is` / `errors.As`, and written in lowercase unless a leading token must stay uppercase
- concrete types are preferred; interfaces stay small and consumer-driven
- `panic` is avoided in normal production control flow

These conventions are now scaffolded into the canonical Go skill.

## Implementation artifacts

The MVP scaffolding for this system now lives in:

- `AGENTS.md`
- `.agents/skills/go-coding-standard/SKILL.md`
- `.agents/prompts/refactor-to-standard.md`
- `.pi/settings.json`

## Spikes

- **X1 — cross-harness skill layout and invocation:** `planning/initiatives/mvp/D-289-go-skill/spike-skill-portability.md` ✅
- **X2 — generic refactor prompt packaging and invocation:** `planning/initiatives/mvp/D-289-go-skill/spike-refactor-prompt.md` ✅
- **X3 — first-cut Go standard content and validation boundaries:** `planning/initiatives/mvp/D-289-go-skill/spike-go-standard.md`

## Open questions

Current unresolved questions are tracked in `planning/initiatives/mvp/D-289-go-skill/open_questions.md`.
