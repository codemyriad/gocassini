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
  transcription_status: "completed", transcription_model: "parakeet-tdt-0.6b-v3", transcription_revision: "4a1c2e9", transcription_device: "cuda",
  summary_status: "completed", summary_model: "gpt-4.1-mini",
  ...paths, ...stamps,
};
const buildLog = "loading model parakeet-tdt-0.6b-v3\ntranscribing 2 tracks\nAlex: 812 words\nChris: 640 words\ndone\n";

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
    if (path === "/operator/jobs/01KSTANDUPRUN/attempts/1/logs/build") {
      return new Response(buildLog, { headers: { "Content-Type": "text/plain; charset=utf-8" } });
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
  await expect.element(page.getByText("12m · 4.8× real time", { exact: true }).first()).toBeVisible();
  await expect.element(page.getByText("Audio only", { exact: true })).toBeVisible();
  await expect.element(page.getByText("Room empty", { exact: true }).first()).toBeVisible();
  await expect.element(page.getByText("parakeet-tdt-0.6b-v3 on GPU", { exact: true }).first()).toBeVisible();
  await expect.element(page.getByText("gpt-4.1-mini", { exact: true }).first()).toBeVisible();
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
  // Logs are read in place; one removed by retention cannot be opened.
  await expect.element(page.getByRole("button", { name: "Record log", exact: true })).toBeDisabled();
  await userEvent.click(page.getByRole("button", { name: "Build log", exact: true }));
  await expect.element(page.getByRole("region", { name: "Build log" })).toHaveTextContent("Chris: 640 words");
  await expect.element(page.getByRole("link", { name: "Download" })).toHaveAttribute("href", "/operator/jobs/01KSTANDUPRUN/attempts/1/logs/build?download=1");
  await page.screenshot({ element: document.querySelector(".op-detail-pane")!, path: `${SHOTS}/02-log.png` });
  await userEvent.click(page.getByRole("button", { name: "Close the log" }));
  await expect.element(page.getByRole("region", { name: "Build log" })).not.toBeInTheDocument();

  for (const disclosure of page.getByText("Technical details", { exact: true }).elements()) {
    await userEvent.click(disclosure);
  }
  await expect.element(page.getByText(job.id, { exact: true })).toBeVisible();
  await expect.element(page.getByText(paths.artifact_opus_path, { exact: true })).toBeVisible();
  // The logs' paths are not repeated now that they can be read.
  expect(document.body.textContent).not.toContain(attempt.build_log_path);
  await page.screenshot({ element: document.querySelector(".op-detail-pane")!, path: `${SHOTS}/03-unfolded.png` });
});

const filesGoneNote = "This server no longer keeps a copy of the meeting, so a summary can't be written for this run.";
const filesGoneWarning = "Source recording is missing; this job can no longer be rerun.";

function serveWithoutCopy(availability: Record<string, unknown>) {
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const path = decodeURIComponent(new URL(String(input), location.href).pathname);
    if (path === "/operator/jobs") return Response.json([job]);
    if (path === "/operator/jobs/01KSTANDUPRUN") return Response.json({ job, attempts: [attempt], availability });
    if (path === "/operator/jobs/01KSTANDUPRUN/summary") {
      return Response.json({ available: false, reason: "no_local_copy", has_summary: false, calls: 0, words: 0, estimated_input_tokens: 0, external: false });
    }
    return Response.json({});
  }));
}

it("says once that a run's files are gone", async () => {
  serveWithoutCopy({ source: "missing", output: "missing", published_attempt: 1, rerun_blocked_reason: filesGoneWarning });
  await openRun();
  await expect.element(page.getByText(filesGoneWarning, { exact: true })).toBeVisible();
  expect(document.body.textContent).not.toContain(filesGoneNote);
});

it("says why a summary can't be written when only the meeting's copy is gone", async () => {
  // The recording is still there, so nothing else explains the missing action.
  serveWithoutCopy({ source: "present", output: "missing", published_attempt: 1 });
  await openRun();
  await expect.element(page.getByText(filesGoneNote, { exact: true })).toBeVisible();
});

it("keeps every field in its place when a run has little to show", async () => {
  const bare = {
    ...job, state: "running", stage: "build", stop_reason: null, completed_at: null, request_json: "{}",
    build_finished_at: null, seal_started_at: null, seal_finished_at: null, publish_started_at: null, publish_finished_at: null,
    artifact_meeting_path: null, artifact_opus_path: null,
  };
  const building = {
    ...attempt, ...bare, transcription_status: undefined, summary_status: undefined, transcription_model: "", transcription_device: "", summary_model: "",
    build_log_path: null, files_present: {},
  };
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const path = decodeURIComponent(new URL(String(input), location.href).pathname);
    if (path === "/operator/jobs") return Response.json([bare]);
    if (path === "/operator/jobs/01KSTANDUPRUN") {
      return Response.json({ job: bare, attempts: [building], availability: { source: "present", output: "missing", published_attempt: null } });
    }
    return Response.json({});
  }));
  await openRun();

  const pane = document.querySelector(".op-detail-pane")!;
  const labels = (root: Element) => [...root.querySelectorAll(":scope > div > dt")].map((dt) => dt.textContent?.trim());
  const [run] = pane.querySelectorAll("section dl");
  expect(labels(run)).toEqual([
    "Recorded", "Processing", "Completed", "Speech model", "Summary model", "Capture", "Stopped because", "Conversation", "Recording",
  ]);
  for (const missing of ["Processing", "Completed", "Speech model", "Summary model", "Stopped because", "Conversation"]) {
    const cell = [...run.querySelectorAll("dt")].find((dt) => dt.textContent?.trim() === missing)!.nextElementSibling!;
    expect(cell.textContent?.trim(), missing).toBe("Not available");
  }

  await userEvent.click(page.getByText("Attempt 1", { exact: true }));
  const attemptFields = pane.querySelector("details[open] > div > dl")!;
  expect(labels(attemptFields)).toEqual(["Queued", "Processing", "Completed", "Reason", "Speech model", "Summary model"]);
  // All four logs are offered; the ones not written cannot be opened.
  for (const name of ["Record log", "Build log", "Seal log", "Publish log"]) {
    await expect.element(page.getByRole("button", { name, exact: true })).toBeVisible();
  }
  await expect.element(page.getByRole("button", { name: "Build log", exact: true })).toBeDisabled();
  await page.screenshot({ element: pane, path: `${SHOTS}/04-sparse.png` });
});

it("tells a summary-only attempt apart by what it did", async () => {
  const summarize = {
    ...attempt, attempt_number: 2, trigger_kind: "summarize", stop_reason: null,
    record_queued_at: null, record_started_at: null, record_finished_at: null, record_log_path: null,
    build_queued_at: at(88), build_started_at: at(88), build_finished_at: at(89),
    seal_started_at: at(89), seal_finished_at: at(89.5), publish_started_at: at(89.5), publish_finished_at: at(90),
    // Copied from the meeting it reused, not a model it ran.
    transcription_model: "parakeet-tdt-0.6b-v3", transcription_device: "cuda", summary_model: "gpt-4.1-mini",
  };
  const skipped = { ...attempt, summary_status: "skipped", summary_reason: "disabled", summary_model: "" };
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const path = decodeURIComponent(new URL(String(input), location.href).pathname);
    if (path === "/operator/jobs") return Response.json([job]);
    if (path === "/operator/jobs/01KSTANDUPRUN") {
      return Response.json({ job, attempts: [summarize, skipped], availability: { source: "present", output: "present", published_attempt: 2 } });
    }
    return Response.json({});
  }));
  await openRun();
  await userEvent.click(page.getByText("Attempt 2", { exact: true }));
  await expect.element(page.getByText("Summary requested", { exact: true })).toBeVisible();
  await expect.element(page.getByText("Reused from attempt 1", { exact: true })).toBeVisible();
  await expect.element(page.getByRole("button", { name: "Record log", exact: true }).first())
    .toHaveAttribute("title", "Not part of this attempt: only the first one records");

  await userEvent.click(page.getByText("Attempt 1", { exact: true }));
  await expect.element(page.getByText("Recording ended · Room empty", { exact: true })).toBeVisible();
  await expect.element(page.getByText("Skipped: summaries are turned off", { exact: true })).toBeVisible();
  await page.screenshot({ element: document.querySelector(".op-detail-pane")!, path: `${SHOTS}/05-kinds.png` });
});

it("says a muted call failed to capture audio, and names the real reason", async () => {
  // A run whose participants were all muted fails with exit code 3. It has
  // nothing to publish and nothing to rerun, and the detail pane must name the
  // reason — not show "Failed · Process exited with error".
  const emptyJob = {
    ...job, id: "01KEMPTYRUN", state: "failed", stop_reason: "no_audio_captured",
    record_exit_code: 3, error: "no audio captured: every participant was muted or silent",
    artifact_run_path: "/var/lib/cassini-operator/jobs/current/01KEMPTYRUN.run",
    artifact_meeting_path: null, artifact_opus_path: null, artifact_opus_sha256: null, artifact_site_path: null,
    build_queued_at: null, build_started_at: null, build_finished_at: null,
    seal_queued_at: null, seal_started_at: null, seal_finished_at: null,
    publish_queued_at: null, publish_started_at: null, publish_finished_at: null,
    completed_at: at(2),
  };
  const emptyAttempt = {
    ...attempt, job_id: "01KEMPTYRUN", state: "failed", stage: "done", stop_reason: "no_audio_captured",
    record_exit_code: 3, error: "no audio captured: every participant was muted or silent",
    artifact_run_path: emptyJob.artifact_run_path, artifact_meeting_path: null, artifact_opus_path: null, artifact_opus_sha256: null, artifact_site_path: null,
    build_queued_at: null, build_started_at: null, build_finished_at: null,
    seal_queued_at: null, seal_started_at: null, seal_finished_at: null,
    publish_queued_at: null, publish_started_at: null, publish_finished_at: null,
    transcription_status: undefined, transcription_model: "", transcription_device: "", summary_status: undefined, summary_model: "",
    build_log_path: null, files_present: {},
  };
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const path = decodeURIComponent(new URL(String(input), location.href).pathname);
    if (path === "/operator/jobs") return Response.json([emptyJob]);
    if (path === "/operator/jobs/01KEMPTYRUN") {
      return Response.json({
        job: emptyJob, attempts: [emptyAttempt],
        availability: { source: "empty", output: "missing", published_attempt: 0, rerun_blocked_reason: "No audio was captured; this run has nothing to rerun." },
      });
    }
    return Response.json({});
  }));
  await openRun();
  await expect.element(page.getByText("No audio was captured. Were all participants muted?", { exact: true }).first()).toBeVisible();
  await expect.element(page.getByText("This recording has no audio to transcribe or publish.", { exact: true }).first()).toBeVisible();
  await expect.element(page.getByText("No audio captured", { exact: true }).first()).toBeVisible();
  const pane = document.querySelector(".op-detail-pane")!;
  expect(pane.textContent).not.toContain("Failed");
  expect(pane.textContent).not.toContain("Process exited with error");
  // There is no recording, so rerun has nothing to offer.
  await expect.element(page.getByText("No audio was captured; this run has nothing to rerun.", { exact: true })).toBeVisible();
  await page.screenshot({ element: pane, path: `${SHOTS}/06-no-audio.png` });
});
