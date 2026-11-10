# Translations

How the Nextcloud app's own words (buttons, headings, messages) are translated,
how a developer adds a string, and how the Weblate project is set up. For
translators, the guide is [Help translate Cassini](https://gocassini.com/docs/guides/translate)
(source: `cassini-microsite/src/content/docs/guides/translate.md`).

## What is translated

- **Translated:** the words of the shell (`cassini-app`) and the viewing layer
  (`cassini-viewer`), inside Nextcloud, in the standalone viewer, and in the
  public `<cassini-meeting>` embed.
- **Not translated:** what people said. A transcript stays in the language of
  the meeting.
- **Not translated here:** summaries, insights and tags come from insight
  templates. To get them in another language, an administrator writes or
  duplicates a template in that language (Operator › Insight templates).
- **Not yet:** the app's name and description in the App Store (`appinfo/info.xml`)
  and the Cassini entry in Nextcloud's top menu, which Nextcloud draws itself.

Strings are being moved into the dictionaries area by area. Anything still
written into a component appears in English in every language.

## Languages

| Code | Language | Note |
|---|---|---|
| `en` | English | The source. Developers write it in TypeScript. |
| `fr` | French | |
| `es` | Spanish | |
| `it` | Italian | |
| `pt-PT` | Portuguese (Portugal) | Also shown to Brazilian Portuguese users, as the closest we have. |
| `de` | German | Informal ("du"). Nextcloud's formal German (`de_DE`) also gets it. |
| `nl` | Dutch | |
| `da` | Danish | |
| `pl` | Polish | |
| `hr` | Croatian | |

## Which language a reader sees

`matchLocale` in `cassini-viewer/src/i18n/index.ts` takes a list of language
tags, most preferred first, and returns the first one we have. It ignores
case and accepts `de_DE` or `de-DE`, and falls back to the base language
(`nl-BE` → `nl`). Anything it cannot match gets English.

| Surface | Asked in this order |
|---|---|
| Inside Nextcloud (`cassini-app/src/embedded.ts`) | The user's Nextcloud language (`OC.getLanguage()`, then `<html lang>`), then the browser |
| The standalone viewer and the operator's `/viewer` page | A `?lang=` on the address (e.g. `?lang=pt-PT`), then the browser |
| The public embed (`cassini-viewer/src/public.ts`) | The element's `lang` attribute, then the nearest `lang` on the page around it, then the browser |

Every language is bundled. The embedded entry points must stay a single file
(`scripts/assert-embedded-single-bundle.mjs`), and the dictionaries are small.

## Dates, times and numbers

These follow the reader's **locale**, which Nextcloud keeps separate from
their language (Personal info › Locale). Someone can read the app in English
and still see dates as `13/03/2026, 14:30`. Nextcloud uses the locale for dates
on its own pages, and the app does the same.

| Surface | Locale asked in this order |
|---|---|
| Inside Nextcloud | `OC.getLocale()`, then the Nextcloud language, then the browser |
| Standalone viewer | `?lang=`, then the browser |
| Public embed | The same as its language: `lang`, the page's `lang`, the browser |

Until an entry point chooses, and in unit tests, the locale is `en-GB`: day
before month, 24-hour clock.

Every date, time and number formatter goes through `cassini-viewer/src/i18n/format.ts`:
`dateFormat(options)`, `numberFormat(options)` and `relativeTimeFormat(options)`
return a cached `Intl` formatter for the current locale. On top of them:
`formatCount(n)` for every number put into a message, `formatCompactCount(n)`
("7.5K"), `durationFormat({ hours, minutes, seconds }, "short" | "narrow")` for
a length of time, and `relativeNow()` for an age under a minute. Don't write a
unit or "just now" into a dictionary string. Don't name a locale
(`"en-GB"`, `undefined`) in a formatter that shows a date to someone. The one
exception is code that takes a date apart to calculate with it
(`zonedPartsFormat` in `meetingTime.ts`), which keeps a fixed locale because
its output is parsed, not read.

## Adding a string

1. Add the key and its English text to `cassini-viewer/src/i18n/en/<area>/index.ts`:
   `viewer` for the viewing layer and the embed, `app` for the Nextcloud shell,
   and the root `en/index.ts` (`common`) for words both use.
2. Use it. In a component: `{$LL.viewer.export.button()}`. Outside one:
   `get(LL).viewer.export.button()` (`get` from `svelte/store`). Import `LL`
   from `cassini-viewer/i18n` in the shell and from `../i18n` (relative) inside
   the viewer.
3. Regenerate the types and the JSON, and commit both:

   ```sh
   npm run i18n:generate -w cassini-viewer   # or `npm run i18n -w cassini-viewer` to watch
   npm run i18n:sync -w cassini-viewer
   ```

   CI runs both and fails if anything changes (`.github/workflows/ci.yml`, the
   `cassini-viewer` UI job).
4. Tests check text through the dictionary, e.g. `get(LL).app.setup_notice.try_again()`,
   rather than copying the English into the test.

Writing strings that translate well:

- Name placeholders: `"Shared with {name}"`, never pieced together from
  fragments. Weblate checks that a translation keeps every `{name}`.
- No typesafe-i18n plural syntax (`{{meeting|meetings}}`). Translators would
  have to write it by hand, and Polish and Croatian need more forms than
  English has. Write two keys ("Copy transcript" and "Copy transcripts"), or
  rephrase ("Meetings: {count}").
- One key per meaning, even when the English matches. "Export" the button and
  "Export" the heading can need different words.
- Keep product terms the same everywhere. They are in the Weblate glossary.

Non-English files are Weblate's. Don't edit `<lang>/**/index.json` by hand
except through `i18n:sync`, or a Weblate change will conflict with yours.

## Adding a language

1. Copy one language's folder, e.g. `cassini-viewer/src/i18n/da`, to the new
   code (BCP 47, as Weblate writes it: `sv`, `pt-BR`).
2. Run `npm run i18n:generate -w cassini-viewer` and `npm run i18n:sync -w cassini-viewer`.
   The second empties every string in the copied JSON.
3. Add the code to the language test in `cassini-viewer/src/i18n/i18n.test.ts`
   and to the table above.
4. After it merges, add the language in each Weblate component (Tools › Start new translation).

Weblate cannot add a language by itself. Each one needs these TypeScript
files, so its components are set to ask a maintainer (see below).

## Weblate

Instance: <https://weblate.codemyriad.io>. Project: [Cassini](https://weblate.codemyriad.io/projects/cassini/).

One project for every Cassini app, with a category per app, so translators
share one glossary and one translation memory across them. Today only the
Nextcloud app is in it. Native apps join as their own categories (`android`,
`ios`, `mac`, `gnome`), each with components in that app's own format.

| Category | Component | Files | Base file |
|---|---|---|---|
| Nextcloud | `nextcloud-common` | `cassini-viewer/src/i18n/*/index.json` | `cassini-viewer/src/i18n/en/index.json` |
| Nextcloud | `nextcloud-viewer` | `cassini-viewer/src/i18n/*/viewer/index.json` | `cassini-viewer/src/i18n/en/viewer/index.json` |
| Nextcloud | `nextcloud-app` | `cassini-viewer/src/i18n/*/app/index.json` | `cassini-viewer/src/i18n/en/app/index.json` |
| — | `glossary` | Weblate's own (TBX) | — |

Component settings, all three the same:

- **File format:** JSON nested structure file, monolingual. The base file is
  English, written by `i18n:sync`. English is not edited in Weblate.
- **Language filter:** `^[^.]+$`. **Language code style:** BCP (`pt-PT`).
- **Adding new translation:** "Contact maintainers about new translation" (see
  Adding a language).
- **Repository:** `codemyriad/gocassini`. Weblate pulls `main` and pushes to
  the `weblate-translations` branch. A maintainer opens and merges the pull
  request from that branch. `nextcloud-common` holds the clone; the other two
  point at it (`weblate://cassini/nextcloud/nextcloud-common`).
- **Commit delay:** 24 hours, so a day's work arrives as one change.
- **Translation flags:** `check-glossary` (a translation that doesn't use the
  glossary's word is flagged) and `placeholders:r"\{[a-zA-Z0-9_]+\}"` (a
  translation that drops or changes a `{name}` is flagged).
- **Add-ons:** "Customize JSON output" (2-space indent, keys in source order,
  matching `i18n:sync` so its rewrites don't show as changes) and "Squash Git
  commits" (one commit per language).
- **Reviews:** on for the project. A string is approved only after a reviewer
  accepts it.

A GitHub webhook (`https://weblate.codemyriad.io/hooks/github/`, push events)
tells Weblate when `main` changes, so new English strings show up without
waiting for its daily pull.

### Seeding a language with machine translation

A new language starts with machine translations, so translators correct text
rather than starting from nothing:

1. Translate the English JSON with an LLM, file by file, using the glossary.
2. Upload each file in Weblate (the language's page › Files › Upload
   translation) with the method **"Add as translation needing edit"**.

Never commit seeded text to the repository. The JSON has no place to mark a
string as unchecked, so committed machine text would look finished and nobody
would review it. Uploaded with "needing edit", it shows as unfinished in
Weblate and in the coverage count until a person accepts or rewrites it.

### Checking progress

```sh
npm run i18n:sync -w cassini-viewer -- --coverage
```

prints how many strings each language has in the repository. Weblate's project
page shows the same, plus what is waiting for review.
