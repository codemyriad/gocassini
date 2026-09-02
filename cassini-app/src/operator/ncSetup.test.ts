import { afterEach, describe, expect, it, vi } from "vitest";

import { NcSetupError, isSetupAvailable, nextcloudUrl, runSetupPlan } from "./ncSetup";
import type { StorageSetupStep } from "./types";

// Performing the setup from the administrator's browser (D-671). The operator
// is refused these writes on every current Nextcloud; this page is not, because
// its session carries a login token Nextcloud's password-confirmation
// middleware accepts.

interface StubOptions {
  root?: string;
  requiresConfirmation?: boolean;
  // cancel makes the administrator dismiss Nextcloud's dialog.
  cancel?: boolean;
  missing?: boolean;
}

function stubNextcloud(options: StubOptions = {}): { confirmations: number } {
  const counters = { confirmations: 0 };
  if (options.missing) {
    delete (globalThis as { OC?: unknown }).OC;
    return counters;
  }
  (globalThis as { OC?: unknown }).OC = {
    requestToken: "token-abc",
    getRootPath: () => options.root ?? "",
    PasswordConfirmation: {
      requiresPasswordConfirmation: () => options.requiresConfirmation !== false,
      requirePasswordConfirmation: (callback: () => void, _o: unknown, reject?: () => void) => {
        counters.confirmations += 1;
        if (options.cancel) {
          reject?.();
          return;
        }
        callback();
      },
    },
  };
  return counters;
}

interface Call {
  url: string;
  method: string;
  body: string;
  headers: Record<string, string>;
}

// stubFetch records every request and answers from a per-URL-substring map.
function stubFetch(
  responders: Array<[string, () => Response]> = [],
): { calls: Call[]; impl: typeof fetch } {
  const calls: Call[] = [];
  const impl = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push({
      url,
      method: init?.method ?? "GET",
      body: String(init?.body ?? ""),
      headers: (init?.headers ?? {}) as Record<string, string>,
    });
    for (const [needle, respond] of responders) {
      if (url.includes(needle)) {
        return respond();
      }
    }
    return ocsOk();
  }) as unknown as typeof fetch;
  return { calls, impl };
}

function ocsOk(data: unknown = []): Response {
  return new Response(JSON.stringify({ ocs: { meta: { statuscode: 100 }, data } }), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

function ocsFail(status: number, statuscode: number, message: string): Response {
  return new Response(
    JSON.stringify({ ocs: { meta: { status: "failure", statuscode, message }, data: [] } }),
    { status, headers: { "Content-Type": "application/json" } },
  );
}

function step(partial: Partial<StorageSetupStep> & { id: string; action: string }): StorageSetupStep {
  return {
    title: partial.id,
    args: {},
    browser: true,
    occ: "",
    app_url: "",
    ...partial,
  };
}

const GROUP_STEP = step({ id: "group", action: "create_group", args: { group: "cassini" } });
const ACCOUNT_STEP = step({
  id: "account",
  action: "create_user",
  args: { user: "cassini", group: "cassini", display_name: "Cassini recordings" },
});

afterEach(() => {
  delete (globalThis as { OC?: unknown }).OC;
  vi.unstubAllGlobals();
});

describe("isSetupAvailable", () => {
  it("is false where Nextcloud's own scripts are absent", () => {
    stubNextcloud({ missing: true });
    expect(isSetupAvailable()).toBe(false);
  });

  it("is true on a page that carries them", () => {
    stubNextcloud();
    expect(isSetupAvailable()).toBe(true);
  });
});

describe("nextcloudUrl", () => {
  // The SPA is served THROUGH the AppAPI proxy, so a relative URL would address
  // the proxy and reach the ExApp — which is exactly the caller Nextcloud
  // refuses. These have to be absolute on the Nextcloud origin.
  it("hangs paths off Nextcloud's own web root, not the proxy", () => {
    stubNextcloud({ root: "" });
    expect(nextcloudUrl("/ocs/v2.php/cloud/groups")).toBe("/ocs/v2.php/cloud/groups");
    stubNextcloud({ root: "/nextcloud" });
    expect(nextcloudUrl("/ocs/v2.php/cloud/groups")).toBe("/nextcloud/ocs/v2.php/cloud/groups");
    stubNextcloud({ root: "/nextcloud/" });
    expect(nextcloudUrl("/ocs/v2.php/cloud/groups")).toBe("/nextcloud/ocs/v2.php/cloud/groups");
  });
});

describe("runSetupPlan", () => {
  it("creates the service account as the signed-in administrator", async () => {
    stubNextcloud();
    const { calls, impl } = stubFetch();

    await runSetupPlan([GROUP_STEP, ACCOUNT_STEP], { fetchImpl: impl });

    expect(calls.map((c) => c.url)).toEqual([
      "/ocs/v2.php/cloud/groups?format=json",
      "/ocs/v2.php/cloud/users?format=json",
    ]);
    for (const call of calls) {
      expect(call.method).toBe("POST");
      // The session cookie is the whole mechanism; the CSRF token is what
      // Nextcloud demands of a browser write.
      expect(call.headers.requesttoken).toBe("token-abc");
      expect(call.headers["OCS-APIRequest"]).toBe("true");
    }
    // "groups[]", not "groups": OCS decodes it as a PHP array and answers a
    // bare 400 for a scalar.
    expect(calls[1].body).toContain("groups%5B%5D=cassini");
    expect(calls[1].body).toContain("userid=cassini");
  });

  // The password is Nextcloud's business. Cassini generates one only to satisfy
  // the create contract, and nothing ever authenticates with it.
  it("never sends a password it was given, and never asks for one", async () => {
    stubNextcloud();
    const { calls, impl } = stubFetch();

    await runSetupPlan([ACCOUNT_STEP], { fetchImpl: impl });

    const body = calls[0].body;
    expect(body).toContain("password=");
    // A generated one, not an administrator's: it is 32 random bytes.
    const password = decodeURIComponent(new URLSearchParams(body).get("password") ?? "");
    expect(password.startsWith("Cw1!")).toBe(true);
    expect(password.length).toBeGreaterThan(40);
    expect(calls.some((c) => c.url.includes("/login/confirm"))).toBe(false);
  });

  it("confirms once for the whole run, not once per step", async () => {
    const counters = stubNextcloud();
    const { impl } = stubFetch();

    await runSetupPlan([GROUP_STEP, ACCOUNT_STEP], { fetchImpl: impl });

    expect(counters.confirmations).toBe(1);
  });

  it("does not open the dialog when Nextcloud says the session is already confirmed", async () => {
    const counters = stubNextcloud({ requiresConfirmation: false });
    const { impl } = stubFetch();

    await runSetupPlan([GROUP_STEP], { fetchImpl: impl });

    expect(counters.confirmations).toBe(0);
  });

  it("stops when the administrator dismisses the dialog, and changes nothing", async () => {
    stubNextcloud({ cancel: true });
    const { calls, impl } = stubFetch();

    await expect(runSetupPlan([GROUP_STEP], { fetchImpl: impl })).rejects.toMatchObject({
      reason: "cancelled",
    });
    expect(calls).toHaveLength(0);
  });

  // An "already exists" is what a re-run after a partial failure looks like,
  // and a setup flow is re-run more often than it is run clean.
  it("treats an already-existing group as done", async () => {
    stubNextcloud();
    const { impl } = stubFetch([
      ["/cloud/groups", () => ocsFail(400, 102, "group exists")],
    ]);

    await expect(runSetupPlan([GROUP_STEP], { fetchImpl: impl })).resolves.toBeUndefined();
  });

  // A 403 mid-run is almost always the 30-minute confirmation window closing.
  it("re-confirms once and retries a step Nextcloud denied", async () => {
    const counters = stubNextcloud();
    let attempts = 0;
    const { impl } = stubFetch([
      [
        "/cloud/groups",
        () => {
          attempts += 1;
          return attempts === 1
            ? ocsFail(403, 403, "Password confirmation is required")
            : ocsOk();
        },
      ],
    ]);

    await runSetupPlan([GROUP_STEP], { fetchImpl: impl });

    expect(attempts).toBe(2);
    expect(counters.confirmations).toBe(2);
  });

  it("gives up on a second denial rather than looping", async () => {
    stubNextcloud();
    const { impl } = stubFetch([
      ["/cloud/groups", () => ocsFail(403, 403, "Password confirmation is required")],
    ]);

    await expect(runSetupPlan([GROUP_STEP], { fetchImpl: impl })).rejects.toMatchObject({
      reason: "denied",
    });
  });

  // The Team folder's id does not exist while the plan is being built, so the
  // mapping steps name the mount point and the executor resolves it once.
  it("resolves the Team folder by mount point, after creating it", async () => {
    stubNextcloud();
    const { calls, impl } = stubFetch([
      [
        "groupfolders/folders?format=json",
        () => ocsOk({ "7": { id: 7, mount_point: "Cassini" } }),
      ],
    ]);

    await runSetupPlan(
      [
        step({ id: "folder", action: "create_team_folder", args: { mount: "Cassini" } }),
        step({
          id: "mount:cassini",
          action: "map_group",
          args: { mount: "Cassini", group: "cassini", permissions: "31" },
        }),
        step({ id: "acl", action: "enable_folder_acl", args: { mount: "Cassini" } }),
        step({
          id: "manager",
          action: "delegate_manager",
          args: { mount: "Cassini", user: "cassini" },
        }),
      ],
      { fetchImpl: impl },
    );

    const urls = calls.map((c) => c.url);
    expect(urls).toContain("/index.php/apps/groupfolders/folders/7/groups?format=json");
    expect(urls).toContain("/index.php/apps/groupfolders/folders/7/groups/cassini?format=json");
    expect(urls).toContain("/index.php/apps/groupfolders/folders/7/acl?format=json");
    expect(urls).toContain("/index.php/apps/groupfolders/folders/7/manageACL?format=json");
    // Resolved once, not once per step that needs it. Filtered by method: the
    // create POSTs the same URL the lookup GETs.
    const lookups = calls.filter(
      (c) => c.method === "GET" && c.url.endsWith("/folders?format=json"),
    );
    expect(lookups).toHaveLength(1);
  });

  it("picks the lowest id when a mount point is duplicated, like the operator does", async () => {
    stubNextcloud();
    const { calls, impl } = stubFetch([
      [
        "groupfolders/folders?format=json",
        () =>
          ocsOk({
            "9": { id: 9, mount_point: "Cassini" },
            "4": { id: 4, mountPoint: "Cassini" },
          }),
      ],
    ]);

    await runSetupPlan(
      [step({ id: "acl", action: "enable_folder_acl", args: { mount: "Cassini" } })],
      { fetchImpl: impl },
    );

    expect(calls.map((c) => c.url)).toContain("/index.php/apps/groupfolders/folders/4/acl?format=json");
  });

  // The app installs are strict: Nextcloud wants the password on the request,
  // which no session supplies. Attempting them here would 403 every time.
  it("skips the steps the operator said the browser cannot do", async () => {
    stubNextcloud();
    const { calls, impl } = stubFetch();

    await runSetupPlan(
      [
        step({ id: "app:groupfolders", action: "enable_app", args: { app: "groupfolders" }, browser: false }),
        GROUP_STEP,
      ],
      { fetchImpl: impl },
    );

    expect(calls.map((c) => c.url)).toEqual(["/ocs/v2.php/cloud/groups?format=json"]);
  });

  it("does nothing at all when every step is beyond the browser", async () => {
    const counters = stubNextcloud();
    const { calls, impl } = stubFetch();

    await runSetupPlan(
      [step({ id: "app:groupfolders", action: "enable_app", browser: false })],
      { fetchImpl: impl },
    );

    expect(calls).toHaveLength(0);
    // …including asking for a password confirmation it has no use for.
    expect(counters.confirmations).toBe(0);
  });

  // An action a newer operator emits and this build does not know. Skipping it
  // would silently leave a half-built substrate that later reads as healthy.
  it("fails loudly on an action it does not recognise", async () => {
    stubNextcloud();
    const { impl } = stubFetch();

    await expect(
      runSetupPlan([step({ id: "future", action: "invent_a_folder" })], { fetchImpl: impl }),
    ).rejects.toBeInstanceOf(NcSetupError);
  });

  it("refuses to start where Nextcloud's scripts are absent", async () => {
    stubNextcloud({ missing: true });
    const { calls, impl } = stubFetch();

    await expect(runSetupPlan([GROUP_STEP], { fetchImpl: impl })).rejects.toMatchObject({
      reason: "unavailable",
    });
    expect(calls).toHaveLength(0);
  });

  it("names the step it failed on", async () => {
    stubNextcloud();
    const { impl } = stubFetch([["/cloud/users", () => ocsFail(500, 996, "boom")]]);

    await expect(
      runSetupPlan([GROUP_STEP, ACCOUNT_STEP], { fetchImpl: impl }),
    ).rejects.toMatchObject({ step: "account" });
  });
});
