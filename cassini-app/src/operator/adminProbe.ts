// Admin-surface detection for the Cassini shell (D-420 V3).
//
// The operator JSON API is the REAL boundary: info.xml marks the whole
// operator/* API ADMIN (the one exception, operator/setup, is USER precisely
// because it answers a question that is not the API's), so a non-admin who
// reaches one through the AppAPI proxy gets a 403 (AppAPI has been observed
// answering 404 instead, hiding the route entirely — either way, denied).
// Client-side detection is therefore UX-only —
// we probe that boundary to decide whether to SHOW the operator surface, never
// to guard it. Whatever the client renders, the operator API stays the brace.
//
// probeOperatorAvailable() issues a cheap GET <base>/status (the operator's
// doctor endpoint — returns non-secret health, never mutates) and maps the
// result: an operator that ANSWERED with its status payload -> available,
// denied / unreachable -> hidden.
//
// Answering is the test, not 200. /status returns 503 when the deployment
// cannot serve recordings (D-585), and treating that as "not an admin" is how
// the one person who could fix a broken install lost the operator surface and
// got the same "ask your administrator" the users got. The body is the
// evidence: only the operator emits it, and it is also where the diagnosis
// lives (setupHealth.readRecordingsAccess).

export interface OperatorProbeResult {
  available: boolean;
  // The HTTP status observed, or null on a network/transport failure. Kept for
  // logging/telemetry; the shell only branches on `available`.
  status: number | null;
  // The decoded /status body when the operator answered with one, else null.
  // Carries `recordings_access`, which is the admin-only half of the setup
  // notice.
  body: unknown;
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
    if (response.status === 200) {
      return { available: true, status: 200, body: await readJSON(response) };
    }
    // 503 is the operator saying it is unhealthy — which only an admin can be
    // told. Anything else with a status payload is equally proof we got through
    // to the operator rather than to the proxy's denial. A 503 from AppAPI or a
    // gateway in front of it carries no such body, so it stays hidden.
    const body = await readJSON(response);
    return { available: body !== null, status: response.status, body };
  } catch {
    // Network/transport failure — fail closed for UX (hide the operator tab).
    return { available: false, status: null, body: null };
  }
}

// readJSON returns the decoded body only when it is recognisably the operator's
// status payload: an object with the `ok` boolean and the `recordings_access`
// block every build emits. An HTML error page, an OCS envelope, or an empty
// body yields null.
async function readJSON(response: Response): Promise<unknown> {
  try {
    const parsed: unknown = await response.json();
    if (
      typeof parsed === "object" &&
      parsed !== null &&
      typeof (parsed as { ok?: unknown }).ok === "boolean" &&
      typeof (parsed as { recordings_access?: unknown }).recordings_access === "object" &&
      (parsed as { recordings_access?: unknown }).recordings_access !== null
    ) {
      return parsed;
    }
    return null;
  } catch {
    return null;
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
