import { mount, unmount } from "svelte";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { page, userEvent } from "vitest/browser";
import App from "../../App.svelte";
import "../../app.css";
import { annotationFixture, focusTag, savedMark } from "./annotations.fixture";

let fixture: ReturnType<typeof annotationFixture>;
let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;
let root: HTMLElement | ShadowRoot;
let originalUrl: string;

beforeEach(() => {
  originalUrl = location.href;
  history.replaceState(null, "", location.pathname + location.search);
  host = document.createElement("div");
  host.style.height = "100vh";
  document.body.append(host);
  root = host;
});

afterEach(async () => {
  if (app) await unmount(app);
  app = undefined;
  fixture?.dispose();
  document.getSelection()?.removeAllRanges();
  host.remove();
  history.replaceState(null, "", originalUrl);
});

function mountApp() {
  app = mount(App, { target: root, props: { dataProvider: fixture.provider } });
}
const header = () => page.getByRole("group", { name: "Tags on the whole meeting" });
const openMeeting = () => page.getByRole("button", { name: /^Annotation test Mon/ }).click();
const closeMeeting = () => page.getByLabelText("Meeting view", { exact: true })
  .getByRole("button", { name: "Close the meeting", exact: true }).click();
async function pickTag(label: string) {
  await page.getByRole("combobox", { name: "Find or create a tag" }).fill(label);
  await userEvent.keyboard("{Enter}");
}
const archivePending = () => expect.element(header().getByText("Tags saved · updating recording…", { exact: true })).toBeVisible();

describe("annotation controls with delayed saves", () => {
  it("renders a whole-meeting tag immediately, retains it on reopen, and queues its removal", async () => {
    fixture = annotationFixture();
    mountApp();
    await openMeeting();
    await header().getByRole("button", { name: "Add tag", exact: true }).click();
    await pickTag("Immediate");
    await expect.element(header().getByRole("button", { name: "Immediate tag options", exact: true })).toBeVisible();
    await expect.element(header().getByRole("button", { name: "Add tag", exact: true })).toBeEnabled();
    await expect.element(header().getByText("Saving annotations…", { exact: true })).toBeVisible();
    expect(fixture.snapshot().annotations!.items).toEqual([]);

    await closeMeeting();
    await openMeeting();
    await header().getByRole("button", { name: "Immediate tag options", exact: true }).click();
    await page.getByRole("menuitem", { name: "Remove Immediate" }).click();
    await expect.element(header().getByRole("button", { name: "Immediate tag options", exact: true })).not.toBeInTheDocument();
    expect(fixture.apply).toHaveBeenCalledTimes(1);
    expect(fixture.apply.mock.calls[0][1].ops).toEqual([
      { op: "mark", tag: { label: "Immediate" }, target: { kind: "meeting" } },
    ]);
    const tag = { id: "t-immediate", label: "Immediate", color: "teal", icon: "" };
    await fixture.acknowledge([savedMark("i-whole", tag.id, { kind: "meeting" })], [tag]);
    await expect.poll(() => fixture.apply.mock.calls.length).toBe(2);
    expect(fixture.apply.mock.calls[1][1].ops).toEqual([
      { op: "unmark-tag", tagId: tag.id, target: { kind: "meeting" } },
    ]);
    await fixture.acknowledge([]);
    await archivePending();
  });

  it("moves then removes a region before acknowledgement, using its confirmed ID for removal", async () => {
    fixture = annotationFixture([
      savedMark("i-initial", focusTag.id, { kind: "time-range", startMs: 1000, endMs: 3000 }),
    ], [focusTag]);
    mountApp();
    await openMeeting();
    await page.getByRole("button", { name: /^Focus, .*marked by ana$/ }).click();
    await expect.element(page.getByRole("slider", { name: "Where the section ends" })).not.toBeInTheDocument();
    await expect.element(page.getByRole("button", { name: "Remove", exact: true })).not.toBeInTheDocument();
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    page.getByRole("slider", { name: "Where the section ends" }).element().focus();
    await userEvent.keyboard("{ArrowRight}");
    await page.getByRole("button", { name: /^Save changes/ }).click();
    await expect.element(page.getByRole("button", { name: /^Save changes/ })).not.toBeInTheDocument();
    await page.getByRole("button", { name: /^Focus, .*marked by You$/ }).click();
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    await page.getByRole("button", { name: "Remove", exact: true }).click();
    await expect.element(page.getByRole("button", { name: /^Focus, .*marked by/ })).not.toBeInTheDocument();
    expect(fixture.apply).toHaveBeenCalledTimes(1);
    expect(fixture.apply.mock.calls[0][1].ops).toEqual([
      { op: "unmark", itemId: "i-initial" },
      { op: "mark", tag: { id: focusTag.id, label: "Focus" },
        target: { kind: "time-range", startMs: 1000, endMs: 4000 } },
    ]);
    await fixture.acknowledge([
      savedMark("i-moved", focusTag.id, { kind: "time-range", startMs: 1000, endMs: 4000 }),
    ]);
    await expect.poll(() => fixture.apply.mock.calls.length).toBe(2);
    expect(fixture.apply.mock.calls[1][1].ops).toEqual([{ op: "unmark", itemId: "i-moved" }]);
    await fixture.acknowledge([]);
    await archivePending();
  });

  it.each(["document", "shadow root"])("saves a text selection as a region and reloads it in a fresh app (%s)", async (surface) => {
    if (surface === "shadow root") {
      root = host.attachShadow({ mode: "open" });
      // Like the embedded shell, put the actual compiled styles inside its root.
      for (const style of document.head.querySelectorAll("style")) root.append(style.cloneNode(true));
    }
    fixture = annotationFixture();
    mountApp();
    await openMeeting();
    await expect.poll(() => root.querySelectorAll("[data-word-id]").length).toBe(6);
    const words = root.querySelectorAll("[data-word-id]");
    const first = words[3].firstChild!;
    const last = words[4].lastChild!;
    // Exercise the browser Selection API and the component's selectionchange
    // handler, without directly constructing an annotation request or session.
    document.getSelection()!.setBaseAndExtent(first, 0, last, last.textContent!.length);
    document.dispatchEvent(new Event("selectionchange"));
    await page.getByRole("button", { name: /^Tag selection/ }).click();
    await pickTag("New region");
    const region = () => page.getByRole("button", { name: /^New region, 0:03 to 0:05, marked by/ });
    await expect.element(region()).toBeVisible();
    await expect.element(page.getByRole("button", { name: /^Tag selection/ })).not.toBeInTheDocument();
    await expect.element(header().getByRole("button", { name: "Remove New region", exact: true })).not.toBeInTheDocument();
    await expect.poll(() => fixture.apply.mock.calls.length).toBe(1);
    expect(fixture.apply.mock.calls[0][1].ops).toEqual([
      { op: "mark", tag: { label: "New region" }, target: { kind: "time-range", startMs: 3000, endMs: 5000 } },
    ]);
    expect(fixture.snapshot().annotations!.items).toEqual([]);

    // Pending selection survives dialog destruction while the save is held.
    await closeMeeting();
    await openMeeting();
    await expect.element(region()).toBeVisible();
    const tag = { id: "t-region", label: "New region", color: "teal", icon: "" };
    await fixture.acknowledge([
      savedMark("i-region", tag.id, { kind: "time-range", startMs: 3000, endMs: 5000 }),
    ], [tag]);
    await archivePending();
    await closeMeeting();
    await openMeeting();
    await expect.element(region()).toBeVisible();
    await expect.element(header().getByRole("button", { name: "Remove New region", exact: true })).not.toBeInTheDocument();

    // Discard every optimistic session. The fresh app must read the saved
    // provider document; a retained in-memory mark cannot satisfy this check.
    await closeMeeting();
    await unmount(app!);
    app = undefined;
    fixture.load.mockClear();
    mountApp();
    await openMeeting();
    await expect.element(region()).toBeVisible();
    expect(fixture.load).toHaveBeenCalled();
    await expect.element(header().getByRole("button", { name: "Remove New region", exact: true })).not.toBeInTheDocument();
  });
});
