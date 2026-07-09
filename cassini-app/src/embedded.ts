// Embedded entry for the AppAPI-registered ui/script — the Cassini shell
// (D-420). Derived from cassini-viewer/src/embedded.ts (D-381/D-383): the shell
// re-uses the exact viewer embedding contract because it IS served as the
// single "Cassini" top-menu entry at /ui/viewer.js. The only differences from
// the viewer's file are that it mounts the shell App (which hosts the viewing
// layer) and pulls the layer's stylesheet.
//
// NOTE (D-420 debt): this duplicates the viewer's embedded bootstrap. When the
// operator surface lands (V3) it also needs the operator-base capture from the
// old control-panel embedded.ts, at which point the shared shadow-root / NC-
// theme / base-capture helpers should be extracted into the viewing layer and
// imported here instead of copied. Tracked with V3.
//
// AppAPI renders the embedded page (/index.php/apps/app_api/embedded/
// gocassini/viewer) as a bare <div id="content"></div> and loads the script the
// ExApp registered as a Nextcloud-nonce'd `<script defer src=".../proxy/
// gocassini/ui/viewer.js">`. The page CSP is `script-src-elem 'strict-dynamic'
// 'nonce-…'`, so this already-trusted script runs, but any further <script src>
// it writes would NOT — we mount the SPA directly from this single bundle.
//
// Because AppAPI loads us with `defer`, document.currentScript is null by the
// time IIFE code runs. We recover the proxy base by scanning document.scripts
// for our own /ui/viewer.js src, at top-level synchronous eval (before mount),
// so the viewing layer's catalog.ts / loadArtifact.ts see
// window.__CASSINI_VIEWER_BASE__ during onMount.
//
// CSS isolation (D-383): the SPA mounts INSIDE an open shadow root and its
// bundled stylesheet (served at <base>ui/viewer.css) is injected INTO that
// shadow. The daisyUI theme tokens are emitted on :host (app.css
// `root: :where(:root,:host)`) so they inherit across the shadow boundary.

import { mount } from "svelte";
import App from "./App.svelte";
import "cassini-viewer/app.css";

// VIEWER_JS_SRC_PATTERN matches the registered ui/viewer.js src and captures
// the AppAPI proxy base. Exported for the unit test so test and runtime agree.
//   ".../index.php/apps/app_api/proxy/gocassini/ui/viewer.js" -> base
//   "   …/index.php/apps/app_api/proxy/gocassini/"
export const VIEWER_JS_SRC_PATTERN = /^(.*)\/ui\/viewer\.js(?:\?.*)?$/;

// captureViewerBaseFrom scans the given script list for the registered
// ui/viewer.js entry and returns the proxy base (with trailing slash), or null
// when not found. Pure (no globals) so it is unit-testable in node.
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
// sets window.__CASSINI_VIEWER_BASE__ so the viewing layer sees it.
export function captureViewerBase(doc: Document, win: Window): void {
  const base = captureViewerBaseFrom(doc.scripts);
  if (base) {
    win.__CASSINI_VIEWER_BASE__ = base;
  }
}

// ensureShadowAppRoot creates the shadow host inside the embedded template's
// <div id="content"> (falling back to <body>), attaches an OPEN shadow root,
// injects the bundled stylesheet at cssHref INTO the shadow, and returns the
// #app element — inside the shadow — the SPA mounts into. Idempotent.
export function ensureShadowAppRoot(doc: Document, cssHref: string): HTMLElement {
  const existingHost = doc.getElementById("cassini-shadow-host");
  const host = existingHost ?? doc.createElement("div");
  if (!existingHost) {
    host.id = "cassini-shadow-host";
    host.style.cssText = "display:block;width:100%;height:100%";
    (doc.getElementById("content") ?? doc.body).appendChild(host);
  }
  const shadow = host.shadowRoot ?? host.attachShadow({ mode: "open" });
  let appRoot = shadow.getElementById?.("app") ?? null;
  if (appRoot) {
    return appRoot as HTMLElement;
  }
  if (cssHref) {
    const link = doc.createElement("link");
    link.rel = "stylesheet";
    link.href = cssHref;
    shadow.appendChild(link);
  }
  appRoot = doc.createElement("div");
  (appRoot as HTMLElement).id = "app";
  shadow.appendChild(appRoot);
  return appRoot as HTMLElement;
}

// viewerStylesheetHref builds the shadow stylesheet URL from the captured proxy
// base, mirroring the version query from the viewer.js script tag so the CSS
// cache-buster stays in sync with the JS bundle. Falls back to a relative
// "ui/viewer.css" if the base is absent (the SPA still mounts; styling only).
function viewerStylesheetHref(win: Window, doc: Document): string {
  const base = win.__CASSINI_VIEWER_BASE__;
  if (!base) return "ui/viewer.css";
  let versionQuery = "";
  for (let i = 0; i < doc.scripts.length; i++) {
    const src = doc.scripts[i]?.src ?? "";
    if (VIEWER_JS_SRC_PATTERN.test(src)) {
      const qIdx = src.lastIndexOf("?");
      if (qIdx >= 0) versionQuery = src.slice(qIdx);
      break;
    }
  }
  return base + "ui/viewer.css" + versionQuery;
}

// applyNextcloudTheme bridges Nextcloud's user colour/accessibility preferences
// into the shadow root (D-414). Sets --nc-primary on the shadow host and
// records the active NC theme as data-nc-theme so app.css's override block
// activates. Returns true when NC theming was applied (OCA.Theming present),
// false when running standalone. Pure enough to unit-test (globals injected).
export function applyNextcloudTheme(
  host: HTMLElement,
  bodyDataThemes: string | undefined,
  primaryColor: string | null | undefined,
): boolean {
  if (!primaryColor) return false;
  host.style.setProperty("--nc-primary", primaryColor);
  const themes = bodyDataThemes ?? "";
  const isDark = themes.includes("dark");
  const isHighContrast = themes.includes("highcontrast");
  let themeValue = "light";
  if (isDark && isHighContrast) themeValue = "dark-highcontrast";
  else if (isDark) themeValue = "dark";
  else if (isHighContrast) themeValue = "highcontrast";
  host.dataset.ncTheme = themeValue;
  return true;
}

function mountEmbeddedShell(): void {
  const appRoot = ensureShadowAppRoot(document, viewerStylesheetHref(window, document));
  const shadowHost = document.getElementById("cassini-shadow-host");
  let ncMode = false;
  if (shadowHost) {
    const oca = (window as unknown as Record<string, unknown>).OCA as Record<string, unknown> | undefined;
    const theming = oca?.Theming as Record<string, unknown> | undefined;
    const primaryColor = theming?.primaryColor as string | undefined;
    // body.dataset.themes may not be populated at window.load time (NC's
    // accessibility theme JS runs after load). Fall back to the reliably-set
    // OCA.Theming.enabledThemes array, which mirrors the body attribute values.
    const enabledThemes = theming?.enabledThemes as string[] | undefined;
    const themesStr = document.body.dataset.themes ?? enabledThemes?.join(" ") ?? "";
    ncMode = applyNextcloudTheme(shadowHost, themesStr, primaryColor);
  }
  mount(App, { target: appRoot, props: { ncMode } });
}

function bootstrap(): void {
  captureViewerBase(document, window);
  // Wait for the window load event unless already complete: OCA.Theming is
  // populated by a Nextcloud dynamic import that resolves after
  // DOMContentLoaded; the load event is the earliest reliable read point.
  if (document.readyState === "complete") {
    mountEmbeddedShell();
  } else {
    window.addEventListener("load", mountEmbeddedShell, { once: true });
  }
}

// Only run the bootstrap in a real browser/DOM. Guard so importing this module
// in a node unit test does not crash on the svelte mount.
if (typeof document !== "undefined" && !import.meta.env?.VITEST) {
  bootstrap();
}
