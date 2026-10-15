import { formatMeetingDateShort, parseDateLabelMs, type MeetingCatalogEntry } from "./catalog";
import { NO_ROOM_KEY, roomKeyOf, roomLabelOf } from "./rooms";

// The name an insight is given before anybody types one (D-791). Pure, so the
// suggestion can be tested apart from the card that shows it.
//
// The point is a list that can be told apart. Every unnamed summary used to be
// titled "summarise", so the default says what the insight is and what it read,
// in the words the browse list already uses for a meeting's room and date.
//
// It is the template and the selection for EVERY template, a question run
// included. The question is not the name: it is typed in a box that takes
// several lines, it can be a paragraph, and the operator refuses a name with a
// line break in it — so a question-as-name was a 400 for a name the asker never
// typed. The question is still shown, quoted under the sources, wherever the
// name says something else.

// The operator's bound on a name; a default longer than it would be refused.
export const INSIGHT_NAME_MAX = 120;

export interface DefaultInsightNameInput {
  // The template's display name, or null where the caller cannot read the
  // registry (a non-admin), which then says "Insight".
  templateName?: string | null;
  entries: readonly MeetingCatalogEntry[];
}

// defaultInsightName suggests a name: the template and what it read.
// "Summary · Design review, 3 Oct 2026",
// "Ask · Design, 4 meetings, 28 Sept – 12 Oct 2026".
export function defaultInsightName({ templateName, entries }: DefaultInsightNameInput): string {
  const template = oneLine(templateName ?? "") || "Insight";
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
    const title = oneLine(entry.title ?? "") || "1 meeting";
    return day === "" ? title : `${title}, ${formatMeetingDateShort(day)}`;
  }
  const parts: string[] = [];
  const rooms = new Set(entries.map(roomKeyOf));
  // One room is worth naming; "No room" is not a room, so a set of roomless
  // meetings is described by count and dates alone.
  if (rooms.size === 1 && !rooms.has(NO_ROOM_KEY)) {
    parts.push(oneLine(roomLabelOf(entries[0])));
  }
  parts.push(`${entries.length} meetings`);
  const range = formatDateRange(entries.map((entry) => entry.dateLabel));
  if (range !== "") {
    parts.push(range);
  }
  return parts.join(", ");
}

// oneLine makes free text fit a name: a meeting title or a template name can
// carry a line break or a tab, and the operator refuses a name with a control
// character in it. Whitespace runs become one space; control and invisible
// format characters go.
function oneLine(text: string): string {
  return text.replace(/[\p{Cc}\p{Cf}\s]+/gu, " ").trim();
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
