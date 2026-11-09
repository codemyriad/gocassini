// The recordedAtUtc file migration, as the Doctor shows it: /status's
// recorded_at_migration (cassini-operator recorded_at_migration.go). The
// operator enumerates the past recordings whose delivered file lacks the
// recording time; nothing rewrites them until an administrator presses the
// button, and the same panel pauses it. The notification sentences are the
// operator's — it is the side that read Nextcloud's capabilities — so the panel
// shows them verbatim rather than rewording what it cannot check.

export type RecordedAtMigrationState =
  | "not_applicable" | "checking" | "needed" | "running" | "pausing" | "paused" | "blocked" | "done" | "not_needed" | "unknown";

export interface RecordedAtBlockedFile {
  meeting: string;
  title?: string;
  date_label?: string;
  reason: string;
}

export interface RecordedAtNotifications {
  known: boolean;
  error?: string;
  account: string;
  nextcloud_version?: string;
  activity_app?: boolean;
  notifications_app?: boolean;
  web_push?: boolean;
  versioning?: boolean;
  details: string[];
}

export interface RecordedAtMigration {
  applicable: boolean;
  state: RecordedAtMigrationState;
  checked: boolean;
  paused: boolean;
  needed: number;
  queued: number;
  settling: number;
  in_progress: number;
  done: number;
  blocked: number;
  blocked_items: RecordedAtBlockedFile[];
  workers: number;
  pace_seconds: number;
  notifications?: RecordedAtNotifications;
  error?: string;
}

const states = new Set<RecordedAtMigrationState>(["not_applicable", "checking", "needed", "running", "pausing", "paused", "blocked", "done", "not_needed", "unknown"]);
const count = (value: unknown) => (typeof value === "number" && Number.isFinite(value) && value > 0 ? Math.floor(value) : 0);
const text = (value: unknown) => (typeof value === "string" ? value : "");
const flag = (value: unknown) => (typeof value === "boolean" ? value : undefined);

// An operator that predates the migration sends no block at all: that reads as
// not_applicable, and the panel shows nothing.
export function normalizeRecordedAtMigration(raw: unknown): RecordedAtMigration {
  const value = (raw && typeof raw === "object" ? raw : {}) as Record<string, unknown>;
  const state = text(value.state) as RecordedAtMigrationState;
  const notices = value.notifications && typeof value.notifications === "object" ? value.notifications as Record<string, unknown> : null;
  return {
    applicable: value.applicable === true,
    state: states.has(state) ? state : value.applicable === true ? "unknown" : "not_applicable",
    checked: value.checked === true,
    paused: value.paused === true,
    needed: count(value.needed),
    queued: count(value.queued),
    settling: count(value.settling),
    in_progress: count(value.in_progress),
    done: count(value.done),
    blocked: count(value.blocked),
    blocked_items: Array.isArray(value.blocked_items)
      ? value.blocked_items.filter((item): item is Record<string, unknown> => !!item && typeof item === "object")
        .map(item => ({ meeting: text(item.meeting), title: text(item.title) || undefined, date_label: text(item.date_label) || undefined, reason: text(item.reason) }))
      : [],
    workers: count(value.workers),
    pace_seconds: count(value.pace_seconds),
    notifications: notices ? {
      known: notices.known === true,
      error: text(notices.error) || undefined,
      account: text(notices.account) || "cassini",
      nextcloud_version: text(notices.nextcloud_version) || undefined,
      activity_app: flag(notices.activity_app),
      notifications_app: flag(notices.notifications_app),
      web_push: flag(notices.web_push),
      versioning: flag(notices.versioning),
      details: Array.isArray(notices.details) ? notices.details.filter((line): line is string => typeof line === "string" && line.trim() !== "") : [],
    } : undefined,
    error: text(value.error) || undefined,
  };
}

const recordings = (n: number) => `${n} ${n === 1 ? "recording" : "recordings"}`;

// Whether the panel shows anything. Nothing to say where nothing applies, where
// no past recording needs it, and while the first count is still running with
// nothing found yet.
export function migrationVisible(m: RecordedAtMigration | null): m is RecordedAtMigration {
  return !!m && m.state !== "not_applicable" && m.state !== "not_needed" && m.state !== "checking";
}

// The rewrite is under way (or settling): the panel re-reads by itself.
export function migrationActive(m: RecordedAtMigration): boolean {
  return m.state === "running" || m.state === "pausing";
}

export type MigrationAction = "start" | "pause";

export interface MigrationButton {
  action: MigrationAction;
  label: string;
}

// The one button for the state, or none.
export function migrationButton(m: RecordedAtMigration): MigrationButton | null {
  switch (m.state) {
    case "needed": return { action: "start", label: m.blocked > 0 ? `Write the recording time into ${recordings(m.needed + m.blocked)}` : `Write the recording time into ${recordings(m.needed)}` };
    case "running": return { action: "pause", label: "Pause" };
    case "paused": return { action: "start", label: "Resume" };
    case "blocked": return { action: "start", label: m.blocked === 1 ? "Retry the blocked recording" : `Retry ${m.blocked} blocked recordings` };
    default: return null;
  }
}

// The sentence the panel leads with.
export function migrationSummary(m: RecordedAtMigration): string {
  const left = m.queued + m.settling + m.in_progress;
  switch (m.state) {
    case "needed":
      return `${recordings(m.needed)} ${m.needed === 1 ? "was" : "were"} published before Cassini wrote the recording time into the file itself. Browse already shows the right time; writing it into the ${m.needed === 1 ? "file" : "files"} makes it travel with downloads, the Nextcloud apps and the cassini command line.` +
        (m.checked ? "" : " Cassini is still counting, so this number may grow.");
    case "running":
      return `Writing the recording time into ${recordings(left)}${m.done > 0 ? `; ${m.done} done` : ""}. Cassini works on ${m.workers > 1 ? `up to ${m.workers} at a time` : "one at a time"}, ${m.pace_seconds} seconds apart, so it stays out of Nextcloud's way.`;
    case "pausing":
      return `Pausing. ${m.in_progress === 1 ? "The recording being written now finishes first" : `The ${m.in_progress} recordings being written now finish first`}, so no file is left half-written; ${recordings(m.queued + m.settling)} then wait.`;
    case "paused":
      return `Paused with ${recordings(m.queued + m.settling)} left${m.done > 0 ? ` and ${m.done} done` : ""}. Nothing is being written until you resume.` +
        (m.settling > 0 ? ` ${m.settling === 1 ? "One upload that was already sent is" : `${m.settling} uploads that were already sent are`} still confirmed, so ${m.settling === 1 ? "its file is" : "their files are"} not left half-written.` : "");
    case "blocked":
      return `${recordings(m.blocked)} could not be written and ${m.blocked === 1 ? "needs" : "need"} you${m.done > 0 ? `; ${m.done} ${m.done === 1 ? "is" : "are"} done` : ""}. Fix what each one says, then retry.`;
    case "done":
      return `Every past recording carries its recording time in the file itself (${m.done} written).`;
    case "unknown":
      return m.error ?? "Cassini could not read the migration's progress.";
    default:
      return "";
  }
}

// What was established about Nextcloud, as short labelled facts. Absent when
// the capabilities could not be read: the sentences say so instead.
export function notificationFacts(n: RecordedAtNotifications | undefined): Array<{ label: string; value: string }> {
  if (!n || !n.known) return [];
  const on = (value: boolean | undefined) => (value === undefined ? "Unknown" : value ? "Enabled" : "Not enabled");
  const facts = [
    { label: "File versioning", value: on(n.versioning) },
    { label: "Activity app", value: on(n.activity_app) },
    { label: "Notifications app", value: n.notifications_app && n.web_push ? "Enabled, with browser push" : on(n.notifications_app) },
  ];
  if (n.nextcloud_version) facts.push({ label: "Nextcloud", value: n.nextcloud_version });
  return facts;
}
