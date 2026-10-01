import { describe, expect, it } from "vitest";

import panelSource from "./RecordingSetup.svelte?raw";

// The panel does not check, and does not refresh itself.
//
// The operator establishes a baseline when its container boots; after that the
// findings change only when somebody asks for a check. So there is nothing for
// a background refresh to discover, and a page that triggers work on load or on
// a timer is how this panel twice ended up probing without being asked.
describe("the panel never checks on its own", () => {
  it("has no timer", () => {
    expect(panelSource).not.toContain("setInterval");
    expect(panelSource).not.toContain("polling");
  });

  it("reads on mount rather than checking", () => {
    // load(false) is GET /health; load(true) posts a check.
    expect(panelSource).toContain("void load(false)");
    expect(panelSource).not.toContain("void load(true)");
  });

  it("reacts to a setup change by re-reading, not re-probing", () => {
    expect(panelSource).toContain("onSetupChanged(() => void load(false))");
  });

  it("never disables a control for a background refresh", () => {
    expect(panelSource).not.toContain("busy || polling");
    expect(panelSource).toContain("disabled={busy}");
  });
});
