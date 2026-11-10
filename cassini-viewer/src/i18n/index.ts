// The one import for translated text (docs/translations.md).
//
//   import { LL } from "cassini-viewer/i18n";      // in the shell
//   import { LL } from "../i18n";                  // in the viewing layer
//   <button>{$LL.viewer.export.button()}</button>
//
// Outside a component, read the store once: `get(LL).viewer.embed.badge()`.
//
// Every language is bundled. The embedded entries must stay one file
// (scripts/assert-embedded-single-bundle.mjs), and the dictionaries are small,
// so there is nothing to load later. English is set the moment this module is
// imported, so a component that renders before any entry point chooses a
// language (or in a unit test) gets English rather than an empty string.
import { get } from "svelte/store";

import { LL, locale, setLocale } from "./i18n-svelte.js";
import type { Locales } from "./i18n-types.js";
import { baseLocale, locales } from "./i18n-util.js";
import { loadAllLocales } from "./i18n-util.sync.js";

loadAllLocales();
setLocale(baseLocale);

export { LL, locale, locales, baseLocale };
export { dateFormat, formatLocale, nextcloudLocale, numberFormat, relativeTimeFormat, useFormatLocale } from "./format.js";
export type { Locales };

// A language people may ask for under another name. Nextcloud's pt_BR and
// plain "pt" get European Portuguese, the only Portuguese we have, which reads
// better to a Brazilian than English does.
const ALIASES: Record<string, Locales> = {
  pt: "pt-PT",
  "pt-br": "pt-PT",
};

// The first candidate we have a translation for, else English. Candidates come
// most-preferred first, in any of the spellings in use: Nextcloud's "de_DE",
// a browser's "de-DE", an HTML lang="de".
//
// Nextcloud's two Germans ("de" informal, "de_DE" formal) both get our one
// German, which is informal (docs/translations.md).
export function matchLocale(candidates: readonly (string | null | undefined)[]): Locales {
  const byLower = new Map(locales.map((l) => [l.toLowerCase(), l]));
  for (const raw of candidates) {
    const tag = raw?.trim().replace(/_/g, "-").toLowerCase();
    if (!tag) continue;
    const exact = byLower.get(tag) ?? ALIASES[tag];
    if (exact) return exact;
    const language = tag.split("-")[0];
    const sameLanguage = byLower.get(language) ?? ALIASES[language];
    if (sameLanguage) return sameLanguage;
  }
  return baseLocale;
}

// The language the Nextcloud user picked in their personal settings, or null
// outside Nextcloud. OC.getLanguage() is the setting itself; the page's
// <html lang> carries the same value when OC is not there yet.
export function nextcloudLanguage(win: Window = window): string | null {
  const oc = (win as unknown as { OC?: { getLanguage?: () => string } }).OC;
  try {
    const fromOC = oc?.getLanguage?.();
    if (fromOC) return fromOC;
  } catch {
    // Fall through to the page's lang.
  }
  return win.document?.documentElement?.lang || null;
}

// A `?lang=pt-PT` on a standalone page's address, to see it in another
// language without changing the browser's. Null when there is none.
export function urlLanguage(loc: Location | undefined = globalThis.location): string | null {
  try {
    return loc ? new URLSearchParams(loc.search).get("lang") : null;
  } catch {
    return null;
  }
}

// The reader's browser languages, most-preferred first.
export function browserLanguages(nav: Navigator | undefined = globalThis.navigator): string[] {
  if (!nav) return [];
  return [...(nav.languages ?? []), nav.language].filter(Boolean);
}

// Switch every translated string on the page, and return what was chosen.
export function useLocale(candidates: readonly (string | null | undefined)[]): Locales {
  const chosen = matchLocale(candidates);
  if (get(locale) !== chosen) setLocale(chosen);
  return chosen;
}
