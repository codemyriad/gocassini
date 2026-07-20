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

const SURFACE_PARAM = "surface";

export function readSurface(hash: string): Surface {
  const params = new URLSearchParams(hash.replace(/^#/, ""));
  return params.get(SURFACE_PARAM) === "operator" ? "operator" : "browse";
}

// surfaceHash returns the fragment for a surface: "#surface=operator" for the
// operator surface, "" for browse (the default — no marker).
export function surfaceHash(surface: Surface): string {
  return surface === "operator" ? `#${SURFACE_PARAM}=operator` : "";
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
  const parts = surface === "operator" ? [`${SURFACE_PARAM}=operator`, ...rest] : rest;
  return parts.length > 0 ? `#${parts.join("&")}` : "";
}
