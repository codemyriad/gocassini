import { formatMeetingDateShort, parseDateLabelMs, type MeetingCatalogEntry } from "./catalog";
import { NO_ROOM_KEY, roomKeyOf, roomLabelOf } from "./rooms";

// The name an insight is given before anybody types one (D-791). Pure, so the
// suggestion can be tested apart from the card that shows it.
//
// The point is a list that can be told apart. Every unnamed summary used to be
// titled "summarise", so the default says what the insight is and what it read,
// in the words the browse list already uses for a meeting's room and date.

// The operator's bound on a name; a default longer than it would be refused.
export const INSIGHT_NAME_MAX = 120;

export interface DefaultInsightNameInput {
  // The template's display name, or null where the caller cannot read the
  // registry (a non-admin), which then says "Insight".
  templateName?: string | null;
  // The question as typed, for a template that takes one.
  question?: string;
  entries: readonly MeetingCatalogEntry[];
}

// defaultInsightName suggests a name. A question is already what the asker
// wants to know, so it is the name as typed. Anything else is the template and
// what it read: "Summary · Design review, 3 Oct 2026",
// "Summary · Design, 4 meetings, 28 Sept – 12 Oct 2026".
export function defaultInsightName({ templateName, question, entries }: DefaultInsightNameInput): string {
  const asked = question?.trim() ?? "";
  if (asked !== "") {
    return capName(asked);
  }
  const template = templateName?.trim() || "Insight";
  const read = describeSources(entries);
  return capName(read === "" ? template : `${template} · ${read}`);
}

function describeSources(entries: readonly MeetingCatalogEntry[]): string {
  if (entries.length === 0) {
    return "";
  }
  if (entries.length === 1) {
    const [entry] = entries;
    const day = dayOf(entry.dateLabel);
    const title = entry.title?.trim() || "1 meeting";
    return day === "" ? title : `${title}, ${formatMeetingDateShort(day)}`;
  }
  const parts: string[] = [];
  const rooms = new Set(entries.map(roomKeyOf));
  // One room is worth naming; "No room" is not a room, so a set of roomless
  // meetings is described by count and dates alone.
  if (rooms.size === 1 && !rooms.has(NO_ROOM_KEY)) {
    parts.push(roomLabelOf(entries[0]));
  }
  parts.push(`${entries.length} meetings`);
  const range = formatDateRange(entries.map((entry) => entry.dateLabel));
  if (range !== "") {
    parts.push(range);
  }
  return parts.join(", ");
}

// The day a label names, without the time: a name is about which meetings,
// and "3 Oct 2026, 14:30" is more precision than a title has room for.
function dayOf(dateLabel: string | undefined): string {
  const day = (dateLabel ?? "").slice(0, 10);
  return parseDateLabelMs(day) === null ? "" : day;
}

const DAY_FORMAT = new Intl.DateTimeFormat("en-GB", { day: "numeric", timeZone: "UTC" });
const DAY_MONTH_FORMAT = new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "short", timeZone: "UTC" });

// formatDateRange spans the dated labels, in the browse list's own date style:
// one day as "3 Oct 2026", one month as "1–12 Oct 2026", one year as
// "28 Sept – 12 Oct 2026", and otherwise both dates in full. Undated labels are
// ignored, and "" when none is dated.
export function formatDateRange(dateLabels: readonly (string | undefined)[]): string {
  const days = dateLabels.map(dayOf).filter((day) => day !== "").sort();
  if (days.length === 0) {
    return "";
  }
  const first = days[0];
  const last = days[days.length - 1];
  if (first === last) {
    return formatMeetingDateShort(first);
  }
  const lastFull = formatMeetingDateShort(last);
  const firstUTC = new Date(`${first}T00:00:00Z`);
  if (first.slice(0, 7) === last.slice(0, 7)) {
    return `${DAY_FORMAT.format(firstUTC)}–${lastFull}`;
  }
  if (first.slice(0, 4) === last.slice(0, 4)) {
    return `${DAY_MONTH_FORMAT.format(firstUTC)} – ${lastFull}`;
  }
  return `${formatMeetingDateShort(first)} – ${lastFull}`;
}

function capName(name: string): string {
  const chars = Array.from(name);
  if (chars.length <= INSIGHT_NAME_MAX) {
    return name;
  }
  return `${chars.slice(0, INSIGHT_NAME_MAX - 1).join("").trimEnd()}…`;
}
