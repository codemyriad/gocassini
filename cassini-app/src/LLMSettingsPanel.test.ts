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

  it("warns about an OpenRouter key pasted without its prefix, and still saves it", () => {
    // A key saved without "sk-or-v1-" fails every run with a 401. The warning
    // is for OpenRouter's host only, for a typed key only, and it blocks
    // nothing: draftReady does not read it.
    expect(llmSettingsPanelSource).toContain(
      'draft && hostOf(draft.baseUrl) === "openrouter.ai" && looksUnprefixed(draft.key)',
    );
    expect(llmSettingsPanelSource).toContain('return typed !== "" && !typed.startsWith("sk-or-");');
    expect(llmSettingsPanelSource).toContain(
      "OpenRouter keys start with sk-or-v1-. Check you pasted the whole key.",
    );
    expect(llmSettingsPanelSource).not.toContain("draftReady = draftReady && !keyPrefixWarning");
    // The placeholder asks for the whole key rather than showing the prefix
    // as if it were already there.
    expect(llmSettingsPanelSource).toContain(
      "Paste the full API key (sk-or-v1-… for OpenRouter). Self-hosted servers usually don't need one.",
    );
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
    expect(llmSettingsPanelSource).toContain("The saved API key is deleted");
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
    expect(llmSettingsPanelSource).toContain("Model list unavailable");
    expect(llmSettingsPanelSource).toContain("can still type a model ID");
  });
});

describe("AI providers edit placement (D-749)", () => {
  it("edits a provider on its own card, not in a form under the list", () => {
    // Opening the form under the whole list read as adding a second endpoint
    // rather than changing the one just clicked. The card being edited swaps
    // its read-only body for the form; the other cards stay as they are.
    expect(llmSettingsPanelSource).toContain("{#snippet providerForm(heading: string)}");
    expect(llmSettingsPanelSource).toContain("{#if draft?.existing && draft.id === provider.id}");
    expect(llmSettingsPanelSource).toContain(
      "{@render providerForm(`Editing ${providerLabel(provider)}`)}",
    );
  });

  it("keeps only a new provider's form under the list, and says so in a heading", () => {
    expect(llmSettingsPanelSource).toContain("{#if draft && !draft.existing}");
    expect(llmSettingsPanelSource).toContain('{@render providerForm("New provider")}');
    expect(llmSettingsPanelSource).toContain('<h3 class="set-row-name">{heading}</h3>');
    // The single form is the whole point: one markup, two placements.
    expect(llmSettingsPanelSource.split("<ModelCombobox").length - 1).toBe(1);
  });

  it("still resets the inputs when the draft it shows is swapped", () => {
    expect(llmSettingsPanelSource).toContain("{#key draft.id}");
  });
});

describe("AI providers card layout (D-749)", () => {
  it("keeps Edit and Remove beside the facts whatever the model-list error says", () => {
    // The header row is name and facts on the left, actions on the right, and
    // it does not wrap: a long "Model list unavailable" message used to push
    // the buttons onto their own line under the text. The message now has a
    // full-width line of its own beneath the row.
    const header = '<div class="flex items-start justify-between gap-2">';
    expect(llmSettingsPanelSource).toContain(header);
    expect(llmSettingsPanelSource).not.toContain("flex flex-wrap items-start justify-between");
    expect(llmSettingsPanelSource).toContain('class="flex flex-none items-center gap-3"');
    const row = llmSettingsPanelSource.indexOf(header);
    const remove = llmSettingsPanelSource.indexOf("Remove\n                    </button>", row);
    const warning = llmSettingsPanelSource.indexOf("{modelListUnavailable(state.status", row);
    expect(row).toBeGreaterThan(-1);
    expect(remove).toBeGreaterThan(row);
    expect(warning).toBeGreaterThan(remove);
  });
});

describe("AI providers default model (D-749)", () => {
  it("edits one default model per endpoint, on the provider draft", () => {
    // Summaries and insights ask for this model unless a step names its own,
    // and whoever creates an insight gets it with the endpoint they pick.
    expect(llmSettingsPanelSource).toContain("<ModelCombobox");
    expect(llmSettingsPanelSource).toContain("bind:value={draft.model}");
    expect(llmSettingsPanelSource).toContain('label="Default model"');
    expect(llmSettingsPanelSource).toContain("model: provider.model,");
    expect(llmSettingsPanelSource).toContain("model: current.model.trim(),");
  });

  it("carries every other endpoint's model through a save and a remove", () => {
    // PUT replaces the whole list, so a row sent back without its model is a
    // model silently cleared.
    // providerUpdates (save) and removeProvider both rebuild the list.
    expect(llmSettingsPanelSource).toContain("      model: provider.model,\n      // api_key omitted");
    expect(llmSettingsPanelSource).toContain("model: row.model,");
  });

  it("says when an endpoint has no default model", () => {
    // The recorder then falls back to its own default, which a local endpoint
    // has probably never heard of.
    expect(llmSettingsPanelSource).toContain("No default model");
  });

  it("offers a saved endpoint's own listing and says why an unsaved one has none", () => {
    expect(llmSettingsPanelSource).toContain("Save the provider first to list its models.");
    expect(llmSettingsPanelSource).toContain("models: LLMModel[]");
  });
});
