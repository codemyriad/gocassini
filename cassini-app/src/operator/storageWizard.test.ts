import { describe, expect, it } from "vitest";

import {
  carryChoiceNeeded,
  conflictChoiceNeeded,
  describeArchive,
  migrationFacts,
  modeCard,
  policyToSend,
  strategyOptions,
  wizardNeeded,
} from "./storageWizard";
import type {
  StorageArchiveFacts,
  StorageModeOption,
  StorageStatus,
  StorageTransitionPreview,
} from "./types";

function archive(partial: Partial<StorageArchiveFacts> = {}): StorageArchiveFacts {
  return { probed: true, present: true, meetings: 0, catalog: false, ...partial };
}

function option(partial: Partial<StorageModeOption> = {}): StorageModeOption {
  return {
    mode: "default",
    label: "Default",
    active: false,
    available: true,
    summary: "",
    consequence: "",
    blocker: "",
    step: "",
    instructions: [],
    setup: [],
    root: "CassiniNoACL/Recordings",
    archive: archive(),
    ...partial,
  };
}

function preview(partial: Partial<StorageTransitionPreview> = {}): StorageTransitionPreview {
  return {
    mode: "access_controlled",
    ready: true,
    step: "",
    detail: "",
    source_root: "CassiniNoACL/Recordings",
    destination_root: "Cassini/Recordings",
    source_readable: true,
    destination_readable: true,
    meetings: 0,
    catalog_present: false,
    destination_meetings: 0,
    nothing_to_move: false,
    strategy: "merge",
    on_conflict: "skip",
    choice_required: false,
    strategy_matters: false,
    conflict_matters: false,
    conflict_names: [],
    conflicts: 0,
    would_copy: 0,
    would_replace: 0,
    would_skip: 0,
    would_keep_in_source: 0,
    would_delete_at_destination: 0,
    pending_cleanup: "",
    warnings: [],
    ...partial,
  };
}

describe("describeArchive", () => {
  // The failure this exists to prevent: an unread folder rendering as an empty
  // one on the screen where somebody decides which archive is the real one.
  it("never calls a folder it could not read empty", () => {
    expect(describeArchive(archive({ probed: false }))).toContain("could not read");
    expect(describeArchive(archive({ probed: false }))).not.toContain("empty");
  });

  it("tells an absent folder apart from an empty one", () => {
    expect(describeArchive(archive({ present: false }))).toContain("not created yet");
    expect(describeArchive(archive({ present: true, meetings: 0 }))).toBe("empty");
  });

  it("counts what is there", () => {
    expect(describeArchive(archive({ meetings: 1 }))).toBe("1 recording");
    expect(describeArchive(archive({ meetings: 41 }))).toBe("41 recordings");
  });
});

describe("modeCard", () => {
  // The spec's rule: a mode whose prerequisites are missing can ONLY be
  // scaffolded, and the switch appears once they are met.
  it("offers a blocked mode as something to set up, not to switch to", () => {
    const card = modeCard(
      option({
        available: false,
        blocker: "the groupfolders app is not enabled",
        setup: [
          { id: "app", action: "enable_app", title: "", args: {}, browser: false, occ: "", app_url: "" },
        ],
      }),
    );
    expect(card.action).toBe("scaffold");
    expect(card.actionLabel).toBe("Set up default");
    expect(card.blocker).toBe("the groupfolders app is not enabled");
  });

  // A blocker with no step is a dead end and has to look like one, rather than
  // offering a button that would succeed and change nothing.
  it("does not offer to set up a mode it has no steps for", () => {
    const card = modeCard(option({ available: false, blocker: "a Team folder is mounted over it" }));
    expect(card.action).toBe("blocked");
    expect(card.actionLabel).toBe("Not available yet");
  });

  it("offers an available mode as something to use, with what is in it", () => {
    const card = modeCard(option({ available: true, archive: archive({ meetings: 3 }) }));
    expect(card.action).toBe("use");
    expect(card.contents).toBe("3 recordings");
  });
});

describe("wizardNeeded", () => {
  // Not `mode === ""`. An install carrying a mode a previous build recorded on
  // its own has a mode and has never been asked, and presenting that as a
  // settled choice is what this whole change removes.
  it("asks whenever nobody confirmed the mode, even when one is in force", () => {
    expect(wizardNeeded({ mode: "default", mode_confirmed: false } as StorageStatus)).toBe(true);
    expect(wizardNeeded({ mode: "", mode_confirmed: false } as StorageStatus)).toBe(true);
    expect(wizardNeeded({ mode: "default", mode_confirmed: true } as StorageStatus)).toBe(false);
  });

  it("asks nothing before the status has loaded", () => {
    expect(wizardNeeded(null)).toBe(false);
  });
});

describe("the carry-over controls", () => {
  // The spec's IMPORTANT: no controls when there is no choice to be made.
  it("stays hidden when the operator found nothing to decide", () => {
    expect(carryChoiceNeeded(preview({ choice_required: false }))).toBe(false);
    expect(conflictChoiceNeeded(preview({ conflict_matters: false }))).toBe(false);
  });

  it("appears when recordings are in both folders", () => {
    expect(carryChoiceNeeded(preview({ choice_required: true, strategy_matters: true }))).toBe(true);
  });

  // The conflict rule is the narrower half: it can only act on a name that is in
  // both, so it is offered only then — a strategy question does not drag it in.
  it("offers the conflict rule only when the same recording is in both", () => {
    expect(
      conflictChoiceNeeded(preview({ choice_required: true, strategy_matters: true, conflict_matters: false })),
    ).toBe(false);
    expect(conflictChoiceNeeded(preview({ choice_required: true, conflict_matters: true }))).toBe(true);
  });

  it("says nothing at all before a preview has arrived", () => {
    expect(carryChoiceNeeded(null)).toBe(false);
    expect(conflictChoiceNeeded(null)).toBe(false);
  });

  // Sending a default unconditionally would defeat the operator's own refusal to
  // pick one for a conflict nobody was shown.
  it("sends a policy only when the administrator was asked for one", () => {
    const chosen = { strategy: "overwrite", on_conflict: "skip" } as const;
    expect(policyToSend(preview({ choice_required: false }), chosen)).toBeUndefined();
    expect(policyToSend(preview({ choice_required: true }), chosen)).toEqual(chosen);
  });

  it("describes overwrite as the deletion it is", () => {
    const overwrite = strategyOptions("A", "B").find((entry) => entry.value === "overwrite");
    expect(overwrite?.detail).toContain("deleted");
  });
});

describe("migrationFacts", () => {
  // Most consequential first, and the deletion leads.
  it("leads with what would be deleted", () => {
    const facts = migrationFacts(
      preview({ would_delete_at_destination: 2, would_copy: 5, would_replace: 1 }),
    );
    expect(facts[0]).toContain("2 recordings in Cassini/Recordings will be deleted");
    expect(facts[1]).toContain("5 recordings will be copied");
  });

  it("names the copies that survive in both places", () => {
    const facts = migrationFacts(preview({ would_skip: 1, would_keep_in_source: 1 }));
    expect(facts.join(" ")).toContain("both copies survive");
  });

  it("says plainly when nothing moves", () => {
    expect(migrationFacts(preview())).toEqual(["Nothing moves. Only the storage mode changes."]);
  });

  it("says nothing before a preview has arrived", () => {
    expect(migrationFacts(null)).toEqual([]);
  });
});
