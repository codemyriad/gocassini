import { mount, unmount } from "svelte";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { page } from "vitest/browser";
import ImportSettingsPanel from "./ImportSettingsPanel.svelte";
import { OperatorClient } from "./operator/client";
import "./app.css";

const initial = { max_concurrent_imports: 3, default: 3, max: 10 };
let stored = { ...initial };
let puts: unknown[] = [];
let served = true;
let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;

beforeEach(() => {
  stored = { ...initial };
  puts = [];
  served = true;
  host = document.createElement("div");
  document.body.append(host);
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    expect(new URL(String(input), location.href).pathname).toBe("/operator/settings/imports");
    if (!served) return Response.json({ error: "imports are not available on this server" }, { status: 404 });
    if (init?.method === "PUT") {
      const body = JSON.parse(init.body as string);
      puts.push(body);
      stored = { ...stored, max_concurrent_imports: body.max_concurrent_imports };
    }
    return Response.json(stored);
  }));
});

afterEach(async () => {
  if (app) await unmount(app);
  app = undefined;
  host.remove();
  vi.unstubAllGlobals();
});

it("saves the number of imports run at once", async () => {
  app = mount(ImportSettingsPanel, { target: host, props: { operatorClient: new OperatorClient("/operator") } });
  const field = page.getByLabelText("Imports at once");
  await expect.element(field).toHaveValue(3);
  await expect.element(page.getByText("1 to 10")).toBeVisible();
  await expect.element(page.getByText(/^The default is 3\./)).toBeVisible();
  await expect.element(page.getByRole("button", { name: "Save import settings" })).toBeDisabled();

  await field.fill("11");
  await expect.element(page.getByText(/^Choose a whole number from 1 to 10\. 10 is the most/)).toBeVisible();
  await expect.element(page.getByRole("button", { name: "Save import settings" })).toBeDisabled();

  await field.fill("5");
  await page.getByRole("button", { name: "Save import settings" }).click();
  await expect.element(page.getByText(/^Saved\./)).toBeVisible();
  expect(puts).toEqual([{ max_concurrent_imports: 5 }]);
  await expect.element(page.getByRole("button", { name: "Save import settings" })).toBeDisabled();
});

it("is not shown on a server that does not serve imports", async () => {
  served = false;
  app = mount(ImportSettingsPanel, { target: host, props: { operatorClient: new OperatorClient("/operator") } });
  await vi.waitFor(() => expect(vi.mocked(fetch)).toHaveBeenCalled());
  await expect.element(page.getByRole("heading", { name: "Imports" })).not.toBeInTheDocument();
  await expect.element(page.getByRole("alert")).not.toBeInTheDocument();
});
