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

  it("hands every Generate card's created run back to the viewer", () => {
    // Submit closes the panel (D-749). The viewer owns the list and the panel;
    // the card only knows a run started. The callback rides down as a slot
    // prop, and a call site that forgot it would leave that reader with a
    // button that did nothing visible.
    const forwarded = appSource.match(/on:created=\{\(event\) => onInsightCreated\(event\.detail\)\}/g) ?? [];
    expect(forwarded).toHaveLength(3);
    expect(appSource.match(/let:onInsightCreated/g) ?? []).toHaveLength(3);
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

  it("re-reads them each time the Prepare panel opens, at every mount of the viewer", () => {
    // A non-admin never leaves the operator surface, so the return-leg refresh
    // never fires for them; the panel opening is the moment the readiness card
    // is looked at (D-749).
    const refreshed = appSource.match(/on:prepareOpen=\{\(\) => void refreshSetupFeatures\(\)\}/g) ?? [];
    expect(refreshed).toHaveLength(3);
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
    // The broken-install branches carry a step with `action: "settings"` since
    // D-759. It opens Operator › Publish pipeline, where "Who can see
    // recordings" and the service-account controls live (D-757), rather than
    // the operator's default Recordings panel.
    expect(appSource).toContain("on:navigate={openPublishPipeline}");
    expect(appSource).not.toContain('on:navigate={() => selectSurface("operator")}');
  });

  // "Try again" on the notice is the operator's own re-check (D-759), not
  // another read of the verdict: the verdict is the last recorded outcome of a
  // preflight, so re-reading it would render the same answer and teach an
  // administrator that the button does nothing.
  it("re-runs the operator's check when the notice asks, then re-reads the answer", () => {
    expect(appSource).toContain("on:retry={retrySetupCheck}");
    expect(appSource).toContain("await operatorClient?.recheckStorage();");
    expect(appSource).toMatch(/async function retrySetupCheck[\s\S]{0,900}await readInstanceState\(\);/);
    // The notice says which tone to draw itself in, and the shell says whether
    // the request it asked for is still running.
    expect(appSource).toContain("tone={setupNotice.tone}");
    expect(appSource).toContain("busy={setupRetryBusy}");
  });

  it("gives every browse surface the audience the chip renders", () => {
    // Three call sites — with the operator tab, under an advisory setup strip,
    // and bare — and the chip is for every user, so a reader must not get a
    // different answer depending on which of them drew their list.
    const mounts = appSource.match(/<ViewerApp \{ncMode\} \{dataProvider\} \{audience\}[^>]*>/g) ?? [];
    expect(mounts).toHaveLength(3);
    expect(appSource).toContain("$: audience = recordingAudience(setupHealth);");
  });

  // D-763's warning, rehomed. Its old destination (#surface=setup) is gone
  // (D-756), and a warning that navigates nowhere teaches people to ignore it.
  it("sends the recording warning to where the checks now live", () => {
    expect(appSource).toContain("function openRecordingSetup()");
    // Both hops: the surface alone opens the default panel (Recordings) and
    // leaves the reader hunting for the checks.
    expect(appSource).toContain('applyPanel(applySurface(window.location.hash, "operator"), "pipeline")');
    // Announced once, so the surface, the panel nav and the viewer agree.
    expect(appSource).toContain('window.dispatchEvent(new PopStateEvent("popstate"))');
    // Nothing may point at the surface D-756 removed.
    expect(appSource).not.toContain('selectSurface("setup")');
    expect(appSource).not.toContain('href="#surface=setup"');
  });

  it("offers the route only to someone who can act on it, but tells everyone", () => {
    // A non-admin's every request to that surface 403s at the proxy, so the
    // button is admin-only — while the fact stays visible to everyone, because
    // it explains why their recordings are not appearing.
    expect(appSource).toMatch(/\{#if recordingNeedsAction\}[\s\S]{0,400}\{#if operatorAvailable\}/);
    expect(appSource).toMatch(/function openRecordingSetup\(\)[\s\S]{0,400}if \(!operatorAvailable\)/);
  });
});
