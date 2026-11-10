import { describe, expect, it } from "vitest";
import { render } from "svelte/server";
import { get } from "svelte/store";

import { LL } from "../i18n";

import SelectionBar from "./SelectionBar.svelte";
import selectionBarSource from "./SelectionBar.svelte?raw";

// The viewer's suite runs in node with no DOM to render a component into, so
// the assertions that can be made about a component are made against its
// source. They are chosen to be the ones a reader would call a bug if they
// stopped holding — not a transcription of the markup.

describe("SelectionBar", () => {
  it("is never fixed to the viewport", () => {
    // The app mounts into a shadow root inside a Nextcloud page: a fixed bar
    // floats over Nextcloud's own chrome and the shell's Browse/Operator nav
    // instead of over the list it belongs to. The prototype uses fixed; the
    // product must not. The shell docks this absolutely against the browse
    // shell, the same way the meeting sheet is anchored.
    expect(selectionBarSource).not.toContain("position: fixed");
  });

  it("says how many picked meetings the current narrowing is not showing", () => {
    // A selection spans rooms and survives every filter change, so a bar that
    // reported only the total would read as a claim about the visible list.
    expect(selectionBarSource).toContain("hiddenCount");
    expect(selectionBarSource).toContain("text.not_shown_count({ count: hiddenCount })");
    expect(get(LL).viewer.browse.selection_bar.not_shown_count({ count: 2 })).toBe("2 not shown here.");
  });

  it("reports meetings that left the archive rather than dropping them quietly", () => {
    expect(selectionBarSource).toContain("text.dropped_other({ count: droppedCount })");
    expect(get(LL).viewer.browse.selection_bar.dropped_other({ count: 2 })).toContain("no longer in the archive");
    expect(selectionBarSource).toContain("dismissDropped");
  });

  it("renders the loss on its own when it took the last pick with it", () => {
    // The bar is the only surface that reports a meeting leaving the archive,
    // and the shell keeps it up for a loss with no picks left
    // (selectionModel.shouldShowSelectionBar). A count and a Prepare button
    // over an empty set would describe a bundle nobody can ask for, so both
    // hang off the picks and the notice does not.
    const countGate = selectionBarSource.indexOf("{#if count > 0}");
    const droppedGate = selectionBarSource.indexOf("{#if droppedCount > 0}");
    expect(countGate).toBeGreaterThan(-1);
    expect(droppedGate).toBeGreaterThan(countGate);
    const gated = selectionBarSource.slice(countGate, droppedGate);
    expect(gated).toContain('dispatch("prepare")');
    expect(gated).toContain("text.selected_other({ count })");
    expect(selectionBarSource.slice(droppedGate)).toContain("text.dropped_one()");
  });

  it("offers clearing and Prepare, and decides neither itself", () => {
    // Presentational: the shell owns the selection and whether this exists at
    // all, so there is no local state here to drift from it.
    expect(selectionBarSource).toContain('dispatch("clear")');
    expect(selectionBarSource).toContain('dispatch("prepare")');
    expect(selectionBarSource).not.toContain("let selection");
  });

  it("refuses Prepare above the operator's cap, and says the number", () => {
    // Every action in the panel would be refused above it; the bar says so
    // before the panel opens (D-749).
    expect(selectionBarSource).toContain('import { MAX_SELECTED_MEETINGS } from "../viewer/selectionModel";');
    expect(selectionBarSource).toContain("$: overCap = count > MAX_SELECTED_MEETINGS;");
    expect(selectionBarSource).toContain("disabled={overCap}");
    expect(selectionBarSource).toContain("text.over_cap_other({ max: MAX_SELECTED_MEETINGS, count: excess })");
    expect(get(LL).viewer.browse.selection_bar.over_cap_other({ max: 100, count: 3 })).toBe(
      "You can work with up to 100 meetings at once. Unpick 3.",
    );
  });
});

describe("SelectionBar tagging", () => {
  const html = (props: Record<string, unknown>) =>
    render(SelectionBar as never, { props: { count: 5, ...props } } as never).body;

  it("offers Tag only where tagging is", () => {
    expect(html({})).not.toContain("selbar-tag");
    expect(html({ tags: [] })).toMatch(/class="selbar-tag[^"]*"[^>]*aria-haspopup="dialog"/);
  });

  it("opens the picker over every selected meeting, ticked and half-ticked", () => {
    expect(selectionBarSource).toMatch(/multiple\s+selected=\{tagSelected\}\s+mixed=\{tagMixed\}\s+anchor=\{tagButton\}/);
  });

  it("disables another bulk edit and exposes a safe retry", () => {
    expect(html({ tags: [], tagBusy: true })).toMatch(/class="selbar-tag[^"]*"[^>]*disabled/);
    expect(html({ tags: [], tagReport: "Could not confirm", tagRetry: true })).toContain("Retry tag update");
  });

  it("says plainly how a bulk tag went", () => {
    expect(html({ tags: [], tagReport: "Tagged 4 of 5 — 1 failed" })).toContain("Tagged 4 of 5 — 1 failed");
  });
});
