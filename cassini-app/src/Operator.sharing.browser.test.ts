import { mount, unmount } from "svelte";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import Operator from "./Operator.svelte";
import "./app.css";

// A run whose meeting Nextcloud would not share with everyone says so, and
// who (D-885): in the run list as a flag, in the run detail with the list.

const stamp = "2026-11-01T10:00:00Z";
const job = (id: string, state: string, extra: Record<string, unknown> = {}) => ({
  id, source_expired: false, provider: "nextcloud-talk", request_json: "{}",
  stage: "done", state, current_attempt_number: 1, rerun_count: 0,
  artifact_run_path: `/current/${id}.run`, room_name: id,
  created_at: stamp, updated_at: stamp, completed_at: stamp,
  record_started_at: stamp, record_finished_at: stamp,
  build_started_at: stamp, build_finished_at: stamp,
  seal_started_at: stamp, seal_finished_at: stamp,
  publish_started_at: stamp, publish_finished_at: stamp,
  ...extra,
});

const SHOTS = "../node_modules/.cache/vitest-screenshots/operator-sharing";

let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;
let originalUrl: string;

function serve(recording: ReturnType<typeof job>, sharing: unknown) {
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const path = decodeURIComponent(new URL(String(input), location.href).pathname);
    if (path === "/operator/jobs") return Response.json([recording]);
    if (path === `/operator/jobs/${recording.id}`) {
      return Response.json({
        job: recording,
        attempts: [{ job_id: recording.id, attempt_number: 1, trigger_kind: "initial", request_json: "{}", stage: "done", state: recording.state, created_at: stamp, updated_at: stamp, build_deferral_count: 0 }],
        availability: { source: "present", output: "present", published_attempt: recording.state === "succeeded" ? 1 : 0 },
        sharing,
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

it("flags a partly shared run and lists who it was not shared with", async () => {
  serve(job("Standup", "succeeded", { unshared_count: 2 }), {
    status: "partial",
    skipped: [
      { type: "user", id: "bob", reason: "Sharing is only allowed with group members" },
      { type: "group", id: "marketing", reason: "Sharing is only allowed with group members" },
    ],
  });
  await open("Standup");
  // The flag on the run card, before anyone opens the run.
  const card = document.querySelector(".run-card")!;
  expect(card.textContent).toContain("Not shared with everyone");
  await expect.element(page.getByText("Published, but not shared with everyone: Nextcloud refused 2 recipients", { exact: true })).toBeVisible();
  await userEvent.click(page.getByText("Not shared with 2 recipients", { exact: true }));
  await expect.element(page.getByText("bob: Sharing is only allowed with group members", { exact: true })).toBeVisible();
  await expect.element(page.getByText("marketing (group): Sharing is only allowed with group members", { exact: true })).toBeVisible();
  await page.screenshot({ element: document.querySelector(".op-detail-pane")!, path: `${SHOTS}/01-partial.png` });
});

it("says a failed run was refused for the person who started it", async () => {
  serve(job("Retro", "failed", { error: "publish: the recording could not be shared with its starter, mallory: Nextcloud share API: HTTP 403, OCS 403: Sharing is only allowed with group members" }), {
    status: "starter_refused",
    skipped: [{ type: "user", id: "mallory", reason: "Sharing is only allowed with group members" }],
  });
  await open("Retro");
  await expect.element(page.getByText("Not published: Nextcloud refused to share it with the person who started the recording", { exact: true })).toBeVisible();
  await expect.element(page.getByText("mallory: Sharing is only allowed with group members", { exact: true })).toBeVisible();
  await page.screenshot({ element: document.querySelector(".op-detail-pane")!, path: `${SHOTS}/02-starter-refused.png` });
});

it("says nothing for a run shared with everyone", async () => {
  serve(job("Planning", "succeeded"), { status: "complete", skipped: [] });
  await open("Planning");
  await expect.element(page.getByRole("button", { name: "Rerun", exact: true })).toBeVisible();
  expect(document.body.textContent).not.toContain("not shared with everyone");
  expect(document.body.textContent).not.toContain("Not shared with everyone");
});
