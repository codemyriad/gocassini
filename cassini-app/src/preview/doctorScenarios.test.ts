import { afterEach, expect, it, vi } from "vitest";
import { createPreviewClient, doctorScenarios, scenarioReport } from "./doctorScenarios";

afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); });

it("keeps optional search problems separate from recording failures", () => {
  for (const id of ["search-partial", "search-running", "search-failed"]) {
    expect(scenarioReport(id).recording_state).toBe("passed");
    expect(scenarioReport(id).state).toBe("warn");
  }
});

// The gallery must not model rows the panel does not show, or a reviewer signs
// off on a checklist that does not exist. ffmpeg, ffprobe, free space and
// optional transcription were removed in the 2026-09-25 review. `test` is NOT
// among them: the row is back, now that the tool behind it can be reached
// without configuring a room.
it("models no row the checklist no longer has", () => {
  const removed = ["processing", "host.ffmpeg", "host.ffprobe", "host.workdir.space", "host.tmpdir.space"];
  for (const scenario of doctorScenarios) {
    for (const check of scenarioReport(scenario.id).checks) {
      expect(removed).not.toContain(check.id);
    }
  }
});

// Every preview must offer the test where a test could work, and nowhere else:
// the gallery is where the gating is reviewed without a Talk install.
it("offers the test recording only where its prerequisites hold", () => {
  for (const scenario of doctorScenarios) {
    const report = scenarioReport(scenario.id);
    const row = report.checks.find(check => check.id === "test");
    if (!row) continue;
    const blockers = report.checks.filter(check =>
      ["storage", "talk.hpb", "talk.discovery", "talk.handoff"].includes(check.id) &&
      check.state === "needs_action");
    if (blockers.length > 0 && row.action) {
      throw new Error(`${scenario.id} invites a test recording while ${blockers[0].id} needs attention`);
    }
  }
});

// A designer reading row order in the gallery must be reading the product's
// order. Every fixture had drifted from it before this was pinned.
it("renders every scenario in the order the operator sends rows", () => {
  const order = ["configuration", "host", "host.workdir", "host.tmpdir.writable", "storage",
    "talk.hpb", "talk.discovery", "talk.handoff", "test", "archive.search"];
  for (const scenario of doctorScenarios) {
    const ranks = scenarioReport(scenario.id).checks.map(check => {
      const at = order.indexOf(check.id);
      if (at < 0) throw new Error(`${scenario.id} has unplaced row ${check.id}`);
      return at;
    });
    expect(ranks, `${scenario.id} is out of order`).toEqual([...ranks].sort((a, b) => a - b));
  }
});

// The row that decides whether recording can work at all is on every report the
// operator sends, including ones where nothing has been established. Two
// fixtures used to delete it, which is the bug it was deleted around.
it("shows the backend row in every scenario", () => {
  for (const scenario of doctorScenarios) {
    const rows = scenarioReport(scenario.id).checks.filter(check => check.id === "talk.hpb");
    expect(rows.length, `${scenario.id} has ${rows.length} backend rows`).toBe(1);
  }
});

// A designer judging the row's affordances has to see the buttons the product
// renders. The fixtures set no `checkable` and no `probe`, so every row was
// missing its own Check button and no two rows shared a spinner.
it("marks the rows the product marks as checkable", () => {
  for (const scenario of doctorScenarios) {
    for (const check of scenarioReport(scenario.id).checks) {
      const probed = ["storage", "talk.hpb", "talk.discovery", "archive.search"].includes(check.id)
        || check.id.startsWith("host.");
      if (check.code === "check_blocked") {
        expect(check.checkable, `${scenario.id}/${check.id} offers a check it cannot run`).toBeFalsy();
        continue;
      }
      expect(!!check.checkable, `${scenario.id}/${check.id} checkable`).toBe(probed);
      expect(!!check.probe, `${scenario.id}/${check.id} probe`).toBe(probed);
    }
  }
});

it("simulates a repair without network calls, duplicate actions or shared state", async () => {
  vi.useFakeTimers();
  const fetch = vi.fn(() => { throw new Error("A preview must never access the network"); });
  vi.stubGlobal("fetch", fetch);
  const client = createPreviewClient("search-partial");
  const repair = await client.repairReadiness("backfill_search");
  expect(repair.checks.find(row => row.id === "archive.search")?.repair).toBeUndefined();
  expect(repair.checks.find(row => row.id === "archive.search")?.message).toContain("running now");
  await vi.advanceTimersByTimeAsync(4500);
  const finished = await client.getReadiness();
  expect(finished.checks.find(row => row.id === "archive.search")?.message).toContain("The last re-index added 3");
  expect(finished.state).toBe("passed");
  expect((await createPreviewClient("search-partial").getReadiness()).state).toBe("warn");
  expect(fetch).not.toHaveBeenCalled();
});

it("discards entered secrets and URLs while keeping edits within one preview", async () => {
  vi.useFakeTimers();
  const client = createPreviewClient("missing-secret");
  const promise = client.updateRecordingSetup({ internal_secret: "example-input", test_room_url: "https://real.example/call/private" });
  await vi.advanceTimersByTimeAsync(300);
  const saved = await promise;
  expect(saved.secret_configured).toBe(true);
  expect(JSON.stringify(saved)).not.toContain("example-input");
  expect(JSON.stringify(saved)).not.toContain("real.example");
  expect((await createPreviewClient("missing-secret").getReadiness()).secret_configured).toBe(false);
});

it("keeps a failed refresh visible across polling", async () => {
  vi.useFakeTimers();
  const client = createPreviewClient("refresh-error");
  expect((await client.getReadiness()).state).toBe("passed");
  const pending = expect(client.checkReadiness()).rejects.toMatchObject({ status: 503 });
  await vi.advanceTimersByTimeAsync(700);
  await pending;
  await expect(client.getReadiness()).rejects.toMatchObject({ status: 503 });
});

it("has valid independent fixtures for every listed URL and rejects unknown ones", () => {
  expect(new Set(doctorScenarios.map(item => item.id)).size).toBe(doctorScenarios.length);
  for (const item of doctorScenarios) {
    const report = scenarioReport(item.id);
    expect(report.checks.length).toBeGreaterThan(0);
    expect(report.test).toBeDefined();
  }
  expect(() => scenarioReport("invalid")).toThrow("Unknown Doctor preview");
});
