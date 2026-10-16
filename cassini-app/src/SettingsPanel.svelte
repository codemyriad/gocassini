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
  import { ChevronDown, Info, TriangleAlert } from "@lucide/svelte";
  import { OperatorClient, OperatorHttpError } from "./operator/client";
  import SpeechModels from "./SpeechModels.svelte";
  import VoiceSeparationModel from "./VoiceSeparationModel.svelte";
  import ModelCombobox from "./ModelCombobox.svelte";
  // D-757: "Who can see recordings" is a section of the Settings panel, above
  // the pipeline it applies to. Its own component because it is a page's worth
  // of state — a switch, its prerequisites, its progress — and none of it is
  // shared with the settings below.
  import DirectShareAccessPanel from "./DirectShareAccessPanel.svelte";
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
  import { workflowTakesQuestion } from "./insights/client";
  import { formatSearchAliases, parseSearchAliases } from "./operator/searchAliases";
  import { pipelineStatus, retentionSpan, runningDownload, speechModelFor, type StepStatus } from "./operator/pipelineStatus";
  import type { RetentionSettings } from "./operator/retention";
  import { onRetentionChanged } from "./operator/retentionSignal";
  import type {
    InsightWorkflow,
    LLMModel,
    LLMSettings,
    LLMStep,
    Settings,
    SettingsQuality,
    SpeechModel,
    SpeechModelInventory,
  } from "./operator/types";

  export let operatorClient: OperatorClient | null = null;

  const dispatch = createEventDispatcher<{ openProviders: void; openTemplates: void; openStorage: void }>();

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
  let meetingFormat: "opus" | "json" = "opus";
  let savedMeetingFormat: "opus" | "json" = "opus";
  let transcriptionEnabled = false;
  let retainVideo = false;
  let sourceRetention: "storage-policy" | "delete-after-processing" = "storage-policy";
  let savedSourceRetention = "storage-policy";
  let savedRetainVideo = false;
  let transcriptionSettings: HTMLElement;
  let publicationSettings: HTMLElement;
  let summarySettings: HTMLElement;
  let keepSettings: HTMLElement;
  let hardwareOpen = false;

  function focusSection(section: HTMLElement | null | undefined) {
    if (!section) return;
    section.focus({ preventScroll: true });
    section.scrollIntoView({
      block: "start",
      behavior: window.matchMedia("(prefers-reduced-motion: reduce)").matches ? "instant" : "smooth",
    });
  }
  function focusTranscriptionSettings() {
    focusSection(transcriptionSettings);
  }
  $: recordingMedia = sourceRetention === "delete-after-processing" ? "nothing" : retainVideo ? "audio-video" : "audio-only";

  function selectRecordingMedia(value: "nothing" | "audio-video" | "audio-only") {
    sourceRetention = value === "nothing" ? "delete-after-processing" : "storage-policy";
    retainVideo = value === "audio-video";
    if (value === "nothing") meetingFormat = "json";
  }
  // Keeping nothing leaves no audio to publish, so publishing audio while
  // Nothing is chosen falls back to keeping audio only.
  function selectMeetingFormat(value: "opus" | "json") {
    meetingFormat = value;
    if (value === "opus" && sourceRetention === "delete-after-processing") selectRecordingMedia("audio-only");
  }
  let activeModel = "";
  let activeRevision = "";
  let readyModel: SpeechModel | null = null;
  let modelInventory: SpeechModelInventory | null = null;
  function toggleTranscription(on: boolean) {
    if (on && readyModel) {
      activeModel = readyModel.id;
      activeRevision = readyModel.revision;
    }
    transcriptionEnabled = on && !!readyModel;
  }
  let savedTranscription = "";
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

  // The Recordings retention policy, for how long kept media stays. Null when
  // it could not be read: the wording then leaves the span out rather than
  // guessing one.
  let retention: RetentionSettings | null = null;

  onMount(() => {
    void loadAll();
    void loadRetention();
    return onRetentionChanged((next) => { retention = next; });
  });

  async function loadRetention() {
    if (!operatorClient) return;
    try {
      retention = await operatorClient.getRetention();
    } catch {
      retention = null;
    }
  }

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
    meetingFormat = next.meeting_format ?? "opus";
    savedMeetingFormat = meetingFormat;
    transcriptionEnabled = next.transcription_enabled === true;
    retainVideo = next.retain_video === true;
    sourceRetention = next.source_retention ?? "storage-policy";
    savedSourceRetention = sourceRetention;
    savedRetainVideo = retainVideo;
    activeModel = next.active_model ?? "";
    activeRevision = next.active_revision ?? "";
    savedTranscription = JSON.stringify([transcriptionEnabled,activeModel,activeRevision]);
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
            meeting_format: publishedFormat,
            source_retention: sourceRetention,
            transcription_enabled: transcriptionEnabled,
            retain_video: retainVideo,
            active_model: activeModel,
            active_revision: activeRevision,
            ...(quality !== savedQuality ? { quality } : {}),
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
  $: selectedDevice = deviceOverride === "cpu" || deviceOverride === "cuda" ? deviceOverride : effectiveDevice || "cpu";
  $: runsOnGPU = selectedDevice === "cuda";

  // The status strip reads the SAVED settings — the snapshot the dirty check
  // compares against — so it says what recordings get today and only moves
  // after a successful Save or load.
  $: savedKeep = savedSourceRetention === "delete-after-processing" ? "nothing" as const : savedRetainVideo ? "audio-video" as const : "audio-only" as const;
  $: savedDevice = settings?.effective?.device ?? "cpu";
  $: savedModel = modelInventory?.models.find((m) => m.id === speechModelFor(savedQuality, savedDevice));
  $: status = pipelineStatus({
    meetingFormat: savedMeetingFormat,
    transcriptionEnabled: settings?.transcription_enabled === true,
    quality: savedQuality,
    activeModel: settings?.active_model,
    activeModelInstalled: modelInventory === null || !settings?.active_model ? null : modelInventory.models.find((m) => m.id === settings?.active_model)?.installed === true,
    device: savedDevice,
    modelInstalled: modelInventory === null ? null : savedModel?.installed === true,
    download: runningDownload(modelInventory),
    activeModelDownload: settings?.active_model ? runningDownload(modelInventory, settings.active_model) : null,
    summaryEnabled: llm?.summary.enabled === true,
    providerName: llm ? providerName(llm.summary.provider) : "",
    hasProvider: llm === null ? null : llm.providers.length > 0,
    keep: savedKeep,
    recordingsPolicy: retention?.recordings ?? null,
  });
  $: chips = [
    { name: "Transcribe", step: status.transcribe, target: () => transcriptionSettings },
    { name: "Summarise", step: status.summarise, target: () => summarySettings },
    { name: "Publish", step: status.publish, target: () => publicationSettings },
    { name: "Keep on server", step: status.keep, target: () => keepSettings },
  ] satisfies { name: string; step: StepStatus; target: () => HTMLElement }[];

  // Everything after the transcript can be changed once transcription is
  // SAVED on, like the strip: turning the switch on in the draft says to save
  // first.
  $: savedTranscriptionOn = settings?.transcription_enabled === true;
  // An older save publishes transcript-only meetings with no transcript.
  // Saving with transcription off publishes the audio, so it says so.
  $: savedTranscriptOnlyWithout = !savedTranscriptionOn && savedMeetingFormat === "json";
  // Without a transcript the meeting is published as audio, whatever the
  // format select last said.
  $: publishedFormat = transcriptionEnabled ? meetingFormat : "opus" as const;
  // "the GPU (CUDA)", "the CPU", or "the CPU (no GPU found)": the end of the
  // sentence that names the model in use.
  $: runsOnLine = savedDevice === "cuda"
    ? `the ${deviceLabel("cuda")}`
    : settings?.detected_gpu ? "the CPU" : "the CPU (no GPU found)";
  $: inUseModel = modelInventory?.models.find((m) => m.id === activeModel);
  // The model the picked quality runs, which the switch turns on.
  $: qualityModel = modelInventory?.models.find((m) => m.id === speechModelFor(quality, selectedDevice));

  // How long kept media stays, from the Recordings policy: "for 30 days",
  // "kept forever", or "" when retention could not be read.
  $: keptSpan = retentionSpan(retention?.recordings);
  $: keptFor = keptSpan === null ? "" : keptSpan === "forever" ? "kept forever" : `for ${keptSpan}`;

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
    (sourceRetention !== savedSourceRetention || retainVideo !== savedRetainVideo || JSON.stringify([transcriptionEnabled,activeModel,activeRevision]) !== savedTranscription ||
      meetingFormat !== savedMeetingFormat ||
      quality !== savedQuality ||
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
  </header>

<!-- The readiness checks offer a "Set up storage" action that scrolls here,
     which is what the removed Setup page's own anchor used to do. Keeping the
     id on a wrapper rather than inside RecordingAccessPanel leaves that
     component untouched and keeps the cross-link a property of the layout that
     places the two sections, which is where it belongs. -->
<div id="recording-storage">
  <DirectShareAccessPanel {operatorClient} />
</div>

  {#if loadError}
    <div class="err-box" role="alert">{loadError}</div>
  {:else if loading}
    <p class="op-state">Loading settings…</p>
  {:else if !settings}
    <p class="op-state">Settings aren't available.</p>
  {:else}
    <div class="pipe-body">
      <section class="op-tint p-4 grid gap-4 pipe-card" aria-labelledby="recording-publication-heading">
        <div class="strip-head">
          <h2 id="recording-publication-heading" class="set-row-name op-card-title">After each meeting</h2>
          <span class="strip-current" title="The saved settings new recordings get">Current</span>
        </div>
        <!-- What new recordings get with the SAVED settings, one chip per step;
             chip N jumps to question N below. -->
        <ul class="pipe-strip" aria-label="Current settings">
          {#each chips as chip (chip.name)}
            <li>
              <button type="button" class="strip-chip tone-{chip.step.tone}" on:click={() => focusSection(chip.target())}>
                <span class="chip-box">
                  <span class="chip-name">{chip.name}</span>
                  <span class="chip-state">{chip.step.state}</span>
                </span>
                {#if chip.step.reason}
                  <span class="chip-note chip-reason">{chip.step.reason}</span>
                {:else if chip.step.badge}
                  <span class="chip-note">{chip.step.badge}</span>
                {/if}
              </button>
            </li>
          {/each}
        </ul>

        <!-- 1. Whether a transcript is made decides the questions after it: a
             summary needs one, and so does publishing a transcript. The switch
             sits in the heading; turning it on selects the ready model for the
             chosen quality. -->
        <section class="pub-q scroll-mt-4" aria-label="Transcription settings" tabindex="-1" bind:this={transcriptionSettings}>
          <div class="q-head">
            <span class="q-num" aria-hidden="true">1</span>
            <h3 class="q-title">Transcript</h3>
            <input
              type="checkbox"
              class="op-switch"
              aria-label="Transcribe every recording"
              disabled={!transcriptionEnabled && !readyModel}
              checked={transcriptionEnabled}
              on:change={(event) => toggleTranscription(event.currentTarget.checked)}
            />
          </div>
          {#if transcriptionEnabled && !savedTranscriptionOn}
            <p class="set-row-sub">Save to set up the summary and what participants receive.</p>
          {/if}
          {#if !transcriptionEnabled && !readyModel}
            <!-- The switch needs the picked quality's model installed AND checked
                 on this build; say which of the two is missing. -->
            <p class="set-row-sub">{qualityModel?.installed ? "Check the model below to turn it on." : "Download the model below to turn it on."}</p>
          {/if}
          {#if runsOnGPU}
            <p class="set-row-sub">On the GPU, every quality setting uses the same full-precision model, so this only changes transcription on the CPU.</p>
          {/if}
          <!-- What transcribes new recordings, and where. Builds use the saved
               model, not the quality picked below, so this names it. The
               device is auto-selected; on a host with no usable GPU that is the
               CPU, said out loud (D-702). The facts and the override open under
               "Change". -->
          <div class="in-use pipe-gap" class:in-use-warn={transcriptionEnabled && inUseModel && !inUseModel.installed}>
            <!-- One sentence: what transcribes and where. Installed but not yet
                 runtime-checked is normal after any new build (the operator
                 checks it before transcribing); only missing files mean
                 recordings go without a transcript. -->
            <p class="in-use-text" role="status">
              {#if transcriptionEnabled && inUseModel && !inUseModel.installed}
                <TriangleAlert size={14} class="in-use-warn-icon" aria-hidden="true" />
                <span><strong>{inUseModel.name}</strong> isn't installed, so recordings get no transcript.</span>
              {:else if transcriptionEnabled && inUseModel}
                <span>Transcribing with <strong>{inUseModel.name}</strong> on {runsOnLine}.</span>
              {:else}
                <span>Transcription runs on {runsOnLine}.</span>
              {/if}
            </p>
            <button type="button" class="in-use-toggle" aria-expanded={hardwareOpen} aria-controls="hardware-details" on:click={() => (hardwareOpen = !hardwareOpen)}>
              Hardware
              <ChevronDown size={14} class={hardwareOpen ? "in-use-chevron open" : "in-use-chevron"} aria-hidden="true" />
            </button>
          </div>
          {#if hardwareOpen}
            <!-- One box: what the operator found, the device choice, and one tip.
                 What runs and where is already said in the line above. -->
            <div id="hardware-details" class="hw-details">
              <dl class="hw-facts">
                <dt>GPU</dt>
                <dd>{settings.detected_gpu ? "Found" : "None found"}</dd>
                <dt>CPU cores</dt>
                <dd>{settings.cores}</dd>
                {#if settings.effective.min_free_memory_mb > 0}
                  <dt>Memory</dt>
                  <dd>Each recording waits for {formatMemory(settings.effective.min_free_memory_mb)} free</dd>
                {/if}
                <dt><label for="stt-device">Device</label></dt>
                <dd class="hw-device">
                  <span class="op-select hw-device-select">
                    <select id="stt-device" bind:value={deviceOverride} class="op-input">
                      <option value="">Auto</option>
                      <option value="cpu">CPU</option>
                      <option value="cuda">GPU (CUDA)</option>
                    </select>
                    <ChevronDown size={14} class="op-select-chevron" aria-hidden="true" />
                  </span>
                  <span class="hw-help">Auto uses the GPU when there is one. Change it only if the hardware above is wrong.</span>
                </dd>
              </dl>
              {#if deviceOverride === "cuda" && !settings.detected_gpu}
                <p class="hw-note pipe-warn" role="status">No GPU was found, so recordings won't be processed on this setting.</p>
              {:else if !settings.detected_gpu}
                <p class="hw-note">For faster transcription, run Cassini's CUDA image on a server with a GPU.</p>
              {:else if settings.effective.note}
                <p class="hw-note">{settings.effective.note}</p>
              {/if}
            </div>
          {/if}
          <p id="stt-quality-heading" class="op-field-label pipe-gap">Quality</p>
          <div class="q-list" role="radiogroup" aria-labelledby="stt-quality-heading">
            {#each QUALITY_OPTIONS as option (option.value)}
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
          {#if !(transcriptionEnabled && inUseModel?.installed && inUseModel.id === speechModelFor(quality, selectedDevice))}
            <p class="model-heading pipe-gap">{QUALITY_OPTIONS.find((o) => o.value === quality)?.label ?? "This quality"} model</p>
          {/if}
          <SpeechModels client={operatorClient} {quality} device={selectedDevice} enabled={transcriptionEnabled} {activeModel} {activeRevision}
            bind:readyModel bind:inventory={modelInventory}
            on:select={(event) => { activeModel = event.detail.id; activeRevision = event.detail.revision; transcriptionEnabled = true; }} />
          {#if !savedTranscriptionOn}
            {#if savedTranscriptOnlyWithout}
              <p class="set-row-sub pipe-warn pipe-gap" role="status">Saved settings publish transcript-only meetings with no transcript. Save to publish the audio instead.</p>
            {/if}
            <p class="q-callout pipe-gap">
              <Info size={14} class="q-callout-icon" aria-hidden="true" />
              <span>Transcription needs to be on to edit the settings below.</span>
            </p>
          {/if}
        </section>

        <!-- 2–4 need a SAVED transcript, like the strip: until transcription
             is saved on they are shown dimmed and cannot be changed, and the
             chips still jump to them. -->
        <!-- 2. Whether each meeting is summarised, and with which provider. The
             switch belongs to the heading: what is turned on is the step. -->
          <section class="pub-q scroll-mt-4" class:off={!savedTranscriptionOn} aria-labelledby="summary-heading" tabindex="-1" bind:this={summarySettings}>
            <div class="q-head">
              <span class="q-num" aria-hidden="true">2</span>
              <h3 id="summary-heading" class="q-title" class:off={hasProvider === false}>Summary</h3>
              <input
                type="checkbox"
                class="op-switch"
                aria-label="Summarise every meeting"
                disabled={hasProvider !== true || !savedTranscriptionOn}
                checked={summary.enabled && hasProvider === true}
                on:change={(event) => toggleSummary(event.currentTarget.checked)}
              />
            </div>
            {#if llmError}
              <p class="set-row-sub pipe-warn">
                Couldn't load the AI provider settings, so this step can't be shown or changed:
                {llmError}
              </p>
            {:else if hasProvider === false}
              <p class="set-row-sub">Needs a provider. <button type="button" class="link" disabled={!savedTranscriptionOn} on:click={() => dispatch("openProviders")}>Add one</button></p>
            {:else if hasProvider === true}
              <p class="set-row-sub">Sends each transcript to the provider and writes the summary shown on the meeting.</p>
              {#if savedTranscriptionOn && !transcriptionEnabled}
                <p class="set-row-sub">A summary needs a transcript. While transcription is off, recordings get no summary.</p>
              {/if}
              {#if summary.enabled}
              <fieldset class="step-config" disabled={!savedTranscriptionOn || saving}>
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
              </fieldset>
              {/if}
            {/if}
          </section>

        <!-- 3. What participants receive. -->
          <section class="pub-q scroll-mt-4" class:off={!savedTranscriptionOn} aria-labelledby="publication-heading" tabindex="-1" bind:this={publicationSettings}>
            <div class="q-head">
              <span class="q-num" aria-hidden="true">3</span>
              <h3 id="publication-heading" class="q-title">What participants receive</h3>
            </div>
            <div class="q-row">
              <select id="meeting-format" class="op-input q-select" aria-label="Published meeting" value={meetingFormat} on:change={(event) => selectMeetingFormat(event.currentTarget.value === "json" ? "json" : "opus")} disabled={saving || !savedTranscriptionOn} aria-describedby="publication-help">
                <option value="opus">Audio and transcript (.opus)</option>
                <option value="json">Transcript only (.json)</option>
              </select>
              <p id="publication-help" class="set-row-sub">Camera video is never published.</p>
            </div>
            {#if meetingFormat === "json" && !transcriptionEnabled}
              <p class="set-row-sub pipe-warn">Transcription is off. Turn it on above to include a transcript.</p>
            {/if}
          </section>

        <!-- 4. What stays on the server. Independent of transcription: audio
             only versus audio and video is a choice either way. -->
        <fieldset class="pub-q grid gap-3 scroll-mt-4" class:off={!savedTranscriptionOn} disabled={saving || !savedTranscriptionOn} tabindex="-1" bind:this={keepSettings}>
          <legend class="q-head">
            <span class="q-num" aria-hidden="true">4</span>
            <span class="q-title">What stays on this server</span>
          </legend>
          <label class="flex items-start gap-3 text-sm" class:keep-unavailable={publishedFormat === "opus"}>
            <input class="radio radio-primary radio-sm" type="radio" name="recording-media" value="nothing" aria-label="Nothing" checked={recordingMedia === "nothing"} disabled={publishedFormat === "opus"} on:change={() => selectRecordingMedia("nothing")} aria-describedby="keep-nothing-help" />
            <span><strong>Nothing.</strong> <span id="keep-nothing-help">The recording is deleted after publishing and cannot be reprocessed.</span>
              {#if publishedFormat === "opus"}<span class="set-row-sub block">Unavailable while audio is published.</span>{/if}</span>
          </label>
          <label class="flex items-start gap-3 text-sm">
            <input class="radio radio-primary radio-sm" type="radio" name="recording-media" value="audio-video" aria-label="Audio and video" checked={recordingMedia === "audio-video"} on:change={() => selectRecordingMedia("audio-video")} aria-describedby="keep-video-help" />
            <span>{#if keptFor}<strong>Audio and video</strong>, {keptFor}.{:else}<strong>Audio and video.</strong>{/if} <span id="keep-video-help">Uses the most space.</span></span>
          </label>
          <label class="flex items-start gap-3 text-sm">
            <input class="radio radio-primary radio-sm" type="radio" name="recording-media" value="audio-only" aria-label="Audio only" checked={recordingMedia === "audio-only"} on:change={() => selectRecordingMedia("audio-only")} />
            <span>{#if keptFor}<strong>Audio only</strong>, {keptFor}.{:else}<strong>Audio only.</strong>{/if}</span>
          </label>
          {#if recordingMedia === "nothing"}
            {#if !transcriptionEnabled || !activeModel || !activeRevision}
              <p class="set-row-sub" role="status"><strong>Transcription must be on to keep nothing.</strong> <button type="button" class="link" on:click={focusTranscriptionSettings}>Set up transcription</button></p>
            {/if}
          {:else if keptSpan === "forever"}
            <p class="set-row-sub">Kept forever: change this in <button type="button" class="link" on:click={() => dispatch("openStorage")}>Storage</button>.</p>
          {:else if keptSpan}
            <p class="set-row-sub">The {keptSpan} {keptSpan === "1 day" ? "comes" : "come"} from <button type="button" class="link" on:click={() => dispatch("openStorage")}>Storage</button>.</p>
          {:else}
            <p class="set-row-sub">How long kept media stays is set in <button type="button" class="link" on:click={() => dispatch("openStorage")}>Storage</button>.</p>
          {/if}
        </fieldset>
        <p class="set-row-sub">Applies to new recordings after saving.</p>
      </section>


      <VoiceSeparationModel client={operatorClient} inventory={modelInventory} />

      <!-- Both fields steer how names come out: one when transcribing, one when
           searching what was transcribed. -->
      <section class="op-tint pipe-step hints-card" aria-labelledby="transcript-hints-heading">
        <h2 id="transcript-hints-heading" class="set-row-name op-card-title">Transcript hints</h2>
        <div class="set-row-main pipe-text-step">
          <div>
            <h3 class="q-title">Vocabulary</h3>
            <p class="set-row-sub">Saved names and preferred spellings for transcription hints.</p>
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
            <li>Participant names are added automatically on models that support hints.</li>
            <li>Hints guide recognition toward preferred spellings; they do not guarantee the correct word. When hints cannot be used, the recording notes why.</li>
          </ul>
        </div>
        <div class="set-row-main pipe-text-step hints-part">
          <div>
            <h3 class="q-title">Search spellings</h3>
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
            <li>Use these spellings when the transcriber mishears a name.</li>
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
          <button class="sb-btn sb-light" type="button" disabled={saving || !isDirty || (sourceRetention === "delete-after-processing" && (!transcriptionEnabled || !activeModel || !activeRevision))} on:click={handleSave}>
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
  .strip-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 12px;
  }
  .strip-current {
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: color-mix(in oklch, var(--color-base-content) 45%, transparent);
  }
  .pipe-strip {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 8px;
    margin: -6px 0 0;
    padding: 0;
    list-style: none;
  }
  /* The strip follows the card's width, not the window's: inside Nextcloud
     the card is much narrower than the viewport. */
  .pipe-card {
    container-type: inline-size;
  }
  .pipe-strip li {
    display: flex;
  }
  .strip-chip {
    display: grid;
    grid-template-rows: 1fr auto;
    align-content: start;
    gap: 4px;
    justify-items: start;
    width: 100%;
    padding: 0;
    text-align: left;
    cursor: pointer;
    background: none;
    border: 0;
  }
  .chip-box {
    display: grid;
    align-content: start;
    gap: 1px;
    width: 100%;
    padding: 7px 10px;
    border: 1px solid var(--op-inset-border);
    border-radius: var(--radius-box, 0.5rem);
  }
  .strip-chip:hover .chip-box {
    border-color: color-mix(in oklch, var(--color-base-content) 30%, var(--color-base-200));
  }
  .strip-chip:focus-visible {
    outline: none;
  }
  .strip-chip:focus-visible .chip-box {
    outline: 2px solid var(--color-primary);
    outline-offset: 2px;
  }
  .chip-name {
    font-size: 11.5px;
    font-weight: 600;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  .chip-state {
    font-size: 13px;
    font-weight: 600;
    overflow-wrap: anywhere;
  }
  .tone-on .chip-box {
    background-color: color-mix(in srgb, var(--color-primary) 14%, var(--color-base-100));
  }
  .tone-off .chip-state {
    font-weight: 500;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  .tone-busy .chip-box {
    border-color: var(--color-primary);
  }
  .tone-amber .chip-box {
    background-color: color-mix(in srgb, var(--color-warning) 14%, var(--color-base-100));
    border-color: var(--color-warning);
  }
  .chip-note {
    padding: 2px 6px;
    font-size: 11px;
    font-weight: 500;
    line-height: 1.4;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
    background-color: var(--op-inset);
    border: 1px solid var(--op-inset-border);
    border-radius: var(--radius-selector, 0.25rem);
  }
  .chip-reason {
    color: var(--color-warning);
    background-color: color-mix(in srgb, var(--color-warning) 14%, var(--color-base-100));
    border-color: var(--color-warning);
  }
  @container (max-width: 640px) {
    .pipe-strip {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
  }
  /* Narrow: one row per step, name left and state right. */
  @container (max-width: 420px) {
    .pipe-strip {
      grid-template-columns: 1fr;
      gap: 0;
      border: 1px solid var(--op-inset-border);
      border-radius: var(--radius-box, 0.5rem);
      overflow: hidden;
    }
    .pipe-strip li + li {
      border-top: 1px solid var(--op-inset-border);
    }
    .strip-chip {
      grid-template-columns: auto 1fr;
      align-items: baseline;
      gap: 2px 12px;
      padding: 8px 12px;
    }
    .chip-box {
      display: contents;
    }
    .chip-state {
      text-align: right;
      justify-self: end;
    }
    .chip-note {
      grid-column: 2;
      justify-self: end;
    }
    .strip-chip.tone-amber {
      background-color: color-mix(in srgb, var(--color-warning) 14%, var(--color-base-100));
    }
    .strip-chip.tone-busy .chip-state {
      color: var(--color-primary);
    }
    .strip-chip:focus-visible {
      outline: 2px solid var(--color-primary);
      outline-offset: -2px;
    }
  }
  .keep-unavailable {
    color: color-mix(in oklch, var(--color-base-content) 60%, transparent);
  }
  .pub-q {
    display: grid;
    gap: 4px;
    min-width: 0;
    margin: 0;
    padding: 16px 0 0;
    border: 0;
    border-top: 1px solid var(--op-border, color-mix(in oklch, var(--color-base-content) 12%, transparent));
  }
  .pub-q:focus {
    outline: none;
  }
  .pub-q:focus-visible {
    outline: 2px solid var(--color-primary);
    outline-offset: 4px;
  }
  .q-head {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 0;
    margin-bottom: 4px;
  }
  /* A fieldset's legend sits on the border unless it floats into the box. */
  fieldset.pub-q > legend.q-head {
    float: left;
    width: 100%;
  }
  fieldset.pub-q > legend.q-head + * {
    clear: both;
  }
  .q-num {
    display: inline-grid;
    place-items: center;
    flex: none;
    width: 18px;
    height: 18px;
    font-size: 11px;
    font-weight: 600;
    line-height: 1;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
    border: 1px solid color-mix(in oklch, var(--color-base-content) 30%, transparent);
    border-radius: 50%;
  }
  .q-title {
    margin: 0;
    font-size: 14px;
    font-weight: 600;
    line-height: 1.4;
  }
  .q-title.off {
    color: color-mix(in oklch, var(--color-base-content) 45%, transparent);
  }
  .q-row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 12px;
  }
  .q-select {
    width: auto;
    max-width: 100%;
  }
  .hw-details {
    display: grid;
    gap: 10px;
    margin-top: 6px;
    padding: 12px 14px;
    font-size: 12.5px;
    background-color: var(--op-inset);
    border: 1px solid var(--op-inset-border);
    border-radius: var(--radius-box, 0.5rem);
  }
  .hw-facts {
    display: grid;
    grid-template-columns: max-content minmax(0, 1fr);
    align-items: baseline;
    gap: 8px 20px;
    margin: 0;
  }
  .hw-facts dt {
    font-weight: 600;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  .hw-facts dd {
    margin: 0;
  }
  .hw-device {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 12px;
  }
  .hw-device-select {
    display: block;
    width: 10rem;
  }
  .hw-help,
  .hw-note {
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  .hw-note {
    margin: 0;
    padding-top: 10px;
    border-top: 1px solid var(--op-inset-border);
  }
  .pipe-body {
    display: grid;
    gap: calc(var(--op-x, 20px) + 8px);
    margin-top: calc(var(--op-x, 20px) + 8px - 16px);
  }
  .pipe-warn {
    color: var(--color-warning);
  }
  .set-row-sub {
    display: block;
    text-wrap: pretty;
  }

  .pipe-step {
    display: flex;
    align-items: flex-start;
    gap: 14px;
    padding: 14px 16px;
  }
  .pipe-gap {
    margin-top: 8px;
  }
  .in-use {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px 12px;
    padding: 6px 6px 6px 10px;
    font-size: 12.5px;
    line-height: 1.35;
    border-radius: var(--radius-box, 0.5rem);
    background: var(--op-inset, color-mix(in oklch, var(--color-base-content) 5%, transparent));
  }
  .in-use-text {
    display: flex;
    flex: 1 1 16rem;
    align-items: center;
    gap: 6px;
    margin: 0;
  }
  /* Nextcloud styles every button with a min-height and padding. */
  .in-use-toggle {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    min-height: 0;
    margin: 0 0 0 auto;
    padding: 3px 8px;
    font-size: 12px;
    font-weight: 600;
    color: inherit;
    background: none;
    border: 1px solid var(--op-inset-border, color-mix(in oklch, var(--color-base-content) 15%, transparent));
    border-radius: var(--radius-field, 0.375rem);
    cursor: pointer;
  }
  .in-use-toggle:hover {
    border-color: color-mix(in oklch, var(--color-base-content) 30%, var(--color-base-200));
  }
  .in-use-toggle :global(.in-use-chevron) {
    transition: transform 0.15s;
  }
  .in-use-toggle :global(.in-use-chevron.open) {
    transform: rotate(180deg);
  }
  .in-use-warn {
    background-color: color-mix(in srgb, var(--color-warning) 14%, var(--color-base-100));
    border: 1px solid var(--color-warning);
  }
  .in-use-warn :global(.in-use-warn-icon) {
    align-self: center;
    color: var(--color-warning);
  }
  .model-heading {
    margin-bottom: -4px;
    font-size: 11.5px;
    font-weight: 600;
    color: color-mix(in oklch, var(--color-base-content) 60%, transparent);
  }
  .q-callout {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    margin-bottom: 0;
    padding: 8px 10px;
    font-size: 12.5px;
    line-height: 1.5;
    color: color-mix(in oklch, var(--color-base-content) 75%, transparent);
    background-color: var(--op-inset);
    border: 1px solid var(--op-inset-border);
    border-radius: var(--radius-box, 0.5rem);
  }
  .q-callout :global(.q-callout-icon) {
    flex: none;
    margin-top: 3px;
  }
  .step-config {
    min-width: 0;
    padding: 0;
    border: 0;
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
  .hints-card {
    flex-direction: column;
    align-items: stretch;
  }
  .hints-part {
    padding-top: 14px;
    border-top: 1px solid var(--op-border, color-mix(in oklch, var(--color-base-content) 12%, transparent));
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
