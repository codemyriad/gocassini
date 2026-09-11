import { describe, expect, it } from "vitest";
import { render } from "svelte/server";

import type { VocabularyTag } from "../viewer/annotations";
import RoomsRail from "./RoomsRail.svelte";
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

describe("RoomsRail tag filter", () => {
  const tag = (tagId: string, meetings: number, color: VocabularyTag["color"]): VocabularyTag => ({
    tagId,
    namespace: "ns",
    label: tagId,
    meetings,
    marks: meetings,
    color,
    icon: "",
    changedBy: "",
    changedAtUtc: "",
  });
  const tags = [tag("budget", 2, "teal"), tag("hiring", 5, "red")];
  const html = (props: Record<string, unknown>) =>
    render(RoomsRail as never, { props: { tagsOffered: true, tags, ...props } } as never).body;

  it("lists every tag in its colour with its meeting count, most used first, none ticked", () => {
    const rail = html({});
    expect(rail).toMatch(/data-tag-color="red"[\s\S]*>hiring<[\s\S]*>5<[\s\S]*data-tag-color="teal"[\s\S]*>budget</);
    expect(rail).not.toContain(" checked");
    expect(rail).toContain("Manage tags");
  });

  it("offers any or all only once two tags are ticked", () => {
    expect(html({ selectedTagIds: ["hiring"] })).not.toContain("of them");
    expect(html({ selectedTagIds: ["hiring", "budget"] })).toMatch(/aria-pressed="true"[^>]*>any of them/);
  });

  it("says quietly when tags are unavailable", () => {
    expect(html({ tags: null, tagsFailed: true })).toContain("Tags are unavailable right now.");
  });

  it("offers no tag filter where the build cannot tag", () => {
    const rail = render(RoomsRail as never, { props: {} } as never).body;
    expect(rail).not.toContain(">Tags</h2>");
    expect(rail).not.toContain("Manage tags");
  });

  it("asks the shell to open the tag manager", () => {
    expect(roomsRailSource).toContain('dispatch("manageTags")');
  });
});
