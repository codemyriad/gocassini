import { describe, expect, it } from "vitest";

import { isLikelyAdminHint, probeOperatorAvailable } from "./adminProbe";

// A minimal fetch stub. `body`, when given, is what response.json() resolves
// to; without it json() throws, which is what a bodiless denial looks like.
function fetchWithStatus(
  status: number,
  capture?: (url: string) => void,
  body?: unknown,
): typeof fetch {
  return (async (url: string) => {
    capture?.(url);
    return {
      status,
      json: async () => {
        if (body === undefined) {
          throw new Error("no body");
        }
        return body;
      },
    } as Response;
  }) as unknown as typeof fetch;
}

// A minimal but recognisable /status payload — `ok` plus the recordings_access
// block, which is what the probe uses to tell the operator apart from whatever
// else answers on that URL.
function statusBody(recordingsAccess: Record<string, unknown>): Record<string, unknown> {
  return { ok: recordingsAccess.ok === true, recordings_access: recordingsAccess };
}

describe("probeOperatorAvailable", () => {
  it("reports available on 200", async () => {
    expect(await probeOperatorAvailable("/operator", fetchWithStatus(200))).toEqual({
      available: true,
      status: 200,
      body: null,
    });
  });

  it("returns the decoded status payload so the shell can read the diagnosis", async () => {
    const body = statusBody({ ok: true, state: "provisioned" });
    expect(await probeOperatorAvailable("/operator", fetchWithStatus(200, undefined, body))).toEqual({
      available: true,
      status: 200,
      body,
    });
  });

  // The regression this file exists for: /status answers 503 when the
  // deployment cannot serve recordings (D-585). Only an administrator can be
  // told that at all — the route is ADMIN — so treating it as "not an admin"
  // hid the operator surface from the one person able to fix the install, and
  // showed them the same "ask your administrator" everyone else got.
  it("reports available on 503 when the operator answered with its status payload", async () => {
    const body = statusBody({ ok: false, state: "unavailable", step: "app_missing:groupfolders" });
    expect(await probeOperatorAvailable("/operator", fetchWithStatus(503, undefined, body))).toEqual({
      available: true,
      status: 503,
      body,
    });
  });

  it("stays hidden on a 503 that did not come from the operator", async () => {
    // AppAPI, or a gateway in front of it, answering for a container that is
    // down. No status payload, so nothing proves we reached the operator.
    expect(
      await probeOperatorAvailable("/operator", fetchWithStatus(503, undefined, { message: "Service unavailable" })),
    ).toEqual({ available: false, status: 503, body: null });
  });

  it("reports unavailable on 403 (non-admin through the ADMIN-gated proxy)", async () => {
    expect(await probeOperatorAvailable("/operator", fetchWithStatus(403))).toEqual({
      available: false,
      status: 403,
      body: null,
    });
  });

  it("reports unavailable on 404 (AppAPI hiding the ADMIN route entirely)", async () => {
    expect(await probeOperatorAvailable("/operator", fetchWithStatus(404))).toEqual({
      available: false,
      status: 404,
      body: null,
    });
  });

  it("reports unavailable on any other non-200 without a status payload", async () => {
    expect(await probeOperatorAvailable("/operator", fetchWithStatus(500))).toEqual({
      available: false,
      status: 500,
      body: null,
    });
  });

  it("fails closed on a transport error", async () => {
    const throwing = (async () => {
      throw new Error("network down");
    }) as unknown as typeof fetch;
    expect(await probeOperatorAvailable("/operator", throwing)).toEqual({
      available: false,
      status: null,
      body: null,
    });
  });

  it("appends /status to the base, tolerating a trailing slash", async () => {
    let called = "";
    await probeOperatorAvailable("/index.php/apps/app_api/proxy/gocassini/operator/", fetchWithStatus(200, (u) => (called = u)));
    expect(called).toBe("/index.php/apps/app_api/proxy/gocassini/operator/status");
  });
});

describe("isLikelyAdminHint", () => {
  it("returns the OC.isUserAdmin() value when Nextcloud exposes it", () => {
    expect(isLikelyAdminHint({ OC: { isUserAdmin: () => true } })).toBe(true);
    expect(isLikelyAdminHint({ OC: { isUserAdmin: () => false } })).toBe(false);
  });

  it("returns null when OC is absent (standalone / non-NC context)", () => {
    expect(isLikelyAdminHint({})).toBeNull();
    expect(isLikelyAdminHint(undefined)).toBeNull();
    expect(isLikelyAdminHint(null)).toBeNull();
  });

  it("returns null when OC.isUserAdmin throws", () => {
    expect(
      isLikelyAdminHint({
        OC: {
          isUserAdmin: () => {
            throw new Error("boom");
          },
        },
      }),
    ).toBeNull();
  });
});
