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
//
// CSS isolation (D-383): the SPA is mounted INSIDE an open shadow root and its
// bundled stylesheet (served by the operator at <base>ui/viewer.css) is
// injected INTO that shadow — not registered as a global Nextcloud ui/style
// link. This scopes Tailwind Preflight + daisyUI to the SPA: the SPA no longer
// bleeds onto NC chrome, and NC's chrome no longer bleeds into the SPA. The
// daisyUI theme tokens are emitted on :host (app.css `root: :where(:root,:host)`)
// so they inherit across the shadow boundary.

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

// ensureShadowAppRoot creates the shadow host inside the embedded template's
// <div id="content"> (falling back to <body>), attaches an OPEN shadow root,
// injects the bundled stylesheet at cssHref INTO the shadow (so Tailwind/daisyUI
// are scoped to the SPA and do not leak to/from NC chrome), and returns the #app
// element — inside the shadow — the SPA mounts into. cssHref is the operator-
// served, proxy-allowed "<base>ui/viewer.css" (CSP style-src 'self' permits it).
// Idempotent: re-uses an existing shadow host if one is already present.
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
// base, mirroring the version query string from the viewer.js script tag so the
// browser cache expires together with the JS bundle. Falls back to a relative
// "ui/viewer.css" if the base is absent (the SPA still mounts; only styling
// would degrade).
function viewerStylesheetHref(win: Window, doc: Document): string {
  const base = win.__CASSINI_VIEWER_BASE__;
  if (!base) return "ui/viewer.css";
  // Extract the ?v=... query string from the registered viewer.js script src
  // so the CSS cache-buster stays in sync with the JS cache-buster.
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
// into the shadow root (D-414). It sets --nc-primary as an inline style on the
// shadow host (aliasing NC's --color-primary to avoid a circular var() reference
// with DaisyUI's own --color-primary) and records the active NC theme as
// data-nc-theme so the CSS override block in app.css can activate. Returns true
// when NC theming was applied (OCA.Theming.primaryColor present), false when
// running outside Nextcloud (standalone viewer — saturn themes remain active).
// Pure enough to unit-test: all globals are injected.
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

function mountEmbeddedViewer(): void {
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
  // Wait for the window load event unless the page is already fully loaded.
  // DOMContentLoaded fires after deferred scripts, but OCA.Theming is populated
  // by a Nextcloud dynamic import that resolves asynchronously after
  // DOMContentLoaded. The load event fires once readyState reaches "complete",
  // at which point OCA.Theming.primaryColor is reliably available.
  if (document.readyState === "complete") {
    mountEmbeddedViewer();
  } else {
    window.addEventListener("load", mountEmbeddedViewer, { once: true });
  }
}

// Only run the bootstrap in a real browser/DOM. Guard so importing this module
// in a node unit test (to exercise the exported pure helpers) does not crash on
// the svelte mount. import.meta.env.VITEST is set by Vitest.
if (typeof document !== "undefined" && !import.meta.env?.VITEST) {
  bootstrap();
}
