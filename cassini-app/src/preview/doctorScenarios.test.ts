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
// optional transcription were removed in the 2026-09-25 review.
it("models no row the checklist no longer has", () => {
  const removed = ["processing", "test", "host.ffmpeg", "host.ffprobe", "host.workdir.space", "host.tmpdir.space"];
  for (const scenario of doctorScenarios) {
    for (const check of scenarioReport(scenario.id).checks) {
      expect(removed).not.toContain(check.id);
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
  expect(finished.checks.find(row => row.id === "archive.search")?.message).toContain("Last re-index: 3 indexed");
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
  const pending = expect(client.checkReadiness()).rejects.toThrow("could not refresh");
  await vi.advanceTimersByTimeAsync(700);
  await pending;
  await expect(client.getReadiness()).rejects.toThrow("could not refresh");
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
