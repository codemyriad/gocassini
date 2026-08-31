import { describe, expect, it } from "vitest";
import {
  captureDirName,
  isTalkBundleURL,
  mergeMuteIntervals,
  normalizeRootPath,
  roomTokenFromPath,
  talkCallScopes,
} from "./protocol";

describe("normalizeRootPath", () => {
  it("treats a domain-root install as /", () => {
    expect(normalizeRootPath("")).toBe("/");
    expect(normalizeRootPath(null)).toBe("/");
  });

  it("normalizes a subfolder install to a single trailing slash", () => {
    expect(normalizeRootPath("/nextcloud")).toBe("/nextcloud/");
    expect(normalizeRootPath("/nextcloud/")).toBe("/nextcloud/");
    expect(normalizeRootPath("nextcloud")).toBe("/nextcloud/");
  });
});

describe("talkCallScopes", () => {
  it("covers both the pretty and index.php call URLs", () => {
    expect(talkCallScopes("")).toEqual(["/call/", "/index.php/call/"]);
  });

  it("keeps the install subfolder", () => {
    expect(talkCallScopes("/nextcloud")).toEqual(["/nextcloud/call/", "/nextcloud/index.php/call/"]);
  });

  it("never claims the root scope, which would replace core's own worker", () => {
    // Nextcloud's Files app registers its preview service worker at "/", and a
    // same-scope registration replaces rather than coexists.
    for (const scope of [...talkCallScopes(""), ...talkCallScopes("/nextcloud")]) {
      expect(scope).not.toBe("/");
    }
  });
});

describe("isTalkBundleURL", () => {
  it("matches Talk's entry bundle across its naming variants", () => {
    expect(isTalkBundleURL("https://cloud.example.com/apps/spreed/js/talk-main.mjs")).toBe(true);
    expect(isTalkBundleURL("https://cloud.example.com/apps/spreed/js/talk-main.js?v=abc")).toBe(true);
    expect(isTalkBundleURL("https://cloud.example.com/nc/apps/spreed/js/talk-main-a1b2c3.mjs")).toBe(true);
  });

  it("ignores Talk's other chunks and other apps", () => {
    expect(isTalkBundleURL("https://cloud.example.com/apps/spreed/js/talk-files-sidebar.mjs")).toBe(false);
    expect(isTalkBundleURL("https://cloud.example.com/apps/files/js/main.mjs")).toBe(false);
    expect(isTalkBundleURL("https://cloud.example.com/apps/spreed/css/talk-main.css")).toBe(false);
  });

  it("does not throw on a malformed URL", () => {
    expect(isTalkBundleURL("not a url")).toBe(false);
  });
});

describe("roomTokenFromPath", () => {
  it("reads the token from both URL shapes and subfolder installs", () => {
    expect(roomTokenFromPath("/call/abc123")).toBe("abc123");
    expect(roomTokenFromPath("/index.php/call/abc123")).toBe("abc123");
    expect(roomTokenFromPath("/nextcloud/index.php/call/abc123")).toBe("abc123");
  });

  it("returns null off a call page", () => {
    expect(roomTokenFromPath("/apps/files/")).toBeNull();
    expect(roomTokenFromPath("/call/")).toBeNull();
  });
});

describe("mergeMuteIntervals", () => {
  it("coalesces the adjacent spans polling produces", () => {
    expect(
      mergeMuteIntervals([
        [1000, 1250],
        [1250, 1500],
        [1500, 1750],
      ]),
    ).toEqual([[1000, 1750]]);
  });

  it("keeps genuinely separate mutes apart", () => {
    expect(
      mergeMuteIntervals([
        [1000, 1250],
        [9000, 9250],
      ]),
    ).toEqual([
      [1000, 1250],
      [9000, 9250],
    ]);
  });

  it("drops malformed spans rather than emitting negative durations", () => {
    expect(mergeMuteIntervals([[500, 100]])).toEqual([]);
  });
});

describe("captureDirName", () => {
  it("is unique per room and call, and safe as a file name", () => {
    expect(captureDirName("abc123", 1700)).toBe("capture-abc123-1700");
    expect(captureDirName("../../etc", 1700)).toBe("capture-etc-1700");
  });
});
