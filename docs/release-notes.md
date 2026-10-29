# Writing the release notes

Cassini has two changelogs, written for different readers.

| | Developer changelog | Release notes |
|---|---|---|
| File | [`CHANGELOG.md`](../CHANGELOG.md), folded from [`changelog.d/`](../changelog.d/README.md) | `cassini-microsite/src/content/changelog/<date>-v<version>.md` |
| Reader | developers, and operators debugging an upgrade | administrators deciding whether to upgrade, and the people who use Cassini |
| Written | in each PR, one fragment per change | once per release, at release time |
| Says | every change, in detail | what someone will notice, and what they have to do |
| Shown | the repository | [gocassini.com/changelog](https://gocassini.com/changelog), the GitHub release, the App Store's changelog |

Fragments stay as they are: precise, one per PR. Release notes are written
from them, not instead of them. Writing them is an editing job. It reads a
hundred lines and keeps the dozen that matter to someone running Cassini.

## When

- **Every stable release has notes.** `prepare-release.sh` refuses a stable
  version without them (`--no-user-notes` overrides, for a release with nothing
  a user would notice).
- **Prereleases are optional.** Write notes for one you ask people to try.
  Without notes, the GitHub release and the App Store show its developer
  section, as before.
- **Notes cover everything since the last version that has notes.** A stable
  release after a run of alphas and betas summarises the whole run.

## Procedure

Do this before cutting the release, on a branch, like any change.

1. **Gather.** `./scripts/user-release-notes.sh --sources` prints every
   developer section since the last version with notes, then the pending
   fragments. That is the material, and the only material.
2. **Draft.** By hand, or ask Claude Code to "draft the release notes for
   0.3.0". The `release-notes` skill follows this page.
3. **Write** `cassini-microsite/src/content/changelog/<date>-v<version>.md`,
   with dots in the version written as dashes (`2026-11-04-v0-3-0.md`), using
   [the template](#template). Use the planned release date, and keep
   `draft: true`: the site deploys from main and hides drafts, so the notes
   don't appear before the release does.
4. **Review** against [the checklist](#checklist). Have someone who did not
   write the code read it.
5. **Merge** it, then cut the release (`./scripts/prepare-release.sh`). The
   release commit removes `draft: true`, so the notes go on the site with the
   release. `build/release/gocassini-<version>-notes.md` is the GitHub release
   body. Read it before you push.

`./scripts/user-release-notes.sh --version <v>` prints a version's notes,
`--release-body <v>` the GitHub release body, and `--changelog` the
`CHANGELOG.md` that goes into the App Store package.

## How to write them

### 1. Sort every entry

Put each line of the material in one of five piles:

- **Highlight**: something a reader would mention to a colleague. A new thing
  they can do, or an existing thing that is clearly better. One to five per
  release.
- **Improvement**: a real change, but not a headline.
- **Fix**: a bug that someone could have hit on a release that already had
  notes. That is usually the last stable release.
- **Action**: something an administrator has to do or know before
  upgrading. Examples: a new requirement, a changed default, a download that
  starts on its own, a removed setting, a renamed endpoint, a version of
  Nextcloud dropped.
- **Leave out**: everything else.

Leave out:

- Bugs introduced and fixed since the last release with notes. Readers never
  had them.
- Refactors, CI, tests, build caching, release tooling, and evidence
  collection.
- Runtime and library versions (`sherpa-onnx v1.13.7-cassini.8`), unless the
  version is itself the news.
- Copy edits, padding, alignment, button styling, contrast tweaks. Collapse
  these into one line if there are many.
- Documentation changes, unless the docs were wrong in a way that misled
  people.
- Developer-only additions (source maps, an internal API route). If
  integrators need them, give them one line in [*For integrators*](#template).

### 2. Collapse by what the reader sees

Group by the part of Cassini the reader recognises, not by PR or by
Added/Changed/Fixed. When a dozen entries polish one area, the reader needs one
line about what got better there.

> **From** eight entries in 0.2.0 about Doctor: the page header, the verdict
> callout, a next step for each check, blocking checks, why Nextcloud is
> unreachable, labelled commands, search coverage wording, refresh errors.
>
> **To:** **Doctor tells you what to do next.** Each problem leads with the
> step that fixes it. When one thing blocks the rest, it is the only row to act
> on. When Nextcloud cannot be reached, it says why.

### 3. Say what changed for the reader, not how

Fragments describe how something works. Notes describe what is different for
the person reading. Ask "what will they notice?" and write that. Name the
mechanism only when it helps them choose, configure or trust the change.

> **From:** "Parakeet Ultra replaces Parakeet v3 … Speech is now decoded with
> greedy search on every quality tier … participant names and the transcript
> vocabulary no longer bias decoding: the beam search that hints need dropped
> whole passages of speech."
>
> **To:** **Transcripts no longer lose passages of speech.** On a GPU, 0.2.0
> could leave whole passages out of a transcript. Cassini no longer decodes
> speech the way that lost them, and it now uses Parakeet Ultra. On a GPU,
> transcription takes about 40% less time and is about as accurate.
>
> And under *Before you upgrade*: the new model downloads on its own, and the
> previous one keeps transcribing until it's ready. A GPU now needs 7 GiB of
> free video memory, up from 5.5 GiB. Participant names no longer steer
> transcription.

The obvious headline would have been "more accurate transcripts". The
measurements didn't support it: on a GPU, the accuracy difference on our own
meetings was not significant. What changed for the reader was the missing
speech and the speed. **Headline what the evidence shows, not what the change
was meant to do.** Say so when something got slightly worse in exchange.

Give a number when you measured one and it means something to the reader.
"About 30% smaller audio files" is useful. "WER 8.10 → 7.25 on the MAI silver
set" belongs in the developer changelog or a linked report. Link that report
from the notes.

### 4. Tell administrators what to do

Anything in the *Action* pile goes under *Before you upgrade*, even when the
same change is also a highlight. Say what happens if nobody acts. Say where
the setting is. Say how to go back, if they can.

## Template

```markdown
---
title: "<What this release is about, in under ten words>"
date: YYYY-MM-DD
version: X.Y.Z
draft: true
---

<One to three sentences: who should upgrade, and the one or two things that make it worth it.>

### Highlights

**<An outcome, in plain words>.** <Two to four sentences: what is new or better, and where to find it.>

**<…>** <…>

### Before you upgrade

- <Each action, changed default, new requirement, or removed setting, and what to do about it.>

### Improvements

- **<Area>:** <One line that covers several entries.>

### Fixes

- <One line per fix someone on the last stable release could have hit. Put small interface fixes together on one line.>

### For integrators

- <API routes, CLI flags, environment variables, the embed. Leave this section out if it is empty.>

Every change is listed in the [developer changelog](https://github.com/codemyriad/gocassini/blob/main/CHANGELOG.md).
```

Leave out any section with nothing in it. The page shows `title` as the
heading, and the body's `###` headings sit under it. The App Store and the
GitHub release show the body without the title, so the first paragraph has to
stand on its own. Don't write "here", "this page" or "below" in it: the same
text appears in three places.

Write the way the existing posts on the site are written. Address the reader
as "you", use the present tense, and give concrete nouns: the setting's name,
where it lives, what happens. Leave out "we're excited", "powerful",
"seamless", and exclamation marks.

## Checklist

- [ ] Covers everything `--sources` printed. Every entry is in a pile, even
      if the pile is *Leave out*.
- [ ] Nothing in *Fixes* was introduced since the last release with notes.
- [ ] An administrator who reads only *Before you upgrade* is not surprised by
      the upgrade.
- [ ] No internal names (job stages, Go packages, runtime versions,
      environment variables outside *For integrators*).
- [ ] Every setting is named as it reads on screen in that release, with where
      to find it. Check the code at the release tag, not the fragment: the
      wording on screen often changed later.
- [ ] Every number was measured, and the developer changelog or a linked
      report says how.
- [ ] Short enough to read in a few minutes: about 300 words for a patch
      release, up to 800 for a minor release that ends a long run of
      prereleases.
