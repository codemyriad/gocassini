import type { MeetingCatalogEntry } from "./catalog";

// What the shell should show once a catalog has actually been observed.
export interface CatalogSelection {
  // selectedMeetingId is "" when the list should be shown with nothing open.
  selectedMeetingId: string;
  // notFoundMessage is non-empty only when a specific meeting was asked for and
  // the catalog does not contain it. It must never be set for a catalog that
  // was never loaded — "not found" and "not loaded yet" are different answers.
  notFoundMessage: string;
  // seedHistory reports whether the caller should seed a list history entry
  // below the opened meeting, so Back returns to the list rather than leaving
  // the app.
  seedHistory: boolean;
}

const NOTHING_SELECTED: CatalogSelection = {
  selectedMeetingId: "",
  notFoundMessage: "",
  seedHistory: false,
};

// resolveCatalogSelection decides which meeting a freshly observed catalog
// should open, given a meeting id that was requested but not yet satisfiable.
//
// It exists because that decision used to live only on the mount path
// (App.svelte's `if (catalog)` branch), so a catalog that arrived later — via
// the refresh that recovers from a null-at-mount — resolved nothing: a deep
// link opened during that window was dropped for good, and a fresh install
// whose first recording appeared through the refresh did not auto-open it even
// though reloading the page would have (D-543).
//
// Pure so it can be tested: the viewer's suite runs in Vitest's node
// environment, with no DOM to render a component into.
export function resolveCatalogSelection(
  meetings: readonly MeetingCatalogEntry[],
  requestedMeetingId: string | null,
): CatalogSelection {
  const requested = requestedMeetingId?.trim() ? requestedMeetingId.trim() : null;

  if (requested) {
    const found = meetings.some((meeting) => meeting.id === requested);
    return {
      selectedMeetingId: requested,
      notFoundMessage: found ? "" : `Meeting not found in catalog: ${requested}`,
      seedHistory: true,
    };
  }

  // Nothing was asked for: a single-meeting catalog opens itself, because a
  // list of one is not a choice. Anything else shows the list.
  if (meetings.length === 1) {
    return {
      selectedMeetingId: meetings[0].id,
      notFoundMessage: "",
      seedHistory: true,
    };
  }
  return NOTHING_SELECTED;
}
