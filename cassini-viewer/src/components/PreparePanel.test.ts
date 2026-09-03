import { describe, expect, it } from "vitest";

import preparePanelSource from "./PreparePanel.svelte?raw";

// Source-level assertions, for the reason MeetingView.transcript.test.ts gives:
// the suite runs in node with no DOM harness. What is asserted here is what a
// reader would call a bug — the panel assembling its own bundle, a clipboard
// failure passing silently, or the gap disclosure being computed twice.

describe("PreparePanel", () => {
  it("never assembles a bundle of its own", () => {
    // The single most important property of this panel: one implementation
    // produces these bytes, and it is the one the CLI uses. A browser-side
    // assembly that "looks the same" is the failure this design exists to
    // prevent, so the panel renders the response and nothing else.
    expect(preparePanelSource).toContain("await loadBundle()");
    expect(preparePanelSource).not.toContain("## Transcript");
    expect(preparePanelSource).not.toContain("No summary was generated");
    expect(preparePanelSource).not.toMatch(/join\(["'`]\\n---/);
  });

  it("copies and downloads the same bytes", () => {
    // Two outputs, one document: both go through ensureBundle, which asks once
    // and keeps the answer for the second press.
    const copyThenDownload = preparePanelSource.split("async function handleDownload");
    expect(copyThenDownload).toHaveLength(2);
    expect(copyThenDownload[0]).toContain("ensureBundle()");
    expect(copyThenDownload[1]).toContain("await ensureBundle()");
    expect(preparePanelSource).toContain("clipboard.writeText(text)");
    expect(preparePanelSource).toContain("new Blob([text]");
  });

  it("does not spend the click's activation waiting for the bundle", () => {
    // Safari grants the clipboard the click's user activation and takes it back
    // at the first await, so a Copy that fetched the bundle before reaching the
    // clipboard would be refused on the first press, every press being the
    // first. The promise goes to the ClipboardItem instead, inside the gesture.
    const handleCopy = preparePanelSource.slice(
      preparePanelSource.indexOf("async function handleCopy"),
      preparePanelSource.indexOf("async function handleDownload"),
    );
    expect(handleCopy).toContain("const pending = ensureBundle();");
    expect(handleCopy).toContain("new ClipboardItem(");
    // The fetch must not be awaited before the clipboard is reached for it.
    expect(handleCopy.indexOf("ClipboardItem")).toBeLessThan(handleCopy.indexOf("await pending"));
  });

  it("says so when the clipboard is unavailable or refuses", () => {
    // This runs inside a shadow root in Nextcloud, where the API may be absent
    // (insecure origin) or deny the write. A Copy that silently did nothing
    // would look like a bundle that came out empty, so each case names the way
    // through — and where the bytes are already assembled, that is a second
    // press rather than giving up on Copy.
    expect(preparePanelSource).toContain("Clipboard unavailable here — use Download.");
    expect(preparePanelSource).toContain(
      "Clipboard blocked here — press Copy again, or use Download.",
    );
  });

  it("marks the rows its gap sentence counts", () => {
    // "One of these predates the single-file format" is a count; the row is
    // where it becomes a meeting to unpick. Both use selectionModel's predicate
    // so the mark and the sentence cannot disagree about which meetings.
    expect(preparePanelSource).toContain("lacksPortableAudio(entry)");
    expect(preparePanelSource).toContain("Blocks Prepare");
    expect(preparePanelSource).not.toContain("entry.audioPath");
  });

  it("takes its gap disclosure already decided", () => {
    // The counts and their wording are one decision, made in selectionModel
    // where a test can reach it; a panel that re-derived them from the entries
    // would be a second place for "no summary" and "does not say" to drift
    // apart.
    expect(preparePanelSource).toContain("export let gaps");
    expect(preparePanelSource).not.toContain("hasSummary");
  });

  it("leaves a slot for the Generate card that lands here later", () => {
    expect(preparePanelSource).toContain('<slot name="generate" />');
  });

  it("is not fixed to the viewport", () => {
    expect(preparePanelSource).not.toContain("position: fixed");
  });

  it("takes the readiness of the deployment as a slot, and decides none of it", () => {
    // Whether this deployment has anything to ask a question of is a fact about
    // an operator, and the viewing layer has none — a standalone export has no
    // operator at all. So the panel holds a place for the answer and never
    // reaches for one: an empty slot is silence, which is the honest reading of
    // a question nobody could ask, and a viewer that guessed would be a second
    // answer free to disagree with /setup.
    expect(preparePanelSource).toContain('<slot name="readiness" />');
    expect(preparePanelSource).not.toContain("features");
    expect(preparePanelSource).not.toContain("fetch(");
    expect(preparePanelSource).not.toContain("administrator");
  });
});
