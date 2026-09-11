import { describe, expect, it } from "vitest";

import llmSettingsPanelSource from "./LLMSettingsPanel.svelte?raw";

describe("AI providers key handling", () => {
  it("takes keys through a password input and never renders a stored key", () => {
    expect(llmSettingsPanelSource).toContain('type="password"');
    // The server only ever reports api_key_configured; the panel must not
    // expect or bind a raw key field.
    expect(llmSettingsPanelSource).toContain("keyConfigured");
    expect(llmSettingsPanelSource).not.toContain("api_key_value");
  });

  it("distinguishes keeping, replacing and clearing a stored key", () => {
    // Three outcomes from one empty-looking input, and the PUT means something
    // different for each: omitted keeps, "" clears, anything else replaces.
    expect(llmSettingsPanelSource).toContain("keyCleared");
    expect(llmSettingsPanelSource).toContain('edited.api_key = "";');
    expect(llmSettingsPanelSource).toContain("edited.api_key = current.key;");
  });

  it("does not require a key, so a self-hosted endpoint can be saved", () => {
    // llama.cpp, vLLM and Ollama need none, and demanding one would lock this
    // panel against exactly the endpoints the privacy story is built around.
    expect(llmSettingsPanelSource).toContain('$: draftReady = (draft?.baseUrl ?? "").trim() !== "";');
  });
});

describe("AI providers scope", () => {
  it("configures endpoints only — the pipeline steps live with the pipeline", () => {
    // Summarisation moved to Publish pipeline, where the step it switches on
    // actually runs; the insight step is gone from the UI entirely.
    expect(llmSettingsPanelSource).not.toContain("Meeting summary");
    expect(llmSettingsPanelSource).not.toContain("bind:checked={summary.enabled}");
    expect(llmSettingsPanelSource).not.toContain("bind:checked={insight.enabled}");
    expect(llmSettingsPanelSource).not.toContain("bind:value={insight.provider}");
  });

  it("round-trips the steps it no longer edits rather than clearing them", () => {
    // PUT leaves an omitted step alone, so saving a provider must not send a
    // summary or insight body at all — sending an empty one would switch off a
    // step this panel does not even show.
    expect(llmSettingsPanelSource).toContain(
      "putLLMSettings({ providers: providerUpdates(current) })",
    );
  });

  it("drops a removed endpoint from both steps", () => {
    // The server rejects an ENABLED step pointing at a provider that is gone,
    // so a delete that did not cascade would 400 the whole save.
    expect(llmSettingsPanelSource).toContain("summary: detachedStep(current.summary, provider.id)");
    expect(llmSettingsPanelSource).toContain("insight: detachedStep(current.insight, provider.id)");
    expect(llmSettingsPanelSource).toContain('return { ...step, enabled: false, provider: "" };');
  });

  it("asks before removing an endpoint, naming what is lost", () => {
    // One click used to destroy the stored key and silently switch off the
    // step that ran on the endpoint. Remove now opens an inline confirmation
    // (a top-layer <dialog> is unreliable inside the shadow root) that names
    // the endpoint, says the key is destroyed and that summarising stops.
    expect(llmSettingsPanelSource).toContain("on:click={() => (pendingRemoval = provider)}");
    expect(llmSettingsPanelSource).toContain('role="alertdialog"');
    expect(llmSettingsPanelSource).toContain("Remove {provider.name || provider.base_url || provider.id}?");
    expect(llmSettingsPanelSource).toContain("The stored key is destroyed");
    expect(llmSettingsPanelSource).toContain('names.push("meeting summaries");');
    // The write happens only from the confirmation's own button.
    expect(llmSettingsPanelSource.match(/void removeProvider\(provider\)/g)).toHaveLength(1);
    expect(llmSettingsPanelSource).toContain("Yes, remove it");
  });

  it("sends every untouched provider back, so a save cannot drop one", () => {
    // PUT replaces the whole list. A body carrying only the edited row would
    // delete every other endpoint on the deployment.
    expect(llmSettingsPanelSource).toContain("const rows = (settings?.providers ?? []).map(");
  });
});

describe("AI providers verification", () => {
  it("earns its tick by listing models rather than by a row existing", () => {
    expect(llmSettingsPanelSource).toContain("listProviderModels(providerId)");
    expect(llmSettingsPanelSource).toContain('probes[provider.id]?.status === "ok"');
  });

  it("reports a failed listing as itself, not as a broken endpoint", () => {
    // An endpoint with no /models route still answers completions perfectly
    // well, so this must not read as "this provider does not work".
    expect(llmSettingsPanelSource).toContain("Its model list could not be read");
    expect(llmSettingsPanelSource).toContain("Summaries and insights may still work");
  });
});
