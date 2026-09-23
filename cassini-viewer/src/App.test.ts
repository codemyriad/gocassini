import { describe, expect, it } from "vitest";

import appSource from "./App.svelte?raw";

describe("the shell's answer to meeting-detail tag writes", () => {
  it("reconciles the list session and refreshes the vocabulary", () => {
    const handler = appSource.slice(
      appSource.indexOf("function reconcileMeetingTags"),
      appSource.indexOf("const bulkTags"),
    );
    expect(handler).toContain(
      "listTagSession.updateConfirmed((vocabulary) => withMeetingResult(vocabulary, result));",
    );
    expect(handler).toContain("refreshTags(true);");
    expect(appSource).toContain(
      "on:tagsChanged={(event) => reconcileMeetingTags(event.detail)}",
    );
    expect(appSource).not.toContain("applied(event.detail)");
  });
});

// Source-level assertions, the convention this repo follows for .svelte files:
// the suite runs in node with no DOM harness. What is asserted is the shell's
// wiring — which surfaces can ask for a retry, and what happens to the list
// when one is answered — because a retry that only worked from one of the two
// places a failed run is seen was the bug (D-749).

describe("the shell's insight retry", () => {
  it("is offered exactly where the provider can perform one", () => {
    expect(appSource).toContain('$: canRetryInsight = typeof dataProvider.retryInsight === "function";');
    expect(appSource).toContain("insightsRetryable={canRetryInsight}");
    expect(appSource).toContain("canRetry={canRetryInsight}");
  });

  it("is reachable from the browse card and from the document sheet", () => {
    expect(appSource).toContain("on:retryInsight={(event) => void retryInsightRun(event.detail)}");
    expect(appSource).toContain("on:retry={retrySelectedInsight}");
  });

  it("puts the answered record in the list and then re-reads the list", () => {
    // The card that said "failed" says "queued" the moment the operator
    // answers, and the shared tick keeps it moving from there.
    expect(appSource).toContain(
      "insights = insights.map((row) => (row.id === updated.id ? updated : row));",
    );
    const handler = appSource.slice(
      appSource.indexOf("async function retryInsightRun"),
      appSource.indexOf("function retrySelectedInsight"),
    );
    expect(handler).toContain("void refreshInsights();");
  });

  it("runs one retry at a time", () => {
    expect(appSource).toContain('if (!provider.retryInsight || retryingInsightId !== "") {');
  });
});

describe("the shell's answer to a created insight", () => {
  it("puts the record at the top of the list, closes Prepare, and re-reads", () => {
    // The panel's subject was the set the question was asked of; once asked,
    // the answer belongs where every insight is shown (D-749).
    const handler = appSource.slice(
      appSource.indexOf("function handleInsightCreated"),
      appSource.indexOf("// ensureInsightDocument fetches"),
    );
    expect(handler).toContain(
      "insights = [record, ...insights.filter((row) => row.id !== record.id)];",
    );
    expect(handler).toContain("prepareOpen = false;");
    expect(handler).toContain("void refreshInsights();");
  });

  it("hands the callback down the generate slot for the shell's card to call", () => {
    expect(appSource).toContain(
      '<slot name="prepare-generate" {entries} onInsightCreated={handleInsightCreated} />',
    );
  });
});

describe("the shell's Prepare panel", () => {
  it("announces each opening, so the shell around it can re-read what fills the panel", () => {
    expect(appSource).toContain(
      'const dispatch = createEventDispatcher<{ prepareOpen: void; overlay: boolean }>();',
    );
    expect(appSource).toContain('$: if (prepareOpen) {\n    dispatch("prepareOpen");\n  }');
    // And whether anything is open at all, so the shell can cover the tabs it
    // draws above this component with the same scrim.
    expect(appSource).toContain('dispatch("overlay", overlayOpen);');
  });

  // D-771: a search result is not a reason to override the reader's own
  // narrowing, and a narrowing change under a live query must re-ask.
  it("resolves transcript-only hits against the narrowed set, not the whole catalog", () => {
    // roomMeetings is room AND tag narrowed; catalogMeetings is everything.
    // Resolving against the latter is what let a hit render outside the filter.
    expect(appSource).toContain("const known = new Map(roomMeetings.map((meeting) => [meeting.id, meeting]));");
    expect(appSource).not.toContain("const known = new Map(catalogMeetings.map");
  });

  it("sends the narrowing the server can apply, rather than filtering the answer", () => {
    expect(appSource).toContain("roomId: searchNarrowing.roomId");
    expect(appSource).toContain("tag: searchNarrowing.tag");
    // Only an `id:` room key names a room the endpoint knows; `name:` and
    // `no-room` are viewer-side groupings with nothing to send.
    expect(appSource).toContain('selectedRoomKey?.startsWith("id:")');
    // The endpoint takes one tag, so several picked tags stay client-side.
    expect(appSource).toContain("activeTagIds.length === 1 ? activeTagIds[0]");
  });

  it("re-runs the search when the narrowing changes under a live query", () => {
    expect(appSource).toContain("narrowingKey !== lastNarrowingKey");
    expect(appSource).toMatch(/narrowingKey !== lastNarrowingKey[\s\S]{0,240}runSearch\(searchQuery\)/);
  });
});
