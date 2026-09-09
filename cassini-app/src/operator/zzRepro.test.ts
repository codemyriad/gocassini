import { afterEach, describe, expect, it } from "vitest";

import { runSetupPlan } from "./ncSetup";
import type { StorageSetupStep } from "./types";

// Repro: the operator's plan for a stock Nextcloud (verified against
// storageSetupPlan(true, probe{both apps missing})), partitioned exactly the way
// StoragePanel.runSetup does.

function stubNextcloud() {
  (globalThis as { OC?: unknown }).OC = {
    requestToken: "token-abc",
    getRootPath: () => "",
    PasswordConfirmation: {
      requiresPasswordConfirmation: () => false,
      requirePasswordConfirmation: (cb: () => void) => cb(),
    },
  };
}

afterEach(() => {
  delete (globalThis as { OC?: unknown }).OC;
});

function s(p: Partial<StorageSetupStep> & { id: string; action: string; browser: boolean }): StorageSetupStep {
  return { title: p.id, args: {}, occ: "", app_url: "", ...p } as StorageSetupStep;
}

const PLAN: StorageSetupStep[] = [
  s({ id: "group", action: "create_group", browser: true, args: { group: "cassini" } }),
  s({ id: "account", action: "create_user", browser: true, args: { user: "cassini", group: "cassini" } }),
  s({ id: "app:groupfolders", action: "enable_app", browser: false, args: { app: "groupfolders" } }),
  s({ id: "app:group_everyone", action: "enable_app", browser: false, args: { app: "group_everyone" } }),
  s({ id: "folder", action: "create_team_folder", browser: true, args: { mount: "Cassini" } }),
  s({ id: "mount:cassini", action: "map_group", browser: true, args: { mount: "Cassini", group: "cassini", permissions: "31" } }),
  s({ id: "mount:everyone", action: "map_group", browser: true, args: { mount: "Cassini", group: "everyone", permissions: "1" } }),
  s({ id: "acl", action: "enable_folder_acl", browser: true, args: { mount: "Cassini" } }),
  s({ id: "manager", action: "delegate_manager", browser: true, args: { mount: "Cassini", user: "cassini" } }),
];

describe("repro", () => {
  it("aborts on the Team-folder write before the app installs run", async () => {
    stubNextcloud();
    const calls: string[] = [];
    let installsAttempted = 0;
    const impl = (async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      calls.push(`${init?.method ?? "GET"} ${url}`);
      if (url.includes("/apps/groupfolders/")) {
        // A disabled/absent app: Nextcloud has no route, so 404 + HTML.
        return new Response("<!DOCTYPE html><html><body>404</body></html>", {
          status: 404,
          headers: { "Content-Type": "text/html" },
        });
      }
      return new Response(JSON.stringify({ ocs: { meta: { statuscode: 100 }, data: [] } }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }) as unknown as typeof fetch;

    // The partition StoragePanel.runSetup performs, verbatim.
    const browserSteps = PLAN.filter((step) => step.browser);
    const appSteps = PLAN.filter((step) => !step.browser);
    expect(appSteps).toHaveLength(2);

    let thrown: unknown = null;
    try {
      if (browserSteps.length > 0) {
        await runSetupPlan(browserSteps, { fetchImpl: impl });
      }
      if (appSteps.length > 0) {
        installsAttempted += 1; // stands in for operatorClient.installStorageApps()
      }
    } catch (error) {
      thrown = error;
    }

    console.log("calls:", calls);
    console.log("thrown:", thrown);
    console.log("installsAttempted:", installsAttempted);
    expect(thrown).toMatchObject({ reason: "failed", step: "folder" });
    expect(installsAttempted).toBe(0);
  });
});
