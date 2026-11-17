import { mount, unmount } from "svelte";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { page } from "vitest/browser";
import { get } from "svelte/store";
import { LL } from "cassini-viewer/i18n";
import App from "./App.svelte";
import "./app.css";

// #500: the "Recording setup needs an administrator's attention" banner sits
// above the browse surface. Whatever else the shell draws, the viewer must fit
// in what is left — the frame clips overflow, so anything pushed below it (the
// selection bar's "Prepare meeting for insight") cannot be reached at all.

const HEIGHT = 800;
const catalog = {
  version: "cassini.viewer.catalog.v1",
  meetings: [
    { id: "m1", title: "Layout fixture", dateLabel: "2026-11-17", audioPath: "m1.opus" },
    { id: "m2", title: "Second fixture", dateLabel: "2026-11-16", audioPath: "m2.opus" },
  ],
};

let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;
const originalUrl = location.href;

function stubOperator(options: { admin: boolean; setupOk: boolean }) {
  vi.stubGlobal("OC", { isUserAdmin: () => options.admin });
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const path = new URL(String(input), location.href).pathname;
    if (path === "/operator/status") return Response.json({ ok: true, recordings_access: { ok: true, setup: [] } });
    if (path === "/operator/setup") {
      return Response.json(options.setupOk
        ? { ok: true, state: "ready", mode: "direct_shares", recording_state: "needs_action", features: { summaries: false, insights: false } }
        : { ok: false, state: "unconfigured", mode: "", cause: "no_service_account", recording_state: "needs_action", features: { summaries: false, insights: false } });
    }
    if (path.endsWith("/catalog.json")) {
      // A constructed Response has no url, and the catalog resolves its
      // asset paths against it.
      const response = Response.json(catalog);
      Object.defineProperty(response, "url", { value: new URL(path, location.href).toString() });
      return response;
    }
    if (path === "/operator/settings/workflows") return Response.json([]);
    if (path === "/operator/storage/retention") return Response.json({ revision: 1 });
    return new Response(null, { status: 404 });
  }));
}

beforeEach(() => {
  Object.assign(window, { __CASSINI_CONFIG__: { operatorBasePath: "/operator" } });
  history.replaceState({}, "", location.pathname + location.search);
  host = document.createElement("div");
  host.style.cssText = `position:fixed;top:0;left:0;right:0;height:${HEIGHT}px`;
  document.body.append(host);
});

afterEach(async () => {
  if (app) await unmount(app);
  app = undefined;
  host.remove();
  history.replaceState({}, "", originalUrl);
  delete (window as Window & { __CASSINI_CONFIG__?: unknown }).__CASSINI_CONFIG__;
  vi.unstubAllGlobals();
});

const banner = () => page.getByText(get(LL).app.shell.app.recording_needs_action());

for (const scenario of [
  { name: "a reader with no operator tab", admin: false, setupOk: true },
  { name: "a reader under the setup notice", admin: false, setupOk: false },
  { name: "an administrator", admin: true, setupOk: true },
]) {
  it(`keeps the selection bar inside the frame under the recording warning, for ${scenario.name}`, async () => {
    stubOperator(scenario);
    app = mount(App, { target: host });
    await expect.element(banner()).toBeVisible();
    await page.getByRole("checkbox", { name: "Select Layout fixture", exact: true }).click();
    await expect.poll(() => host.querySelector(".selection-dock")).not.toBeNull();

    const frame = host.getBoundingClientRect();
    const dockElement = host.querySelector<HTMLElement>(".selection-dock")!;
    const dock = dockElement.getBoundingClientRect();
    expect(dock.height).toBeGreaterThan(0);
    expect(dock.bottom).toBeLessThanOrEqual(frame.bottom + 0.5);
    // The browse surface as a whole, not only the bar: nothing of it is clipped.
    const root = dockElement.closest<HTMLElement>(".cassini-root")!.getBoundingClientRect();
    expect(root.bottom).toBeLessThanOrEqual(frame.bottom + 0.5);

    // The warning is themed: it paints an opaque surface, not Nextcloud's
    // wallpaper through a transparent box.
    const strip = banner().element() as HTMLElement;
    expect(strip.closest("[data-theme]")).not.toBeNull();
    let painted: HTMLElement | null = strip;
    while (painted && getComputedStyle(painted).backgroundColor === "rgba(0, 0, 0, 0)") painted = painted.parentElement;
    expect(painted && host.contains(painted)).toBe(true);

    const openSetup = page.getByRole("button", { name: get(LL).app.shell.app.open_recording_setup() });
    if (!scenario.admin) {
      await expect.element(openSetup).not.toBeInTheDocument();
      return;
    }
    // An administrator's route still lands on the operator surface, and the
    // warning stays above it.
    await openSetup.click();
    await expect.poll(() => host.querySelector(".cassini-shell-scroll")).not.toBeNull();
    expect(dockElement.closest(".cassini-shell-hidden")).not.toBeNull();
    await expect.element(banner()).toBeVisible();
    const operator = host.querySelector<HTMLElement>(".cassini-shell-scroll")!.getBoundingClientRect();
    expect(operator.height).toBeGreaterThan(0);
    expect(operator.bottom).toBeLessThanOrEqual(frame.bottom + 0.5);
  });
}
