import { mount, unmount } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { page } from "vitest/browser";
import TopicSuggestionsSettings from "./TopicSuggestionsSettings.svelte";
import { OperatorClient } from "./operator/client";
import type { SpeechModel, SpeechModelInventory, TopicSettingsStatus } from "./operator/types";
import { TOPIC_SUGGESTIONS_MODEL_ID } from "./speechModels";
import "./app.css";

const embedder = (ready: boolean): SpeechModel => ({
  id: TOPIC_SUGGESTIONS_MODEL_ID, kind: "embedding", name: "EmbeddingGemma 2 (text) — int8", description: "Multilingual text embeddings",
  revision: "e".repeat(64), download_bytes: 288733465, installed_bytes: 318909106, installed: ready, ready, device: "cpu", runtime_supported: true,
});
const inventory = (ready: boolean): SpeechModelInventory => ({ models: [embedder(ready)], jobs: [], downloads_allowed: true, device: "cpu" });

function status(over: Partial<TopicSettingsStatus> = {}): TopicSettingsStatus {
  return {
    enabled: false, threads: 0, available: true, ready: false, scorerVersion: 1,
    counts: {}, unavailable: {}, oldestWaitingSeconds: 0, blocked: [], vectorsBytes: 0, estimateSecondsPerMeeting: 10, worker: {},
    ...over,
  };
}

let current = status();
let requests: { method: string; path: string; body: unknown }[] = [];
let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;

beforeEach(() => {
  current = status();
  requests = [];
  host = document.createElement("div");
  document.body.append(host);
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(input), location.href).pathname.replace(/^\/operator/, "");
    const method = init?.method ?? "GET";
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    requests.push({ method, path, body });
    if (path === "/settings/topics" && method === "PUT") {
      if (!current.ready) return Response.json({ error: "Install the topic suggestions model first." }, { status: 409 });
      current = { ...current, enabled: body.enabled };
    }
    if (path === "/settings/topics/backfill") {
      current = { ...current, backfillSince: body.since ?? "all", counts: { ...current.counts, queued: 12 } };
      return Response.json({ queued: 12, estimateSeconds: 120 });
    }
    if (path === "/settings/topics/retry") {
      current = { ...current, blocked: [], counts: { ...current.counts, blocked: 0, queued: (current.counts.queued ?? 0) + 1 } };
      return Response.json({ retried: 1 });
    }
    return Response.json(current);
  }));
});

afterEach(async () => {
  if (app) await unmount(app);
  app = undefined;
  host.remove();
  vi.unstubAllGlobals();
});

function mountCard(ready: boolean) {
  app = mount(TopicSuggestionsSettings, { target: host, props: { client: new OperatorClient("/operator"), inventory: inventory(ready), refreshMs: 0 } });
}

describe("topic suggestions in Settings", () => {
  it("keeps the switch off until the model is ready", async () => {
    mountCard(false);
    const toggle = page.getByRole("checkbox", { name: "Suggest topics after each meeting" });
    await expect.element(toggle).toBeDisabled();
    await expect.element(page.getByText("Download the model and let its check pass first.")).toBeVisible();
    expect(requests.some((r) => r.method === "PUT")).toBe(false);
  });

  it("switches on, checks past meetings from a date and shows the work counts", async () => {
    current = status({ ready: true, counts: { done: 3 } });
    mountCard(true);
    const toggle = page.getByRole("checkbox", { name: "Suggest topics after each meeting" });
    await expect.element(toggle).toBeEnabled();
    await toggle.click();
    await expect.element(toggle).toBeChecked();
    expect(requests.find((r) => r.method === "PUT")?.body).toEqual({ enabled: true });
    await expect.element(page.getByText(/Meetings from before suggestions were switched on are not checked yet/)).toBeVisible();

    await page.getByLabelText("Check meetings recorded on or after").fill("2026-06-01");
    await page.getByRole("button", { name: "Check past meetings" }).click();
    await expect.element(page.getByText("Queued 12 past meetings. About 2 minutes of work is waiting.")).toBeVisible();
    expect(requests.find((r) => r.path === "/settings/topics/backfill")?.body).toEqual({ since: "2026-06-01" });
    await expect.element(page.getByTestId("topic-counts")).toHaveTextContent("Checked 3 · 12 waiting. Meetings since 2026-06-01 are included.");
  });

  it("lists blocked meetings and retries them", async () => {
    current = status({ ready: true, enabled: true, counts: { done: 1, blocked: 1 }, blocked: [{ meeting: "abc.opus", reason: "failed", error: "onnx runtime crashed" }] });
    mountCard(true);
    await expect.element(page.getByText("1 meeting could not be checked after several tries.")).toBeVisible();
    await expect.element(page.getByText("onnx runtime crashed", { exact: false })).toBeVisible();
    await page.getByRole("button", { name: "Retry" }).click();
    await expect.element(page.getByText(/could not be checked after several tries/)).not.toBeInTheDocument();
    expect(requests.some((r) => r.path === "/settings/topics/retry" && r.method === "POST")).toBe(true);
  });

  it("says why it is paused when the model's check failed", async () => {
    current = status({ ready: false, enabled: true, reason: "model-fault" });
    mountCard(false);
    await expect.element(page.getByText("Paused: the model's check failed. Check it again above.")).toBeVisible();
  });
});
