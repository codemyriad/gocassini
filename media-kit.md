# Gocassini Branding

## Core identity

Gocassini is a CLI meeting recorder focused on producing reliable, deterministic outputs.
It is not a conferencing platform and not a dashboarded SaaS.

## Primary contract

- Input: a Nextcloud Talk room (recording mode).
- Output: one recording artifact per session as `*.mkv` with per-participant tracks.
- Design choice: CLI-first, automation-friendly, and stable in downstream pipelines.

## Output contract promises

- A meeting produces one primary artifact per run.
- Track labels should remain stable and tied to session identity.
- Track naming convention: `participant:<stable_id>`
- Artifact naming defaults can be human readable and date-based.
- Prefer standard metadata fields over custom binary blobs.

## Developer principles

- Do one thing well.
- Keep behavior deterministic.
- Keep observability human-first by default.
- Keep logs machine-safe by preserving diagnostics on stderr.

## Language and tone

- Technical and concise.
- Practical and slightly playful.
- No corporate hype and no over-branding.
- Strong focus on “works and explains itself”.

## What we are not

- No GUI.
- No embedded dashboard.
- No productized workflow engine.
- No cloud service entitlement layer.

## Scope

- Current provider focus is Nextcloud Talk.
- Output stability is the first API contract.
- Provider abstraction is planned for future iterations.
