import { mount, unmount } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import Operator from "./Operator.svelte";
import "./app.css";

const stamp = "2026-01-01T10:00:00Z";
const job = (id: string, expired: boolean) => ({
  id, source_expired: expired, provider: "nextcloud-talk", request_json: "{}",
  stage: "done", state: "succeeded", current_attempt_number: 1, rerun_count: 0,
  artifact_run_path: `/current/${id}.run`, room_name: id,
  created_at: stamp, updated_at: stamp, completed_at: stamp,
  record_started_at: stamp, record_finished_at: stamp,
  build_started_at: stamp, build_finished_at: stamp,
  seal_started_at: stamp, seal_finished_at: stamp,
  publish_started_at: stamp, publish_finished_at: stamp,
});
const jobs = [job("Expired recording", true), job("Available recording", false)];

let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;
let originalUrl: string;

beforeEach(() => {
  originalUrl = location.href;
  history.replaceState(null, "", location.pathname + location.search);
  Object.assign(window, { __CASSINI_CONFIG__: { operatorBasePath: "/operator" } });
  vi.stubGlobal("EventSource", undefined);
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const path = decodeURIComponent(new URL(String(input), location.href).pathname);
    if (path === "/operator/jobs") return Response.json(jobs);
    const selected = jobs.find((candidate) => path === `/operator/jobs/${candidate.id}`);
    if (selected) return Response.json({
      job: selected, attempts: [],
      availability: {
        source: selected.source_expired ? "expired" : "present", output: "present", published_attempt: 1,
        rerun_blocked_reason: selected.source_expired ? "Source recording is expired; this job can no longer be rerun." : "",
      },
    });
    return Response.json({});
  }));
  host = document.createElement("div");
  document.body.append(host);
});

afterEach(async () => {
  if (app) await unmount(app);
  app = undefined;
  host.remove();
  history.replaceState(null, "", originalUrl);
  delete (window as Window & { __CASSINI_CONFIG__?: unknown }).__CASSINI_CONFIG__;
  vi.unstubAllGlobals();
  await page.viewport(1500, 1000);
});

function card(label: string) {
  return page.getByText(label, { exact: true }).element().closest(".run-card")!;
}

function detail() {
  return document.querySelector(".op-detail-pane")!;
}

function stageBars() {
  return detail().querySelectorAll(".run-detail-card span.h-1\\.5");
}

describe("retention eviction in the operator browser UI", () => {
  it("marks an expired source, blocks its rerun, and keeps an available source rerunnable", async () => {
    app = mount(Operator, { target: host });
    await expect.element(page.getByText("Recording deleted", { exact: true })).toBeVisible();
    expect(card("Expired recording").textContent).toContain("Recording deleted");
    expect(card("Available recording").textContent).not.toContain("Recording deleted");

    await userEvent.click(card("Expired recording"));
    await expect.element(page.getByText("The source recording was deleted by the retention policy. This job can no longer be rerun.", { exact: true })).toBeVisible();
    await expect.element(page.getByRole("button", { name: "Rerun", exact: true })).toBeDisabled();
    await expect.element(page.getByText("Record (deleted)", { exact: true })).toBeVisible();
    expect(stageBars()[0].className).toMatch(/bg-base-content\/12/);
    expect(stageBars()[1].className).toMatch(/bg-success/);

    await userEvent.click(card("Available recording"));
    await expect.element(page.getByRole("button", { name: "Rerun", exact: true })).toBeEnabled();
    expect(stageBars()[0].className).toMatch(/bg-success/);

    await userEvent.click(card("Expired recording"));
    await page.viewport(390, 900);
    await expect.element(page.getByText("The source recording was deleted by the retention policy. This job can no longer be rerun.", { exact: true })).toBeVisible();
  });
});

it("blocks rerun while deletion failed and offers a separate cleanup retry", async () => {
  const cleanup = { status: "error", last_error: "permission denied" };
  const recording = { ...job("Transcript meeting", false), media_cleanup: cleanup };
  const writes: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = decodeURIComponent(new URL(String(input), location.href).pathname);
    if (init?.method === "POST") { writes.push(path); return Response.json({ status: "queued" }); }
    if (path === "/operator/jobs") return Response.json([recording]);
    if (path === "/operator/jobs/Transcript meeting") return Response.json({ job: recording, attempts: [], availability: { source: "present", output: "present", published_attempt: 1, media_cleanup: cleanup, rerun_blocked_reason: "This recording deletes source media after processing; processing cannot be rerun." } });
    return Response.json({});
  }));
  app = mount(Operator, { target: host });
  await expect.element(page.getByText("Transcript meeting", { exact: true })).toBeVisible();
  await userEvent.click(card("Transcript meeting"));
  await expect.element(page.getByRole("button", { name: "Rerun", exact: true })).toBeDisabled();
  await expect.element(page.getByText("permission denied", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Retry media deletion", exact: true }).click();
  expect(writes).toEqual(["/operator/jobs/Transcript meeting/cleanup"]);
  await expect.element(page.getByText("Retry requested. Cleanup is checked within 30 seconds.")).toBeVisible();
});
