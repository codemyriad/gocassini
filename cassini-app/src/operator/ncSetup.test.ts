import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { isSetupAvailable, runSetupPlan } from "./ncSetup";
import type { StorageSetupStep } from "./types";

const create: StorageSetupStep = {
  id: "owner", action: "create_user", title: "Create recordings account",
  args: { user: "cassini", display_name: "Cassini" }, browser: true, occ: "occ user:add cassini",
};
function response(statuscode = 100, message = "OK"): Response {
  return new Response(JSON.stringify({ ocs: { meta: { statuscode, message } } }), {
    status: 200, headers: { "Content-Type": "application/json" },
  });
}
beforeEach(() => {
  vi.stubGlobal("OC", {
    requestToken: "token",
    getRootPath: () => "/nextcloud",
    PasswordConfirmation: {
      requiresPasswordConfirmation: () => false,
      requirePasswordConfirmation: (done: () => void) => done(),
    },
  });
});
afterEach(() => vi.unstubAllGlobals());

describe("browser account setup", () => {
  it("creates only the owner account using Nextcloud's session and returns its credential", async () => {
    const fetchImpl = vi.fn(async () => response()) as unknown as typeof fetch;
    const outcome = await runSetupPlan([create], { fetchImpl });
    expect(outcome.createdAccount).toBe("cassini");
    expect(outcome.password.length).toBeGreaterThan(32);
    const [url, init] = (fetchImpl as unknown as ReturnType<typeof vi.fn>).mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/nextcloud/ocs/v2.php/cloud/users?format=json");
    expect(init.credentials).toBe("same-origin");
    const body = new URLSearchParams(String(init.body));
    expect(body.get("userid")).toBe("cassini");
    expect(body.get("password")).toBe(outcome.password);
    expect(body.has("groups[]")).toBe(false);
  });
  it("does not claim a new password for an existing account", async () => {
    const outcome = await runSetupPlan([create], { fetchImpl: vi.fn(async () => response(102, "already exists")) as unknown as typeof fetch });
    expect(outcome).toEqual({ createdAccount: "", password: "" });
  });
  it("rejects an unknown browser step", async () => {
    await expect(runSetupPlan([{ ...create, action: "create_team_folder" }])).rejects.toThrow("can't run");
  });
  it("requires Nextcloud's own password confirmation API", () => {
    vi.stubGlobal("OC", undefined);
    expect(isSetupAvailable()).toBe(false);
  });
});
