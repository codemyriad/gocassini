import { describe, expect, it } from "vitest";

import settingsPanelSource from "./SettingsPanel.svelte?raw";

describe("SettingsPanel device policy", () => {
  it("offers auto, CPU and CUDA as device overrides, and no model override", () => {
    expect(settingsPanelSource).toContain('<option value="">Auto</option>');
    expect(settingsPanelSource).toContain('<option value="cpu">CPU</option>');
    expect(settingsPanelSource).toContain('<option value="cuda">CUDA</option>');
    // The model override accepted only the three models the quality tiers
    // already reach, so it duplicated the tier selector with no discoverability
    // and was removed (D-702). The design prototype draws it back in beside the
    // device; it stays out, and the only "Model" on this page is the summary
    // step's, which names a chat model and is a different thing entirely.
    expect(settingsPanelSource).not.toContain("modelOverride");
    expect(settingsPanelSource).not.toContain("model_override");
  });

  it("shows the device and model the next build will actually use", () => {
    // The device is auto-selected, so the tier alone does not tell an admin
    // what will happen. Without this an install with no GPU looks configured
    // and only reveals the CPU path once a build has run (D-702).
    expect(settingsPanelSource).toContain("deviceLabel(settings.effective.device)");
    expect(settingsPanelSource).toContain("settings.effective.model");
    expect(settingsPanelSource).toContain("settings.effective.note");
  });
});

describe("SettingsPanel search spellings", () => {
  it("sends the aliases and tracks them as unsaved changes", () => {
    // Without the payload field the control would look like it worked and
    // silently change nothing; without the dirty check Save stays disabled
    // after editing it.
    expect(settingsPanelSource).toContain("search_aliases: parseSearchAliases(searchAliasesText)");
    expect(settingsPanelSource).toContain("searchAliasesText !== savedSearchAliasesText");
  });

  it("says what the setting does and does not do", () => {
    // It widens a search; it never rewrites a recording. An admin who thought
    // this fixed transcripts would use it for the wrong job — the vocabulary
    // above is that one.
    expect(settingsPanelSource).toContain("does not change any recording");
  });
});

describe("SettingsPanel summarisation step", () => {
  it("writes the two stores behind this page separately", () => {
    // Quality and the device override are STT settings; summarisation is LLM
    // policy. One Save, two PUTs, and neither is sent unless it changed — a
    // page that always wrote both would clobber a concurrent edit to the other.
    expect(settingsPanelSource).toContain("if (sttDirty)");
    expect(settingsPanelSource).toContain("if (summaryDirty)");
    expect(settingsPanelSource).toContain("putLLMSettings({ summary })");
  });

  it("names a template from the registry instead of asking for a workflow id", () => {
    // "Workflow" was a free-text field for an id the registry could have told
    // an administrator, and a typo saved cleanly and failed much later, at the
    // run (D-718/D-719).
    expect(settingsPanelSource).toContain("listInsightWorkflows()");
    expect(settingsPanelSource).toContain("Insight template");
    expect(settingsPanelSource).toContain("bind:value={summary.template}");
    expect(settingsPanelSource).not.toContain('placeholder="summarise (the one Cassini ships)"');
  });

  it("keeps a template this build has never heard of selectable", () => {
    // Saved by an older or newer image, it is still what the step runs. A
    // picker that silently reset it to the default would change behaviour by
    // rendering.
    expect(settingsPanelSource).toContain("not in this build");
  });

  it("chooses a provider when the step is switched on", () => {
    // The operator refuses an enabled step with no provider, so a bare toggle
    // would produce a body that 400s on Save.
    expect(settingsPanelSource).toContain("function toggleSummary(");
    expect(settingsPanelSource).toContain("first.id");
  });

  it("clears the model when the endpoint changes", () => {
    // A model id belongs to the server that serves it; carried across, it names
    // a model the new endpoint may never have heard of and fails a meeting
    // later.
    expect(settingsPanelSource).toContain('summary = { ...summary, provider: id, model: "" };');
  });

  it("loads models when the field is opened, not from a button", () => {
    expect(settingsPanelSource).toContain("on:open={() => void loadModels(summary.provider)}");
    expect(settingsPanelSource).not.toContain("Load models");
  });

  it("separates 'no provider' from 'the AI settings could not be read'", () => {
    // Three states. Rendering the locked card for an unreadable settings fetch
    // would accuse a configured deployment of being unconfigured — the same
    // rule the catalog's hasSummary and /setup's features both follow.
    expect(settingsPanelSource).toContain("hasProvider = llm === null ? null : providers.length > 0");
    expect(settingsPanelSource).toContain("{#if llmError}");
    expect(settingsPanelSource).toContain("{:else if hasProvider === false}");
  });

  it("offers the way out of the locked state", () => {
    expect(settingsPanelSource).toContain("NeedsProviderCard");
    expect(settingsPanelSource).toContain('dispatch("openProviders")');
  });
});

describe("SettingsPanel layout", () => {
  it("keeps the device override with the hardware it corrects", () => {
    // It is the answer to "what the operator found is wrong", so it sits under
    // the finding rather than at the end of the page — and under a rule rather
    // than in a card of its own, which would read as a pipeline step.
    const hardwareAt = settingsPanelSource.indexOf(">Detected hardware<");
    const deviceAt = settingsPanelSource.indexOf(">Device override<");
    const qualityAt = settingsPanelSource.indexOf(">Quality<");
    expect(hardwareAt).toBeGreaterThan(-1);
    expect(deviceAt).toBeGreaterThan(hardwareAt);
    expect(deviceAt).toBeLessThan(qualityAt);
  });

  it("gives each pipeline step an edge of its own", () => {
    // Quality and Summarisation were rows of one shared card separated by
    // hairlines, which made two independent settings — one of which sends text
    // to a third party — read as one block.
    const cards = settingsPanelSource.match(
      /<section class="rounded-box border border-base-300 bg-base-200 p-3">/g,
    );
    expect(cards?.length).toBe(3);
  });
});

describe("SettingsPanel template picker", () => {
  it("opens on the shipped summary template, not a synthetic default", () => {
    // "Default (the one Cassini ships)" named no template and disclosed no
    // question, and the template it stood for is one of the options below it.
    expect(settingsPanelSource).not.toContain("Default (the one Cassini ships)");
    expect(settingsPanelSource).toContain('const SHIPPED_SUMMARY_TEMPLATE = "summarise";');
    expect(settingsPanelSource).toContain("function resolveSummaryTemplate()");
    // Called from both fetches, not from whichever is written second: they are
    // started together, and resolving in only one of them was a race.
    expect(settingsPanelSource.match(/resolveSummaryTemplate\(\);/g)?.length).toBe(2);
  });

  it("resolving the stored empty template does not light up Save", () => {
    // "" and the id it resolves to mean the same thing to the recorder, so
    // opening the page must not look like an edit an administrator made.
    expect(settingsPanelSource).toContain("savedSummary = JSON.stringify(summary);");
  });

  it("does not offer a freeform template as the automatic summary", () => {
    // A workflow that takes a question cannot summarise every meeting: the
    // pipeline runs unattended, there is nobody to ask, and the recorder
    // refuses a question-taking workflow with no question. Decided on the
    // registry's bytes, the same way the Prepare panel decides whether to show
    // a question box.
    expect(settingsPanelSource).toContain(
      "return list.filter((workflow) => !workflowTakesQuestion(workflow));",
    );
    expect(settingsPanelSource).toContain("{#each summaryTemplates as workflow (workflow.id)}");
  });

  it("still says the pipeline does not read the template back", () => {
    // internal/transcribe/summary.go splices the shipped prompt directly. A
    // control that silently does nothing is worse than one that admits it.
    expect(settingsPanelSource).toContain(
      "The publish pipeline still runs the summary prompt",
    );
  });

  it("names the provider's default model as what an empty summary model means", () => {
    // "endpoint default" named nothing; the provider's default model is what
    // actually runs when the step names none (D-749).
    expect(settingsPanelSource).toContain("placeholder={providerModelPlaceholder(summary.provider)}");
    expect(settingsPanelSource).toContain("(the provider's default)");
    expect(settingsPanelSource).toContain("no default model set on this provider");
  });
});
