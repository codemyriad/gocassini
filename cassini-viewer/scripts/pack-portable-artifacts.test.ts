import { describe, expect, it } from "vitest";

import {
  canonicalPortableMeetingName,
  packArtifactDirectory,
} from "./pack-portable-artifacts.mjs";

describe("packArtifactDirectory", () => {
  it("delegates portable v3 production to the canonical Go packer", async () => {
    const calls: unknown[][] = [];
    const result = await packArtifactDirectory(
      "/tmp/example.meeting",
      "/tmp/example.opus",
      (...args: unknown[]) => calls.push(args),
    );

    expect(result).toEqual({ status: "write" });
    expect(calls).toEqual([
      [
        "cassini",
        ["pack", "/tmp/example.meeting", "--out", "/tmp/example.opus"],
        { stdio: "inherit" },
      ],
    ]);
  });
});

describe("canonicalPortableMeetingName", () => {
  it("strips the bundle suffix from meeting artifact directories", () => {
    expect(canonicalPortableMeetingName("/tmp/daily-meeting-2026-03-12--12:29.meeting")).toBe(
      "daily-meeting-2026-03-12--12:29",
    );
  });

  it("leaves plain directory names unchanged", () => {
    expect(canonicalPortableMeetingName("/tmp/daily-meeting-2026-03-12--12:29")).toBe(
      "daily-meeting-2026-03-12--12:29",
    );
  });
});
