// How dates, times and numbers are written: the reader's locale, which is not
// always their language. Nextcloud keeps the two apart (Personal info ›
// Language, and › Locale), so a user can read the app in English and still see
// "13/03/2026, 14:30". Every Intl formatter in the app goes through here,
// rather than naming a locale itself.
//
// Until an entry point picks one, and in unit tests, this is en-GB: day before
// month and a 24-hour clock, what the app pinned before it followed the reader
// (D-685).
//
// Not for taking a date apart: code that reads a date's fields back out
// (meetingTime.ts's zonedPartsFormat) keeps its own fixed locale, because its
// output is parsed, not read.

const DEFAULT_FORMAT_LOCALE = "en-GB";

let current = DEFAULT_FORMAT_LOCALE;

export function formatLocale(): string {
  return current;
}

// Use the first candidate Intl can format for, most-preferred first, in either
// spelling ("de_DE" from Nextcloud, "de-DE" from a browser).
export function useFormatLocale(candidates: readonly (string | null | undefined)[]): string {
  for (const raw of candidates) {
    const tag = raw?.trim().replace(/_/g, "-");
    if (!tag) continue;
    try {
      const [supported] = Intl.DateTimeFormat.supportedLocalesOf(tag);
      if (supported) {
        current = supported;
        return current;
      }
    } catch {
      // Not a well-formed tag (Nextcloud's "en@pirate"); try the next.
    }
  }
  current = DEFAULT_FORMAT_LOCALE;
  return current;
}

// The Nextcloud user's locale setting, or null outside Nextcloud.
export function nextcloudLocale(win: Window = window): string | null {
  const oc = (win as unknown as { OC?: { getLocale?: () => string } }).OC;
  try {
    return oc?.getLocale?.() || null;
  } catch {
    return null;
  }
}

// Constructing an Intl formatter is the expensive part, and lists format every
// row on each keystroke, so each (locale, options) pair is built once.
const dateFormats = new Map<string, Intl.DateTimeFormat>();
const numberFormats = new Map<string, Intl.NumberFormat>();
const relativeFormats = new Map<string, Intl.RelativeTimeFormat>();

function cached<F>(cache: Map<string, F>, options: object, build: (locale: string) => F): F {
  const key = `${current}|${JSON.stringify(options)}`;
  let format = cache.get(key);
  if (!format) {
    format = build(current);
    cache.set(key, format);
  }
  return format;
}

export function dateFormat(options: Intl.DateTimeFormatOptions): Intl.DateTimeFormat {
  return cached(dateFormats, options, (locale) => new Intl.DateTimeFormat(locale, options));
}

export function numberFormat(options: Intl.NumberFormatOptions = {}): Intl.NumberFormat {
  return cached(numberFormats, options, (locale) => new Intl.NumberFormat(locale, options));
}

export function relativeTimeFormat(options: Intl.RelativeTimeFormatOptions = {}): Intl.RelativeTimeFormat {
  return cached(relativeFormats, options, (locale) => new Intl.RelativeTimeFormat(locale, options));
}
