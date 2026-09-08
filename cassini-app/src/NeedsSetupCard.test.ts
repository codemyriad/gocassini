import { describe, expect, it } from "vitest";

import needsSetupCardSource from "./NeedsSetupCard.svelte?raw";
import { buildFeatureNotice } from "./operator/setupHealth";
import { buildRunFailureNotice } from "./insights/client";

// Source-level assertions, the convention this repo already follows for
// .svelte files (see InsightTemplatesPanel.test.ts): the suite runs in node
// with no DOM harness. What is asserted here is what a reader would call a bug
// — the card inventing copy, offering a non-admin a control that 403s, or
// treating an unanswered question as "not configured".

describe("NeedsSetupCard", () => {
  it("renders a decision it did not make", () => {
    // Every word comes from buildFeatureNotice, where a test can reach it. A
    // sentence written here would be a second account of what this deployment
    // does with your transcript, free to drift from the one in setupHealth.ts
    // and from docs/privacy.md.
    expect(needsSetupCardSource).toContain("{notice.title}");
    expect(needsSetupCardSource).toContain("{notice.summary}");
    expect(needsSetupCardSource).toContain("{notice.actionLabel}");
    expect(needsSetupCardSource).not.toContain("administrator can");
    expect(needsSetupCardSource).not.toContain("endpoint is configured");
  });

  it("says nothing when nobody answered", () => {
    // A null notice is BOTH "configured" and "the question was never asked" —
    // the standalone export has no operator to ask. Rendering an empty card, or
    // a "not configured" one, would be the app claiming something it does not
    // know.
    expect(needsSetupCardSource).toContain("{#if notice}");
    expect(needsSetupCardSource).toContain("export let notice: FeatureNotice | null = null;");
  });

  it("offers the link only when the notice carries a panel", () => {
    // buildFeatureNotice leaves panel empty for anyone who is not an
    // administrator, and that is the whole of the gate: the card must not have
    // a second opinion about who may act.
    expect(needsSetupCardSource).toContain("{#if notice.panel}");
    expect(needsSetupCardSource).not.toContain("isAdmin");
  });

  it("builds a route-preserving link, from the fragment at the moment it is followed", () => {
    // Following this and coming back must not cost the reader the meeting they
    // had open: applySurface keeps the viewer's params (and writes surface
    // first), applyPanel appends without ever landing past the viewer's `t=`.
    // Hand-assembling the hash here is how that gets undone.
    expect(needsSetupCardSource).toContain(
      'applyPanel(applySurface(window.location.hash, "operator"), panel)',
    );
    expect(needsSetupCardSource).not.toContain('"#surface=operator');
    // Read inside a function rather than captured into a variable, so the
    // click the shell acts on is assembled from the fragment as it stands at
    // that moment — the viewing layer rewrites it on every navigation, and a
    // URL frozen earlier describes a page the reader has already left. The
    // href attribute is a render-time snapshot of the same call (the viewer
    // moves by pushState, which fires no event to recompute it on); it is what
    // a middle-click gets, and handleOpen is what every ordinary click gets.
    expect(needsSetupCardSource).toContain("function panelUrl(");
    expect(needsSetupCardSource).toContain("href={panelUrl(notice.panel)}");
    expect(needsSetupCardSource).toContain(
      'dispatch("open", { panel: notice.panel, href: panelUrl(notice.panel) })',
    );
  });

  it("leaves a modified click to the browser", () => {
    // It is a real link with a real address. Swallowing cmd/ctrl-click into an
    // in-page surface switch would take away the one thing an anchor is for.
    expect(needsSetupCardSource).toContain("event.metaKey || event.ctrlKey");
  });
});

describe("the locked card", () => {
  it("is compact for an administrator and prose for everyone else", () => {
    // An admin looking at an unconfigured capability knows what an endpoint is
    // and the button is right there; three sentences under a warning triangle
    // are in the way of it. A non-admin has no button and needs the sentences.
    const forAdmin = buildFeatureNotice({
      features: { summaries: false, insights: false },
      feature: "insights",
      isAdmin: true,
    });
    expect(forAdmin?.actionTitle).toBe("Add a provider to create insights");
    expect(forAdmin?.actionShortLabel).toBe("Add a provider");

    const forEveryoneElse = buildFeatureNotice({
      features: { summaries: false, insights: false },
      feature: "insights",
      isAdmin: false,
    });
    expect(forEveryoneElse?.actionTitle).toBeUndefined();
    expect(forEveryoneElse?.summary).toContain("nothing wrong with your account");
  });

  it("names the remedy for the capability that is missing", () => {
    const summaries = buildFeatureNotice({
      features: { summaries: false, insights: true },
      feature: "summaries",
      isAdmin: true,
    });
    expect(summaries?.actionTitle).toBe("Add a provider to write summaries");
  });

  it("keeps the prose for a run that failed", () => {
    // "The endpoint rejected the request" is not a state anybody can infer from
    // a title, so a failure never collapses into the compact card even for an
    // administrator who could act on it.
    const failure = buildRunFailureNotice({
      run: {
        id: "r1",
        status: "failed",
        // classifyRunError matches on the reason key the operator reports.
        error: "provider-refused: 401 from the endpoint",
      } as Parameters<typeof buildRunFailureNotice>[0]["run"],
      isAdmin: true,
    });
    expect(failure?.actionTitle).toBeUndefined();
    expect(failure?.panel).toBe("endpoints");
  });
});
