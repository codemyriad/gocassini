import type { StorageSetupStep, StorageStatus } from "./types";

// What the first-run dialog says and does, apart from the component that draws
// it (D-756).
//
// A fresh install resolves its own storage mode and records from then on. The
// one thing left that Nextcloud will not let the operator do on every release
// is create the `cassini` account: those provisioning writes are guarded by
// password confirmation, which an ExApp's act-as-user request can never satisfy
// and the administrator's own browser session can (ncSetup.ts). So the dialog
// is shown once per install, it says who will see recordings, and it makes that
// one account.
//
// The decisions live here rather than in FirstRunDialog.svelte for the reason
// setupHealth.ts gives: `.svelte` files in this repo are tested by reading their
// source text, which can say nothing about which of three shapes a dialog took.
//
//	nothing         no administrator, no answer from the operator, or the flag
//	                has already been acknowledged.
//	creates         the account is missing and the operator handed us a plan for
//	                it. Two paragraphs, and the button makes it.
//	acknowledges    the account is already there (the operator made it on
//	                enable, or an earlier install did). One paragraph, and the
//	                button only records that this was seen.

// The plan actions this dialog will perform, and no others.
//
// The operator's plan for a mode can also carry the Team folder, its group
// mappings and its ACLs; none of that belongs in a dialog about a fresh install
// recording for the first time, and the mode being resolved here needs none of
// it. Naming the two actions is what keeps a longer plan from being run by
// surprise.
const ACCOUNT_ACTIONS: readonly string[] = ["create_group", "create_user"];

export interface FirstRunPlan {
  // creates says the dialog's second paragraph is shown and its primary button
  // makes the service account. False means the account exists (or the operator
  // offered no way to make it) and the button only acknowledges.
  creates: boolean;
  // steps are the operator's own browser-side steps, in its own order: the
  // group before the account that joins it.
  steps: StorageSetupStep[];
  // unavailable says the account has to be made and this page cannot make it —
  // the standalone build, which is served from Cassini's own origin and has
  // neither Nextcloud's scripts nor its session. The note stands in for the
  // button there, because a button that cannot work is worse than a sentence.
  unavailable: boolean;
}

// accountSteps pulls the two account steps out of the plan for the mode that is
// IN FORCE.
//
// Not out of every mode: each mode carries its own plan, and the one Cassini is
// not using can name a Team folder this install has no use for. The active flag
// is the fallback for a record whose `mode` has not been written yet.
export function accountSteps(status: StorageStatus | null): StorageSetupStep[] {
  if (!status) {
    return [];
  }
  const option =
    status.modes.find((entry) => entry.mode === status.mode && status.mode !== "") ??
    status.modes.find((entry) => entry.active) ??
    null;
  return (option?.setup ?? []).filter(
    (step) => step.browser && ACCOUNT_ACTIONS.includes(step.action),
  );
}

// firstRunPlan answers what to show, or null for "show nothing".
export function firstRunPlan(
  status: StorageStatus | null,
  options: { isAdmin: boolean; setupAvailable: boolean },
): FirstRunPlan | null {
  // Only an administrator, and only once per install. `first_run` is the
  // operator's flag rather than this browser's, so a second administrator does
  // not meet a dialog the first one has already answered.
  if (!options.isAdmin || !status || !status.first_run) {
    return null;
  }
  const steps = accountSteps(status);
  // The second paragraph is a promise, so it is made only where it can be kept:
  // the account is genuinely missing AND the operator gave us a way to make it.
  // An operator that reports the account missing with no plan for it has
  // something wrong with it, and that is what the setup notice is for — this
  // dialog must not offer a button that does nothing.
  const creates = !status.service_account.exists && steps.length > 0;
  return { creates, steps, unavailable: creates && !options.setupAvailable };
}
