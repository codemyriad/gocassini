import { describe, expect, it } from "vitest";

import appSource from "./App.svelte?raw";

// Source-level assertions, the convention this repo follows for .svelte files
// (see NeedsSetupCard.test.ts): the suite runs in node with no DOM harness.
// What is asserted here is the shell's part of the deep link's round trip,
// which no unit below it can hold on its own.

describe("the shell's setup features", () => {
  it("re-reads them when the reader leaves the operator surface", () => {
    // The whole point of NeedsSetupCard's link is the trip out and back:
    // browse -> "Open AI providers" -> configure -> Back. Read once at mount
    // and never again, the card that sent the administrator still says "No AI
    // endpoint is available" and still links to the panel they just fixed
    // (D-722). Both ways back have to trigger it: the browser's Back button
    // (popstate) and the Browse tab (selectSurface).
    expect(appSource).toContain("function refreshFeaturesOnLeavingOperator(previous: Surface)");
    expect(appSource).toMatch(
      /function handlePopState\(\): void \{\s*const previous = surface;\s*applySurfaceFromLocation\(\);\s*refreshFeaturesOnLeavingOperator\(previous\);/,
    );
    expect(appSource).toMatch(
      /window\.history\.pushState\([^;]*applySurface[^;]*\);\s*refreshFeaturesOnLeavingOperator\(previous\);/,
    );
  });

  it("re-reads them only on the way out of the operator surface", () => {
    // Not on every navigation: the fragment changes on every meeting the
    // viewer opens, and a /setup round trip per click would be a question
    // asked hundreds of times for an answer that changes when an
    // administrator acts.
    expect(appSource).toMatch(
      /if \(previous !== "operator" \|\| surface === "operator"\) \{\s*return;\s*\}/,
    );
  });

  it("lets a failed re-check leave the last answer standing", () => {
    // fetchSetupHealth answers null for a failed or unparseable check, and
    // null is "nobody said" rather than "no". Assigning it would retract what
    // mount established and tell a working deployment it is unconfigured.
    expect(appSource).toMatch(/if \(health\) \{\s*setupFeatures = health\.features;/);
    expect(appSource).toContain("the setup re-check failed.");
  });
});
