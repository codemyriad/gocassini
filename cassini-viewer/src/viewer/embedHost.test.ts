import { describe, expect, it } from "vitest";

import meetingViewSource from "../components/MeetingView.svelte?raw";
import { eventIsInside, followScrollTop, ownsLocationHash } from "./embedHost";

const root = { id: "viewer" } as unknown as EventTarget;
const inViewer = { composedPath: () => [{}, root, {}] as EventTarget[] };
const onHostPage = { composedPath: () => [{}, {}] as EventTarget[] };

describe("eventIsInside", () => {
  it("claims a key pressed inside the viewer, across its shadow root", () => {
    expect(eventIsInside(inViewer, root)).toBe(true);
  });

  it("leaves a key pressed on the host page to the host page", () => {
    // The embed used to take Space on the whole page, so a reader of an article
    // with a recording in it could not scroll the article with the keyboard.
    expect(eventIsInside(onHostPage, root)).toBe(false);
  });

  it("claims nothing before the viewer has mounted", () => {
    expect(eventIsInside(inViewer, undefined)).toBe(false);
  });
});

describe("followScrollTop", () => {
  const pane = { top: 100, bottom: 400 };

  it("leaves the pane alone while the playing turn is in view", () => {
    expect(followScrollTop(pane, { top: 200, bottom: 260 }, 500)).toBeNull();
  });

  it("brings a turn below the fold to a third of the way down", () => {
    expect(followScrollTop(pane, { top: 450, bottom: 500 }, 500)).toBe(500 + 350 - 100);
  });

  it("brings a turn above the pane back, and never scrolls past the top", () => {
    expect(followScrollTop(pane, { top: 40, bottom: 90 }, 300)).toBe(300 + 40 - 100 - 100);
    expect(followScrollTop(pane, { top: -500, bottom: -450 }, 100)).toBe(0);
  });

  it("treats a turn inside the margin as out of view", () => {
    expect(followScrollTop(pane, { top: 110, bottom: 150 }, 0)).not.toBeNull();
    expect(followScrollTop(pane, { top: 300, bottom: 390 }, 0)).not.toBeNull();
  });
});

describe("ownsLocationHash", () => {
  it("gives the app its hash route and leaves an embed's host page its anchors", () => {
    expect(ownsLocationHash("app")).toBe(true);
    expect(ownsLocationHash("embed")).toBe(false);
  });
});

describe("MeetingView as an embed", () => {
  it("asks before taking Space, and only as an embed", () => {
    const handler = meetingViewSource.slice(meetingViewSource.indexOf("function handleWindowKeydown"));
    expect(handler.slice(0, handler.indexOf("\n  }\n"))).toContain(
      'if (surface === "embed" && !eventIsInside(event, viewRootEl)) {',
    );
  });

  it("scrolls its own pane, not the page, as an embed", () => {
    const scroll = meetingViewSource.slice(meetingViewSource.indexOf("async function scrollSegmentIntoView"));
    const body = scroll.slice(0, scroll.indexOf("\n  }\n"));
    expect(body).toContain('if (surface === "embed")');
    expect(body).toContain("followScrollTop(");
    // Inside the app the viewer is the page, and scrollIntoView stays right.
    expect(body).toContain('element?.scrollIntoView({ behavior, block: "center" });');
  });

  it("offers no way back to a meeting list as an embed, at any width", () => {
    // Narrower than 720px the embed counts as not-desktop, and showed the app's
    // "Back to meeting list" arrow, which did nothing on a page with no list.
    expect(meetingViewSource).toContain('{#if !isDesktop && !inSheet && surface !== "embed"}');
  });

  it("is not the page's <main> landmark as an embed", () => {
    expect(meetingViewSource).toContain('<svelte:element this={surface === "embed" ? "div" : "main"}');
  });

  it("reports audio it cannot play instead of failing silently", () => {
    expect(meetingViewSource).toContain("playbackerror: string;");
    expect(meetingViewSource).toContain("void audioEl.play().catch(");
    expect(meetingViewSource).toContain("on:error={handleAudioError}");
  });

  it("reads the page's fragment only through hostHash, and writes it only when it owns it", () => {
    // One raw read, inside hostHash itself; every other read goes through it.
    expect(meetingViewSource.match(/window\.location\.hash/g)).toHaveLength(1);
    expect(meetingViewSource).toContain("return ownsHash ? window.location.hash : \"\";");
    for (const writer of ["function writeTranscriptUrlParam", "function clearTranscriptUrlParam"]) {
      const body = meetingViewSource.slice(meetingViewSource.indexOf(writer));
      expect(body.slice(0, body.indexOf("\n  }\n"))).toContain("if (!ownsHash) return;");
    }
    expect(meetingViewSource.match(/window\.history\.replaceState/g)).toHaveLength(2);
  });
});
