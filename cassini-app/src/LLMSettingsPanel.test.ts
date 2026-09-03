import { describe, expect, it } from "vitest";

import llmSettingsPanelSource from "./LLMSettingsPanel.svelte?raw";

describe("LLMSettingsPanel key handling", () => {
  it("takes keys through a password input and never renders a stored key", () => {
    expect(llmSettingsPanelSource).toContain('type="password"');
    // The server only ever reports api_key_configured; the panel must not
    // expect or bind a raw key field.
    expect(llmSettingsPanelSource).toContain("keyConfigured");
    expect(llmSettingsPanelSource).not.toContain("api_key_value");
  });

  it("offers fetched model lists while keeping the model field free text", () => {
    expect(llmSettingsPanelSource).toContain("Load models");
    expect(llmSettingsPanelSource).toContain("<datalist");
    expect(llmSettingsPanelSource).toContain('type="text"');
  });
});

describe("LLMSettingsPanel insight step", () => {
  it("gives the insight step its own endpoint, model and workflow", () => {
    expect(llmSettingsPanelSource).toContain("bind:checked={insight.enabled}");
    expect(llmSettingsPanelSource).toContain("bind:value={insight.provider}");
    expect(llmSettingsPanelSource).toContain("bind:value={insight.model}");
    expect(llmSettingsPanelSource).toContain("bind:value={insight.template}");
    expect(llmSettingsPanelSource).toContain("bind:value={summary.template}");
    // Its own model list, or the summary step's would be offered for the
    // wrong endpoint.
    expect(llmSettingsPanelSource).toContain('id="llm-models-insight"');
  });

  it("saves both steps in the one PUT and counts both towards dirty", () => {
    expect(llmSettingsPanelSource).toContain("insight,\n        }),");
    expect(llmSettingsPanelSource).toContain("JSON.stringify({ providers, summary, insight })");
  });

  it("distinguishes an inherited insight endpoint from an owned one", () => {
    // The two behave differently the moment the summary endpoint is repointed,
    // so the panel must not collapse them into one "currently:" line (D-719).
    expect(llmSettingsPanelSource).toContain("effective.inherited");
    expect(llmSettingsPanelSource).toContain("Inherits the meeting-summary endpoint");
    expect(llmSettingsPanelSource).toContain("No endpoint configured — an insight has nothing to ask.");
  });

  it("checks a workflow id against the registry the recorder actually ships", () => {
    // The operator validates only the SHAPE of a template id — it is a separate
    // Go module and cannot see the registry (D-719) — so a typo saves cleanly
    // and fails much later, at the run. This panel is where it can be caught,
    // now that GET /settings/workflows exists to be asked (D-718).
    expect(llmSettingsPanelSource).toContain("listInsightWorkflows()");
    expect(llmSettingsPanelSource).toContain('id="llm-workflows"');
    expect(llmSettingsPanelSource).toContain("{#if summaryWorkflowUnknown}");
    expect(llmSettingsPanelSource).toContain("{#if insightWorkflowUnknown}");
    // Derived reactively, not called from the markup: the registry arrives
    // after the settings do, and an expression naming only `summary` would
    // never re-run when it lands.
    expect(llmSettingsPanelSource).toContain("$: summaryWorkflowUnknown =");
    expect(llmSettingsPanelSource).toContain("$: insightWorkflowUnknown =");
    // Named, not just refused: the remedy is one of the shipped ids.
    expect(llmSettingsPanelSource).toContain("This build ships no workflow with that id");
  });

  it("keeps the workflow field usable when the registry cannot be read", () => {
    // Advisory, not a gate. A failed registry fetch must not stop an
    // administrator configuring an endpoint, and an id from a newer recorder
    // image must still be typeable — so the warning is suppressed rather than
    // the field disabled.
    expect(llmSettingsPanelSource).toContain("workflowsKnown");
    expect(llmSettingsPanelSource).not.toContain("disabled={!workflowsKnown}");
  });

  it("drops a removed endpoint from both steps", () => {
    // The server rejects an enabled step pointing at a provider that is gone,
    // so a delete that only cascaded to the summary would 400 the whole save.
    expect(llmSettingsPanelSource).toContain("if (insight.provider === id)");
  });
});
