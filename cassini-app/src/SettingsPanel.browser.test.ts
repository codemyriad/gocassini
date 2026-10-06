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
