// Hash-only routing for the viewer.
//
// The embedded build mounts on Nextcloud's AppAPI page
// (/index.php/apps/app_api/embedded/gocassini/viewer); rewriting that page's
// pathname/search via the History API would desync the URL from the NC route
// and break the browser back button at the NC chrome level. Encoding all
// viewer state in location.hash (#meeting=<id>&tx=<id>&t=<time>) keeps
// deep-links + in-app back/forward working in BOTH embedded and standalone
// without ever touching pathname or search.
//
// `t` (the seek time) is always written LAST so the existing
// core/transcript.ts parseTimeHash() — which anchors `#t=…` at end-of-string —
// keeps matching.

export interface ViewerTimeRange {
  startMs: number;
  endMs: number;
}

function validRange(range: ViewerTimeRange): boolean {
  return Number.isSafeInteger(range.startMs) && Number.isSafeInteger(range.endMs) &&
    range.startMs >= 0 && range.endMs > range.startMs;
}

export function readViewerRange(hash: string): ViewerTimeRange | null {
  const params = new URLSearchParams(hash.replace(/^#/, ""));
  const values = params.getAll("range");
  if (values.length !== 1 || !/^\d+,\d+$/.test(values[0]!)) return null;
  const [startMs, endMs] = values[0]!.split(",").map(Number);
  const range = { startMs: startMs!, endMs: endMs! };
  return validRange(range) ? range : null;
}

export interface ViewerHashState {
  meeting: string;
  tx: string;
}

export function readViewerHash(hash: string): ViewerHashState {
  const raw = hash.replace(/^#/, "");
  const params = new URLSearchParams(raw);
  return {
    meeting: params.get("meeting") ?? "",
    tx: params.get("tx") ?? "",
  };
}

export function buildViewerHash(state: {
  meeting?: string;
  tx?: string;
  timeMs?: number | null;
  range?: ViewerTimeRange | null;
}): string {
  const parts: string[] = [];
  if (state.meeting) {
    parts.push(`meeting=${encodeURIComponent(state.meeting)}`);
  }
  if (state.tx) {
    parts.push(`tx=${encodeURIComponent(state.tx)}`);
  }
  if (state.range && validRange(state.range)) {
    parts.push(`range=${state.range.startMs},${state.range.endMs}`);
  }
  // Keep t= last so parseTimeHash (anchored at end-of-string) still matches.
  if (typeof state.timeMs === "number" && Number.isFinite(state.timeMs)) {
    parts.push(`t=${Math.round(state.timeMs)}ms`);
  }
  return parts.length > 0 ? `#${parts.join("&")}` : "";
}

// viewerUrlWithHash rewrites ONLY the fragment of the given href, leaving
// pathname + search untouched (the NC embedded-page contract).
export function viewerUrlWithHash(href: string, hash: string): string {
  const url = new URL(href);
  url.hash = hash;
  return url.toString();
}
