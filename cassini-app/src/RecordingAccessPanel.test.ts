import { describe, expect, it } from "vitest";

import panelSource from "./RecordingAccessPanel.svelte?raw";
import settingsPanelSource from "./SettingsPanel.svelte?raw";

// Operator › Settings › Who can see recordings (D-757, D-758).
//
// Svelte components are not mounted in this repo, so the section's load-bearing
// decisions are asserted against its source. The sentences that carry a number
// live in operator/recordingAccess.ts and are asserted there, as values; what
// is pinned here is the copy that is markup, and the properties a refactor
// could quietly lose — each of which would make the switch either unusable or
// dangerous.

describe("the section", () => {
  it("is a section of the Settings panel, above the pipeline it applies to", () => {
    expect(settingsPanelSource).toContain(
      'import RecordingAccessPanel from "./RecordingAccessPanel.svelte"',
    );
    expect(settingsPanelSource).toContain("<RecordingAccessPanel {operatorClient} />");
    expect(settingsPanelSource.indexOf("<RecordingAccessPanel")).toBeLessThan(
      settingsPanelSource.indexOf("Publish pipeline</h2>"),
    );
  });

  it("leads with who can see a recording", () => {
    expect(panelSource).toContain("<h2 class=\"font-semibold\">Who can see recordings</h2>");
    expect(panelSource).toContain("Applies to every recording Cassini publishes to this Nextcloud.");
  });

  it("offers the two audiences as one radiogroup, with the current one marked", () => {
    expect(panelSource).toContain('role="radiogroup"');
    expect(panelSource).toContain('role="radio"');
    expect(panelSource).toContain("aria-checked={option.current}");
    expect(panelSource).toContain('<span class="badge badge-primary badge-sm">Current</span>');
    // The names and the sentence under each are values, not markup: they are
    // the same two strings the chip and the first-run dialog use.
    expect(panelSource).toContain("{option.title}");
    expect(panelSource).toContain("{option.description}");
    expect(panelSource).toContain("accessOptions(status)");
  });

  // "default" and "access_controlled" are the operator's vocabulary. They may
  // appear in the technical disclosure and nowhere else.
  it("keeps the enum inside Details for administrators", () => {
    expect(panelSource).toContain("Details for administrators");
    const beforeDetails = panelSource.slice(
      panelSource.indexOf("<section class="),
      panelSource.indexOf("Details for administrators"),
    );
    expect(beforeDetails).not.toContain("access_controlled");
    expect(beforeDetails).not.toContain("Access controlled");
  });

  it("states what the existing recordings are visible to, permanently", () => {
    expect(panelSource).toContain("{existingLine}");
    expect(panelSource).toContain("existingRecordingsLine(status, switched)");
    // Not inside the success branch: the sentence is true whether or not a
    // switch just happened, and it is the one an administrator forgets.
    expect(panelSource.indexOf("{existingLine}")).toBeGreaterThan(panelSource.indexOf("{#if done}"));
    expect(panelSource).toContain('role="radiogroup"');
  });
});

describe("choosing the other option", () => {
  it("never switches on one click", () => {
    // choose() only opens a panel; only confirmSwitch calls the operator. A
    // card wired straight to putStorage would move every recording in the
    // instance on one click.
    expect(panelSource).toContain("function choose(mode: AccessMode)");
    expect(panelSource).toContain("async function confirmSwitch()");
    expect(panelSource).toContain("on:click={() => choose(option.mode)}");
    expect(panelSource).toContain("on:click={confirmSwitch}");
    const putCalls = panelSource.match(/operatorClient\.putStorage\(/g) ?? [];
    expect(putCalls).toHaveLength(1);
    const choose = panelSource.slice(
      panelSource.indexOf("function choose(mode: AccessMode)"),
      panelSource.indexOf("function cancel()"),
    );
    expect(choose).not.toContain("putStorage");
    expect(choose).toContain("needsPrerequisites(status, mode)");
  });

  // The whole app runs inside a shadow root on Nextcloud's embedded page, where
  // the top layer is the one thing whose styling and focus behaviour do not
  // reliably follow it.
  it("asks inline rather than in a <dialog>", () => {
    expect(panelSource).toContain('role="alertdialog"');
    expect(panelSource).not.toContain("<dialog");
    expect(panelSource).not.toContain("showModal");
  });

  it("lists the two apps, with the instance-wide effect before the install", () => {
    expect(panelSource).toContain("Two Nextcloud apps are needed");
    expect(panelSource).toContain('{app.installed ? "Installed" : "Not installed"}');
    expect(panelSource).toContain(
      "Cassini can install it for you. If Nextcloud refuses, install it from Nextcloud's Apps",
    );
    expect(panelSource).toContain("page; Cassini notices when it is there.");
    expect(panelSource).toContain(
      "Everyone Group adds a group called <b>Everyone</b> to the whole of Nextcloud. It shows up",
    );
    expect(panelSource).toContain("when sharing files in other apps too, not only in Cassini.");
  });

  // Cassini's own steps are one sentence, not a checklist: the administrator is
  // not being asked to do any of them.
  it("does not itemise the steps Cassini performs itself", () => {
    expect(panelSource).toContain("Cassini does the rest itself while the switch runs:");
    expect(panelSource).not.toContain("{#each pending.setup");
    expect(panelSource).not.toContain("{#each option.setup");
  });

  it("offers Install as the primary action and Nextcloud's own Apps page beside it", () => {
    expect(panelSource).toContain("{installButtonLabel(missing)}");
    expect(panelSource).toContain('class="btn btn-sm btn-primary"');
    expect(panelSource).toContain('href={nextcloudUrl("/settings/apps")}');
    expect(panelSource).toContain("Open Nextcloud Apps");
    expect(panelSource).toContain("on:click={installApps}");
  });

  // The install is the operator's action, and it can only be believed after the
  // operator has looked again: an app installed from Nextcloud's own Apps page
  // happens outside Cassini entirely.
  it("re-checks after installing, rather than assuming it worked", () => {
    const install = panelSource.slice(
      panelSource.indexOf("async function installApps()"),
      panelSource.indexOf("async function confirmSwitch()"),
    );
    expect(install).toContain("operatorClient.installStorageApps()");
    expect(install.indexOf("installStorageApps()")).toBeLessThan(
      install.indexOf("recheckStorage()"),
    );
  });

  it("renders the confirmation the module wrote, in both directions", () => {
    expect(panelSource).toContain("switchConfirmation(status, target ?? PARTICIPANTS)");
    expect(panelSource).toContain("{confirmation.title}");
    expect(panelSource).toContain("{#each confirmation.lines as line");
    expect(panelSource).toContain("{confirmation.pause}");
    expect(panelSource).toContain("{confirmation.confirmLabel}");
    // The widening direction is the one dialog allowed to look dangerous.
    expect(panelSource).toContain("confirmation.danger");
    expect(panelSource).toContain("'btn-error'");
  });

  // The 409 guard stays in the API. The section does not offer a way past it:
  // a destination that already holds recordings is the "both places" state, and
  // it is resolved before anyone reaches a switch.
  it("never offers to overwrite, and never lists what would be", () => {
    expect(panelSource).toContain("operatorClient.putStorage(mode === PARTICIPANTS)");
    expect(panelSource).not.toContain("confirm_overwrite");
    expect(panelSource).not.toContain("overwrite_names");
    expect(panelSource).not.toContain("overwrite_required");
    expect(panelSource).not.toContain("previewStorageSwitch");
  });

  it("shows the operator's own refusal and stops", () => {
    const confirm = panelSource.slice(
      panelSource.indexOf("async function confirmSwitch()"),
      panelSource.indexOf("async function resume()"),
    );
    expect(confirm).toContain("actionError = asMessage(error);");
    expect(confirm).toContain("flow = null;");
    // Re-read rather than trusting the pre-switch snapshot: a transition that
    // fails AFTER moving the archive has already changed the mode in force.
    expect(confirm).toContain("operatorClient.getStorage()");
  });
});

describe("while the switch runs", () => {
  // The PUT blocks for the length of the move, so progress is a second,
  // parallel reader of the same operator.
  it("polls for progress alongside the call that is doing the work", () => {
    expect(panelSource).toContain("const POLL_MS = 2000;");
    expect(panelSource).toContain("function pollMigration(");
    const confirm = panelSource.slice(
      panelSource.indexOf("async function confirmSwitch()"),
      panelSource.indexOf("async function resume()"),
    );
    expect(confirm.indexOf("stopPoll = pollMigration(null)")).toBeLessThan(
      confirm.indexOf("operatorClient.putStorage("),
    );
    expect(confirm).toContain("stopPoll?.();");
  });

  // A snapshot taken mid-move must not replace the status: the PUT's own answer
  // is the authoritative one, and the poll is only reading progress.
  it("lets the poll touch the progress and nothing else", () => {
    const poll = panelSource.slice(
      panelSource.indexOf("function pollMigration("),
      panelSource.indexOf("function watchRunningSwitch()"),
    );
    expect(poll).toContain("migration = snapshot.migration;");
    expect(poll).not.toContain("status = snapshot");
  });

  it("stops polling when the component goes away", () => {
    const destroy = panelSource.slice(
      panelSource.indexOf("onDestroy(() => {"),
      panelSource.indexOf("async function load()"),
    );
    expect(destroy).toContain("stopPoll?.();");
  });

  it("names the four steps and the permission to close the page", () => {
    expect(panelSource).toContain("{switchingTitle(target)}");
    expect(panelSource).toContain("{switchingLead(migration)}");
    expect(panelSource).toContain("{#each steps as step (step.phase)}");
    expect(panelSource).toContain("{step.label}");
    expect(panelSource).toContain("{step.count}");
    expect(panelSource).toContain('step.state === "done"');
    expect(panelSource).toContain('step.state === "now"');
  });

  // The move survives a closed tab. A section that said nothing about it on the
  // way back would be the tab's failure, not the switch's.
  it("picks up a switch that was already running when the page opened", () => {
    expect(panelSource).toContain("function watchRunningSwitch()");
    expect(panelSource).toContain("status?.migration == null");
    const load = panelSource.slice(
      panelSource.indexOf("async function load()"),
      panelSource.indexOf("async function recheck()"),
    );
    expect(load).toContain("watchRunningSwitch();");
  });
});

describe("an interrupted switch", () => {
  // The archive is COMPLETE at the mode in force — the operator copies before
  // it flips, and only clears afterwards — so this is a tidy-up with one
  // button, not a card about a root nobody reads.
  it("is one line and one button, wired to the operator's own repair", () => {
    expect(panelSource).toContain("{#if !status.migration_clean}");
    expect(panelSource).toContain("A switch didn't finish.");
    expect(panelSource).toContain("on:click={resume}");
    expect(panelSource).toContain("operatorClient.finishStorageMigration()");
    expect(panelSource).toMatch(/{:else}\s+Resume\s+{\/if}/);
    // The card it replaces named the leftover root and explained it.
    expect(panelSource).not.toContain("pending_cleanup");
    expect(panelSource).not.toContain("stranded_root");
  });
});

describe("details for administrators", () => {
  it("is collapsed, and holds the technical answers", () => {
    expect(panelSource).toContain("<details");
    expect(panelSource).not.toContain("<details open");
    for (const label of [
      "Recordings are stored in",
      "Nextcloud apps in use",
      "Storage check",
      "The rule Cassini has recorded",
      "Service account",
      "Do it by hand instead",
    ]) {
      expect(panelSource).toContain(label);
    }
    expect(panelSource).toContain("{appsInUse(status)}");
    expect(panelSource).toContain("{checkLine}");
    expect(panelSource).toContain("{modeSourceLabel(status.mode_source)}");
    expect(panelSource).toContain("{place.root}");
    expect(panelSource).toContain("{place.container}");
  });

  it("links the full report at the operator's own status route", () => {
    expect(panelSource).toContain("`${loadConfig().operatorBasePath}/status`");
    expect(panelSource).toContain(">Full report</a");
  });

  it("says what the service account is for, and offers the one password action", () => {
    expect(panelSource).toContain("Recordings are written and read by a Nextcloud account called");
    expect(panelSource).toContain("Cassini doesn't need its password and");
    expect(panelSource).toContain("doesn't keep one.");
    expect(panelSource).toContain("Set a password");
    expect(panelSource).toContain("if you want to sign in as it.");
    expect(panelSource).toContain("resetServiceAccountPassword(user)");
  });

  // There is no second chance at that string: it is minted in this browser and
  // never reaches the operator, so it is shown once, and everything else is
  // disabled until it is acknowledged.
  it("shows a minted password through the one component that does that", () => {
    expect(panelSource).toContain('import PasswordReveal from "./PasswordReveal.svelte"');
    expect(panelSource).toContain("<PasswordReveal");
    expect(panelSource).toContain("on:acknowledge={() => (credential = null)}");
    expect(panelSource).toContain("credential !== null");
  });

  it("prints the operator's own recipe, and only when there is one", () => {
    expect(panelSource).toContain("{#if occ.length > 0}");
    expect(panelSource).toContain('occ.join(\n                    "\\n",\n                  )');
    expect(panelSource).toContain("occRecipe(status)");
  });

  it("says when this build cannot make the changes itself", () => {
    expect(panelSource).toContain("{#if !setupAvailable}");
    expect(panelSource).toContain("This build cannot make these changes itself.");
    expect(panelSource).toContain("isSetupAvailable()");
  });
});

describe("the rest of the app", () => {
  it("never reloads the page", () => {
    for (const forbidden of ["location.reload", "window.location.href ="]) {
      expect(panelSource).not.toContain(forbidden);
    }
  });

  // App.svelte reads its setup health once, at mount. Everything here can
  // change that health, so every successful action says so — in the same
  // session, with no reload.
  it("tells the shell after every action that changed this Nextcloud", () => {
    expect(panelSource).toContain('import { notifySetupChanged } from "./operator/setupSignal"');
    for (const action of ["async function installApps()", "async function resume()"]) {
      const body = panelSource.slice(
        panelSource.indexOf(action),
        panelSource.indexOf("}", panelSource.indexOf("} catch", panelSource.indexOf(action))),
      );
      expect(body).toContain("notifySetupChanged();");
    }
    const confirm = panelSource.slice(
      panelSource.indexOf("async function confirmSwitch()"),
      panelSource.indexOf("async function resume()"),
    );
    expect(confirm).toContain("done = doneMessage(mode);");
    // The last one is the switch's own: the earlier one belongs to the app
    // install that did not finish, which is a different outcome.
    expect(confirm.lastIndexOf("notifySetupChanged();")).toBeGreaterThan(
      confirm.indexOf("operatorClient.putStorage("),
    );
  });

  // The apps-first ordering and the plan recompute live in
  // operator/runModeSetup.ts, because the order inside them is load-bearing in
  // a way that is easy to get wrong twice. They are asserted there as
  // behaviour, which is strictly better than as source text.
  it("builds what a mode needs through the shared sequence rather than its own", () => {
    expect(panelSource).toContain('from "./operator/runModeSetup"');
    expect(panelSource).toContain("runModeSetup(operatorClient, option, (message)");
    expect(panelSource).not.toContain("runSetupPlan(");
  });

  it("decides nothing a unit test could not read back", () => {
    // Every sentence carrying a number comes from the module beside it. A
    // number formatted in the markup is a claim no test can reach.
    expect(panelSource).toContain('from "./operator/recordingAccess"');
    expect(panelSource).not.toMatch(/recordings\.length/);
    expect(panelSource).not.toContain("archive.meetings");
  });
});
