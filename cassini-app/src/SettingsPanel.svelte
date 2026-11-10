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
  import { richText, slotMark, type RichPart } from "cassini-viewer/ui/richText";
  import { fly } from "svelte/transition";
  import { cancelLeave, confirmLeave, guardLeave, leavePrompt, unsavedChanges } from "./operator/unsaved";
  import { ChevronDown, TriangleAlert } from "@lucide/svelte";
  import { formatCount, LL, numberFormat } from "cassini-viewer/i18n";
  import { OperatorClient, OperatorHttpError, runnableWorkflows } from "./operator/client";
  import SpeechModelCell from "./SpeechModelCell.svelte";
  import PublishedFormatSelect from "./PublishedFormatSelect.svelte";
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
  import { inUseModelState, pipelineStatus, retentionSpan, runningDownload, speechModelFor, type StepStatus } from "./operator/pipelineStatus";
  import { summaryStepLead } from "./operator/summaryAction";
  import { jobRunning, modelJob } from "./speechModels";
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
  // A step to land on rather than the panel's top: another panel's link to the
  // summary step (a template refused deletion because it runs there).
  export let focus: "" | "summary" = "";

  const dispatch = createEventDispatcher<{ openProviders: void; openTemplates: void; openStorage: void }>();

  interface QualityOption {
    value: SettingsQuality;
    label: string;
    description: string;
  }
  // A quality tier as a row of the Transcript step: its model on the chosen
  // device, whether it is the one in use, and whether it can be picked.
  interface TierRow {
    option: QualityOption;
    modelId: string;
    model: SpeechModel | undefined;
    running: boolean;
    selected: boolean;
    selectable: boolean;
  }

  $: t = $LL.app.settings.pipeline;

  let QUALITY_OPTIONS: QualityOption[];
  $: QUALITY_OPTIONS = [
    {
      value: "fast",
      label: $LL.app.quality.fast(),
      description: t.quality_fast_description(),
    },
    {
      value: "balanced",
      label: $LL.app.quality.balanced(),
      description: t.quality_balanced_description(),
    },
    {
      value: "best",
      label: $LL.app.quality.best(),
      description: t.quality_best_description(),
    },
  ];

  // Sentences with marked-up parts in them (a model name in bold, a command
  // as code, a link to Storage), translated whole and cut where each part goes
  // (cassini-viewer/ui/richText).
  type Slot = "model" | "media" | "storage" | "pack" | "import";
  const slot = (name: Slot): string => slotMark(name);
  type Segment = RichPart<Slot>;
  const segments = (sentence: string): Segment[] => richText<Slot>(sentence);

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

  // The speech model inventory, polled here: every tier row of the Transcript
  // step reads it, and so does the voice separation card below. Re-read from
  // scratch when the device changes, since the inventory is per device.
  let modelInventory: SpeechModelInventory | null = null;
  let modelInventoryError = "";
  let inventoryDevice = "";
  let inventoryGeneration = 0;
  let inventoryMounted = false;
  let refreshingInventory = false;
  // A model whose download or check was started from this page while
  // transcription is off. Downloading a model is the clearest statement of
  // intent there is, so the moment it is ready it is picked and transcription
  // turns on in the draft, for Save to apply.
  let pendingSelect = "";

  async function refreshInventory() {
    if (!operatorClient || !inventoryMounted || refreshingInventory) return;
    refreshingInventory = true;
    const generation = inventoryGeneration;
    const target = selectedDevice;
    try {
      const result = await operatorClient.getSpeechModels(target);
      if (!inventoryMounted || generation !== inventoryGeneration || target !== selectedDevice) return;
      modelInventory = result;
      modelInventoryError = "";
      pickWhenReady(result, target);
    } catch (e) {
      if (inventoryMounted && generation === inventoryGeneration) modelInventoryError = t.inventory_refresh_failed({ error: e instanceof Error ? e.message : String(e) });
    } finally {
      refreshingInventory = false;
    }
  }
  onMount(() => {
    inventoryMounted = true;
    inventoryDevice = selectedDevice;
    void refreshInventory();
    const timer = setInterval(() => void refreshInventory(), 2000);
    return () => { inventoryMounted = false; inventoryGeneration++; clearInterval(timer); };
  });
  $: if (inventoryMounted && inventoryDevice !== selectedDevice) {
    inventoryDevice = selectedDevice;
    inventoryGeneration++;
    modelInventory = null;
    void refreshInventory();
  }
  function pickWhenReady(inventory: SpeechModelInventory, device: string) {
    if (pendingSelect === "" || transcriptionEnabled) return;
    const ready = inventory.models.find((m) => m.id === pendingSelect && m.ready);
    if (!ready) return;
    const tier = QUALITY_OPTIONS.find((option) => speechModelFor(option.value, device) === ready.id);
    if (tier) quality = tier.value;
    useModel(ready);
    pendingSelect = "";
  }
  function useModel(model: SpeechModel) {
    activeModel = model.id;
    activeRevision = model.revision;
    transcriptionEnabled = true;
  }
  // One list answers what transcribes recordings: Off, or a quality tier whose
  // model is ready. Picking a tier sets the tier, the model and the switch at
  // once; they were three controls that had to agree.
  function selectTier(row: TierRow) {
    if (!row.model?.ready) return;
    quality = row.option.value;
    pendingSelect = "";
    useModel(row.model);
  }
  function selectOff() {
    transcriptionEnabled = false;
  }
  let savedTranscription = "";
  let quality: SettingsQuality = "balanced";
  let deviceOverride = "";
  let searchAliasesText = "";
  let savedSearchAliasesText = "";

  let savedQuality: SettingsQuality = "balanced";
  let savedDeviceOverride = "";

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
    if (focus === "summary") {
      await tick();
      focusSection(summarySettings);
    }
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
    searchAliasesText = formatSearchAliases(next.search_aliases);
    savedQuality = next.quality;
    savedDeviceOverride = next.device_override;
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

  // formatMemory reads a size in MB back to an administrator. Gigabytes are
  // what a host is described in, so a four-digit MB number is the wrong unit.
  function formatMemory(mb: number): string {
    if (mb >= 1024) {
      return t.memory_gb({ size: numberFormat({ minimumFractionDigits: 1, maximumFractionDigits: 1 }).format(mb / 1024) });
    }
    return t.memory_mb({ size: numberFormat().format(mb) });
  }

  // The id the publish pipeline's summary step runs when nothing names one
  // (internal/insight/workflows: SummariseID). Named rather than derived from
  // list order, and it falls back to the first entry: the operator accepts any
  // id the registry resolves, so a build that stopped shipping this one opens
  // on a different template instead of on an error.
  const SHIPPED_SUMMARY_TEMPLATE = "summarise";
  // The publish pipeline reads the chosen template back (D-819): the operator
  // hands it to the build as CASSINI_SUMMARY_TEMPLATE, and the summary records
  // the id, version and hash of the template that wrote it.

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
    return runnableWorkflows(list).filter((workflow) => !workflowTakesQuestion(workflow));
  }

  $: summaryTemplates = summaryCandidates(workflows);

  // What the picker says of a configured template it cannot offer. A template
  // the registry lists but cannot run says why, in the registry's words; only
  // one it does not list at all is from another build.
  function unrunnableSummaryTemplateLabel(id: string, list: InsightWorkflow[]): string {
    const broken = list.find((workflow) => workflow.id === id && workflow.error);
    if (broken) {
      return t.template_cannot_run({ id, error: broken.error ?? "" });
    }
    if (list.some((workflow) => workflow.id === id && workflowTakesQuestion(workflow))) {
      return t.template_asks_question({ id });
    }
    return t.template_not_in_build({ id });
  }

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
      ? t.provider_default_model({ model })
      : t.no_provider_default();
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
    activeModelStandIn: !!modelInventory?.models.find((m) => m.id === settings?.active_model)?.stand_in,
    summaryEnabled: llm?.summary.enabled === true,
    providerName: llm ? providerName(llm.summary.provider) : "",
    hasProvider: llm === null ? null : llm.providers.length > 0,
    keep: savedKeep,
    recordingsPolicy: retention?.recordings ?? null,
  });
  $: chips = [
    { name: t.chip_transcribe(), step: status.transcribe, target: () => transcriptionSettings },
    { name: t.chip_summarise(), step: status.summarise, target: () => summarySettings },
    { name: t.chip_publish(), step: status.publish, target: () => publicationSettings },
    { name: t.chip_retention(), step: status.keep, target: () => keepSettings },
  ] satisfies { name: string; step: StepStatus; target: () => HTMLElement }[];

  // Keep the saved state for warnings; editable controls follow the draft.
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
    ? t.runs_on_gpu()
    : settings?.detected_gpu ? t.runs_on_cpu() : t.runs_on_cpu_no_gpu();
  $: inUseModel = modelInventory?.models.find((m) => m.id === activeModel);
  // While it is not installed: its own download, if one is running, and what
  // recordings get meanwhile (the model it replaced, a wait, or nothing).
  $: inUseDownload = inUseModel && !inUseModel.installed ? runningDownload(modelInventory, inUseModel.id) : null;
  $: inUseState = transcriptionEnabled && inUseModel ? inUseModelState(inUseModel, inUseDownload) : null;
  $: inUsePercent = inUseDownload?.percent ?? null;
  // The footer's one sentence, cut where the model's name goes (in bold).
  $: inUseLine = segments(
    inUseModel && inUseState === "missing"
      ? t.in_use_missing({ model: slot("model") })
      : inUseModel && inUseState === "stand-in"
        ? inUseDownload
          ? inUsePercent === null
            ? t.in_use_stand_in_downloading({ model: slot("model"), device: runsOnLine })
            : t.in_use_stand_in_downloading_percent({ model: slot("model"), device: runsOnLine, percent: inUsePercent })
          : inUseModel.installed
            ? t.in_use_stand_in_failed_check({ model: slot("model"), device: runsOnLine })
            : t.in_use_stand_in_missing({ model: slot("model"), device: runsOnLine })
        : inUseModel && inUseState === "downloading"
          ? inUsePercent === null
            ? t.in_use_downloading({ model: slot("model") })
            : t.in_use_downloading_percent({ model: slot("model"), percent: inUsePercent })
          : transcriptionEnabled && inUseModel
            ? t.in_use_transcribing({ model: slot("model"), device: runsOnLine })
            : t.in_use_runs_on({ device: runsOnLine }),
  );
  // The tier rows. On the GPU every tier runs the same model, so the rows
  // then carry no model of their own and one model row follows them.
  $: tierModelIds = QUALITY_OPTIONS.map((option) => speechModelFor(option.value, selectedDevice));
  $: tiersShareModel = new Set(tierModelIds).size === 1;
  $: sharedModelId = tiersShareModel ? tierModelIds[0] : "";
  $: tierRows = QUALITY_OPTIONS.map((option, index): TierRow => {
    const modelId = tierModelIds[index];
    const model = modelInventory?.models.find((m) => m.id === modelId);
    return {
      option,
      modelId,
      model,
      running: jobRunning(modelJob(modelInventory, model, selectedDevice)),
      selected: transcriptionEnabled && activeModel === modelId && (!tiersShareModel || quality === option.value),
      selectable: model?.ready === true,
    };
  });
  $: selectedTier = tierRows.find((row) => row.selected)?.option.label ?? "";
  $: pendingRow = pendingSelect === "" ? undefined : tierRows.find((row) => row.modelId === pendingSelect && row.running);
  // The line under the step's heading: what to do next while nothing
  // transcribes, then what is happening and whether it is saved.
  $: transcriptLead = !transcriptionEnabled
    ? pendingRow
      ? t.lead_downloading({ tier: pendingRow.option.label })
      : t.lead_off()
    : !savedTranscriptionOn
      ? t.lead_on_unsaved()
      : activeModel !== (settings?.active_model ?? "")
        ? selectedTier ? t.lead_switched({ tier: selectedTier }) : t.lead_switched_unnamed()
        : selectedTier
          ? t.lead_tier({ tier: selectedTier })
          : t.lead_on();

  // How long kept media stays, from the Recordings policy: "for 30 days",
  // "kept forever", or "" when retention could not be read.
  $: keptSpan = retentionSpan(retention?.recordings);
  // How many days the recordings policy keeps media, when it is a number of days.
  $: keptDays = keptSpan !== null && keptSpan !== "forever" ? (retention?.recordings.count ?? 0) : 0;
  // "{media}, for 30 days.": one sentence per option, with the option's name in bold.
  function keptLine(media: string, span: string | null, days: number): Segment[] {
    const m = slot("media");
    const sentence = span === null ? t.kept_plain({ media: m })
      : span === "forever" ? t.kept_forever({ media: m })
      : days === 1 ? t.kept_days_one({ media: m })
      : t.kept_days_other({ media: m, count: formatCount(days) });
    return segments(sentence).map((part) => (part.slot === "media" ? { ...part, text: media } : part));
  }
  $: keepVideoLine = keptLine($LL.app.keep.audio_video(), keptSpan, keptDays);
  $: keepAudioLine = keptLine($LL.app.keep.audio_only(), keptSpan, keptDays);
  $: keepNothingLine = segments(t.kept_plain({ media: slot("media") })).map((part) => (part.slot === "media" ? { ...part, text: $LL.app.keep.nothing() } : part));
  $: keepSource = segments(
    keptSpan === "forever" ? t.source_forever({ storage: slot("storage") })
      : keptSpan ? keptDays === 1 ? t.source_days_one({ storage: slot("storage") }) : t.source_days_other({ storage: slot("storage"), count: formatCount(keptDays) })
      : t.source_unset({ storage: slot("storage") }),
  );
  $: downloadsDisabled = segments(t.downloads_disabled({ pack: slot("pack"), import: slot("import") }));

  $: providers = llm?.providers ?? [];
  // The endpoint new recordings' summaries go to, so Settings can say whether
  // each call leaves this network (D-814).
  $: summaryProviderRow = providers.find((row) => row.id === summary.provider) ?? null;
  // Null while the LLM settings have not been read: three states, and the
  // locked card belongs to exactly one of them.
  $: hasProvider = llm === null ? null : providers.length > 0;
  $: summaryModels = modelsByProvider[summary.provider] ?? [];

  // Save each settings store only when its fields have changed.
  $: sttDirty =
    settings !== null &&
    (sourceRetention !== savedSourceRetention || retainVideo !== savedRetainVideo || JSON.stringify([transcriptionEnabled,activeModel,activeRevision]) !== savedTranscription ||
      meetingFormat !== savedMeetingFormat ||
      quality !== savedQuality ||
      deviceOverride !== savedDeviceOverride ||
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
        <h1>{t.heading()}</h1>
      </div>
      <p>{t.intro()}</p>
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
    <p class="op-state">{t.loading()}</p>
  {:else if !settings}
    <p class="op-state">{t.unavailable()}</p>
  {:else}
    <div class="pipe-body">
      <section class="op-tint p-4 grid gap-4 pipe-card" aria-labelledby="recording-publication-heading">
        <div class="strip-head">
          <h2 id="recording-publication-heading" class="set-row-name op-card-title">{t.after_meeting()}</h2>
          <span class="strip-current" title={t.current_title()}>{t.current()}</span>
        </div>
        <!-- What new recordings get with the SAVED settings, one chip per step;
             chip N carries the number of step N below and jumps to it. -->
        <ul class="pipe-strip" aria-label={t.strip_label()}>
          {#each chips as chip, index (chip.name)}
            <li>
              <button type="button" class="strip-chip tone-{chip.step.tone}" on:click={() => focusSection(chip.target())}>
                <span class="chip-num" aria-hidden="true">{index + 1}</span>
                <span class="chip-name">{chip.name}</span>
                <span class="chip-state">{chip.step.state}</span>
                {#if chip.step.reason}
                  <span class="chip-note chip-reason">{chip.step.reason}</span>
                {:else if chip.step.badge}
                  <span class="chip-note">{chip.step.badge}</span>
                {/if}
              </button>
            </li>
          {/each}
        </ul>

        <!-- The four steps, each an inset panel on a rail. The number sits on
             the rail; the panel is the focus target the chips jump to. -->
        <div class="pipe-steps">
          <!-- 1. Whether a transcript is made decides the steps after it: a
               summary needs one, and so does publishing a transcript. One list
               answers it: Off, or a quality tier whose model is ready. Each
               tier row carries its model, so the download that makes a tier
               usable sits on the tier itself. -->
          <div class="pipe-step-row">
            <span class="q-num" aria-hidden="true">1</span>
            <section class="pub-q scroll-mt-4" aria-label={t.transcription_label()} tabindex="-1" bind:this={transcriptionSettings}>
              <div class="q-head">
                <h3 class="q-title">{t.transcript_heading()}</h3>
              </div>
              <!-- What to do next, and then what is happening; it changes with
                   the draft and with the saved state. -->
              <p class="set-row-sub q-lead" role="status">{transcriptLead}</p>
              {#if runsOnGPU}
                <p class="set-row-sub">{t.gpu_same_model()}</p>
              {/if}
              {#if modelInventoryError}
                <p class="set-row-sub pipe-warn" role="status">{modelInventoryError}</p>
              {/if}
              <div class="q-list" role="radiogroup" aria-label={t.tiers_label()}>
                <div class="q-opt q-opt-off" class:selected={!transcriptionEnabled}>
                  <label class="q-pick">
                    <input type="radio" name="stt-model" class="q-radio" value="off" checked={!transcriptionEnabled} on:change={selectOff} />
                    <span class="q-main">
                      <span class="q-label">{t.off()}</span>
                      <span class="q-desc">{t.off_description()}</span>
                    </span>
                  </label>
                </div>
                {#each tierRows as row (row.option.value)}
                  <!-- A tier whose model is not ready cannot be picked, so its
                       text is greyed; its Download (or Check) button is the one
                       live control on the row. -->
                  <div class="q-opt" class:selected={row.selected} class:unavailable={!row.selectable}>
                    <label class="q-pick">
                      <input type="radio" name="stt-model" class="q-radio" value={row.option.value} checked={row.selected} disabled={!row.selectable} on:change={() => selectTier(row)} />
                      <span class="q-main">
                        <span class="q-label">{row.option.label}</span>
                        <span class="q-desc">{row.option.description}</span>
                      </span>
                    </label>
                    {#if !tiersShareModel}
                      <SpeechModelCell client={operatorClient} inventory={modelInventory} modelId={row.modelId} device={selectedDevice} selected={row.selected}
                        on:queued={(event) => (pendingSelect = event.detail.id)} on:refresh={() => void refreshInventory()} />
                    {/if}
                  </div>
                {/each}
                {#if tiersShareModel}
                  <!-- On the GPU every tier runs the same model, so it is one
                       row under the tiers rather than the same row three times. -->
                  <div class="q-opt q-opt-model">
                    <SpeechModelCell client={operatorClient} inventory={modelInventory} modelId={sharedModelId} device={selectedDevice} selected={transcriptionEnabled && activeModel === sharedModelId}
                      on:queued={(event) => (pendingSelect = event.detail.id)} on:refresh={() => void refreshInventory()} />
                  </div>
                {/if}
              </div>
              {#if modelInventory === null && modelInventoryError === ""}
                <p class="set-row-sub">{t.loading_inventory()}</p>
              {:else if modelInventory && !modelInventory.downloads_allowed}
                <p class="set-row-sub">{#each downloadsDisabled as part}{#if part.slot === "pack"}<code>cassini models pack</code>{:else if part.slot === "import"}<code>cassini models import</code>{:else}{part.text}{/if}{/each}</p>
              {/if}
              {#if !transcriptionEnabled && savedTranscriptOnlyWithout}
                <p class="set-row-sub pipe-warn" role="status">{t.transcript_only_without()}</p>
              {/if}
              <!-- What transcribes new recordings, and where: the step's
                   footer. Builds use the saved model, not the tier picked
                   above, so this names it. The device is auto-selected; on a
                   host with no usable GPU that is the CPU, said out loud
                   (D-702). The facts and the override open under "Hardware". -->
              <div class="in-use q-foot" class:in-use-warn={inUseState === "missing"}>
                <!-- One sentence: what transcribes and where. Installed but not yet
                     runtime-checked is normal after any new build (the operator
                     checks it before transcribing). Missing files mean recordings
                     go without a transcript, unless the model it replaced is still
                     installed (it transcribes them meanwhile) or its download is
                     running (they wait for it). That model also transcribes for
                     installed files whose runtime check failed: the operator
                     names it (stand_in) on an installed model only then, not
                     while the check is still to run (builds run it first). -->
                <p class="in-use-text" role="status">
                  {#if inUseModel && inUseState === "missing"}
                    <TriangleAlert size={14} class="in-use-warn-icon" aria-hidden="true" />
                  {/if}
                  <span>{#each inUseLine as part}{#if part.slot === "model"}<strong>{inUseModel?.name}</strong>{:else}{part.text}{/if}{/each}</span>
                </p>
                <button type="button" class="in-use-toggle" aria-expanded={hardwareOpen} aria-controls="hardware-details" on:click={() => (hardwareOpen = !hardwareOpen)}>
                  {t.hardware()}
                  <ChevronDown size={14} class={hardwareOpen ? "in-use-chevron open" : "in-use-chevron"} aria-hidden="true" />
                </button>
              </div>
              {#if hardwareOpen}
                <!-- One box: what the operator found, the device choice, and one tip.
                     What runs and where is already said in the line above. -->
                <div id="hardware-details" class="hw-details">
                  <dl class="hw-facts">
                    <dt>{t.hw_gpu()}</dt>
                    <dd>{settings.detected_gpu ? t.hw_gpu_found() : t.hw_gpu_none()}</dd>
                    <dt>{t.hw_cores()}</dt>
                    <dd>{settings.cores}</dd>
                    {#if settings.effective.min_free_memory_mb > 0}
                      <dt>{t.hw_memory()}</dt>
                      <dd>{t.hw_memory_wait({ size: formatMemory(settings.effective.min_free_memory_mb) })}</dd>
                    {/if}
                    <dt><label for="stt-device">{t.hw_device()}</label></dt>
                    <dd class="hw-device">
                      <span class="op-select hw-device-select">
                        <select id="stt-device" bind:value={deviceOverride} class="op-input">
                          <option value="">{t.hw_device_auto()}</option>
                          <option value="cpu">CPU</option>
                          <option value="cuda">GPU (CUDA)</option>
                        </select>
                        <ChevronDown size={14} class="op-select-chevron" aria-hidden="true" />
                      </span>
                      <span class="hw-help">{t.hw_device_help()}</span>
                    </dd>
                  </dl>
                  {#if deviceOverride === "cuda" && !settings.detected_gpu}
                    <p class="hw-note pipe-warn" role="status">{t.hw_no_gpu_forced()}</p>
                  {:else if !settings.detected_gpu}
                    <p class="hw-note">{t.hw_cuda_tip()}</p>
                  {:else if settings.effective.note}
                    <p class="hw-note">{settings.effective.note}</p>
                  {/if}
                </div>
              {/if}
            </section>
          </div>

          <!-- 2–4 follow the draft transcription choice so the whole
               configuration can be edited before saving. The strip still shows
               saved settings. -->
          <!-- 2. Whether each meeting is summarised, and with which provider.
               While there is no transcript the step is inert: greyed, its
               switch out of the way, and an amber pill saying why. -->
          <div class="pipe-step-row">
            <span class="q-num" aria-hidden="true">2</span>
            <section class="pub-q scroll-mt-4" class:off={!transcriptionEnabled} aria-labelledby="summary-heading" tabindex="-1" bind:this={summarySettings}>
              <div class="q-head">
                <h3 id="summary-heading" class="q-title" class:off={hasProvider === false}>{t.summary_heading()}</h3>
                {#if !transcriptionEnabled}
                  <span class="q-pill q-pill-warn">{t.needs_transcript()}</span>
                {:else}
                  <input
                    type="checkbox"
                    class="op-switch"
                    aria-label={t.summarise_switch()}
                    disabled={hasProvider !== true}
                    checked={summary.enabled && hasProvider === true}
                    on:change={(event) => toggleSummary(event.currentTarget.checked)}
                  />
                {/if}
              </div>
              {#if llmError}
                <p class="set-row-sub pipe-warn">{t.llm_load_failed({ error: llmError })}</p>
              {:else if hasProvider === false}
                <p class="set-row-sub">{t.needs_provider()} <button type="button" class="link" disabled={!transcriptionEnabled} on:click={() => dispatch("openProviders")}>{t.add_provider()}</button></p>
              {:else if hasProvider === true}
                <!-- The step's title and fields already say that each meeting
                     gets a summary from this provider. The line carries what
                     cannot be read off them: transcripts leave this server, and
                     it may cost money (D-814). -->
                {#if transcriptionEnabled && summary.enabled && summaryProviderRow}
                  <p class="set-row-sub">{summaryStepLead(providerName(summaryProviderRow.id), summaryProviderRow.base_url)}</p>
                {/if}
                {#if summary.enabled}
                <fieldset class="step-config" disabled={!transcriptionEnabled || saving}>
                  <label class="op-field">
                    <span class="op-field-label">{t.field_provider()}</span>
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
                  <label class="op-field">
                    <span class="op-field-label">{t.field_template()}</span>
                    <select
                      bind:value={summary.template}
                      class="op-input"
                      disabled={!workflowsKnown}
                    >
                      <!-- No synthetic "default" row. It named no template and
                           disclosed no question, and the template it stood for is
                           one of the options below it. Empty is still what a policy
                           written before the field existed stores, and it is
                           resolved to a real id the moment the registry lands, so
                           this option is only ever reachable when it could not be. -->
                      {#if summary.template === ""}
                        <option value="">{t.template_shipped()}</option>
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
                          {unrunnableSummaryTemplateLabel(summary.template, workflows)}
                        </option>
                      {/if}
                    </select>
                    {#if !workflowsKnown}
                      <span class="pipe-help">{t.templates_unlisted()}</span>
                    {/if}
                    <span class="pipe-help">{t.template_help()}</span>
                    <button type="button" class="link-btn pipe-link" on:click={() => dispatch("openTemplates")}>
                      {t.edit_templates()}
                    </button>
                  </label>
                </fieldset>
                {/if}
              {/if}
            </section>
          </div>

          <!-- 3. What participants receive. Editable whatever step 1 says: only
               a transcript-only meeting needs a transcript, and that option says
               so in its place. -->
          <div class="pipe-step-row">
            <span class="q-num" aria-hidden="true">3</span>
            <section class="pub-q scroll-mt-4" aria-labelledby="publication-heading" tabindex="-1" bind:this={publicationSettings}>
              <div class="q-head">
                <h3 id="publication-heading" class="q-title">{t.publication_heading()}</h3>
              </div>
              <div class="q-stack">
                <!-- Shows what is published, which is the audio while transcription
                     is off whatever was last chosen. -->
                <PublishedFormatSelect value={publishedFormat} transcript={transcriptionEnabled} disabled={saving} on:change={(event) => selectMeetingFormat(event.detail)} />
                <p id="publication-help" class="set-row-sub">{t.no_video_published()}</p>
              </div>
            </section>
          </div>

          <!-- 4. What media stays on the server. Independent of transcription:
               audio only versus audio and video is a choice either way. Nothing
               depends on step 3, because the audio participants receive is the
               kept recording. -->
          <div class="pipe-step-row">
            <span class="q-num" aria-hidden="true">4</span>
            <fieldset class="pub-q grid gap-3 scroll-mt-4" disabled={saving} tabindex="-1" bind:this={keepSettings}>
              <legend class="q-head">
                <span class="q-title">{t.keep_heading()}</span>
              </legend>
              <label class="flex items-start gap-3 text-sm" class:keep-unavailable={publishedFormat === "opus"}>
                <input class="radio radio-primary radio-sm" type="radio" name="recording-media" value="nothing" aria-label={$LL.app.keep.nothing()} checked={recordingMedia === "nothing"} disabled={publishedFormat === "opus"} on:change={() => selectRecordingMedia("nothing")} aria-describedby="keep-nothing-help" />
                <span>{#each keepNothingLine as part}{#if part.slot === "media"}<strong>{part.text}</strong>{:else}{part.text}{/if}{/each} <span id="keep-nothing-help">{t.keep_nothing_help()}</span>
                  {#if publishedFormat === "opus"}<span class="set-row-sub block pipe-warn">{t.keep_nothing_unavailable()}</span>{/if}</span>
              </label>
              <label class="flex items-start gap-3 text-sm">
                <input class="radio radio-primary radio-sm" type="radio" name="recording-media" value="audio-video" aria-label={$LL.app.keep.audio_video()} checked={recordingMedia === "audio-video"} on:change={() => selectRecordingMedia("audio-video")} aria-describedby="keep-video-help" />
                <span>{#each keepVideoLine as part}{#if part.slot === "media"}<strong>{part.text}</strong>{:else}{part.text}{/if}{/each} <span id="keep-video-help">{t.keep_video_help()}</span></span>
              </label>
              <label class="flex items-start gap-3 text-sm">
                <input class="radio radio-primary radio-sm" type="radio" name="recording-media" value="audio-only" aria-label={$LL.app.keep.audio_only()} checked={recordingMedia === "audio-only"} on:change={() => selectRecordingMedia("audio-only")} />
                <span>{#each keepAudioLine as part}{#if part.slot === "media"}<strong>{part.text}</strong>{:else}{part.text}{/if}{/each}</span>
              </label>
              {#if recordingMedia !== "nothing"}
                <p class="set-row-sub">{#each keepSource as part}{#if part.slot === "storage"}<button type="button" class="link" on:click={() => dispatch("openStorage")}>{t.storage_link()}</button>{:else}{part.text}{/if}{/each}</p>
              {/if}
            </fieldset>
          </div>
        </div>
        {#if recordingMedia === "nothing" && (!transcriptionEnabled || !activeModel || !activeRevision)}
          <p class="set-row-sub" role="status"><strong>{t.must_transcribe()}</strong> <button type="button" class="link" on:click={focusTranscriptionSettings}>{t.set_up_transcription()}</button></p>
        {/if}
        <p class="set-row-sub">{t.applies_to_new()}</p>
      </section>


      <VoiceSeparationModel client={operatorClient} inventory={modelInventory} />

      <section class="op-tint pipe-step search-spellings-card" aria-labelledby="search-spellings-heading">
        <h2 id="search-spellings-heading" class="set-row-name op-card-title">{t.search_heading()}</h2>
        <div class="set-row-main pipe-text-step">
          <p class="set-row-sub">{t.search_intro()}</p>
          <label class="op-field">
            <span class="pipe-field-head">
              <span class="op-field-label">{t.search_field()}</span>
              <span class="pipe-limit">{t.search_limit()}</span>
            </span>
            <textarea
              bind:value={searchAliasesText}
              class="op-input pipe-textarea"
              maxlength={10_100}
              placeholder={t.search_placeholder()}
            ></textarea>
          </label>
          <ul class="pipe-notes">
            <li>{t.search_note_transcripts()}</li>
            <li>{t.search_note_results()}</li>
            <li>{t.search_note_when()}</li>
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
        aria-label={$leavePrompt ? $LL.app.settings.unsaved.leave_title() : t.unsaved_region()}
        transition:fly={{ y: 16, duration: prefersReducedMotion() ? 0 : 180 }}
      >
        {#if $leavePrompt}
          <TriangleAlert size={16} class="save-bar-icon" aria-hidden="true" />
          <p class="save-bar-text">{$LL.app.settings.unsaved.leave_prompt()}</p>
          <div class="save-bar-actions">
            <button bind:this={stayButton} class="sb-btn sb-light" type="button" on:click={cancelLeave}>
              {$LL.app.settings.unsaved.stay()}
            </button>
            <button class="sb-btn sb-danger" type="button" on:click={confirmLeave}>{$LL.app.settings.unsaved.leave()}</button>
          </div>
        {:else}
          <p class="save-bar-text">{saving ? t.saving_changes() : t.unsaved()}</p>
          <button class="sb-btn sb-light" type="button" disabled={saving || !isDirty || (sourceRetention === "delete-after-processing" && (!transcriptionEnabled || !activeModel || !activeRevision))} on:click={handleSave}>
            {#if saving}
              <span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
              {$LL.common.action.saving()}
            {:else}
              {$LL.common.action.save()}
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
     the card is much narrower than the viewport. The card's own tint is what
     the rail markers and the tier rows sit on, so it is named once here. */
  .pipe-card {
    --pipe-card-bg: color-mix(in oklch, var(--color-base-content) 4%, var(--color-base-200));
    container-type: inline-size;
  }
  .pipe-strip li {
    display: flex;
  }
  /* One box per step: its number in a gutter, its name beside it, the state
     under the name, and any note at the bottom of the box, so the four boxes
     are the same height and their text shares one left edge. */
  .strip-chip {
    display: grid;
    grid-template-columns: 16px minmax(0, 1fr);
    grid-template-rows: auto auto 1fr;
    gap: 2px 6px;
    align-items: start;
    align-content: start;
    width: 100%;
    padding: 7px 10px 8px;
    color: inherit;
    text-align: left;
    cursor: pointer;
    background: none;
    border: 1px solid var(--op-inset-border);
    border-radius: var(--radius-box, 0.5rem);
  }
  .strip-chip:hover {
    border-color: color-mix(in oklch, var(--color-base-content) 30%, var(--color-base-200));
  }
  .strip-chip:focus-visible {
    outline: 2px solid var(--color-primary);
    outline-offset: 2px;
  }
  .chip-num {
    display: grid;
    place-items: center;
    grid-column: 1;
    grid-row: 1;
    width: 16px;
    height: 16px;
    font-size: 10px;
    font-weight: 700;
    line-height: 1;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
    border: 1px solid color-mix(in oklch, var(--color-base-content) 30%, transparent);
    border-radius: 50%;
  }
  .chip-name {
    grid-column: 2;
    grid-row: 1;
    align-self: center;
    font-size: 11.5px;
    font-weight: 600;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  .chip-state {
    grid-column: 2;
    grid-row: 2;
    font-size: 13px;
    font-weight: 600;
    overflow-wrap: anywhere;
  }
  .tone-on.strip-chip {
    background-color: color-mix(in srgb, var(--color-primary) 14%, var(--color-base-100));
  }
  .tone-on .chip-num {
    color: var(--color-base-content);
    border-color: var(--color-primary);
  }
  .tone-off .chip-state {
    font-weight: 500;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  .tone-busy.strip-chip {
    border-color: var(--color-primary);
  }
  .tone-amber.strip-chip {
    background-color: color-mix(in srgb, var(--color-warning) 14%, var(--color-base-100));
    border-color: var(--color-warning);
  }
  .tone-amber .chip-num {
    color: var(--color-base-content);
    border-color: var(--color-warning);
  }
  .chip-note {
    grid-column: 2;
    grid-row: 3;
    align-self: end;
    justify-self: start;
    margin-top: 4px;
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
  /* Narrow: one row per step, number and name left, state right. */
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
      grid-template-columns: 16px auto minmax(0, 1fr);
      grid-template-rows: auto;
      align-items: baseline;
      gap: 2px 10px;
      padding: 8px 12px;
      border: 0;
      border-radius: 0;
    }
    .chip-num {
      align-self: center;
    }
    .chip-state {
      grid-column: 3;
      grid-row: 1;
      text-align: right;
      justify-self: end;
    }
    .chip-note {
      grid-column: 3;
      grid-row: 2;
      justify-self: end;
      margin-top: 2px;
    }
    .strip-chip:focus-visible {
      outline-offset: -2px;
    }
  }
  .keep-unavailable {
    color: color-mix(in oklch, var(--color-base-content) 60%, transparent);
  }

  /* The steps: a number on a rail, and beside it the step's panel, inset in
     the card so each step reads as one thing. */
  .pipe-steps {
    display: grid;
    gap: 12px;
  }
  .pipe-step-row {
    position: relative;
    display: grid;
    grid-template-columns: 22px minmax(0, 1fr);
    gap: 0 10px;
  }
  .pipe-step-row::before {
    content: "";
    position: absolute;
    top: 34px;
    bottom: -12px;
    left: 10px;
    width: 2px;
    background: var(--op-inset-border);
  }
  .pipe-step-row:last-child::before {
    display: none;
  }
  .q-num {
    position: relative;
    z-index: 1;
    display: inline-grid;
    place-items: center;
    width: 22px;
    height: 22px;
    margin-top: 11px;
    font-size: 11px;
    font-weight: 600;
    line-height: 1;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
    background: var(--pipe-card-bg);
    border: 1px solid color-mix(in oklch, var(--color-base-content) 30%, transparent);
    border-radius: 50%;
  }
  .pub-q {
    display: grid;
    gap: 8px;
    min-width: 0;
    margin: 0;
    padding: 12px 14px 14px;
    background-color: var(--op-inset);
    border: 1px solid var(--op-inset-border);
    border-radius: var(--radius-box, 0.5rem);
  }
  .pub-q:focus {
    outline: none;
  }
  .pub-q:focus-visible {
    outline: 2px solid var(--color-primary);
    outline-offset: 2px;
  }
  /* Inert while there is no transcript: everything greys but the pill that
     says why. */
  .pub-q.off > :not(.q-head),
  .pub-q.off .q-head > :not(.q-pill) {
    opacity: 0.6;
  }
  .q-head {
    display: flex;
    align-items: center;
    gap: 8px;
    min-height: 20px;
    padding: 0;
  }
  .q-head .op-switch {
    margin-left: auto;
  }
  /* A fieldset's legend sits on the border unless it floats into the box. */
  fieldset.pub-q > legend.q-head {
    float: left;
    width: 100%;
  }
  fieldset.pub-q > legend.q-head + * {
    clear: both;
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
  .q-pill {
    margin-left: auto;
    padding: 2px 8px;
    font-size: 11px;
    font-weight: 600;
    line-height: 1.4;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
    white-space: nowrap;
    border: 1px solid var(--op-inset-border);
    border-radius: 999px;
  }
  .q-pill-warn {
    color: var(--color-warning);
    background-color: color-mix(in srgb, var(--color-warning) 12%, var(--color-base-100));
    border-color: color-mix(in srgb, var(--color-warning) 55%, transparent);
  }
  .q-lead {
    font-size: 12.5px;
  }
  .q-stack {
    display: grid;
    gap: 6px;
    justify-items: start;
  }
  .hw-details {
    display: grid;
    gap: 10px;
    padding: 12px 14px;
    font-size: 12.5px;
    background-color: var(--pipe-card-bg);
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
  /* The step's footer: what transcribes and where, with the hardware facts
     under a toggle. */
  .in-use {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px 12px;
    margin-top: 2px;
    padding: 8px 0 0;
    font-size: 12.5px;
    line-height: 1.35;
    border-top: 1px dashed var(--op-inset-border);
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
    padding: 6px 6px 6px 10px;
    background-color: color-mix(in srgb, var(--color-warning) 14%, var(--color-base-100));
    border: 1px solid var(--color-warning);
    border-radius: var(--radius-box, 0.5rem);
  }
  .in-use-warn :global(.in-use-warn-icon) {
    align-self: center;
    color: var(--color-warning);
  }
  .step-config {
    min-width: 0;
    padding: 0;
    border: 0;
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: 12px;
    margin-top: 2px;
  }
  .pipe-help {
    font-size: 11.5px;
    line-height: 1.45;
    color: color-mix(in oklch, var(--color-base-content) 45%, transparent);
  }
  .search-spellings-card {
    flex-direction: column;
    align-items: stretch;
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

  /* One list: Off, then a row per quality tier. A tier row is a grid: the
     radio and its words, the model's name and state, its one action, and a
     full-width line under them for a download's progress or error. The
     progress line is only a row while it exists, so nothing is centred
     against an empty track. */
  .q-list {
    display: grid;
    gap: 6px;
  }
  .q-opt {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto auto;
    gap: 6px 12px;
    align-items: center;
    min-height: 44px;
    padding: 7px 10px;
    background-color: var(--pipe-card-bg);
    border: 1px solid var(--op-inset-border);
    border-radius: var(--radius-box, 0.5rem);
  }
  .q-opt.selected {
    background-color: color-mix(in srgb, var(--color-primary) 14%, var(--color-base-100));
    border-color: var(--color-primary);
  }
  .q-opt-off {
    background: none;
  }
  .q-opt-model {
    grid-template-columns: minmax(0, 1fr) auto;
  }
  .q-pick {
    display: flex;
    align-items: center;
    gap: 10px;
    min-width: 0;
    cursor: pointer;
  }
  .q-opt.unavailable .q-pick {
    cursor: default;
    opacity: 0.55;
  }
  .q-opt :global(.model-meta) {
    justify-items: end;
    text-align: right;
    white-space: nowrap;
  }
  .q-opt :global(.model-action) {
    justify-content: flex-end;
    min-width: 5.5rem;
  }
  .q-opt :global(.model-action:empty) {
    display: none;
  }
  .q-opt :global(.model-detail) {
    grid-column: 1 / -1;
  }
  @container (max-width: 560px) {
    .q-opt {
      grid-template-columns: minmax(0, 1fr);
    }
    .q-opt :global(.model-meta) {
      justify-items: start;
      text-align: left;
      white-space: normal;
    }
    .q-opt :global(.model-action) {
      justify-content: flex-start;
      min-width: 0;
    }
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
  .q-radio:disabled {
    cursor: default;
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
