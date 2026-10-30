import { mount, unmount } from "svelte";
import { expect, it } from "vitest";
import { page } from "vitest/browser";
import App from "./App.svelte";
import type { DataProvider, MeetingSearchOutcome } from "./viewer/dataProvider";
import "./app.css";

it("distinguishes pending, answered and failed searches and ignores superseded answers", async () => {
  history.replaceState(null, "", location.pathname + location.search);
  const previousBase = window.__CASSINI_VIEWER_BASE__;
  window.__CASSINI_VIEWER_BASE__ = location.origin + "/";
  const requests: { query: string; signal?: AbortSignal; resolve: (value: MeetingSearchOutcome) => void }[] = [];
  const unused = async (): Promise<never> => { throw new Error("Unexpected artifact load"); };
  const provider: DataProvider = {
    loadCatalog: async () => ({ version: "cassini.viewer.catalog.v1", meetings: [
      { id: "m1", title: "Weekly meeting", dateLabel: "2026-09-28" },
      { id: "m2", title: "Other meeting", dateLabel: "2026-09-29" },
    ] }),
    loadMeetingForEntry: unused, loadBundledArtifact: unused, switchTranscript: unused,
    loadMeetingSummary: async () => null,
    // Deliberately allow answers after abort, as a provider is allowed to do.
    searchMeetings: (query, options) => new Promise(resolve => requests.push({ query, signal: options?.signal, resolve })),
  };
  const answer: MeetingSearchOutcome = { status: "ok", coverage: { visible: 2, searched: 2 }, widened: false,
    hits: [{ meetingId: "m1", title: "Weekly meeting", dateLabel: "2026-09-28", roomId: "", roomName: "",
      segmentId: "s1", startMs: 0, endMs: 1000, speakerId: "s1", matched: "exact", snippet: "Discuss cassini today" }] };
  const host = document.createElement("div"); document.body.append(host);
  const app = mount(App, { target: host, props: { dataProvider: provider } });
  const list = () => host.querySelector('[aria-label="Meeting list"]')!.textContent!;
  const search = page.getByRole("searchbox");
  try {
    await expect.poll(list).toContain("2 meetings");
    await search.fill("cassini");
    await expect.poll(list).toContain("Searching meetings…");
    expect(list()).not.toContain("Nothing matches");
    expect(list()).not.toContain("0 of 2 meetings");
    await expect.poll(() => requests.length).toBe(1);
    requests[0].resolve(answer);
    await expect.poll(list).toContain("1 of 2 meetings");
    expect(list()).toContain("Discuss cassini today");

    await search.fill("no-match");
    await expect.poll(list).toContain("Searching meetings…");
    expect(list()).not.toContain("Discuss cassini today");
    expect(list()).not.toContain("Weekly meeting");
    expect(list()).not.toContain("1 of 2 meetings");
    await expect.poll(() => requests.length).toBe(2);
    requests[1].resolve({ ...answer, hits: [] });
    await expect.poll(list).toContain("Nothing matches");

    await search.fill("older");
    await expect.poll(() => requests.length).toBe(3);
    await search.fill("newer");
    expect(requests[2].signal?.aborted).toBe(true);
    requests[2].resolve(answer);
    await new Promise(resolve => setTimeout(resolve, 30));
    expect(list()).not.toContain("Discuss cassini today");
    expect(list()).toContain("Finding matching meetings…");
    await expect.poll(() => requests.length).toBe(4);
    requests[3].resolve({ status: "failed", message: "Search unavailable" });
    await expect.poll(list).toContain("Search could not finish");
    expect(list()).not.toContain("Nothing matches");

    await search.fill("pending");
    await expect.poll(() => requests.length).toBe(5);
    await page.getByRole("button", { name: "Clear search", exact: true }).first().click();
    requests[4].resolve(answer);
    await expect.poll(list).toContain("2 meetings");
    expect(list()).not.toContain("Searching…");
    expect(list()).not.toContain("Discuss cassini today");
  } finally {
    await unmount(app); host.remove();
    window.__CASSINI_VIEWER_BASE__ = previousBase;
  }
});
