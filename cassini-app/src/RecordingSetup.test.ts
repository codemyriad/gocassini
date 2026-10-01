import { describe, expect, it } from "vitest";

import panelSource from "./RecordingSetup.svelte?raw";

// The panel re-reads findings every five seconds. That is a READ, and it used
// to disable every button for its duration — so the whole row of controls
// flickered on a 5s cycle — and to early-return out of every user action, so a
// click landing during a refresh was silently dropped.
describe("a background refresh does not interrupt the reader", () => {
  it("never disables a control because a poll is in flight", () => {
    expect(panelSource).not.toContain("disabled={busy || polling}");
    expect(panelSource).toContain("disabled={busy}");
  });

  it("does not drop a user action that lands during a poll", () => {
    // `polling` may still guard the poll from overlapping itself; it must not
    // guard the handlers a reader triggers.
    for (const handler of ["async function load(", "async function repair("]) {
      const at = panelSource.indexOf(handler);
      expect(at).toBeGreaterThan(-1);
      const body = panelSource.slice(at, at + 400);
      expect(body).not.toContain("busy || polling");
    }
  });

  it("claims the response so a stale poll cannot overwrite it", () => {
    // The poll captures reportVersion before its request and discards its
    // result if it moved. User actions bump it, which is what makes the
    // previous test safe rather than merely racy.
    expect(panelSource).toContain("++reportVersion");
    expect(panelSource).toContain("version === reportVersion");
  });
});
