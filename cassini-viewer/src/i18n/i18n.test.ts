import { get } from "svelte/store";
import { afterEach, describe, expect, it } from "vitest";

import { LL, baseLocale, formatLocale, locales, matchLocale, nextcloudLanguage, nextcloudLocale, useFormatLocale, useLocale } from "./index";
import { formatDateRange } from "../viewer/insightName";
import { formatMeetingStartShort } from "../viewer/meetingTime";
import { mergeTranslations } from "./translations";
import en_viewer from "./en/viewer/index";

afterEach(() => {
  useLocale([baseLocale]);
});

describe("matchLocale", () => {
  it("reads every spelling a language arrives in", () => {
    expect(matchLocale(["fr"])).toBe("fr");
    expect(matchLocale(["de_DE"])).toBe("de"); // Nextcloud
    expect(matchLocale(["nl-BE"])).toBe("nl"); // a browser
    expect(matchLocale(["PT-pt"])).toBe("pt-PT");
  });

  it("gives every Portuguese the European one we have", () => {
    expect(matchLocale(["pt_BR"])).toBe("pt-PT");
    expect(matchLocale(["pt"])).toBe("pt-PT");
  });

  it("takes the first candidate it has, and English when it has none", () => {
    expect(matchLocale([null, "ja", "", "es-MX", "fr"])).toBe("es");
    expect(matchLocale(["ja", "zh-Hans"])).toBe("en");
    expect(matchLocale([])).toBe("en");
  });

  it("knows the ten languages the app ships", () => {
    expect([...locales].sort()).toEqual(["da", "de", "en", "es", "fr", "hr", "it", "nl", "pl", "pt-PT"]);
  });
});

describe("nextcloudLanguage", () => {
  const page = (oc: unknown, lang: string) =>
    ({ OC: oc, document: { documentElement: { lang } } }) as unknown as Window;

  it("prefers the user's Nextcloud setting, then the page's lang", () => {
    expect(nextcloudLanguage(page({ getLanguage: () => "it" }, "en"))).toBe("it");
    expect(nextcloudLanguage(page(undefined, "da"))).toBe("da");
    expect(nextcloudLanguage(page({ getLanguage: () => { throw new Error("not ready"); } }, ""))).toBeNull();
  });
});

describe("a language without a translation for a string", () => {
  it("shows the English string instead of an empty one", () => {
    const partial = mergeTranslations<typeof en_viewer>(en_viewer, { export: { button: "Exporter", download_audio: "" } });
    expect(partial.export.button).toBe("Exporter");
    expect(partial.export.download_audio).toBe("Download audio");
  });

  it("is what every component gets until Weblate fills it in", () => {
    useLocale(["hr"]);
    expect(get(LL).viewer.export.button().length).toBeGreaterThan(0);
  });
});

describe("dates and times in the reader's locale", () => {
  afterEach(() => {
    useFormatLocale([]);
  });

  const instant = { dateLabel: "2026-03-09 21:11", recordedAtUtc: "2026-03-09T21:11:00Z" }; // 22:11 in Rome (vitest.config.ts)

  it("writes day-month and a 24-hour clock until an entry point chooses (en-GB)", () => {
    expect(formatLocale()).toBe("en-GB");
    expect(formatMeetingStartShort(instant)).toBe("9 Mar 2026, 22:11");
  });

  it("follows the locale it is given, in Nextcloud's spelling too", () => {
    expect(useFormatLocale(["en_US"])).toBe("en-US");
    expect(formatMeetingStartShort(instant)).toMatch(/^Mar 9, 2026, 10:11\s?PM$/);
    useFormatLocale(["pt_PT"]);
    expect(formatMeetingStartShort(instant)).toBe("9/03/2026, 22:11");
  });

  it("skips a tag Intl cannot read and falls back to en-GB", () => {
    expect(useFormatLocale(["en@pirate", "de-DE"])).toBe("de-DE");
    expect(useFormatLocale(["en@pirate"])).toBe("en-GB");
  });

  it("reads the Nextcloud user's locale setting", () => {
    const page = { OC: { getLocale: () => "da_DK" } } as unknown as Window;
    expect(nextcloudLocale(page)).toBe("da_DK");
    expect(nextcloudLocale({} as Window)).toBeNull();
  });

  it("shortens a span the way the locale does, with plain spaces", () => {
    expect(formatDateRange(["2026-09-28", "2026-10-12"])).toBe("28 Sept – 12 Oct 2026");
    useFormatLocale(["en-US"]);
    expect(formatDateRange(["2026-10-01", "2026-10-12"])).toBe("Oct 1 – 12, 2026");
  });
});
