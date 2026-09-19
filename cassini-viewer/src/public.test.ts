import { describe, expect, it } from "vitest";

import { BADGE_HREF, BADGE_TEXT, ELEMENT_NAME, embedStylesheetHref, findEmbedScriptSrc } from "./public";

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
    expect(BADGE_TEXT).toBe("Recorded with Cassini");
  });
});
