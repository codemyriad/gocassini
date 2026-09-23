import { describe, expect, it } from "vitest";
import { accountSteps, firstRunPlan, firstRunReady } from "./firstRun";
import type { StorageSetupStep, StorageStatus } from "./types";

const create: StorageSetupStep = {
  id: "owner_account", action: "create_user", title: "Create account",
  args: { user: "cassini" }, browser: true, occ: "occ user:add cassini",
};
function status(changes: Partial<StorageStatus> = {}): StorageStatus {
  return {
    first_run: true,
    service_account: { user: "cassini", known: true, exists: false, reset_occ: "" },
    ok: false, state: "unavailable", step: "owner_account", detail: "", checked_at: "", setup: [create],
    ...changes,
  };
}

describe("first run with direct shares", () => {
  it("only runs the account creation step", () => {
    const other = { ...create, id: "old", action: "create_team_folder" };
    expect(accountSteps(status({ setup: [other, create] }))).toEqual([create]);
  });
  it("offers account creation to an administrator in Nextcloud", () => {
    const plan = firstRunPlan(status(), { isAdmin: true, setupAvailable: true });
    expect(plan?.creates).toBe(true);
    expect(firstRunReady(plan!)).toBe(true);
  });
  it("does not acknowledge a missing account when it cannot be created", () => {
    const plan = firstRunPlan(status({ setup: [] }), { isAdmin: true, setupAvailable: true });
    expect(plan?.blocked).toBe(true);
    expect(firstRunReady(plan!)).toBe(false);
  });
  it("does not show the dialog after acknowledgement or to a non-admin", () => {
    expect(firstRunPlan(status({ first_run: false }), { isAdmin: true, setupAvailable: true })).toBeNull();
    expect(firstRunPlan(status(), { isAdmin: false, setupAvailable: true })).toBeNull();
  });
});
