# Translations

This page is for developers working on the Nextcloud app (`cassini-app` and
`cassini-viewer`) and for whoever looks after the Weblate project. It explains
how the app's own words (its buttons, headings and messages) reach a reader in
their language, what to do when you add a string or a language, and how the
Weblate project is set up.

The app uses [typesafe-i18n](https://github.com/ivanhofer/typesafe-i18n):
developers write English in TypeScript dictionaries, typesafe-i18n generates
the types that check every use of a string, and translators work on JSON
copies of those dictionaries in [Weblate](https://weblate.codemyriad.io/projects/cassini/).
Every language ships with the app, so a translation reaches readers in the
next release.

Translators don't need this page. Their guide is [Help translate Cassini](https://gocassini.com/docs/guides/translate)
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

A string written straight into a component, rather than taken from a
dictionary, appears in English in every language. Code review is the check.

### Not translated yet

**Raw errors from Nextcloud or the system** (an HTTP failure, a file-system
error, the operator's state and stage names) stay English, on purpose: they
are what an administrator searches for and quotes in a bug report. Where one
sits inside a translated sentence, it is a parameter and stays as it came.

**The note on a recording share** (`share_note.go`), which Nextcloud also puts
in its share email, is English for every recipient. Translating it is tracked
in #476.

### Sentences the operator writes

The operator (Go) writes some sentences the app shows, and keeps writing them
in English for its log, the support bundle, the `cassini doctor` command and
older apps. Alongside each it sends a code and the values the sentence
embeds, and the app words the sentence again in the reader's language:

- **Doctor's check rows** (`operator/doctorText.ts`). Each check has a `code`
  and `params`: the paths, groups, counts and so on in its message, the
  blocking row's id (not its English name) for `check_blocked`, and, where
  one code has several sentences, a `variant` naming which. Each remedy step
  has an `id` and `params`. The dictionary is `app.doctor_checks`.
- **Why a recording is blocked** in the recording-time migration:
  `blocked_items[].reason_code` and `reason_params`, beside the English
  `reason` (`blockedReason` in the same module).
- **Doctor's refusals**: a route that refuses with a sentence for the reader
  (a secret managed by the deployment, a test room Cassini could not create,
  the migration still counting) sends `error_code` beside `error`
  (`writeCodedJSONError`; `operatorErrorText` in the same module). Errors
  that embed a raw failure keep only `error`.
- **The migration's "What this does in Nextcloud"** (`operator/recordedAtNotices.ts`)
  and **the setup notice's cause** (`operator/setupHealth.ts`, by step).

Anything the app cannot word shows the operator's English: a code, step,
variant or reason this build does not know (a newer operator), or a missing
param (an older operator, or a file blocked before it sent codes).

**Adding a check, a step or a reason.** Every code, step id, reason,
`variant`, sharing issue, `reindex` outcome and `error_code` the operator and
the recorder can emit is listed in `cassini-app/src/operator/doctorCatalog.json`.
Two tests keep it honest:

1. `cassini-operator/internal/operator/doctor_catalog_test.go` reads the Go
   source of the files that build Doctor's rows (and the recorder's
   `talk/readiness.go`, `talk/unreachable.go` and `cassini/doctor.go`) and
   collects every string literal in a code position: a row's `Code`, a
   step's `ID`, a blocked file's `code`, a sharing issue's `id`, the code
   passed to `writeCodedJSONError`, and the value of `"variant"` or
   `"reindex"` in a params map. It fails if that set differs from the JSON, if a step
   written there has no `ID`, or if a row is built in a file it does not
   read. Regenerate the JSON with
   `CASSINI_UPDATE_DOCTOR_CATALOG=1 go test ./internal/operator -run TestDoctorCatalog`
   (in `cassini-operator`).
2. `cassini-app/src/operator/doctorText.test.ts` fails unless `doctorText.ts`
   words exactly what is in each of the JSON's lists.

So a new code fails CI until it has English in
`cassini-viewer/src/i18n/en/app/doctor_checks.ts` and a line in
`doctorText.ts`. Write the English as the Go sentence, word for word.
`preview/doctorScenarios.test.ts` words every Doctor preview row and checks
it gives back the fixture's English, so keep the fixtures' params with their
messages.

What the tests do not see, by design (keep to the patterns above instead):

- A code, id or variant held in a constant or a variable rather than written
  as a literal where it is used.
- A step whose label is passed in from elsewhere (doctor's advice): it may
  have no id and stays English.
- A Go sentence reworded under an existing code: change the dictionary in the
  same commit.

## Languages

| Code | Language | Note |
|---|---|---|
| `en` | English | The source. Developers write it in TypeScript. |
| `fr` | French | |
| `es` | Spanish | |
| `it` | Italian | |
| `pt-PT` | Portuguese (Portugal) | Also shown to Brazilian Portuguese users, as the closest we have. |
| `de` | German | Informal ("du"), Nextcloud's `de`. |
| `de-DE` | German (formal) | Formal ("Sie"), Nextcloud's `de_DE`. A string not translated here shows the informal German, then English. A browser's `de-DE` also gets it. |
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

Inside Nextcloud and in the standalone viewer every language is bundled: the
embedded entry points must stay a single file
(`scripts/assert-embedded-single-bundle.mjs`). Builds drop untranslated
(empty) strings (`cassini-viewer/scripts/i18n-bundle-plugin.mjs`).

The public embed bundles English only. Each other language's `common` and
`viewer` strings are published beside `viewer.js` as `i18n/<lang>.json` (a
regional variant such as `de-DE` already has its base language merged in), and
the embed fetches the one it needs before it shows anything. If that fetch
fails, for example under a page's CSP, it shows English (D-920).

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
`formatCompactCount(n)` ("7.5K"), `durationFormat({ hours, minutes, seconds }, "short" | "narrow")`
for a length of time, and `relativeNow()` for an age under a minute. Don't
write a unit or "just now" into a dictionary string.

A count in a message formats itself, using typesafe-i18n's
[formatters](https://github.com/ivanhofer/typesafe-i18n/tree/main/packages/runtime#format-passed-in-arguments)
and [plural syntax](https://github.com/ivanhofer/typesafe-i18n/tree/main/packages/runtime#plural)
(see Counts, below): `{count:number|num}` runs the `num` formatter (`|compact` for "7.5K"), defined in `cassini-viewer/src/i18n/formatters.ts`,
which writes the number in the reader's locale, not their language. Pass the
raw number. `formatCount(n)` is only for a number in a string with no plural
(`"{done} of {total}"`).

`durationFormat` joins `Intl.NumberFormat` units ("1 hr 32 mins") rather than
using `Intl.DurationFormat`, which only some runtimes have and which words
some units differently, so every reader sees the same text. Don't name a locale
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
- A phrase put into a sentence (a bold title, a link) carries none of the
  sentence's punctuation: `"{title}, like {example}"` with `"Ask it something"`,
  so a translator can move the comma.
- A count is one key in typesafe-i18n's plural syntax (below), never a `_one` /
  `_other` pair the code picks between.
- One key per meaning, even when the English matches. "Export" the button and
  "Export" the heading can need different words.
- Keep product terms the same everywhere. They are in the Weblate glossary.

### Counts

Plurals use [typesafe-i18n's plural syntax](https://github.com/ivanhofer/typesafe-i18n/tree/main/packages/runtime#plural).
typesafe-i18n picks the form from the number and the language's plural rules
(`Intl.PluralRules`), so Polish and Croatian translators write their few and
many forms without a code change. Write the English with two forms, the plural
next to the noun, and pass the raw number:

```ts
// Plural; see the translator guide. {count}: meetings.
meetings: "{count:number|num} {{meeting|meetings}}",
```

```svelte
{$LL.viewer.browse.tags.meetings({ count: tag.meetings })}
```

- `{{one|other}}` binds to the nearest argument before it (or the first one in
  the string). When a string has more than one argument, name it:
  `"{voices:number|num} {{voices:voice|voices}} on {devices:number|num} {{devices:device|devices}}"`.
  A plural can also bind to an argument that is not shown:
  `"{{count:Remove it|Remove them}}"`.
- `??` inside the braces is the number, unformatted. Use it only for whole-sentence
  forms of small numbers (`{{count:One recording check needs attention|?? recording checks need attention}}`);
  otherwise put `{count:number|num}` outside the braces.
- No `{placeholder}` and no `:` inside `{{…}}`: the parser cannot read them.
- With two forms, 0 takes the plural. `{{zero|one|other}}` (three) or the full
  six, `{{zero|one|two|few|many|other}}`, give 0 its own form; four or five
  forms do not work.
- A different message for exactly one item that is not a plural, such as naming
  the meeting (`delete_failed_named`: "“{title}” could not be deleted."), is a
  separate key; the counted key still has its plural.
- Comment every count key `// Plural; see the translator guide.` and say what
  the number counts.

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
files, so its components are set to ask a maintainer (see below): a
translator's **Start new translation** sends the project's administrators a
"new language requested" notification in Weblate instead of creating files.
Requests can also arrive as a GitHub issue titled like "Translation: Swedish".

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
  The project maps the `de-DE` folder to Weblate's formal German
  (Project › Settings › Language aliases: `de-DE:de@formal`); without it
  Weblate reads `de-DE` as a second copy of `de` and refuses the component.
- **Adding new translation:** "Contact maintainers about new translation" (see
  Adding a language).
- **Repository:** `codemyriad/gocassini`. Weblate pulls `main` and pushes to
  the `weblate-translations` branch. A maintainer opens and merges the pull
  request from that branch. `nextcloud-common` holds the clone; the other two
  point at it (`weblate://cassini/nextcloud/nextcloud-common`).
- **Commit delay:** 24 hours, so a day's work arrives as one change.
- **Translation flags:** `check-glossary` (a translation that doesn't use the
  glossary's word is flagged) and
  `placeholders:r"(?<!\{)\{[a-zA-Z0-9_]+(?::[a-z]+)?(?:\|[a-z]+)?\}(?!\})|\{\{|\}\}|\?\?"`.
  The placeholders check compares the matches in the source with those in the
  translation, so it flags a translation that drops or changes a `{name}` or a
  `{count:number|num}`, or loses the plural's `{{`, `}}` or `??`. The plural's
  words are not matched (they are the translation), and the lookarounds keep
  `{meeting|meetings}` inside `{{…}}` from reading as a placeholder. Weblate
  has no check for typesafe-i18n's plural syntax itself, and a `regex:` flag
  would apply to every string, with or without a plural.
- **Add-ons:** "Customize JSON output" (2-space indent, keys in source order,
  matching `i18n:sync` so its rewrites don't show as changes) and "Squash Git
  commits" (one commit per language).
- **Reviews:** on for the project. A string is approved only after a reviewer
  accepts it.

**Translator context.** The `//` comments above English keys don't survive
into the JSON, so `npm run i18n:explain -w cassini-viewer` copies each one
(and its section's comment) onto the English string in Weblate as its
explanation, which Weblate keeps in its own database and shows beside every
translation. Run it after merging new or changed comments to `main`; it sends
only what differs. `-- --dry-run` prints them. It needs a Weblate API token
(`WEBLATE_TOKEN`, or `~/.config/weblate/codemyriad-token`).

A GitHub webhook (`https://weblate.codemyriad.io/hooks/github/`, push events)
tells Weblate when `main` changes, so new English strings show up without
waiting for its daily pull.

### Seeding a language with machine translation

A new language starts with machine translations, so translators correct text
rather than starting from nothing:

1. Translate the English JSON with an LLM, file by file, using the
   cross-app glossary (Silvio's `ux-draft/i18n/GLOSSARY.md`: it, fr, es, pt-PT;
   other languages follow its rules and keep their own terms consistent).
2. Upload each file in Weblate (the language's page › Files › Upload
   translation) with the method **"Add as translation needing edit"**.

Weblate writes strings needing edit into the JSON like any other, so the seeds
reach the repository with the next "Translations from Weblate" pull request
and ship in the release after it. That is deliberate: machine German reads
better to a German user than English does. In Weblate they stay marked as
needing edit, and out of the translated count, until a person accepts or
rewrites them. Don't commit seeds to the repository directly: they would skip
Weblate, and nothing would mark them as unchecked.

### Checking progress

```sh
npm run i18n:sync -w cassini-viewer -- --coverage
```

prints how many strings each language has in the repository. Weblate's project
page shows the same, plus what is waiting for review.
