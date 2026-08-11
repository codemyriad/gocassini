import { describe, expect, it } from "vitest";

import { resolveCatalogSelection } from "./catalogSelection";
import type { MeetingCatalogEntry } from "./catalog";

// resolveCatalogSelection is the mount branch's selection decision, lifted out
// so the refresh that recovers a null-at-mount catalog can make the same one
// (D-543). Before it existed, a deep link opened during that window was dropped
// for good and a single-meeting catalog that arrived late did not auto-open.

function entry(id: string): MeetingCatalogEntry {
  return {
    id,
    title: id,
    dateLabel: "2026-06-12",
    audioPath: `./meetings/${id}.opus`,
  } as MeetingCatalogEntry;
}

describe("resolveCatalogSelection", () => {
  it("opens the requested meeting when the catalog has it", () => {
    expect(resolveCatalogSelection([entry("a"), entry("b")], "b")).toEqual({
      selectedMeetingId: "b",
      notFoundMessage: "",
      seedHistory: true,
    });
  });

  it("still opens the requested id when the catalog lacks it, and says so", () => {
    // Selecting the id rather than falling back to the list is deliberate: the
    // user asked for a specific meeting and deserves an answer about it, not a
    // silent redirect to a list that does not contain it.
    expect(resolveCatalogSelection([entry("a")], "missing")).toEqual({
      selectedMeetingId: "missing",
      notFoundMessage: "Meeting not found in catalog: missing",
      seedHistory: true,
    });
  });

  it("auto-opens a single-meeting catalog when nothing was requested", () => {
    expect(resolveCatalogSelection([entry("only")], null)).toEqual({
      selectedMeetingId: "only",
      notFoundMessage: "",
      seedHistory: true,
    });
  });

  it("shows the list for a multi-meeting catalog when nothing was requested", () => {
    expect(resolveCatalogSelection([entry("a"), entry("b")], null)).toEqual({
      selectedMeetingId: "",
      notFoundMessage: "",
      seedHistory: false,
    });
  });

  it("shows the list for an empty catalog", () => {
    // A fresh install with no recordings yet: an empty list, never an error.
    expect(resolveCatalogSelection([], null)).toEqual({
      selectedMeetingId: "",
      notFoundMessage: "",
      seedHistory: false,
    });
  });

  it("treats a blank requested id as no request", () => {
    expect(resolveCatalogSelection([entry("a"), entry("b")], "   ")).toEqual({
      selectedMeetingId: "",
      notFoundMessage: "",
      seedHistory: false,
    });
  });

  it("never reports not-found for an empty catalog and no request", () => {
    // "not loaded yet" and "not found" are different answers; only a specific
    // unsatisfied request may produce the second.
    const selection = resolveCatalogSelection([], null);
    expect(selection.notFoundMessage).toBe("");
  });
});
