import { describe, expect, it } from "vitest";
import { get } from "svelte/store";
import { LL } from "cassini-viewer/i18n";

import settingsPanelSource from "./SettingsPanel.svelte?raw";

const t = get(LL).app.settings.pipeline;

describe("SettingsPanel device policy", () => {
  it("offers auto, CPU and CUDA as device overrides, and no model override", () => {
    expect(settingsPanelSource).toContain('<option value="">{t.hw_device_auto()}</option>');
    expect(t.hw_device_auto()).toBe("Auto");
    expect(settingsPanelSource).toContain('<option value="cpu">CPU</option>');
    expect(settingsPanelSource).toContain('<option value="cuda">GPU (CUDA)</option>');
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
    // The "In use" line names the saved model and the effective device.
    expect(settingsPanelSource).toContain('savedDevice = settings?.effective?.device');
    expect(settingsPanelSource).toContain('<strong>{inUseModel?.name}</strong>');
    expect(settingsPanelSource).toContain("t.in_use_transcribing({ model: slot(\"model\"), device: runsOnLine })");
    expect(t.in_use_transcribing({ model: "M", device: "the CPU" })).toBe("Transcribing with M on the CPU.");
    expect(settingsPanelSource).toContain("settings.effective.note");
  });

  it("warns that recordings get no transcript only when that is what happens", () => {
    // While Ultra downloads, v3 transcribes (stand_in) or recordings wait for
    // the download; the warning is for a model nothing transcribes for.
    expect(settingsPanelSource).toContain("inUseModelState(inUseModel, inUseDownload)");
    expect(settingsPanelSource).toContain('class:in-use-warn={inUseState === "missing"}');
    expect(settingsPanelSource).toContain('{#if inUseModel && inUseState === "missing"}');
    expect(settingsPanelSource).toContain("t.in_use_stand_in_missing({ model: slot(\"model\"), device: runsOnLine })");
    expect(t.in_use_stand_in_missing({ model: "M", device: "D" })).toContain("the previous model transcribes recordings on D meanwhile.");
    // Installed files that failed their check: v3 transcribes, not because
    // Ultra is downloading or missing.
    expect(settingsPanelSource).toContain("t.in_use_stand_in_failed_check(");
    expect(t.in_use_stand_in_failed_check({ model: "M", device: "D" })).toContain("is installed but failed its runtime check; the previous model");
    expect(settingsPanelSource).toContain("t.in_use_downloading({ model: slot(\"model\") })");
    expect(t.in_use_downloading({ model: "M" })).toContain("; recordings wait for it.");
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
    // this fixed transcripts would use it for the wrong job.
    expect(settingsPanelSource).toContain("{t.search_note_transcripts()}");
    expect(t.search_note_transcripts()).toBe("Doesn't change any transcript, only what search finds.");
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
    expect(settingsPanelSource).toContain("{t.field_template()}");
    expect(t.field_template()).toBe("Insight template");
    expect(settingsPanelSource).toContain("bind:value={summary.template}");
    expect(settingsPanelSource).not.toContain('placeholder="summarise (the one Cassini ships)"');
  });

  it("keeps a template this build has never heard of selectable", () => {
    // Saved by an older or newer image, it is still what the step runs. A
    // picker that silently reset it to the default would change behaviour by
    // rendering.
    expect(settingsPanelSource).toContain("t.template_not_in_build({ id })");
    expect(t.template_not_in_build({ id: "x" })).toContain("not in this build");
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
    expect(settingsPanelSource).toContain("{t.needs_provider()}");
    expect(t.needs_provider()).toBe("Needs a provider.");
    expect(settingsPanelSource).toContain('on:click={() => dispatch("openProviders")}>{t.add_provider()}</button>');
    expect(t.add_provider()).toBe("Add one");
  });
});

describe("SettingsPanel layout", () => {
  it("keeps the device override with the hardware it corrects, inside the transcript question", () => {
    // It is the answer to "what the operator found is wrong", so it opens
    // under the finding rather than at the end of the page or in a card of its
    // own, which would read as a pipeline step.
    const transcriptAt = settingsPanelSource.indexOf("aria-label={t.transcription_label()}");
    const hardwareAt = settingsPanelSource.indexOf('id="hardware-details"');
    const deviceAt = settingsPanelSource.indexOf(">{t.hw_device()}<");
    const summaryAt = settingsPanelSource.indexOf('aria-labelledby="summary-heading"');
    expect([transcriptAt, hardwareAt, deviceAt, summaryAt].every((at) => at > -1)).toBe(true);
    expect([transcriptAt, hardwareAt, deviceAt, summaryAt]).toEqual(
      [...[transcriptAt, hardwareAt, deviceAt, summaryAt]].sort((a, b) => a - b));
    expect(settingsPanelSource).not.toContain(">Hardware<");
  });

  it("asks the four questions in one card, in the order of the strip", () => {
    // Transcript, then the two steps that need one, then what is kept. The
    // chips above follow the same order, so chip N points at question N.
    const cardAt = settingsPanelSource.indexOf('aria-labelledby="recording-publication-heading"');
    const headingAt = settingsPanelSource.indexOf(">{t.after_meeting()}<");
    const at = [">{t.transcript_heading()}<", ">{t.summary_heading()}<", ">{t.publication_heading()}<", ">{t.keep_heading()}<"]
      .map((title) => settingsPanelSource.indexOf(title));
    expect([cardAt, headingAt, ...at].every((index) => index > -1)).toBe(true);
    expect([cardAt, headingAt, ...at]).toEqual([...[cardAt, headingAt, ...at]].sort((a, b) => a - b));
    expect(settingsPanelSource).not.toContain("Summarisation\n");
  });

  it("asks the summary switch, provider and template inside question 2", () => {
    // One place for the whole step, under its switch; no separate card.
    const switchAt = settingsPanelSource.indexOf("aria-label={t.summarise_switch()}");
    const providerAt = settingsPanelSource.indexOf('<span class="op-field-label">{t.field_provider()}</span>');
    const templateAt = settingsPanelSource.indexOf("{t.field_template()}");
    const publicationAt = settingsPanelSource.indexOf(">{t.publication_heading()}<");
    expect([switchAt, providerAt, templateAt, publicationAt].every((at) => at > -1)).toBe(true);
    expect([switchAt, providerAt, templateAt, publicationAt]).toEqual(
      [...[switchAt, providerAt, templateAt, publicationAt]].sort((a, b) => a - b));
    expect(settingsPanelSource).not.toContain(">Summary settings<");
  });

  it("asks the tiers, each with its model, inside the transcript question", () => {
    const transcriptAt = settingsPanelSource.indexOf("aria-label={t.transcription_label()}");
    const listAt = settingsPanelSource.indexOf("aria-label={t.tiers_label()}");
    const modelsAt = settingsPanelSource.indexOf("<SpeechModelCell");
    const summaryAt = settingsPanelSource.indexOf('aria-labelledby="summary-heading"');
    expect([transcriptAt, listAt, modelsAt, summaryAt].every((at) => at > -1)).toBe(true);
    expect([transcriptAt, listAt, modelsAt, summaryAt]).toEqual(
      [...[transcriptAt, listAt, modelsAt, summaryAt]].sort((a, b) => a - b));
  });

  it("keeps search spellings in their own card without transcription hints", () => {
    expect(settingsPanelSource).toContain('aria-labelledby="search-spellings-heading"');
    expect(settingsPanelSource).toContain(">{t.search_heading()}<");
    expect(t.search_heading()).toBe("Search spellings");
    expect(settingsPanelSource).not.toContain(">Transcript hints<");
    expect(settingsPanelSource).not.toContain(">Vocabulary<");
    expect(settingsPanelSource).not.toContain("transcription_terms:");
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
      "return runnableWorkflows(list).filter((workflow) => !workflowTakesQuestion(workflow));",
    );
    expect(settingsPanelSource).toContain("{#each summaryTemplates as workflow (workflow.id)}");
  });

  it("offers the template choice now the pipeline reads it back", () => {
    // D-819: the summary step runs the chosen template, so the choice is shown.
    expect(settingsPanelSource).not.toContain("SUMMARY_TEMPLATE_CHOICE");
    expect(settingsPanelSource).not.toContain("summaries still use the built-in template for now");
    expect(settingsPanelSource).toContain("{t.template_help()}");
    expect(t.template_help()).toBe("Meetings published from now on are summarised with this template.");
    expect(settingsPanelSource).toContain("{t.edit_templates()}");
  });

  it("names the provider's default model as what an empty summary model means", () => {
    // "endpoint default" named nothing; the provider's default model is what
    // actually runs when the step names none (D-749).
    expect(settingsPanelSource).toContain("placeholder={providerModelPlaceholder(summary.provider)}");
    expect(settingsPanelSource).toContain("t.provider_default_model({ model })");
    expect(t.provider_default_model({ model: "m" })).toContain("(the provider's default)");
    expect(settingsPanelSource).toContain("t.no_provider_default()");
    expect(t.no_provider_default()).toBe("no default model set on this provider");
  });
});

// D-763's readiness checks, rehomed. They lived under a Setup tab that D-751
// removed; this panel is where they now belong, and the order is load-bearing.
describe("SettingsPanel recording readiness", () => {
  it("keeps the anchor the checks' Set up storage action scrolls to", () => {
    // RecordingSetup scrolls to #recording-storage for its setup_storage
    // action. The removed Setup page used to provide that id; without it the
    // action silently does nothing.
    expect(settingsPanelSource).toContain('<div id="recording-storage">');
  });
});

// The Transcript step as one list, and the steps after it as inset panels.
// The old layout put a switch that could not be flipped above a hint that
// pointed five blocks down to the Download button; the tier is the model now.
describe("SettingsPanel transcript step", () => {
  it("answers what transcribes recordings with one list: Off, or a tier", () => {
    expect(settingsPanelSource).toContain("aria-label={t.tiers_label()}");
    expect(settingsPanelSource).toContain('<span class="q-label">{t.off()}</span>');
    expect(t.off()).toBe("Off");
    expect(settingsPanelSource).toContain("{t.off_description()}");
    expect(t.off_description()).toBe("Recordings are published without a transcript.");
    expect(settingsPanelSource).not.toContain('aria-label="Transcribe every recording"');
    expect(settingsPanelSource).not.toContain("Download the model below to turn it on.");
    expect(settingsPanelSource).not.toContain("Transcription needs to be on to edit the settings below.");
  });

  it("puts each tier's model, state and download on the tier's own row", () => {
    // A tier whose model is not ready cannot be picked, so its words grey;
    // the Download (or Check) button is the one live control on the row.
    expect(settingsPanelSource).toContain("class:unavailable={!row.selectable}");
    expect(settingsPanelSource).toContain("disabled={!row.selectable}");
    expect(settingsPanelSource).toContain("selectable: model?.ready === true");
    // Picking a tier sets the tier, the model and the switch at once.
    expect(settingsPanelSource).toContain("quality = row.option.value;");
    expect(settingsPanelSource).toContain("function useModel(model: SpeechModel)");
  });

  it("turns transcription on when a download started here is ready", () => {
    // Downloading a model is the clearest statement of intent there is; the
    // pick still has to be saved.
    expect(settingsPanelSource).toContain("on:queued={(event) => (pendingSelect = event.detail.id)}");
    expect(settingsPanelSource).toContain("function pickWhenReady(");
    expect(settingsPanelSource).toContain("t.lead_downloading({ tier: pendingRow.option.label })");
    expect(t.lead_downloading({ tier: "Fast" })).toContain("Transcription turns on when it finishes.");
    expect(settingsPanelSource).toContain("t.lead_off()");
    expect(t.lead_off()).toContain("Install and pick a model to turn transcription on.");
  });

  it("shows the GPU's one model once under the tiers", () => {
    expect(settingsPanelSource).toContain("tiersShareModel = new Set(tierModelIds).size === 1");
    expect(settingsPanelSource).toContain("{#if tiersShareModel}");
  });

  it("numbers the strip chips after the steps they jump to", () => {
    expect(settingsPanelSource).toContain('<span class="chip-num" aria-hidden="true">{index + 1}</span>');
    expect(settingsPanelSource).toContain("{ name: t.chip_retention(), step: status.keep");
    expect(t.chip_retention()).toBe("Media retention");
  });

  it("greys the summary step while there is no transcript and says why in a pill", () => {
    expect(settingsPanelSource).toContain('<span class="q-pill q-pill-warn">{t.needs_transcript()}</span>');
    expect(t.needs_transcript()).toBe("needs a transcript");
    // The switch is out of the way rather than shown on inside a greyed step.
    const pillAt = settingsPanelSource.indexOf('<span class="q-pill q-pill-warn">{t.needs_transcript()}</span>');
    const switchAt = settingsPanelSource.indexOf("aria-label={t.summarise_switch()}");
    expect(settingsPanelSource.slice(pillAt, switchAt)).toContain("{:else}");
    expect(settingsPanelSource).toContain("summaryStepLead(providerName(summaryProviderRow.id), summaryProviderRow.base_url)");
  });

  it("keeps steps 3 and 4 editable while transcription is off", () => {
    // Only a transcript-only meeting and keeping nothing need a transcript,
    // and each says so in its place; the rest of both steps is a choice
    // either way.
    expect(settingsPanelSource).toContain("<PublishedFormatSelect value={publishedFormat} transcript={transcriptionEnabled} disabled={saving}");
    expect(settingsPanelSource).toContain('disabled={saving} tabindex="-1" bind:this={keepSettings}');
    expect(settingsPanelSource).toContain("{t.keep_nothing_unavailable()}");
    expect(t.keep_nothing_unavailable()).toBe("Unavailable while audio is published (step 3).");
    expect(settingsPanelSource).not.toContain("Transcription is off. Turn it on above to include a transcript.");
  });
});
