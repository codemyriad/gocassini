import type { MeetingCatalogEntry } from "./catalog";

// Which meetings are PICKED, for the context bundle Prepare hands over (D-626).
//
// This is not catalogSelection.ts. That module answers which single meeting to
// OPEN — a routing decision, driven by the `meeting` hash param, that ends in
// one id or none. This one answers which meetings a bundle is being assembled
// from, which is a set, spans rooms, and is never routed. Keeping them apart is
// deliberate: they change for different reasons and a single "selection" that
// meant both would have to fight over what a click on a row means.
//
// Pure, for the same reason catalogSelection.ts and rooms.ts are: the viewer's
// suite runs in Vitest's node environment with no DOM to render a component
// into, so the decisions that matter — including every sentence the Prepare
// panel puts on screen about what a selection contains — have to live somewhere
// a test can reach.

export interface MeetingSelection {
  // Insertion order, not catalog order: the bundle prints its meetings in the
  // order the caller named them, so the order a user picks in is the order an
  // agent reads in. A Set would lose exactly that.
  ids: readonly string[];
  // Picked meetings the catalog no longer describes. The embedded viewer
  // re-reads the catalog every 15 seconds, so a meeting can leave the archive
  // while it sits in a selection; dropping it silently would change the bundle
  // under the user, which is the one thing a document assembled from a named
  // set must not do. Held here so the shell can say so, and cleared only when
  // the user has seen it (acknowledgeDropped) or clears the selection.
  dropped: readonly string[];
}

export const EMPTY_SELECTION: MeetingSelection = { ids: [], dropped: [] };

export function isSelected(selection: MeetingSelection, id: string): boolean {
  return selection.ids.includes(id);
}

// toggleSelected adds an id to the end (see MeetingSelection.ids for why the
// end) or removes it, leaving the rest of the order untouched.
export function toggleSelected(selection: MeetingSelection, id: string): MeetingSelection {
  if (isSelected(selection, id)) {
    return { ...selection, ids: selection.ids.filter((selectedId) => selectedId !== id) };
  }
  // A first pick into an empty selection starts a new one, so a loss notice
  // left over from the selection before it is expired here. It described a set
  // the user no longer has; carried forward it would attach "1 selected meeting
  // is no longer in the archive" to a meeting picked minutes later, which reads
  // as a claim about the new pick. A loss inside a selection that still has
  // picks is a different thing and survives — see the branch below.
  if (selection.ids.length === 0) {
    return { ids: [id], dropped: [] };
  }
  return { ...selection, ids: [...selection.ids, id] };
}

// shouldShowSelectionBar decides whether the shell docks the bar at all.
//
// Not `ids.length > 0`: the bar is the only thing that renders the loss notice,
// so gating it on the picks alone means the one case where every pick was
// dropped — a deleted meeting, an unshared one, or the operator's per-caller
// catalog momentarily failing closed to empty — clears the checkboxes and says
// nothing. That is exactly the silent shrink reconcileSelection exists to
// prevent, so the decision lives here where a test can reach it rather than in
// the template.
export function shouldShowSelectionBar(selection: MeetingSelection): boolean {
  return selection.ids.length > 0 || selection.dropped.length > 0;
}

export function clearSelection(): MeetingSelection {
  return EMPTY_SELECTION;
}

// reconcileSelection drops picked ids the catalog no longer contains and
// records them, so the shell can report the loss instead of quietly shrinking
// the bundle. The same self-heal App.svelte already performs for a room key
// whose bucket disappeared, with the loss made visible rather than swallowed.
//
// Returns the SAME object when nothing changed. The caller re-runs this on
// every catalog it observes, and an assignment on each pass would invalidate
// derived state — and the selection bar's own identity — four times a minute
// for no reason.
export function reconcileSelection(
  selection: MeetingSelection,
  meetings: readonly MeetingCatalogEntry[],
): MeetingSelection {
  if (selection.ids.length === 0) {
    return selection;
  }
  const present = new Set(meetings.map((meeting) => meeting.id));
  const kept = selection.ids.filter((id) => present.has(id));
  if (kept.length === selection.ids.length) {
    return selection;
  }
  const lost = selection.ids.filter((id) => !present.has(id));
  return {
    ids: kept,
    // Accumulated, not replaced: a second refresh that drops nothing must not
    // erase the notice about the first one before it has been read.
    dropped: [...selection.dropped, ...lost.filter((id) => !selection.dropped.includes(id))],
  };
}

// acknowledgeDropped clears the loss notice without touching the picks.
export function acknowledgeDropped(selection: MeetingSelection): MeetingSelection {
  if (selection.dropped.length === 0) {
    return selection;
  }
  return { ...selection, dropped: [] };
}

// selectedEntries resolves the picked ids against the catalog, in pick order.
// An id with no entry is skipped rather than faked: reconcileSelection is what
// removes those, and inventing a placeholder here would put a meeting in the
// panel's list that the bundle cannot contain.
export function selectedEntries(
  selection: MeetingSelection,
  meetings: readonly MeetingCatalogEntry[],
): MeetingCatalogEntry[] {
  const byId = new Map(meetings.map((meeting) => [meeting.id, meeting]));
  const entries: MeetingCatalogEntry[] = [];
  for (const id of selection.ids) {
    const entry = byId.get(id);
    if (entry) {
      entries.push(entry);
    }
  }
  return entries;
}

// countHiddenByView reports how many picked meetings the list is not currently
// showing. A selection spans rooms and survives every narrowing, so without
// this the bar's count reads as a claim about the visible list — "3 selected"
// over a list showing one of them — and the bundle arrives with meetings the
// user last saw somewhere else.
export function countHiddenByView(
  selection: MeetingSelection,
  visible: readonly MeetingCatalogEntry[],
): number {
  if (selection.ids.length === 0) {
    return 0;
  }
  const shown = new Set(visible.map((meeting) => meeting.id));
  return selection.ids.filter((id) => !shown.has(id)).length;
}

// What a selection contains, read from the catalog alone (D-716) and never by
// fetching a recording. Every "without" count has an "unknown" twin, because
// hasSummary and wordCount are optional and their absence is a third state: an
// archive published before those fields existed says nothing about either, and
// counting that silence as "no summary" or "0 words" would state a gap that is
// not there — as wrong as hiding one.
export interface SelectionTotals {
  count: number;
  // Sum of the word counts that are known. A floor, not a total, whenever
  // meetingsWithoutWordCount is non-zero.
  wordCount: number;
  meetingsWithWordCount: number;
  meetingsWithoutWordCount: number;
  // hasSummary === false: the recording was published carrying no summary.
  withoutSummary: number;
  // hasSummary === undefined: the catalog does not say either way.
  summaryUnknown: number;
  // No audioPath, so no single-file recording for the bundle to be read from.
  withoutPortableAudio: number;
}

// lacksPortableAudio is the one gap a single row can be marked with, and it is
// the same test summarizeSelection counts by. Exported so the panel marks its
// rows with it rather than re-deciding, one line under a sentence counting
// them, what "predates the single-file format" means.
export function lacksPortableAudio(entry: MeetingCatalogEntry): boolean {
  return !entry.audioPath;
}

export function summarizeSelection(entries: readonly MeetingCatalogEntry[]): SelectionTotals {
  const totals: SelectionTotals = {
    count: entries.length,
    wordCount: 0,
    meetingsWithWordCount: 0,
    meetingsWithoutWordCount: 0,
    withoutSummary: 0,
    summaryUnknown: 0,
    withoutPortableAudio: 0,
  };
  for (const entry of entries) {
    if (typeof entry.wordCount === "number") {
      totals.wordCount += entry.wordCount;
      totals.meetingsWithWordCount += 1;
    } else {
      totals.meetingsWithoutWordCount += 1;
    }
    if (entry.hasSummary === false) {
      totals.withoutSummary += 1;
    } else if (entry.hasSummary === undefined) {
      totals.summaryUnknown += 1;
    }
    if (lacksPortableAudio(entry)) {
      totals.withoutPortableAudio += 1;
    }
  }
  return totals;
}

// Pinned to en-GB for the same reason the catalog's date formatters are: the
// grouping separator should not change with the browser's locale in a panel
// whose numbers are compared against the CLI's.
const WORD_COUNT_FORMAT = new Intl.NumberFormat("en-GB");

// formatWordCount renders one meeting's size. An entry with no wordCount says
// so; it does not read as a short meeting.
export function formatWordCount(wordCount: number | undefined): string {
  if (typeof wordCount !== "number") {
    return "Length not recorded";
  }
  return `${WORD_COUNT_FORMAT.format(wordCount)} words`;
}

// formatSelectionWordCount renders the selection's total, saying "at least"
// whenever some of it is unknown — a total that silently omits the meetings it
// could not measure understates the bundle an agent is about to be handed.
export function formatSelectionWordCount(totals: SelectionTotals): string {
  if (totals.meetingsWithWordCount === 0) {
    return "Length not recorded";
  }
  const words = `${WORD_COUNT_FORMAT.format(totals.wordCount)} words`;
  return totals.meetingsWithoutWordCount > 0 ? `At least ${words}` : words;
}

// describeSelectionGaps is the Prepare panel's disclosure: what this bundle
// will not contain, stated before it is handed over rather than discovered
// downstream. Sentences, in the order they should be read, and none at all
// when the selection has no gaps.
//
// It lives here, not in the component, because the counts and the wording are
// the same decision — "2 have no summary" and "2 do not say whether they have
// one" are different claims about different meetings, and getting that pairing
// wrong is the failure this panel exists to prevent.
export function describeSelectionGaps(totals: SelectionTotals): string[] {
  const gaps: string[] = [];
  if (totals.withoutSummary > 0) {
    gaps.push(
      totals.withoutSummary === 1
        ? "One of these has no summary. Its transcript is complete; only the summary section is missing."
        : `${totals.withoutSummary} of these have no summary. Their transcripts are complete; only the summary section is missing.`,
    );
  }
  if (totals.summaryUnknown > 0) {
    // Deliberately not folded into the sentence above. These meetings were
    // published before the index recorded summaries, so "no summary" is a claim
    // nothing here can support — and the bundle will carry one for each of them
    // that turns out to have it.
    gaps.push(
      totals.summaryUnknown === 1
        ? "One of these does not record whether it has a summary. The bundle carries one if the recording holds it."
        : `${totals.summaryUnknown} of these do not record whether they have a summary. The bundle carries one wherever the recording holds it.`,
    );
  }
  if (totals.withoutPortableAudio > 0) {
    // Certain, and about the whole bundle, because that is what happens: every
    // meeting in a bundle is read from its single-file recording, and one that
    // has none is not in the operator's readable set at all — the request comes
    // back 404 for the set, not for the meeting. Hedging it as "may fail for
    // it" left the reader to discover both the certainty and the scope from an
    // error that blames availability. The panel marks the rows, so the meeting
    // to unpick can be found without counting.
    gaps.push(
      totals.withoutPortableAudio === 1
        ? "One of these predates the single-file format — it is marked in the list. The bundle is read from that file, so Prepare will fail for the whole selection until you unpick it."
        : `${totals.withoutPortableAudio} of these predate the single-file format — they are marked in the list. The bundle is read from those files, so Prepare will fail for the whole selection until you unpick them.`,
    );
  }
  if (totals.meetingsWithoutWordCount > 0 && totals.meetingsWithWordCount > 0) {
    gaps.push(
      totals.meetingsWithoutWordCount === 1
        ? "One of these does not record its length, so the total is a floor."
        : `${totals.meetingsWithoutWordCount} of these do not record their length, so the total is a floor.`,
    );
  }
  return gaps;
}
