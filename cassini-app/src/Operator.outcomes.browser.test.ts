import { mount, unmount } from "svelte";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import Operator from "./Operator.svelte";
import "./app.css";

// A run that published without a transcript or a summary says so in its
// detail, from the attempt it published (D-854, D-814).

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
const attempt = (outcomes: Record<string, string>) => ({
  job_id: "Standup", attempt_number: 1, trigger_kind: "initial", request_json: "{}",
  stage: "done", state: "succeeded", created_at: stamp, updated_at: stamp, build_deferral_count: 0,
  ...outcomes,
});

// For review only: git-ignored, like the other UI suites' screenshots.
const SHOTS = "../node_modules/.cache/vitest-screenshots/operator-outcomes";

let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;
let originalUrl: string;

function serve(id: string, outcomes: Record<string, string>) {
  const recording = job(id);
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const path = decodeURIComponent(new URL(String(input), location.href).pathname);
    if (path === "/operator/jobs") return Response.json([recording]);
    if (path === `/operator/jobs/${id}`) {
      return Response.json({
        job: recording,
        attempts: [{ ...attempt(outcomes), job_id: id }],
        availability: { source: "present", output: "present", published_attempt: 1 },
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

it("says a run published without a transcript, why, and that a rerun can transcribe it", async () => {
  serve("Standup", {
    transcription_status: "failed", transcription_reason: "transcription_failed",
    transcription_detail: "CUDA error: out of memory",
    summary_status: "skipped", summary_reason: "no_transcript",
  });
  await open("Standup");
  await expect.element(page.getByText("Published without a transcript: transcription failed", { exact: true })).toBeVisible();
  await expect.element(page.getByText("CUDA error: out of memory", { exact: true })).toBeVisible();
  await expect.element(page.getByText("Rerun transcribes it again.", { exact: true })).toBeVisible();
  // No transcript means nothing to summarize: one notice, not two.
  expect(document.body.textContent).not.toContain("No summary");
  await page.screenshot({ element: document.querySelector(".op-detail-pane")!, path: `${SHOTS}/01-no-transcript.png` });
});

it("says a transcribed run published without a summary", async () => {
  serve("Review", {
    transcription_status: "completed",
    summary_status: "failed", summary_reason: "llm_failed",
  });
  await open("Review");
  await expect.element(page.getByText("No summary: the summary could not be generated", { exact: true })).toBeVisible();
  expect(document.body.textContent).not.toContain("Published without a transcript");
});

it("says nothing more for a run with both", async () => {
  serve("Planning", { transcription_status: "completed", summary_status: "completed" });
  await open("Planning");
  await expect.element(page.getByRole("button", { name: "Rerun", exact: true })).toBeVisible();
  expect(document.body.textContent).not.toContain("Published without a transcript");
  expect(document.body.textContent).not.toContain("No summary");
});
