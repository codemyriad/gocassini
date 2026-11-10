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

// A number with the locale's digit grouping: 1,234 in English, 1.234 in
// German. A count key formats its own ("{count:number|num}", formatters.ts) and
// takes the raw number for its plural; this is for a number in any other
// string ("{done} of {total}").
export function formatCount(value: number): string {
  return numberFormat().format(value);
}

// A large count shortened: "7.5K" in English, "7500" or "7,5 Tsd." elsewhere.
export function formatCompactCount(value: number): string {
  return numberFormat({ notation: "compact", maximumFractionDigits: 1 }).format(value);
}

// "Now", for an age under a minute, in the locale's words ("now", "jetzt").
export function relativeNow(): string {
  return relativeTimeFormat({ numeric: "auto" }).format(0, "second");
}

// A length of time, in the locale's units: "1 hr, 5 min" (short) or "1h 5m"
// (narrow, for tight rows). Zero parts are left out; all zero shows the
// smallest part given as 0. Intl.DurationFormat where the runtime has it;
// otherwise each part through Intl.NumberFormat's units, joined by a space.
export type DurationParts = { hours?: number; minutes?: number; seconds?: number };
export type DurationStyle = "short" | "narrow";

type DurationFormatter = { format(duration: DurationParts): string };
type DurationFormatCtor = new (locale: string, options: { style: DurationStyle }) => DurationFormatter;
const durationFormats = new Map<string, DurationFormatter | null>();
const DURATION_UNITS = [
  ["hours", "hour"],
  ["minutes", "minute"],
  ["seconds", "second"],
] as const;

export function durationFormat(parts: DurationParts, style: DurationStyle = "short"): string {
  const given = DURATION_UNITS.filter(([key]) => parts[key] !== undefined);
  const shown = given.filter(([key]) => (parts[key] ?? 0) > 0);
  const used = shown.length > 0 ? shown : given.slice(-1);
  const value: DurationParts = {};
  for (const [key] of used) value[key] = used === shown ? parts[key] : 0;
  const native = nativeDurationFormat(style);
  if (native && shown.length > 0) {
    try {
      return native.format(value);
    } catch {
      // Fall through to the unit formatter.
    }
  }
  return used
    .map(([key, unit]) => numberFormat({ style: "unit", unit, unitDisplay: style }).format(value[key] ?? 0))
    .join(" ");
}

function nativeDurationFormat(style: DurationStyle): DurationFormatter | null {
  const key = `${current}|${style}`;
  if (durationFormats.has(key)) return durationFormats.get(key) ?? null;
  const Ctor = (Intl as unknown as { DurationFormat?: DurationFormatCtor }).DurationFormat;
  let format: DurationFormatter | null = null;
  try {
    format = Ctor ? new Ctor(current, { style }) : null;
  } catch {
    format = null;
  }
  durationFormats.set(key, format);
  return format;
}
