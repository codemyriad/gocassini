import { afterEach, describe, expect, it } from "vitest";

import { isEmbeddedViewer, readViewerBase } from "./appBase";

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

// isEmbeddedViewer answers "am I inside the ExApp shell", which is a different
// question from ncMode (Boolean(OCA.Theming.primaryColor)). Keying the
// catalog/bundled decision on ncMode left an ExApp with Theming disabled
// falling back to a bundled artifact that does not exist there, with no
// recovery — the defect behind D-543.
describe("isEmbeddedViewer", () => {
  const originalWindow = globalThis.window;

  afterEach(() => {
    globalThis.window = originalWindow;
  });

  function setWindow(base: unknown) {
    globalThis.window = {
      location: { href: "http://127.0.0.1:8765/some/page/" },
      __CASSINI_VIEWER_BASE__: base,
    } as unknown as Window & typeof globalThis;
  }

  it("is false outside the embedded build", () => {
    setWindow(undefined);
    expect(isEmbeddedViewer()).toBe(false);
  });

  it("is false when the base is an empty string", () => {
    setWindow("");
    expect(isEmbeddedViewer()).toBe(false);
  });

  it("is true when the embedded build injected a proxy base", () => {
    setWindow("/index.php/apps/app_api/proxy/gocassini/");
    expect(isEmbeddedViewer()).toBe(true);
  });

  it("does not depend on Nextcloud Theming being present", () => {
    // The whole point: no OCA global is consulted, so an ExApp whose Theming
    // app is off is still recognised as embedded.
    setWindow("https://cloud.example.com/proxy/gocassini/");
    expect(isEmbeddedViewer()).toBe(true);
  });
});
