---
shaping: true
---

# Make advertised Nextcloud support verifiable

Date: 2026-09-20. Status: shape B selected and implementation authorized; hosted qualification is in progress.

## Source

> We currently test with nextcloud 35 as highest version, and what exact lowest version?
> When did we add 35 testing?
> What will we need to do when 36 will be out?
>
> On the app store we say:
>
> Releases
> Nextcloud version	Stable channel	Nightly channel	All releases
> 35		0.2.0-beta.7 (Unstable)	35
> 34		0.2.0-beta.7 (Unstable)	34
> 33		0.2.0-beta.7 (Unstable)	33
> 32		0.2.0-beta.7 (Unstable)	32
>
> ...and I'm not sure we actually test for this. I wish you to collect information to improve our testing infrastructure in general.
> Think high level.

> Plan how to do this. Use shapeup methodology. /home/silvio/.local/share/shaping-skills/README.md

User's subsequent appetite choice:

> Two weeks for one engineer: close the compatibility and release-evidence gaps (Recommended)

User's subsequent support-policy choice:

> Support maintained majors, currently 33–35; retire 32 explicitly (Recommended)

## Problem

Cassini advertises Nextcloud 32–35, while automated Nextcloud integration runs
against a floating 33 image and pinned 34.0.0. A successful release does not
currently establish that each advertised major passed installed-product tests.
The oldest exact tested patch is not recorded in the inspected CI log.

This is a gap between our support promise and the evidence we retain. It can
recur on every upstream release even if we add the missing jobs once.

## Outcome

A maintainer can open a release and answer which Nextcloud versions and app
dependencies were exercised, which Cassini image ran, and what passed. Publishing
refuses incomplete or mismatched evidence. Adding an upstream major follows a
repeatable qualification process.

## Bet

**Confirmed appetite: two weeks for one engineer, with maintainer review.** This is a limit
on investment, not an estimate or delivery promise. Preserve the existing test
investment and connect it to compatibility and release decisions.

**Confirmed policy: support upstream-maintained majors, currently 33–35, and
retire 32 explicitly in the next release.** Implement retirement through the
release notes, support documentation and manifest; old published releases and
their image tags remain available. The exact patch floor within 33 is proposed
in shaping and must be qualified before changing the manifest.

The bet delivers an authoritative support inventory, installed-product evidence
across advertised majors, a release gate tied to exact artifacts, and scheduled
upstream checks. A separate bounded follow-on covers persisted upgrades; its
35→36 case must be qualified before advertising 36.

## Reading order

- [Shaping](shaping.md): evidence, requirements, alternative shapes, recommended
  mechanism, breadboard, scope boundaries, and Nextcloud 36 procedure.
- [Slices](slices.md): four demonstrable increments and completion criteria.
- [Release handoff investigation](spike-release-handoff.md): existing mechanisms
  and the concrete changes needed to enforce release evidence.
- [Upgrade investigation brief](spike-upgrades.md): the information needed before
  betting on the follow-on upgrade harness.

These documents follow the supplied [shaping skill](/home/silvio/.local/share/shaping-skills/shaping/SKILL.md)
and [breadboarding skill](/home/silvio/.local/share/shaping-skills/breadboarding/skill.md).
The skill links refer to this workstation; they are not repository dependencies.
