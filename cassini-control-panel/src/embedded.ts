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
//
// CSS isolation (D-383): the panel is mounted INSIDE an open shadow root and its
// bundled stylesheet (served by the operator at <base>ui/control-panel.css) is
// injected INTO that shadow — not registered as a global Nextcloud ui/style
// link. This scopes Tailwind Preflight + daisyUI to the panel: it no longer
// bleeds onto NC chrome, and NC's chrome no longer bleeds into it. daisyUI theme
// tokens are emitted on :host (app.css `root: :where(:root,:host)`) so they
// inherit across the shadow boundary.

import { mount } from "svelte";
import App from "./App.svelte";
import "./app.css";

// CONTROL_PANEL_JS_SRC_PATTERN matches the registered ui/control-panel.js src
// and captures the AppAPI proxy base. Exported for the unit test so the test
// and the runtime agree on the exact shape.
//   ".../index.php/apps/app_api/proxy/gocassini/ui/control-panel.js" -> base
//   "   …/index.php/apps/app_api/proxy/gocassini/"
export const CONTROL_PANEL_JS_SRC_PATTERN = /^(.*)\/ui\/control-panel\.js(?:\?.*)?$/;

// rootRelativeBaseFrom turns a (possibly absolute) script src into a
// root-relative proxy base ending in a trailing slash. In a real DOM,
// HTMLScriptElement.src reflects the RESOLVED ABSOLUTE URL (scheme+host), e.g.
// "https://nc.example.com/index.php/apps/app_api/proxy/gocassini/ui/control-panel.js".
// We MUST keep only the path: operator/config.ts treats
// window.__CASSINI_CONFIG__.operatorBasePath as authoritative and runs it
// through a STRICT root-relative validator (normalizeOperatorBasePath throws on
// anything that does not start with "/"). A root-relative base also keeps the
// operator fetch/EventSource same-origin (CSP `connect-src 'self'`) through the
// AppAPI proxy. So strip the origin here: the absolute src would otherwise reach
// loadConfig() and throw, rendering the config-error alert — the exact
// blank/broken-panel bug D-382 fixes. (This is the one place the control panel
// diverges from the viewer, which can keep the absolute base because its code
// joins it via `new URL(...)`.)
//   "https://nc.example.com/…/proxy/gocassini/ui/control-panel.js"
//     -> "/index.php/apps/app_api/proxy/gocassini/"
function rootRelativeBaseFrom(matchedPrefix: string): string {
  // matchedPrefix is everything before "/ui/control-panel.js"; URL-parse it to
  // drop scheme+host when present, falling back to the raw value for an
  // already-relative src (dev/standalone).
  let path = matchedPrefix;
  try {
    // A second arg makes parsing succeed for relative prefixes too; we only
    // ever read .pathname, so the placeholder origin is never stored.
    path = new URL(matchedPrefix, "http://placeholder.invalid/").pathname;
  } catch {
    // Keep the raw prefix if it is not a parseable URL.
  }
  return path.endsWith("/") ? path : path + "/";
}

// captureProxyBaseFrom scans the given script list for the registered
// ui/control-panel.js entry and returns the proxy base as a ROOT-RELATIVE path
// (origin stripped) with a trailing slash, or null when not found. Pure (no
// globals) so it is unit-testable in node.
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
      return rootRelativeBaseFrom(match[1]);
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

// ensureShadowAppRoot creates the shadow host inside the embedded template's
// <div id="content"> (falling back to <body>), attaches an OPEN shadow root,
// injects the bundled stylesheet at cssHref INTO the shadow (so Tailwind/daisyUI
// are scoped to the panel and do not leak to/from NC chrome), and returns the
// #app element — inside the shadow — the SPA mounts into. cssHref is the
// operator-served, proxy-allowed "<base>ui/control-panel.css" (CSP style-src
// 'self' permits it). Idempotent: re-uses an existing shadow host if present.
export function ensureShadowAppRoot(doc: Document, cssHref: string): HTMLElement {
  const existingHost = doc.getElementById("cassini-shadow-host");
  const host = existingHost ?? doc.createElement("div");
  if (!existingHost) {
    host.id = "cassini-shadow-host";
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

// controlPanelStylesheetHref builds the shadow stylesheet URL from the captured
// proxy base. Falls back to a relative "ui/control-panel.css" if the base is
// somehow absent (the panel still mounts; only styling would degrade).
export function controlPanelStylesheetHref(doc: Document): string {
  const base = captureProxyBaseFrom(doc.scripts);
  return base ? base + "ui/control-panel.css" : "ui/control-panel.css";
}

// applyNextcloudTheme bridges Nextcloud's user colour/accessibility preferences
// into the shadow root (D-414). Mirrors the same function in the viewer's
// embedded.ts — see that file for full rationale. Returns true when NC theming
// was applied, false when running outside Nextcloud.
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

function mountEmbeddedControlPanel(): void {
  const appRoot = ensureShadowAppRoot(document, controlPanelStylesheetHref(document));
  const shadowHost = document.getElementById("cassini-shadow-host");
  if (shadowHost) {
    const oca = (window as unknown as Record<string, unknown>).OCA as Record<string, unknown> | undefined;
    const theming = oca?.Theming as Record<string, unknown> | undefined;
    const primaryColor = theming?.primaryColor as string | undefined;
    applyNextcloudTheme(shadowHost, document.body.dataset.themes, primaryColor);
  }
  mount(App, { target: appRoot });
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
