import { mount, unmount } from "svelte";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import Operator from "./Operator.svelte";
import "./app.css";

// Writing or regenerating a run's summary from the Operator (D-814): nothing
// is sent before the administrator has seen what it costs and confirmed.

const SHOTS = "../node_modules/.cache/vitest-screenshots/operator-summary";
const stamp = "2026-01-01T10:00:00Z";
const job = (id: string) => ({
  id, source_expired: false, provider: "nextcloud-talk", request_json: "{}",
  stage: "done", state: "succeeded", current_attempt_number: 1, rerun_count: 0,
  artifact_run_path: `/current/${id}.run`, room_name: id,
  created_at: stamp, updated_at: stamp, completed_at: stamp,
  record_started_at: stamp, record_finished_at: stamp,
  build_started_at: stamp, build_finished_at: stamp,
  seal_started_at: stamp, seal_finished_at: stamp,
  publish_started_at: stamp, publish_finished_at: stamp,
});
const plan = (overrides: Record<string, unknown> = {}) => ({
  available: true, has_summary: false, calls: 1, words: 5213, estimated_input_tokens: 7480,
  provider: "Open Router", model: "deepseek/deepseek-v4.1-flash", external: true, ...overrides,
});

let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;
let originalUrl: string;
let posts: { path: string; body: unknown }[];
// Detail reads of the run since the last POST: the action has settled once
// the run was read again.
let rereads: number;

function serve(id: string, summaryPlan: Record<string, unknown>) {
  const recording = job(id);
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = decodeURIComponent(new URL(String(input), location.href).pathname);
    if (init?.method === "POST") {
      rereads = 0;
      posts.push({ path, body: JSON.parse(String(init.body ?? "{}")) });
      return Response.json({ id, attempt_number: 2 }, { status: 202 });
    }
    if (path === "/operator/jobs") return Response.json([recording]);
    if (path === `/operator/jobs/${id}/summary`) return Response.json(summaryPlan);
    if (path === `/operator/jobs/${id}`) {
      rereads += 1;
      return Response.json({ job: recording, attempts: [], availability: { source: "present", output: "present", published_attempt: 1 } });
    }
    return Response.json({});
  }));
}

async function open(id: string) {
  app = mount(Operator, { target: host });
  await expect.element(page.getByText(id, { exact: true })).toBeVisible();
  await userEvent.click(page.getByText(id, { exact: true }).element().closest(".run-card")!);
}

beforeEach(() => {
  originalUrl = location.href;
  history.replaceState(null, "", location.pathname + location.search);
  Object.assign(window, { __CASSINI_CONFIG__: { operatorBasePath: "/operator" } });
  vi.stubGlobal("EventSource", undefined);
  posts = [];
  rereads = 0;
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
});

it("writes a missing summary only after the cost is shown and confirmed", async () => {
  serve("Standup", plan());
  await open("Standup");
  await userEvent.click(page.getByRole("button", { name: "Write summary", exact: true }));
  const confirm = page.getByRole("alertdialog", { name: "Write a summary for this meeting?" });
  await expect.element(confirm).toBeVisible();
  await expect.element(confirm.getByText("1 call to deepseek/deepseek-v4.1-flash (Open Router) · about 7.5k input tokens, 5,213 words")).toBeVisible();
  await expect.element(confirm.getByText("The transcript is sent to an external service, which may bill for it.")).toBeVisible();
  expect(posts).toEqual([]);
  await page.screenshot({ element: document.querySelector(".op-detail-pane")!, path: `${SHOTS}/01-confirm-write.png` });

  await userEvent.click(confirm.getByRole("button", { name: "Write summary", exact: true }));
  await expect.poll(() => posts).toEqual([{ path: "/operator/jobs/Standup/summary", body: { replace: false } }]);
  // Settled: the confirmation closes and the run is read again.
  await expect.element(confirm).not.toBeInTheDocument();
  await expect.poll(() => rereads).toBeGreaterThan(0);
  await expect.element(page.getByRole("button", { name: "Write summary", exact: true })).toBeVisible();
});

it("asks before replacing a summary, and cancelling sends nothing", async () => {
  serve("Review", plan({ has_summary: true, external: false, provider: "Local", model: "" }));
  await open("Review");
  await userEvent.click(page.getByRole("button", { name: "Regenerate summary", exact: true }));
  const confirm = page.getByRole("alertdialog", { name: "Replace this meeting's summary?" });
  await expect.element(confirm.getByText("The model runs on your own network.")).toBeVisible();
  await expect.element(confirm.getByText(/instead of the current one/)).toBeVisible();
  await userEvent.click(confirm.getByRole("button", { name: "Cancel", exact: true }));
  await expect.element(confirm).not.toBeInTheDocument();
  expect(posts).toEqual([]);

  await userEvent.click(page.getByRole("button", { name: "Regenerate summary", exact: true }));
  await userEvent.click(page.getByRole("alertdialog").getByRole("button", { name: "Regenerate summary", exact: true }));
  await expect.poll(() => posts).toEqual([{ path: "/operator/jobs/Review/summary", body: { replace: true } }]);
  await expect.element(page.getByRole("alertdialog")).not.toBeInTheDocument();
  await expect.poll(() => rereads).toBeGreaterThan(0);
  await expect.element(page.getByRole("button", { name: "Regenerate summary", exact: true })).toBeVisible();
});

it("says why it cannot write one when an administrator can fix that", async () => {
  serve("Planning", plan({ available: false, reason: "summaries_off" }));
  await open("Planning");
  await expect.element(page.getByText("Summaries are off. Turn them on in Settings › Publish pipeline to write one.")).toBeVisible();
  await expect.element(page.getByRole("button", { name: "Write summary", exact: true })).not.toBeInTheDocument();
});
