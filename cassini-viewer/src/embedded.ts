// Embedded entry for the AppAPI-registered ui/script.
//
// AppAPI renders the embedded page (/index.php/apps/app_api/embedded/
// gocassini/viewer) as a bare <div id="content"></div> and loads the script
// the ExApp registered for that top-menu entry as a normal, Nextcloud-nonce'd
//   <script defer src=".../proxy/gocassini/ui/viewer.js"></script>
// The page CSP is `script-src-elem 'strict-dynamic' 'nonce-…'`, so this
// already-trusted script runs, but any further <script src> it writes into the
// HTML would NOT (strict-dynamic ignores host-allowlist / unsafe-inline). We
// therefore mount the SPA directly from this bundle — no iframe, no injected
// <script src>.
//
// Because AppAPI loads us with `defer`, document.currentScript is null by the
// time module/IIFE code runs. To recover the proxy base we scan
// document.scripts for our own /ui/viewer.js src. The capture runs at
// top-level synchronous eval (before mount) so catalog.ts / loadArtifact.ts
// see window.__CASSINI_VIEWER_BASE__ during onMount.

import { mount } from "svelte";
import App from "./App.svelte";
import "./app.css";

// VIEWER_JS_SRC_PATTERN matches the registered ui/viewer.js src and captures
// the AppAPI proxy base. Exported for the unit test so the test and the
// runtime agree on the exact shape.
//   ".../index.php/apps/app_api/proxy/gocassini/ui/viewer.js" -> base
//   "   …/index.php/apps/app_api/proxy/gocassini/"
export const VIEWER_JS_SRC_PATTERN = /^(.*)\/ui\/viewer\.js(?:\?.*)?$/;

// captureViewerBaseFrom scans the given script list for the registered
// ui/viewer.js entry and returns the proxy base (with trailing slash), or
// null when not found. Pure (no globals) so it is unit-testable in node.
export function captureViewerBaseFrom(
  scripts: ArrayLike<{ src?: string }> | null | undefined,
): string | null {
  if (!scripts) {
    return null;
  }
  for (let i = 0; i < scripts.length; i += 1) {
    const src = scripts[i]?.src;
    if (!src) {
      continue;
    }
    const match = VIEWER_JS_SRC_PATTERN.exec(src);
    if (match) {
      return match[1] + "/";
    }
  }
  return null;
}

// captureViewerBase captures the AppAPI proxy base synchronously, at module
// eval, by scanning document.scripts (currentScript is null under `defer`) and
// sets window.__CASSINI_VIEWER_BASE__ so catalog.ts / loadArtifact.ts see it.
export function captureViewerBase(doc: Document, win: Window): void {
  const base = captureViewerBaseFrom(doc.scripts);
  if (base) {
    win.__CASSINI_VIEWER_BASE__ = base;
  }
}

// ensureAppRoot returns the #app element the SPA mounts into, creating it
// inside the embedded template's <div id="content"> (falling back to <body>
// if the host markup ever changes). Pure w.r.t. the passed document.
export function ensureAppRoot(doc: Document): HTMLElement {
  let appRoot = doc.getElementById("app");
  if (appRoot) {
    return appRoot;
  }
  const host = doc.getElementById("content") ?? doc.body;
  appRoot = doc.createElement("div");
  appRoot.id = "app";
  host.appendChild(appRoot);
  return appRoot;
}

function mountEmbeddedViewer(): void {
  mount(App, { target: ensureAppRoot(document) });
}

function bootstrap(): void {
  captureViewerBase(document, window);
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", mountEmbeddedViewer, { once: true });
  } else {
    mountEmbeddedViewer();
  }
}

// Only run the bootstrap in a real browser/DOM. Guard so importing this module
// in a node unit test (to exercise the exported pure helpers) does not crash on
// the svelte mount. import.meta.env.VITEST is set by Vitest.
if (typeof document !== "undefined" && !import.meta.env?.VITEST) {
  bootstrap();
}
