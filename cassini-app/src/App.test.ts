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

  it("drives the readiness card and the Generate card from the same bit", () => {
    // buildFeatureNotice returns non-null exactly when `insights` is false, so
    // these two are mutually exclusive by construction rather than by two
    // conditions someone has to keep in step (D-700). Both read setupFeatures
    // and neither invents a second source of truth.
    expect(appSource).toContain("$: insightsReady = setupFeatures?.insights === true;");
    expect(appSource).toContain("{#if insightsReady}");
    expect(appSource).not.toContain("insightsReady = true");
  });

  it("says nothing at all until the deployment has answered", () => {
    // `setupFeatures` is null for a standalone export and for an operator too
    // old to say. Neither card renders there: absence must not read as "not
    // configured", the same three-state rule the catalog's hasSummary follows.
    // `=== true` is what makes null a third state rather than a falsy default.
    expect(appSource).toMatch(/setupFeatures\?\.insights === true/);
  });

  it("fills both Prepare slots wherever the browse surface is mounted", () => {
    // Three call sites — with the operator tab, under an advisory setup strip,
    // and bare — and a reader who picks meetings gets the same panel in all of
    // them. A slot filled in two of the three is a card that appears only for
    // some readers, with nothing on screen to explain why.
    const readiness = appSource.match(/slot="prepare-readiness"/g) ?? [];
    const generate = appSource.match(/slot="prepare-generate"/g) ?? [];
    expect(readiness).toHaveLength(3);
    expect(generate).toHaveLength(3);
  });

  it("builds the template client from the probe, never from the admin hint", () => {
    // The hint (OC.isUserAdmin) exists to stop the operator tab flashing in.
    // `operator/settings/workflows` is ADMIN at the proxy, so acting on the
    // hint here would put a template picker in front of someone whose request
    // for it 403s.
    expect(appSource).toContain(
      "operatorClient = probe.available ? new OperatorClient(operatorBasePath) : null;",
    );
    expect(appSource).not.toMatch(/isLikelyAdminHint[^\n]*operatorClient/);
  });

  it("lets a failed re-check leave the last answer standing", () => {
    // fetchSetupHealth answers null for a failed or unparseable check, and
    // null is "nobody said" rather than "no". Assigning it would retract what
    // mount established and tell a working deployment it is unconfigured.
    expect(appSource).toMatch(
      /if \(health\) \{\s*setupHealth = health;\s*setupFeatures = health\.features;/,
    );
    expect(appSource).toContain("the setup re-check failed.");
  });
});

// D-756: the Setup tab and its wizard are gone, and two things take their
// place — a dialog shown once per install, and a chip that says who can see
// recordings on every browse surface in the shell.
describe("the shell after the Setup tab", () => {
  it("has two tabs, and no way to reach a surface that no longer exists", () => {
    expect(appSource).toContain(">\n        Browse\n      </button>");
    expect(appSource).toContain(">\n        Operator\n      </button>");
    expect(appSource).not.toContain('selectSurface("setup")');
    expect(appSource).not.toContain('surface === "setup"');
    expect(appSource).not.toContain("Setup.svelte");
  });

  it("sends the setup notice's own button somewhere that exists", () => {
    // The broken-install branches still carry a step with `action: "setup"`,
    // and their copy is D-759's to rewrite. Until then the button goes to the
    // operator surface, which is where the storage control lands in phase 3.
    expect(appSource).toContain('on:navigate={() => selectSurface("operator")}');
  });

  it("shows the first-run dialog only to an administrator the operator answered for", () => {
    // operatorAvailable is the same probe that gates the operator surface, and
    // firstRunPlan returns null for everything else — a non-admin, an
    // unreadable /storage, an install already acknowledged.
    expect(appSource).toContain("isAdmin: operatorAvailable,");
    expect(appSource).toContain("setupAvailable: isSetupAvailable(),");
    expect(appSource).toContain("{#if firstRun && operatorClient}");
  });

  it("never lets a failed storage read take the operator surface away", () => {
    // Everything else the shell shows is independent of that answer. No answer
    // means no dialog, not a shell that decided it is not an administrator.
    expect(appSource).toMatch(
      /async function readStorageStatus\([\s\S]{0,400}catch \(error\) \{[\s\S]{0,200}return null;/,
    );
  });

  it("gives every browse surface the audience the chip renders", () => {
    // Three call sites — with the operator tab, under an advisory setup strip,
    // and bare — and the chip is for every user, so a reader must not get a
    // different answer depending on which of them drew their list.
    const mounts = appSource.match(/<ViewerApp \{ncMode\} \{dataProvider\} \{audience\}>/g) ?? [];
    expect(mounts).toHaveLength(3);
    expect(appSource).toContain("$: audience = recordingAudience(setupHealth);");
  });
});
