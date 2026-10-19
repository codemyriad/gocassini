import { describe, expect, it } from "vitest";

import { parseTimeHash } from "../core/transcript";
import { buildViewerHash, readViewerHash, readViewerRange, viewerUrlWithHash } from "./hashRouting";

// Routing must stay hash-only so the AppAPI embedded page's pathname/search is
// never rewritten. These tests pin: nav state round-trips through the hash;
// pathname + search are untouched; and the seek-time hash stays parseable by
// the existing parseTimeHash().

describe("buildViewerHash / readViewerHash round-trip", () => {
  it("encodes the meeting id into the hash and reads it back", () => {
    const hash = buildViewerHash({ meeting: "daily--2026-03-10" });
    expect(hash).toBe("#meeting=daily--2026-03-10");
    expect(readViewerHash(hash)).toEqual({ meeting: "daily--2026-03-10", tx: "" });
  });

  it("encodes meeting + tx and reads both back", () => {
    const hash = buildViewerHash({ meeting: "m1", tx: "asr" });
    expect(readViewerHash(hash)).toEqual({ meeting: "m1", tx: "asr" });
  });

  it("produces an empty hash for the list view (no meeting)", () => {
    expect(buildViewerHash({})).toBe("");
    expect(readViewerHash("")).toEqual({ meeting: "", tx: "" });
  });

  it("percent-encodes ids with reserved characters", () => {
    const id = "daily-meeting-2026-03-18--12:30";
    const hash = buildViewerHash({ meeting: id });
    expect(hash).toContain("%3A"); // ':' encoded
    expect(readViewerHash(hash).meeting).toBe(id);
  });

  it("keeps t= last so parseTimeHash still matches", () => {
    const hash = buildViewerHash({ meeting: "m1", tx: "clean", timeMs: 65000 });
    expect(hash.endsWith("t=65000ms")).toBe(true);
    expect(parseTimeHash(hash)).toBe(65000);
    // And the meeting/tx are still recoverable alongside the time.
    expect(readViewerHash(hash)).toEqual({ meeting: "m1", tx: "clean" });
  });
});

describe("viewerUrlWithHash", () => {
  it("rewrites only the fragment on the embedded AppAPI page (pathname/search untouched)", () => {
    const embedded =
      "https://nc.example.com/index.php/apps/app_api/embedded/gocassini/viewer?foo=bar";
    const next = viewerUrlWithHash(embedded, buildViewerHash({ meeting: "m1" }));
    const url = new URL(next);
    expect(url.pathname).toBe("/index.php/apps/app_api/embedded/gocassini/viewer");
    expect(url.search).toBe("?foo=bar");
    expect(url.hash).toBe("#meeting=m1");
  });

  it("clears the fragment when navigating back to the list", () => {
    const withMeeting = "https://example.com/viewer/#meeting=m1&t=1000ms";
    const next = viewerUrlWithHash(withMeeting, buildViewerHash({}));
    const url = new URL(next);
    expect(url.pathname).toBe("/viewer/");
    expect(url.hash).toBe("");
  });

  it("preserves the standalone pathname while swapping meetings", () => {
    const standalone = "https://example.com/portable/site/#meeting=a";
    const next = viewerUrlWithHash(standalone, buildViewerHash({ meeting: "b" }));
    const url = new URL(next);
    expect(url.pathname).toBe("/portable/site/");
    expect(url.search).toBe("");
    expect(url.hash).toBe("#meeting=b");
  });
});


describe("shared time ranges", () => {
  it("round-trips a selection alongside transcript and legacy seek time", () => {
    const range = { startMs: 0, endMs: 18500 };
    const hash = buildViewerHash({ meeting: "m1", tx: "clean", range, timeMs: 12000 });
    expect(readViewerRange(hash)).toEqual(range);
    expect(readViewerHash(hash)).toEqual({ meeting: "m1", tx: "clean" });
    expect(parseTimeHash(hash)).toBe(12000);
    expect(new URL(viewerUrlWithHash("https://example.com/viewer?foo=bar", hash)).search).toBe("?foo=bar");
  });
  it.each(["", "1", "-1,20", "20,10", "1,1", "1.5,2", "NaN,20", "0,Infinity", "1,2,3", "1e2,200", "0,9007199254740992", "1,2&range=3,4"])("ignores invalid range %s", (value) => {
    expect(readViewerRange(`#range=${value}`)).toBeNull();
  });
  it("does not write invalid bounds", () => {
    expect(buildViewerHash({ meeting: "m1", range: { startMs: 2, endMs: 1 } })).toBe("#meeting=m1");
  });
});
