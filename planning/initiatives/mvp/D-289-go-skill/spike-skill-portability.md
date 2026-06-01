---
shaping: true
---

# X1 Spike: Cross-harness skill layout and invocation

## Context

Before creating the actual skill and `AGENTS.md`, we needed to choose the canonical shared-skill layout and the initial invocation contract.

The user wanted:

- `AGENTS.md` as the durable scaffold
- skills for deeper engagement-specific policy
- Pi to work well now
- a path that does not paint us into a Pi-only corner
- a named and explicit way to run standards-driven refactors after the standard changes

## Goal

Describe the simplest shared structure that Pi can use natively and that other harnesses can adopt later with minimal friction.

## Questions

| # | Question |
|---|----------|
| **X1-Q1** | Which project skill directory gives us the best cross-harness starting point while still loading natively in Pi? |
| **X1-Q2** | What should count as the MVP “special command” for refactoring the repo to the current standard in Pi? |
| **X1-Q3** | How much of the invocation story belongs in `AGENTS.md` versus the skill itself? |

## Acceptance

Spike is complete when we can describe:

1. the canonical file layout for the shared Go standard skill
2. how `AGENTS.md` should route work to that skill
3. whether refactor-to-standard belongs inside that skill or outside it

## Findings

### Pi discovery and commands

1. Pi loads project context from `AGENTS.md`.
2. Pi loads project skills from both `.pi/skills/` and `.agents/skills/`.
3. Pi implements the Agent Skills standard and explicitly supports using skills from other harness-oriented directories.
4. Pi registers skills as `/skill:name` commands, so a skill already behaves like a named command in Pi without extra extension work.

### Repo baseline

1. The repo does not currently have a root `AGENTS.md`.
2. The repo does not currently have a project skill directory.
3. The repo already has a `.claude/` directory, which is a signal that cross-harness friendliness is relevant even if Pi remains the primary harness.

## Question log and responses

Q1:
- **Question:** Which project skill directory should be canonical for this system: `.agents/skills/` or a Pi-specific location?
- **Suggestion:** Use `.agents/skills/` as the canonical repo location, with Pi consuming it via native discovery.
- **Rationale:** It stays closest to the Agent Skills convention and gives us the best cross-harness starting point while still working in Pi today.
- **Alternatives:** Use `.pi/skills/` and accept Pi-first lock-in; duplicate the skill in both locations; keep the skill elsewhere and wire it in with Pi settings.
- **Response:** Seems right.

Q2:
- **Question:** For MVP, what should count as the “special command” that refactors the repo to the current standard?
- **Suggestion:** Make the Pi-native skill command the special command: `/skill:go-coding-standard` (with a natural-language refactor request or a `refactor <scope>` style argument).
- **Rationale:** It requires no custom extension work, keeps the behavior in the skill, and maps cleanly to other harnesses later.
- **Alternatives:** Build a custom Pi extension command now; rely only on natural-language prompting with no named command; add a repo script or Make target that the skill calls.
- **Response:** Separate the skill (Go, coding standards) and the refactor prompt (which can be skill / standard agnostic).

Q3:
- **Question:** How light should `AGENTS.md` be when mentioning this skill?
- **Suggestion:** Keep `AGENTS.md` to a 1–2 bullet mention: use the Go skill for Go authoring/review/refactors; keep all concrete rules in the skill.
- **Rationale:** This preserves `AGENTS.md` as the durable scaffold and avoids duplicating standards in two places.
- **Alternatives:** Include a sample `/skill:...` invocation in `AGENTS.md`; summarize a few top-level Go rules in `AGENTS.md`; put the full standard in `AGENTS.md` and treat the skill as secondary.
- **Response:** Seems right.

## Result

This spike resolved three decisions:

1. **Canonical Go skill location:**
   - `AGENTS.md`
   - `.agents/skills/go-coding-standard/SKILL.md`

2. **`AGENTS.md` role:**
   - keep it light
   - route Go work to the Go skill
   - do not duplicate the detailed standard there

3. **Refactor-to-standard separation:**
   - the Go skill should remain a standards artifact, not a multi-mode workflow surface
   - the refactor mechanism should be a **separate generic prompt** that can later be reused with other standards and skills

## Follow-up created by this spike

This spike intentionally leaves one new design question open:

- where the generic `refactor-to-standard` prompt should live and how Pi should register it while keeping the canonical artifact as harness-neutral as possible

That follow-up is now tracked in:

- `planning/initiatives/mvp/D-289-go-skill/spike-refactor-prompt.md`
