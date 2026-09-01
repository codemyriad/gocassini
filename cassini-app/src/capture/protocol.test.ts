import { describe, expect, it } from "vitest";
import {
  captureDirName,
  mergeMuteIntervals,
  roomTokenFromPath,
} from "./protocol";

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
