import { describe, expect, it } from "vitest";
import { buildSetupNotice, readRecordingsAccess, readSetupHealth, recordingAudience } from "./setupHealth";

const health = { ok: true, state: "provisioned", mode: "direct_shares", cause: "", features: null };

describe("direct share setup status", () => {
  it("shows the participant audience only for a known direct-share response", () => {
    expect(recordingAudience(health)).toBe("participants");
    expect(recordingAudience(null)).toBe("");
    expect(recordingAudience({ ...health, mode: "" })).toBe("");
  });
  it("keeps the user-facing verdict separate from admin details", () => {
    expect(readSetupHealth(health)).toEqual(health);
    expect(readRecordingsAccess({ recordings_access: { ok: false, state: "unavailable", step: "owner_account", detail: "private detail" } })?.detail).toBe("private detail");
    const notice = buildSetupNotice({ health: { ...health, ok: false, state: "unavailable" }, access: null, isAdmin: false, appUrl: "https://example.test/app" });
    expect(notice?.detail).toBe("");
    expect(notice?.shareUrl).toBe("https://example.test/app");
  });
  it("keeps existing reads visible while setup is unverified", () => {
    const notice = buildSetupNotice({ health: { ...health, ok: false, state: "unknown" }, access: null, isAdmin: true, appUrl: "" });
    expect(notice?.blocking).toBe(false);
    expect(notice?.tone).toBe("neutral");
  });
});
