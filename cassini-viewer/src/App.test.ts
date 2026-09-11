import { describe, expect, it } from "vitest";

import appSource from "./App.svelte?raw";

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
