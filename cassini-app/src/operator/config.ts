export interface PanelConfig {
  operatorBasePath: string;
}

export function loadConfig(): PanelConfig {
  const runtimeValue = window.__CASSINI_CONFIG__?.operatorBasePath;
  if (runtimeValue != null) {
    // Runtime-injected config is authoritative: deployments that front the
    // panel behind a custom prefix can spell out the exact operator path.
    return { operatorBasePath: normalizeOperatorBasePath(runtimeValue) };
  }
  const operatorBasePath = normalizeOperatorBasePath(__CASSINI_OPERATOR_BASE_PATH__);
  return {
    operatorBasePath: joinPrefix(proxyPrefixFromPathname(window.location.pathname), operatorBasePath),
  };
}

function normalizeOperatorBasePath(value: string): string {
  const trimmed = value.trim();
  const normalized = (trimmed === "" ? "/" : trimmed).replace(/\/+$/, "") || "/";
  // Accept either a root-relative path (`/operator` — the dev/standalone and
  // CASSINI_OPERATOR_BASE_PATH shape) OR an absolute http(s) URL. The AppAPI
  // embedded build (D-420 V3) captures the proxy base from the ui/viewer.js
  // script src, which the DOM resolves to an ABSOLUTE, same-origin URL
  // (http://host/index.php/apps/app_api/proxy/gocassini/operator) — the viewer
  // base is used absolutely for the same reason. Rejecting it here made
  // loadConfig throw on the embedded page, so the operator surface (and its
  // admin probe) silently disappeared for admins.
  if (!normalized.startsWith("/") && !/^https?:\/\//i.test(normalized)) {
    throw new Error(
      "operatorBasePath must be a root-relative path (e.g. /operator) or an absolute http(s) URL.",
    );
  }
  return normalized;
}

// proxyPrefixFromPathname derives the path prefix in front of the SPA mount
// point. Served directly by the operator the panel lives at /control-panel/,
// but through the Nextcloud AppAPI proxy it lives at
// /index.php/apps/app_api/proxy/<appid>/control-panel/ — there an
// origin-absolute /operator request would bypass the proxy prefix and 404 on
// Nextcloud. Everything before the /control-panel segment is the prefix the
// operator base path must be mounted under.
function proxyPrefixFromPathname(pathname: string): string {
  const marker = "/control-panel";
  const idx = pathname.indexOf(`${marker}/`);
  if (idx > 0) {
    return pathname.slice(0, idx);
  }
  if (idx < 0 && pathname.endsWith(marker)) {
    return pathname.slice(0, pathname.length - marker.length);
  }
  return "";
}

function joinPrefix(prefix: string, basePath: string): string {
  if (prefix === "") {
    return basePath;
  }
  return basePath === "/" ? prefix : prefix + basePath;
}
