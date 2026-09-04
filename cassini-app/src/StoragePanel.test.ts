import { describe, expect, it } from "vitest";

import storagePanelSource from "./StoragePanel.svelte?raw";
import setupSource from "./Setup.svelte?raw";
import appSource from "./App.svelte?raw";

// Svelte components are not mounted in this repo, so the panel's load-bearing
// decisions are asserted against its source. What matters here is not the
// markup but three properties a refactor could quietly lose — each of which
// would make the switch either unusable or dangerous.

describe("StoragePanel", () => {
  it("confirms before switching, and only from the confirmation", () => {
    // requestSwitch stages the mode; only confirmSwitch calls the operator. A
    // button wired straight to putStorage would move every recording in the
    // instance on one click.
    expect(storagePanelSource).toContain("function requestSwitch");
    expect(storagePanelSource).toContain("function confirmSwitch");
    expect(storagePanelSource).toContain("requestSwitch(option)");
    expect(storagePanelSource).toContain("on:click={confirmSwitch}");
    const putCalls = storagePanelSource.match(/operatorClient\.putStorage\(/g) ?? [];
    expect(putCalls).toHaveLength(1);
    expect(storagePanelSource).toContain('"Confirm storage mode change"');
  });

  // The switch and the setup are different decisions with different
  // consequences, and confirmSwitch must not be able to do the wrong one.
  it("distinguishes switching a mode from building one", () => {
    expect(storagePanelSource).toContain('pendingKind === "setup"');
    expect(storagePanelSource).toContain("await runSetup(target)");
    expect(storagePanelSource).toContain("await operatorClient.putStorage(");
    expect(storagePanelSource).toContain('"Confirm Nextcloud setup changes"');
  });

  it("never switches modes as a side effect of setting one up", () => {
    // Building a mode and moving into it are separate decisions. runSetup
    // re-checks; it must not call putStorage.
    const runSetup = storagePanelSource.slice(
      storagePanelSource.indexOf("async function runSetup("),
      storagePanelSource.indexOf("async function confirmSwitch("),
    );
    expect(runSetup).toContain("recheckStorage()");
    expect(runSetup).not.toContain("putStorage(");
  });

  it("cannot switch to a mode the operator did not call available", () => {
    // An unavailable mode now offers SETUP rather than a switch, so the guard
    // moved — but a switch still requires `available`.
    expect(storagePanelSource).toContain('pendingKind = option.available ? "switch" : "setup"');
    // …and a mode with no plan is still a dead end, not a button that does nothing.
    expect(storagePanelSource).toContain("(!option.available && option.setup.length === 0)");
  });

  it("re-reads the mode after a failed switch instead of claiming nothing changed", () => {
    // A transition that fails AFTER moving the archive HAS changed the mode the
    // operator is using, and its own message says so. Asserting "the mode was
    // not changed" there would contradict the sentence printed beneath it.
    expect(storagePanelSource).toContain("switchError = asMessage(error);");
    expect(storagePanelSource).toContain("Switching the storage mode failed.");
    expect(storagePanelSource).not.toContain("The storage mode was not changed.");
    // The catch block re-reads before it finishes.
    const catchBlock = storagePanelSource.slice(
      storagePanelSource.indexOf("switchError = asMessage(error);"),
      storagePanelSource.indexOf("} finally {", storagePanelSource.indexOf("switchError = asMessage(error);")),
    );
    expect(catchBlock).toContain("operatorClient.getStorage()");
  });

  // The plan is emitted in dependency order and everything after the apps lives
  // INSIDE them: creating the Team folder POSTs to /apps/groupfolders/…, which
  // 404s while the app is absent. Running the browser steps first aborted the
  // whole run at the folder on exactly the instance this feature is for — one
  // with neither app installed.
  it("installs the Nextcloud apps before the steps that live inside them", () => {
    const runSetup = storagePanelSource.slice(
      storagePanelSource.indexOf("async function runSetup("),
      storagePanelSource.indexOf("function modeOptionFor("),
    );
    const installAt = runSetup.indexOf("installStorageApps()");
    const browserAt = runSetup.indexOf("runSetupPlan(");
    expect(installAt).toBeGreaterThan(-1);
    expect(browserAt).toBeGreaterThan(-1);
    expect(installAt).toBeLessThan(browserAt);
  });

  // The operator cannot SEE a Team folder until groupfolders is enabled, so a
  // plan built before the install says "create the folder" whether or not one
  // exists. Acting on the stale plan would make a second Cassini folder.
  it("recomputes the plan after installing the apps", () => {
    const runSetup = storagePanelSource.slice(
      storagePanelSource.indexOf("async function runSetup("),
      storagePanelSource.indexOf("function modeOptionFor("),
    );
    expect(runSetup).toContain("modeOptionFor(status, plan.mode)");
    expect(runSetup).toContain("plan = refreshed");
    // …and it stops rather than running folder steps that would 404.
    expect(runSetup).toContain("refreshed.setup.some((step) => !step.browser)");
  });

  it("renders the operator's own words rather than copy of its own", () => {
    for (const field of ["option.summary", "option.blocker", "pending.consequence", "status.detail"]) {
      expect(storagePanelSource).toContain(`{${field}}`);
    }
    expect(storagePanelSource).toContain("option.instructions.join(");
  });

  // "" is a third answer, not a missing one: an unresolved mode means nothing
  // has checked the instance, and calling that "default" would claim a decision
  // nobody made.
  it("distinguishes an unresolved mode from the default one", () => {
    expect(storagePanelSource).toContain('status.mode === ""');
    expect(storagePanelSource).toContain("Not checked yet");
  });
});

describe("the Setup surface", () => {
  it("is the storage panel's only host, and builds its own client", () => {
    expect(setupSource).toContain("import StoragePanel from \"./StoragePanel.svelte\"");
    expect(setupSource).toContain("new OperatorClient(loadConfig().operatorBasePath)");
  });

  it("is a shell tab gated on the same admin probe as the operator surface", () => {
    expect(appSource).toContain('import Setup from "./Setup.svelte"');
    expect(appSource).toContain('aria-current={surface === "setup" ? "page" : undefined}');
    expect(appSource).toContain('on:click={() => selectSurface("setup")}');
    // The tab bar lives inside `{#if operatorAvailable}`, so there is exactly
    // one gate for every admin surface.
    expect(appSource.indexOf("{#if operatorAvailable}")).toBeLessThan(
      appSource.indexOf('selectSurface("setup")'),
    );
  });

  it("hides browse for any admin surface, not only the operator one", () => {
    // `surface === "operator"` here would leave the meeting list rendered
    // underneath the Setup surface.
    expect(appSource).toContain('class:cassini-shell-hidden={surface !== "browse"}');
    expect(appSource).not.toContain('class:cassini-shell-hidden={surface === "operator"}');
  });
});

describe("StoragePanel transition preview", () => {
  // The transition relocates an entire published archive and, going into the
  // Team folder, makes every already-published recording readable by every
  // account. The confirmation stated the policy but none of the facts, so an
  // administrator pressed the button and found out afterwards.
  it("fetches the preview when the prompt opens, not when it is confirmed", () => {
    expect(storagePanelSource).toContain("void loadPreview(option)");
    // Inside requestSwitch, which is what opens the prompt.
    const requestSwitch = storagePanelSource.slice(
      storagePanelSource.indexOf("function requestSwitch"),
      storagePanelSource.indexOf("async function loadPreview"),
    );
    expect(requestSwitch).toContain("loadPreview");
    // And NOT inside confirmSwitch, where it would be too late to matter.
    const confirmSwitch = storagePanelSource.slice(
      storagePanelSource.indexOf("async function confirmSwitch"),
      storagePanelSource.indexOf("function asMessage"),
    );
    expect(confirmSwitch).not.toContain("loadPreview");
  });

  it("only previews a switch, never a setup", () => {
    expect(storagePanelSource).toContain('if (pendingKind === "switch")');
  });

  it("discards a diff for a mode nobody is looking at any more", () => {
    // The prompt can be cancelled or re-pointed while the request is in flight.
    expect(storagePanelSource).toContain("if (pending?.mode === asked)");
  });

  it("renders the counts and the source and destination roots", () => {
    expect(storagePanelSource).toContain("preview.meetings");
    expect(storagePanelSource).toContain("preview.source_root");
    expect(storagePanelSource).toContain("preview.destination_root");
  });

  it("says there is nothing to move rather than showing an empty diff", () => {
    expect(storagePanelSource).toContain("preview.nothing_to_move");
    expect(storagePanelSource).toContain("no published recordings to move");
  });

  it("renders every warning the operator returned", () => {
    expect(storagePanelSource).toContain("{#each preview.warnings as warning");
  });

  it("does not let a failed preview read as an empty one", () => {
    // A preview that could not run must not render as "nothing to move" — and
    // must not block the switch either, since the transition has its own guards.
    expect(storagePanelSource).toContain("previewError");
    expect(storagePanelSource).toContain("could not work out what would move");
  });

  it("clears the preview when the prompt is cancelled", () => {
    const cancel = storagePanelSource.slice(
      storagePanelSource.indexOf("function cancelSwitch"),
      storagePanelSource.indexOf("function cancelSwitch") + 200,
    );
    expect(cancel).toContain("preview = null");
  });
});

describe("StoragePanel on an install whose routes predate the tab", () => {
  // AppAPI learns an ExApp's routes from the manifest it was REGISTERED with, so
  // an installation updated in place from a version that predates /storage would
  // 404 every request this tab makes. Whether that actually happens is
  // unverified (see docs/exapp-update-constraints.md 5a) — but a Setup tab whose
  // buttons all fail with a bare HTTP error says nothing about the cause, and
  // saying so costs one branch.
  it("explains a 404 as a stale registration rather than showing the status code", () => {
    expect(storagePanelSource).toContain("error.status === 404");
    expect(storagePanelSource).toContain("Re-register the app in Nextcloud");
  });
});
