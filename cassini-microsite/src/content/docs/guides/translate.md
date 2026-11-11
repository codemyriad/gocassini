---
title: Help translate Cassini
description: Cassini's buttons, menus and messages come in eleven languages, translated by the people who use them. How to fix a word, finish a language, or ask for a new one.
source: docs/translations.md
copied: "2026-11-11"
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

English, French, Spanish, Italian, Portuguese (Portugal), German (informal,
"du"), German (formal, "Sie"), Dutch, Danish, Polish and Croatian.

The two Germans match Nextcloud's: **Deutsch (Persönlich: Du)** gets the
informal one, **Deutsch (Förmlich: Sie)** the formal one. A string not yet
translated into formal German shows the informal German until it is.

Cassini uses the language set in your Nextcloud account (**Personal settings ›
Personal info › Language**). If yours isn't on the list, you see English.

New languages start with a machine translation, marked in Weblate as needing
editing. People already see it in Cassini, so until someone has checked a
string it may read oddly in the app, and that is the most useful thing to fix.

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
  wherever your language needs it. The same goes for `{count:number|num}`,
  which becomes a number. Weblate warns you if one goes missing.
- **Check the glossary.** It's in the panel beside each string and gives the
  agreed translation of Cassini's terms (meeting, room, insight, tag). Using
  the same word everywhere matters more than finding the perfect one.
- **Say what you weren't sure about.** Use a string's comments to ask, or to
  explain a choice.

## Numbers and plurals

A string with a count has its forms in double braces, chosen by the number:

```
{count:number|num} {{meeting|meetings}}
```

- **Two forms** are `one|other`: "1 meeting", "5 meetings". 0 takes the second.
- **More forms:** write all six, in this order, `zero|one|two|few|many|other`.
  Skip none; leave a slot empty if your language doesn't use it. An empty
  `zero` lets 0 follow your language's own rule.
- **`??`** inside the braces stands for the number.
- Keep the `{placeholders}`, outside the braces, as they are. A name before the
  forms, as in `{{devices:device|devices}}`, says which number they follow:
  keep it, and add one if you move the forms before their number.
- Apart from that name, don't put a colon (`:`) or a `{placeholder}` inside
  the double braces.

Polish, for `{count:number|num} {{meeting|meetings}}`:

```
{count:number|num} {{|spotkanie||spotkania|spotkań|spotkania}}
```

1 spotkanie, 3 spotkania, 5 spotkań, 22 spotkania, 0 spotkań. Zero and two are
empty: Polish has no form for two, and 0 takes the "many" form.

## When you'll see it

Translations are reviewed, then go into the next Cassini release. Once your
administrator updates the app, you'll see them in Nextcloud.

## Add your language

If your language isn't on the list, you can ask for it from Weblate itself:

1. Sign in to Weblate and open the [Cassini project](https://weblate.codemyriad.io/projects/cassini/).
2. Open any of the three parts (**nextcloud-app**, **nextcloud-viewer** or
   **nextcloud-common**) and choose **Start new translation**.
3. Pick your language and send the request. Weblate lets us know.

If you'd rather, [open an issue](https://github.com/codemyriad/gocassini/issues/new)
titled "Translation: <your language>" instead.

Weblate can't start the language on its own: Cassini has to be told that a
new language exists, which takes a small change to the app. We make that
change, fill the language with a machine translation marked as needing
editing, and let you know when it's in Weblate. From then on it works like any
other language:

- Start with the strings people see most: **nextcloud-common**, then
  **nextcloud-viewer**.
- Agree on your words for Cassini's terms (meeting, room, insight, tag) in the
  glossary early, so everyone translating your language uses the same ones.
- If your language has more than two plural forms, check those strings first
  (see Numbers and plurals). Machine translation gets them wrong most often.

The language goes into the next Cassini release. Any string not translated yet
shows in English until it is.
