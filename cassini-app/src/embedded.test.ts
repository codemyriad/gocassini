import { describe, expect, it } from "vitest";

import {
  captureOperatorBase,
  neutralizeNestedContentChrome,
  operatorBaseFrom,
  retireAbandonedCaptureState,
} from "./embedded";

describe("operatorBaseFrom", () => {
  it("appends /operator to the captured proxy base", () => {
    expect(operatorBaseFrom("/index.php/apps/app_api/proxy/gocassini/")).toBe(
      "/index.php/apps/app_api/proxy/gocassini/operator",
    );
  });

  it("tolerates a base without a trailing slash", () => {
    expect(operatorBaseFrom("/proxy/gocassini")).toBe("/proxy/gocassini/operator");
  });

  it("returns null when no base was captured (standalone / dev)", () => {
    expect(operatorBaseFrom(null)).toBeNull();
    expect(operatorBaseFrom(undefined)).toBeNull();
    expect(operatorBaseFrom("")).toBeNull();
  });
});

describe("captureOperatorBase", () => {
  it("publishes operatorBasePath from the viewer base, preserving other config", () => {
    const win = {
      __CASSINI_VIEWER_BASE__: "/proxy/gocassini/",
      __CASSINI_CONFIG__: { operatorBasePath: "/stale" },
    } as unknown as Window;
    captureOperatorBase(win);
    expect(win.__CASSINI_CONFIG__?.operatorBasePath).toBe("/proxy/gocassini/operator");
  });

  it("is a no-op when no viewer base was captured", () => {
    const win = {} as unknown as Window;
    captureOperatorBase(win);
    expect(win.__CASSINI_CONFIG__).toBeUndefined();
  });
});

describe("neutralizeNestedContentChrome", () => {
  // Only inline-style assignment and identity/length checks are exercised, so a
  // plain { style: {} } stub is enough — no DOM environment needed.
  function contentStub(): HTMLElement {
    return { style: {} } as unknown as HTMLElement;
  }

  it("no-ops when there is only one #content (no duplication to undo)", () => {
    const only = contentStub();
    expect(neutralizeNestedContentChrome([only], only)).toBe(false);
    expect(only.style.position).toBeUndefined();
  });

  it("no-ops when the mount target is the outer #content itself", () => {
    const outer = contentStub();
    const inner = contentStub();
    expect(neutralizeNestedContentChrome([outer, inner], outer)).toBe(false);
    expect(outer.style.position).toBeUndefined();
  });

  it("fills the nested #content and leaves the outer untouched", () => {
    const outer = contentStub();
    const inner = contentStub();
    expect(neutralizeNestedContentChrome([outer, inner], inner)).toBe(true);
    expect(inner.style.position).toBe("absolute");
    expect(inner.style.inset).toBe("0");
    expect(inner.style.margin).toBe("0");
    expect(inner.style.width).toBe("auto");
    expect(inner.style.height).toBe("auto");
    expect(inner.style.borderRadius).toBe("0");
    expect(outer.style.position).toBeUndefined();
  });
});

describe("retireAbandonedCaptureState", () => {
  // The Cassini page is the other place a leftover opt-in can be reached, and
  // for many people the likelier one: somebody who stopped joining calls still
  // opens the archive. That key is a recorded answer to a question this build
  // no longer asks, and docs/privacy.md tells administrators no such answer is
  // kept anywhere, so every page load deletes it.
  it("forgets an older build's opt-in without touching delivery bookkeeping", () => {
    const entries = new Map<string, string>([
      ["cassini.sourceCapture.consent", "granted"],
      ["cassini.sourceCapture.uploadAttempts", '{"capture-room-1":1}'],
      ["cassini.viewer.lastRoom", "kept"],
    ]);
    const globals = globalThis as { localStorage?: unknown };
    const original = globals.localStorage;
    globals.localStorage = {
      getItem: (key: string) => entries.get(key) ?? null,
      removeItem: (key: string) => void entries.delete(key),
    };
    try {
      retireAbandonedCaptureState();
    } finally {
      globals.localStorage = original;
    }

    expect(entries.has("cassini.sourceCapture.consent")).toBe(false);
    expect(entries.get("cassini.sourceCapture.uploadAttempts")).toBe('{"capture-room-1":1}');
    expect(entries.get("cassini.viewer.lastRoom")).toBe("kept");
  });
});
