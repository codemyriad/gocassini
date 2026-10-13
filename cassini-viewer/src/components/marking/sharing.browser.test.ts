import { mount, unmount } from "svelte";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import App from "../../App.svelte";
import MeetingView from "../MeetingView.svelte";
import { buildTranscriptIndex } from "../../core/transcript";
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

it("restores the requested transcript and returns to the default through history", async () => {
  const originalLoad = fixture.provider.loadMeetingForEntry;
  const catalog = await fixture.provider.loadCatalog();
  const base = await originalLoad(catalog!.meetings[0]);
  const availableTranscripts = ["original", "alternate"].map(id => ({ id, label: id, description: "", isDefault: id === "original" }));
  const loadCatalog = fixture.provider.loadCatalog;
  fixture.provider.loadCatalog = async () => {
    const catalog = await loadCatalog();
    return { ...catalog!, meetings: catalog!.meetings.map(entry => ({ ...entry, meetingPath: `${entry.id}.meeting` })) };
  };
  fixture.provider.loadMeetingForEntry = async () => ({ ...base, availableTranscripts, currentTranscriptId: "original" });
  const alternate = structuredClone(base.transcript);
  for (const segment of alternate.segments) {
    segment.text = segment.text.toUpperCase();
    for (const word of segment.words ?? []) word.text = word.text.toUpperCase();
  }
  fixture.provider.switchTranscript = vi.fn(async (_entry, id) => ({
    ...base, availableTranscripts, currentTranscriptId: id,
    transcript: id === "alternate" ? alternate : base.transcript,
    index: buildTranscriptIndex(id === "alternate" ? alternate : base.transcript),
  }));
  open("#meeting=m1&tx=alternate&range=1000,3000");
  await expect.poll(selected).toEqual(["SECOND", "THIRD"]);
  await page.getByRole("button", { name: "Copy link", exact: true }).click();
  expect(new URL(writeText.mock.calls[0][0]).hash).toContain("tx=alternate");
  await navigate("#meeting=m1&range=3000,5000");
  await expect.poll(selected).toEqual(["fourth", "fifth"]);
  expect(fixture.provider.switchTranscript).toHaveBeenLastCalledWith(expect.objectContaining({ id: "m1" }), "original");
  history.back();
  await expect.poll(selected).toEqual(["SECOND", "THIRD"]);
  history.forward();
  await expect.poll(selected).toEqual(["fourth", "fifth"]);
  await navigate("#meeting=m1&tx=missing&range=0,1000");
  await expect.poll(selected).toEqual(["First"]);
  expect(location.hash).toBe("#meeting=m1&range=0,1000");
});


it("does not let an older meeting load replace a newly opened selection", async () => {
  const load = fixture.provider.loadMeetingForEntry;
  let release!: () => void;
  const gate = new Promise<void>(resolve => { release = resolve; });
  fixture.provider.loadMeetingForEntry = async entry => {
    if (entry.id === "m1") await gate;
    return load(entry);
  };
  open();
  await navigate("#meeting=m2&range=4000,6000");
  await expect.poll(selected).toEqual(["fifth", "sixth"]);
  release();
  await new Promise(resolve => setTimeout(resolve, 100));
  expect(selected()).toEqual(["fifth", "sixth"]);
  await page.getByRole("button", { name: "Copy link", exact: true }).click();
  expect(new URL(writeText.mock.calls[0][0]).hash).toBe("#meeting=m2&range=4000,6000");
});

it("seeks to the selection start without autoplay and retains timestamp links", async () => {
  // Six seconds of silent PCM, so the real media element can load and seek.
  const bytes = new Uint8Array(44 + 6 * 8000 * 2);
  const view = new DataView(bytes.buffer);
  const text = (at: number, value: string) => bytes.set(new TextEncoder().encode(value), at);
  text(0, "RIFF"); view.setUint32(4, bytes.length - 8, true);
  text(8, "WAVEfmt "); view.setUint32(16, 16, true);
  view.setUint16(20, 1, true); view.setUint16(22, 1, true);
  view.setUint32(24, 8000, true); view.setUint32(28, 16000, true);
  view.setUint16(32, 2, true); view.setUint16(34, 16, true);
  text(36, "data"); view.setUint32(40, bytes.length - 44, true);
  const url = URL.createObjectURL(new Blob([bytes], { type: "audio/wav" }));
  const load = fixture.provider.loadMeetingForEntry;
  fixture.provider.loadMeetingForEntry = async entry => ({ ...await load(entry), audioSrc: url });
  try {
    open("#meeting=m1&range=2000,4000");
    await expect.poll(selected).toEqual(["third", "fourth"]);
    await expect.poll(() => host.querySelector("audio")?.currentTime).toBe(2);
    expect(host.querySelector("audio")?.paused).toBe(true);
    await navigate("#meeting=m1&t=4500ms");
    await expect.poll(() => host.querySelector("audio")?.currentTime).toBe(4.5);
    expect(selected()).toEqual([]);
  } finally {
    URL.revokeObjectURL(url);
  }
});
