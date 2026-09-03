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

  it("takes its accent from a token the Nextcloud theme does not remap", () => {
    // primary is already the open row and the active-narrowing chips, and in
    // the embedded build it becomes the instance's own accent — an insight has
    // to stay visibly a different kind of thing under any theming.
    expect(insightCardSource).toContain("var(--color-secondary)");
    expect(insightCardSource).not.toContain("var(--color-primary)");
  });
});
