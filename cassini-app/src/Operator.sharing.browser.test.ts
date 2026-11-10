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
  await expect.element(page.getByText("Published. Not shared: 2 recipients", { exact: true })).toBeVisible();
  // Closed by default, the count in its summary.
  const disclosure = document.querySelector<HTMLDetailsElement>(".op-detail-pane details")!;
  expect(disclosure.open).toBe(false);
  await userEvent.click(page.getByText("Who was refused (2)", { exact: true }));
  const rows = [...disclosure.querySelectorAll("li")].map((row) => row.textContent?.replace(/\s+/g, " ").trim());
  expect(rows).toEqual([
    "bob person · Sharing is only allowed with group members",
    "marketing group · Sharing is only allowed with group members",
  ]);
  expect(disclosure.querySelector("li strong")?.textContent).toBe("bob");
  expect(document.querySelector(".op-detail-pane")?.textContent).toContain("Nextcloud refused them because of a sharing setting. Doctor › Recording sharing names it and the fix.");
  expect(document.querySelector(".op-detail-pane")?.textContent).toContain("Adds whoever is missing, including anyone removed by hand. People who accept shares by hand will find it waiting in Browse.");
  await page.screenshot({ element: document.querySelector(".op-detail-pane")!, path: `${SHOTS}/01-partial.png` });
});

it("counts the refused against the room when the operator says how big it was", async () => {
  serve(job("Standup", "succeeded", { unshared_count: 2 }), {
    status: "partial",
    audience_count: 8,
    skipped: [{ type: "user", id: "bob" }, { type: "circle", id: "design", reason: "Team sharing is off" }],
  });
  await open("Standup");
  await expect.element(page.getByText("Published, but not shared with 2 of 8 recipients", { exact: true })).toBeVisible();
  await userEvent.click(page.getByText("Who was refused (2)", { exact: true }));
  const rows = [...document.querySelectorAll(".op-detail-pane details li")].map((row) => row.textContent?.replace(/\s+/g, " ").trim());
  expect(rows).toEqual(["bob person", "design Team · Team sharing is off"]);
});

it("says a failed run was refused for the person who started it", async () => {
  serve(job("Retro", "failed", { error: "publish: the recording could not be shared with its starter, mallory: Nextcloud share API: HTTP 403, OCS 403: Sharing is only allowed with group members" }), {
    status: "starter_refused",
    skipped: [{ type: "user", id: "mallory", reason: "Sharing is only allowed with group members" }],
  });
  await open("Retro");
  await expect.element(page.getByText("Not published: Nextcloud refused to share it with mallory, who started the recording.", { exact: true })).toBeVisible();
  await expect.element(page.getByText("mallory: Sharing is only allowed with group members", { exact: true })).toBeVisible();
  expect(document.querySelector(".op-detail-pane")?.textContent).toContain("Fix the setting Doctor › Recording sharing names, then Rerun. Rerun publishes it and shares it with the whole room.");
  await page.screenshot({ element: document.querySelector(".op-detail-pane")!, path: `${SHOTS}/02-starter-refused.png` });
});

it("says nothing for a run shared with everyone", async () => {
  serve(job("Planning", "succeeded"), { status: "complete", skipped: [] });
  await open("Planning");
  await expect.element(page.getByRole("button", { name: "Rerun", exact: true })).toBeVisible();
  expect(document.body.textContent).not.toContain("not shared with everyone");
  expect(document.body.textContent).not.toContain("Not shared with everyone");
});

it("flags a run Nextcloud's share limit held back, and offers the re-share", async () => {
  serve(job("All hands", "succeeded", { unshared_count: 3 }), {
    status: "deferred",
    audience_count: 30,
    deferred: [{ type: "user", id: "dave" }, { type: "user", id: "erin" }, { type: "group", id: "marketing" }],
  });
  await open("All hands");
  await expect.element(page.getByText("Published. Held back by Nextcloud's share limit: 3 recipients. Re-share in 10 minutes, or raise the limit (Doctor › Recording sharing).", { exact: true })).toBeVisible();
  // The flag is how an administrator finds it.
  expect(document.querySelector(".run-card")!.textContent).toContain("Not shared with everyone");
  expect(document.querySelector(".op-detail-pane")!.textContent).not.toContain("refused");
  await expect.element(page.getByRole("button", { name: "Re-share with the room" })).toBeVisible();
  await page.screenshot({ element: document.querySelector(".op-detail-pane")!, path: `${SHOTS}/04-deferred.png` });
});

it("adds the held-back sentence under the refused notice when both happened", async () => {
  serve(job("All hands", "succeeded", { unshared_count: 1 }), {
    status: "partial",
    skipped: [{ type: "user", id: "bob", reason: "Sharing is only allowed with group members" }],
    deferred: [{ type: "user", id: "dave" }],
  });
  await open("All hands");
  await expect.element(page.getByText("Published. Not shared: 1 recipient", { exact: true })).toBeVisible();
  await expect.element(page.getByText("Nextcloud's share limit also held back 1.", { exact: true })).toBeVisible();
  await expect.element(page.getByText("Who was refused (1)", { exact: true })).toBeVisible();
  expect(document.querySelector(".run-card")!.textContent).toContain("Not shared with everyone");
});

it("answers a re-share the limit held back again with how many and when to try", async () => {
  const recording = job("Standup", "succeeded", { unshared_count: 1 });
  const partial = { status: "partial", skipped: [{ type: "user", id: "bob", reason: "Sharing is only allowed with group members" }] };
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = decodeURIComponent(new URL(String(input), location.href).pathname);
    if (init?.method === "POST") return Response.json({ sharing: { status: "deferred", skipped: [], deferred: [{ type: "user", id: "bob" }, { type: "user", id: "carol" }] } });
    if (path === "/operator/jobs") return Response.json([recording]);
    if (path === "/operator/jobs/Standup") {
      return Response.json({
        job: recording,
        attempts: [{ job_id: "Standup", attempt_number: 1, trigger_kind: "initial", request_json: "{}", stage: "done", state: "succeeded", created_at: stamp, updated_at: stamp, build_deferral_count: 0 }],
        availability: { source: "present", output: "present", published_attempt: 1 },
        sharing: partial,
      });
    }
    return Response.json({});
  }));
  await open("Standup");
  await userEvent.click(page.getByRole("button", { name: "Re-share with the room" }));
  await expect.element(page.getByText("Shared again. Still held back by Nextcloud's share limit: 2 recipients. Re-share in 10 minutes.", { exact: true })).toBeVisible();
});

function serveReshare(recording: ReturnType<typeof job>, answer: "fixed" | "fails") {
  let reshared = false;
  const posts: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = decodeURIComponent(new URL(String(input), location.href).pathname);
    if (init?.method === "POST") {
      posts.push(path);
      if (answer === "fails") return Response.json({ error: "Cassini could not share the meeting again: Nextcloud recording shares are not ready" }, { status: 502 });
      reshared = true;
      return Response.json({ sharing: { status: "complete", skipped: [] } });
    }
    const current = reshared ? { ...recording, unshared_count: 0 } : recording;
    if (path === "/operator/jobs") return Response.json([current]);
    if (path === `/operator/jobs/${recording.id}`) {
      return Response.json({
        job: current,
        attempts: [{ job_id: recording.id, attempt_number: 1, trigger_kind: "initial", request_json: "{}", stage: "done", state: "succeeded", created_at: stamp, updated_at: stamp, build_deferral_count: 0 }],
        availability: { source: "present", output: "present", published_attempt: 1 },
        sharing: reshared ? { status: "complete", skipped: [] } : { status: "partial", skipped: [{ type: "user", id: "bob", reason: "Sharing is only allowed with group members" }] },
      });
    }
    return Response.json({});
  }));
  return posts;
}

it("re-shares a partly shared meeting once the policy is fixed", async () => {
  const posts = serveReshare(job("Standup", "succeeded", { unshared_count: 1 }), "fixed");
  await open("Standup");
  await expect.element(page.getByText("Published. Not shared: 1 recipient", { exact: true })).toBeVisible();
  await userEvent.click(page.getByRole("button", { name: "Re-share with the room" }));
  await expect.poll(() => posts).toEqual(["/operator/jobs/Standup/reshare"]);
  // The notice goes with the next read of the run; the answer stays.
  await expect.element(page.getByText("Shared with everyone.", { exact: true })).toBeVisible();
  await expect.poll(() => document.body.textContent?.includes("Published, but not shared with")).toBe(false);
  await expect.poll(() => document.querySelector(".run-card")!.textContent).not.toContain("Not shared with everyone");
  await page.screenshot({ element: document.querySelector(".op-detail-pane")!, path: `${SHOTS}/03-reshared.png` });
});

it("drops a re-share's answer once the person leaves the run", async () => {
  const standup = job("Standup", "succeeded", { unshared_count: 1 });
  const planning = job("Planning", "succeeded", { created_at: "2026-11-01T09:00:00Z" });
  const partial = { status: "partial", skipped: [{ type: "user", id: "bob", reason: "Sharing is only allowed with group members" }] };
  let reshared = false;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = decodeURIComponent(new URL(String(input), location.href).pathname);
    if (init?.method === "POST") {
      reshared = true;
      return Response.json({ sharing: { status: "complete", skipped: [] } });
    }
    const current = reshared ? { ...standup, unshared_count: 0 } : standup;
    if (path === "/operator/jobs") return Response.json([current, planning]);
    const id = path.split("/").pop()!;
    const recording = id === "Standup" ? current : planning;
    return Response.json({
      job: recording,
      attempts: [{ job_id: id, attempt_number: 1, trigger_kind: "initial", request_json: "{}", stage: "done", state: "succeeded", created_at: stamp, updated_at: stamp, build_deferral_count: 0 }],
      availability: { source: "present", output: "present", published_attempt: 1 },
      sharing: id === "Standup" && !reshared ? partial : { status: "complete", skipped: [] },
    });
  }));
  app = mount(Operator, { target: host });
  await expect.element(page.getByText("Standup", { exact: true })).toBeVisible();
  await userEvent.click(page.getByText("Standup", { exact: true }).element().closest(".run-card")!);
  await userEvent.click(page.getByRole("button", { name: "Re-share with the room" }));
  await expect.element(page.getByText("Shared with everyone.", { exact: true })).toBeVisible();
  // Read there; gone with the run. Coming back finds the run as it now is,
  // not the answer again.
  await userEvent.click(page.getByText("Planning", { exact: true }).element().closest(".run-card")!);
  await expect.element(page.getByRole("heading", { name: "Planning" })).toBeVisible();
  expect(document.querySelector(".op-detail-pane")?.textContent).not.toContain("Shared with everyone.");
  await userEvent.click(page.getByText("Standup", { exact: true }).element().closest(".run-card")!);
  await expect.element(page.getByRole("heading", { name: "Standup" })).toBeVisible();
  expect(document.querySelector(".op-detail-pane")?.textContent).not.toContain("Shared with everyone.");
  expect(document.querySelector(".op-detail-pane")?.textContent).not.toContain("Published, but not shared with");
});

it("says beside the button why a re-share failed", async () => {
  serveReshare(job("Standup", "succeeded", { unshared_count: 1 }), "fails");
  await open("Standup");
  await userEvent.click(page.getByRole("button", { name: "Re-share with the room" }));
  await expect.element(page.getByRole("alert").filter({ hasText: "could not share the meeting again" })).toBeVisible();
  // The warning stays: nothing changed.
  await expect.element(page.getByText("Published. Not shared: 1 recipient", { exact: true })).toBeVisible();
});

it("acknowledges a re-share that Nextcloud still partly refused", async () => {
  const recording = job("Standup", "succeeded", { unshared_count: 1 });
  const partial = { status: "partial", skipped: [{ type: "user", id: "bob", reason: "Sharing is only allowed with group members" }] };
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = decodeURIComponent(new URL(String(input), location.href).pathname);
    if (init?.method === "POST") return Response.json({ sharing: partial });
    if (path === "/operator/jobs") return Response.json([recording]);
    if (path === "/operator/jobs/Standup") {
      return Response.json({
        job: recording,
        attempts: [{ job_id: "Standup", attempt_number: 1, trigger_kind: "initial", request_json: "{}", stage: "done", state: "succeeded", created_at: stamp, updated_at: stamp, build_deferral_count: 0 }],
        availability: { source: "present", output: "present", published_attempt: 1 },
        sharing: partial,
      });
    }
    return Response.json({});
  }));
  await open("Standup");
  await userEvent.click(page.getByRole("button", { name: "Re-share with the room" }));
  await expect.element(page.getByText("Shared again. Nextcloud still refused 1 recipient (bob). Check Doctor › Recording sharing.", { exact: true })).toBeVisible();
});

it("keeps a pending re-share's answer with the run it was made for", async () => {
  const alpha = job("Alpha", "succeeded", { unshared_count: 1 });
  const beta = job("Beta", "succeeded", { unshared_count: 1, created_at: "2026-11-01T09:00:00Z" });
  const partial = { status: "partial", skipped: [{ type: "user", id: "bob", reason: "Sharing is only allowed with group members" }] };
  let answer: (response: Response) => void = () => {};
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = decodeURIComponent(new URL(String(input), location.href).pathname);
    if (init?.method === "POST") return new Promise<Response>(resolve => { answer = resolve; });
    if (path === "/operator/jobs") return Response.json([alpha, beta]);
    const id = path.split("/").pop()!;
    const recording = id === "Alpha" ? alpha : beta;
    return Response.json({
      job: recording,
      attempts: [{ job_id: id, attempt_number: 1, trigger_kind: "initial", request_json: "{}", stage: "done", state: "succeeded", created_at: stamp, updated_at: stamp, build_deferral_count: 0 }],
      availability: { source: "present", output: "present", published_attempt: 1 },
      sharing: partial,
    });
  }));
  app = mount(Operator, { target: host });
  await expect.element(page.getByText("Alpha", { exact: true })).toBeVisible();
  await userEvent.click(page.getByText("Alpha", { exact: true }).element().closest(".run-card")!);
  await userEvent.click(page.getByRole("button", { name: "Re-share with the room" }));
  // Open Beta while Alpha's re-share is still pending.
  await userEvent.click(page.getByText("Beta", { exact: true }).element().closest(".run-card")!);
  await expect.element(page.getByRole("heading", { name: "Beta" })).toBeVisible();
  // Alpha's request is still pending: Beta shows no spinner of its own.
  expect(document.querySelector(".op-detail-pane .loading")).toBeNull();
  answer(Response.json({ error: "Cassini could not share the meeting again." }, { status: 502 }));
  await new Promise(resolve => setTimeout(resolve, 200));
  expect(document.querySelector(".op-detail-pane")?.textContent).not.toContain("could not share the meeting again");
  expect(document.querySelector(".op-detail-pane .loading")).toBeNull();
});
