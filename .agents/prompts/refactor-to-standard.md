---
description: Refactor a repo-root-relative scope to match a named standard artifact
argument-hint: "<standard> <scope>"
---
Refactor the requested scope to the current standard.

Standard artifact: `$1`
Scope: `${@:2}`

Instructions:

1. Load and inspect the referenced standard artifact before making changes. If it is a skill, read its full contents.
2. Interpret the requested scope as repo-root-relative. `*` means the whole repo.
3. Do not change files outside the requested scope unless a directly related compile/test fix requires it. If that happens, keep the change minimal and explain it.
4. Briefly restate the intended standards-alignment before editing.
5. Refactor to match the referenced standard. Prefer small, behavior-preserving changes over unrelated rewrites.
6. Run the narrowest sensible validation implied by the standard and the touched modules/packages.
7. Report:
   - what changed
   - what validation ran
   - any remaining gaps or follow-up work
