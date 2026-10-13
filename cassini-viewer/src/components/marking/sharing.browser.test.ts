import { mount, unmount } from "svelte";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import App from "../../App.svelte";
import MeetingView from "../MeetingView.svelte";
import { readViewerRange } from "../../viewer/hashRouting";
import "../../app.css";
import { annotationFixture, focusTag, savedMark } from "./annotations.fixture";

let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;
let fixture: ReturnType<typeof annotationFixture>;
let originalUrl: string;
let clipboard: PropertyDescriptor | undefined;
const writeText = vi.fn(async (_text: string) => {});
const selected = () => [...host.querySelectorAll('[data-word-id][data-sel]')].map(el => el.textContent?.trim());

beforeEach(() => {
  originalUrl = location.href;
  clipboard = Object.getOwnPropertyDescriptor(navigator, "clipboard");
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
  writeText.mockClear();
  host = document.createElement("div");
  host.style.height = "100vh";
  document.body.append(host);
  fixture = annotationFixture();
});
afterEach(async () => {
  if (app) await unmount(app);
  app = undefined;
  fixture.dispose();
  host.remove();
  document.getSelection()?.removeAllRanges();
  history.replaceState(null, "", originalUrl);
  if (clipboard) Object.defineProperty(navigator, "clipboard", clipboard);
  else Reflect.deleteProperty(navigator, "clipboard");
  await page.viewport(1440, 1000);
});
function open(hash = "#meeting=m1&range=1000,4000") {
  history.replaceState(null, "", location.pathname + location.search + hash);
  app = mount(App, { target: host, props: { dataProvider: fixture.provider } });
}
async function navigate(hash: string) {
  history.pushState(null, "", hash);
  window.dispatchEvent(new PopStateEvent("popstate"));
}

it.each([1440, 390])("restores, edits, copies and reopens an untagged selection at width %s", async (width) => {
  await page.viewport(width, 900);
  open();
  await expect.poll(selected).toEqual(["second", "third", "fourth"]);
  expect(location.hash).toBe("#meeting=m1&range=1000,4000");
  await expect.element(page.getByRole("button", { name: /^Tag selection/ })).toBeVisible();
  page.getByRole("slider", { name: "Where the section ends" }).element().focus();
  await userEvent.keyboard("{ArrowRight}");
  await expect.poll(selected).toEqual(["second", "third", "fourth", "fifth"]);
  await page.getByRole("button", { name: "Copy link", exact: true }).click();
  await expect.poll(() => writeText.mock.calls.length).toBe(1);
  const url = new URL(writeText.mock.calls[0][0]);
  expect(readViewerRange(url.hash)).toEqual({ startMs: 1000, endMs: 5000 });
  expect(location.hash).toBe("#meeting=m1&range=1000,4000");
  expect(fixture.apply).not.toHaveBeenCalled();
  await page.getByRole("button", { name: /^Cancel/ }).click();
  await expect.poll(selected).toEqual([]);
  await navigate(url.hash);
  await expect.poll(selected).toEqual(["second", "third", "fourth", "fifth"]);
  await navigate("#meeting=m2");
  await expect.poll(selected).toEqual([]);
  await navigate("#meeting=m1&range=0,1000");
  await expect.poll(selected).toEqual(["First"]);
});

it("allows a reader with no annotation API to select and share without tag controls", async () => {
  delete fixture.provider.loadMeetingAnnotations;
  delete fixture.provider.applyAnnotationOps;
  open();
  await expect.poll(selected).toEqual(["second", "third", "fourth"]);
  await expect.element(page.getByRole("button", { name: /^Tag selection/ })).not.toBeInTheDocument();
  await page.getByRole("button", { name: "Copy link", exact: true }).click();
  await expect.poll(() => writeText.mock.calls.length).toBe(1);
  await page.getByRole("button", { name: /^Cancel/ }).click();
  const words = host.querySelectorAll<HTMLElement>("[data-word-id]");
  document.getSelection()!.setBaseAndExtent(words[0].firstChild!, 0, words[1].lastChild!, words[1].lastChild!.textContent!.length);
  document.dispatchEvent(new Event("selectionchange"));
  await expect.poll(selected).toEqual(["First", "second"]);
  await expect.element(page.getByRole("button", { name: "Copy link", exact: true })).toBeVisible();
  expect(fixture.apply).not.toHaveBeenCalled();
});

it("copies a marked range without changing annotations and offers a clipboard fallback", async () => {
  fixture = annotationFixture([savedMark("i1", focusTag.id, { kind: "time-range", startMs: 1000, endMs: 3000 })], [focusTag]);
  open("#meeting=m1");
  await page.getByRole("button", { name: /^Focus, .*marked by ana$/ }).click();
  writeText.mockRejectedValueOnce(new Error("Clipboard unavailable"));
  await page.getByRole("button", { name: "Copy link", exact: true }).click();
  await expect.element(page.getByRole("textbox", { name: "Selection link" })).toBeVisible();
  const url = new URL((page.getByRole("textbox", { name: "Selection link" }).element() as HTMLInputElement).value);
  expect(readViewerRange(url.hash)).toEqual({ startMs: 1000, endMs: 3000 });
  expect(fixture.apply).not.toHaveBeenCalled();
});

it("waits for a delayed transcript, then respects clearing the selection", async () => {
  const load = fixture.provider.loadMeetingForEntry;
  let release!: () => void;
  const gate = new Promise<void>(resolve => { release = resolve; });
  fixture.provider.loadMeetingForEntry = async entry => { await gate; return load(entry); };
  open();
  expect(selected()).toEqual([]);
  release();
  await expect.poll(selected).toEqual(["second", "third", "fourth"]);
  await page.getByRole("button", { name: /^Cancel/ }).click();
  window.dispatchEvent(new HashChangeEvent("hashchange"));
  await expect.poll(selected).toEqual([]);
  await expect.element(page.getByRole("button", { name: "Copy link", exact: true })).not.toBeInTheDocument();
});

it.each(["-1,2000", "4000,1000", "9000,10000"])("ignores an unusable range %s", async range => {
  open(`#meeting=m1&range=${range}`);
  await expect.poll(() => host.querySelectorAll("[data-word-id]").length).toBe(6);
  expect(selected()).toEqual([]);
  await expect.element(page.getByRole("button", { name: "Copy link", exact: true })).not.toBeInTheDocument();
});

it("supports bundled standalone recordings while leaving host-page embeds alone", async () => {
  history.replaceState(null, "", "#range=1000,3000");
  app = mount(MeetingView, { target: host, props: { dataProvider: fixture.provider, bundled: true } });
  await expect.poll(selected).toEqual(["second", "third"]);
  await unmount(app);
  app = mount(MeetingView, { target: host, props: { dataProvider: fixture.provider, bundled: true, surface: "embed" } });
  await expect.poll(() => host.querySelectorAll("[data-word-id]").length).toBe(6);
  expect(selected()).toEqual([]);
  await expect.element(page.getByRole("button", { name: "Copy link", exact: true })).not.toBeInTheDocument();
  expect(location.hash).toBe("#range=1000,3000");
});
