import { afterEach, describe, expect, it, vi } from "vitest";
import { OperatorClient } from "./client";
import { migrationActive, migrationButton, migrationCounting, migrationSummary, migrationVisible, normalizeRecordedAtMigration, notificationFacts } from "./recordedAtMigration";

afterEach(() => vi.unstubAllGlobals());

const status = (over: Record<string, unknown>) => normalizeRecordedAtMigration({
  applicable: true, checked: true, paused: false, needed: 0, queued: 0, settling: 0, in_progress: 0, done: 0, blocked: 0, workers: 2, pace_seconds: 30, ...over,
});

describe("normalizeRecordedAtMigration", () => {
  it("reads an operator without the block as not applicable", () => {
    const m = normalizeRecordedAtMigration(undefined);
    expect(m.state).toBe("not_applicable");
    expect(migrationVisible(m)).toBe(false);
  });

  it("keeps the operator's notification facts and sentences", () => {
    const m = status({ state: "needed", needed: 3, notifications: { known: true, account: "cassini", activity_app: true, notifications_app: false, versioning: true, details: ["one", "", 4, "two"] } });
    expect(m.notifications?.details).toEqual(["one", "two"]);
    expect(notificationFacts(m.notifications)).toEqual([
      { label: "File versioning", value: "Enabled" },
      { label: "Activity app", value: "Enabled" },
      { label: "Notifications app", value: "Not enabled" },
    ]);
  });

  it("tells web push from browser push", () => {
    const facts = (push: Record<string, boolean>) => notificationFacts(status({ state: "needed", needed: 1, notifications: { known: true, notifications_app: true, ...push, details: [] } }).notifications)
      .find(fact => fact.label === "Web push")?.value;
    expect(facts({ web_push: true, browser_push: false })).toBe("Allowed, not for browsers");
    expect(facts({ web_push: true, browser_push: true })).toBe("Allowed, browsers too");
    expect(facts({ web_push: false, browser_push: false })).toBe("Not allowed");
    expect(facts({})).toBeUndefined();
  });

  it("states no facts it could not read", () => {
    const m = status({ state: "needed", needed: 3, notifications: { known: false, error: "could not read", details: ["defaults"] } });
    expect(notificationFacts(m.notifications)).toEqual([]);
    expect(m.notifications?.activity_app).toBeUndefined();
  });
});

describe("what the panel offers", () => {
  it("shows nothing when nothing needs migrating, and a line while the first count runs", () => {
    for (const state of ["not_needed", "not_applicable"]) expect(migrationVisible(status({ state }))).toBe(false);
    for (const state of ["checking", "needed", "running", "pausing", "paused", "blocked", "done"]) expect(migrationVisible(status({ state }))).toBe(true);
    expect(migrationSummary(status({ state: "checking", checked: false }))).toContain("checking which past recordings");
  });

  it("keeps a start disabled until the first count finished", () => {
    expect(migrationButton(status({ state: "needed", needed: 3, checked: false }))?.disabled).toBe(true);
    expect(migrationButton(status({ state: "paused", queued: 3, checked: false }))?.disabled).toBe(true);
    expect(migrationButton(status({ state: "running", queued: 3, checked: false }))?.disabled).toBe(false);
    expect(migrationButton(status({ state: "needed", needed: 3 }))?.disabled).toBe(false);
  });

  it("offers one button per state", () => {
    expect(migrationButton(status({ state: "needed", needed: 37 }))).toEqual({ action: "start", label: "Write the recording time into 37 recordings", disabled: false });
    expect(migrationButton(status({ state: "running", queued: 3 }))).toEqual({ action: "pause", label: "Pause", disabled: false });
    expect(migrationButton(status({ state: "pausing", in_progress: 1 }))).toBeNull();
    expect(migrationButton(status({ state: "paused", queued: 3 }))).toEqual({ action: "start", label: "Resume", disabled: false });
    expect(migrationButton(status({ state: "blocked", blocked: 1 }))?.label).toBe("Retry 1 blocked recording");
    expect(migrationButton(status({ state: "done", done: 4 }))).toBeNull();
  });

  it("polls while files are being written, and more slowly while still counting", () => {
    expect(migrationActive(status({ state: "running" }))).toBe(true);
    expect(migrationActive(status({ state: "pausing" }))).toBe(true);
    expect(migrationActive(status({ state: "paused" }))).toBe(false);
    expect(migrationCounting(status({ state: "checking", checked: false }))).toBe(true);
    expect(migrationCounting(status({ state: "needed", needed: 2, checked: false }))).toBe(true);
    expect(migrationCounting(status({ state: "needed", needed: 2 }))).toBe(false);
    expect(migrationCounting(status({ state: "not_applicable", applicable: false }))).toBe(false);
  });

  it("says a count may still grow before the first count finished", () => {
    expect(migrationSummary(status({ state: "needed", needed: 1, checked: false }))).toContain("still counting");
    expect(migrationSummary(status({ state: "needed", needed: 1 }))).not.toContain("still counting");
  });

  it("says a pause lets the files in progress finish", () => {
    expect(migrationSummary(status({ state: "pausing", in_progress: 2, queued: 5 }))).toBe("Pausing. The 2 recordings already being written are finished first, or left as they were if their upload had not gone through, so no file is left half-written; 5 recordings then wait.");
    // An upload sent before the pause is still settled: pausing, not paused.
    expect(migrationSummary(status({ state: "pausing", settling: 1, queued: 5 }))).toContain("The recording already being written is finished first");
    expect(migrationSummary(status({ state: "running", queued: 5, in_progress: 2, done: 3 }))).toContain("7 recordings; 3 done");
  });
});

describe("the client", () => {
  it("reads the migration from /status, even when /status answers 503 for something else", async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ ok: false, recordings_access: {}, recorded_at_migration: { applicable: true, state: "needed", needed: 4 } }), { status: 503 }));
    vi.stubGlobal("fetch", fetchMock);
    const m = await new OperatorClient("https://operator.test/operator").getRecordedAtMigration();
    expect(m.state).toBe("needed");
    expect(m.needed).toBe(4);
    expect((fetchMock.mock.calls[0] as unknown as [string])[0]).toBe("https://operator.test/operator/status");
  });

  it("fails on a 503 that is not the operator's", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("<html>", { status: 503, statusText: "Service Unavailable" })));
    await expect(new OperatorClient("https://operator.test").getRecordedAtMigration()).rejects.toThrow("503");
  });

  it("starts and pauses with POSTs to the admin routes", async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ applicable: true, state: "running", queued: 4 }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new OperatorClient("https://operator.test");
    expect((await client.startRecordedAtMigration()).state).toBe("running");
    await client.pauseRecordedAtMigration();
    const calls = fetchMock.mock.calls as unknown as Array<[string, RequestInit]>;
    expect(calls.map(([url, init]) => `${init.method} ${url}`)).toEqual([
      "POST https://operator.test/migrations/recorded-at/start",
      "POST https://operator.test/migrations/recorded-at/pause",
    ]);
  });
});
