import { mount, unmount } from "svelte";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import App from "../../App.svelte";
import "../../app.css";
import { AnnotationError } from "../../viewer/annotations";
import { annotationFixture, focusTag, savedMark } from "./annotations.fixture";

let fixture: ReturnType<typeof annotationFixture>;
let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;
let originalUrl: string;

beforeEach(() => {
  originalUrl = location.href;
  history.replaceState(null, "", location.pathname + location.search);
  host = document.createElement("div");
  host.style.height = "100vh";
  document.body.append(host);
  fixture = annotationFixture();
});

afterEach(async () => {
  if (app) await unmount(app);
  app = undefined;
  fixture.dispose();
  delete window.__CASSINI_VIEWER_BASE__;
  host.remove();
  history.replaceState(null, "", originalUrl);
});

it("ignores a catalog response started before a title save", async () => {
  // A refresh already in flight when the rename commits was fetched with the
  // old title; applying it would visibly revert the card (same race the room
  // name dialog guards against).
  const initial = fixture.snapshot();
  initial.sync = { state: "saved", desired: 1, confirmed: 1 };
  const renamed = structuredClone(initial);
  renamed.revision = renamed.annotations!.revision = 2;
  renamed.stateToken = "epoch:2";
  renamed.sync = { state: "saved", desired: 2, confirmed: 2 };
  renamed.annotations!.meeting = {
    title: "Renamed meeting", setAtUtc: "2026-10-21T12:00:00Z",
    actor: { kind: "person", id: "ana" },
  };
  fixture.load.mockResolvedValue(initial);
  fixture.apply.mockResolvedValueOnce(renamed);

  const load = fixture.provider.loadCatalog.bind(fixture.provider);
  let release: ((value: Awaited<ReturnType<typeof load>>) => void) | undefined;
  const old = await load();
  const listing = vi.fn(load);
  fixture.provider.loadCatalog = listing;
  window.__CASSINI_VIEWER_BASE__ = location.origin + "/";
  app = mount(App, { target: host, props: { dataProvider: fixture.provider } });
  await page.getByRole("button", { name: /^Annotation test Mon/ }).click();
  listing.mockImplementationOnce(() => new Promise((resolve) => { release = resolve; }));
  window.dispatchEvent(new Event("focus"));
  await expect.poll(() => !!release).toBe(true);

  await page.getByRole("button", { name: "Rename meeting", exact: true }).click();
  await page.getByRole("textbox", { name: "Meeting title" }).fill("Renamed meeting");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect.element(page.getByRole("textbox", { name: "Meeting title" })).not.toBeInTheDocument();
  await expect.element(page.getByRole("button", { name: /^Renamed meeting/ })).toBeInTheDocument();

  release!(old);
  await expect.element(page.getByRole("button", { name: /^Renamed meeting/ })).toBeInTheDocument();
  await expect.element(page.getByRole("button", { name: /^Annotation test Mon/ })).not.toBeInTheDocument();
});

it.each(["Save", "Reset"])("can tag the same open meeting after a title %s", async (action) => {
  // Start fully synchronized: an existing pending-save poll must not repair
  // the session behind the test's back.
  const initial = fixture.snapshot();
  initial.sync = { state: "saved", desired: 1, confirmed: 1 };
  if (action === "Reset") initial.annotations!.meeting = {
    title: "Annotation test", setAtUtc: "2026-10-20T12:00:00Z",
    actor: { kind: "person", id: "ana" },
  };
  const renamed = structuredClone(initial);
  renamed.revision = renamed.annotations!.revision = 2;
  renamed.stateToken = "epoch:2";
  renamed.sync = { state: "saved", desired: 2, confirmed: 2 };
  renamed.annotations!.meeting = action === "Reset" ? null : {
    title: "Renamed meeting", setAtUtc: "2026-10-21T12:00:00Z",
    actor: { kind: "person", id: "ana" },
  };
  const tagged = structuredClone(renamed);
  tagged.revision = tagged.annotations!.revision = 3;
  tagged.stateToken = "epoch:3";
  tagged.sync = { state: "saved", desired: 3, confirmed: 3 };
  tagged.annotations!.tags = [focusTag];
  tagged.annotations!.items = [savedMark("i-focus", focusTag.id, { kind: "meeting" })];

  fixture.load.mockResolvedValue(initial);
  fixture.apply.mockResolvedValueOnce(renamed).mockImplementationOnce(async (_entry, request) => {
    if (request.stateToken !== renamed.stateToken) throw new AnnotationError(409, "revision-conflict");
    return tagged;
  });
  app = mount(App, { target: host, props: { dataProvider: fixture.provider } });
  await page.getByRole("button", { name: /^Annotation test Mon/ }).click();
  const tags = page.getByRole("group", { name: "Tags on the whole meeting" });
  await expect.element(tags.getByRole("button", { name: "Add tag", exact: true })).toBeEnabled();
  await page.getByRole("button", { name: "Rename meeting", exact: true }).click();
  await page.getByRole("textbox", { name: "Meeting title" }).fill("Renamed meeting");
  await page.getByRole("button", { name: action, exact: true }).click();
  await expect.element(page.getByRole("textbox", { name: "Meeting title" })).not.toBeInTheDocument();
  expect(fixture.apply.mock.calls[0][1].ops).toEqual([
    { op: "set-title", title: action === "Reset" ? "" : "Renamed meeting" },
  ]);

  await tags.getByRole("button", { name: "Add tag", exact: true }).click();
  await page.getByRole("combobox", { name: "Find or create a tag" }).fill("Focus");
  await userEvent.keyboard("{Enter}");
  await expect.poll(() => fixture.apply.mock.calls.length).toBe(2);
  expect(fixture.apply.mock.calls[1][1].stateToken).toBe("epoch:2");
  await expect.element(tags.getByRole("button", { name: "Focus tag options", exact: true })).toBeVisible();
  await expect.element(tags.getByText("Saving annotations…", { exact: true })).not.toBeInTheDocument();
});
