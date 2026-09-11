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
  import { createEventDispatcher, onMount } from "svelte";
  import { RefreshCw } from "@lucide/svelte";
  import { OperatorClient, OperatorHttpError } from "./operator/client";
  import ModelCombobox from "./ModelCombobox.svelte";
  // D-757: "Who can see recordings" is a section of the Settings panel, above
  // the pipeline it applies to. Its own component because it is a page's worth
  // of state — a switch, its prerequisites, its progress — and none of it is
  // shared with the settings below.
  import RecordingAccessPanel from "./RecordingAccessPanel.svelte";
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

  const dispatch = createEventDispatcher<{ openProviders: void }>();

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
      return "User override";
    }
    if (source === "auto") {
      return "Auto-detected";
    }
    return source || "—";
  }

  // The id the publish pipeline's summary step runs when nothing names one
  // (internal/insight/workflows: SummariseID). Named rather than derived from
  // list order, and it falls back to the first entry: the operator accepts any
  // id the registry resolves, so a build that stopped shipping this one opens
  // on a different template instead of on an error.
  const SHIPPED_SUMMARY_TEMPLATE = "summarise";

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
</script>

<RecordingAccessPanel {operatorClient} />

<section class="rounded-box border border-base-300 bg-base-100 shadow-sm">
  <header class="flex items-start justify-between gap-3 px-4 py-3">
    <div>
      <h2 class="font-semibold">Publish pipeline</h2>
      <p class="text-xs text-base-content/60">
        Every step between the call ending and the meeting being published. These apply to
        every room.
      </p>
    </div>
    <div class="flex items-center gap-2">
      {#if settings}
        <button
          class="btn btn-primary btn-sm hidden text-sm sm:inline-flex"
          type="button"
          disabled={saving || !isDirty}
          on:click={handleSave}
        >
          {#if saving}
            <span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
            Saving…
          {:else}
            Save
          {/if}
        </button>
      {/if}
      <button
        class="btn btn-ghost btn-sm btn-square"
        type="button"
        on:click={loadAll}
        disabled={loading || !operatorClient}
        aria-label="Reload the publish pipeline"
      >
        <RefreshCw size={16} aria-hidden="true" />
      </button>
    </div>
  </header>

  {#if loadError}
    <div class="px-4 py-4">
      <div class="alert alert-error text-sm">{loadError}</div>
    </div>
  {:else if loading}
    <div class="flex items-center justify-center p-6 text-sm text-base-content/60">Loading settings…</div>
  {:else if !settings}
    <div class="flex items-center justify-center p-6 text-sm text-base-content/60">No settings available.</div>
  {:else}
    <div class="grid gap-4 p-4">
      <!-- What the operator found, and — the part the tier alone does not
           answer — what the next build will actually do with it. The device is
           auto-selected, and on a host with no usable GPU that answer is the
           CPU: slower, but a transcript. Saying so here is what keeps the
           fallback explicit rather than something an admin discovers from a
           blocked build (D-702). -->
      <section class="rounded-box border border-base-300 bg-base-200 p-3">
        <p class="text-sm font-semibold">Detected hardware</p>
        <p class="text-xs text-base-content/60">What the operator found on this host.</p>
        <dl class="mt-2 flex flex-wrap items-center gap-x-6 gap-y-2">
          <div class="flex items-center gap-2">
            <dt class="text-sm text-base-content/60">GPU</dt>
            <dd class="text-sm">
              {#if settings.detected_gpu}
                <span class="badge badge-success badge-outline badge-sm">Yes</span>
              {:else}
                <span class="badge badge-outline badge-sm border-base-content/20 text-base-content">No</span>
              {/if}
            </dd>
          </div>
          <div class="flex items-center gap-2">
            <dt class="text-sm text-base-content/60">CPU cores</dt>
            <dd class="text-sm">
              <span class="badge badge-outline badge-sm border-base-content/20 text-base-content"
                >{settings.cores}</span
              >
            </dd>
          </div>
          <div class="flex items-center gap-2">
            <dt class="text-sm text-base-content/60">Source</dt>
            <dd class="text-sm">
              <span class="badge badge-outline badge-sm border-base-content/20 text-base-content"
                >{sourceLabel(settings.source)}</span
              >
            </dd>
          </div>
        </dl>
        <div class="mt-3 border-t border-base-300 pt-3">
          <p class="text-sm">
            Transcribes on
            <span class="font-semibold">{deviceLabel(settings.effective.device)}</span>
            {#if settings.effective.model}
              using <code class="text-xs">{settings.effective.model}</code>
            {/if}
          </p>
          {#if settings.effective.min_free_memory_mb > 0}
            <p class="mt-1 text-xs text-base-content/60">
              A build of this tier starts when {formatMemory(settings.effective.min_free_memory_mb)}
              of memory is free.
            </p>
          {/if}
          {#if settings.effective.model_download_mb > 0}
            <p class="mt-1 text-xs text-warning">
              This image does not carry that model. The first build downloads it once,
              about {settings.effective.model_download_mb} MB, and later builds start at once.
            </p>
          {/if}
          {#if settings.effective.note}
            <p class="mt-1 text-xs text-base-content/60">{settings.effective.note}</p>
          {/if}
        </div>

        <!-- The override belongs to the hardware, not to the end of the page:
             it is the answer to "what the operator found is wrong", so it sits
             under the finding it corrects rather than three sections away from
             it. A rule rather than a card of its own, because on its own it
             would read as a step in the pipeline, which it is not. -->
        <div class="mt-3 border-t border-base-300 pt-3">
          <p class="text-sm font-semibold">Device override</p>
          <p class="text-xs text-base-content/60">
            Leave this on the default unless the detected hardware is wrong. Auto uses the GPU
            when this host has a usable one; pinning a device the host cannot provide blocks
            builds rather than quietly using the other.
          </p>
          <label class="mt-2 flex w-full max-w-xs flex-col gap-1">
            <span class="text-xs font-medium text-base-content/70">Device</span>
            <select bind:value={deviceOverride} class="select select-sm w-full border-base-300 shadow-none">
              <option value="">Auto</option>
              <option value="cpu">CPU</option>
              <option value="cuda">CUDA</option>
            </select>
          </label>
        </div>
      </section>

      <!-- One card per step, in pipeline order: transcribe, then summarise.
           They were rows of one shared card separated by hairlines, which made
           two independent settings — one of which sends text to a third party —
           read as one block. A step is a thing you switch on and configure, so
           it gets an edge of its own. -->
      <section class="rounded-box border border-base-300 bg-base-200 p-3">
        <div>
          <p id="stt-quality-heading" class="text-sm font-semibold">Quality</p>
          <p class="text-xs text-base-content/60">
            Applies to every recording on this host.
            {#if runsOnGPU}
              Every tier loads the same fp32 model on CUDA — this choice takes effect on CPU
              builds.
            {:else}
              A higher tier loads a larger model: more accurate, and slower here.
            {/if}
          </p>
          <div class="mt-2 grid gap-2" role="radiogroup" aria-labelledby="stt-quality-heading">
            {#each QUALITY_OPTIONS as option}
              <label
                class="flex cursor-pointer items-center gap-2.5 rounded-box border px-3 py-2 transition {quality ===
                option.value
                  ? 'border-primary bg-primary/25 ring-1 ring-inset ring-primary'
                  : 'border-base-300 bg-base-100 hover:border-primary/50'}"
              >
                <input
                  type="radio"
                  name="stt-quality"
                  class="radio radio-primary radio-xs shrink-0"
                  value={option.value}
                  bind:group={quality}
                />
                <span class="min-w-0 flex-1">
                  <span class="text-sm font-medium">{option.label}</span>
                  <span class="ml-2 text-xs text-base-content/60">{option.description}</span>
                </span>
              </label>
            {/each}
          </div>
        </div>

      </section>

      <!-- Summarisation. The switch belongs to the name, not to the far edge of
           the row: what is being turned on is the step, and the row is the
           step. -->
      <section class="rounded-box border border-base-300 bg-base-200 p-3">
        <div>
          <div class="flex items-center gap-2.5">
            <p class="text-sm font-semibold" class:opacity-50={hasProvider === false}>
              Summarisation
            </p>
            <input
              type="checkbox"
              class="toggle toggle-primary toggle-sm"
              aria-label="Summarise every meeting"
              disabled={hasProvider !== true}
              checked={summary.enabled && hasProvider === true}
              on:change={(event) => toggleSummary(event.currentTarget.checked)}
            />
          </div>
          <p class="text-xs text-base-content/60" class:opacity-50={hasProvider === false}>
            Sends each transcript to the provider and writes the summary shown on the meeting.
          </p>

          {#if llmError}
            <p class="mt-2 text-xs text-warning">
              The AI settings could not be read, so this step cannot be shown or changed:
              {llmError}
            </p>
          {:else if hasProvider === false}
            <div class="mt-3">
              <NeedsProviderCard
                title="Add a provider to write summaries"
                on:open={() => dispatch("openProviders")}
              />
            </div>
          {:else if hasProvider === true && summary.enabled}
            <div class="mt-3 grid gap-3 sm:grid-cols-3">
              <label class="flex w-full flex-col gap-1">
                <span class="text-xs font-medium text-base-content/70">Provider</span>
                <select
                  class="select select-sm w-full border-base-300 shadow-none"
                  value={summary.provider}
                  on:change={(event) => chooseProvider(event.currentTarget.value)}
                >
                  {#each providers as provider (provider.id)}
                    <option value={provider.id}>{providerName(provider.id)}</option>
                  {/each}
                </select>
              </label>

              <ModelCombobox
                bind:value={summary.model}
                models={summaryModels}
                loading={loadingModelsFor === summary.provider}
                error={modelsErrorByProvider[summary.provider] ?? ""}
                on:open={() => void loadModels(summary.provider)}
              />

              <!-- The summary is an insight like any other, so it names the
                   template it runs rather than hiding a prompt of its own. A
                   free-text "workflow" field asked an administrator to know an
                   id the registry could have told them. -->
              <label class="flex w-full flex-col gap-1">
                <span class="text-xs font-medium text-base-content/70">Insight template</span>
                <select
                  bind:value={summary.template}
                  class="select select-sm w-full border-base-300 shadow-none"
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
                  <span class="text-xs text-base-content/50">
                    The template list could not be read, so this keeps what was saved.
                  </span>
                {/if}
                <!-- The field is saved and served; the publish pipeline does not
                     resolve it yet (internal/transcribe/summary.go splices the
                     shipped prompt directly). Saying so beats a control that
                     silently does nothing — delete this line when the pipeline
                     reads it back (D-719). -->
                <span class="text-xs text-base-content/50">
                  Saved with the policy. The publish pipeline still runs the summary prompt
                  Cassini ships.
                </span>
              </label>
            </div>
          {/if}
        </div>

      </section>

      <section class="grid content-start gap-2 rounded-box border border-base-300 bg-base-200 p-3">
        <div>
          <h3 class="text-sm font-semibold">Participant and project vocabulary</h3>
          <p class="text-xs text-base-content/60">
            Preferred spellings for names and terms. The transcriber is biased towards them,
            so it can write words it would otherwise get wrong.
          </p>
        </div>
        <label class="flex w-full flex-col gap-1">
          <span class="text-xs font-medium text-base-content/70">One term per line</span>
          <textarea
            bind:value={transcriptionTermsText}
            class="textarea min-h-28 w-full border-base-300 shadow-none"
            maxlength={10_100}
            placeholder={'Gocassini\nNextcloud Talk\nProject Cassini'}
          ></textarea>
          <span class="text-xs text-base-content/60">
            Up to 100 terms and 100 characters per term. Participant display names are supplied
            automatically. A term is only written where the audio already supports it, so this
            corrects spellings without putting words in anyone's mouth. It needs a transcription
            model that ships a BPE vocabulary, which the <em>fast</em> tier never does; when a
            vocabulary cannot be used, the build records that and says why.
          </span>
        </label>
      </section>

      <section class="grid content-start gap-2 rounded-box border border-base-300 bg-base-200 p-3">
        <div>
          <h3 class="text-sm font-semibold">Search spellings</h3>
          <p class="text-xs text-base-content/60">
            What transcription writes when it mishears a name. Searching for the name also
            finds the recordings where it came out differently.
          </p>
        </div>
        <label class="flex w-full flex-col gap-1">
          <span class="text-xs font-medium text-base-content/70">
            One name per line, spellings separated by commas
          </span>
          <textarea
            bind:value={searchAliasesText}
            class="textarea min-h-28 w-full border-base-300 shadow-none"
            maxlength={10_100}
            placeholder={'Cassini, casino, casini\nEisbuk, ice book, icebook'}
          ></textarea>
          <span class="text-xs text-base-content/60">
            Up to 100 names and 25 spellings each. This does not change any recording — it
            only widens what a search looks for, and a result says whether it matched what you
            typed or one of these. Add a spelling when you notice a transcript using it. The
            vocabulary above is the other half: it biases new transcriptions so the name comes
            out right next time.
          </span>
        </label>
      </section>

      <button
        class="btn btn-primary w-full text-sm sm:hidden"
        type="button"
        disabled={saving || !isDirty}
        on:click={handleSave}
      >
        {#if saving}
          <span class="loading loading-spinner loading-sm" aria-hidden="true"></span>
          Saving…
        {:else}
          Save
        {/if}
      </button>

      {#if saveError}
        <div class="alert alert-error text-sm">{saveError}</div>
      {/if}
    </div>
  {/if}
</section>
