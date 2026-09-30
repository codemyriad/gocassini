import { mount, unmount } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import App from "./App.svelte";
import "./app.css";

const defaults = () => ({
  version: 3, revision: 0, schedule: { time: "02:00", timezone: "UTC" },
  recordings: { forever: true }, current: { forever: true }, logs: { forever: true },
  history: { mode: "group", fine_initialized: false, policy: { forever: true },
    fine: Object.fromEntries(["failed_capture", "failed_build", "superseded", "failed_publish"]
      .map((key) => [key, { forever: true }])) },
});

let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;
let originalUrl: string;
let saved: ReturnType<typeof defaults>;
let admin: boolean;
let failGet: boolean;
let failPut: boolean;
let failCapturePut: boolean;
let failCaptureGet: boolean;
let capturePolicy: { quality: string; retain_video?: boolean };
let capturePuts: number;
let failAccount: boolean;
let holdSave: boolean;
let releaseSave: (() => void) | null;
let puts: number;
let gets: number;
let statusCalls: number;
let access: {
  ok: boolean;
  state: string;
  service_account: { exists: boolean; user?: string };
  setup: Array<{ id: string; action: string; browser: boolean; args: { user: string } }>;
};

beforeEach(() => {
  originalUrl = location.href;
  history.replaceState({}, "", location.pathname + location.search);
  localStorage.clear();
  saved = defaults();
  admin = true;
  failGet = false;
  failPut = false;
  failCapturePut = false;
  failCaptureGet = false;
  capturePolicy = { quality: "balanced" };
  capturePuts = 0;
  failAccount = false;
  holdSave = false;
  releaseSave = null;
  puts = 0;
  gets = 0;
  statusCalls = 0;
  access = { ok: true, state: "ready", service_account: { exists: true }, setup: [] };
  Object.assign(window, {
    __CASSINI_CONFIG__: { operatorBasePath: "/operator" },
    OC: { isUserAdmin: () => true },
  });
  vi.stubGlobal("EventSource", undefined);
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(input), location.href).pathname;
    const method = init?.method ?? "GET";
    if (path === "/operator/status") {
      statusCalls += 1;
      return Response.json(admin ? { ok: true, recordings_access: access } : { error: "Forbidden" },
        { status: admin ? 200 : 403 });
    }
    if (path === "/operator/setup") return Response.json({ ok: true, state: "ready", mode: "direct_shares",
      features: { summaries: false, insights: false } });
    if (path === "/operator/settings") {
      if (method === "GET") return Response.json(failCaptureGet ? { error: "Capture policy unavailable" } : capturePolicy, { status: failCaptureGet ? 503 : 200 });
      capturePuts += 1;
      if (failCapturePut) return Response.json({ error: "Could not save capture policy" }, { status: 500 });
      capturePolicy = { ...capturePolicy, ...JSON.parse(init!.body as string) };
      return Response.json(capturePolicy);
    }
    if (path === "/operator/storage/retention") {
      if (method === "GET") {
        gets += 1;
        return Response.json(failGet ? { error: "Synthetic retention read failure" } : saved,
          { status: failGet ? 503 : 200 });
      }
      puts += 1;
      if (failPut) return Response.json({ error: "Could not persist retention settings" }, { status: 500 });
      if ((init?.headers as Record<string, string>)["If-Match"] !== `"${saved.revision}"`) {
        return Response.json({ error: "Settings changed; reload before saving" }, { status: 412 });
      }
      if (holdSave) await new Promise<void>((resolve) => { releaseSave = resolve; });
      saved = { ...JSON.parse(init!.body as string), revision: saved.revision + 1 };
      return Response.json(saved);
    }
    if (path === "/operator/storage") return Response.json(failAccount
      ? { error: "Synthetic account check failure" } : access, { status: failAccount ? 503 : 200 });
    if (path.startsWith("/operator/storage/usage")) return Response.json({
      measured_at: new Date().toISOString(), categories: [], published: [], directories: [],
    });
    if (path === "/ocs/v2.php/cloud/users") {
      access.ok = true;
      access.service_account.exists = true;
      access.setup = [];
      return Response.json({ ocs: { meta: { statuscode: 100 }, data: {} } });
    }
    if (path.startsWith("/published/") || path === "/annotations/tags" || path === "/insights") {
      return Response.json(path === "/annotations/tags"
        ? { tags: [], meetings: [], revision: 0 } : path === "/insights" ? { insights: [] } : { version: 1, meetings: [] });
    }
    return Response.json({ jobs: [], attempts: [] });
  }));
  host = document.createElement("div");
  host.style.height = "100%";
  document.body.append(host);
});

afterEach(async () => {
  if (app) await unmount(app);
  app = undefined;
  host.remove();
  history.replaceState({}, "", originalUrl);
  delete (window as Window & { __CASSINI_CONFIG__?: unknown }).__CASSINI_CONFIG__;
  delete (window as Window & { OC?: unknown }).OC;
  vi.unstubAllGlobals();
  await page.viewport(1500, 1000);
});

const dialog = () => page.getByRole("dialog", { name: "Choose what Cassini keeps" });
const source = () => page.getByRole("group", { name: "Source recordings", exact: true });

async function open(hash = "") {
  if (app) await unmount(app);
  app = undefined;
  history.replaceState({}, "", location.pathname + location.search + hash);
  app = mount(App, { target: host });
  await expect.element(dialog()).toBeVisible();
  await expect.element(page.getByRole("button", { name: "Save and continue", exact: true })).toBeVisible();
}

describe("first-run retention review in the browser", () => {
  it("guards edits, recovers from failed and stale saves, and persists completion", async () => {
    await page.viewport(1280, 1000);
    await open();
    await expect.element(page.getByRole("heading", { name: "Who can see recordings" })).toBeVisible();
    await expect.element(page.getByRole("checkbox", { name: "Capture video", exact: true })).toBeVisible();
    expect(page.getByRole("checkbox").all()).toHaveLength(5);
    expect(puts).toBe(0);
    await expect.element(page.getByRole("button", { name: "Save and continue" })).toBeDisabled();
    expect(host.querySelector(".cassini-shell")).not.toBeNull();
    expect(host.querySelector(".cassini-app-content")!.hasAttribute("inert")).toBe(true);
    expect(page.getByRole("button", { name: "Set up later" }).all()).toHaveLength(0);
    page.getByRole("button", { name: "Reload saved settings" }).element().focus();
    await userEvent.keyboard("{Tab}");
    expect(document.activeElement).toBe(page.getByRole("region", { name: "Setup settings" }).element());
    await userEvent.keyboard("{Shift>}{Tab}{/Shift}");
    expect(document.activeElement).toBe(page.getByRole("button", { name: "Reload saved settings" }).element());
    await userEvent.keyboard("{Escape}");
    await expect.element(dialog()).toBeVisible();
    // Implicit form submission must not bypass the review gate.
    await source().getByRole("button", { name: "Custom days", exact: true }).click();
    await source().getByLabelText("Source recordings days").fill("30");
    await userEvent.keyboard("{Enter}");
    expect(puts).toBe(0);
    await page.getByRole("button", { name: "Reload saved settings" }).click();
    await expect.element(page.getByRole("alertdialog")).toBeVisible();
    await page.getByRole("button", { name: "Stay", exact: true }).click();
    await expect.element(source().getByLabelText("Source recordings days")).toHaveValue(30);
    await page.getByRole("button", { name: "Reload saved settings" }).click();
    await page.getByRole("button", { name: "Leave", exact: true }).click();
    await expect.element(dialog()).toBeVisible();
    expect(puts).toBe(0);
    await expect.element(source().getByRole("checkbox")).toBeChecked();

    failPut = true;
    await reviewAllSettings();
    await page.getByRole("button", { name: "Save and continue" }).click();
    await expect.element(page.getByRole("alert")).toHaveTextContent("Could not persist");
    await expect.element(dialog()).toBeVisible();
    failPut = false;
    saved.revision += 1;
    await source().getByRole("button", { name: "60 days", exact: true }).click();
    await reviewAllSettings();
    await page.getByRole("button", { name: "Save and continue" }).click();
    await expect.element(page.getByRole("alert")).toHaveTextContent("Settings changed");
    failGet = true;
    await page.getByRole("button", { name: "Reload saved settings" }).click();
    await page.getByRole("button", { name: "Leave", exact: true }).click();
    await expect.element(page.getByRole("alert")).toHaveTextContent("Synthetic retention read failure");
    await page.getByRole("button", { name: "Reload saved settings" }).click();
    await expect.element(page.getByRole("alertdialog")).toBeVisible();
    await page.getByRole("button", { name: "Stay", exact: true }).click();
    failGet = false;
    await page.getByRole("button", { name: "Reload saved settings" }).click();
    await page.getByRole("button", { name: "Leave", exact: true }).click();
    await expect.element(source().getByRole("checkbox")).toBeChecked();

    await source().getByRole("button", { name: "Custom days", exact: true }).click();
    await source().getByLabelText("Source recordings days").fill("0");
    const beforeInvalid = puts;
    await reviewAllSettings();
    await page.getByRole("button", { name: "Save and continue" }).click();
    expect(puts).toBe(beforeInvalid);
    await source().getByLabelText("Source recordings days").fill("45");
    await page.getByLabelText("Policy control").selectOptions("fine");
    await page.getByRole("group", { name: "Failed recordings", exact: true })
      .getByRole("button", { name: "7 days", exact: true }).click();
    await page.getByLabelText("Sweep time", { exact: true }).fill("15:45");
    await page.getByLabelText("Timezone", { exact: true }).fill("Europe/Zagreb");
    holdSave = true;
    await reviewAllSettings();
    await page.getByRole("button", { name: "Save and continue" }).click();
    await expect.element(page.getByRole("button", { name: "Reload saved settings" })).toBeDisabled();
    await expect.element(page.getByRole("button", { name: "Check this Nextcloud again" })).toBeDisabled();
    await expect.poll(() => releaseSave !== null).toBe(true);
    releaseSave!();
    holdSave = false;
    releaseSave = null;
    await expect.element(dialog()).not.toBeInTheDocument();
    expect(saved.recordings).toMatchObject({ count: 45 });
    expect(saved.history.fine.failed_capture).toMatchObject({ count: 7 });
    expect(saved.schedule.timezone).toBe("Europe/Zagreb");
    await openStorage();
    await expect.element(page.getByRole("heading", { name: "Retention policies", exact: true })).toBeVisible();
    await expect.element(source().getByLabelText("Source recordings days")).toHaveValue(45);
    await expect.element(page.getByLabelText("Policy control")).toHaveValue("fine");
    await expect.element(page.getByLabelText("Sweep time", { exact: true })).toHaveValue("15:45");
  });

  it("handles unchanged confirmation, account creation, admin gating, and responsive layouts", async () => {
    failAccount = true;
    await open("#surface=operator&panel=storage");
    expect(page.getByRole("heading", { name: "Retention policies", exact: true }).all()).toHaveLength(1);
    await expect.element(page.getByRole("alert")).toHaveTextContent("Synthetic account check failure");
    await reviewAllSettings();
    await page.getByRole("button", { name: "Save and continue" }).click();
    await expect.element(dialog()).not.toBeInTheDocument();
    expect(saved.revision).toBe(1);
    expect(saved.recordings.forever).toBe(true);
    failAccount = false;

    saved = defaults();
    access.ok = false;
    access.service_account = { exists: false, user: "cassini" };
    access.setup = [{ id: "account", action: "create_user", browser: true, args: { user: "cassini" } }];
    await open();
    Object.assign((window as unknown as { OC: Record<string, unknown> }).OC, { PasswordConfirmation: {
      requiresPasswordConfirmation: () => true,
      requirePasswordConfirmation: (callback: () => void) => {
        const fixture = document.createElement("div");
        fixture.id = "nc-password-fixture";
        fixture.innerHTML = '<input aria-label="Nextcloud password"><button>Confirm Nextcloud password</button>';
        document.body.append(fixture);
        fixture.querySelector("input")!.focus();
        fixture.querySelector("button")!.addEventListener("click", () => { fixture.remove(); callback(); });
      },
    } });
    await page.getByRole("button", { name: "Check this Nextcloud again" }).click();
    await source().getByRole("button", { name: "90 days", exact: true }).click();
    const beforeAccountGets = gets;
    await page.getByRole("button", { name: "Create recordings account", exact: true }).click();
    await expect.element(page.getByRole("button", { name: "Save and continue" })).toBeDisabled();
    await expect.element(page.getByRole("button", { name: "Reload saved settings" })).toBeDisabled();
    await userEvent.keyboard("{Tab}");
    expect(document.activeElement).toBe(page.getByRole("button", { name: "Confirm Nextcloud password" }).element());
    await page.getByRole("button", { name: "Confirm Nextcloud password" }).click();
    await expect.element(page.getByText("Nextcloud sharing is ready.", { exact: true })).toBeVisible();
    expect(source().getByRole("button", { name: "90 days", exact: true }).element().getAttribute("aria-pressed")).toBe("true");
    expect(gets).toBe(beforeAccountGets);
    await reviewAllSettings();
    await page.getByRole("button", { name: "Save and continue" }).click();
    await expect.element(dialog()).not.toBeInTheDocument();

    failGet = true;
    await remount();
    await page.getByRole("button", { name: "Review retention in Storage" }).click();
    await expect.element(page.getByRole("alert")).toHaveTextContent("Synthetic retention read failure");
    expect(dialog().all()).toHaveLength(0);
    failGet = false;

    admin = false;
    const beforeNonAdmin = gets;
    const beforeStatus = statusCalls;
    await remount();
    await expect.poll(() => statusCalls).toBeGreaterThan(beforeStatus);
    await new Promise((resolve) => setTimeout(resolve, 100));
    expect(gets).toBe(beforeNonAdmin);
    expect(dialog().all()).toHaveLength(0);
    expect(page.getByRole("button", { name: "Operator", exact: true }).all()).toHaveLength(0);

    admin = true;
    saved = defaults();
    await open();
    for (const [name, width, height, theme] of [
      ["desktop", 1280, 900, "saturn-light"], ["mobile", 390, 900, "saturn-light"], ["dark-mobile", 390, 900, "saturn-dark"], ["landscape", 900, 450, "saturn-light"],
    ] as const) {
      await page.viewport(width, height);
      document.querySelector(".cassini-retention-setup")!.setAttribute("data-theme", theme);
      page.getByRole("region", { name: "Setup settings" }).element().scrollTo(0, 0);
      await page.screenshot({ path: `../node_modules/.cache/vitest-screenshots/retention-setup-${name}.png` });
      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(innerWidth);
      const dialogElement = dialog().element();
      expect(dialogElement.scrollWidth).toBeLessThanOrEqual(dialogElement.clientWidth);
      const box = dialogElement.getBoundingClientRect();
      expect(box.top).toBeGreaterThanOrEqual(12);
      expect(box.bottom).toBeLessThanOrEqual(innerHeight - 12);
      expect(document.documentElement.scrollHeight).toBeLessThanOrEqual(innerHeight);
      const button = page.getByRole("button", { name: "Save and continue" }).element();
      const topButtonBox = button.getBoundingClientRect();
      expect(topButtonBox.bottom).toBeLessThanOrEqual(box.bottom);
      const scroller = page.getByRole("region", { name: "Setup settings" }).element();
      expect(scroller.scrollHeight).toBeGreaterThan(scroller.clientHeight);
      await reviewAllSettings();
      expect(button.getBoundingClientRect().top).toBe(topButtonBox.top);
      scroller.scrollTo(0, 0);
      await expect.element(page.getByRole("button", { name: "Save and continue" })).toBeEnabled();
    }
  });
});

async function reviewAllSettings() {
  const scroller = page.getByRole("region", { name: "Setup settings" }).element();
  scroller.scrollTo(0, scroller.scrollHeight);
  await expect.element(page.getByRole("button", { name: "Save and continue" })).toBeEnabled();
}

async function remount(hash = "") {
  if (app) await unmount(app);
  app = undefined;
  history.replaceState({}, "", location.pathname + location.search + hash);
  app = mount(App, { target: host });
}

async function openStorage() {
  await remount("#surface=operator&panel=storage");
}

 describe("install-time video consent", () => {
  it("shows video off alongside retention and saves untouched defaults", async () => {
    await open();
    const video = page.getByRole("checkbox", { name: "Capture video", exact: true });
    await expect.element(video).toBeVisible();
    await expect.element(video).not.toBeChecked();
    await expect.element(page.getByRole("heading", { name: "Retention policies" })).toBeVisible();
    await reviewAllSettings();
    await page.getByRole("button", { name: "Save and continue" }).click();
    await expect.element(dialog()).not.toBeInTheDocument();
    expect(capturePolicy.retain_video).toBe(false);
    expect(capturePuts).toBe(1);
    expect(saved.revision).toBe(1);
  });
  it("requires explicit opt-in and keeps failed saves open and retryable", async () => {
    await open();
    await page.getByRole("checkbox", { name: "Capture video", exact: true }).click();
    failCapturePut = true;
    await reviewAllSettings();
    await page.getByRole("button", { name: "Save and continue" }).click();
    await expect.element(page.getByRole("alert")).toHaveTextContent("Could not save capture policy");
    expect(puts).toBe(0);
    expect(saved.revision).toBe(0);
    await expect.element(dialog()).toBeVisible();
    failCapturePut = false;
    failPut = true;
    await page.getByRole("button", { name: "Save and continue" }).click();
    await expect.element(page.getByRole("alert")).toHaveTextContent("Could not persist retention settings");
    expect(capturePolicy.retain_video).toBe(true);
    expect(saved.revision).toBe(0);
    failPut = false;
    await page.getByRole("button", { name: "Save and continue" }).click();
    await expect.element(dialog()).not.toBeInTheDocument();
    expect(capturePolicy.retain_video).toBe(true);
    expect(saved.revision).toBe(1);
  });
  it("blocks completion when capture policy cannot be loaded, then recovers", async () => {
    failCaptureGet = true;
    await open();
    await expect.element(page.getByRole("alert")).toHaveTextContent("Capture policy unavailable");
    page.getByRole("region", { name: "Setup settings" }).element().scrollTo(0, 10000);
    await expect.element(page.getByRole("button", { name: "Save and continue" })).toBeDisabled();
    failCaptureGet = false;
    await page.getByRole("button", { name: "Retry capture policy" }).click();
    await expect.element(page.getByRole("checkbox", { name: "Capture video", exact: true })).not.toBeChecked();
  });
 });
