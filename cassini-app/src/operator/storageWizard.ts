import type {
  StorageArchiveFacts,
  StorageModeOption,
  StorageStatus,
  StorageTransitionPreview,
} from "./types";

// The decisions behind the setup wizard, apart from the component that renders
// them (D-708).
//
// The split is the one setupHealth.ts already uses, and for the same reason:
// `.svelte` files in this repo are tested by reading their source text, which
// cannot say anything about a stepped flow's behaviour. Everything here is
// ordinary TypeScript with ordinary unit tests, and SetupWizard.svelte renders
// what it returns.
//
//	  1 Review      what is on this Nextcloud, for BOTH models
//	  2 Choose      a mode. Available -> use it; blocked -> scaffold it first
//	  3 Carry over  ONLY when the choice would change the outcome
//	  4 Confirm     the preview's facts, then one click
//
// Step 3 is the whole of the spec's "do not show the config when there is no
// choice to be made". A confirmation that asks about something with one possible
// answer is a confirmation nobody reads.

export type WizardStep = "review" | "choose" | "carry" | "confirm";

// WizardAction is what a mode's button does, and it is the spec's rule about
// prerequisites made explicit: a mode whose prerequisites are missing can only
// be SCAFFOLDED, and the switch appears once they are met.
export type WizardAction = "use" | "scaffold" | "blocked";

export interface WizardModeCard {
  mode: StorageModeOption["mode"];
  label: string;
  summary: string;
  active: boolean;
  available: boolean;
  action: WizardAction;
  actionLabel: string;
  // contents is what is in this mode's root right now, in one phrase. It is the
  // fact the choice actually turns on.
  contents: string;
  // blocker is the operator's sentence about what is missing. Empty when the
  // mode is available.
  blocker: string;
  root: string;
}

// describeArchive says what is in a root, and refuses to call an unread one
// empty.
//
// "We could not look" and "there is nothing there" are the same zero, and every
// version of this feature that conflated them produced a screen that told an
// administrator their recordings did not exist. The wizard's first screen is the
// one place that matters most: it is where somebody decides which archive is the
// real one.
export function describeArchive(archive: StorageArchiveFacts): string {
  if (!archive.probed) {
    return "Cassini could not read this folder, so it cannot say what is in it";
  }
  if (!archive.present) {
    return "not created yet — Cassini makes it when it needs it";
  }
  if (archive.meetings === 0) {
    return "empty";
  }
  return archive.meetings === 1 ? "1 recording" : `${archive.meetings} recordings`;
}

// modeCard turns one operator-supplied mode into the card the wizard renders.
export function modeCard(option: StorageModeOption): WizardModeCard {
  let action: WizardAction = option.available ? "use" : "blocked";
  if (!option.available && option.setup.length > 0) {
    action = "scaffold";
  }
  let actionLabel = `Use ${option.label.toLowerCase()}`;
  if (action === "scaffold") {
    actionLabel = `Set up ${option.label.toLowerCase()}`;
  } else if (action === "blocked") {
    actionLabel = "Not available yet";
  }
  return {
    mode: option.mode,
    label: option.label,
    summary: option.summary,
    active: option.active,
    available: option.available,
    action,
    actionLabel,
    contents: describeArchive(option.archive),
    blocker: option.blocker,
    root: option.root,
  };
}

export function modeCards(status: StorageStatus | null): WizardModeCard[] {
  return (status?.modes ?? []).map(modeCard);
}

// wizardNeeded reports whether the Setup tab should ask rather than present.
//
// It is `mode_confirmed`, not `mode === ""`. An install carrying a mode a
// previous build recorded on its own, or one an interrupted first decision left
// behind, has a mode and has not been asked — and presenting that as a settled
// choice is exactly what this whole change removes.
export function wizardNeeded(status: StorageStatus | null): boolean {
  return status !== null && !status.mode_confirmed;
}

// migrationFacts is the fixed overwrite migration in administrator-facing
// language. The operator supplies every count and enforces confirmation again
// under its own lock.
export function migrationFacts(preview: StorageTransitionPreview | null): string[] {
  if (!preview) {
    return [];
  }
  if (preview.adopting_destination) {
    return ["The existing archive will be kept. Nothing will be overwritten, copied, or removed."];
  }
  const out: string[] = [];
  if (preview.overwrite_required) {
    out.push(
      `${plural(preview.overwrite_names.length, "destination artefact")} will be overwritten or removed.`,
    );
  }
  if (preview.meetings > 0) out.push(`${plural(preview.meetings, "recording")} will be copied across.`);
  if (out.length === 0) {
    out.push("Nothing moves. Only the storage mode changes.");
  }
  return out;
}

function plural(count: number, noun: string): string {
  return count === 1 ? `1 ${noun}` : `${count} ${noun}s`;
}
