---
name: release-notes
description: Draft or review Cassini's user-facing release notes — the curated summary of a release shown on gocassini.com/changelog, the GitHub release and the App Store, as opposed to the per-PR developer CHANGELOG.md. Use when asked to write, draft, update or review release notes, "what's new", or the public changelog for a version, or when prepare-release.sh refuses a stable version for missing user-facing notes.
---

# Drafting user-facing release notes

The method is [`docs/release-notes.md`](../../../docs/release-notes.md). Read it in full first; this skill only says how to apply it.

1. **Material.** Run `./scripts/user-release-notes.sh --sources` on `release/X.Y` if that branch exists (on main otherwise), and work only from its output. If the release includes work that is not merged yet, ask which branches to include and read their `changelog.d/` fragments with `git show <branch>:changelog.d/<file>`.
2. **Sort.** Make a working list (not in the file) that assigns every entry to one pile: Highlight, Improvement, Fix, Action, or Leave out. Group the entries by the area a user recognises. Drop fixes for bugs introduced since the last release with notes: check each one against the developer sections that earlier notes covered.
3. **Check facts you are unsure of.** Use the code, `docs/`, or the PR (`gh pr view`). Settings must be named as they read on screen. If a number is not in the material or a linked report, don't use it.
4. **Write** `cassini-microsite/src/content/changelog/<date>-v<version-with-dashes>.md` from the template in the doc, with `version:` exactly equal to the release version and `draft: true` (the release commit publishes it). Then `./scripts/user-release-notes.sh --version <v>` must print it.
5. **Report** to the person:
   - the file path;
   - the word count;
   - each highlight, in one line;
   - the entries you left out, grouped, so they can object;
   - every fact you could not confirm.

Do not edit `CHANGELOG.md` or fragments to make the notes easier to write: they are the developer record.
