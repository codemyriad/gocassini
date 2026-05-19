# Structure: developer docs

## Audience

Developers changing code in this repo.

## Outcome

They should be able to understand the system boundaries, find the relevant component, run the common local flows, and know which artifact contracts matter before they edit code.

## Suggested flow

1. What Cassini is at the product and architecture level
2. The artifact pipeline and operating modes
3. Repo/component map
4. Local development entry points and common commands
5. Cross-cutting contracts and constraints
6. Recommended deeper reading by component

A single landing page is enough to start; expand into a small docset only when the audience needs more separation.

## Include

- `.run`, `.meeting`, `.site`, and portable `.opus` relationships
- CLI vs operator responsibilities
- component responsibilities across recorder, publisher, operator, control panel, viewer, and harness
- local development commands that are already part of the repo story
- links to component deep-dives and specs

## Exclude

- admin-only deployment detail that does not affect code changes
- public-facing product messaging
- speculative roadmap content unless the source explicitly treats it as current context

## Tone and framing

Technical, direct, and repo-oriented. Prefer concrete paths, commands, and contracts.

## Candidate outputs

- `README.md` for the developer docset
- optional follow-on pages for architecture or local development later

## Notes for the writer/agent

Optimize for helping someone decide where to read code next. When in doubt, keep the artifact contracts and component boundaries visible.
