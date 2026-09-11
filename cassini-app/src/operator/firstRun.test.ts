import { describe, expect, it } from "vitest";

import { accountSteps, firstRunPlan } from "./firstRun";
import type { StorageModeOption, StorageSetupStep, StorageStatus } from "./types";

function step(partial: Partial<StorageSetupStep> = {}): StorageSetupStep {
  return {
    id: "account",
    action: "create_user",
    title: "Create the cassini account",
    args: { user: "cassini", group: "cassini" },
    browser: true,
    occ: "",
    app_url: "",
    ...partial,
  };
}

function mode(partial: Partial<StorageModeOption> = {}): StorageModeOption {
  return {
    mode: "default",
    label: "Default",
    active: true,
    available: false,
    summary: "",
    consequence: "",
    blocker: "",
    step: "",
    instructions: [],
    setup: [],
    root: "CassiniNoACL/Recordings",
    archive: { probed: true, present: false, meetings: 0, catalog: false },
    ...partial,
  };
}

function status(partial: Partial<StorageStatus> = {}): StorageStatus {
  return {
    mode: "default",
    mode_source: "resolved_on_enable",
    mode_confirmed: true,
    awaiting_choice: false,
    first_run: true,
    service_account: { user: "cassini", known: true, exists: false, reset_occ: "" },
    migration_clean: true,
    pending_cleanup: "",
    stranded_root: "",
    stranded_recordings: 0,
    ok: false,
    state: "unavailable",
    step: "owner_account",
    detail: "",
    checked_at: "",
    modes: [
      mode({
        setup: [
          step({ id: "group", action: "create_group" }),
          step({ id: "user", action: "create_user" }),
        ],
      }),
    ],
    transition: null,
    installs: [],
    preview: null,
    ...partial,
  };
}

const READY = { isAdmin: true, setupAvailable: true };

describe("firstRunPlan", () => {
  it("is shown once, to an administrator, while the operator says so", () => {
    expect(firstRunPlan(status(), READY)).not.toBeNull();
    // The flag is the operator's, so an install an administrator has already
    // answered for stays answered for everybody.
    expect(firstRunPlan(status({ first_run: false }), READY)).toBeNull();
    expect(firstRunPlan(null, READY)).toBeNull();
  });

  it("is never shown to anyone who is not an administrator", () => {
    // They cannot create a Nextcloud account, and there is nothing here for
    // them to acknowledge: the chip is what tells them who can see recordings.
    expect(firstRunPlan(status(), { isAdmin: false, setupAvailable: true })).toBeNull();
  });

  it("offers to create the account, in the operator's own order", () => {
    const plan = firstRunPlan(status(), READY);
    expect(plan?.creates).toBe(true);
    // The group first: the account joins it as it is created, and a plan run
    // the other way round makes an account with no write-capable mount.
    expect(plan?.steps.map((entry) => entry.action)).toEqual(["create_group", "create_user"]);
  });

  it("only acknowledges when the account is already there", () => {
    // The operator made it on enable, or an earlier install did. Then the
    // second paragraph would describe something that is not going to happen.
    const plan = firstRunPlan(
      status({ service_account: { user: "cassini", known: true, exists: true, reset_occ: "" } }),
      READY,
    );
    expect(plan).not.toBeNull();
    expect(plan?.creates).toBe(false);
    expect(plan?.unavailable).toBe(false);
  });

  it("does not promise an account it has no way to create", () => {
    // An operator reporting the account missing with no plan for it is a fault,
    // and the setup notice is what says so. A button that would do nothing is
    // worse than no button.
    const plan = firstRunPlan(status({ modes: [mode({ setup: [] })] }), READY);
    expect(plan?.creates).toBe(false);
    expect(plan?.steps).toEqual([]);
  });

  it("says the standalone build cannot make the account, rather than offering to", () => {
    // Served from Cassini's own origin, with neither Nextcloud's scripts nor
    // its session: every provisioning write is refused before it is sent.
    const plan = firstRunPlan(status(), { isAdmin: true, setupAvailable: false });
    expect(plan?.creates).toBe(true);
    expect(plan?.unavailable).toBe(true);
  });

  it("still lets a standalone build acknowledge an install that needs no account", () => {
    // Nothing is written to Nextcloud in that branch — the only call is the
    // operator's own — so the button works there.
    const plan = firstRunPlan(
      status({ service_account: { user: "cassini", known: true, exists: true, reset_occ: "" } }),
      { isAdmin: true, setupAvailable: false },
    );
    expect(plan?.unavailable).toBe(false);
  });
});

describe("accountSteps", () => {
  it("takes the plan of the mode that is in force, not of the other one", () => {
    // Each mode carries its own plan, and the one Cassini is not using names a
    // Team folder this install has no use for.
    const steps = accountSteps(
      status({
        mode: "default",
        modes: [
          mode({ mode: "default", active: true, setup: [step({ id: "user" })] }),
          mode({
            mode: "access_controlled",
            active: false,
            setup: [step({ id: "folder", action: "create_team_folder" })],
          }),
        ],
      }),
    );
    expect(steps.map((entry) => entry.id)).toEqual(["user"]);
  });

  it("falls back to the active mode when no mode is recorded", () => {
    const steps = accountSteps(
      status({
        mode: "",
        modes: [
          mode({ mode: "default", active: false, setup: [step({ id: "unused" })] }),
          mode({ mode: "access_controlled", active: true, setup: [step({ id: "active" })] }),
        ],
      }),
    );
    expect(steps.map((entry) => entry.id)).toEqual(["active"]);
  });

  it("runs the account steps and nothing else the plan happens to carry", () => {
    // A dialog about recording for the first time does not build a Team folder,
    // map groups or set ACLs, however much of that is in the plan.
    const steps = accountSteps(
      status({
        modes: [
          mode({
            setup: [
              step({ id: "group", action: "create_group" }),
              step({ id: "user", action: "create_user" }),
              step({ id: "folder", action: "create_team_folder" }),
              step({ id: "acl", action: "enable_folder_acl" }),
            ],
          }),
        ],
      }),
    );
    expect(steps.map((entry) => entry.action)).toEqual(["create_group", "create_user"]);
  });

  it("never runs a step the operator did not call browser-doable", () => {
    // `browser: false` means Nextcloud demands the password on the request
    // itself, which no session satisfies and Cassini will not do.
    const steps = accountSteps(
      status({
        modes: [mode({ setup: [step({ id: "user", action: "create_user", browser: false })] })],
      }),
    );
    expect(steps).toEqual([]);
  });
});
