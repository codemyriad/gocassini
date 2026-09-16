import { describe, expect, it } from "vitest";

import insightCardSource from "./InsightCard.svelte?raw";

// Source-level assertions, for the reason MeetingList.test.ts gives: the suite
// runs in node with no DOM harness. What is asserted here is what makes an
// insight card an insight card rather than a meeting row wearing a label.

describe("InsightCard", () => {
  it("is a card, not a row: one control, and no way to pick it", () => {
    // Same list, visibly a different kind of thing — and a bundle is made of
    // meetings, so there is nothing on the card to select.
    expect(insightCardSource).toContain('class="insight-card"');
    expect(insightCardSource).not.toContain("<input");
    expect(insightCardSource).toContain('dispatch("open")');
  });

  it("says what the run is when it is not an answer yet", () => {
    // A queued or failed insight is in the list too, or it materialises from
    // nowhere a minute later; the badge is the only thing separating the three
    // states from a card with a document behind it.
    expect(insightCardSource).toContain('pending = insight.status !== "succeeded"');
    expect(insightCardSource).toContain("{#if pending}");
    expect(insightCardSource).toContain("formatInsightStatus(insight.status)");
  });

  it("scans as the question it asked and how much it read", () => {
    expect(insightCardSource).toContain("insightHeadline(insight)");
    expect(insightCardSource).toContain("formatInsightCreated(insight)");
    expect(insightCardSource).toContain("{#if sourceCount > 0}");
  });

  it("counts only sources the caller can see, and says nothing when there are none", () => {
    // sourceCount is resolved by the shell against the caller's own catalog;
    // meetingIds.length would disclose a meeting they may not read.
    expect(insightCardSource).toContain("export let sourceCount = 0;");
    expect(insightCardSource).not.toMatch(/\{insight\.meetingIds/);
  });

  it("takes its accent from the app's own colour", () => {
    // An insight is the app's headline object, so it wears the app's colour —
    // in the embedded build, the instance's own accent. It was amber to stay
    // distinct from primary under any theming, which made the one thing
    // Cassini produces the one thing that looked borrowed.
    expect(insightCardSource).toContain("var(--color-primary)");
    expect(insightCardSource).not.toContain("var(--color-secondary)");
  });
});

describe("InsightCard surface", () => {
  it("is filled, not outlined", () => {
    // On the list's own ground a base-100 fill is the same colour as everything
    // around it, leaving the left rule to do all the work of saying "different
    // kind of thing". Mixed INTO base-100 rather than into transparent so it
    // stays opaque over the row separators and stable in both themes.
    expect(insightCardSource).toContain(
      "background-color: color-mix(in oklch, var(--color-primary) 10%, var(--color-base-100));",
    );
    expect(insightCardSource).not.toContain("background-color: var(--color-base-100);");
  });

  it("keeps the open card distinguishable from a hovered one", () => {
    // Three states on one surface, so the wash has to step rather than repeat.
    expect(insightCardSource).toContain(
      "background-color: color-mix(in oklch, var(--color-primary) 18%, var(--color-base-100));",
    );
    expect(insightCardSource).toContain(
      "background-color: color-mix(in oklch, var(--color-primary) 26%, var(--color-base-100));",
    );
  });

  it("lets a failed run be retried on the card, without a button inside a button", () => {
    // The card is a container holding the open control and, for a failed run
    // where the provider can retry, a Retry control beside it (D-749). They
    // are siblings: the open button is closed before Retry begins.
    expect(insightCardSource).toMatch(/<div class="insight-card"/);
    expect(insightCardSource).not.toMatch(/<button[^>]*class="insight-card"/);
    const open = insightCardSource.indexOf('class="insight-open"');
    const openEnd = insightCardSource.indexOf("</button>", open);
    const retry = insightCardSource.indexOf("{#if failed && canRetry}");
    const cardEnd = insightCardSource.indexOf("</div>", retry);
    expect(retry).toBeGreaterThan(openEnd);
    expect(cardEnd).toBeGreaterThan(retry);
    expect(insightCardSource.slice(retry, cardEnd)).toContain('class="insight-retry"');
    expect(insightCardSource).toContain('dispatch("retry")');
    expect(insightCardSource).toContain("export let canRetry = false;");
  });

  it("puts Retry on the header row, right-aligned, and the whole card stays the open target", () => {
    // Laid over the card rather than taking a flex slot from the open button.
    expect(insightCardSource).toContain(".insight-card {\n    position: relative;");
    expect(insightCardSource).toContain(".insight-retry {\n    position: absolute;");
    expect(insightCardSource).toContain('{retrying ? "Retrying…" : "Retry"}');
    expect(insightCardSource).toContain('<p class="insight-retry-error" role="status">{retryError}</p>');
  });
});
