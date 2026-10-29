import { mount, unmount } from "svelte";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import Operator from "./Operator.svelte";
import "./app.css";

// A run's detail keeps what an administrator checks a run for in view and
// folds paths, ids and bookkeeping timestamps behind one control (D-816).

const at = (minutes: number) => new Date(Date.UTC(2026, 0, 1, 10, 0) + minutes * 60_000).toISOString();
const stamps = {
  record_queued_at: at(0), record_started_at: at(0), record_finished_at: at(58),
  build_started_at: at(60), build_finished_at: at(70),
  seal_started_at: at(70), seal_finished_at: at(71),
  publish_started_at: at(71), publish_finished_at: at(72),
  created_at: at(0), updated_at: at(90), completed_at: at(72),
};
const paths = {
  artifact_run_path: "/var/lib/cassini-operator/jobs/runs/Standup--attempt-001.run",
  artifact_meeting_path: "/var/lib/cassini-operator/jobs/runs/Standup--attempt-001.meeting",
  artifact_opus_path: "/var/lib/cassini-operator/jobs/runs/Standup--attempt-001.opus",
  artifact_opus_sha256: "eba6bd6674d35522dddbd774d3fc6a1fbcdab025aa30a5993d49216d0c13f59f",
  artifact_site_path: null,
};
const request = JSON.stringify({ url: "https://cloud.example.com/call/abc123", capture_mode: "audio-only" });
const job = {
  id: "01KSTANDUPRUN", source_expired: false, provider: "nextcloud-talk", request_json: request,
  stage: "done", state: "succeeded", current_attempt_number: 1, rerun_count: 0,
  stop_reason: "room_empty", room_name: "Standup", interrupted_at: null, ...paths, ...stamps,
};
const attempt = {
  job_id: "01KSTANDUPRUN", attempt_number: 1, trigger_kind: "initial", request_json: request,
  stage: "done", state: "succeeded", build_deferral_count: 0, stop_reason: "room_empty",
  record_log_path: "/var/lib/cassini-operator/jobs/runs/Standup--attempt-001.logs/record.log",
  build_log_path: "/var/lib/cassini-operator/jobs/runs/Standup--attempt-001.logs/build.log",
  files_present: { record_log: false, build_log: true },
  ...paths, ...stamps,
};

// For review only: git-ignored, like the other UI suites' screenshots.
const SHOTS = "../node_modules/.cache/vitest-screenshots/operator-detail";

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
    if (path === "/operator/jobs") return Response.json([job]);
    if (path === "/operator/jobs/01KSTANDUPRUN") {
      return Response.json({ job, attempts: [attempt], availability: { source: "present", output: "present", published_attempt: 1 } });
    }
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
});

async function openRun() {
  app = mount(Operator, { target: host });
  await expect.element(page.getByText("Standup", { exact: true })).toBeVisible();
  await userEvent.click(page.getByText("Standup", { exact: true }).element().closest(".run-card")!);
  await expect.element(page.getByRole("heading", { name: "Attempt history" })).toBeVisible();
}

it("keeps the outcome in view and folds the bookkeeping away", async () => {
  await openRun();
  await expect.element(page.getByText("12m · 4.8× real time", { exact: true })).toBeVisible();
  await expect.element(page.getByText("Audio only", { exact: true })).toBeVisible();
  await expect.element(page.getByText("Room empty", { exact: true }).first()).toBeVisible();
  await expect.element(page.getByRole("link", { name: "Open in Talk" })).toHaveAttribute("href", "https://cloud.example.com/call/abc123");

  const text = document.querySelector(".op-detail-pane")!.textContent ?? "";
  // Always Nextcloud Talk; the attempt history already counts attempts.
  for (const gone of ["Provider", "nextcloud-talk", "Reruns", "Current attempt", "sha256"]) {
    expect(text).not.toContain(gone);
  }
  await expect.element(page.getByText(job.id, { exact: true })).not.toBeVisible();
  // The attempt says how long it took to process, not how long it recorded.
  await expect.element(page.getByText("· processed in 12m", { exact: true })).toBeVisible();
  await page.screenshot({ element: document.querySelector(".op-detail-pane")!, path: `${SHOTS}/01-folded.png` });

  await userEvent.click(page.getByText("Attempt 1", { exact: true }));
  await expect.element(page.getByText(attempt.build_log_path)).not.toBeVisible();
  for (const disclosure of page.getByText("Technical details", { exact: true }).elements()) {
    await userEvent.click(disclosure);
  }
  await expect.element(page.getByText(job.id, { exact: true })).toBeVisible();
  await expect.element(page.getByText(attempt.build_log_path, { exact: true })).toBeVisible();
  await expect.element(page.getByText(`${attempt.record_log_path} (no longer on disk)`, { exact: true })).toBeVisible();
  await page.screenshot({ element: document.querySelector(".op-detail-pane")!, path: `${SHOTS}/02-unfolded.png` });
});
