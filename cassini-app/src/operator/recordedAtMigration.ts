import { get } from "svelte/store";
import { formatCount, LL } from "cassini-viewer/i18n";

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
  // web_push: Nextcloud's "webpush" (web push allowed, for apps with their
  // own push distributor); browser_push: "webpush-browsers", the separate
  // switch that lets browsers use it too.
  web_push?: boolean;
  browser_push?: boolean;
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
      browser_push: flag(notices.browser_push),
      versioning: flag(notices.versioning),
      details: Array.isArray(notices.details) ? notices.details.filter((line): line is string => typeof line === "string" && line.trim() !== "") : [],
    } : undefined,
    error: text(value.error) || undefined,
  };
}

const copy = () => get(LL).app.operator.recorded_at_migration;
// "1 recording" or "3 recordings", for a count inside a longer sentence.
const recordings = (n: number) => (n === 1 ? copy().recordings_one() : copy().recordings_other({ count: formatCount(n) }));

// Whether the panel shows anything. Nothing to say where nothing applies or
// where no past recording needs it. While the first count is still running
// with nothing found yet, one line says so.
export function migrationVisible(m: RecordedAtMigration | null): m is RecordedAtMigration {
  return !!m && m.state !== "not_applicable" && m.state !== "not_needed";
}

// The rewrite is under way (or settling): the panel re-reads by itself.
export function migrationActive(m: RecordedAtMigration): boolean {
  return m.state === "running" || m.state === "pausing";
}

// The first count has not finished: the panel re-reads, more slowly, until
// it has, so the count and the button do not go stale.
export function migrationCounting(m: RecordedAtMigration): boolean {
  return m.applicable && (!m.checked || m.state === "checking");
}

export type MigrationAction = "start" | "pause";

export interface MigrationButton {
  action: MigrationAction;
  label: string;
  // A start waits for the first count to finish: the operator refuses it
  // before then, since it would cover only part of the archive.
  disabled: boolean;
}

// The one button for the state, or none.
export function migrationButton(m: RecordedAtMigration): MigrationButton | null {
  const t = copy().button;
  const start = (label: string): MigrationButton => ({ action: "start", label, disabled: !m.checked });
  switch (m.state) {
    case "needed": {
      const n = m.blocked > 0 ? m.needed + m.blocked : m.needed;
      return start(n === 1 ? t.write_one() : t.write_other({ count: formatCount(n) }));
    }
    case "running": return { action: "pause", label: t.pause(), disabled: false };
    case "paused": return start(t.resume());
    case "blocked": return start(m.blocked === 1 ? t.retry_one() : t.retry_other({ count: formatCount(m.blocked) }));
    default: return null;
  }
}

// The sentence the panel leads with.
export function migrationSummary(m: RecordedAtMigration): string {
  const t = copy().summary;
  const left = m.queued + m.settling + m.in_progress;
  switch (m.state) {
    case "needed": {
      const lead = m.needed === 1 ? t.needed_one() : t.needed_other({ count: formatCount(m.needed) });
      return m.checked ? lead : `${lead} ${t.still_counting()}`;
    }
    case "checking":
      return t.checking();
    case "running": {
      const progress = m.done > 0
        ? left === 1 ? t.running_one_done({ done: formatCount(m.done) }) : t.running_other_done({ count: formatCount(left), done: formatCount(m.done) })
        : left === 1 ? t.running_one() : t.running_other({ count: formatCount(left) });
      const pace = m.workers > 1
        ? t.pace_many({ workers: formatCount(m.workers), seconds: formatCount(m.pace_seconds) })
        : t.pace_one({ seconds: formatCount(m.pace_seconds) });
      return `${progress} ${pace}`;
    }
    case "pausing": {
      // In progress, or an upload sent before the pause still being settled.
      const started = m.in_progress + m.settling;
      const waiting = recordings(m.queued);
      return started === 1 ? t.pausing_one({ waiting }) : t.pausing_other({ count: formatCount(started), waiting });
    }
    case "paused": {
      const remaining = recordings(m.queued + m.settling);
      return m.done > 0 ? t.paused_done({ remaining, done: formatCount(m.done) }) : t.paused({ remaining });
    }
    case "blocked": {
      const one = m.blocked === 1;
      if (m.done === 0) return one ? t.blocked_one() : t.blocked_other({ count: formatCount(m.blocked) });
      if (m.done === 1) return one ? t.blocked_one_done_one() : t.blocked_other_done_one({ count: formatCount(m.blocked) });
      return one ? t.blocked_one_done_other({ done: formatCount(m.done) }) : t.blocked_other_done_other({ count: formatCount(m.blocked), done: formatCount(m.done) });
    }
    case "done":
      return t.done({ done: formatCount(m.done) });
    case "unknown":
      // The operator's own error, in English, when it sent one.
      return m.error ?? t.unknown();
    default:
      return "";
  }
}

// What was established about Nextcloud, as short labelled facts. Absent when
// the capabilities could not be read: the sentences say so instead.
export function notificationFacts(n: RecordedAtNotifications | undefined): Array<{ label: string; value: string }> {
  if (!n || !n.known) return [];
  const t = copy().facts;
  const on = (value: boolean | undefined) => (value === undefined ? t.unknown() : value ? t.enabled() : t.not_enabled());
  const facts: Array<{ label: string; value: string }> = [
    { label: t.versioning(), value: on(n.versioning) },
    { label: t.activity_app(), value: on(n.activity_app) },
    { label: t.notifications_app(), value: on(n.notifications_app) },
  ];
  if (n.notifications_app && n.web_push !== undefined) {
    facts.push({ label: t.web_push(), value: n.browser_push ? t.web_push_browsers() : n.web_push ? t.web_push_apps() : t.web_push_off() });
  }
  if (n.nextcloud_version) facts.push({ label: "Nextcloud", value: n.nextcloud_version });
  return facts;
}
