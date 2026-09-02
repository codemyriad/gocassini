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
// unshadowed), and the admin surfaces carry no meeting/tx/t of their own.
// The viewer's readViewerHash() ignores the surface param; readSurface() here
// ignores everything else.
//
// `setup` (D-616) is the third surface: instance-level configuration, today the
// storage-mode switch. It is deliberately not a panel inside the operator
// surface — the operator surface is about runs, and a control that moves every
// recording in the instance does not belong beside a run list.

export type Surface = "browse" | "operator" | "setup";

const SURFACE_PARAM = "surface";
const JOB_PARAM = "job";

// ADMIN_SURFACES are the ones the shell shows only when the operator boundary
// probe succeeds. Keeping them in one list is what stops readSurface and the
// tab bar drifting apart when a fourth is added.
export const ADMIN_SURFACES: readonly Surface[] = ["operator", "setup"];

function isAdminSurface(value: string | null): value is Surface {
  return value !== null && (ADMIN_SURFACES as readonly string[]).includes(value);
}

export function readSurface(hash: string): Surface {
  const params = new URLSearchParams(hash.replace(/^#/, ""));
  const value = params.get(SURFACE_PARAM);
  return isAdminSurface(value) ? value : "browse";
}

// surfaceHash returns the fragment for a surface: "#surface=operator" for an
// admin surface, "" for browse (the default — no marker).
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
  const parts = jobId === "" ? rest : [...rest, `${JOB_PARAM}=${encodeURIComponent(jobId)}`];
  return parts.length > 0 ? `#${parts.join("&")}` : "";
}
