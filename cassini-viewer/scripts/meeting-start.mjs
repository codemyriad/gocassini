// When a meeting happened, read from a decoded portable meeting, by the format
// spec's "When the meeting happened" (cassini-format SPEC.md). One copy of the
// rule, shared by the exporters (Node) and the viewer (bundled by Vite); the
// Docker images copy this file next to export-static-meetings.mjs.
//
// The rule, first that applies:
//
//   1. meeting.recordedAtUtc — an instant.
//   2. the DATE tag, when it is RFC 3339 with an offset (Z or ±HH:MM) and does
//      not name the same instant as meeting.createdAtUtc — an instant. A DATE
//      equal to createdAtUtc is the pack stamp older producers wrote there and
//      says nothing about the recording.
//   3. meeting.recordedAtLocal — a label, shown as written, never converted.
//
// The CASSINI_RECORDED_AT_UTC tag is not read. It mirrors meeting.recordedAtUtc
// for tag-only tools; a reader that has the manifest uses the manifest, so a
// manifest without the member has no recordedAtUtc whatever the tag says.
//
// Both instants answer the same question — when the recording started, which
// is what playback position 0 is — so either comes back as `recordedAtUtc`: a
// UTC string in the shape the catalog's recordedAtUtc field takes. Step 1's
// value is returned as written; a DATE is converted to UTC (millisecond
// precision). `source` still says which one it was.

// recordedAtUtc's shape: UTC, a literal Z, optional fractional seconds.
const RECORDED_AT_UTC_PATTERN = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(\.\d+)?Z$/;

// RFC 3339 date-time with an offset. RFC 3339 allows a lowercase t/z and a
// space in place of the T; the format's reference reader accepts the same.
const RFC3339_OFFSET_PATTERN =
  /^(\d{4})-(\d{2})-(\d{2})[Tt ](\d{2}):(\d{2}):(\d{2})(\.\d+)?([Zz]|([+-])(\d{2}):(\d{2}))$/;

function fieldsAreReal(year, month, day, hour, minute, second) {
  const y = Number(year);
  const mo = Number(month);
  const d = Number(day);
  if (mo < 1 || mo > 12 || d < 1) return false;
  // Day 0 of the next month is the last day of this one.
  if (d > new Date(Date.UTC(y, mo, 0)).getUTCDate()) return false;
  return Number(hour) <= 23 && Number(minute) <= 59 && Number(second) <= 59;
}

// Epoch milliseconds of a well-formed recordedAtUtc, else null.
export function recordedAtUtcInstantMs(value) {
  if (typeof value !== "string") return null;
  const match = RECORDED_AT_UTC_PATTERN.exec(value);
  if (!match) return null;
  const [, y, mo, d, h, mi, s, frac] = match;
  if (!fieldsAreReal(y, mo, d, h, mi, s)) return null;
  const ms = Date.parse(`${y}-${mo}-${d}T${h}:${mi}:${s}${(frac ?? "").slice(0, 4)}Z`);
  return Number.isFinite(ms) ? ms : null;
}

// Epoch milliseconds of an RFC 3339 date-time with an offset, else null.
export function rfc3339OffsetInstantMs(value) {
  if (typeof value !== "string") return null;
  const match = RFC3339_OFFSET_PATTERN.exec(value.trim());
  if (!match) return null;
  const [, y, mo, d, h, mi, s, frac, , sign, oh, om] = match;
  if (!fieldsAreReal(y, mo, d, h, mi, s)) return null;
  if (sign && (Number(oh) > 23 || Number(om) > 59)) return null;
  const offset = sign ? `${sign}${oh}:${om}` : "Z";
  const ms = Date.parse(`${y}-${mo}-${d}T${h}:${mi}:${s}${(frac ?? "").slice(0, 4)}${offset}`);
  return Number.isFinite(ms) ? ms : null;
}

function tagValue(tags, name) {
  if (!tags || typeof tags !== "object") return undefined;
  const value = tags[name];
  return typeof value === "string" ? value : undefined;
}

// portableMeetingStart applies the rule to a decoded manifest and its tags
// (upper-case Vorbis comment names). Returns null when none of the three
// applies: the meeting time is unknown, and createdAtUtc is never the answer.
export function portableMeetingStart(manifest, tags) {
  const meeting = manifest && typeof manifest === "object" && manifest.meeting && typeof manifest.meeting === "object"
    ? manifest.meeting
    : {};

  if (recordedAtUtcInstantMs(meeting.recordedAtUtc) !== null) {
    return { source: "recordedAtUtc", value: meeting.recordedAtUtc, recordedAtUtc: meeting.recordedAtUtc };
  }

  const date = tagValue(tags, "DATE");
  const dateMs = rfc3339OffsetInstantMs(date);
  if (dateMs !== null && dateMs !== rfc3339OffsetInstantMs(meeting.createdAtUtc)) {
    return { source: "DATE", value: date.trim(), recordedAtUtc: new Date(dateMs).toISOString() };
  }

  if (typeof meeting.recordedAtLocal === "string" && meeting.recordedAtLocal.trim() !== "") {
    return { source: "recordedAtLocal", value: meeting.recordedAtLocal };
  }
  return null;
}

// The start as a UTC instant (steps 1 and 2), or "" when the file states none.
export function portableRecordedAtUtc(manifest, tags) {
  return portableMeetingStart(manifest, tags)?.recordedAtUtc ?? "";
}
