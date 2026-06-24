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
