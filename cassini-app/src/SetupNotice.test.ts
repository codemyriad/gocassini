import { describe, expect, it } from "vitest";

import noticeSource from "./SetupNotice.svelte?raw";

// Source-level assertions, the convention this repo follows for .svelte files:
// the suite runs in node with no DOM harness. What the notice SAYS is decided in
// operator/setupHealth.ts and tested there; what is asserted here is the shape
// the rewrite gave it (D-759), and the two things a refactor could quietly lose:
// the tone, and the fact that everything technical is behind a disclosure.

describe("the setup notice", () => {
  it("takes its tone rather than assuming the loud one", () => {
    expect(noticeSource).toContain('export let tone: SetupNoticeTone = "warning";');
    // Both layouts branch on it. The advisory strip used to be an
    // `alert alert-warning` whatever it was saying, which is how a notice about
    // a check that has not run yet came to look like a fault.
    expect(noticeSource).toContain("{tone === 'warning' ? 'alert-warning' : ''}");
    const triangles = noticeSource.match(/<TriangleAlert/g) ?? [];
    const infos = noticeSource.match(/<Info/g) ?? [];
    expect(triangles).toHaveLength(2);
    expect(infos).toHaveLength(2);
  });

  it("puts the technical account behind a collapsed disclosure", () => {
    const disclosures = noticeSource.match(/<summary class="cursor-pointer font-medium">Details for administrators<\/summary>/g) ?? [];
    expect(disclosures).toHaveLength(2);
    // Bound, not `open`: the disclosure starts closed, and "Show details" and
    // the triangle on the <details> itself cannot disagree about the state.
    expect(noticeSource).toContain("bind:open={detailsOpen}");
    expect(noticeSource).toContain("let detailsOpen = false;");
    expect(noticeSource).not.toContain("<details open");
  });

  it("offers the two actions from the mock, as buttons", () => {
    const showDetails = noticeSource.match(/\n\s*Show details\n\s*<\/button>/g) ?? [];
    expect(showDetails).toHaveLength(2);
    expect(noticeSource).toContain('{busy ? "Checking…" : "Try again"}');
    // Try again is a request to the operator, which the shell owns; the
    // component asks for it and does nothing itself.
    expect(noticeSource).toContain('dispatch("retry")');
    expect(noticeSource).toContain("disabled={busy}");
  });

  it("sends the step that is a navigation to the settings section", () => {
    // The Setup tab is gone (D-756) and the storage controls are a section of
    // Operator › Settings (D-757).
    expect(noticeSource).toContain("Open Operator › Settings");
    expect(noticeSource).not.toContain("Open the Setup tab");
    expect(noticeSource).toContain('dispatch("navigate", "settings")');
  });

  it("keeps the strip and the card, and shows the cause under the consequence", () => {
    expect(noticeSource).toContain("{#if notice.blocking}");
    expect(noticeSource).toContain('class="alert items-start gap-3 py-2');
    expect(noticeSource).toContain("{#if notice.cause}");
    expect(noticeSource.indexOf("{notice.summary}")).toBeLessThan(noticeSource.indexOf("{notice.cause}"));
  });

  // Everyone who is not an administrator gets a sentence and the link to hand
  // on. No buttons: Try again would re-run a check whose answer they are not
  // shown, and there is nothing behind "Show details" for them.
  it("draws no buttons when there is no diagnosis to show", () => {
    expect(noticeSource).toContain("{#if hasDetails}");
    expect(noticeSource).toContain(
      "notice.detail || notice.note || notice.reference || notice.steps.length > 0",
    );
    expect(noticeSource).toContain("{#if notice.shareUrl}");
  });
});
