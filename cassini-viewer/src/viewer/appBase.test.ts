import { afterEach, describe, expect, it } from "vitest";

import { readViewerBase } from "./appBase";

// readViewerBase is the single shared resolver for the AppAPI proxy base that
// used to be copy-pasted into both catalog.ts and loadArtifact.ts (D-415).
// These tests pin the exact behaviour both call sites depend on.
describe("readViewerBase", () => {
  const originalWindow = globalThis.window;

  afterEach(() => {
    globalThis.window = originalWindow;
  });

  function setWindow(base: unknown, href = "http://127.0.0.1:8765/some/page/") {
    globalThis.window = {
      location: { href },
      __CASSINI_VIEWER_BASE__: base,
    } as unknown as Window & typeof globalThis;
  }

  it("returns empty string when the base is absent", () => {
    setWindow(undefined);
    expect(readViewerBase()).toBe("");
  });

  it("returns empty string when the base is an empty string", () => {
    setWindow("");
    expect(readViewerBase()).toBe("");
  });

  it("returns empty string when the base is not a string", () => {
    setWindow(42);
    expect(readViewerBase()).toBe("");
  });

  it("resolves a proxy base relative to window.location.href into an absolute URL", () => {
    setWindow("/index.php/apps/app_api/proxy/gocassini/");
    expect(readViewerBase()).toBe("http://127.0.0.1:8765/index.php/apps/app_api/proxy/gocassini/");
  });

  it("returns an already-absolute base unchanged", () => {
    setWindow("https://cloud.example.com/proxy/gocassini/");
    expect(readViewerBase()).toBe("https://cloud.example.com/proxy/gocassini/");
  });
});
