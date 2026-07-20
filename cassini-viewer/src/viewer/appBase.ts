// readViewerBase returns the AppAPI proxy base captured by src/embedded.ts into
// window.__CASSINI_VIEWER_BASE__ (resolved to an absolute URL), or "" outside
// the embedded build. Shared by catalog.ts and loadArtifact.ts so both resolve
// their bundled/document-relative fallbacks against the proxy base identically.
//
// Asset paths in the embedded viewer are published-archive paths the operator
// serves under "<base>published/..."; catalog.ts already rewrites those into
// absolute URLs via the fetched catalog's response.url, but bundled/document-
// relative fallbacks must still resolve against the proxy base rather than the
// Nextcloud embedded-page pathname.
export function readViewerBase(): string {
  const base = typeof window !== "undefined" ? window.__CASSINI_VIEWER_BASE__ : undefined;
  if (typeof base !== "string" || base === "") {
    return "";
  }
  return new URL(base, window.location.href).toString();
}

export function resolveAppBaseUrl(): URL {
  const base = import.meta.env.BASE_URL;
  if (base && base !== "./" && base !== "/") {
    return new URL(base, window.location.href);
  }
  if (base === "/") {
    return new URL("/", window.location.href);
  }

  const scriptBase = resolveScriptBaseUrl();
  if (scriptBase) {
    return new URL(scriptBase);
  }

  return new URL("/", window.location.href);
}

function resolveScriptBaseUrl(): string {
  if (typeof document === "undefined") {
    return "";
  }

  const candidates: string[] = [];
  const current = document.currentScript as HTMLScriptElement | null;
  if (current?.src) {
    candidates.push(current.src);
  }
  for (const script of Array.from(document.scripts)) {
    if (script.src) {
      candidates.push(script.src);
    }
  }

  for (const src of candidates.reverse()) {
    const url = new URL(src, window.location.href);
    const assetIndex = url.pathname.lastIndexOf("/assets/");
    if (assetIndex >= 0) {
      url.pathname = url.pathname.slice(0, assetIndex + 1);
      url.search = "";
      url.hash = "";
      return url.toString();
    }
  }

  return "";
}
