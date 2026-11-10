// When a meeting started, for every surface that shows, sorts or groups by it.
//
// A meeting can know its start in one of two ways, and they are not the same
// kind of fact:
//
//   - recordedAtUtc, an RFC 3339 UTC instant (catalog entry, or the file's own
//     manifest `meeting.recordedAtUtc`). It names a real moment, so it renders
//     in the READER's clock — the browser's zone, never a hard-coded one —
//     exactly as insight timestamps already do.
//   - dateLabel, the legacy zone-less "YYYY-MM-DD[ HH:MM]" label. Nobody knows
//     which clock its digits were read from, so they are shown as they are and
//     no zone is claimed (D-484). It is the fallback for entries that carry no
//     instant: older catalogs, imports, cron-era recordings.
//
// Everything below reduces both to one "wall clock": the year, month, day and
// time the reader is shown. Sorting, month headings, the feed interleave and
// the filter all work on that wall clock, so what a row says, where it sorts
// and which heading it sits under can never disagree — the property
// insights.groupBrowseFeedByMonth depends on. For an instant the wall clock is
// its local time; for a label it is the label's digits.
//
// The rendering of a wall clock is pinned to en-GB and to UTC arithmetic on
// purpose: the digits are already decided by the time they are formatted, and
// formatting them through the runtime's default zone would let that zone shift
// them a second time.

export interface MeetingTimeSource {
  dateLabel?: string;
  recordedAtUtc?: string;
}

export interface WallClock {
  year: number;
  // 1-12.
  month: number;
  day: number;
  // Absent for a date-only label.
  hour?: number;
  minute?: number;
  second?: number;
}

export type MeetingStart =
  | { kind: "instant"; ms: number; wall: WallClock }
  | { kind: "label"; wall: WallClock }
  | { kind: "none" };

// The shared contract's shape for recordedAtUtc: UTC, a literal Z, optional
// fractional seconds. An offset other than Z is not accepted: the producer
// writes UTC, and anything else is not the field.
const RECORDED_AT_UTC_PATTERN =
  /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(\.\d+)?Z$/;

// parseRecordedAtUtc reads an optional recordedAtUtc leniently: a value that is
// not one — wrong shape, impossible date, not a string — means "not recorded",
// never an error, so one malformed entry cannot take a meeting list down.
export function parseRecordedAtUtc(value: unknown): string | undefined {
  if (typeof value !== "string") {
    return undefined;
  }
  const match = RECORDED_AT_UTC_PATTERN.exec(value);
  if (!match) {
    return undefined;
  }
  const [, y, mo, d, h, mi, s] = match;
  const ms = Date.UTC(Number(y), Number(mo) - 1, Number(d), Number(h), Number(mi), Number(s));
  const check = new Date(ms);
  // Date.UTC rolls "2026-02-30" into March; the round trip catches that.
  if (
    !Number.isFinite(ms) ||
    check.getUTCFullYear() !== Number(y) ||
    check.getUTCMonth() !== Number(mo) - 1 ||
    check.getUTCDate() !== Number(d) ||
    check.getUTCHours() !== Number(h) ||
    check.getUTCMinutes() !== Number(mi) ||
    check.getUTCSeconds() !== Number(s)
  ) {
    return undefined;
  }
  return value;
}

export function recordedAtUtcMs(value: string | undefined): number | null {
  const valid = parseRecordedAtUtc(value);
  if (valid === undefined) {
    return null;
  }
  const ms = Date.parse(valid);
  return Number.isFinite(ms) ? ms : null;
}

// meetingStart resolves an entry to the one start the reader is shown.
// `timeZone` exists for tests; production callers leave it out, and the
// browser's own zone is used.
export function meetingStart(entry: MeetingTimeSource | null | undefined, timeZone?: string): MeetingStart {
  const ms = recordedAtUtcMs(entry?.recordedAtUtc);
  if (ms !== null) {
    return { kind: "instant", ms, wall: instantWallClock(ms, timeZone) };
  }
  const wall = parseDateLabelWall(entry?.dateLabel ?? "");
  return wall ? { kind: "label", wall } : { kind: "none" };
}

// withRecordedAtUtc completes an entry with the instant its file states
// (manifest meeting.recordedAtUtc), read after the file has loaded. The file is
// the record, so a valid value from it wins over the entry's own; an invalid or
// missing one leaves the entry as it was. Returns the same object when nothing
// changes, so a reactive caller does not re-render for nothing.
export function withRecordedAtUtc<T extends MeetingTimeSource>(entry: T, fromFile: string | null | undefined): T;
export function withRecordedAtUtc<T extends MeetingTimeSource>(entry: T | null, fromFile: string | null | undefined): T | null;
export function withRecordedAtUtc<T extends MeetingTimeSource>(entry: T | null, fromFile: string | null | undefined): T | null {
  const recordedAtUtc = parseRecordedAtUtc(fromFile);
  if (!entry || recordedAtUtc === undefined || entry.recordedAtUtc === recordedAtUtc) {
    return entry;
  }
  return { ...entry, recordedAtUtc };
}

// Whether there is a start to SHOW. A little wider than meetingStart: a label
// whose date is fine but whose time is out of range ("2026-04-08 99:99") still
// shows its day, as the label formatters always did, though it does not sort.
export function hasMeetingStart(entry: MeetingTimeSource | null | undefined): boolean {
  return displayWall(entry) !== null;
}

function displayWall(entry: MeetingTimeSource | null | undefined, timeZone?: string): WallClock | null {
  const start = meetingStart(entry, timeZone);
  if (start.kind !== "none") {
    return start.wall;
  }
  const label = entry?.dateLabel ?? "";
  return /^\d{4}-\d{2}-\d{2}/.test(label) ? parseDateLabelWall(label.slice(0, 10)) : null;
}

// The sort key: the wall clock the reader sees, as if those digits were UTC.
// Not an instant — for an instant entry it is the local time shifted by the
// reader's offset — but it is the one key that sorts every entry, instant or
// label, consistently with what the rows print and the month they sit under.
export function meetingStartSortKey(entry: MeetingTimeSource | null | undefined, timeZone?: string): number | null {
  const start = meetingStart(entry, timeZone);
  return start.kind === "none" ? null : wallClockKey(start.wall);
}

// instantSortKey is meetingStartSortKey for a bare instant, so things that are
// not meetings (an insight's createdAt) can be placed among meetings on the
// same scale.
export function instantSortKey(ms: number, timeZone?: string): number {
  return wallClockKey(instantWallClock(ms, timeZone));
}

// StartOrder places a start for ordering: the wall-clock key every start has,
// and the instant when there is one. Two instants compare by instant, since
// wall-clock order is not instant order across a DST fall-back (02:30 CEST is
// before 02:10 CET); anything involving a label compares wall-clock keys.
export interface StartOrder {
  key: number;
  ms: number | null;
}

export function meetingStartOrder(entry: MeetingTimeSource | null | undefined, timeZone?: string): StartOrder | null {
  const start = meetingStart(entry, timeZone);
  if (start.kind === "none") {
    return null;
  }
  return { key: wallClockKey(start.wall), ms: start.kind === "instant" ? start.ms : null };
}

export function instantStartOrder(ms: number, timeZone?: string): StartOrder {
  return { key: instantSortKey(ms, timeZone), ms };
}

// Positive when a is later than b, negative when earlier, 0 when level.
export function compareStartOrder(a: StartOrder, b: StartOrder): number {
  if (a.ms !== null && b.ms !== null) {
    return a.ms - b.ms;
  }
  return a.key - b.key;
}

export function wallClockKey(wall: WallClock): number {
  return Date.UTC(wall.year, wall.month - 1, wall.day, wall.hour ?? 0, wall.minute ?? 0, wall.second ?? 0);
}

// "13 Mar 2026, 12:29" — day, short month, year and, when known, the start
// time on a 24-hour clock. For a label with no recognisable date, the label
// itself, as formatMeetingDateShort always did.
export function formatMeetingStartShort(entry: MeetingTimeSource | null | undefined, timeZone?: string): string {
  const wall = displayWall(entry, timeZone);
  return wall ? formatWallShort(wall) : (entry?.dateLabel ?? "");
}

// "Wed 16 Sept 2026, 10:31".
export function formatMeetingStartWithDay(entry: MeetingTimeSource | null | undefined, timeZone?: string): string {
  const wall = displayWall(entry, timeZone);
  return wall ? `${WEEKDAY_FORMAT.format(wallDate(wall))} ${formatWallShort(wall)}` : (entry?.dateLabel ?? "");
}

// "13 Mar 2026", the day only.
export function formatMeetingStartDay(entry: MeetingTimeSource | null | undefined, timeZone?: string): string {
  const wall = displayWall(entry, timeZone);
  return wall ? SHORT_DATE_FORMAT.format(wallDate(wall)) : "";
}

// The day the reader is shown, as "YYYY-MM-DD", or "" when there is none.
export function meetingStartDayKey(entry: MeetingTimeSource | null | undefined, timeZone?: string): string {
  const start = meetingStart(entry, timeZone);
  if (start.kind === "none") {
    return "";
  }
  const { year, month, day } = start.wall;
  return `${String(year).padStart(4, "0")}-${pad(month)}-${pad(day)}`;
}

// The month heading a meeting sits under, or null when it has no date.
export function meetingStartMonth(
  entry: MeetingTimeSource | null | undefined,
  timeZone?: string,
): { key: string; label: string } | null {
  const start = meetingStart(entry, timeZone);
  if (start.kind === "none") {
    return null;
  }
  return wallClockMonth(start.wall);
}

export function wallClockMonth(wall: WallClock): { key: string; label: string } {
  return {
    key: `${wall.year}-${pad(wall.month)}`,
    label: MONTH_FORMAT.format(new Date(Date.UTC(wall.year, wall.month - 1, 1))),
  };
}

// For text that leaves Cassini (a copied transcript, an exported insight):
// whoever reads it may not share this clock, so an instant carries its zone
// ("Mon 9 Nov 2026, 12:31 CET"). A label keeps the form each export already
// used, since it has no zone to name.
export function formatMeetingStartForExport(
  entry: MeetingTimeSource | null | undefined,
  timeZone?: string,
): string {
  const start = meetingStart(entry, timeZone);
  if (start.kind !== "instant") {
    return formatMeetingStartWithDay(entry, timeZone);
  }
  const zone = zoneName(start.ms, timeZone);
  const text = formatMeetingStartWithDay(entry, timeZone);
  return zone ? `${text} ${zone}` : text;
}

// The instant's wall clock in the given zone, or the runtime's own zone when
// none is given. The default path reads the Date's local fields rather than an
// Intl formatter built once: those follow the zone at the moment of the call.
export function instantWallClock(ms: number, timeZone?: string): WallClock {
  if (timeZone === undefined) {
    const date = new Date(ms);
    return {
      year: date.getFullYear(),
      month: date.getMonth() + 1,
      day: date.getDate(),
      hour: date.getHours(),
      minute: date.getMinutes(),
      second: date.getSeconds(),
    };
  }
  const parts = zonedPartsFormat(timeZone).formatToParts(new Date(ms));
  const read = (type: Intl.DateTimeFormatPartTypes) =>
    Number(parts.find((part) => part.type === type)?.value ?? "0");
  return {
    year: read("year"),
    month: read("month"),
    day: read("day"),
    hour: read("hour") % 24,
    minute: read("minute"),
    second: read("second"),
  };
}

// parseDateLabelMs parses the catalog's "YYYY-MM-DD" / "YYYY-MM-DD HH:MM[:SS]"
// date labels into a comparable timestamp, or null when the label does not
// follow that shape or its fields are out of range (Date.UTC would silently
// roll "2026-13-05" over into 2027 and sort it above real meetings). Labels
// carry no timezone, so their digits are read as UTC consistently — the result
// is a wall-clock key, comparable with meetingStartSortKey, not an instant.
export function parseDateLabelMs(dateLabel: string): number | null {
  const wall = parseDateLabelWall(dateLabel);
  return wall ? wallClockKey(wall) : null;
}

export function parseDateLabelWall(dateLabel: string): WallClock | null {
  const match =
    /^(\d{4})-(\d{2})-(\d{2})(?:[ T](\d{2}):(\d{2})(?::(\d{2}))?)?/.exec(
      dateLabel.trim(),
    );
  if (!match) {
    return null;
  }
  const [, year, month, day, hour, minute, second] = match;
  const monthNum = Number(month);
  const dayNum = Number(day);
  const hourNum = hour !== undefined ? Number(hour) : 0;
  const minuteNum = minute !== undefined ? Number(minute) : 0;
  const secondNum = second !== undefined ? Number(second) : 0;
  if (
    monthNum < 1 ||
    monthNum > 12 ||
    dayNum < 1 ||
    dayNum > 31 ||
    hourNum > 23 ||
    minuteNum > 59 ||
    secondNum > 59
  ) {
    return null;
  }
  const wall: WallClock = { year: Number(year), month: monthNum, day: dayNum };
  if (hour !== undefined && minute !== undefined) {
    wall.hour = hourNum;
    wall.minute = minuteNum;
    wall.second = secondNum;
  }
  return Number.isFinite(wallClockKey(wall)) ? wall : null;
}

// Catalog `dateLabel` ships as ISO-ish "YYYY-MM-DD" or "YYYY-MM-DD HH:MM" and
// carries NO timezone: producers stamp it from different clocks (filename
// wall-clock, UTC-derived job ids). The label formatters below render the
// label's own digits and do NOT attach a zone — the label doesn't know its
// zone, so a "GMT+1" suffix would assert an instant we can't actually justify
// (D-484). They are the fallback for entries without recordedAtUtc; anything
// that has the entry should call the formatMeetingStart* functions above.

// Whether a dateLabel is a date at all. A meeting whose id stood in for its
// date rendered that id under a calendar icon (D-775).
export function hasMeetingDate(dateLabel: string): boolean {
  return /^(\d{4})-(\d{2})-(\d{2})/.test(dateLabel);
}

// "13 Mar 2026, 12:29" from a label's digits. Locale-pinned to en-GB so the
// day-month-year order and the 24-hour clock are consistent regardless of the
// viewer's browser locale (D-685).
export function formatMeetingDateShort(dateLabel: string): string {
  return formatMeetingStartShort({ dateLabel });
}

export function formatMeetingDateWithDay(dateLabel: string): string {
  return formatMeetingStartWithDay({ dateLabel });
}

function formatWallShort(wall: WallClock): string {
  const day = SHORT_DATE_FORMAT.format(wallDate(wall));
  if (wall.hour === undefined || wall.minute === undefined) {
    return day;
  }
  return `${day}, ${pad(wall.hour)}:${pad(wall.minute)}`;
}

function wallDate(wall: WallClock): Date {
  return new Date(Date.UTC(wall.year, wall.month - 1, wall.day));
}

function pad(value: number): string {
  return String(value).padStart(2, "0");
}

// Hoisted: the filter re-formats every catalog entry on each keystroke, and
// Intl.DateTimeFormat construction is the expensive part. All pinned to UTC
// because they format wall-clock digits already decided (see the top).
const SHORT_DATE_FORMAT = new Intl.DateTimeFormat("en-GB", {
  day: "numeric",
  month: "short",
  year: "numeric",
  timeZone: "UTC",
});
const WEEKDAY_FORMAT = new Intl.DateTimeFormat("en-GB", { weekday: "short", timeZone: "UTC" });
const MONTH_FORMAT = new Intl.DateTimeFormat("en-GB", {
  month: "long",
  year: "numeric",
  timeZone: "UTC",
});

const zonedFormats = new Map<string, Intl.DateTimeFormat>();

function zonedPartsFormat(timeZone: string): Intl.DateTimeFormat {
  let format = zonedFormats.get(timeZone);
  if (!format) {
    format = new Intl.DateTimeFormat("en-GB", {
      timeZone,
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      hourCycle: "h23",
    });
    zonedFormats.set(timeZone, format);
  }
  return format;
}

function zoneName(ms: number, timeZone?: string): string {
  const parts = new Intl.DateTimeFormat("en-GB", {
    timeZone,
    hour: "2-digit",
    timeZoneName: "short",
  }).formatToParts(new Date(ms));
  return parts.find((part) => part.type === "timeZoneName")?.value ?? "";
}
