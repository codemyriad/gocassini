import { readViewerBase, resolveAppBaseUrl } from "./appBase";

export interface MeetingCatalogEntry {
  id: string;
  title: string;
  dateLabel: string;
  artifactPath?: string;
  audioPath?: string;
  speakerCount?: number;
  segmentCount?: number;
  digestDurationMs?: number;
  // Which conversation the meeting was recorded in (D-622). roomId is a
  // deterministic one-way derivation of the room's identity — for a Talk
  // recording, of its conversation token — and never the token itself, because
  // for a public conversation that token is also the link that joins it. Both
  // are optional: a meeting recorded before the field existed, or one whose
  // room lookup failed, simply has no room, and no consumer may require one.
  //
  // roomName often equals title, and is still a separate field: a title is
  // free text that may have been overridden or derived from a file name, while
  // roomName is a claim about which room this is. Only the second is safe to
  // group by.
  //
  // The two have different homes as of D-640, which matters to anything that
  // wants to CHANGE one. roomId lives in the .opus and the exporter re-derives
  // it from there on every republish, so changing it means re-tagging the file.
  // roomName lives only here — a display name is editable and a sealed
  // recording is not — and is stamped by the operator on every publish.
  roomId?: string;
  roomName?: string;
  // Which operator job and attempt produced the artifact (D-640). Both
  // optional; a meeting published by any other producer has neither. jobId
  // normally equals `id`, because the operator publishes its artifact under the
  // job id — carried explicitly rather than assumed, since that equality is a
  // convention of one publish path.
  jobId?: string;
  attemptNumber?: number;
}

export interface MeetingCatalog {
  version: "cassini.viewer.catalog.v1";
  meetings: MeetingCatalogEntry[];
}

const DEFAULT_CATALOG_PATH = "./catalog.json";

export async function loadMeetingCatalog(
  path = DEFAULT_CATALOG_PATH,
): Promise<MeetingCatalog | null> {
  const targetUrl = path === DEFAULT_CATALOG_PATH ? resolveCatalogUrl() : path;
  // catalog.json is mutable: every publish replaces it with the current
  // meeting index. Bypass the browser's HTTP cache so reopening/focusing the
  // embedded viewer cannot remain pinned to AppAPI's prior one-hour response.
  const response = await fetch(targetUrl, { cache: "no-store" });
  if (!response.ok) {
    // null means "this site has no catalog" — a standalone single-meeting
    // export — and the caller answers it by loading the bundled artifact. Only
    // a 404 means that. Anything else is a failure to reach a catalog that
    // does exist (Nextcloud unreachable, the proxy erroring), and returning
    // null there sends the viewer looking for a bundled artifact that is not
    // in an ExApp build, turning an outage into a confusing empty state
    // instead of an error (D-550).
    if (response.status === 404) {
      return null;
    }
    throw new Error(
      `Could not load the meeting list (HTTP ${response.status}).`,
    );
  }
  const catalog = validateMeetingCatalog((await response.json()) as unknown);
  return {
    ...catalog,
    meetings: sortMeetingCatalogEntries(
      catalog.meetings.map((meeting) => ({
        ...meeting,
        artifactPath: meeting.artifactPath
          ? resolveCatalogAssetUrl(meeting.artifactPath, response.url)
          : undefined,
        audioPath: meeting.audioPath
          ? resolveCatalogAssetUrl(meeting.audioPath, response.url)
          : undefined,
      })),
    ),
  };
}

export function validateMeetingCatalog(value: unknown): MeetingCatalog {
  if (!isRecord(value)) {
    throw new Error("catalog must be an object");
  }
  if (value.version !== "cassini.viewer.catalog.v1") {
    throw new Error("unsupported catalog version");
  }
  if (!Array.isArray(value.meetings)) {
    throw new Error("catalog meetings must be an array");
  }

  return {
    version: "cassini.viewer.catalog.v1",
    meetings: value.meetings.map((entry, index) =>
      validateMeetingCatalogEntry(entry, index),
    ),
  };
}

function validateMeetingCatalogEntry(
  value: unknown,
  index: number,
): MeetingCatalogEntry {
  if (!isRecord(value)) {
    throw new Error(`catalog entry ${index} must be an object`);
  }

  const id = requireNonEmptyString(value.id, `catalog entry ${index} id`);
  const title = requireNonEmptyString(
    value.title,
    `catalog entry ${index} title`,
  );
  const dateLabel = requireNonEmptyString(
    value.dateLabel,
    `catalog entry ${index} dateLabel`,
  );
  const artifactPath = optionalNonEmptyString(
    value.artifactPath,
    `catalog entry ${index} artifactPath`,
  );
  const audioPath = optionalNonEmptyString(
    value.audioPath,
    `catalog entry ${index} audioPath`,
  );
  if (!artifactPath && !audioPath) {
    throw new Error(
      `catalog entry ${index} must define artifactPath or audioPath`,
    );
  }

  return {
    id,
    artifactPath,
    audioPath,
    title,
    dateLabel,
    speakerCount: optionalNumber(
      value.speakerCount,
      `catalog entry ${index} speakerCount`,
    ),
    segmentCount: optionalNumber(
      value.segmentCount,
      `catalog entry ${index} segmentCount`,
    ),
    digestDurationMs: optionalNumber(
      value.digestDurationMs,
      `catalog entry ${index} digestDurationMs`,
    ),
    // This function rebuilds every entry from an explicit literal, so a field
    // missing HERE is dropped at load with no error anywhere — even though it
    // is present in the catalog the browser just fetched.
    //
    // Read leniently, unlike artifactPath/audioPath above. Those are
    // load-bearing — an entry with a blank one cannot be opened, so refusing
    // the catalog is the honest answer. A blank room is merely a room we do not
    // know, which is the normal state for most of an archive; and
    // validateMeetingCatalog has no per-entry recovery, so throwing here would
    // let one stray `""` from a hand-edited or third-party catalog take down
    // the whole meeting list.
    roomId: optionalRoomString(value.roomId),
    roomName: optionalRoomString(value.roomName),
    jobId: optionalRoomString(value.jobId),
    attemptNumber: optionalPositiveInteger(value.attemptNumber),
  };
}

// optionalPositiveInteger reads an optional 1-based ordinal. Anything that is
// not one — missing, a string, a float, zero — means "not recorded", on the
// same principle as optionalRoomString: this is lineage, not something the
// viewer opens a meeting with, so a bad value must not fail the catalog load.
function optionalPositiveInteger(value: unknown): number | undefined {
  if (typeof value !== "number" || !Number.isInteger(value) || value < 1) {
    return undefined;
  }
  return value;
}

// optionalRoomString reads an optional room field. A missing value, a blank
// one, or one that is not a string all mean the same thing — no room was
// recorded — and none of them is an error.
function optionalRoomString(value: unknown): string | undefined {
  if (typeof value !== "string") {
    return undefined;
  }
  const trimmed = value.trim();
  return trimmed === "" ? undefined : trimmed;
}

// Newest first. Entries whose dateLabel cannot be parsed (e.g. legacy catalogs
// that shipped a raw id as the label) sort after every dated entry instead of
// wherever plain string comparison happens to land them.
export function sortMeetingCatalogEntries(
  meetings: MeetingCatalogEntry[],
): MeetingCatalogEntry[] {
  return [...meetings].sort((left, right) => {
    const leftMs = parseDateLabelMs(left.dateLabel);
    const rightMs = parseDateLabelMs(right.dateLabel);
    if (leftMs !== null && rightMs !== null && leftMs !== rightMs) {
      return rightMs - leftMs;
    }
    if (leftMs !== null && rightMs === null) {
      return -1;
    }
    if (leftMs === null && rightMs !== null) {
      return 1;
    }
    return compareDescending(right.id, left.id);
  });
}

// parseDateLabelMs parses the catalog's "YYYY-MM-DD" / "YYYY-MM-DD HH:MM[:SS]"
// date labels into a comparable timestamp, or null when the label does not
// follow that shape or its fields are out of range (Date.UTC would silently
// roll "2026-13-05" over into 2027 and sort it above real meetings). Labels
// carry no timezone, so UTC is used consistently — only the relative order
// matters here.
export function parseDateLabelMs(dateLabel: string): number | null {
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
  const ms = Date.UTC(
    Number(year),
    monthNum - 1,
    dayNum,
    hourNum,
    minuteNum,
    secondNum,
  );
  return Number.isFinite(ms) ? ms : null;
}

// filterMeetingCatalogEntries narrows the list with a case-insensitive
// substring match against the title, the raw date label ("2026-03-09 21:11"),
// and the short rendered date ("9 Mar 2026") so users can type dates the way
// the cards display them.
export function filterMeetingCatalogEntries(
  meetings: MeetingCatalogEntry[],
  query: string,
): MeetingCatalogEntry[] {
  const needle = query.trim().toLowerCase();
  if (needle === "") {
    return meetings;
  }
  return meetings.filter((meeting) =>
    [
      meeting.title,
      meeting.dateLabel,
      formatMeetingDateShort(meeting.dateLabel),
    ].some((haystack) => haystack.toLowerCase().includes(needle)),
  );
}

// Catalog `dateLabel` ships as ISO-ish "YYYY-MM-DD" or "YYYY-MM-DD HH:MM" and
// carries NO timezone: producers stamp it from different clocks (filename
// wall-clock, UTC-derived job ids). Render the label's own digits in the user's
// locale (e.g. "Fri, Mar 13, 2026, 12:29 PM"), but do NOT attach a
// `timeZoneName` — the label doesn't know its zone, so a "GMT+1" suffix would
// assert an instant we can't actually justify (a UTC-stamped "21:11" would
// display as "9:11 PM GMT+1" to a CET viewer, an hour off and falsely precise).
// We show the wall-clock digits as-is and make no timezone claim (D-484).
export function formatMeetingDate(dateLabel: string): string {
  const match = /^(\d{4})-(\d{2})-(\d{2})(?:[ T](\d{2}):(\d{2}))?/.exec(
    dateLabel,
  );
  if (!match) {
    return dateLabel;
  }
  const [, y, m, d, h, mn] = match;
  const hasTime = h !== undefined && mn !== undefined;
  const date = new Date(
    Number(y),
    Number(m) - 1,
    Number(d),
    hasTime ? Number(h) : 0,
    hasTime ? Number(mn) : 0,
  );
  if (Number.isNaN(date.getTime())) {
    return dateLabel;
  }
  return new Intl.DateTimeFormat(undefined, {
    weekday: "short",
    month: "short",
    day: "numeric",
    year: "numeric",
    ...(hasTime ? { hour: "numeric", minute: "2-digit" } : {}),
  }).format(date);
}

// Hoisted: the filter re-formats every catalog entry on each keystroke, and
// Intl.DateTimeFormat construction is the expensive part.
const SHORT_DATE_FORMAT = new Intl.DateTimeFormat("en-GB", {
  day: "numeric",
  month: "short",
  year: "numeric",
});

// Compact variant of formatMeetingDate for places with little space
// (e.g. catalog cards). "13 Mar 2026, 12:29" — day, short month, year, and the
// meeting's start time when the label carries one (D-685). Locale-pinned to
// en-GB so the day-month-year order and the 24-hour clock are consistent
// regardless of the viewer's browser locale.
//
// The time is appended from the label's own digits rather than through Intl:
// like formatMeetingDate, this makes no timezone claim (D-484), and hour12
// would otherwise turn a compact "12:29" into "12:29 pm" on a card that has
// none of that room.
export function formatMeetingDateShort(dateLabel: string): string {
  const match = /^(\d{4})-(\d{2})-(\d{2})(?:[ T](\d{2}):(\d{2}))?/.exec(dateLabel);
  if (!match) {
    return dateLabel;
  }
  const [, y, m, d, h, mn] = match;
  const date = new Date(Number(y), Number(m) - 1, Number(d));
  if (Number.isNaN(date.getTime())) {
    return dateLabel;
  }
  const day = SHORT_DATE_FORMAT.format(date);
  if (h === undefined || mn === undefined) {
    return day;
  }
  if (Number(h) > 23 || Number(mn) > 59) {
    return day;
  }
  return `${day}, ${h}:${mn}`;
}

// formatMeetingDuration renders a catalog entry's duration the way the browse
// list wants it: a coarse "46m" / "1h 5m", not a clock time. The list is a
// scanning surface — the exact seconds belong to the player, which shows them.
// Rounds to the nearest minute, and floors at "1m" so a short-but-real
// recording never reads as an empty one.
export function formatMeetingDuration(durationMs: number): string {
  const minutes = Math.max(1, Math.round(durationMs / 60000));
  if (minutes < 60) {
    return `${minutes}m`;
  }
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

function requireNonEmptyString(value: unknown, label: string): string {
  if (typeof value !== "string" || value.trim() === "") {
    throw new Error(`${label} must be a non-empty string`);
  }
  return value;
}

function requireNumber(value: unknown, label: string): number {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    throw new Error(`${label} must be a finite number`);
  }
  return value;
}

function optionalNonEmptyString(
  value: unknown,
  label: string,
): string | undefined {
  if (value === undefined) {
    return undefined;
  }
  return requireNonEmptyString(value, label);
}

function optionalNumber(value: unknown, label: string): number | undefined {
  if (value === undefined) {
    return undefined;
  }
  return requireNumber(value, label);
}

function compareDescending(left: string, right: string): number {
  return left.localeCompare(right);
}

function resolveAppAssetUrl(assetPath: string): string {
  return new URL(assetPath, resolveAppBaseUrl()).toString();
}

// resolveCatalogUrl returns the URL the viewer should hit for catalog.json.
//
// Embedded mode (highest precedence): when src/embedded.ts has captured the
// AppAPI proxy base into window.__CASSINI_VIEWER_BASE__ (e.g.
// "/index.php/apps/app_api/proxy/gocassini/"), the published archive is served
// by the operator at "<base>published/". Resolving catalog.json there — rather
// than relative to the embedded page's own pathname (which is the Nextcloud
// /index.php/apps/app_api/embedded/... route, not the proxy) — is what makes
// the fetch land. artifactPath / audioPath follow automatically because they
// resolve against the fetched catalog's response.url.
//
// Standalone mode: resolve relative to the SPA's BASE_URL (Vite base), so a
// stand-alone exported site at https://example.com/foo/ fetches
// https://example.com/foo/catalog.json — what the existing portable export
// flow expects.
function resolveCatalogUrl(): string {
  const viewerBase = readViewerBase();
  if (viewerBase) {
    return new URL("published/catalog.json", viewerBase).toString();
  }
  return resolveAppAssetUrl(DEFAULT_CATALOG_PATH);
}

function resolveCatalogAssetUrl(assetPath: string, catalogUrl: string): string {
  return new URL(assetPath, catalogUrl).toString();
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}
