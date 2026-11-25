import { mount, unmount } from "svelte";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { page } from "vitest/browser";
import ImportSettingsPanel from "./ImportSettingsPanel.svelte";
import { OperatorClient } from "./operator/client";
import "./app.css";

let stored = { max_concurrent_imports: 3, source: "deploy", default: 3, max: 10 };
let puts: unknown[] = [];
let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;

beforeEach(() => {
  stored = { max_concurrent_imports: 3, source: "deploy", default: 3, max: 10 };
  puts = [];
  host = document.createElement("div");
  document.body.append(host);
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    expect(new URL(String(input), location.href).pathname).toBe("/operator/settings/imports");
    if (init?.method === "PUT") {
      const body = JSON.parse(init.body as string);
      puts.push(body);
      stored = { ...stored, max_concurrent_imports: body.max_concurrent_imports, source: "admin" };
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

it("saves the number of imports run at once and says where it comes from", async () => {
  app = mount(ImportSettingsPanel, { target: host, props: { operatorClient: new OperatorClient("/operator") } });
  const field = page.getByLabelText("Imports at once");
  await expect.element(field).toHaveValue(3);
  await expect.element(page.getByText(/Set when the app was deployed/)).toBeVisible();
  await expect.element(page.getByRole("button", { name: "Save import settings" })).toBeDisabled();

  await field.fill("11");
  await expect.element(page.getByText("Choose a whole number from 1 to 10.")).toBeVisible();
  await expect.element(page.getByRole("button", { name: "Save import settings" })).toBeDisabled();

  await field.fill("5");
  await page.getByRole("button", { name: "Save import settings" }).click();
  await expect.element(page.getByText(/^Saved\./)).toBeVisible();
  await expect.element(page.getByText("Saved here. Redeploying the app keeps it.")).toBeVisible();
  expect(puts).toEqual([{ max_concurrent_imports: 5 }]);
  await expect.element(page.getByRole("button", { name: "Save import settings" })).toBeDisabled();
});
