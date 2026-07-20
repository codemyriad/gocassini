import { describe, expect, it } from "vitest";

import { isLikelyAdminHint, probeOperatorAvailable } from "./adminProbe";

// A minimal fetch stub: resolves with just the `status` the probe branches on.
function fetchWithStatus(status: number, capture?: (url: string) => void): typeof fetch {
  return (async (url: string) => {
    capture?.(url);
    return { status } as Response;
  }) as unknown as typeof fetch;
}

describe("probeOperatorAvailable", () => {
  it("reports available on 200", async () => {
    expect(await probeOperatorAvailable("/operator", fetchWithStatus(200))).toEqual({
      available: true,
      status: 200,
    });
  });

  it("reports unavailable on 403 (non-admin through the ADMIN-gated proxy)", async () => {
    expect(await probeOperatorAvailable("/operator", fetchWithStatus(403))).toEqual({
      available: false,
      status: 403,
    });
  });

  it("reports unavailable on any other non-200", async () => {
    expect(await probeOperatorAvailable("/operator", fetchWithStatus(500))).toEqual({
      available: false,
      status: 500,
    });
  });

  it("fails closed on a transport error", async () => {
    const throwing = (async () => {
      throw new Error("network down");
    }) as unknown as typeof fetch;
    expect(await probeOperatorAvailable("/operator", throwing)).toEqual({
      available: false,
      status: null,
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
