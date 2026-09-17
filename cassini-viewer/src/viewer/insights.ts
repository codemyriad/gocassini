import { parseDateLabelMs, type MeetingCatalogEntry } from "./catalog";
import { NO_ROOM_KEY, UNDATED_MONTH_KEY, UNDATED_MONTH_LABEL } from "./rooms";

// Insights in the browse catalogue (D-721). Pure, for the same reason rooms.ts
// and selectionModel.ts are: the viewer's suite runs in node with no DOM, and
// the decisions worth testing here — which room an insight surfaces under,
// which of its sources the reader may actually see, where it lands among the
// meetings — are all decisions about data, not about markup.
//
// An insight is NOT a meeting and never becomes one: it has no audio, no
// transcript, no `.opus`, and cannot be picked into a context bundle. What it
// shares with a meeting is the list it appears in and the sheet it opens in,
// which is the whole design: an insight belongs beside the conversations it
// summarises, not in a separate library.

// The run's state, in the operator's own four words (see gocassini's
// internal/insight package doc, which fixes the vocabulary). Deliberately not
// re-worded here: a second vocabulary for the same four states is how a UI and
// its backend start disagreeing about what "done" means.
export type InsightStatus = "queued" | "running" | "succeeded" | "failed";

// InsightRecord is the `{run}` object the operator serves from `GET insights`
// — the whole record, not a view of it. It crosses a module boundary (the app's
// DataProvider implements the fetch; this package only reads the result), so
// the accessors below tolerate a missing array rather than letting one stray
// field take the browse list down with it.
export interface InsightRecord {
  id: string;
  status: InsightStatus;
  createdBy: string;
  attemptNumber: number;
  workflowId: string;
  workflowVersion: string;
  workflowSha256: string;
  // The meetings the run read, in the order they were picked. This is the
  // stored source set — never derived from what happens to share a room with
  // the insight, which is what the v3 prototype did and the one thing about
  // insights it is provably wrong about.
  meetingIds: string[];
  // Every room those meetings came from. A set, not a room: see
  // filterInsightsByRoom.
  roomIds: string[];
  question: string;
  provider: string;
  model: string;
  documentPath: string;
  // Why the latest attempt failed, as one of the operator's reason tokens;
  // "" for a run that did not fail. See InsightReason.
  reason: string;
  // The same token, kept for one release. A record written before tokens
  // existed carries a sentence here; only its token prefix is read.
  error: string;
  // RFC3339. Unlike a meeting's dateLabel these name a real instant, with a
  // zone — see formatInsightCreated.
  createdAt: string;
  updatedAt: string;
}

// --- What a failed run says ---
//
// The operator records WHY a run failed as one reason token on `reason`, and
// nothing else: no sentence, no stderr, no status code (D-749, decided 14
// September). Every word a reader sees for a failure is written here, so the
// wording can improve without touching a stored row, and a run that failed
// last month reads in this month's words. `error` carries the same token for
// one release; before that it carried a sentence with a token prefix, which
// insightReasonOf still classifies on.

export type InsightReason =
  | "bad-request"
  | "no-provider"
  | "provider-refused"
  | "model-failed"
  | "write-failed"
  | "deliver-failed"
  | "timeout"
  | "staging-failed"
  | "catalog-failed"
  | "meeting-unavailable"
  | "download-failed"
  | "assemble-failed"
  | "interrupted"
  | "unknown";

// One entry per reason: the title says what happened, the line says what to
// do, and neither repeats the other. Plain and short on purpose — these are
// the first words, not the last.
export const INSIGHT_FAILURE_COPY: Record<InsightReason, { title: string; line: string }> = {
  "bad-request": { title: "This request could not be run", line: "Check the app log." },
  "no-provider": { title: "No AI endpoint", line: "Add one under Operator › AI providers." },
  "provider-refused": {
    title: "The endpoint refused the request",
    line: "Check its key, quota or model name under Operator › AI providers.",
  },
  "model-failed": { title: "The model did not answer", line: "Retry, or pick fewer meetings." },
  "write-failed": {
    title: "The insight could not be written to disk",
    line: "Check the app's storage.",
  },
  "deliver-failed": {
    title: "The insight could not be saved to your Nextcloud files",
    line: "Check your space, then retry.",
  },
  timeout: {
    title: "Stopped after an hour",
    line: "Retry with fewer meetings or a faster endpoint.",
  },
  "staging-failed": { title: "The meetings could not be staged", line: "Check the app's storage." },
  "catalog-failed": { title: "Your meeting list could not be read", line: "Retry in a moment." },
  "meeting-unavailable": {
    title: "One of these meetings is no longer available to you",
    line: "Unpick it and ask again.",
  },
  "download-failed": {
    title: "A meeting could not be downloaded from Nextcloud",
    line: "Retry in a moment.",
  },
  "assemble-failed": {
    title: "The meetings could not be assembled into one document",
    line: "Check the app log.",
  },
  interrupted: { title: "Cassini restarted before this insight finished", line: "Retry it." },
  unknown: { title: "The insight failed", line: "Retry, or check the app log." },
};

function isInsightReason(value: string): value is InsightReason {
  return Object.prototype.hasOwnProperty.call(INSIGHT_FAILURE_COPY, value);
}

// The four tokens the operator used to put on the front of a stored sentence.
// A run that failed before D-749 still carries one, and it is the only part of
// that sentence still read.
const LEGACY_ERROR_PREFIX = /^\s*(bad-request|no-provider|provider-refused|model-failed)\s*:/i;

// insightReasonOf is the reason a failed run carries: `reason` when the
// operator set one, else the token prefix of the old `error` sentence, else
// unknown. Whatever prose is left in `error` is ignored — it is a sentence
// from whichever release stored it, and the words are this build's to choose.
export function insightReasonOf(record: Pick<InsightRecord, "reason" | "error">): InsightReason {
  const reason = record.reason?.trim() ?? "";
  if (isInsightReason(reason)) {
    return reason;
  }
  const legacy = LEGACY_ERROR_PREFIX.exec(record.error ?? "");
  if (legacy) {
    return legacy[1].toLowerCase() as InsightReason;
  }
  return "unknown";
}

// describeInsightFailure is what a failed run's sheet renders: a title and one
// line, from the table above and from nowhere else.
export function describeInsightFailure(
  record: Pick<InsightRecord, "reason" | "error">,
): { title: string; line: string } {
  return INSIGHT_FAILURE_COPY[insightReasonOf(record)];
}

// Which kinds of thing the browse list is showing. Both true is the default;
// see toggleBrowseType for why both false is not reachable.
export interface BrowseTypeFilter {
  meetings: boolean;
  insights: boolean;
}

export type BrowseType = "meetings" | "insights";

export const ALL_BROWSE_TYPES: BrowseTypeFilter = { meetings: true, insights: true };

// isLastBrowseType reports whether a type is the only one left switched on.
// The control for that type is then disabled: an empty list is not a filter
// state anyone wants to land in, and "everything is hidden" is indistinguishable
// on screen from "there is nothing here".
export function isLastBrowseType(filter: BrowseTypeFilter, type: BrowseType): boolean {
  return filter[type] && !filter[type === "meetings" ? "insights" : "meetings"];
}

export function toggleBrowseType(
  filter: BrowseTypeFilter,
  type: BrowseType,
): BrowseTypeFilter {
  if (isLastBrowseType(filter, type)) {
    return filter;
  }
  return { ...filter, [type]: !filter[type] };
}

function stringsOf(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
}

// insightRoomKeys maps a record onto the room keys the rail and the room filter
// speak (rooms.roomKeyOf), so an insight can be narrowed to the same buckets a
// meeting is.
//
// A record with no room at all lands in the "no room" bucket rather than
// nowhere, on rooms.ts's own reasoning: a bucket with a real name beats being
// dropped out of a room-grouped list. An insight drawn from meetings whose room
// is known only by NAME (roomName with no roomId — possible, the catalog reads
// the two independently) also lands there, because there is no id for the
// operator to have recorded; that is a narrower miss than filing it under a
// room it did not come from.
export function insightRoomKeys(record: InsightRecord): string[] {
  const keys = stringsOf(record.roomIds)
    .filter((roomId) => roomId !== "")
    .map((roomId) => `id:${roomId}`);
  return keys.length > 0 ? [...new Set(keys)] : [NO_ROOM_KEY];
}

// filterInsightsByRoom narrows to one room bucket, matching on ANY of the
// insight's source rooms.
//
// This is the one place the v3 prototype is provably wrong and the correction
// is deliberate (decided 3 September): the prototype files each insight under a
// single `spec.room` and keys the room filter on it, which cannot represent a
// selection that spans rooms — and spanning rooms is the entire premise of
// asking one question of several meetings. So the record carries a SET of
// source room ids and an insight surfaces under every room it drew from.
// Whoever reads this next will otherwise assume the prototype was right.
//
// A null key means "every room", exactly as rooms.filterMeetingsByRoom.
export function filterInsightsByRoom(
  insights: readonly InsightRecord[],
  roomKey: string | null,
): InsightRecord[] {
  if (roomKey === null) {
    return [...insights];
  }
  return insights.filter((record) => insightRoomKeys(record).includes(roomKey));
}

// insightHeadline is what names an insight in a list and at the head of its
// document: the question it was asked.
//
// A workflow can be run with no question of its own ("summarise these"), and
// then the workflow id is the honest fallback — the workflow's display NAME
// lives in the registry the operator serves, not here, and inventing one in the
// viewer would put a second set of names in front of the same four ids.
export function insightHeadline(record: InsightRecord): string {
  const question = record.question?.trim() ?? "";
  return question !== "" ? question : record.workflowId || "Insight";
}

// The four wire words, capitalised and otherwise untouched. An unrecognised
// status is shown as it arrived rather than mapped to a guess: a status this
// build does not know about is exactly the thing a reader needs to see.
export function formatInsightStatus(status: string): string {
  switch (status) {
    case "queued":
      return "Queued";
    case "running":
      return "Running";
    case "succeeded":
      return "Succeeded";
    case "failed":
      return "Failed";
    default:
      return status;
  }
}

// insightTimestampMs reads createdAt as the instant it is. Null when the record
// carries something that is not a timestamp — the run still belongs in the
// list, at the end of it, rather than being dropped for a bad date.
export function insightTimestampMs(record: InsightRecord): number | null {
  const ms = Date.parse(record.createdAt ?? "");
  return Number.isFinite(ms) ? ms : null;
}

const CREATED_DATE_FORMAT = new Intl.DateTimeFormat("en-GB", {
  day: "numeric",
  month: "short",
  year: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  hour12: false,
});

// formatInsightCreated renders createdAt in the reader's own timezone.
//
// This deliberately differs from formatMeetingDateShort, which shows a catalog
// label's digits and makes no timezone claim (D-484) — because a meeting's
// dateLabel carries no zone and nobody knows which one it meant. An insight's
// createdAt is RFC3339: it names an actual instant, so rendering it anywhere
// but in the reader's own clock would be the less honest answer.
export function formatInsightCreated(record: InsightRecord): string {
  const ms = insightTimestampMs(record);
  if (ms === null) {
    return record.createdAt ?? "";
  }
  return CREATED_DATE_FORMAT.format(new Date(ms));
}

// stripInsightFrontMatter removes the YAML provenance block the recorder writes
// at the top of every insight document, leaving the model's answer.
//
// The block is deliberate and it stays in the file: `cassini insight run`
// writes it so a document that gets moved, shared and found again a month later
// can still say which meetings, which prompt version and which model produced
// it (internal/insight/record.go). Nothing parses it back — the machine-readable
// copy is the JSON beside it — and it is not written for a reader.
//
// It reached the screen because a document is rendered as markdown, and to
// markdown `---` is a horizontal rule: the fences became two lines and the
// twenty YAML keys between them became one run-on paragraph of quoted hashes
// and IDs above the answer somebody asked for. The panel shows the same facts
// from the run record instead, in its header and its provenance list, so
// nothing is lost by cutting it here.
//
// Cut conservatively. A document whose opening fence is never closed is not
// front matter — it is a document that happens to start with a rule — and
// swallowing it to the end would delete the answer rather than tidy it.
export function stripInsightFrontMatter(markdown: string): string {
  // A leading BOM survives a round trip through Files and would stop the fence
  // matching on the first character.
  const text = markdown.replace(/^﻿/, "");
  if (!/^---[ \t]*\r?\n/.test(text)) {
    return markdown;
  }
  const lines = text.split("\n");
  for (let i = 1; i < lines.length; i += 1) {
    if (/^---[ \t]*\r?$/.test(lines[i]) || lines[i].trimEnd() === "---") {
      // Past the closing fence, and past the blank lines under it, so the
      // answer starts at the top rather than after a gap.
      let start = i + 1;
      while (start < lines.length && lines[start].trim() === "") {
        start += 1;
      }
      return lines.slice(start).join("\n");
    }
  }
  return markdown;
}

// resolveInsightSources answers, for every insight at once, which of its source
// meetings this caller can actually see — the entries present in the catalog
// they were served, in the order the run recorded them.
//
// A source the caller may not read is ABSENT, and nothing counts it: no "1
// meeting you cannot see", no total that disagrees with the rows beneath it.
// Disclosing that a meeting exists is the disclosure the per-caller filter is
// there to prevent, and a count is a disclosure. In the normal case nothing is
// missing — an insight is listed from its creator's own files and they picked
// its sources out of their own catalog — so this only bites when access changed
// after the run, which is exactly when it must not leak.
//
// Built as one index rather than a search per source, so a list of insights
// over an archive-sized catalog is one pass.
export function resolveInsightSources(
  insights: readonly InsightRecord[],
  meetings: readonly MeetingCatalogEntry[],
): Map<string, MeetingCatalogEntry[]> {
  const byId = new Map(meetings.map((meeting) => [meeting.id, meeting]));
  const resolved = new Map<string, MeetingCatalogEntry[]>();
  for (const record of insights) {
    const sources: MeetingCatalogEntry[] = [];
    for (const meetingId of stringsOf(record.meetingIds)) {
      const meeting = byId.get(meetingId);
      if (meeting) {
        sources.push(meeting);
      }
    }
    resolved.set(record.id, sources);
  }
  return resolved;
}

// insightsForMeeting is the other direction: which insights read this meeting.
// Same rule in reverse — an insight the caller was not served is not here to be
// found, because the list it comes from is already theirs alone.
export function insightsForMeeting(
  insights: readonly InsightRecord[],
  meetingId: string,
): InsightRecord[] {
  if (!meetingId) {
    return [];
  }
  return insights.filter((record) => stringsOf(record.meetingIds).includes(meetingId));
}

// filterInsights is the list's text filter, applied to insights: the question,
// the workflow id, and the created date AS THE CARD PRINTS IT. Those three are
// exactly what an insight row shows, which is the rule — the date is included
// because it is on the card, and the answer is excluded for the same reason the
// meeting filter does not reach into transcripts: a hit the list cannot show is
// a hit that looks like a bug.
export function filterInsights(
  insights: readonly InsightRecord[],
  query: string,
): InsightRecord[] {
  const needle = query.trim().toLowerCase();
  if (needle === "") {
    return [...insights];
  }
  return insights.filter((record) =>
    [record.question ?? "", record.workflowId ?? "", formatInsightCreated(record)].some(
      (haystack) => haystack.toLowerCase().includes(needle),
    ),
  );
}

// One row of the browse list: a meeting, or an insight drawn from meetings.
export type BrowseFeedItem =
  | { key: string; kind: "meeting"; meeting: MeetingCatalogEntry }
  | { key: string; kind: "insight"; insight: InsightRecord };

export interface BrowseFeedGroup {
  key: string;
  label: string;
  items: BrowseFeedItem[];
}

// buildBrowseFeed interleaves the two kinds into the one list.
//
// The meetings arrive in the order the caller sorted them and keep it exactly
// — this decides only where an insight lands among them, by merging rather than
// re-sorting, so the list's order stays the catalog's own (the same promise
// rooms.groupMeetingsByMonth makes).
//
// The type filter composes here and the room filter composes before it: the
// caller narrows each kind to the selected room, then this drops whichever kind
// is switched off. The two never fight, because they filter on different
// things — which rooms an item belongs to, and which kind it is.
//
// An insight sorts above a meeting at the same instant: it was drawn FROM
// meetings, so it is never older than the ones it read.
export function buildBrowseFeed(input: {
  meetings: readonly MeetingCatalogEntry[];
  insights: readonly InsightRecord[];
  types: BrowseTypeFilter;
}): BrowseFeedItem[] {
  const meetings = input.types.meetings ? input.meetings : [];
  const insights = input.types.insights
    ? [...input.insights].sort(compareInsightsNewestFirst)
    : [];

  const items: BrowseFeedItem[] = [];
  let meetingIndex = 0;
  let insightIndex = 0;
  while (meetingIndex < meetings.length && insightIndex < insights.length) {
    const meeting = meetings[meetingIndex];
    const insight = insights[insightIndex];
    if (insightGoesFirst(insightTimestampMs(insight), parseDateLabelMs(meeting.dateLabel))) {
      items.push(insightItem(insight));
      insightIndex += 1;
    } else {
      items.push(meetingItem(meeting));
      meetingIndex += 1;
    }
  }
  for (; meetingIndex < meetings.length; meetingIndex += 1) {
    items.push(meetingItem(meetings[meetingIndex]));
  }
  for (; insightIndex < insights.length; insightIndex += 1) {
    items.push(insightItem(insights[insightIndex]));
  }
  return items;
}

function meetingItem(meeting: MeetingCatalogEntry): BrowseFeedItem {
  // Prefixed: a meeting id and an insight id share one keyed `{#each}`, and a
  // collision there would make Svelte reuse one row's DOM for the other kind.
  return { key: `meeting:${meeting.id}`, kind: "meeting", meeting };
}

function insightItem(insight: InsightRecord): BrowseFeedItem {
  return { key: `insight:${insight.id}`, kind: "insight", insight };
}

// Undated sorts last, whichever kind it is: a row whose date could not be read
// is the one row nobody is looking for at the top of the list.
function insightGoesFirst(insightMs: number | null, meetingMs: number | null): boolean {
  if (insightMs === null) {
    return false;
  }
  if (meetingMs === null) {
    return true;
  }
  return insightMs >= meetingMs;
}

function compareInsightsNewestFirst(left: InsightRecord, right: InsightRecord): number {
  const leftMs = insightTimestampMs(left);
  const rightMs = insightTimestampMs(right);
  if (leftMs !== null && rightMs !== null && leftMs !== rightMs) {
    return rightMs - leftMs;
  }
  if (leftMs !== null && rightMs === null) {
    return -1;
  }
  if (leftMs === null && rightMs !== null) {
    return 1;
  }
  return right.id < left.id ? -1 : right.id > left.id ? 1 : 0;
}

// Pinned to en-GB and UTC for the reason rooms.ts pins its month format: the
// heading text must not change with the browser's locale, and it is built from
// the year and month the row itself displays.
const MONTH_FORMAT = new Intl.DateTimeFormat("en-GB", {
  month: "long",
  year: "numeric",
  timeZone: "UTC",
});

// groupBrowseFeedByMonth puts the month headings in, without reordering.
//
// ONE CLOCK for both kinds, and it is UTC — the clock buildBrowseFeed already
// ordered the list by. This grouping keeps insertion order, so it is only ever
// correct while the month key is a monotone function of the sort key; filing
// meetings by their label's own digits (UTC, as rooms.groupMeetingsByMonth
// does) and insights by the reader's local clock broke exactly that. West of
// UTC, an insight created just after midnight on the 1st displays the previous
// month, so the first group emitted was "August", the next "September", and a
// list whose whole promise is newest-first rendered an older heading above a
// newer one with the rows to match. A boundary row whose printed local time
// reads as the month before its heading is a much smaller lie than a heading
// out of order, and it is the same approximation the meeting rows have always
// made.
export function groupBrowseFeedByMonth(
  items: readonly BrowseFeedItem[],
): BrowseFeedGroup[] {
  const groups = new Map<string, BrowseFeedGroup>();
  for (const item of items) {
    const { key, label } = monthOf(item);
    const existing = groups.get(key);
    if (existing) {
      existing.items.push(item);
      continue;
    }
    groups.set(key, { key, label, items: [item] });
  }
  return [...groups.values()];
}

function monthOf(item: BrowseFeedItem): { key: string; label: string } {
  if (item.kind === "meeting") {
    const ms = parseDateLabelMs(item.meeting.dateLabel);
    if (ms === null) {
      return { key: UNDATED_MONTH_KEY, label: UNDATED_MONTH_LABEL };
    }
    const date = new Date(ms);
    return monthLabel(date.getUTCFullYear(), date.getUTCMonth());
  }
  const ms = insightTimestampMs(item.insight);
  if (ms === null) {
    return { key: UNDATED_MONTH_KEY, label: UNDATED_MONTH_LABEL };
  }
  const date = new Date(ms);
  return monthLabel(date.getUTCFullYear(), date.getUTCMonth());
}

function monthLabel(year: number, monthIndex: number): { key: string; label: string } {
  const month = String(monthIndex + 1).padStart(2, "0");
  return {
    key: `${year}-${month}`,
    label: MONTH_FORMAT.format(new Date(Date.UTC(year, monthIndex, 1))),
  };
}
