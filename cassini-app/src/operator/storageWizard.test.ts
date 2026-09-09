import { describe, expect, it } from "vitest";

import { describeArchive, migrationFacts, modeCard, wizardNeeded } from "./storageWizard";
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
    overwrite_names: [],
    overwrite_required: false,
    adopting_destination: false,
    nothing_to_move: false,
    pending_cleanup: "",
    warnings: [],
    ...partial,
  };
}

describe("describeArchive", () => {
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
  it("offers a blocked mode as something to set up, not to switch to", () => {
    const card = modeCard(
      option({
        available: false,
        blocker: "the groupfolders app is not enabled",
        setup: [{ id: "app", action: "enable_app", title: "", args: {}, browser: false, occ: "", app_url: "" }],
      }),
    );
    expect(card.action).toBe("scaffold");
    expect(card.actionLabel).toBe("Set up default");
  });

  it("does not offer to set up a mode it has no steps for", () => {
    const card = modeCard(option({ available: false, blocker: "a Team folder is mounted over it" }));
    expect(card.action).toBe("blocked");
    expect(card.actionLabel).toBe("Not available yet");
  });

  it("offers an available mode with what is in it", () => {
    const card = modeCard(option({ available: true, archive: archive({ meetings: 3 }) }));
    expect(card.action).toBe("use");
    expect(card.contents).toBe("3 recordings");
  });
});

describe("wizardNeeded", () => {
  it("asks whenever nobody confirmed the mode, even when one is in force", () => {
    expect(wizardNeeded({ mode: "default", mode_confirmed: false } as StorageStatus)).toBe(true);
    expect(wizardNeeded({ mode: "", mode_confirmed: false } as StorageStatus)).toBe(true);
    expect(wizardNeeded({ mode: "default", mode_confirmed: true } as StorageStatus)).toBe(false);
  });

  it("asks nothing before the status has loaded", () => {
    expect(wizardNeeded(null)).toBe(false);
  });
});

describe("migrationFacts", () => {
  it("names destination artefacts that require overwrite confirmation", () => {
    const facts = migrationFacts(
      preview({ overwrite_required: true, overwrite_names: ["old.opus", "catalog.json"], meetings: 5 }),
    );
    expect(facts[0]).toBe("2 destination artefacts will be overwritten or removed.");
    expect(facts[1]).toBe("5 recordings will be copied across.");
  });

  it("says plainly when no archive moves", () => {
    expect(migrationFacts(preview())).toEqual(["Nothing moves. Only the storage mode changes."]);
  });

  it("says an existing first-choice archive is preserved", () => {
    expect(migrationFacts(preview({ adopting_destination: true, destination_meetings: 2 }))).toEqual([
      "The existing archive will be kept. Nothing will be overwritten, copied, or removed.",
    ]);
  });

  it("says nothing before a preview has arrived", () => {
    expect(migrationFacts(null)).toEqual([]);
  });
});
