// Shell-level surface routing for the Cassini app (D-420 V3, slice B5).
//
// The shell hosts N role-gated surfaces (today: browse + operator). Which one
// is active is encoded in location.hash as `surface=operator`, layered on top
// of the viewing layer's own hash params (meeting/tx/t — see
// cassini-viewer/src/viewer/hashRouting.ts). We deliberately reuse the SAME
// hash-only, fragment-only pushState mechanism the viewer uses — never a
// pathname/history router, which would desync from the AppAPI embedded route
// and break Nextcloud's own back button.
//
// The two param spaces never collide: `browse` is the default and writes NO
// marker (so standalone/share URLs stay clean and the viewer owns meeting/tx/t
// unshadowed), and the `operator` surface carries no meeting/tx/t of its own.
// The viewer's readViewerHash() ignores the surface param; readSurface() here
// ignores everything else.

export type Surface = "browse" | "operator";

// The panels inside the operator surface (D-723). Configuration is NOT a third
// top-level surface: it is a left nav inside Operator, a Console group above a
// Settings group, so there is one admin surface to gate and one place to look
// for a knob. Naming a panel in the hash is what lets an "unconfigured" notice
// elsewhere in the app link straight at the panel that fixes it (D-722).
//
// The ids are the design prototype's own `OP_PANELS`, kept verbatim so a deep
// link written against the design and one written against the code are the
// same URL.
export type OperatorPanel = "recordings" | "endpoints" | "pipeline" | "templates";

export const OPERATOR_PANELS: readonly OperatorPanel[] = [
  "recordings",
  "endpoints",
  "pipeline",
  "templates",
];

// The run console is the operator's default panel, and like browse it writes NO
// marker — so every `#surface=operator` link already in the wild keeps meaning
// exactly what it meant before this file learned about panels.
const DEFAULT_PANEL: OperatorPanel = "recordings";

// There is deliberately no redirect for #207's `surface=settings`. That shape
// only ever existed on the #207 branch — never on main, never in a tagged
// release — so no URL in anyone's hands names it, and an unknown `surface=`
// already falls back to browse. Reviving it would also have meant rewriting a
// non-admin's address bar to a surface the probe then denies them.

const SURFACE_PARAM = "surface";
const JOB_PARAM = "job";
const PANEL_PARAM = "panel";
// The viewing layer's seek param. The shell never reads or writes its value —
// it only has to know the name, to keep its own params from being written after
// it. See appendKeepingTimeLast.
const TIME_PARAM = "t";

export function readSurface(hash: string): Surface {
  const params = new URLSearchParams(hash.replace(/^#/, ""));
  const value = params.get(SURFACE_PARAM);
  return value === "operator" ? "operator" : "browse";
}

// surfaceHash returns the fragment for a surface: "#surface=<name>" for the
// admin surfaces, "" for browse (the default — no marker).
export function surfaceHash(surface: Surface): string {
  return surface === "browse" ? "" : `#${SURFACE_PARAM}=${surface}`;
}

// applySurface sets/clears the surface param on an EXISTING hash while
// preserving the viewer's own params (meeting/tx/t) in their original order —
// so switching surfaces no longer drops a meeting deep-link, and t= stays last
// for core/transcript.ts's parseTimeHash. Surface is written first.
export function applySurface(hash: string, surface: Surface): string {
  const rest = hash
    .replace(/^#/, "")
    .split("&")
    .filter((part) => part !== "" && !part.startsWith(`${SURFACE_PARAM}=`));
  const parts = surface === "browse" ? rest : [`${SURFACE_PARAM}=${surface}`, ...rest];
  return parts.length > 0 ? `#${parts.join("&")}` : "";
}

// readJob returns the operator surface's selected run id, or "" when none is
// selected. The operator owns `job` the way the viewing layer owns meeting/tx/t.
export function readJob(hash: string): string {
  const params = new URLSearchParams(hash.replace(/^#/, ""));
  return params.get(JOB_PARAM) ?? "";
}

// applyJob sets/clears the job param on an EXISTING hash, preserving every other
// param (notably surface, which applySurface keeps first) in its original order.
export function applyJob(hash: string, jobId: string): string {
  const rest = hash
    .replace(/^#/, "")
    .split("&")
    .filter((part) => part !== "" && !part.startsWith(`${JOB_PARAM}=`));
  const parts =
    jobId === ""
      ? rest
      : appendKeepingTimeLast(rest, `${JOB_PARAM}=${encodeURIComponent(jobId)}`);
  return parts.length > 0 ? `#${parts.join("&")}` : "";
}

// appendKeepingTimeLast adds a shell param to an existing param list without
// ever writing it past the viewer's `t=`.
//
// The shell and the viewing layer share ONE location.hash, and
// core/transcript.ts's parseTimeHash anchors `#t=…` at end-of-string — which is
// why cassini-viewer/src/viewer/hashRouting.ts always writes t= last. A shell
// param appended after it does not fight the viewer for a key; it silently
// costs a meeting deep-link its seek time, because the regex simply stops
// matching. applySurface honours the same contract by writing surface= first.
function appendKeepingTimeLast(rest: string[], part: string): string[] {
  const timeIndex = rest.findIndex((param) => param.startsWith(`${TIME_PARAM}=`));
  if (timeIndex === -1) {
    return [...rest, part];
  }
  return [...rest.slice(0, timeIndex), part, ...rest.slice(timeIndex)];
}

export function isOperatorPanel(value: string | null | undefined): value is OperatorPanel {
  return value != null && (OPERATOR_PANELS as readonly string[]).includes(value);
}

// readPanel returns the operator surface's selected panel. An unknown or absent
// panel is the run console, so a hand-edited hash degrades to the surface's own
// front page rather than to a blank one.
export function readPanel(hash: string): OperatorPanel {
  const params = new URLSearchParams(hash.replace(/^#/, ""));
  const value = params.get(PANEL_PARAM);
  if (isOperatorPanel(value)) {
    return value;
  }
  return DEFAULT_PANEL;
}

// applyPanel sets/clears the panel param on an EXISTING hash, preserving every
// other param (notably surface, which applySurface keeps first) in its original
// order — the same shape and the same discipline as applyJob.
export function applyPanel(hash: string, panel: OperatorPanel): string {
  const rest = hash
    .replace(/^#/, "")
    .split("&")
    .filter((part) => part !== "" && !part.startsWith(`${PANEL_PARAM}=`));
  const parts =
    panel === DEFAULT_PANEL ? rest : appendKeepingTimeLast(rest, `${PANEL_PARAM}=${panel}`);
  return parts.length > 0 ? `#${parts.join("&")}` : "";
}
