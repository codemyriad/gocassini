// Cross-meeting search from the meeting list (D-736).
//
// The list's own filter (filterMeetingCatalogEntries) matches title and date
// only, synchronously, over an array already in memory. This is the other half:
// what was SAID, answered by the operator's `published/search`.
//
// Two properties shape everything here.
//
// The first is that a remote search has states a local filter does not. An
// array filter is either "matches" or "does not match"; a request can also be
// in flight, refused for rate (429), unavailable because the index is not built
// (503), or broken because the archive is unreachable (502). D-736 asks for
// each of those to stay distinguishable from "nothing matched", because a
// search that silently answers "no results" when Nextcloud is down tells a
// caller the opposite of the truth. So the outcome is a tagged union and there
// is no shape in it that collapses a failure into an empty list.
//
// The second is that the endpoint answers about MEETINGS the caller can read —
// the visible set is resolved per request from Nextcloud, never cached here.
// The viewer therefore never filters hits itself: what comes back is already
// what this account may see, and re-filtering it against a stale local catalog
// could only ever hide rows that are legitimately visible.

import { readViewerBase } from "./appBase";
import type { MeetingCatalogEntry } from "./catalog";

const SEARCH_PATH = "published/search";

// One moment in one meeting. Mirrors searchResponseHit on the wire.
export interface MeetingSearchHit {
  readonly meetingId: string;
  readonly title: string;
  readonly dateLabel: string;
  readonly roomId: string;
  readonly roomName: string;
  readonly segmentId: string;
  readonly startMs: number;
  readonly endMs: number;
  readonly speakerId: string;
  // "exact" when the caller's own words matched, "alias" when only a known
  // mistranscription of them did. Surfaced because finding "casino" is not the
  // same as finding "cassini", and a row that hides the difference is lying by
  // omission.
  readonly matched: string;
  // A bounded quote of the segment, centred on the term that hit. May be empty
  // for an index written before D-736 and not yet rebuilt.
  readonly snippet: string;
}

// What the index could actually search over, so an answer never implies
// coverage it does not have.
export interface MeetingSearchCoverage {
  readonly visible: number;
  readonly searched: number;
}

// The tagged union. Every failure is its own case ON PURPOSE: collapsing any of
// them into `{ hits: [] }` would render as "nothing matched".
export type MeetingSearchOutcome =
  | { readonly status: "ok"; readonly hits: MeetingSearchHit[]; readonly coverage: MeetingSearchCoverage; readonly widened: boolean }
  // This build has no operator behind it (standalone export), or the operator
  // predates the route. Not an error and not an empty result: the caller should
  // keep offering the local title/date filter and say nothing about transcripts.
  | { readonly status: "unsupported" }
  | { readonly status: "rateLimited"; readonly message: string }
  | { readonly status: "indexUnavailable"; readonly message: string }
  | { readonly status: "failed"; readonly message: string };

// resolveSearchUrl returns "" when there is no operator to ask.
export function resolveSearchUrl(): string {
  const viewerBase = readViewerBase();
  if (!viewerBase) {
    return "";
  }
  return new URL(SEARCH_PATH, viewerBase).toString();
}

export interface MeetingSearchOptions {
  // Cap on hits from any one meeting. Without it a meeting with sixty hits
  // fills the page and every other matching meeting is absent from the list —
  // which reads as "those meetings do not match".
  readonly perMeeting?: number;
  readonly limit?: number;
  readonly signal?: AbortSignal;
}

// searchMeetingTranscripts asks the operator what was said.
//
// A blank query is answered locally with an empty OK rather than by asking the
// server: it is not a question, and the endpoint would reject it as one.
export async function searchMeetingTranscripts(
  query: string,
  options: MeetingSearchOptions = {},
): Promise<MeetingSearchOutcome> {
  const trimmed = query.trim();
  if (trimmed === "") {
    return { status: "ok", hits: [], coverage: { visible: 0, searched: 0 }, widened: false };
  }
  const base = resolveSearchUrl();
  if (!base) {
    return { status: "unsupported" };
  }

  const target = new URL(base);
  target.searchParams.set("q", trimmed);
  if (options.perMeeting && options.perMeeting > 0) {
    target.searchParams.set("perMeeting", String(options.perMeeting));
  }
  if (options.limit && options.limit > 0) {
    target.searchParams.set("limit", String(options.limit));
  }

  let response: Response;
  try {
    // no-store for the same reason the catalog uses it: the visible set is
    // recomputed per request, and a cached answer would pin a set that a
    // revocation has since narrowed.
    response = await fetch(target.toString(), { cache: "no-store", signal: options.signal });
  } catch (error) {
    // An aborted request is the caller's own doing (they kept typing), not a
    // failure to report. Rethrow so the caller can drop it silently.
    if (isAbortError(error)) {
      throw error;
    }
    return { status: "failed", message: "Could not reach the search index." };
  }

  if (response.status === 404) {
    return { status: "unsupported" };
  }
  if (response.status === 429) {
    return {
      status: "rateLimited",
      message: await errorMessage(response, "Too many searches just now — try again in a moment."),
    };
  }
  if (response.status === 503) {
    return {
      status: "indexUnavailable",
      message: await errorMessage(response, "The search index is not ready yet."),
    };
  }
  if (!response.ok) {
    return {
      status: "failed",
      message: await errorMessage(response, `Could not search the meetings (HTTP ${response.status}).`),
    };
  }

  try {
    return materializeSearchResponse(await response.json());
  } catch {
    return { status: "failed", message: "The search index answered something this viewer cannot read." };
  }
}

export function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError";
}

// materializeSearchResponse is deliberately forgiving about MISSING fields and
// strict about the envelope: a hit with no speaker or no snippet is a hit, but
// a body that is not the search response is not something to render as zero
// results.
export function materializeSearchResponse(value: unknown): MeetingSearchOutcome {
  if (!isRecord(value) || !Array.isArray(value.hits)) {
    throw new Error("not a search response");
  }
  const hits: MeetingSearchHit[] = [];
  for (const raw of value.hits) {
    if (!isRecord(raw)) {
      continue;
    }
    const meetingId = optionalString(raw.meetingId);
    if (!meetingId) {
      // Without an id the row cannot be attributed to a meeting or linked to,
      // so it would render as an orphan. Drop it rather than show it.
      continue;
    }
    hits.push({
      meetingId,
      title: optionalString(raw.title) ?? "",
      dateLabel: optionalString(raw.dateLabel) ?? "",
      roomId: optionalString(raw.roomId) ?? "",
      roomName: optionalString(raw.roomName) ?? "",
      segmentId: optionalString(raw.segmentId) ?? "",
      startMs: finiteNumber(raw.startMs) ?? 0,
      endMs: finiteNumber(raw.endMs) ?? 0,
      speakerId: optionalString(raw.speakerId) ?? "",
      matched: optionalString(raw.matched) ?? "",
      snippet: optionalString(raw.snippet) ?? "",
    });
  }
  const coverage = isRecord(value.coverage) ? value.coverage : {};
  return {
    status: "ok",
    hits,
    coverage: {
      visible: finiteNumber(coverage.visible) ?? 0,
      searched: finiteNumber(coverage.searched) ?? 0,
    },
    widened: value.widened === true,
  };
}

// A meeting the list should show because something in it was said, plus the
// moments that matched.
export interface MeetingTranscriptMatch {
  readonly meetingId: string;
  readonly hits: readonly MeetingSearchHit[];
}

// groupHitsByMeeting preserves the server's ranking: meetings appear in the
// order their best hit did, and each meeting's hits stay in rank order.
//
// The order matters and is not incidental — the server ranked these, and
// re-sorting them here (by date, say) would throw away the only signal that
// says which meeting best answers the question.
export function groupHitsByMeeting(hits: readonly MeetingSearchHit[]): MeetingTranscriptMatch[] {
  const order: string[] = [];
  const byMeeting = new Map<string, MeetingSearchHit[]>();
  for (const hit of hits) {
    const existing = byMeeting.get(hit.meetingId);
    if (existing) {
      existing.push(hit);
      continue;
    }
    order.push(hit.meetingId);
    byMeeting.set(hit.meetingId, [hit]);
  }
  return order.map((meetingId) => ({ meetingId, hits: byMeeting.get(meetingId) ?? [] }));
}

// mergeSearchResults is what the list actually renders: the meetings whose
// title or date matched, plus the meetings something was SAID in.
//
// Name/date matches come first and keep the catalog's own order, because that
// is the list the user already had and reordering it under them is
// disorienting. Transcript-only matches follow in the server's rank order. A
// meeting that matched both ways appears once, in its original position, and
// still carries its moments.
export function mergeSearchResults<E extends { readonly id: string }>(
  localMatches: readonly E[],
  allMeetings: readonly E[],
  matches: readonly MeetingTranscriptMatch[],
): { readonly entry: E; readonly hits: readonly MeetingSearchHit[] }[] {
  const byId = new Map(allMeetings.map((meeting) => [meeting.id, meeting]));
  const hitsById = new Map(matches.map((match) => [match.meetingId, match.hits]));

  const rows: { entry: E; hits: readonly MeetingSearchHit[] }[] = [];
  const seen = new Set<string>();
  for (const entry of localMatches) {
    rows.push({ entry, hits: hitsById.get(entry.id) ?? [] });
    seen.add(entry.id);
  }
  for (const match of matches) {
    if (seen.has(match.meetingId)) {
      continue;
    }
    const entry = byId.get(match.meetingId);
    if (!entry) {
      // The endpoint hydrates from the caller's own catalog, so a hit for a
      // meeting this list has never heard of means the two went out of sync —
      // a publish between the list load and the search. Skipping it keeps the
      // row from rendering with no title or date; the next refresh picks it up.
      continue;
    }
    rows.push({ entry, hits: match.hits });
    seen.add(match.meetingId);
  }
  return rows;
}

async function errorMessage(response: Response, fallback: string): Promise<string> {
  try {
    const body = (await response.json()) as unknown;
    if (isRecord(body) && typeof body.error === "string" && body.error.trim() !== "") {
      return body.error;
    }
  } catch {
    // A proxy error page is not worth quoting back at the reader.
  }
  return fallback;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function optionalString(value: unknown): string | undefined {
  if (typeof value !== "string") {
    return undefined;
  }
  const trimmed = value.trim();
  return trimmed === "" ? undefined : trimmed;
}

function finiteNumber(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}
