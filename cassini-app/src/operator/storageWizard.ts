import type {
  StorageArchiveFacts,
  StorageConflictPolicy,
  StorageMigrationPolicy,
  StorageMigrationStrategy,
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

// carryChoiceNeeded is the spec's rule, read off the preview the operator
// computed.
//
// Neither half is guessed here. The operator listed both roots under its own
// lock, and it re-checks before it writes — this is the render of its answer,
// not a second opinion about it.
export function carryChoiceNeeded(preview: StorageTransitionPreview | null): boolean {
  return preview?.choice_required === true;
}

// conflictChoiceNeeded is the narrower half: `on_conflict` can only act on a
// name that exists under both roots, so it is offered only when one does.
export function conflictChoiceNeeded(preview: StorageTransitionPreview | null): boolean {
  return preview?.conflict_matters === true;
}

export const DEFAULT_MIGRATION_POLICY: StorageMigrationPolicy = {
  strategy: "merge",
  on_conflict: "skip",
};

export interface PolicyOption<T> {
  value: T;
  label: string;
  detail: string;
}

// The copy for the controls. It lives here rather than in the component because
// this is what the tests can read, and because a wrong description of
// `overwrite` is a description of a deletion.
export function strategyOptions(sourceRoot: string, destinationRoot: string): PolicyOption<StorageMigrationStrategy>[] {
  return [
    {
      value: "merge",
      label: "Copy them across",
      detail: `Everything in ${sourceRoot} is carried into ${destinationRoot}. Anything already there that the other folder does not have stays.`,
    },
    {
      value: "switch_only",
      label: "Leave them where they are",
      detail: `Nothing is copied. The recordings stay in ${sourceRoot}, which the new mode does not read, so they will not be listed until you carry them across later.`,
    },
    {
      value: "overwrite",
      label: `Replace what is in ${destinationRoot}`,
      detail: `${destinationRoot} is made to match ${sourceRoot} exactly. Recordings there that are not in ${sourceRoot} are deleted.`,
    },
  ];
}

export function conflictOptions(sourceRoot: string, destinationRoot: string): PolicyOption<StorageConflictPolicy>[] {
  return [
    {
      value: "skip",
      label: "Keep both",
      detail: `${destinationRoot} keeps its copy, and ${sourceRoot} keeps its own — so nothing is lost and ${sourceRoot} is not fully emptied.`,
    },
    {
      value: "newest_wins",
      label: "Keep whichever is newer",
      detail: "The copy that was written last survives, in whichever folder it is, and the other one is removed.",
    },
  ];
}

// migrationFacts is the confirmation's body: what THIS policy would do, in
// sentences, ordered most-consequential first.
//
// The numbers come from the operator's own plan, not from arithmetic here. A
// second implementation of the rules in the confirmation dialog is precisely how
// a dialog comes to promise something the operation does not do.
export function migrationFacts(preview: StorageTransitionPreview | null): string[] {
  if (!preview) {
    return [];
  }
  const out: string[] = [];
  if (preview.would_delete_at_destination > 0) {
    out.push(
      `${plural(preview.would_delete_at_destination, "recording")} in ${preview.destination_root} will be deleted.`,
    );
  }
  if (preview.would_copy > 0) {
    out.push(`${plural(preview.would_copy, "recording")} will be copied across.`);
  }
  if (preview.would_replace > 0) {
    out.push(`${plural(preview.would_replace, "recording")} will replace the copy already there.`);
  }
  if (preview.would_skip > 0) {
    out.push(
      `${plural(preview.would_skip, "recording")} already at ${preview.destination_root} will be left as it is.`,
    );
  }
  if (preview.would_keep_in_source > 0) {
    out.push(
      `${plural(preview.would_keep_in_source, "recording")} will also stay in ${preview.source_root}, so both copies survive.`,
    );
  }
  if (out.length === 0) {
    out.push("Nothing moves. Only the storage mode changes.");
  }
  return out;
}

function plural(count: number, noun: string): string {
  return count === 1 ? `1 ${noun}` : `${count} ${noun}s`;
}

// policyToSend decides what the PUT carries.
//
// `undefined` when the administrator was never asked, which is what lets the
// operator refuse rather than assume: a request carrying an answer is taken to
// have been answered by a person, and one carrying none is refused if a choice
// turns out to exist under the operator's own lock.
export function policyToSend(
  preview: StorageTransitionPreview | null,
  chosen: StorageMigrationPolicy,
): StorageMigrationPolicy | undefined {
  return carryChoiceNeeded(preview) ? chosen : undefined;
}
