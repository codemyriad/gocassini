import { mount, unmount } from "svelte";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import Operator from "./Operator.svelte";
import "./app.css";

// A run's detail says how long processing took against the meeting, so an
// admin can tell whether the hardware keeps up (D-817).

const at = (minutes: number) => new Date(Date.UTC(2026, 0, 1, 10, 0) + minutes * 60_000).toISOString();
// Recorded for 58 minutes, built in 10, sealed and published in a minute each.
const times = {
  build_started_at: at(60), build_finished_at: at(70),
  seal_started_at: at(70), seal_finished_at: at(71),
  publish_started_at: at(71), publish_finished_at: at(72),
};
const job = (id: string) => ({
  id, source_expired: false, provider: "nextcloud-talk", request_json: "{}",
  stage: "done", state: "succeeded", current_attempt_number: 2, rerun_count: 1,
  artifact_run_path: `/current/${id}.run`, room_name: id,
  created_at: at(0), updated_at: at(90), completed_at: at(90),
  record_started_at: at(0), record_finished_at: at(58),
  ...times,
});
const attempt = (n: number, kind: string, stamps: Record<string, string>) => ({
  job_id: "Standup", attempt_number: n, trigger_kind: kind, request_json: "{}",
  stage: "done", state: "succeeded", created_at: at(0), updated_at: at(90), build_deferral_count: 0,
  ...stamps,
});

// For review only: git-ignored, like the other UI suites' screenshots.
const SHOTS = "../node_modules/.cache/vitest-screenshots/operator-processing";

let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;
let originalUrl: string;

function serve(id: string, attempts: ReturnType<typeof attempt>[]) {
  const recording = job(id);
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const path = decodeURIComponent(new URL(String(input), location.href).pathname);
    if (path === "/operator/jobs") return Response.json([recording]);
    if (path === `/operator/jobs/${id}`) {
      return Response.json({
        job: recording,
        attempts: attempts.map((a) => ({ ...a, job_id: id })),
        availability: { source: "present", output: "present", published_attempt: attempts.length },
      });
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

it("says how long the meeting took to process, past a summary written later", async () => {
  serve("Standup", [
    attempt(2, "summarize", { build_started_at: at(88), build_finished_at: at(89), seal_started_at: at(89), seal_finished_at: at(89.5), publish_started_at: at(89.5), publish_finished_at: at(90) }),
    attempt(1, "initial", { record_started_at: at(0), record_finished_at: at(58), ...times }),
  ]);
  await open("Standup");
  await expect.element(page.getByText("12m · 4.8× real time", { exact: true })).toBeVisible();
  await page.screenshot({ element: document.querySelector(".op-detail-pane")!, path: `${SHOTS}/01-run.png` });

  await userEvent.click(page.getByText("Attempt 1", { exact: true }));
  await expect.element(page.getByText("10m · 5.8×", { exact: true })).toBeVisible();
  await page.screenshot({ element: document.querySelector(".op-detail-pane")!, path: `${SHOTS}/02-steps.png` });
});

it("says nothing about processing before the meeting is published", async () => {
  serve("Review", [attempt(1, "initial", { record_started_at: at(0), record_finished_at: at(58), build_started_at: at(60) })]);
  await open("Review");
  await expect.element(page.getByRole("heading", { name: "Attempt history" })).toBeVisible();
  expect(document.body.textContent).not.toContain("real time");
});
