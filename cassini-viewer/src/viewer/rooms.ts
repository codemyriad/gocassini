import { parseDateLabelMs, type MeetingCatalogEntry } from "./catalog";

// Room grouping for the browse surface (D-654). Pure, so the viewer's Vitest
// suite — which runs in node with no DOM to render a component into — can cover
// the decisions that actually matter: which bucket a meeting lands in, what a
// bucket is called, and what happens to the meetings that have no room at all.

// The key for meetings that carry neither roomId nor roomName. A meeting with
// no room is the normal state for most of an archive (a non-Talk job, a
// --simulate run, an old recording with no job row), so it gets a real bucket
// with a real name rather than being dropped from a room-grouped list — the one
// outcome D-654 rules out.
//
// It cannot collide with a real room's key: those are all prefixed `id:` or
// `name:` by roomKeyOf below.
export const NO_ROOM_KEY = "no-room";
export const NO_ROOM_LABEL = "No room";

// Meetings whose dateLabel does not parse (a legacy catalog that shipped a raw
// id as the label) still need a heading to sit under.
export const UNDATED_MONTH_KEY = "";
export const UNDATED_MONTH_LABEL = "Undated";

export interface RoomBucket {
  key: string;
  name: string;
  count: number;
  // false only for the NO_ROOM_KEY bucket, so the rail can present it as the
  // absence of a room rather than as a room called "No room".
  hasRoom: boolean;
}

export interface MeetingMonthGroup {
  key: string;
  label: string;
  meetings: MeetingCatalogEntry[];
}

// roomKeyOf identifies the bucket a meeting belongs to.
//
// roomId wins because it is the durable identity: it survives a rename, and two
// rooms that happen to share a display name are still two rooms. roomName is
// the fallback for a catalog entry that carries a name but no id (possible —
// catalog.ts reads the two independently), and grouping those by name is better
// than filing every one of them under "no room".
export function roomKeyOf(meeting: MeetingCatalogEntry): string {
  if (meeting.roomId) {
    return `id:${meeting.roomId}`;
  }
  if (meeting.roomName) {
    return `name:${meeting.roomName}`;
  }
  return NO_ROOM_KEY;
}

// roomLabelOf is what to show for a meeting's room. roomName is the display
// name; an entry with an id but no name falls back to the id, which is ugly but
// honest — it says "this meeting has a room we have no name for", which is a
// different fact from "this meeting has no room".
export function roomLabelOf(meeting: MeetingCatalogEntry): string {
  return meeting.roomName ?? meeting.roomId ?? NO_ROOM_LABEL;
}

// buildRoomBuckets derives the rail's room list from the catalog itself. There
// is no separate room registry in the catalog format — a room exists, as far as
// the viewer is concerned, because a meeting says it does.
//
// Order is alphabetical by name, with "No room" pinned last. Deliberately NOT
// by recency or count: the embedded viewer re-reads the catalog every 15s while
// it is open, and a rail whose entries reshuffle under the pointer when a
// recording lands is worse than one that is merely arbitrary but stable.
export function buildRoomBuckets(
  meetings: readonly MeetingCatalogEntry[],
): RoomBucket[] {
  const buckets = new Map<string, RoomBucket>();
  for (const meeting of meetings) {
    const key = roomKeyOf(meeting);
    const existing = buckets.get(key);
    if (existing) {
      existing.count += 1;
      continue;
    }
    buckets.set(key, {
      key,
      // The FIRST entry seen for a key names the bucket. Callers pass a
      // newest-first list, so after a room is renamed the rail shows the new
      // name while the older meetings — which still carry the old one on their
      // own rows — stay grouped under it by id.
      name: key === NO_ROOM_KEY ? NO_ROOM_LABEL : roomLabelOf(meeting),
      count: 1,
      hasRoom: key !== NO_ROOM_KEY,
    });
  }
  return [...buckets.values()].sort((left, right) => {
    if (left.hasRoom !== right.hasRoom) {
      return left.hasRoom ? -1 : 1;
    }
    return left.name.localeCompare(right.name, undefined, {
      sensitivity: "base",
    });
  });
}

// filterMeetingsByRoom narrows to one bucket. A null key means "all meetings" —
// not "the meetings with no room", which is NO_ROOM_KEY.
export function filterMeetingsByRoom(
  meetings: readonly MeetingCatalogEntry[],
  roomKey: string | null,
): MeetingCatalogEntry[] {
  if (roomKey === null) {
    return [...meetings];
  }
  return meetings.filter((meeting) => roomKeyOf(meeting) === roomKey);
}

// Pinned to en-GB and UTC for the same reasons formatMeetingDateShort is:
// consistent month names regardless of browser locale, and a label that agrees
// with the key, which is derived from the same UTC instant.
const MONTH_FORMAT = new Intl.DateTimeFormat("en-GB", {
  month: "long",
  year: "numeric",
  timeZone: "UTC",
});

// groupMeetingsByMonth splits an already-ordered list into month headings
// without reordering it: the caller's sort (newest first) is the one the list
// renders in, and this only decides where the headings fall.
//
// Keyed by a Map rather than by comparing each entry to the previous one, so a
// list that is not perfectly grouped still produces one heading per month
// instead of the same month twice.
export function groupMeetingsByMonth(
  meetings: readonly MeetingCatalogEntry[],
): MeetingMonthGroup[] {
  const groups = new Map<string, MeetingMonthGroup>();
  for (const meeting of meetings) {
    const { key, label } = monthOf(meeting.dateLabel);
    const existing = groups.get(key);
    if (existing) {
      existing.meetings.push(meeting);
      continue;
    }
    groups.set(key, { key, label, meetings: [meeting] });
  }
  return [...groups.values()];
}

function monthOf(dateLabel: string): { key: string; label: string } {
  const ms = parseDateLabelMs(dateLabel);
  if (ms === null) {
    return { key: UNDATED_MONTH_KEY, label: UNDATED_MONTH_LABEL };
  }
  const date = new Date(ms);
  const month = String(date.getUTCMonth() + 1).padStart(2, "0");
  return {
    key: `${date.getUTCFullYear()}-${month}`,
    label: MONTH_FORMAT.format(date),
  };
}
