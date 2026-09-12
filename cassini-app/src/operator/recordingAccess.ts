import type { StorageMigration, StorageMode, StorageStatus } from "./types";

// Who can see recordings, as sentences (D-757 / D-758).
//
// Every string an administrator reads in the settings section is either here or
// in the operator's own response. It is here rather than in the component
// because .svelte files are not mounted in this repo's tests, and these
// sentences carry NUMBERS and a mode each: "2 recordings stay visible to
// everyone" is a claim about an archive, and a claim about an archive is worth
// a unit test rather than a grep for a substring.
//
// The vocabulary is fixed (D-751): "Everyone with a Nextcloud account" and
// "Meeting participants", never the enum names, which appear only under
// "Details for administrators" as a plain label.

// AccessMode is a mode somebody can actually choose. `""` — no mode resolved —
// is a state of the instance, not an option, so it is not in this union.
export type AccessMode = Exclude<StorageMode, "">;

export const EVERYONE: AccessMode = "default";
export const PARTICIPANTS: AccessMode = "access_controlled";

export const EVERYONE_TITLE = "Everyone with a Nextcloud account";
export const PARTICIPANTS_TITLE = "Meeting participants";

export interface AccessOptionView {
  mode: AccessMode;
  title: string;
  description: string;
  // current marks the rule in force. Both are false while no mode is resolved,
  // which is honest: marking one would claim a rule nothing has recorded.
  current: boolean;
}

const OPTION_COPY: readonly { mode: AccessMode; title: string; description: string }[] = [
  {
    mode: EVERYONE,
    title: EVERYONE_TITLE,
    description:
      "Anyone with an account on this Nextcloud can see every recording and the name of the room it came from. Works with nothing extra installed.",
  },
  {
    mode: PARTICIPANTS,
    title: PARTICIPANTS_TITLE,
    description:
      "Only the people who were in a call can see its recording. Needs two Nextcloud apps: Team folders and Everyone Group.",
  },
];

export function accessOptions(status: StorageStatus | null): AccessOptionView[] {
  return OPTION_COPY.map((option) => ({ ...option, current: status?.mode === option.mode }));
}

export function modeTitle(mode: StorageMode): string {
  if (mode === PARTICIPANTS) {
    return PARTICIPANTS_TITLE;
  }
  return EVERYONE_TITLE;
}

// Counting recordings. `probed` is load-bearing in both directions: a root
// nobody could list reads as zero on the wire, and "You have 0 recordings" is a
// statement of fact made from a question nobody managed to ask.
export interface RecordingCount {
  known: boolean;
  count: number;
}

export function recordingCount(status: StorageStatus | null): RecordingCount {
  const active = status?.modes.find((option) => option.active) ?? null;
  if (!active || !active.archive.probed) {
    return { known: false, count: 0 };
  }
  return { known: true, count: active.archive.meetings };
}

// totalRecordingCount is every recording this Nextcloud holds, in both roots.
// The widening direction names it, because everything in both roots ends up
// readable by every account — not only what the mode in force can see today.
export function totalRecordingCount(status: StorageStatus | null): RecordingCount {
  const rows = status?.modes ?? [];
  // Every row, or no number at all: a total taken from the one root somebody
  // could list is not a total. The danger button would say 134 while 136 are
  // about to become visible, which is the one number in this section that must
  // not be short.
  if (rows.length === 0 || rows.some((option) => !option.archive.probed)) {
    return { known: false, count: 0 };
  }
  return { known: true, count: rows.reduce((sum, option) => sum + option.archive.meetings, 0) };
}

export function plural(n: number, noun: string): string {
  return n === 1 ? `1 ${noun}` : `${n} ${noun}s`;
}

// existingRecordingsLine is the sentence that sits under the two options, and
// it is the one an administrator will otherwise be surprised by later: a switch
// to Meeting participants does NOT narrow the recordings that already exist.
//
// `switched` means this page has just performed a switch, which is the only
// moment the app can tell "before" from "after" — the operator records no
// per-recording audience, so there is nothing to read back on a later load.
export function existingRecordingsLine(status: StorageStatus | null, switched = false): string {
  const { known, count } = recordingCount(status);
  const mode = status?.mode ?? "";
  if (mode === "") {
    // Nothing has resolved a rule, so there is no audience to name. Saying
    // "anyone with a Nextcloud account can see them" here would assert the
    // rule that happens to be the fallback as though it were in force.
    return "";
  }
  if (!known) {
    if (mode === PARTICIPANTS) {
      return "Only the people in each call can see the recordings you already have.";
    }
    return "Anyone with a Nextcloud account can see the recordings you already have.";
  }
  if (count === 0) {
    // "You have 0 recordings" is a sentence about an absence. The audience is
    // still worth saying, in the tense that fits: it is what the next recording
    // gets.
    if (mode === PARTICIPANTS) {
      return "No recordings yet. Only the people in each call will be able to see them.";
    }
    return "No recordings yet. Anyone with a Nextcloud account will be able to see them.";
  }
  const have = `You have ${plural(count, "recording")}`;
  if (mode === PARTICIPANTS && switched) {
    return `${have} from before the switch. Anyone with a Nextcloud account can still see them. New recordings are visible to their participants only.`;
  }
  if (mode === PARTICIPANTS) {
    return `${have}. Only the people in each call can see them.`;
  }
  return `${have}. Anyone with a Nextcloud account can see them.`;
}

// doneMessage is what the switch says when it worked, in the same words the
// section will be rendering a moment later.
export function doneMessage(mode: AccessMode): string {
  if (mode === PARTICIPANTS) {
    return "Done. New recordings are visible to their participants only.";
  }
  return "Done. New recordings are visible to anyone with a Nextcloud account.";
}

// --- The two Nextcloud apps -------------------------------------------------
//
// Their ids are what occ and the App Store want; their names are what an
// administrator sees. Both are compile-time constants in the operator
// (nc_storage_setup.go), so they are literals here too.

export interface RequiredApp {
  id: string;
  name: string;
  installed: boolean;
}

const REQUIRED_APPS: readonly { id: string; name: string }[] = [
  { id: "groupfolders", name: "Team folders" },
  { id: "group_everyone", name: "Everyone Group" },
];

// requiredApps reads the plan the operator emitted for Meeting participants: an
// app with an `enable_app` step is one that is not there. Read from the plan
// rather than from `installs`, which reports only what a past install ATTEMPT
// produced and is empty on an instance nobody has tried to install on.
export function requiredApps(status: StorageStatus | null): RequiredApp[] {
  const option = status?.modes.find((row) => row.mode === PARTICIPANTS) ?? null;
  const missing = new Set(
    (option?.setup ?? [])
      .filter((step) => step.action === "enable_app")
      .map((step) => step.args.app ?? ""),
  );
  return REQUIRED_APPS.map((app) => ({ ...app, installed: !missing.has(app.id) }));
}

export function missingApps(status: StorageStatus | null): RequiredApp[] {
  return requiredApps(status).filter((app) => !app.installed);
}

// needsPrerequisites is the gate on the checklist panel: it is shown only for
// Meeting participants, and only while an app is actually missing. Everything
// else the mode needs (the Team folder, its mappings, the ACL, the manager) is
// done by the browser during the switch and is one sentence, not a checklist.
export function needsPrerequisites(status: StorageStatus | null, target: AccessMode): boolean {
  return target === PARTICIPANTS && missingApps(status).length > 0;
}

export function installButtonLabel(apps: RequiredApp[]): string {
  if (apps.length === 1) {
    return `Install ${apps[0].name}`;
  }
  return "Install both apps";
}

// --- Confirming a switch ----------------------------------------------------

export interface SwitchConfirmation {
  title: string;
  // lines are the consequences, most surprising first. Rendered in order.
  lines: string[];
  // pause is the third sentence: what happens while it runs.
  pause: string;
  confirmLabel: string;
  // danger marks the direction that WIDENS access. It is the one dialog allowed
  // to look dangerous, and its button says the number.
  danger: boolean;
}

const PAUSE_SHORT =
  "Recording pauses while the switch runs, usually under a minute. You can close this page.";
const PAUSE_LONG = "Recording pauses while the switch runs, usually a few minutes for this many.";

export function switchConfirmation(
  status: StorageStatus | null,
  target: AccessMode,
): SwitchConfirmation {
  if (target === PARTICIPANTS) {
    const { known, count } = recordingCount(status);
    return {
      title: "Switch to Meeting participants?",
      lines: [
        "New recordings will only be visible to the people who were in each call.",
        known
          ? `Your ${count} existing ${count === 1 ? "recording stays" : "recordings stay"} visible to everyone.`
          : "Your existing recordings stay visible to everyone.",
      ],
      pause: PAUSE_SHORT,
      confirmLabel: "Switch",
      danger: false,
    };
  }
  const { known, count } = totalRecordingCount(status);
  return {
    title: "Switch to Everyone with a Nextcloud account?",
    lines: [
      known
        ? `All ${plural(count, "recording")}, including the ones currently limited to their participants, will become visible to anyone with an account on this Nextcloud.`
        : "All recordings, including the ones currently limited to their participants, will become visible to anyone with an account on this Nextcloud.",
      "Switching back later won't re-limit them.",
    ],
    pause: PAUSE_LONG,
    confirmLabel: known
      ? `Make ${plural(count, "recording")} visible to everyone`
      : "Make every recording visible to everyone",
    danger: true,
  };
}

// --- While it runs ----------------------------------------------------------

export type MigrationPhase = StorageMigration["phase"];

export type SwitchStepState = "done" | "now" | "pending";

export interface SwitchStep {
  phase: MigrationPhase;
  label: string;
  // count is the copy step's progress, empty on every other step. The operator
  // counts recordings, so this is recordings.
  count: string;
  state: SwitchStepState;
}

// The four steps are the operator's own order (copy, verify, flip, clear), and
// saying them in that order is what makes an interruption honest: whichever
// step it stopped at, a complete archive exists somewhere.
const SWITCH_STEPS: readonly { phase: MigrationPhase; label: string }[] = [
  { phase: "copying", label: "Copy recordings to the new location" },
  { phase: "verifying", label: "Check every file arrived" },
  { phase: "switching", label: "Switch the rule" },
  { phase: "clearing", label: "Remove the old copies" },
];

export function switchSteps(migration: StorageMigration | null): SwitchStep[] {
  const current = Math.max(
    0,
    SWITCH_STEPS.findIndex((step) => step.phase === migration?.phase),
  );
  return SWITCH_STEPS.map((step, index) => ({
    phase: step.phase,
    label: step.label,
    count:
      step.phase === "copying" && migration !== null && migration.total > 0
        ? `${migration.done} of ${migration.total}`
        : "",
    state: index < current ? "done" : index === current ? "now" : "pending",
  }));
}

// preparingTitle is the browser's own half of a switch, which happens BEFORE
// the operator is asked to move anything: the Team folder and its mappings, or
// the service account. It is a separate line from switchingTitle because
// closing the tab here aborts it, and the page must not be offering to be
// closed yet.
export function preparingTitle(target: AccessMode | null): string {
  if (target === PARTICIPANTS) {
    return "Preparing the Team folder…";
  }
  return "Preparing the cassini account…";
}

// switchingTitle names where the switch is going. Null is the switch this page
// did not start and came back to: nothing on the wire says which mode it is
// heading for, and guessing would put the wrong audience on the screen.
export function switchingTitle(target: AccessMode | null): string {
  if (target === null) {
    return "Switching who can see recordings";
  }
  return `Switching to ${modeTitle(target)}`;
}

// switchingLead is the sentence under it: the count, and the permission to walk
// away. The move runs in the operator and survives a closed tab.
export function switchingLead(migration: StorageMigration | null): string {
  const total = migration?.total ?? 0;
  if (total <= 0) {
    return "You can close this page; the switch carries on.";
  }
  return `Moving ${plural(total, "recording")}. You can close this page; the switch carries on.`;
}

// --- Details for administrators ---------------------------------------------

// storageLocation is where recordings are kept, and what kind of place that is.
// The root comes from the operator rather than from a constant here: it is the
// layer that knows the Team folder's mount point.
export function storageLocation(status: StorageStatus | null): { root: string; container: string } {
  const active = status?.modes.find((option) => option.active) ?? null;
  const participants = status?.mode === PARTICIPANTS;
  return {
    root: active?.root ?? "",
    container: participants ? "a Team folder" : "the cassini account's own files",
  };
}

export function appsInUse(status: StorageStatus | null): string {
  return status?.mode === PARTICIPANTS ? "Team folders, Everyone Group" : "None";
}

// modeSourceLabel says where the rule came from, in words. It is the one place
// an administrator can see that nobody chose it.
//
// "resolved_on_enable" is the value D-753 writes when the operator resolves the
// mode from what it found on the instance: not a decision somebody took, and
// not a fallback either, which is why it says when rather than who.
export function modeSourceLabel(source: string): string {
  if (source === "user") return "Chosen here";
  if (source === "resolved_on_enable") return "Set when Cassini was enabled";
  if (source === "env") return "Declared by a deploy option (development/CI)";
  if (source === "migrating") return "Left by an interrupted switch";
  if (source === "default") return "A fallback an older version recorded";
  if (source === "derived") return "Detected from this Nextcloud by an older version";
  if (source === "configured") return "Recorded, but Cassini cannot say by whom";
  return source || "Not recorded yet";
}

// storageCheckLine is the health row: the verdict, and when it was taken. An
// unreadable or absent timestamp drops the clause rather than guessing at one.
export function storageCheckLine(status: StorageStatus | null, now: Date = new Date()): string {
  if (status === null) {
    return "";
  }
  const verdict = status.ok ? "OK" : "Not working";
  const ago = checkedAgo(status.checked_at, now);
  return ago === "" ? verdict : `${verdict}, checked ${ago}`;
}

export function checkedAgo(checkedAt: string, now: Date = new Date()): string {
  const at = Date.parse(checkedAt);
  if (!Number.isFinite(at)) {
    return "";
  }
  const seconds = Math.floor((now.getTime() - at) / 1000);
  if (seconds < 0) {
    // A clock that disagrees with the operator's is not worth a sentence about
    // the future.
    return "just now";
  }
  if (seconds < 60) {
    return "just now";
  }
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) {
    return `${plural(minutes, "minute")} ago`;
  }
  const hours = Math.floor(minutes / 60);
  if (hours < 24) {
    return `${plural(hours, "hour")} ago`;
  }
  return `${plural(Math.floor(hours / 24), "day")} ago`;
}

// occRecipe is the same change as commands, for an administrator who would
// rather do it by hand. It is the operator's own plan — one line per step it
// would perform — rather than a recipe written twice, which is how a printed
// command comes to disagree with what the button does.
export function occRecipe(status: StorageStatus | null): string[] {
  const lines: string[] = [];
  for (const option of status?.modes ?? []) {
    for (const step of option.setup) {
      if (step.occ !== "" && !lines.includes(step.occ)) {
        lines.push(step.occ);
      }
    }
  }
  return lines;
}
