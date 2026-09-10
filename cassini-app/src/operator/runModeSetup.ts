import type { OperatorClient } from "./client";
import { runSetupPlan } from "./ncSetup";
import type { StorageModeOption, StorageStatus } from "./types";

// Building one storage model's prerequisites, from the administrator's browser
// (D-671), shared by the setup wizard and the settled panel (D-708).
//
// It lives here rather than in either component because both need it and the
// ORDER inside it is load-bearing in a way that is easy to get wrong twice:
//
//	1. the apps FIRST. Everything after them lives inside them — creating the
//	   Team folder POSTs to /index.php/apps/groupfolders/…, which 404s while
//	   `groupfolders` is not installed, so running the browser steps first aborts
//	   the whole run at the folder.
//	2. RECOMPUTE the plan. The operator's probe cannot see a Team folder until
//	   that app is enabled, so a plan built beforehand says "create the folder"
//	   whether or not one exists — and acting on it makes a second `Cassini`.
//	3. then the browser steps, then a re-check.

export interface ModeSetupResult {
  // status is the operator's refreshed record, whatever happened.
  status: StorageStatus;
  // finished is false when the run stopped because an app install the operator
  // could not perform is still outstanding. Everything left in the plan lives
  // inside those apps, so stopping is the honest outcome — the per-app detail on
  // the status says what to do, and Nextcloud's own Apps page is one click away.
  finished: boolean;
  // createdAccount / password carry the service account's credential when THIS
  // run created it. They are the only place it exists; see PasswordReveal.
  createdAccount: string;
  password: string;
}

export async function runModeSetup(
  client: OperatorClient,
  option: StorageModeOption,
  onProgress: (message: string) => void,
): Promise<ModeSetupResult> {
  let plan = option;
  let status: StorageStatus | null = null;

  if (plan.setup.some((step) => !step.browser)) {
    onProgress("Installing Nextcloud apps…");
    // The operator's attempt: it succeeds on releases that predate Nextcloud's
    // password-confirmation hardening, and where an administrator set a bypass
    // range. Its per-app outcome comes back on the status.
    status = await client.installStorageApps();
    const refreshed = status.modes.find((entry) => entry.mode === plan.mode) ?? null;
    if (refreshed && refreshed.setup.some((step) => !step.browser)) {
      return { status, finished: false, createdAccount: "", password: "" };
    }
    if (refreshed) {
      plan = refreshed;
    }
  }

  let createdAccount = "";
  let password = "";
  const browserSteps = plan.setup.filter((step) => step.browser);
  if (browserSteps.length > 0) {
    // A failure AFTER the account was created still has a credential, and it
    // exists nowhere else — the account is made and its password is set, and
    // nothing anywhere holds the value. runSetupPlan attaches it to the error it
    // throws (NcSetupError.outcome), and the callers read it from there, so the
    // failure and the password reach the screen together.
    const outcome = await runSetupPlan(browserSteps, {
      onProgress: ({ step, index, total }) => onProgress(`${index + 1}/${total} — ${step.title}`),
    });
    createdAccount = outcome.createdAccount;
    password = outcome.password;
  }

  onProgress("Checking…");
  return { status: await client.recheckStorage(), finished: true, createdAccount, password };
}
