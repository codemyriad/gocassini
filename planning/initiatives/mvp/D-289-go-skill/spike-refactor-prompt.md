---
shaping: true
---

# X2 Spike: Generic refactor prompt packaging and invocation

## Context

We have a confirmed split:

- the Go skill is a coding-standard artifact
- `AGENTS.md` stays light
- refactor-to-standard is a separate generic prompt, not part of the Go skill

That means we needed to choose where the generic prompt lives and how Pi should expose it as a named command while keeping the canonical artifact as shareable as possible.

## Goal

Describe the simplest prompt structure that works in Pi now and leaves us with a clean path for reuse alongside other standards later.

## Questions

| # | Question |
|---|----------|
| **X2-Q1** | Where should the canonical `refactor-to-standard` prompt live in the repo? |
| **X2-Q2** | What should the Pi invocation contract look like for the generic prompt? |
| **X2-Q3** | What should the prompt do, in order, when asked to align a scope to a standard? |

## Acceptance

Spike is complete when we can describe:

1. the canonical file layout for the generic prompt
2. the Pi-specific wiring needed to expose it as a command
3. the invocation contract and execution flow of the prompt itself

## Findings

### Pi prompt template mechanics

1. Pi auto-discovers prompt templates from `.pi/prompts/*.md`.
2. Pi can also load prompt templates from extra directories via `.pi/settings.json`.
3. Paths in `.pi/settings.json` resolve relative to `.pi`, so Pi can be pointed at a shared prompt directory such as `../.agents/prompts`.
4. Prompt templates in Pi become named `/prompt-name` commands.

### Portability constraint

1. Pi has strong first-class support for skills across shared directories.
2. Prompt-template registration is more harness-specific than skills.
3. That means the **prompt content** can be kept generic and reusable, while the **command wiring** may need to be harness-specific.

## Question log and responses

Q1:
- **Question:** Where should the canonical `refactor-to-standard` prompt live in the repo?
- **Suggestion:** Put it in `.agents/prompts/refactor-to-standard.md`, and let Pi load that directory via `.pi/settings.json`.
- **Rationale:** This keeps the canonical prompt artifact in the same shared, harness-neutral family as the skill, while Pi-specific command wiring stays in a tiny adapter file.
- **Alternatives:** Put the prompt directly in `.pi/prompts/`; duplicate it in both `.agents/prompts/` and `.pi/prompts/`; keep it only as documentation and invoke it manually.
- **Response:** LGTM.

Q2:
- **Question:** What should the Pi command contract be for the generic prompt?
- **Suggestion:** Use `/refactor-to-standard <standard> <scope>`, where `<standard>` is the governing skill/standard name and `<scope>` is a required file, package, module, or subtree.
- **Rationale:** Requiring both arguments keeps the prompt reusable and explicit, and it avoids surprising repo-wide edits when the user meant a narrower scope.
- **Alternatives:** Infer the standard from context and require only scope; make scope optional and default to the whole repo; create one prompt per standard instead of one generic prompt.
- **Response:** This seems right. Support wildcards so `*` can mean the whole repo. All scope paths should be repo-root-relative.

Q3:
- **Question:** What should the prompt do, in order, when aligning code to a standard?
- **Suggestion:** Make it follow a generic flow: inspect the referenced standard and scope, restate the intended changes briefly, apply the refactor, run the standard-relevant validation commands it can infer from the standard/repo instructions, and report any remaining gaps.
- **Rationale:** This keeps the prompt generic while still making standards-driven refactors deliberate, auditable, and repeatable.
- **Alternatives:** Edit immediately with no plan step; make the prompt plan-only and require a second command to apply; leave validation entirely outside the prompt.
- **Response:** LGTM.

## Result

This spike resolved the prompt packaging and invocation contract for the MVP.

### Canonical file layout

Use:

```text
AGENTS.md
.agents/skills/go-coding-standard/SKILL.md
.agents/prompts/refactor-to-standard.md
.pi/settings.json
```

### Pi wiring

Use project settings to expose the shared prompt as a Pi command:

```json
{
  "prompts": ["../.agents/prompts"]
}
```

### Pi command contract

Use:

```text
/refactor-to-standard <standard> <scope>
```

Where:

- `<standard>` identifies the governing standard artifact, such as `go-coding-standard`
- `<scope>` is repo-root-relative
- `*` means the whole repo

### Prompt execution flow

The generic prompt should:

1. inspect the referenced standard artifact and the requested scope
2. restate the intended alignment briefly
3. apply the refactor within the requested scope
4. run the validation it can infer from the standard and repo instructions
5. report any remaining gaps or follow-up work

## Follow-up created by this spike

With packaging and invocation confirmed, the next unknown is the content of the first-cut Go standard itself:

- formatting and validation baseline
- testing conventions
- API / error / abstraction rules
- how much of the standard should be conservative to current repo style vs aspirational

That follow-up is now tracked in:

- `planning/initiatives/mvp/D-289-go-skill/spike-go-standard.md`
