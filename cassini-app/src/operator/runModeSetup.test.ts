import { afterEach, describe, expect, it, vi } from "vitest";

import type { OperatorClient } from "./client";
import { runModeSetup } from "./runModeSetup";
import type { StorageModeOption, StorageStatus, StorageSetupStep } from "./types";

// The sequence that builds one storage model's prerequisites, from the
// administrator's browser (D-671), shared by the setup wizard and the settled
// panel (D-708).
//
// It used to live inside StoragePanel and was asserted by reading that
// component's source. Two components need it now, and its ORDER is load-bearing
// in a way that is easy to get wrong twice — so it is a module with behavioural
// tests instead.

vi.mock("./ncSetup", () => ({
  runSetupPlan: vi.fn(async () => ({ createdAccount: "", password: "" })),
}));

const { runSetupPlan } = await import("./ncSetup");

function step(partial: Partial<StorageSetupStep> & { id: string; browser: boolean }): StorageSetupStep {
  return {
    action: "create_group",
    title: "",
    args: {},
    occ: "",
    app_url: "",
    ...partial,
  };
}

function option(setup: StorageSetupStep[]): StorageModeOption {
  return {
    mode: "access_controlled",
    label: "Access controlled",
    active: false,
    available: false,
    summary: "",
    consequence: "",
    blocker: "",
    step: "",
    instructions: [],
    setup,
    root: "Cassini/Recordings",
    archive: { probed: true, present: true, meetings: 0, catalog: false },
  };
}

function status(setup: StorageSetupStep[]): StorageStatus {
  return { modes: [option(setup)] } as unknown as StorageStatus;
}

function client(overrides: Partial<Record<"installStorageApps" | "recheckStorage", () => Promise<StorageStatus>>>) {
  const calls: string[] = [];
  const stub = {
    async installStorageApps() {
      calls.push("install");
      return (await overrides.installStorageApps?.()) ?? status([]);
    },
    async recheckStorage() {
      calls.push("recheck");
      return (await overrides.recheckStorage?.()) ?? status([]);
    },
  } as unknown as OperatorClient;
  return { calls, stub };
}

const APP_STEP = step({ id: "app:groupfolders", action: "enable_app", browser: false });
const FOLDER_STEP = step({ id: "folder", action: "create_team_folder", browser: true });

describe("runModeSetup", () => {
  afterEach(() => {
    vi.mocked(runSetupPlan).mockClear();
    vi.mocked(runSetupPlan).mockResolvedValue({ createdAccount: "", password: "" });
  });

  // The plan is emitted in dependency order and everything after the apps lives
  // INSIDE them: creating the Team folder POSTs to /apps/groupfolders/…, which
  // 404s while the app is absent. Running the browser steps first aborted the
  // whole run at the folder on exactly the instance this feature is for — one
  // with neither app installed.
  it("installs the Nextcloud apps before the steps that live inside them", async () => {
    const { calls, stub } = client({ installStorageApps: async () => status([FOLDER_STEP]) });

    await runModeSetup(stub, option([APP_STEP, FOLDER_STEP]), () => {});

    expect(calls).toEqual(["install", "recheck"]);
    expect(runSetupPlan).toHaveBeenCalledTimes(1);
    // The browser steps ran after the install, and only the browser ones.
    const ran = vi.mocked(runSetupPlan).mock.calls[0]?.[0] ?? [];
    expect(ran.map((entry) => entry.id)).toEqual(["folder"]);
  });

  // The operator cannot SEE a Team folder until groupfolders is enabled, so a
  // plan built before the install says "create the folder" whether or not one
  // exists. Acting on the stale plan would make a second Cassini folder.
  it("recomputes the plan from the refreshed status", async () => {
    const { stub } = client({
      // After the install the operator can look, and finds the folder already
      // there — so the recomputed plan has one step, not two.
      installStorageApps: async () => status([step({ id: "manager", action: "delegate_manager", browser: true })]),
    });

    await runModeSetup(stub, option([APP_STEP, FOLDER_STEP]), () => {});

    const ran = vi.mocked(runSetupPlan).mock.calls[0]?.[0] ?? [];
    expect(ran.map((entry) => entry.id)).toEqual(["manager"]);
  });

  // Everything left in the plan lives inside the apps, so stopping is the honest
  // outcome — running the folder steps would 404 on every one of them.
  it("stops when an app install the operator could not perform is outstanding", async () => {
    const { calls, stub } = client({ installStorageApps: async () => status([APP_STEP, FOLDER_STEP]) });

    const result = await runModeSetup(stub, option([APP_STEP, FOLDER_STEP]), () => {});

    expect(result.finished).toBe(false);
    expect(calls).toEqual(["install"]);
    expect(runSetupPlan).not.toHaveBeenCalled();
  });

  it("skips the install entirely when no app is missing", async () => {
    const { calls, stub } = client({});

    const result = await runModeSetup(stub, option([FOLDER_STEP]), () => {});

    expect(calls).toEqual(["recheck"]);
    expect(result.finished).toBe(true);
  });

  // The service account's password exists in exactly one place: the value the
  // browser generated, on its way to being shown once.
  it("carries the created account's credential back to the caller", async () => {
    vi.mocked(runSetupPlan).mockResolvedValue({ createdAccount: "cassini", password: "Cw1!secret" });
    const { stub } = client({});

    const result = await runModeSetup(stub, option([FOLDER_STEP]), () => {});

    expect(result.createdAccount).toBe("cassini");
    expect(result.password).toBe("Cw1!secret");
  });

  it("reports progress for the slow parts", async () => {
    const seen: string[] = [];
    const { stub } = client({ installStorageApps: async () => status([FOLDER_STEP]) });

    await runModeSetup(stub, option([APP_STEP, FOLDER_STEP]), (message) => seen.push(message));

    expect(seen[0]).toContain("Installing");
    expect(seen[seen.length - 1]).toContain("Checking");
  });
});
