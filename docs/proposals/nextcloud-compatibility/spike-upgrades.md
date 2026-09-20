---
shaping: true
---

# R6 spike: retain a fixture through real upgrades

Status: investigation brief for the follow-on bet; not executed.

## Context

Fresh-install and restart checks do not establish upgrade behavior. The current
installed harness resets the stack and destroys volumes during cleanup. Its
storage-mode “upgrade” scenario starts a fresh server and simulates legacy state;
it is not a server major upgrade or a previous-release image transition.

## Goal

Describe a bounded fixture lifecycle that can upgrade the server and, separately,
Cassini while preserving recordings, configuration, identities and permissions.
Use available 34→35 versions to develop the mechanism, then substitute 35→36
when supported upstream artifacts exist.

## Questions

| ID | Question |
|---|---|
| R6-Q1 | Which Nextcloud, database, HaRP and Cassini volumes and registration records must persist, and which reset/cleanup hooks currently destroy them? |
| R6-Q2 | What is the supported adjacent-major upgrade sequence for the chosen images, Talk, AppAPI and ACL apps, including maintenance mode and app updates? |
| R6-Q3 | How can the existing archive/ACL validator run against a retained stack without invoking fresh-install setup or rewriting the fixture? |
| R6-Q4 | How is a previous published Cassini image upgraded through AppAPI, including manifest routes, registration and persisted configuration? |
| R6-Q5 | Which observable invariants distinguish a safe upgrade: artifact hashes, recording IDs, participant access, outsider denial, settings, embedded UI and ability to publish a new recording? |
| R6-Q6 | How are failed upgrades diagnosed and dedicated fixture resources cleaned without touching another local installation? |

## Acceptance

Produce a concrete state/volume map, supported command sequence, validation entry
points and a retained-fixture demonstration on an available adjacent version pair.
Describe exactly which upgrade scenarios the resulting evidence covers. Then
shape and slice the proposed two-week follow-on.

Scope boundary: one standard PostgreSQL + HaRP topology, one adjacent server
upgrade and one previous-release Cassini upgrade. The ACL-enabled path must prove
that access does not widen. Add a dependency-free/default-storage fixture only
if it fits; do not claim that coverage if it is omitted. Do not combine both
upgrades into a single untraceable step or build a full upgrade-path matrix.
