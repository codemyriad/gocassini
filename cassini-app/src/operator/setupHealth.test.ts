import { describe, expect, it } from "vitest";
import { buildSetupNotice, readRecordingsAccess, readSetupHealth, readSharing, recordingAudience } from "./setupHealth";

const health = { ok: true, state: "provisioned", mode: "direct_shares", cause: "", features: null, sharing: null };

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
    expect(notice?.tone).toBe("neutral");
    // Browsing, not a promise that every read succeeds (D-849).
    expect(notice?.summary).toMatch(/^You can still browse recordings, but new ones will fail/);
  });
  it("says browsing still works when the check fails, since only publishing stops (D-849)", () => {
    const notice = buildSetupNotice({ health: { ...health, ok: false, state: "unavailable" }, access: null, isAdmin: false, appUrl: "" });
    expect(notice?.tone).toBe("warning");
    expect(notice?.title).toBe("Cassini can't save recordings right now");
    expect(notice?.summary).toMatch(/^You can still browse recordings\./);
    expect(notice).not.toHaveProperty("blocking");
  });
});

// The caller's own recording shares, as /setup reports them (D-885). The app
// shows nothing unless the operator established something, so the parse has
// to tell "nobody said" (null) from "checked, nothing waiting" from "could not
// check" — and never invent a count.
describe("the caller's recording shares on /setup", () => {
  const pending = {
    share_ids: ["34", "35"], job_id: "01K9QV7X3B2MA8ZC4D6N0RT5WE",
    title: "Weekly sync", started_at: "2026-10-30T10:00:00Z", duration_seconds: 2520,
  };

  it("reads a checked answer with its pending rows and counts", () => {
    expect(readSharing({ checked: true, pending: [pending], refused: 2, refused_started: 1 })).toEqual({
      checked: true,
      pending: [{ shareIds: ["34", "35"], jobId: "01K9QV7X3B2MA8ZC4D6N0RT5WE", title: "Weekly sync", startedAt: "2026-10-30T10:00:00Z", durationSeconds: 2520 }],
      refused: 2,
      refusedStarted: 1,
    });
    expect(readSetupHealth({ ...health, sharing: { checked: true, pending: [], refused: 0, refused_started: 0 } })?.sharing)
      .toEqual({ checked: true, pending: [], refused: 0, refusedStarted: 0 });
  });

  it("is null for an operator that does not report it, or reports it malformed", () => {
    expect(readSetupHealth(health)?.sharing).toBeNull();
    expect(readSharing(undefined)).toBeNull();
    expect(readSharing("checked")).toBeNull();
    expect(readSharing({ pending: [pending] })).toBeNull();
    expect(readSharing({ checked: "true" })).toBeNull();
  });

  it("keeps every count at zero when the operator could not check", () => {
    // Whatever else the object carries: the operator says it established
    // nothing, and a row shown from an unchecked answer would be a guess.
    expect(readSharing({ checked: false, pending: [pending], refused: 2, refused_started: 1 }))
      .toEqual({ checked: false, pending: [], refused: 0, refusedStarted: 0 });
  });

  it("drops a pending row it could not accept, and tolerates an unknown job", () => {
    const read = readSharing({ checked: true, pending: [
      { share_ids: [], job_id: "x" },
      { share_ids: [36] },
      "35",
      // The operator no longer knows the job: the row still appears, with no
      // title, no date and no duration, and the app shows what it has.
      { share_ids: ["37"] },
      { share_ids: ["38", ""], title: 7, started_at: null, duration_seconds: "long" },
    ], refused: -3, refused_started: 1.9 });
    expect(read?.pending).toEqual([
      { shareIds: ["37"], jobId: "", title: "", startedAt: "", durationSeconds: 0 },
      { shareIds: ["38"], jobId: "", title: "", startedAt: "", durationSeconds: 0 },
    ]);
    expect(read?.refused).toBe(0);
    expect(read?.refusedStarted).toBe(1);
  });
});
