// What Browse tells a reader about their own recording shares (D-885): the
// recordings waiting for them to accept, and the ones a Nextcloud sharing
// setting kept from them. The copy and the wire call live here, pure, for the
// reason buildSetupNotice gives: the sentences are the part worth testing, and
// SharingNotice.svelte renders what it is handed.
import { readServedMessage } from "../servedMessage";
import { isRecord, readSharing, type PendingRecording, type SharingStatus, type UnpublishedRecording } from "./setupHealth";

// hasSharingNews is the one rule for whether there is anything to say: the
// shell uses it to fill the list's empty state, and the notice uses it to
// render nothing otherwise. An unchecked answer says nothing — the counts are
// zero because nothing was established, not because nothing is waiting.
export function hasSharingNews(sharing: SharingStatus | null | undefined): boolean {
  if (!sharing || !sharing.checked) return false;
  return sharing.pending.length > 0 || sharing.refused > 0 || sharing.unpublished.length > 0;
}

// withoutDismissed takes out the unpublished recordings the reader dismissed
// in this page, before the operator's next /setup leaves them out itself.
export function withoutDismissed(sharing: SharingStatus | null, dismissed: ReadonlySet<string>): SharingStatus | null {
  if (!sharing || dismissed.size === 0) return sharing;
  return { ...sharing, unpublished: sharing.unpublished.filter((entry) => !dismissed.has(entry.jobId)) };
}

// The paths a Browse list entry may carry its meeting by. A meeting's files
// are named by its job id ("<job id>.opus"), so their stem is the job id.
export interface ListedMeeting {
  id: string;
  audioPath?: string;
  meetingPath?: string;
  artifactPath?: string;
}

// listedJobIds is every job id the reader's list shows a meeting for. An
// unpublished recording found here was published since — a rerun after the
// sharing setting was fixed — and the reader can open it, so it is not news.
export function listedJobIds(meetings: readonly ListedMeeting[]): Set<string> {
  const ids = new Set<string>();
  for (const meeting of meetings) {
    if (meeting.id) ids.add(meeting.id);
    for (const ref of [meeting.audioPath, meeting.meetingPath, meeting.artifactPath]) {
      const stem = fileStem(ref);
      if (stem) ids.add(stem);
    }
  }
  return ids;
}

function fileStem(ref: string | undefined): string {
  if (!ref) return "";
  const path = ref.split(/[?#]/)[0].replace(/\/+$/, "");
  let base = path.slice(path.lastIndexOf("/") + 1);
  try {
    base = decodeURIComponent(base);
  } catch {
    // Kept as written: a name that is not valid percent-encoding is its own.
  }
  return base.replace(/\.(opus|json)$/i, "");
}

function recordings(count: number): string {
  return count === 1 ? "1 recording" : `${count} recordings`;
}

// --- Pending (contract §5, revised in §8) ---

export function pendingTitle(count: number): string {
  return count === 1 ? "1 recording is waiting for you" : `${count} recordings are waiting for you`;
}

export const PENDING_LINE = "Nextcloud holds files shared with you until you accept them.";
export const SHOW_THEM = "Show them";
export const HIDE_THEM = "Hide them";

// The primary button on the first line. One recording has nothing to say
// "all" about: the button accepts it.
export function acceptAllLabel(count: number): string {
  return count === 1 ? "Accept" : `Accept all ${count}`;
}

export const WHY_ACCEPT =
  "Why? Your account accepts shared files by hand (Personal settings › Sharing), or this server asks everyone to. Cassini never accepts for you.";

// --- Refused: the fact in bold, the cause, then who can fix it — with the
// link to hand on as the last words of the line ---

export function refusedTitle(count: number): string {
  return `${recordings(count)} could not be shared with you.`;
}

export const REFUSED_LINE = "A Nextcloud sharing setting stops it.";

export function starterRefusedTitle(count: number): string {
  return `${recordings(count)} you started could not be published.`;
}

export function starterRefusedLine(count: number): string {
  return `Nextcloud refused to share ${count === 1 ? "it" : "them"} with you.`;
}

// Past this many, the unpublished recordings are one line with the count,
// and their rows behind a disclosure; up to it, each has its own notice.
export const UNPUBLISHED_GROUP_AFTER = 2;

// One unpublished recording's notice, in bold: "Your recording in “Weekly
// sync” on Fri 30 Oct, 10:00 could not be published." The room is what the
// person recognises it by; a title stands in for a room the operator does not
// know, and each part is left out when it is unknown.
export function unpublishedTitle(entry: UnpublishedRecording, options: DateFormatOptions = {}): string {
  const name = entry.roomName || entry.title;
  const date = formatShareDate(entry.startedAt, options);
  if (!name && !date) return "A recording you started could not be published.";
  return `Your recording${name ? ` in “${name}”` : ""}${date ? ` on ${date}` : ""} could not be published.`;
}

// A row in the grouped list: "Weekly sync · Fri 30 Oct, 10:00".
export function unpublishedRowLabel(entry: UnpublishedRecording, options: DateFormatOptions = {}): string {
  const parts = [entry.roomName || entry.title || "A recording", formatShareDate(entry.startedAt, options)];
  return parts.filter((part) => part !== "").join(" · ");
}

export const DISMISS = "Dismiss";
export const DISMISS_ALL = "Dismiss all";

// "An administrator can fix that: send them this link." The link is the app's
// own address, as SetupNotice hands it on; opened by an administrator, Doctor
// says what is stopping it. Without an address the sentence ends after "that".
export const ADMIN_FIX_LEAD = "An administrator can fix that:";
export const ADMIN_FIX_PLAIN = "An administrator can fix that.";
export const SEND_LINK = "send them this link";
export const SEND_LINK_TITLE = "Opening it as an administrator shows exactly what is stopping it";

// --- Rows ---

export interface DateFormatOptions {
  // Test seams. Production leaves both unset: the reader's own locale and zone.
  locale?: string;
  timeZone?: string;
}

// "Fri 30 Oct, 10:00" in the reader's locale: short weekday, day, month, time.
// "" for a date the operator did not know or this browser cannot parse.
export function formatShareDate(iso: string, options: DateFormatOptions = {}): string {
  if (!iso) return "";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "";
  return new Intl.DateTimeFormat(options.locale, {
    weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit",
    ...(options.timeZone ? { timeZone: options.timeZone } : {}),
  }).format(date);
}

// "42 min", "1 h 05"; "" for an unknown duration.
export function formatDuration(seconds: number): string {
  const minutes = Math.round(seconds / 60);
  if (!Number.isFinite(minutes) || minutes < 1) return "";
  if (minutes < 60) return `${minutes} min`;
  return `${Math.floor(minutes / 60)} h ${String(minutes % 60).padStart(2, "0")}`;
}

// The row's first line: the meeting's title; without one the date stands in,
// and without a date it is "A recording" — the operator no longer knows the
// job, and the share is still theirs to accept.
export function pendingRowTitle(entry: PendingRecording, options: DateFormatOptions = {}): string {
  return entry.title || formatShareDate(entry.startedAt, options) || "A recording";
}

// The row's second line: "Fri 30 Oct, 10:00 · 42 min". The date is left out
// when it already stands in for the title.
export function pendingRowMeta(entry: PendingRecording, options: DateFormatOptions = {}): string {
  const parts = [entry.title ? formatShareDate(entry.startedAt, options) : "", formatDuration(entry.durationSeconds)];
  return parts.filter((part) => part !== "").join(" · ");
}

export function pendingRowKey(entry: PendingRecording): string {
  return entry.shareIds.join(",");
}

// --- POST operator/shares/accept ---

export interface AcceptSharesFailure {
  shareId: string;
  reason: string;
}

export interface AcceptSharesResult {
  accepted: string[];
  failed: AcceptSharesFailure[];
  // The refreshed object from /setup, when the operator sent a readable one.
  sharing: SharingStatus | null;
}

// The route takes at most this many ids in one call; past it, "every pending
// recording share of the caller" is asked for by name instead.
const MAX_SHARE_IDS = 100;

// acceptShares asks the operator to accept these shares AS the caller. The
// operator only accepts shares in the caller's own pending list; an id outside
// it comes back in `failed`, never forwarded. A non-200 answer is thrown with
// the operator's own sentence where it gave one.
export async function acceptShares(
  operatorBasePath: string,
  shareIds: string[],
  fetchImpl: typeof fetch = fetch,
): Promise<AcceptSharesResult> {
  const body = shareIds.length > MAX_SHARE_IDS ? { all: true } : { share_ids: shareIds };
  const response = await fetchImpl(`${operatorBasePath.replace(/\/+$/, "")}/shares/accept`, {
    method: "POST",
    headers: { Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (response.status !== 200) {
    throw new Error((await readServedMessage(response)) || `Could not accept the share (HTTP ${response.status}).`);
  }
  return readAcceptSharesResult(await response.json());
}

export function readAcceptSharesResult(value: unknown): AcceptSharesResult {
  const body = isRecord(value) ? value : {};
  const accepted = Array.isArray(body.accepted) ? body.accepted.filter((id): id is string => typeof id === "string") : [];
  const failed = Array.isArray(body.failed)
    ? body.failed.flatMap((entry) => {
        if (!isRecord(entry) || typeof entry.share_id !== "string") return [];
        return [{ shareId: entry.share_id, reason: typeof entry.reason === "string" ? entry.reason : "" }];
      })
    : [];
  return { accepted, failed, sharing: readSharing(body.sharing) };
}

// --- POST operator/shares/dismiss ---

// dismissUnpublished tells the operator the reader dismissed these notices,
// for good and on every device. Throws the operator's own sentence on a
// non-200 answer.
export async function dismissUnpublished(
  operatorBasePath: string,
  jobIds: string[],
  fetchImpl: typeof fetch = fetch,
): Promise<void> {
  const response = await fetchImpl(`${operatorBasePath.replace(/\/+$/, "")}/shares/dismiss`, {
    method: "POST",
    headers: { Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ job_ids: jobIds }),
  });
  if (response.status !== 200) {
    throw new Error((await readServedMessage(response)) || `Could not dismiss the notice (HTTP ${response.status}).`);
  }
}
