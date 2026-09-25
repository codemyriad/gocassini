import { describe, expect, it, vi } from "vitest";
import { changeRetentionMode, type RetentionGroup, type RetentionSettings } from "./retention";
import { OperatorClient } from "./client";

describe("retention control", () => {
  it("copies the group on first split and restores inactive policies on later toggles", () => {
    const group: RetentionGroup = { mode: "group", policy: { forever: false, count: 2, unit: "weeks" }, fine: { failed_capture: { forever: true }, failed_build: { forever: true } } };
    const fine = changeRetentionMode(group, "fine", true);
    expect(fine.fine.failed_capture).toEqual(group.policy);
    expect(fine.fine_initialized).toBe(true);
    fine.fine.failed_build = { forever: false, count: 1, unit: "days" };
    const restored = changeRetentionMode(changeRetentionMode(fine, "group"), "fine");
    expect(restored.fine.failed_build.count).toBe(1);
    expect(restored.policy.count).toBe(2);
    expect(group.fine.failed_build.forever).toBe(true);
  });
  it("sends the revision precondition with a saved policy", async () => {
    const settings = { version: 2, revision: 7 } as RetentionSettings;
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify(settings), { status: 200 }));
    vi.stubGlobal("fetch", fetch);
    try {
      await new OperatorClient("/operator").putRetention(settings);
      expect(fetch.mock.calls[0][0]).toBe("/operator/storage/retention");
      expect(fetch.mock.calls[0][1].headers["If-Match"]).toBe('"7"');
    } finally { vi.unstubAllGlobals(); }
  });
});
