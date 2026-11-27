import { mount, unmount } from "svelte";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { page, userEvent } from "vitest/browser";

import MeetingView from "./MeetingView.svelte";
import SummarySentChip from "./SummarySentChip.svelte";
import "../app.css";
import { meeting, speakersFixture } from "./speakers/speakers.fixture";
import type { SummarySentTo } from "../viewer/catalog";

// #532: a meeting whose summary a webhook receiver accepted says so in its
// header, and its popover says where in one line, with the admin's reason.

const SHOTS = "../../node_modules/.cache/vitest-screenshots/summary-sent";

let view: ReturnType<typeof mount> | undefined;
let root: HTMLDivElement;
let fixture: ReturnType<typeof speakersFixture> | undefined;

function makeRoot(theme: "saturn-light" | "saturn-dark") {
  root = document.createElement("div");
  root.className = "cassini-root";
  root.dataset.theme = theme;
  root.style.height = "100vh";
  root.style.padding = "16px";
  root.style.background = "var(--color-base-100)";
  document.body.append(root);
}

beforeEach(() => makeRoot("saturn-light"));

afterEach(async () => {
  if (view) await unmount(view);
  view = undefined;
  fixture?.dispose();
  fixture = undefined;
  root.remove();
});

function mountChip(sentTo: SummarySentTo | null) {
  view = mount(SummarySentChip, { target: root, props: { sentTo } });
}

const chip = () => page.getByRole("button", { name: "Sent to flows.example.org", exact: true });
const popover = () => page.getByRole("dialog", { name: "Where this summary was sent" });

// The number of line boxes the popover's text takes.
function lineCount(element: Element): number {
  const range = document.createRange();
  range.selectNodeContents(element);
  const tops = new Set([...range.getClientRects()].map((rect) => Math.round(rect.top)));
  return tops.size;
}

describe("SummarySentChip", () => {
  it("shows nothing without a host", async () => {
    mountChip(null);
    await expect.element(page.getByTestId("summary-sent-chip")).not.toBeInTheDocument();
    if (view) await unmount(view);
    mountChip({ host: "" });
    await expect.element(page.getByTestId("summary-sent-chip")).not.toBeInTheDocument();
  });

  it("opens a one-line popover with the stock sentence and the reason as written", async () => {
    mountChip({ host: "flows.example.org", reason: "We keep them in our wiki." });
    await chip().click();
    const text = popover().getByText("A summary of this meeting was sent to flows.example.org. We keep them in our wiki.", { exact: true });
    await expect.element(text).toBeVisible();
    expect(lineCount(text.element())).toBe(1);
    await expect.element(chip()).toHaveAttribute("aria-expanded", "true");
    // A screen reader reads the sentence with the dialog's name.
    await expect.element(popover()).toHaveAccessibleDescription(
      "A summary of this meeting was sent to flows.example.org. We keep them in our wiki.",
    );
    const describedBy = popover().element().getAttribute("aria-describedby");
    expect(describedBy && document.getElementById(describedBy)).toBe(text.element());
    await page.screenshot({ path: `${SHOTS}/01-popover-light.png` });

    await userEvent.keyboard("{Escape}");
    await expect.element(popover()).not.toBeInTheDocument();
  });

  it("keeps a long reason in the same paragraph as the sentence", async () => {
    const reason = "We keep every summary in our team wiki so people who missed the call can catch up, and delete them after a year.";
    mountChip({ host: "flows.example.org", reason });
    await chip().click();
    const text = popover().getByText(`A summary of this meeting was sent to flows.example.org. ${reason}`, { exact: true });
    await expect.element(text).toBeVisible();
    expect(popover().element().querySelectorAll("p").length).toBe(1);
    expect(popover().element().querySelector("br")).toBeNull();
  });

  it("leaves the reason out when there is none", async () => {
    mountChip({ host: "flows.example.org" });
    await chip().click();
    await expect.element(popover().getByText("A summary of this meeting was sent to flows.example.org.", { exact: true })).toBeVisible();
  });

  it("is highlighted in light and dark, unlike the plain chips", async () => {
    for (const theme of ["saturn-light", "saturn-dark"] as const) {
      if (view) await unmount(view);
      root.remove();
      makeRoot(theme);
      mountChip({ host: "flows.example.org" });
      const style = getComputedStyle(chip().element());
      expect(style.backgroundColor).not.toBe("rgba(0, 0, 0, 0)");
      expect(style.borderTopColor).not.toBe(getComputedStyle(root).getPropertyValue("--color-base-300"));
      await page.screenshot({ path: `${SHOTS}/02-chip-${theme}.png` });
    }
  });
});

describe("in a meeting's header", () => {
  it("sits beside the meeting's details chip when the summary was sent", async () => {
    fixture = speakersFixture();
    view = mount(MeetingView, {
      target: root,
      props: {
        dataProvider: fixture.provider,
        meeting: { ...meeting, summarySentTo: { host: "flows.example.org", reason: "We keep summaries in our team wiki." } },
        surface: "app", bundled: false, isDesktop: true,
        speakerEditsPollMs: 20, speakerEditsClock: fixture.time.clock, summaryRequestPollMs: 20,
      },
    });
    // Loading the meeting can be slow on a busy machine.
    await expect.element(page.getByText("Loading meeting…"), { timeout: 10_000 }).not.toBeInTheDocument();
    const details = page.getByRole("button", { name: "Meeting details" });
    await expect.element(details).toBeVisible();
    await expect.element(chip()).toBeVisible();
    // Same row as the facts chip.
    const factsBox = details.element().getBoundingClientRect();
    const chipBox = chip().element().getBoundingClientRect();
    expect(Math.abs(factsBox.top - chipBox.top)).toBeLessThan(4);
    await chip().click();
    await expect.element(popover()).toBeVisible();
    await page.screenshot({ path: `${SHOTS}/03-meeting-header.png` });
  });
});
