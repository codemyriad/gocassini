import { mount, unmount } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import App from "./App.svelte";
import { notifySetupChanged } from "./operator/setupSignal";
import "./app.css";

const defaults = () => ({
  version: 4, revision: 0, nextcloud: { meetings: { forever: true } }, schedule: { time: "02:00", timezone: "UTC" },
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
let failAccount: boolean;
let holdSave: boolean;
let releaseSave: (() => void) | null;
let puts: number;
let gets: number;
let statusCalls: number;
let retainVideo: boolean;
let captureWrites: Record<string, unknown>[];
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
  failAccount = false;
  holdSave = false;
  releaseSave = null;
  puts = 0;
  gets = 0;
  statusCalls = 0;
  retainVideo = false;
  captureWrites = [];
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
    if (path === "/operator/settings") {
      if (method === "PUT") {
        const body = JSON.parse(String(init?.body));
        captureWrites.push(body);
        retainVideo = body.retain_video;
      }
      return Response.json({ quality: "balanced", source: "auto", retain_video: retainVideo, transcription_enabled: false });
    }
    if (path === "/operator/settings/workflows") return Response.json([]);
    if (path === "/operator/settings/models") return Response.json({ models: [], jobs: [], downloads_allowed: false, device: "cpu" });
    if (path === "/operator/setup") return Response.json({ ok: true, state: "ready", mode: "direct_shares",
      features: { summaries: false, insights: false } });
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
    if (path.endsWith("/catalog.json")) return Response.json({ version: "cassini.viewer.catalog.v1", meetings: [] });
    if (path.startsWith("/published/") || path === "/annotations/tags" || path === "/insights") {
      return Response.json(path === "/annotations/tags"
        ? { tags: [], meetings: [], revision: 0 } : path === "/insights" ? { insights: [] } : { version: "cassini.viewer.catalog.v1", meetings: [] });
    }
    return Response.json({ jobs: [], attempts: [] });
  }));
  host = document.createElement("div");
  host.style.height = "100%";
  document.body.append(host);
});

afterEach(async () => {
  releaseSave?.();
  if (app) await unmount(app);
  app = undefined;
  host.remove();
  history.replaceState({}, "", originalUrl);
  delete (window as Window & { __CASSINI_CONFIG__?: unknown }).__CASSINI_CONFIG__;
  delete (window as Window & { OC?: unknown }).OC;
  vi.unstubAllGlobals();
  await page.viewport(1500, 1000);
});

const reminder = () => page.getByRole("region", { name: "Retention reminder" });
const source = () => page.getByRole("group", { name: "Source recordings", exact: true });
const save = () => page.getByRole("button", { name: "Save retention settings", exact: true });
async function open(hash = "") {
  if (app) await unmount(app);
  history.replaceState({}, "", location.pathname + location.search + hash);
  app = mount(App, { target: host });
  await expect.poll(() => statusCalls).toBeGreaterThan(0);
}
async function openStorage() {
  await page.getByRole("button", { name: "Review settings", exact: true }).click();
  await expect.element(save()).toBeEnabled();
}

describe("retention reminder in the app", () => {
  it("leaves the app usable and confirms unchanged defaults in Storage", async () => {
    await open();
    await expect.element(reminder()).toBeVisible();
    expect(host.querySelector(".cassini-app-content")!.hasAttribute("inert")).toBe(false);
    const frame = host.querySelector(".cassini-app-frame")!.getBoundingClientRect();
    const shell = host.querySelector(".cassini-shell")!.getBoundingClientRect();
    expect(Math.abs(shell.bottom - frame.bottom)).toBeLessThan(1);
    expect(page.getByRole("dialog").all()).toHaveLength(0);
    await page.getByRole("button", { name: "Operator", exact: true }).click();
    await expect.poll(() => host.querySelector(".op-shell") !== null || host.querySelector(".op-settings") !== null).toBe(true);
    expect(puts).toBe(0);
    const before = structuredClone(saved);
    await openStorage();
    await save().click();
    await expect.element(reminder()).not.toBeInTheDocument();
    expect(saved).toEqual({ ...before, revision: 1 });
    await open();
    await expect.poll(() => gets).toBeGreaterThan(2);
    expect(reminder().all()).toHaveLength(0);
    expect(puts).toBe(1);
  });

  it("lets a fresh admin submit a recording without acknowledging retention", async () => {
    const originalFetch = window.fetch;
    let requestedURL = "";
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).includes("/jobs?provider=nextcloud-talk") && init?.method === "POST") {
        requestedURL = JSON.parse(String(init.body)).url;
        return Response.json({ error: "Synthetic recorder unavailable" }, { status: 503 });
      }
      return originalFetch(input, init);
    }));
    await open("#surface=operator");
    await page.getByRole("button", { name: "Record a meeting", exact: true }).click();
    await page.getByLabelText("Meeting link", { exact: true }).fill("https://nextcloud.example.test/call/room");
    await page.getByRole("button", { name: "Start recording", exact: true }).click();
    await expect.poll(() => requestedURL).toBe("https://nextcloud.example.test/call/room");
    expect(puts).toBe(0);
    expect(saved.revision).toBe(0);
    expect(retainVideo).toBe(false);
    expect(captureWrites).toEqual([]);
  });

  it.each([false, true])("retention Save and Ignore preserve video consent (%s)", async (video) => {
    retainVideo = video;
    await open();
    await openStorage();
    await save().click();
    await expect.element(reminder()).not.toBeInTheDocument();
    expect(retainVideo).toBe(video);
    expect(captureWrites).toEqual([]);

    saved = defaults();
    await open();
    await page.getByRole("button", { name: "Don't remind again", exact: true }).click();
    await expect.element(reminder()).not.toBeInTheDocument();
    expect(saved.revision).toBe(1);
    expect(retainVideo).toBe(video);
    expect(captureWrites).toEqual([]);
  });

  it("saves video opt-in independently while retention remains unconfirmed", async () => {
    await open("#surface=operator&panel=pipeline");
    const video = page.getByRole("radio", { name: "Full audio + video", exact: true });
    await expect.element(video).not.toBeChecked();
    await expect.element(reminder()).toBeVisible();
    await video.click();
    const ignore = page.getByRole("button", { name: "Don't remind again", exact: true });
    await expect.element(ignore).toBeDisabled();
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect.element(page.getByRole("button", { name: "Save", exact: true })).not.toBeInTheDocument();
    expect(captureWrites).toHaveLength(1);
    expect(captureWrites[0]).toMatchObject({ retain_video: true });
    expect(captureWrites[0]).not.toHaveProperty("quality");
    expect(saved.revision).toBe(0);
    expect(puts).toBe(0);
    await expect.element(reminder()).toBeVisible();
    await expect.element(ignore).toBeEnabled();
    await ignore.click();
    await expect.element(reminder()).not.toBeInTheDocument();
    expect(retainVideo).toBe(true);
    await open("#surface=operator&panel=pipeline");
    await expect.element(video).toBeChecked();
    expect(captureWrites).toHaveLength(1);
  });

  it("describes and preserves Nextcloud expiry when dismissing", async () => {
    Object.assign(saved.nextcloud.meetings, { forever: false, count: 30, unit: "days" });
    const before = structuredClone(saved);
    await open();
    await expect.element(reminder()).toHaveTextContent("Review how long Cassini keeps container files and published meetings.");
    expect(reminder().element().textContent).not.toContain("indefinitely");
    await page.getByRole("button", { name: "Don't remind again", exact: true }).click();
    await expect.element(reminder()).not.toBeInTheDocument();
    expect(saved).toEqual({ ...before, revision: 1 });
  });

  it("does not resurrect an acknowledged reminder when later reads would fail", async () => {
    saved.revision = 2;
    await open();
    await expect.poll(() => gets).toBe(1);
    failGet = true;
    window.dispatchEvent(new Event("focus"));
    await new Promise(resolve => setTimeout(resolve, 50));
    expect(reminder().all()).toHaveLength(0);
    expect(gets).toBe(1);
  });

  it("keeps the reminder across visits without Save and protects dirty navigation", async () => {
    await open();
    await openStorage();
    await source().getByRole("button", { name: "30 days", exact: true }).click();
    await page.getByRole("button", { name: "Review settings", exact: true }).click();
    expect(source().getByRole("button", { name: "30 days", exact: true }).element().getAttribute("aria-pressed")).toBe("true");
    await page.getByRole("button", { name: "Browse", exact: true }).click();
    await expect.element(page.getByRole("alertdialog")).toBeVisible();
    await page.getByRole("button", { name: "Stay", exact: true }).click();
    const unload = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(unload);
    expect(unload.defaultPrevented).toBe(true);
    await page.getByRole("button", { name: "Browse", exact: true }).click();
    await page.getByRole("button", { name: "Leave", exact: true }).click();
    await expect.element(reminder()).toBeVisible();
    await open();
    await expect.element(reminder()).toBeVisible();
    expect(saved.revision).toBe(0);
    expect(puts).toBe(0);
  });

  it("preserves drafts after failed saves and reloads, and handles conflicts", async () => {
    await open();
    await openStorage();
    await source().getByRole("button", { name: "30 days", exact: true }).click();
    failPut = true;
    await save().click();
    await expect.element(page.getByRole("alert")).toHaveTextContent("Could not persist");
    await expect.element(reminder()).toBeVisible();
    failPut = false;
    saved.revision++;
    await save().click();
    await expect.element(page.getByRole("alert")).toHaveTextContent("Settings changed");
    failGet = true;
    await page.getByRole("button", { name: "Reload saved settings" }).click();
    await page.getByRole("button", { name: "Leave", exact: true }).click();
    await expect.element(page.getByRole("alert")).toHaveTextContent("Synthetic retention read failure");
    await page.getByRole("button", { name: "Reload saved settings" }).click();
    await expect.element(page.getByRole("alertdialog")).toBeVisible();
    failGet = false;
    await page.getByRole("button", { name: "Leave", exact: true }).click();
    await expect.element(reminder()).not.toBeInTheDocument();
    await expect.element(source().getByRole("checkbox")).toBeChecked();
  });

  it("recovers failed reads without blocking Operator", async () => {
    failGet = true;
    await open("#surface=operator");
    await expect.element(page.getByRole("alert")).toHaveTextContent("Could not load retention settings");
    failGet = false;
    await page.getByRole("button", { name: "Retry", exact: true }).click();
    await expect.element(page.getByRole("alert")).not.toBeInTheDocument();
    await expect.element(reminder()).toBeVisible();
    expect(puts).toBe(0);
  });

  it("does not request retention for non-admins even with an optimistic admin hint", async () => {
    admin = false;
    await open("#surface=operator&panel=storage");
    await expect.element(page.getByRole("button", { name: "Operator", exact: true })).not.toBeInTheDocument();
    expect(gets).toBe(0);
    expect(reminder().all()).toHaveLength(0);
  });

  it("reconciles another session on focus without overwriting the editor draft", async () => {
    await open();
    await openStorage();
    await source().getByRole("button", { name: "30 days", exact: true }).click();
    saved.revision++;
    window.dispatchEvent(new Event("focus"));
    await expect.element(reminder()).not.toBeInTheDocument();
    expect(source().getByRole("button", { name: "30 days", exact: true }).element().getAttribute("aria-pressed")).toBe("true");
    await save().click();
    await expect.element(page.getByRole("alert")).toHaveTextContent("Settings changed");
  });

  it("does not withhold Operator for a slow read or restore a reminder after Save", async () => {
    const originalFetch = window.fetch;
    let release: (() => void) | undefined;
    let held = false;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const response = await originalFetch(input, init);
      if (!held && String(input).endsWith("/storage/retention") && !init?.method) {
        held = true;
        await new Promise<void>(resolve => { release = resolve; });
      }
      return response;
    }));
    await open("#surface=operator");
    await expect.poll(() => !!release).toBe(true);
    expect(host.querySelector(".op-shell")).not.toBeNull();
    history.pushState({}, "", "#surface=operator&panel=storage");
    window.dispatchEvent(new PopStateEvent("popstate"));
    await expect.element(save()).toBeEnabled();
    await save().click();
    await expect.element(reminder()).not.toBeInTheDocument();
    release!();
    await new Promise(resolve => setTimeout(resolve, 50));
    expect(reminder().all()).toHaveLength(0);
  });

  it("keeps account creation and Nextcloud password confirmation in Publish pipeline", async () => {
    access.ok = false;
    access.service_account = { exists: false, user: "cassini" };
    access.setup = [{ id: "account", action: "create_user", browser: true, args: { user: "cassini" } }];
    await open("#surface=operator&panel=pipeline");
    Object.assign((window as unknown as { OC: Record<string, unknown> }).OC, { PasswordConfirmation: {
      requiresPasswordConfirmation: () => true,
      requirePasswordConfirmation: (callback: () => void) => {
        const fixture = document.createElement("div");
        fixture.innerHTML = '<input aria-label="Nextcloud password"><button>Confirm Nextcloud password</button>';
        document.body.append(fixture);
        fixture.querySelector("input")!.focus();
        fixture.querySelector("button")!.addEventListener("click", () => { fixture.remove(); callback(); });
      },
    } });
    await page.getByRole("button", { name: "Create recordings account", exact: true }).click();
    await userEvent.keyboard("{Tab}");
    expect(document.activeElement).toBe(page.getByRole("button", { name: "Confirm Nextcloud password" }).element());
    await page.getByRole("button", { name: "Confirm Nextcloud password" }).click();
    await expect.element(page.getByText("Nextcloud sharing is ready.", { exact: true })).toBeVisible();
    await expect.element(reminder()).toBeVisible();
    expect(puts).toBe(0);
  });

  it("dismisses only after persistence, keeps all values and survives a new session", async () => {
    await open();
    await expect.element(reminder()).toBeVisible();
    const before = structuredClone(saved);
    holdSave = true;
    await page.getByRole("button", { name: "Don't remind again", exact: true }).click();
    await expect.poll(() => releaseSave !== null).toBe(true);
    await expect.element(reminder()).toBeVisible();
    await expect.element(page.getByRole("button", { name: "Dismissing…", exact: true })).toBeDisabled();
    expect(puts).toBe(1);
    const beforeProbe = statusCalls;
    notifySetupChanged();
    await expect.poll(() => statusCalls).toBeGreaterThan(beforeProbe);
    releaseSave!();
    holdSave = false;
    await expect.element(reminder()).not.toBeInTheDocument();
    expect(saved).toEqual({ ...before, revision: 1 });
    await open();
    await expect.poll(() => gets).toBeGreaterThan(2);
    expect(reminder().all()).toHaveLength(0);
    expect(puts).toBe(1);
  });

  it("keeps a failed dismissal visible and allows retry", async () => {
    await open();
    failPut = true;
    await page.getByRole("button", { name: "Don't remind again", exact: true }).click();
    await expect.element(page.getByRole("alert")).toHaveTextContent("Could not dismiss the reminder");
    expect(saved.revision).toBe(0);
    failPut = false;
    await page.getByRole("button", { name: "Don't remind again", exact: true }).click();
    await expect.element(reminder()).not.toBeInTheDocument();
    expect(saved.revision).toBe(1);
  });

  it("protects a dirty draft and excludes Ignore while the editor saves", async () => {
    await open();
    await openStorage();
    const ignore = page.getByRole("button", { name: "Don't remind again", exact: true });
    await source().getByRole("button", { name: "30 days", exact: true }).click();
    await expect.element(ignore).toBeDisabled();
    await expect.element(page.getByText("Save or discard your changes before dismissing.")).toBeVisible();
    await page.getByRole("button", { name: "Reload saved settings" }).click();
    await page.getByRole("button", { name: "Leave", exact: true }).click();
    await expect.element(ignore).toBeEnabled();
    holdSave = true;
    await save().click();
    await expect.poll(() => releaseSave !== null).toBe(true);
    await expect.element(ignore).toBeDisabled();
    releaseSave!();
    holdSave = false;
    await expect.element(reminder()).not.toBeInTheDocument();
    expect(puts).toBe(1);
  });

  it("acknowledges a save that completes after leaving the pristine editor", async () => {
    await open();
    await openStorage();
    holdSave = true;
    await save().click();
    await expect.poll(() => releaseSave !== null).toBe(true);
    await page.getByRole("button", { name: "Browse", exact: true }).click();
    await expect.element(page.getByRole("heading", { name: "Retention policies", exact: true })).not.toBeInTheDocument();
    releaseSave!();
    holdSave = false;
    await expect.element(reminder()).not.toBeInTheDocument();
    expect(saved.revision).toBe(1);
  });

  it("blocks editor changes while Ignore is saving", async () => {
    await open();
    await openStorage();
    holdSave = true;
    await page.getByRole("button", { name: "Don't remind again", exact: true }).click();
    await expect.poll(() => releaseSave !== null).toBe(true);
    await expect.element(save()).toBeDisabled();
    await expect.element(source().getByRole("checkbox")).toBeDisabled();
    releaseSave!();
    holdSave = false;
    await expect.element(reminder()).not.toBeInTheDocument();
    expect(puts).toBe(1);
  });

  it.each([[1280, 900, "saturn-light"], [390, 900, "saturn-light"], [390, 900, "saturn-dark"], [900, 450, "saturn-light"]] as const)(
    "keeps the banner usable at %ix%i in %s", async (width, height, theme) => {
      await page.viewport(width, height);
      await open();
      await expect.element(reminder()).toBeVisible();
      host.querySelectorAll("[data-theme]").forEach(el => el.setAttribute("data-theme", theme));
      await page.screenshot({ path: `../node_modules/.cache/vitest-screenshots/retention-reminder-${width}-${height}-${theme}.png` });
      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(innerWidth);
      await userEvent.keyboard("{Escape}");
      await expect.element(reminder()).toBeVisible();
      await openStorage();
      await expect.element(save()).toBeEnabled();
    });
});
