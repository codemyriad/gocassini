import { mount, unmount } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { page } from "vitest/browser";
import SettingsPanel from "./SettingsPanel.svelte";
import { OperatorClient } from "./operator/client";
import "./app.css";

let component: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;
let retainVideo: boolean;
let quality: string;
let failSave: boolean;
let writes: Record<string, unknown>[];

beforeEach(() => {
  retainVideo = true; quality = "balanced"; failSave = false; writes = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(input), location.href).pathname;
    if (path === "/operator/settings") {
      if (init?.method === "PUT") {
        const body = JSON.parse(init.body as string); writes.push(body);
        if (failSave) return Response.json({ error: "Settings save failed" }, { status: 500 });
        retainVideo = body.retain_video;
        quality = body.quality ?? quality;
      }
      return Response.json({ quality, retain_video: retainVideo, transcription_enabled: false });
    }
    if (path === "/operator/settings/workflows") return Response.json([]);
    if (path === "/operator/settings/models") return Response.json({ models: [], jobs: [] });
    if (path === "/operator/readiness") return Response.json({ state: "passed", checks: [], secret_configured: true, secret_source: "env", test_room_url: "", test: { state: "", published: false } });
    if (path === "/operator/storage") return Response.json({ ok: true, service_account: { exists: true }, setup: [] });
    return Response.json({});
  }));
  host = document.createElement("div"); document.body.append(host);
});

afterEach(async () => {
  if (component) await unmount(component); component = undefined;
  host.remove(); vi.unstubAllGlobals();
});

describe("later video settings", () => {
  it("reads saved consent, protects failed edits and saves with transcription disabled", async () => {
    component = mount(SettingsPanel, { target: host, props: { operatorClient: new OperatorClient("/operator") } });
    const video = page.getByRole("radio", { name: "Full audio + video", exact: true });
    await expect.element(video).toBeChecked();
    const save = page.getByRole("button", { name: "Save", exact: true });
    await expect.element(save).not.toBeInTheDocument();
    await page.getByRole("radio", { name: "Audio-only", exact: true }).click();
    await expect.element(save).toBeEnabled();
    failSave = true;
    await save.click();
    await expect.element(page.getByText("Settings save failed", { exact: true })).toBeVisible();
    await expect.element(save).toBeEnabled();
    expect(retainVideo).toBe(true);
    failSave = false;
    await save.click();
    await expect.element(save).not.toBeInTheDocument();
    expect(retainVideo).toBe(false);
    expect(writes.at(-1)).toMatchObject({ retain_video: false, transcription_enabled: false });
    expect(writes.at(-1)).not.toHaveProperty("quality");
    await unmount(component); component = mount(SettingsPanel, { target: host, props: { operatorClient: new OperatorClient("/operator") } });
    await expect.element(video).not.toBeChecked();
  });
  it("includes quality when the administrator changes the tier", async () => {
    component = mount(SettingsPanel, { target: host, props: { operatorClient: new OperatorClient("/operator") } });
    const fast = page.getByRole("radio", { name: "Fast Fastest transcription, lower accuracy.", exact: true });
    await expect.element(fast).toBeVisible();
    await fast.click();
    const save = page.getByRole("button", { name: "Save", exact: true });
    await save.click();
    await expect.element(save).not.toBeInTheDocument();
    expect(writes.at(-1)).toMatchObject({ quality: "fast", retain_video: true });
    await expect.element(fast).toBeChecked();
  });

});
