import { mount, unmount } from "svelte";
import { afterEach, expect, it, vi } from "vitest";
import { page } from "vitest/browser";
import SettingsPanel from "./SettingsPanel.svelte";
import { OperatorClient } from "./operator/client";
import "./app.css";

let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;
afterEach(async () => {
  if (app) await unmount(app);
  app = undefined;
  host?.remove();
  vi.unstubAllGlobals();
});

it("saves the publication format through settings and restores it on reload", async () => {
  let saved = { quality: "balanced", meeting_format: "opus", transcription_enabled: false };
  const writes: Record<string, unknown>[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(input), location.href).pathname;
    if (path === "/operator/settings") {
      if (init?.method === "PUT") {
        const update = JSON.parse(init.body as string);
        writes.push(update);
        saved = { ...saved, ...update };
      }
      return Response.json(saved);
    }
    if (path === "/operator/settings/models") return Response.json({ models: [], jobs: [], downloads_allowed: false });
    if (path === "/operator/settings/llm") {
      return Response.json({ providers: [], summary: { enabled: false }, insights: { enabled: false } });
    }
    return Response.json({});
  }));
  host = document.createElement("div");
  document.body.append(host);
  const open = () => app = mount(SettingsPanel, { target: host, props: { operatorClient: new OperatorClient("/operator") } });
  open();
  const format = page.getByRole("combobox", { name: "Published meeting" });
  await expect.element(format).toHaveValue("opus");
  await format.selectOptions("json");
  expect(writes).toHaveLength(0);
  await expect.element(page.getByText(/otherwise new meetings will contain neither/)).toBeVisible();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect.poll(() => saved.meeting_format).toBe("json");
  expect(writes[0].meeting_format).toBe("json");
  await unmount(app!);
  app = undefined;
  open();
  await expect.element(format).toHaveValue("json");
  await format.selectOptions("opus");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect.poll(() => saved.meeting_format).toBe("opus");
});

it("keeps publication separate from source retention and explicitly opts into deletion", async () => {
  let saved = { quality: "balanced", meeting_format: "json", source_retention: "storage-policy", retain_video: true, transcription_enabled: true, active_model: "prepared", active_revision: "r1" };
  const writes: Record<string, unknown>[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(input), location.href).pathname;
    if (path === "/operator/settings") {
      if (init?.method === "PUT") {
        const update = JSON.parse(init.body as string); writes.push(update); saved = { ...saved, ...update };
      }
      return Response.json(saved);
    }
    if (path === "/operator/settings/models") return Response.json({ models: [], jobs: [] });
    if (path === "/operator/settings/llm") return Response.json({ providers: [], summary: { enabled: false }, insights: { enabled: false } });
    return Response.json({});
  }));
  host = document.createElement("div"); document.body.append(host);
  const open = () => app = mount(SettingsPanel, { target: host, props: { operatorClient: new OperatorClient("/operator") } });
  open();
  const retention = page.getByRole("combobox", { name: "Source media after processing" });
  await expect.element(retention).toHaveValue("storage-policy");
  await expect.element(page.getByRole("checkbox", { name: "Capture video", exact: true })).toBeChecked();
  expect(writes).toHaveLength(0);
  await retention.selectOptions("delete-after-processing");
  await expect.element(page.getByText(/Processing cannot be rerun/)).toBeVisible();
  await expect.element(page.getByRole("checkbox", { name: "Capture video", exact: true })).not.toBeChecked();
  await expect.element(page.getByRole("checkbox", { name: "Capture video", exact: true })).toBeDisabled();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect.poll(() => saved.source_retention).toBe("delete-after-processing");
  expect(writes[0]).toMatchObject({ meeting_format: "json", source_retention: "delete-after-processing", retain_video: false, transcription_enabled: true });
  await unmount(app!); app = undefined; open();
  await expect.element(retention).toHaveValue("delete-after-processing");
  await retention.selectOptions("storage-policy");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect.poll(() => saved.source_retention).toBe("storage-policy");
  expect(saved.meeting_format).toBe("json");
});

it("requires prepared transcription before saving deletion", async () => {
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const path = new URL(String(input), location.href).pathname;
    if (path === "/operator/settings") return Response.json({ quality: "balanced", meeting_format: "json", transcription_enabled: false });
    if (path === "/operator/settings/models") return Response.json({ models: [], jobs: [] });
    return Response.json({});
  }));
  host = document.createElement("div"); document.body.append(host);
  app = mount(SettingsPanel, { target: host, props: { operatorClient: new OperatorClient("/operator") } });
  await page.getByRole("combobox", { name: "Source media after processing" }).selectOptions("delete-after-processing");
  await expect.element(page.getByText("Enable a prepared transcription model below before saving source deletion.")).toBeVisible();
  await expect.element(page.getByRole("button", { name: "Save", exact: true })).toBeDisabled();
});
