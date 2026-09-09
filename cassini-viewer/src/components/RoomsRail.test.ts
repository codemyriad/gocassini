import { describe, expect, it } from "vitest";

import roomsRailSource from "./RoomsRail.svelte?raw";

describe("RoomsRail Show filter", () => {
  it("holds the type filter beside the rooms, not over the list", () => {
    // Both are narrowings of the same archive, so they belong in the same
    // column — which is the design prototype's own arrangement. It was in the
    // list header while `types` was list-local state.
    expect(roomsRailSource).toContain(">Show</h2>");
    expect(roomsRailSource).toContain('data-type="meetings"');
    expect(roomsRailSource).toContain('data-type="insights"');
  });

  it("offers no Show section where insights cannot exist", () => {
    // A standalone export's provider cannot list them, and a control that
    // narrows to a kind of thing which cannot exist here is a promise the build
    // cannot keep.
    expect(roomsRailSource).toContain("export let insightsOffered = false;");
    expect(roomsRailSource).toMatch(/\{#if insightsOffered\}[\s\S]{0,200}>Show<\/h2>/);
  });

  it("cannot be narrowed down to showing nothing at all", () => {
    // Everything hidden is indistinguishable on screen from nothing being here,
    // and from here it is one click away.
    expect(roomsRailSource).toContain('disabled={isLastBrowseType(types, "meetings")}');
    expect(roomsRailSource).toContain('disabled={isLastBrowseType(types, "insights")}');
  });

  it("reports what is behind each box", () => {
    // A count under the current room AND the current search: the question a box
    // answers is "is there anything there?".
    expect(roomsRailSource).toContain("export let meetingCount = 0;");
    expect(roomsRailSource).toContain("export let insightCount = 0;");
  });

  it("colours each box as the thing it shows", () => {
    // So the filter reads against the list rather than against itself:
    // base-content for a meeting row, secondary — this theme's amber, the
    // colour every insight surface uses — for an insight card.
    expect(roomsRailSource).toContain(
      '.type-row input[data-type="insights"]:checked {\n    background-color: var(--color-secondary);',
    );
  });

  it("decides nothing itself", () => {
    // Presentational, like the room list above it: the shell owns which kinds
    // are showing and derives every count here.
    expect(roomsRailSource).toContain('dispatch("toggleType", "meetings")');
    expect(roomsRailSource).not.toContain("toggleBrowseType");
  });
});
