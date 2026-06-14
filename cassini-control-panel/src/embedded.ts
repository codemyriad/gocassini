// Embedded entry for the AppAPI-registered ui/script (D-382), mirroring the
// viewer's src/embedded.ts (D-381).
//
// AppAPI renders the embedded page (/index.php/apps/app_api/embedded/
// gocassini/control-panel) as a bare <div id="content"></div> and loads the
// script the ExApp registered for that top-menu entry as a normal,
// Nextcloud-nonce'd
//   <script defer src=".../proxy/gocassini/ui/control-panel.js"></script>
// The page CSP is `script-src-elem 'strict-dynamic' 'nonce-…'`, so this
// already-trusted script runs, but any further <script src> it writes into the
// HTML would NOT (strict-dynamic ignores host-allowlist / unsafe-inline). We
// therefore mount the SPA directly from this bundle — no iframe, no injected
// <script src>.
//
// Because AppAPI loads us with `defer`, document.currentScript is null by the
// time module/IIFE code runs. To recover the proxy base we scan
// document.scripts for our own /ui/control-panel.js src and set
// window.__CASSINI_CONFIG__.operatorBasePath to "<base>operator". The control
// panel's operator/config.ts reads window.__CASSINI_CONFIG__?.operatorBasePath
// FIRST and treats it as authoritative, so this is all that's needed: the
// EventSource/fetch to /operator/* hit the proxy path rather than an
// origin-absolute /operator that would bypass the AppAPI proxy and 404. The
// capture runs at top-level synchronous eval (before mount) so config.ts sees
// it during onMount.

import { mount } from "svelte";
import App from "./App.svelte";
import "./app.css";

// CONTROL_PANEL_JS_SRC_PATTERN matches the registered ui/control-panel.js src
// and captures the AppAPI proxy base. Exported for the unit test so the test
// and the runtime agree on the exact shape.
//   ".../index.php/apps/app_api/proxy/gocassini/ui/control-panel.js" -> base
//   "   …/index.php/apps/app_api/proxy/gocassini/"
export const CONTROL_PANEL_JS_SRC_PATTERN = /^(.*)\/ui\/control-panel\.js(?:\?.*)?$/;

// captureProxyBaseFrom scans the given script list for the registered
// ui/control-panel.js entry and returns the proxy base (with trailing slash),
// or null when not found. Pure (no globals) so it is unit-testable in node.
export function captureProxyBaseFrom(
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
    const match = CONTROL_PANEL_JS_SRC_PATTERN.exec(src);
    if (match) {
      return match[1] + "/";
    }
  }
  return null;
}

// captureOperatorBasePath captures the AppAPI proxy base synchronously, at
// module eval, by scanning document.scripts (currentScript is null under
// `defer`) and sets window.__CASSINI_CONFIG__.operatorBasePath to
// "<base>operator" so operator/config.ts uses the proxied operator API path.
export function captureOperatorBasePath(doc: Document, win: Window): void {
  const base = captureProxyBaseFrom(doc.scripts);
  if (!base) {
    return;
  }
  const config = win.__CASSINI_CONFIG__ ?? {};
  config.operatorBasePath = base + "operator";
  win.__CASSINI_CONFIG__ = config;
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

function mountEmbeddedControlPanel(): void {
  mount(App, { target: ensureAppRoot(document) });
}

function bootstrap(): void {
  captureOperatorBasePath(document, window);
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", mountEmbeddedControlPanel, { once: true });
  } else {
    mountEmbeddedControlPanel();
  }
}

// Only run the bootstrap in a real browser/DOM. Guard so importing this module
// in a node unit test (to exercise the exported pure helpers) does not crash on
// the svelte mount. import.meta.env.VITEST is set by Vitest.
if (typeof document !== "undefined" && !import.meta.env?.VITEST) {
  bootstrap();
}
