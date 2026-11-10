---
title: Help translate Cassini
description: Cassini's buttons, menus and messages come in ten languages, translated by the people who use them. How to fix a word, finish a language, or ask for a new one.
source: docs/translations.md
copied: "2026-11-10"
---

Cassini's own words (its buttons, menus, headings and messages) are translated
on [Weblate](https://weblate.codemyriad.io/projects/cassini/), a website for
translating software together. You don't need to know how to code, and you can
fix a single word.

## What you can translate

Everything Cassini itself says, inside Nextcloud and in a recording shared on
another website.

What you can't translate there:

- **What people said.** A transcript stays in the language the meeting was held in.
- **Summaries, insights and tags.** These are written by the insight template
  your administrator chose. To get them in your language, an administrator
  opens **Operator › Insight templates**, duplicates the built-in template, and
  rewrites it in that language.

## Languages

English, French, Spanish, Italian, Portuguese (Portugal), German, Dutch,
Danish, Polish and Croatian.

Cassini uses the language set in your Nextcloud account (**Personal settings ›
Personal info › Language**). If yours isn't on the list, you see English.

New languages start with a machine translation, marked in Weblate as needing
editing. Until a person has checked a string, it may read oddly, and that is
the most useful thing to fix.

## Fix or finish a translation

1. Create an account at [weblate.codemyriad.io](https://weblate.codemyriad.io/accounts/register/).
2. Open the [Cassini project](https://weblate.codemyriad.io/projects/cassini/)
   and choose your language.
3. Pick a part: **nextcloud-app** is the Nextcloud app around the recordings,
   **nextcloud-viewer** is a meeting and its transcript, **nextcloud-common**
   is words both use. Then choose **Translate**. Weblate shows untranslated and
   unchecked strings first.
4. Write your translation and save. Or, if you're unsure, use **Suggest** and
   someone else can accept it.

A few things that help:

- **Keep the words in braces as they are.** In `Shared with {name}`, `{name}`
  is replaced with a person's name, so leave it untranslated and move it to
  wherever your language needs it. Weblate warns you if one goes missing.
- **Check the glossary.** It's in the panel beside each string and gives the
  agreed translation of Cassini's terms (meeting, room, insight, tag). Using
  the same word everywhere matters more than finding the perfect one.
- **Say what you weren't sure about.** Use a string's comments to ask, or to
  explain a choice.

## When you'll see it

Translations are reviewed, then go into the next Cassini release. Once your
administrator updates the app, you'll see them in Nextcloud.

## Ask for a new language

[Open an issue](https://github.com/codemyriad/gocassini/issues/new) titled
"Translation: <your language>". Adding a language takes a small change to
Cassini itself, so it can't be started from Weblate. We'll add it, seed it with
a machine translation, and let you know when it's ready to correct.
