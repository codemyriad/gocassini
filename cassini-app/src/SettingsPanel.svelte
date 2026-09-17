<script lang="ts">
  // Publish pipeline: every step between the call ending and the meeting being
  // published, in the order they run.
  //
  // It was "Transcription quality" and it is now the whole pipeline, because
  // summarising is one of those steps and was living under AI providers — a
  // page about endpoints, which is where its endpoint is configured and not
  // where an administrator goes to ask "what happens to a recording here?".
  //
  // Two stores behind one page. Quality and the device override are STT
  // settings (`<base>/settings`); summarisation is LLM policy
  // (`<base>/settings/llm`). Save writes whichever of the two is dirty, so the
  // page has one Save the way the design does, and a failure in one does not
  // silently roll back the other — each reports itself.
  import { createEventDispatcher, onDestroy, onMount, tick } from "svelte";
  import { fly } from "svelte/transition";
  import { cancelLeave, confirmLeave, guardLeave, leavePrompt, unsavedChanges } from "./operator/unsaved";
  import { ChevronDown, RefreshCw, TriangleAlert } from "@lucide/svelte";
  import { OperatorClient, OperatorHttpError } from "./operator/client";
  import ModelCombobox from "./ModelCombobox.svelte";
  // D-757: "Who can see recordings" is a section of the Settings panel, above
  // the pipeline it applies to. Its own component because it is a page's worth
  // of state — a switch, its prerequisites, its progress — and none of it is
  // shared with the settings below.
  import RecordingAccessPanel from "./RecordingAccessPanel.svelte";
  // D-763: whether recording WORKS, above who may see what it produces.
  //
  // That order is the point rather than a layout preference. Installing the
  // ExApp does not establish that Talk recording will work — HPB, the signaling
  // secret, storage, processing or the incoming handoff can each be silently
  // incomplete — and an administrator who sets the audience for recordings that
  // never happen has answered the second question while the first is still
  // broken.
  //
  // This lived under a Setup tab that D-751 removed. It belongs here for the
  // same reason "Who can see recordings" does: it is a page's worth of state
  // that applies to the pipeline below it, and it is admin-only, which this
  // panel already is.
  import RecordingSetup from "./RecordingSetup.svelte";
  import NeedsProviderCard from "./NeedsProviderCard.svelte";
  import { workflowTakesQuestion } from "./insights/client";
  import { formatSearchAliases, parseSearchAliases } from "./operator/searchAliases";
  import type {
    InsightWorkflow,
    LLMModel,
    LLMSettings,
    LLMStep,
    Settings,
    SettingsQuality,
  } from "./operator/types";

  export let operatorClient: OperatorClient | null = null;

  const dispatch = createEventDispatcher<{ openProviders: void; openTemplates: void }>();

  interface QualityOption {
    value: SettingsQuality;
    label: string;
    description: string;
  }

  const QUALITY_OPTIONS: QualityOption[] = [
    {
      value: "fast",
      label: "Fast",
      description: "Fastest transcription, lower accuracy.",
    },
    {
      value: "balanced",
      label: "Balanced",
      description: "Default — good accuracy at reasonable speed.",
    },
    {
      value: "best",
      label: "Best",
      description: "Highest accuracy (fp32), slowest.",
    },
  ];

  let settings: Settings | null = null;
  let quality: SettingsQuality = "balanced";
  let deviceOverride = "";
  let transcriptionTermsText = "";
  let searchAliasesText = "";
  let savedSearchAliasesText = "";

  let savedQuality: SettingsQuality = "balanced";
  let savedDeviceOverride = "";
  let savedTranscriptionTermsText = "";

  // The LLM half. Null when it could not be read, which is not the same as a
  // deployment with no providers: the summarisation step says so rather than
  // rendering the locked card, which would accuse a configured install of
  // being unconfigured.
  let llm: LLMSettings | null = null;
  let summary: LLMStep = { enabled: false, provider: "", model: "", template: "" };
  let savedSummary = "";
  let llmError = "";

  // The templates this build ships. Advisory: a step stores its template as a
  // plain id and the operator validates only the shape, so the picker is what
  // catches a typo before it is saved — and a failed fetch must not stop an
  // administrator configuring the step.
  let workflows: InsightWorkflow[] = [];
  let workflowsKnown = false;

  // Model lists, per provider, fetched when the combobox is opened rather than
  // on load: it is one HTTP call per endpoint and most visits to this page do
  // not touch the model.
  let modelsByProvider: Record<string, LLMModel[]> = {};
  let modelsErrorByProvider: Record<string, string> = {};
  let loadingModelsFor = "";

  let loading = true;
  let saving = false;
  let loadError = "";
  let saveError = "";

  onMount(() => {
    void loadAll();
  });

  async function loadAll() {
    if (!operatorClient) {
      return;
    }
    loading = true;
    loadError = "";
    saveError = "";
    llmError = "";
    try {
      applySettings(await operatorClient.getSettings());
    } catch (error) {
      loadError = asMessage(error);
    } finally {
      loading = false;
    }
    await Promise.all([loadLLM(), loadWorkflows()]);
  }

  async function loadLLM() {
    if (!operatorClient) {
      return;
    }
    try {
      applyLLM(await operatorClient.getLLMSettings());
    } catch (error) {
      llm = null;
      llmError = asMessage(error);
    }
  }

  async function loadWorkflows() {
    if (!operatorClient) {
      return;
    }
    try {
      workflows = await operatorClient.listInsightWorkflows();
      workflowsKnown = true;
      resolveSummaryTemplate();
    } catch {
      // Swallowed on purpose. Not knowing the registry costs the picker and
      // nothing else; reporting it beside a settings error would put a second
      // failure in front of an administrator who came to fix the first.
      workflowsKnown = false;
    }
  }

  async function loadModels(providerId: string) {
    if (!operatorClient || providerId === "" || modelsByProvider[providerId] || loadingModelsFor !== "") {
      return;
    }
    loadingModelsFor = providerId;
    modelsErrorByProvider = { ...modelsErrorByProvider, [providerId]: "" };
    try {
      modelsByProvider = {
        ...modelsByProvider,
        [providerId]: await operatorClient.listProviderModels(providerId),
      };
    } catch (error) {
      modelsErrorByProvider = { ...modelsErrorByProvider, [providerId]: asMessage(error) };
    } finally {
      loadingModelsFor = "";
    }
  }

  function applySettings(next: Settings) {
    settings = next;
    quality = next.quality;
    deviceOverride = next.device_override;
    transcriptionTermsText = next.transcription_terms.join("\n");
    searchAliasesText = formatSearchAliases(next.search_aliases);
    savedQuality = next.quality;
    savedDeviceOverride = next.device_override;
    savedTranscriptionTermsText = transcriptionTermsText;
    savedSearchAliasesText = searchAliasesText;
  }

  function applyLLM(next: LLMSettings) {
    llm = next;
    llmError = "";
    summary = { ...next.summary };
    savedSummary = JSON.stringify(summary);
    resolveSummaryTemplate();
  }

  // A policy written before the template field existed stores "", which the
  // recorder reads as "the workflow Cassini ships". Resolved to that workflow's
  // real id so the picker opens on the template the step actually runs, rather
  // than on a row standing in front of it.
  //
  // Called from BOTH sides rather than from whichever fetch happens to be
  // written second: the settings and the registry are two requests started
  // together, and resolving inside only one of them was a race — the settings
  // landing last put "" straight back, which is how a build that resolved
  // correctly on one machine showed "The workflow Cassini ships" on another.
  function resolveSummaryTemplate() {
    if (llm === null || !workflowsKnown || summary.template !== "") {
      return;
    }
    const resolved = shippedSummaryTemplate(workflows);
    if (resolved === "") {
      return;
    }
    summary = { ...summary, template: resolved };
    // Not a change an administrator made, so it must not light up Save: the
    // saved policy and this selection mean the same thing to the recorder.
    savedSummary = JSON.stringify(summary);
  }

  function prefersReducedMotion(): boolean {
    return typeof window !== "undefined" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  }

  async function handleSave() {
    if (!operatorClient || saving) {
      return;
    }
    saving = true;
    saveError = "";
    const failures: string[] = [];
    if (sttDirty) {
      try {
        applySettings(
          await operatorClient.putSettings({
            quality,
            device_override: deviceOverride,
            transcription_terms: transcriptionTermsText.split(/\r?\n/),
            search_aliases: parseSearchAliases(searchAliasesText),
          }),
        );
      } catch (error) {
        failures.push(asMessage(error));
      }
    }
    if (summaryDirty) {
      try {
        applyLLM(await operatorClient.putLLMSettings({ summary }));
      } catch (error) {
        failures.push(asMessage(error));
      }
    }
    saveError = failures.join(" ");
    saving = false;
  }

  function asMessage(error: unknown): string {
    if (error instanceof OperatorHttpError) {
      return error.message;
    }
    return error instanceof Error ? error.message : String(error);
  }

  // deviceLabel names the resolved execution device the way an administrator
  // thinks about it. The operator speaks "cuda"/"cpu"; the panel should not
  // make anyone translate.
  function deviceLabel(device: string): string {
    if (device === "cuda") {
      return "GPU (CUDA)";
    }
    if (device === "cpu") {
      return "CPU";
    }
    return device || "—";
  }

  // formatMemory reads a size in MB back to an administrator. Gigabytes are
  // what a host is described in, so a four-digit MB number is the wrong unit.
  function formatMemory(mb: number): string {
    if (mb >= 1024) {
      return `${(mb / 1024).toFixed(1)} GB`;
    }
    return `${mb} MB`;
  }

  function sourceLabel(source: string): string {
    if (source === "user") {
      return "Set by an administrator";
    }
    if (source === "auto") {
      return "Chosen automatically";
    }
    return source || "—";
  }

  // The id the publish pipeline's summary step runs when nothing names one
  // (internal/insight/workflows: SummariseID). Named rather than derived from
  // list order, and it falls back to the first entry: the operator accepts any
  // id the registry resolves, so a build that stopped shipping this one opens
  // on a different template instead of on an error.
  const SHIPPED_SUMMARY_TEMPLATE = "summarise";
  // Off until the publish pipeline reads the chosen template back (D-719):
  // internal/transcribe/summary.go still runs the shipped prompt, so the
  // choice would be saved and ignored. The policy still resolves and saves it.
  const SUMMARY_TEMPLATE_CHOICE = false;

  // The templates this step can actually run: every one EXCEPT a freeform one.
  //
  // A workflow that takes a question cannot be the automatic summary of every
  // meeting, because there is nobody to ask — the pipeline runs unattended, and
  // `cassini insight run` refuses a question-taking workflow with no question.
  // Offering it here would be offering a step that fails on every recording.
  // Decided on the registry's own bytes, the same way the Prepare panel decides
  // whether to show a question box, so the two cannot disagree about which
  // templates are which.
  //
  // A plain function over its argument rather than a reactive derivation ALONE,
  // because loadWorkflows has to filter in the same tick it assigns `workflows`
  // — a `$:` has not recomputed by then, and reading one there resolved the
  // step's template against an empty list.
  function summaryCandidates(list: InsightWorkflow[]): InsightWorkflow[] {
    return list.filter((workflow) => !workflowTakesQuestion(workflow));
  }

  $: summaryTemplates = summaryCandidates(workflows);

  function shippedSummaryTemplate(list: InsightWorkflow[]): string {
    const candidates = summaryCandidates(list);
    return (
      candidates.find((workflow) => workflow.id === SHIPPED_SUMMARY_TEMPLATE)?.id ??
      candidates[0]?.id ??
      ""
    );
  }

  function providerName(id: string): string {
    const provider = llm?.providers.find((row) => row.id === id);
    return provider ? provider.name || provider.base_url || provider.id : id;
  }

  // What an empty model field on this step means, said in the field: the
  // provider's default model is what runs (D-749), and the placeholder names
  // it rather than the older "endpoint default", which named nothing.
  function providerModelPlaceholder(id: string): string {
    const model = llm?.providers.find((row) => row.id === id)?.model ?? "";
    return model !== ""
      ? `${model} (the provider's default)`
      : "no default model set on this provider";
  }

  // Switching the step on with nothing chosen would send a body the operator
  // refuses ("summary is enabled but has no provider"), so the first provider
  // is chosen for you — which is also what a fresh install wants.
  function toggleSummary(enabled: boolean) {
    const first = llm?.providers[0];
    summary = {
      ...summary,
      enabled,
      provider: enabled && summary.provider === "" && first ? first.id : summary.provider,
    };
  }

  function chooseProvider(id: string) {
    // The model belonged to the old endpoint. Keeping it would name a model on
    // a server that may never have heard of it, and the failure would arrive a
    // meeting later.
    summary = { ...summary, provider: id, model: "" };
  }

  $: effectiveDevice = settings?.effective.device ?? "";
  $: runsOnGPU = effectiveDevice === "cuda";

  $: providers = llm?.providers ?? [];
  // Null while the LLM settings have not been read: three states, and the
  // locked card belongs to exactly one of them.
  $: hasProvider = llm === null ? null : providers.length > 0;
  $: summaryModels = modelsByProvider[summary.provider] ?? [];

  // Two stores, two dirty bits, and the transcription terms belong to the STT
  // one: they ride the same PUT as quality and the device override, so a page
  // whose only edit was a term must still send that request and must not send
  // the LLM one.
  $: sttDirty =
    settings !== null &&
    (quality !== savedQuality ||
      deviceOverride !== savedDeviceOverride ||
      transcriptionTermsText !== savedTranscriptionTermsText ||
      searchAliasesText !== savedSearchAliasesText);
  $: summaryDirty = llm !== null && JSON.stringify(summary) !== savedSummary;
  $: isDirty = sttDirty || summaryDirty;
  $: unsavedChanges.set(isDirty);

  // The address this page was on when its edits began. A back or forward
  // while they are unsaved is put back here until the prompt is answered.
  let stableHref = typeof window !== "undefined" ? window.location.href : "";
  $: if (!isDirty && typeof window !== "undefined") {
    stableHref = window.location.href;
  }

  function holdHistory(event: PopStateEvent) {
    if (!isDirty) {
      return;
    }
    const target = window.location.href;
    if (target === stableHref) {
      return;
    }
    event.stopImmediatePropagation();
    window.history.pushState({}, "", stableHref);
    guardLeave(() => {
      window.history.pushState({}, "", target);
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
  }

  let stayButton: HTMLButtonElement;
  $: if ($leavePrompt) {
    void tick().then(() => stayButton?.focus());
  }

  function handleLeaveKeydown(event: KeyboardEvent) {
    if ($leavePrompt && event.key === "Escape") {
      event.preventDefault();
      cancelLeave();
    }
  }

  function warnBeforeUnload(event: BeforeUnloadEvent) {
    if (!isDirty) {
      return;
    }
    event.preventDefault();
    event.returnValue = "";
  }

  onMount(() => {
    window.addEventListener("popstate", holdHistory, { capture: true });
    window.addEventListener("beforeunload", warnBeforeUnload);
    window.addEventListener("keydown", handleLeaveKeydown);
  });

  onDestroy(() => {
    unsavedChanges.set(false);
    window.removeEventListener("popstate", holdHistory, { capture: true });
    window.removeEventListener("beforeunload", warnBeforeUnload);
    window.removeEventListener("keydown", handleLeaveKeydown);
    cancelLeave();
  });
</script>

<header class="op-panel-head">
    <div>
      <div class="op-panel-title">
        <h1>Publish pipeline</h1>
      </div>
      <p>
        Every step between the call ending and the meeting being published. These apply to
        every room.
      </p>
    </div>
    <div class="op-panel-actions">
      <button
        class="icon-btn"
        type="button"
        on:click={loadAll}
        disabled={loading || !operatorClient}
        aria-label="Reload the publish pipeline"
      >
        <RefreshCw size={15} aria-hidden="true" />
      </button>
    </div>
  </header>

{#if operatorClient}
  <RecordingSetup {operatorClient} />
{/if}

<!-- The readiness checks offer a "Set up storage" action that scrolls here,
     which is what the removed Setup page's own anchor used to do. Keeping the
     id on a wrapper rather than inside RecordingAccessPanel leaves that
     component untouched and keeps the cross-link a property of the layout that
     places the two sections, which is where it belongs. -->
<div id="recording-storage">
  <RecordingAccessPanel {operatorClient} />
</div>

  {#if loadError}
    <div class="err-box" role="alert">{loadError}</div>
  {:else if loading}
    <p class="op-state">Loading settings…</p>
  {:else if !settings}
    <p class="op-state">Settings aren't available.</p>
  {:else}
    <div class="pipe-body">
      <!-- What the operator found, and — the part the tier alone does not
           answer — what the next build will actually do with it. The device is
           auto-selected, and on a host with no usable GPU that answer is the
           CPU: slower, but a transcript. Saying so here is what keeps the
           fallback explicit rather than something an admin discovers from a
           blocked build (D-702). -->
      <section class="op-tint hw-card">
        <p class="set-row-name op-card-title">Hardware</p>
        <p class="set-row-sub">What Cassini found on the machine it runs on, and how it will transcribe.</p>
        <dl class="hw-facts">
          <div class="hw-fact">
            <dt>GPU</dt>
            <dd>
              {#if settings.detected_gpu}
                <span class="pill yes">Yes</span>
              {:else}
                <span class="pill">No</span>
              {/if}
            </dd>
          </div>
          <div class="hw-fact">
            <dt>CPU cores</dt>
            <dd>
              <span class="pill"
                >{settings.cores}</span
              >
            </dd>
          </div>
          <div class="hw-fact">
            <dt>Settings</dt>
            <dd>
              <span class="pill"
                >{sourceLabel(settings.source)}</span
              >
            </dd>
          </div>
        </dl>
        <div class="hw-section">
          <p class="hw-effective">
            Transcribes on the
            <code class="pipe-code">{deviceLabel(settings.effective.device)}</code>
            {#if settings.effective.model}
              with the <code class="pipe-code">{settings.effective.model}</code> model
            {/if}
          </p>
          {#if settings.effective.min_free_memory_mb > 0}
            <p class="set-row-sub">
              Each recording is processed once {formatMemory(settings.effective.min_free_memory_mb)}
              of memory is free.
            </p>
          {/if}
          {#if settings.effective.model_download_mb > 0}
            <p class="set-row-sub pipe-warn">
              This model isn't included yet. The first recording downloads it once (about
              {settings.effective.model_download_mb} MB); after that, processing starts straight away.
            </p>
          {/if}
          {#if settings.effective.note}
            <p class="set-row-sub">{settings.effective.note}</p>
          {/if}
        </div>

        <!-- The override belongs to the hardware, not to the end of the page:
             it is the answer to "what the operator found is wrong", so it sits
             under the finding it corrects rather than three sections away from
             it. A rule rather than a card of its own, because on its own it
             would read as a step in the pipeline, which it is not. -->
        <div class="hw-section">
          <label class="op-field hw-device" for="stt-device">
            <span class="set-row-name">Override transcription device</span>
          </label>
          <span class="op-select hw-device-select">
            <select id="stt-device" bind:value={deviceOverride} class="op-input">
              <option value="">Auto</option>
              <option value="cpu">CPU</option>
              <option value="cuda">GPU (CUDA)</option>
            </select>
            <ChevronDown size={14} class="op-select-chevron" aria-hidden="true" />
          </span>
          <p class="set-row-sub hw-device-note">
            Leave on Auto unless the hardware above is wrong. Auto uses the GPU when one is
            available. If you choose a device this machine doesn't have, recordings won't be
            processed.
          </p>
        </div>
      </section>

      <!-- One row per step, in pipeline order: transcribe, then summarise.
           Each step is its own row under a full rule with extra space above,
           so two independent settings — one of which sends text to a third
           party — do not read as one block, without a card around each. -->
      <section class="op-tint pipe-step">
        <div class="set-row-main">
          <p id="stt-quality-heading" class="set-row-name op-card-title">Quality</p>
          <p class="set-row-sub">
            Applies to every recording on this machine.
            {#if runsOnGPU}
              On the GPU, every quality setting uses the same full-precision model, so this only
              changes transcription on the CPU.
            {:else}
              Higher quality uses a larger model: more accurate, but slower on this machine.
            {/if}
          </p>
          <div class="q-list" role="radiogroup" aria-labelledby="stt-quality-heading">
            {#each QUALITY_OPTIONS as option}
              <label class="q-opt" class:selected={quality === option.value}>
                <input
                  type="radio"
                  name="stt-quality"
                  class="q-radio"
                  value={option.value}
                  bind:group={quality}
                />
                <span class="q-main">
                  <span class="q-label">{option.label}</span>
                  <span class="q-desc">{option.description}</span>
                </span>
              </label>
            {/each}
          </div>
        </div>

      </section>

      <!-- Summarisation. The switch belongs to the name, not to the far edge of
           the row: what is being turned on is the step, and the row is the
           step. -->
      <section class="op-tint pipe-step">
        <div class="set-row-main">
          <div class="step-head">
            <p class="set-row-name op-card-title" class:off={hasProvider === false}>
              Summarisation
            </p>
            <input
              type="checkbox"
              class="op-switch"
              aria-label="Summarise every meeting"
              disabled={hasProvider !== true}
              checked={summary.enabled && hasProvider === true}
              on:change={(event) => toggleSummary(event.currentTarget.checked)}
            />
          </div>
          <p class="set-row-sub" class:off={hasProvider === false}>
            Sends each transcript to the provider and writes the summary shown on the meeting.
          </p>

          {#if llmError}
            <p class="set-row-sub pipe-warn pipe-gap">
              Couldn't load the AI provider settings, so this step can't be shown or changed:
              {llmError}
            </p>
          {:else if hasProvider === false}
            <div class="pipe-gap-lg">
              <NeedsProviderCard
                title="Add a provider to write summaries"
                on:open={() => dispatch("openProviders")}
              />
            </div>
          {:else if hasProvider === true && summary.enabled}
            <div class="step-config">
              <label class="op-field">
                <span class="op-field-label">Provider</span>
                <span class="op-select">
                  <select
                    class="op-input"
                    value={summary.provider}
                    on:change={(event) => chooseProvider(event.currentTarget.value)}
                  >
                    {#each providers as provider (provider.id)}
                      <option value={provider.id}>{providerName(provider.id)}</option>
                    {/each}
                  </select>
                  <ChevronDown size={14} class="op-select-chevron" aria-hidden="true" />
                </span>
              </label>

              <ModelCombobox
                bind:value={summary.model}
                models={summaryModels}
                loading={loadingModelsFor === summary.provider}
                error={modelsErrorByProvider[summary.provider] ?? ""}
                placeholder={providerModelPlaceholder(summary.provider)}
                on:open={() => void loadModels(summary.provider)}
              />

              <!-- The summary is an insight like any other, so it names the
                   template it runs rather than hiding a prompt of its own. A
                   free-text "workflow" field asked an administrator to know an
                   id the registry could have told them. -->
              {#if SUMMARY_TEMPLATE_CHOICE}
              <label class="op-field">
                <span class="op-field-label">Insight template</span>
                <select
                  bind:value={summary.template}
                  class="op-input"
                  disabled={!workflowsKnown}
                >
                  <!-- No synthetic "default" row. It named no template and
                       disclosed no question, and the template it stood for is
                       one of the options below it. Empty is still what a policy
                       written before the field existed stores, and it is
                       resolved to a real id the moment the registry lands — so
                       this option is only ever reachable when it could not be. -->
                  {#if summary.template === ""}
                    <option value="">The workflow Cassini ships</option>
                  {/if}
                  {#each summaryTemplates as workflow (workflow.id)}
                    <option value={workflow.id}>{workflow.name}</option>
                  {/each}
                  <!-- A template saved by an older or newer image is still what
                       this step runs, so it stays selectable rather than
                       silently resetting to the default when this build has
                       never heard of it. -->
                  {#if summary.template !== "" && !summaryTemplates.some((w) => w.id === summary.template)}
                    <option value={summary.template}>
                      {summary.template} — not in this build
                    </option>
                  {/if}
                </select>
                {#if !workflowsKnown}
                  <span class="pipe-help">
                    Templates could not be listed.
                  </span>
                {/if}
                <!-- The field is saved and served; the publish pipeline does not
                     resolve it yet (internal/transcribe/summary.go splices the
                     shipped prompt directly). Saying so beats a control that
                     silently does nothing — delete this line when the pipeline
                     reads it back (D-719). -->
                <span class="pipe-help">
                  Your choice is saved, but summaries still use the built-in template for now.
                </span>
                <button type="button" class="link-btn pipe-link" on:click={() => dispatch("openTemplates")}>
                  View insight templates
                </button>
              </label>
              {/if}
            </div>
          {/if}
        </div>

      </section>

      <section class="op-tint pipe-step">
        <div class="set-row-main pipe-text-step">
          <div>
            <h3 class="set-row-name op-card-title">Vocabulary</h3>
            <p class="set-row-sub">Names and terms the transcriber should spell correctly.</p>
          </div>
          <label class="op-field">
            <span class="pipe-field-head">
              <span class="op-field-label">One term per line</span>
              <span class="pipe-limit">(up to 100, 100 characters each)</span>
            </span>
            <textarea
              bind:value={transcriptionTermsText}
              class="op-input pipe-textarea"
              maxlength={10_100}
              placeholder={'Gocassini\nNextcloud Talk\nProject Cassini'}
            ></textarea>
          </label>
          <ul class="pipe-notes">
            <li>Participant names are added automatically.</li>
            <li>Only used where the audio matches, so it never adds words.</li>
            <li>Not available on the <em>Fast</em> quality setting. If a term can't be used, the recording notes why.</li>
          </ul>
        </div>
      </section>

      <section class="op-tint pipe-step">
        <div class="set-row-main pipe-text-step">
          <div>
            <h3 class="set-row-name op-card-title">Search spellings</h3>
            <p class="set-row-sub">Common mishearings of a name, so searching for the name finds them too.</p>
          </div>
          <label class="op-field">
            <span class="pipe-field-head">
              <span class="op-field-label">One name per line, then its spellings, separated by commas</span>
              <span class="pipe-limit">(up to 100 names, 25 spellings each)</span>
            </span>
            <textarea
              bind:value={searchAliasesText}
              class="op-input pipe-textarea"
              maxlength={10_100}
              placeholder={'Cassini, casino, casini\nEisbuk, ice book, icebook'}
            ></textarea>
          </label>
          <ul class="pipe-notes">
            <li>Doesn't change any transcript, only what search finds.</li>
            <li>Results say when they matched one of these spellings.</li>
            <li>To get the name right in new recordings, add it to Vocabulary above.</li>
          </ul>
        </div>
      </section>

      {#if saveError}
        <div class="err-box" role="alert">{saveError}</div>
      {/if}
    </div>

    {#if isDirty || saving}
      <div
        class="save-bar"
        class:asking={$leavePrompt !== null}
        role={$leavePrompt ? "alertdialog" : "region"}
        aria-label={$leavePrompt ? "Leave without saving?" : "Unsaved changes"}
        transition:fly={{ y: 16, duration: prefersReducedMotion() ? 0 : 180 }}
      >
        {#if $leavePrompt}
          <TriangleAlert size={16} class="save-bar-icon" aria-hidden="true" />
          <p class="save-bar-text">You have unsaved changes. Leave without saving?</p>
          <div class="save-bar-actions">
            <button bind:this={stayButton} class="sb-btn sb-light" type="button" on:click={cancelLeave}>
              Stay
            </button>
            <button class="sb-btn sb-danger" type="button" on:click={confirmLeave}>Leave</button>
          </div>
        {:else}
          <p class="save-bar-text">{saving ? "Saving changes…" : "You have unsaved changes"}</p>
          <button class="sb-btn sb-light" type="button" disabled={saving || !isDirty} on:click={handleSave}>
            {#if saving}
              <span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
              Saving…
            {:else}
              Save
            {/if}
          </button>
        {/if}
      </div>
    {/if}
  {/if}

<style>
  .pipe-body {
    display: grid;
    gap: calc(var(--op-x, 20px) + 8px);
    margin-top: calc(var(--op-x, 20px) + 8px - 16px);
  }
  .hw-card {
    padding: 14px 16px;
  }
  .hw-facts {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px 22px;
    margin: 11px 0 0;
    padding: 10px 12px;
    background-color: var(--op-inset);
    border: 1px solid var(--op-inset-border);
    border-radius: var(--radius-box, 0.5rem);
  }
  .hw-fact {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .hw-fact dt {
    font-size: 12.5px;
    line-height: 20px;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  .hw-fact dd {
    display: flex;
    align-items: center;
    margin: 0;
  }
  .pill {
    display: inline-block;
    padding: 4px 7px;
    font-family: var(--font-mono);
    font-size: 11.5px;
    font-weight: 500;
    line-height: 1;
    color: var(--color-base-content);
    background-color: color-mix(in oklch, var(--color-base-content) 14%, var(--color-base-200));
    border: 1px solid color-mix(in oklch, var(--color-base-content) 22%, var(--color-base-200));
    border-radius: var(--radius-selector, 0.25rem);
  }
  .pill.yes {
    color: var(--color-success);
    border-color: var(--color-success);
  }
  .hw-section {
    margin-top: 12px;
    padding-top: 12px;
    border-top: 1px solid color-mix(in oklch, var(--color-base-content) 9%, var(--color-base-200));
  }
  .hw-effective {
    margin: 0;
    font-size: 13.5px;
    font-weight: 600;
    line-height: 1.5;
  }
  .pipe-code {
    padding: 1px 5px;
    font-family: var(--font-mono);
    font-size: 11.5px;
    font-weight: 500;
    overflow-wrap: anywhere;
    background-color: var(--op-code-bg);
    border: 1px solid var(--op-code-border);
    border-radius: var(--radius-selector, 0.25rem);
  }
  .pipe-warn {
    color: var(--color-warning);
  }
  .set-row-sub {
    display: block;
    text-wrap: pretty;
  }
  .hw-device-select {
    display: block;
    max-width: 20rem;
    margin-top: 8px;
  }
  .hw-device-note {
    margin-top: 6px;
  }

  .pipe-step {
    display: flex;
    align-items: flex-start;
    gap: 14px;
    padding: 14px 16px;
  }
  .step-head {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .pipe-gap {
    margin-top: 8px;
  }
  .pipe-gap-lg {
    margin-top: 12px;
  }
  .step-config {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: 12px;
    margin-top: 10px;
  }
  .pipe-help {
    font-size: 11.5px;
    line-height: 1.45;
    color: color-mix(in oklch, var(--color-base-content) 45%, transparent);
  }
  .pipe-text-step {
    display: grid;
    gap: 10px;
  }

  .pipe-link {
    align-self: flex-start;
    font-weight: 500;
    color: var(--color-base-content);
    text-decoration: underline;
    text-decoration-color: color-mix(in oklch, var(--color-base-content) 40%, transparent);
    text-underline-offset: 2px;
  }
  .pipe-link:hover {
    text-decoration-color: currentColor;
  }
  .pipe-field-head {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 2px 6px;
  }
  .pipe-limit {
    font-size: 11.5px;
    color: color-mix(in oklch, var(--color-base-content) 45%, transparent);
  }
  .pipe-notes {
    display: grid;
    gap: 3px;
    margin: -4px 0 0;
    padding-left: 16px;
    list-style: disc;
    font-size: 12.5px;
    line-height: 1.5;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  .pipe-notes li::marker {
    color: color-mix(in oklch, var(--color-base-content) 35%, transparent);
  }
  .pipe-textarea {
    min-height: 7rem;
    resize: vertical;
  }

  .q-list {
    display: grid;
    gap: 6px;
    margin-top: 11px;
  }
  .q-opt {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 7px 10px;
    cursor: pointer;
    background-color: var(--op-inset);
    border: 1px solid var(--op-inset-border);
    border-radius: var(--radius-box, 0.5rem);
  }
  .q-opt:hover {
    border-color: color-mix(in oklch, var(--color-base-content) 30%, var(--color-base-200));
  }
  .q-opt.selected {
    background-color: color-mix(in srgb, var(--color-primary) 14%, var(--color-base-100));
    border-color: var(--color-primary);
  }
  .q-radio {
    position: relative;
    flex: none;
    width: 16px;
    height: 16px;
    margin: 0;
    appearance: none;
    -webkit-appearance: none;
    cursor: pointer;
    background: var(--color-base-100);
    border: 1px solid color-mix(in oklch, var(--color-base-content) 26%, transparent);
    border-radius: 50%;
  }
  .q-radio:checked {
    border-color: var(--color-primary);
  }
  .q-radio:checked::after {
    content: "";
    position: absolute;
    top: 50%;
    left: 50%;
    width: 8px;
    height: 8px;
    background: var(--color-primary);
    border-radius: 50%;
    transform: translate(-50%, -50%);
  }
  .q-radio:focus-visible {
    outline: 2px solid var(--color-primary);
    outline-offset: 2px;
  }
  .q-main {
    display: flex;
    flex: 1;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 2px 10px;
    min-width: 0;
  }
  .q-label {
    font-size: 13.5px;
    font-weight: 600;
  }
  .q-desc {
    font-size: 12.5px;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }

  .op-switch {
    position: relative;
    flex: none;
    width: 30px;
    height: 17px;
    margin: 0;
    appearance: none;
    -webkit-appearance: none;
    cursor: pointer;
    background: var(--color-base-300);
    border: 1px solid var(--color-base-300);
    border-radius: 999px;
    transition: background-color 0.15s ease, border-color 0.15s ease;
  }
  .op-switch::after {
    content: "";
    position: absolute;
    top: 1px;
    left: 1px;
    width: 13px;
    height: 13px;
    background: var(--color-base-100);
    border-radius: 50%;
    box-shadow: 0 1px 2px oklch(0% 0 0 / 0.25);
    transition: transform 0.15s ease;
  }
  .op-switch:checked {
    background: var(--color-primary);
    border-color: var(--color-primary);
  }
  .op-switch:checked::after {
    transform: translateX(13px);
  }
  .op-switch:disabled {
    cursor: not-allowed;
    opacity: 0.55;
  }
  .op-switch:focus-visible {
    outline: 2px solid var(--color-primary);
    outline-offset: 2px;
  }

  .save-bar {
    position: sticky;
    bottom: 16px;
    z-index: 20;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: center;
    gap: 10px 14px;
    align-self: center;
    max-width: 100%;
    margin-top: 8px;
    padding: 8px 8px 8px 18px;
    color: var(--color-base-100);
    background-color: var(--color-base-content);
    border-radius: var(--radius-box, 0.5rem);
    box-shadow:
      0 1px 3px oklch(0% 0 0 / 0.18),
      0 8px 24px oklch(0% 0 0 / 0.24);
  }
  .save-bar.asking {
    padding: 10px 10px 10px 16px;
    box-shadow:
      0 0 0 2px var(--color-error),
      0 8px 24px oklch(0% 0 0 / 0.24);
  }
  .save-bar :global(.save-bar-icon) {
    flex: none;
    color: var(--color-error);
  }
  .save-bar-text {
    min-width: 0;
    margin: 0;
    font-size: 13px;
    font-weight: 600;
  }
  .save-bar-actions {
    display: flex;
    gap: 8px;
  }
  .sb-btn {
    padding: 6px 14px;
    cursor: pointer;
    border: 0;
    border-radius: var(--radius-field, 0.5rem);
    font-size: 13px;
    font-weight: 600;
    line-height: 1.4;
  }
  .sb-light {
    color: var(--color-base-content);
    background-color: var(--color-base-100);
  }
  .sb-light:not(:disabled):hover {
    background-color: color-mix(in oklch, var(--color-base-100) 85%, var(--color-base-content));
  }
  .sb-light:disabled {
    cursor: default;
    opacity: 0.7;
  }
  .sb-danger {
    color: var(--color-error-content, #fff);
    background-color: var(--color-error);
  }
  .sb-danger:hover {
    background-color: color-mix(in oklch, var(--color-error) 85%, black);
  }
  .sb-btn:focus-visible {
    outline: 2px solid var(--color-primary);
    outline-offset: 2px;
  }
  @media (prefers-reduced-motion: reduce) {
    .op-switch,
    .op-switch::after {
      transition: none;
    }
  }
</style>
