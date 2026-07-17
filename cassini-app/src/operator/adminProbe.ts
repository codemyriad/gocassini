// Admin-surface detection for the Cassini shell (D-420 V3).
//
// The operator JSON API is the REAL boundary: info.xml marks every operator/*
// route ADMIN, so a non-admin who reaches one through the AppAPI proxy gets a
// 403. Client-side detection is therefore UX-only — we probe that boundary to
// decide whether to SHOW the operator surface, never to guard it. Whatever the
// client renders, the operator API stays the brace.
//
// probeOperatorAvailable() issues a cheap GET <base>/status (the operator's
// doctor endpoint — returns non-secret health, never mutates) and maps the
// result: 200 -> available, 403 (or any non-200 / transport error) -> hidden.

export interface OperatorProbeResult {
  available: boolean;
  // The HTTP status observed, or null on a network/transport failure. Kept for
  // logging/telemetry; the shell only branches on `available`.
  status: number | null;
}

export async function probeOperatorAvailable(
  operatorBasePath: string,
  fetchImpl: typeof fetch = fetch,
): Promise<OperatorProbeResult> {
  const url = `${operatorBasePath.replace(/\/+$/, "")}/status`;
  try {
    const response = await fetchImpl(url, {
      method: "GET",
      headers: { Accept: "application/json" },
    });
    return { available: response.status === 200, status: response.status };
  } catch {
    // Network/transport failure — fail closed for UX (hide the operator tab).
    return { available: false, status: null };
  }
}

// isLikelyAdminHint reads Nextcloud's OC.isUserAdmin() when present, as an
// OPTIMISTIC anti-flash hint ONLY: it lets the shell show the operator tab
// immediately instead of waiting a round-trip, and the probe then corrects it.
// Outside Nextcloud (standalone) OC is absent and this returns null (no hint) —
// the boundary probe is always authoritative.
export function isLikelyAdminHint(win: unknown): boolean | null {
  const oc = (win as { OC?: { isUserAdmin?: () => boolean } } | null | undefined)?.OC;
  if (oc && typeof oc.isUserAdmin === "function") {
    try {
      return oc.isUserAdmin();
    } catch {
      return null;
    }
  }
  return null;
}
