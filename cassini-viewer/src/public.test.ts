import { get } from "svelte/store";
import { describe, expect, it } from "vitest";

import attributesDoc from "../ATTRIBUTES.md?raw";
import meetingViewSource from "./components/MeetingView.svelte?raw";
import {
  BADGE_HREF,
  badgeText,
  ELEMENT_NAME,
  EMBED_CSS,
  LAYOUTS,
  LIVE_ATTRIBUTES,
  PALETTE_PROPERTIES,
  PLAYBACK_ERROR_EVENT,
  embedLanguageHref,
  embedLanguages,
  embedStylesheetHref,
  loadEmbedLanguage,
  findEmbedScriptSrc,
} from "./public";
import { LL, setLocale } from "./i18n/i18n-svelte";
import { loadedLocales } from "./i18n/i18n-util";

// The bootstrap is guarded on import.meta.env.VITEST, so importing this module
// exercises the exported helpers without defining a custom element in node.

describe("findEmbedScriptSrc", () => {
  const doc = (currentScript: unknown, srcs: string[]) =>
    ({
      currentScript,
      querySelectorAll: () => srcs.map((src) => ({ src })),
    }) as unknown as Document;

  it("prefers currentScript, which is the reliable answer", () => {
    expect(
      findEmbedScriptSrc(
        doc({ src: "https://x.test/embed/v1/viewer.js" }, ["https://cdn.test/something-else.js"]),
      ),
    ).toBe("https://x.test/embed/v1/viewer.js");
  });

  it("ignores a currentScript with no src, which is an inline script", () => {
    expect(findEmbedScriptSrc(doc({ src: "" }, ["https://x.test/bundle.js"]))).toBe(
      "https://x.test/bundle.js",
    );
    expect(findEmbedScriptSrc(doc({}, ["https://x.test/bundle.js"]))).toBe("https://x.test/bundle.js");
  });

  it("falls back to the last script that looks like ours, under defer or async", () => {
    expect(
      findEmbedScriptSrc(
        doc(null, [
          "https://cdn.test/analytics.js",
          "https://gocassini.com/embed/v1/viewer.js",
          "https://cdn.test/other.js",
        ]),
      ),
    ).toBe("https://gocassini.com/embed/v1/viewer.js");
  });

  it("matches a versioned path and a cache-buster", () => {
    expect(findEmbedScriptSrc(doc(null, ["https://x.test/embed/v1.2.3/viewer.js?v=9"]))).toBe(
      "https://x.test/embed/v1.2.3/viewer.js?v=9",
    );
  });

  it("falls back to the last script at all, then to nothing", () => {
    expect(findEmbedScriptSrc(doc(null, ["https://x.test/bundle.js"]))).toBe("https://x.test/bundle.js");
    expect(findEmbedScriptSrc(doc(null, []))).toBe("");
  });
});

describe("embedStylesheetHref", () => {
  it("is the script's sibling, which is what makes a version directory work", () => {
    expect(embedStylesheetHref("https://gocassini.com/embed/v1/viewer.js")).toBe(
      "https://gocassini.com/embed/v1/viewer.css",
    );
    expect(embedStylesheetHref("https://gocassini.com/embed/v1.2.3/viewer.js")).toBe(
      "https://gocassini.com/embed/v1.2.3/viewer.css",
    );
  });

  it("carries the cache-buster over, so the pair expires together", () => {
    expect(embedStylesheetHref("https://x.test/embed/v1/viewer.js?v=7")).toBe(
      "https://x.test/embed/v1/viewer.css?v=7",
    );
  });

  it("degrades to a relative href rather than failing, when we cannot find ourselves", () => {
    // Only styling is lost; the viewer still mounts.
    expect(embedStylesheetHref("")).toBe("viewer.css");
    expect(embedStylesheetHref("not a url")).toBe("viewer.css");
  });
});

describe("the embed's published names", () => {
  it("are the ones ATTRIBUTES.md documents", () => {
    expect(ELEMENT_NAME).toBe("cassini-meeting");
    expect(BADGE_HREF).toBe("https://gocassini.com");
    expect(badgeText()).toBe("Recorded with Cassini");
    expect(PLAYBACK_ERROR_EVENT).toBe("playbackerror");
    expect(attributesDoc).toContain("`playbackerror`");
  });

  it("follow a theme change, which a page with its own light/dark switch needs (D-838)", () => {
    expect(LIVE_ATTRIBUTES).toEqual(["theme"]);
    expect(attributesDoc).toContain("`theme` is the exception");
  });
});

describe("the embed's styling hooks (D-838)", () => {
  it("consume every palette property ATTRIBUTES.md documents, falling back to the theme", () => {
    for (const property of PALETTE_PROPERTIES) {
      expect(attributesDoc).toContain(`\`${property}\``);
      const token = property.replace("--cassini-color-", "--color-").replace("--cassini-font-sans", "--font-sans");
      const theme = property.replace("--cassini-color-", "--cassini-theme-").replace("--cassini-font-sans", "--cassini-theme-font-sans");
      expect(EMBED_CSS).toContain(`${token}: var(${property}, var(${theme}));`);
      expect(EMBED_CSS).toContain(`${theme}: var(${token});`);
    }
  });

  it("lay out inline with hooks MeetingView actually carries", () => {
    expect(LAYOUTS).toEqual(["inline"]);
    expect(attributesDoc).toContain('`layout`');
    for (const hook of ["mv-title", "mv-meta", "mv-scroll", "mv-player"]) {
      expect(EMBED_CSS).toContain(`.${hook}`);
      expect(meetingViewSource).toMatch(new RegExp(`class="${hook}[ "]`));
    }
  });
});

describe("the embed's language", () => {
  // jsdom-free: a stand-in element with just what embedLanguages reads.
  const element = (own: string | null, page: string | null) =>
    ({
      getAttribute: (name: string) => (name === "lang" ? own : null),
      parentElement: page === null ? null : { closest: () => ({ getAttribute: () => page }) },
    }) as unknown as Element;

  it("prefers its own lang attribute, then the page's", () => {
    expect(embedLanguages(element("es", "de")).slice(0, 2)).toEqual(["es", "de"]);
    expect(embedLanguages(element(null, "de"))[0]).toBe("de");
  });

  it("falls back to the reader's browser when the page names no language", () => {
    expect(embedLanguages(element(null, null))).toEqual(
      [...(navigator.languages ?? []), navigator.language].filter(Boolean),
    );
  });
});

describe("language files", () => {
  it("sit beside the script, cache-buster and all", () => {
    expect(embedLanguageHref("https://gocassini.com/embed/v1/viewer.js?v=3", "de-DE")).toBe(
      "https://gocassini.com/embed/v1/i18n/de-DE.json?v=3",
    );
    expect(embedLanguageHref("", "fr")).toBe("i18n/fr.json");
  });

  const respond = (body: unknown, ok = true) =>
    (async () => ({ ok, json: async () => body })) as unknown as typeof fetch;

  it("load over English, so a missing string reads in English", async () => {
    const english = loadedLocales.en;
    expect(await loadEmbedLanguage("nl", "x", respond({ viewer: { embed: { badge: "Gemaakt met Cassini" } } }))).toBe(true);
    setLocale("nl");
    expect(get(LL).viewer.embed.badge()).toBe("Gemaakt met Cassini");
    expect(get(LL).common.action.save()).toBe(english.common.action.save);
    setLocale("en");
  });

  it("answer false, and change nothing, when the file cannot be had", async () => {
    const before = loadedLocales.pl;
    expect(await loadEmbedLanguage("pl", "x", respond(null, false))).toBe(false);
    expect(await loadEmbedLanguage("pl", "x", (async () => { throw new TypeError("CSP"); }) as typeof fetch)).toBe(false);
    expect(loadedLocales.pl).toBe(before);
  });
});
