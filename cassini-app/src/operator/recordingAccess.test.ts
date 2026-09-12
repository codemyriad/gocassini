import { describe, expect, it } from "vitest";

import {
  accessOptions,
  appsInUse,
  checkedAgo,
  doneMessage,
  existingRecordingsLine,
  installButtonLabel,
  missingApps,
  modeSourceLabel,
  needsPrerequisites,
  occRecipe,
  preparingTitle,
  recordingCount,
  requiredApps,
  storageCheckLine,
  storageLocation,
  switchConfirmation,
  switchSteps,
  switchingLead,
  switchingTitle,
  totalRecordingCount,
} from "./recordingAccess";
import type { StorageModeOption, StorageSetupStep, StorageStatus } from "./types";

// Who can see recordings, as sentences (D-757 / D-758).
//
// These are the strings an administrator reads before moving an entire
// published archive, and most of them carry a number. They are asserted here
// rather than grepped for in the component, because a claim about an archive is
// worth a test that can be wrong.

function appStep(app: string): StorageSetupStep {
  return {
    id: `app:${app}`,
    action: "enable_app",
    title: `Install and enable the ${app} app`,
    args: { app },
    browser: false,
    occ: `occ app:install ${app} && occ app:enable ${app}`,
    app_url: "/settings/apps",
  };
}

function browserStep(id: string, occ: string): StorageSetupStep {
  return { id, action: "create_group", title: id, args: {}, browser: true, occ, app_url: "" };
}

function modeOption(over: Partial<StorageModeOption> & { mode: StorageModeOption["mode"] }): StorageModeOption {
  return {
    label: over.mode === "access_controlled" ? "Access controlled" : "Default",
    active: false,
    available: true,
    summary: "",
    consequence: "",
    blocker: "",
    step: "",
    instructions: [],
    setup: [],
    root: over.mode === "access_controlled" ? "Cassini/Recordings" : "CassiniNoACL/Recordings",
    archive: { probed: true, present: true, meetings: 0, catalog: true },
    ...over,
  };
}

function statusOf(over: Partial<StorageStatus> = {}): StorageStatus {
  return {
    mode: "default",
    mode_source: "resolved_on_enable",
    mode_confirmed: true,
    awaiting_choice: false,
    service_account: { user: "cassini", known: true, exists: true, reset_occ: "occ user:resetpassword cassini" },
    migration_clean: true,
    pending_cleanup: "",
    stranded_root: "",
    stranded_recordings: 0,
    ok: true,
    state: "provisioned",
    step: "",
    detail: "",
    checked_at: "",
    modes: [
      modeOption({ mode: "default", active: true, archive: { probed: true, present: true, meetings: 2, catalog: true } }),
      modeOption({ mode: "access_controlled", archive: { probed: true, present: false, meetings: 0, catalog: false } }),
    ],
    transition: null,
    installs: [],
    preview: null,
    first_run: false,
    migration: null,
    ...over,
  };
}

describe("the two options", () => {
  it("names the audiences, never the enum", () => {
    const options = accessOptions(statusOf());
    expect(options.map((option) => option.title)).toEqual([
      "Everyone with a Nextcloud account",
      "Meeting participants",
    ]);
    expect(options[0].description).toBe(
      "Anyone with an account on this Nextcloud can see every recording and the name of the room it came from. Works with nothing extra installed.",
    );
    expect(options[1].description).toBe(
      "Only the people who were in a call can see its recording. Needs two Nextcloud apps: Team folders and Everyone Group.",
    );
  });

  it("marks the rule in force", () => {
    expect(accessOptions(statusOf()).map((o) => o.current)).toEqual([true, false]);
    expect(accessOptions(statusOf({ mode: "access_controlled" })).map((o) => o.current)).toEqual([
      false,
      true,
    ]);
  });

  // A mode nothing has resolved is not "default". Marking one there would claim
  // a rule nobody recorded.
  it("marks neither while no mode is resolved", () => {
    expect(accessOptions(statusOf({ mode: "" })).map((o) => o.current)).toEqual([false, false]);
    expect(accessOptions(null).map((o) => o.current)).toEqual([false, false]);
  });
});

describe("the existing-recordings line", () => {
  it("counts the archive the mode in force actually reads", () => {
    expect(existingRecordingsLine(statusOf())).toBe(
      "You have 2 recordings. Anyone with a Nextcloud account can see them.",
    );
  });

  it("uses the singular for one recording", () => {
    const status = statusOf({
      modes: [
        modeOption({ mode: "default", active: true, archive: { probed: true, present: true, meetings: 1, catalog: true } }),
        modeOption({ mode: "access_controlled" }),
      ],
    });
    expect(existingRecordingsLine(status)).toBe(
      "You have 1 recording. Anyone with a Nextcloud account can see them.",
    );
  });

  it("says who can see them under Meeting participants", () => {
    const status = statusOf({
      mode: "access_controlled",
      modes: [
        modeOption({ mode: "default" }),
        modeOption({
          mode: "access_controlled",
          active: true,
          archive: { probed: true, present: true, meetings: 134, catalog: true },
        }),
      ],
    });
    expect(existingRecordingsLine(status)).toBe(
      "You have 134 recordings. Only the people in each call can see them.",
    );
  });

  // The one fact an administrator will forget and be surprised by later: a
  // switch does not narrow the recordings that already exist.
  it("says the pre-switch recordings are still visible to everyone", () => {
    const status = statusOf({
      mode: "access_controlled",
      modes: [
        modeOption({ mode: "default" }),
        modeOption({
          mode: "access_controlled",
          active: true,
          archive: { probed: true, present: true, meetings: 2, catalog: true },
        }),
      ],
    });
    expect(existingRecordingsLine(status, true)).toBe(
      "You have 2 recordings from before the switch. Anyone with a Nextcloud account can still see them. New recordings are visible to their participants only.",
    );
  });

  // "You have 0 recordings" is a sentence about an absence. The audience is
  // still worth saying, in the tense that fits.
  it("says there are none yet rather than counting nothing", () => {
    const empty = statusOf({
      modes: [
        modeOption({ mode: "default", active: true, archive: { probed: true, present: true, meetings: 0, catalog: true } }),
        modeOption({ mode: "access_controlled" }),
      ],
    });
    expect(existingRecordingsLine(empty)).toBe(
      "No recordings yet. Anyone with a Nextcloud account will be able to see them.",
    );
    const participants = statusOf({
      mode: "access_controlled",
      modes: [
        modeOption({ mode: "default" }),
        modeOption({
          mode: "access_controlled",
          active: true,
          archive: { probed: true, present: false, meetings: 0, catalog: false },
        }),
      ],
    });
    expect(existingRecordingsLine(participants)).toBe(
      "No recordings yet. Only the people in each call will be able to see them.",
    );
    // Including on the way out of a switch that moved nothing.
    expect(existingRecordingsLine(participants, true)).toBe(
      "No recordings yet. Only the people in each call will be able to see them.",
    );
  });

  // A rule nothing has resolved has no audience to name, and naming the
  // fallback would assert it as though it were in force.
  it("says nothing while no mode is resolved", () => {
    expect(existingRecordingsLine(statusOf({ mode: "" }))).toBe("");
    expect(existingRecordingsLine(null)).toBe("");
  });

  // A root nobody could list reads as zero on the wire. "You have 0 recordings"
  // would be a statement of fact made from a question nobody managed to ask.
  it("never states a count nobody could take", () => {
    const status = statusOf({
      modes: [
        modeOption({ mode: "default", active: true, archive: { probed: false, present: false, meetings: 0, catalog: false } }),
        modeOption({ mode: "access_controlled" }),
      ],
    });
    expect(recordingCount(status).known).toBe(false);
    expect(existingRecordingsLine(status)).toBe(
      "Anyone with a Nextcloud account can see the recordings you already have.",
    );
  });
});

describe("the prerequisite checklist", () => {
  const missingOne = statusOf({
    modes: [
      modeOption({ mode: "default", active: true }),
      modeOption({
        mode: "access_controlled",
        available: false,
        setup: [appStep("group_everyone"), browserStep("folder", "occ groupfolders:create Cassini")],
      }),
    ],
  });

  it("reads what is missing from the operator's own plan", () => {
    expect(requiredApps(missingOne)).toEqual([
      { id: "groupfolders", name: "Team folders", installed: true },
      { id: "group_everyone", name: "Everyone Group", installed: false },
    ]);
    expect(missingApps(missingOne).map((app) => app.name)).toEqual(["Everyone Group"]);
  });

  it("names the one app in the button, and both when both are missing", () => {
    expect(installButtonLabel(missingApps(missingOne))).toBe("Install Everyone Group");
    const both = statusOf({
      modes: [
        modeOption({ mode: "default", active: true }),
        modeOption({
          mode: "access_controlled",
          available: false,
          setup: [appStep("groupfolders"), appStep("group_everyone")],
        }),
      ],
    });
    expect(installButtonLabel(missingApps(both))).toBe("Install both apps");
  });

  // The checklist is about the two apps and nothing else. A mode whose only
  // outstanding steps are Cassini's own (the folder, the mappings, the ACL) is
  // not a question for an administrator.
  it("is shown only for Meeting participants, and only for a missing app", () => {
    expect(needsPrerequisites(missingOne, "access_controlled")).toBe(true);
    expect(needsPrerequisites(missingOne, "default")).toBe(false);
    const foldersOnly = statusOf({
      modes: [
        modeOption({ mode: "default", active: true }),
        modeOption({
          mode: "access_controlled",
          available: false,
          setup: [browserStep("folder", "occ groupfolders:create Cassini")],
        }),
      ],
    });
    expect(needsPrerequisites(foldersOnly, "access_controlled")).toBe(false);
  });
});

describe("confirming a switch", () => {
  it("says what happens to new and to existing recordings, going in", () => {
    expect(switchConfirmation(statusOf(), "access_controlled")).toEqual({
      title: "Switch to Meeting participants?",
      lines: [
        "New recordings will only be visible to the people who were in each call.",
        "Your 2 existing recordings stay visible to everyone.",
      ],
      pause:
        "Recording pauses while the switch runs, usually under a minute. You can close this page.",
      confirmLabel: "Switch",
      danger: false,
    });
  });

  // The widening direction is the one dialog allowed to look dangerous, and its
  // button says the number.
  it("says the number in the button, coming back out", () => {
    const status = statusOf({
      mode: "access_controlled",
      modes: [
        modeOption({ mode: "default", archive: { probed: true, present: true, meetings: 4, catalog: true } }),
        modeOption({
          mode: "access_controlled",
          active: true,
          archive: { probed: true, present: true, meetings: 130, catalog: true },
        }),
      ],
    });
    expect(totalRecordingCount(status)).toEqual({ known: true, count: 134 });
    expect(switchConfirmation(status, "default")).toEqual({
      title: "Switch to Everyone with a Nextcloud account?",
      lines: [
        "All 134 recordings, including the ones currently limited to their participants, will become visible to anyone with an account on this Nextcloud.",
        "Switching back later won't re-limit them.",
      ],
      pause: "Recording pauses while the switch runs, usually a few minutes for this many.",
      confirmLabel: "Make 134 recordings visible to everyone",
      danger: true,
    });
  });

  // The widening button says the number that is about to become visible, so a
  // total taken from the one root somebody could list is not a total: it would
  // say 134 while 136 were about to be widened.
  it("refuses a total while either root is unprobed", () => {
    const half = statusOf({
      mode: "access_controlled",
      modes: [
        modeOption({ mode: "default", archive: { probed: false, present: false, meetings: 0, catalog: false } }),
        modeOption({
          mode: "access_controlled",
          active: true,
          archive: { probed: true, present: true, meetings: 134, catalog: true },
        }),
      ],
    });
    expect(totalRecordingCount(half)).toEqual({ known: false, count: 0 });
    expect(switchConfirmation(half, "default").confirmLabel).toBe(
      "Make every recording visible to everyone",
    );
    expect(totalRecordingCount(null)).toEqual({ known: false, count: 0 });
  });

  it("counts one existing recording in the singular", () => {
    const one = statusOf({
      modes: [
        modeOption({ mode: "default", active: true, archive: { probed: true, present: true, meetings: 1, catalog: true } }),
        modeOption({ mode: "access_controlled" }),
      ],
    });
    expect(switchConfirmation(one, "access_controlled").lines[1]).toBe(
      "Your 1 existing recording stays visible to everyone.",
    );
  });

  it("drops the number rather than inventing one nobody counted", () => {
    const unprobed = statusOf({
      modes: [
        modeOption({ mode: "default", active: true, archive: { probed: false, present: false, meetings: 0, catalog: false } }),
        modeOption({ mode: "access_controlled", archive: { probed: false, present: false, meetings: 0, catalog: false } }),
      ],
    });
    expect(switchConfirmation(unprobed, "access_controlled").lines[1]).toBe(
      "Your existing recordings stay visible to everyone.",
    );
    expect(switchConfirmation(unprobed, "default").confirmLabel).toBe(
      "Make every recording visible to everyone",
    );
  });

  it("says what it did, in the words the section will be rendering", () => {
    expect(doneMessage("access_controlled")).toBe(
      "Done. New recordings are visible to their participants only.",
    );
    expect(doneMessage("default")).toBe(
      "Done. New recordings are visible to anyone with a Nextcloud account.",
    );
  });
});

describe("while the switch runs", () => {
  it("walks the operator's own order, one step at a time", () => {
    const steps = switchSteps({ active: true, phase: "switching", done: 12, total: 12 });
    expect(steps.map((step) => step.label)).toEqual([
      "Copy recordings to the new location",
      "Check every file arrived",
      "Switch the rule",
      "Remove the old copies",
    ]);
    expect(steps.map((step) => step.state)).toEqual(["done", "done", "now", "pending"]);
  });

  it("counts recordings on the step that copies them, and nowhere else", () => {
    const steps = switchSteps({ active: true, phase: "copying", done: 3, total: 134 });
    expect(steps[0].count).toBe("3 of 134");
    expect(steps.slice(1).every((step) => step.count === "")).toBe(true);
    expect(steps.map((step) => step.state)).toEqual(["now", "pending", "pending", "pending"]);
  });

  // The PUT blocks for the whole move, so the first poll has not answered yet
  // when the panel opens. Nothing is finished at that moment, and the panel
  // must not say anything is.
  it("shows nothing as finished before the first progress arrives", () => {
    expect(switchSteps(null).map((step) => step.state)).toEqual([
      "now",
      "pending",
      "pending",
      "pending",
    ]);
    expect(switchingLead(null)).toBe("You can close this page; the switch carries on.");
  });

  it("names the count and the permission to walk away", () => {
    expect(switchingLead({ active: true, phase: "copying", done: 0, total: 2 })).toBe(
      "Moving 2 recordings. You can close this page; the switch carries on.",
    );
  });

  // The browser's half comes first and does NOT survive a closed tab, so it is
  // a separate line from the one that says the switch carries on.
  it("names what this browser is building, before the operator moves anything", () => {
    expect(preparingTitle("access_controlled")).toBe("Preparing the Team folder…");
    expect(preparingTitle("default")).toBe("Preparing the cassini account…");
    expect(preparingTitle(null)).toBe("Preparing the cassini account…");
    expect(preparingTitle("access_controlled")).not.toContain("close this page");
  });

  it("names where the switch is going, and stays quiet when it cannot", () => {
    expect(switchingTitle("access_controlled")).toBe("Switching to Meeting participants");
    expect(switchingTitle("default")).toBe("Switching to Everyone with a Nextcloud account");
    expect(switchingTitle(null)).toBe("Switching who can see recordings");
  });
});

describe("details for administrators", () => {
  it("says where recordings are and what kind of place that is", () => {
    expect(storageLocation(statusOf())).toEqual({
      root: "CassiniNoACL/Recordings",
      container: "the cassini account's own files",
    });
    const participants = statusOf({
      mode: "access_controlled",
      modes: [modeOption({ mode: "default" }), modeOption({ mode: "access_controlled", active: true })],
    });
    expect(storageLocation(participants)).toEqual({
      root: "Cassini/Recordings",
      container: "a Team folder",
    });
    expect(appsInUse(statusOf())).toBe("None");
    expect(appsInUse(participants)).toBe("Team folders, Everyone Group");
  });

  // The one place an administrator can see that nobody chose the rule. The new
  // value is the operator resolving it from what it found on enable (D-753) —
  // not a decision somebody took, and not a fallback either.
  it("maps every mode source to a plain label", () => {
    expect(modeSourceLabel("resolved_on_enable")).toBe("Set when Cassini was enabled");
    expect(modeSourceLabel("user")).toBe("Chosen here");
    expect(modeSourceLabel("env")).toBe("Declared by a deploy option (development/CI)");
    expect(modeSourceLabel("migrating")).toBe("Left by an interrupted switch");
    expect(modeSourceLabel("default")).toBe("A fallback an older version recorded");
    expect(modeSourceLabel("derived")).toBe("Detected from this Nextcloud by an older version");
    expect(modeSourceLabel("configured")).toBe("Recorded, but Cassini cannot say by whom");
    expect(modeSourceLabel("")).toBe("Not recorded yet");
  });

  it("says the verdict and when it was taken", () => {
    const now = new Date("2026-09-11T10:00:00Z");
    expect(storageCheckLine(statusOf({ checked_at: "2026-09-11T09:58:00Z" }), now)).toBe(
      "OK, checked 2 minutes ago",
    );
    expect(storageCheckLine(statusOf({ ok: false, checked_at: "2026-09-11T09:00:00Z" }), now)).toBe(
      "Not working, checked 1 hour ago",
    );
  });

  it("drops the clause rather than guessing at a time", () => {
    expect(checkedAgo("", new Date())).toBe("");
    expect(checkedAgo("not a time", new Date())).toBe("");
    expect(storageCheckLine(statusOf({ checked_at: "" }))).toBe("OK");
  });

  it("reads a fresh check and an old one in the units they happened in", () => {
    const now = new Date("2026-09-11T10:00:00Z");
    expect(checkedAgo("2026-09-11T09:59:30Z", now)).toBe("just now");
    expect(checkedAgo("2026-09-11T09:59:00Z", now)).toBe("1 minute ago");
    expect(checkedAgo("2026-09-11T07:00:00Z", now)).toBe("3 hours ago");
    expect(checkedAgo("2026-09-09T10:00:00Z", now)).toBe("2 days ago");
  });

  // The recipe is the operator's own plan, one line per step it would perform.
  // Written twice, a printed command and the button that replaces it come apart.
  it("prints the operator's plan rather than a recipe of its own", () => {
    const status = statusOf({
      modes: [
        modeOption({ mode: "default", active: true, setup: [browserStep("group", "occ group:add cassini")] }),
        modeOption({
          mode: "access_controlled",
          available: false,
          setup: [
            browserStep("group", "occ group:add cassini"),
            appStep("group_everyone"),
            browserStep("folder", "occ groupfolders:create Cassini"),
          ],
        }),
      ],
    });
    expect(occRecipe(status)).toEqual([
      "occ group:add cassini",
      "occ app:install group_everyone && occ app:enable group_everyone",
      "occ groupfolders:create Cassini",
    ]);
    expect(occRecipe(statusOf())).toEqual([]);
  });
});
