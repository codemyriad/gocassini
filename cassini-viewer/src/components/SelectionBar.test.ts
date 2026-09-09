import { describe, expect, it } from "vitest";

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
    expect(selectionBarSource).toContain("not shown here");
  });

  it("reports meetings that left the archive rather than dropping them quietly", () => {
    expect(selectionBarSource).toContain("no longer in the archive");
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
    expect(gated).toContain("meetings selected");
    expect(selectionBarSource.slice(droppedGate)).toContain("no longer in the archive");
  });

  it("offers clearing and Prepare, and decides neither itself", () => {
    // Presentational: the shell owns the selection and whether this exists at
    // all, so there is no local state here to drift from it.
    expect(selectionBarSource).toContain('dispatch("clear")');
    expect(selectionBarSource).toContain('dispatch("prepare")');
    expect(selectionBarSource).not.toContain("let selection");
  });
});
