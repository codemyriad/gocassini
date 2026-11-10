<script lang="ts">
  // AI providers: where Cassini sends transcripts, and nothing else.
  //
  // This panel used to carry the summarise step and an insight step as well.
  // Both are gone, for different reasons.
  //
  // The summarise step moved to Publish pipeline (SettingsPanel), where it
  // belongs: it is a step between the call ending and the meeting being
  // published, and it was only here because its endpoint is. An endpoint is a
  // thing you register once; a pipeline step is a thing you switch on, and the
  // two answer different questions.
  //
  // The insight step is gone from the UI altogether. It described a
  // configuration nobody was asking for — "run insights somewhere OTHER than
  // the summary endpoint" — while looking like the switch that turns insights
  // on, which it never was. Registering a provider is what an insight needs,
  // and now says so. The field is still persisted and still served, so a
  // deployment that wants a larger model for questions can set it through
  // `PUT operator/settings/llm`; what is removed is a control that mostly
  // taught the wrong model of the product. It is round-tripped here untouched
  // rather than dropped, so saving a provider cannot clear it.
  //
  // No page-level Save, deliberately, and that is the mock's shape rather than
  // an omission: each action here is its own write — saving a provider, or
  // removing one — because there is no coherent half-edited state of a list of
  // endpoints worth holding on to.
  import { onMount } from "svelte";
  import {
    CircleCheck,
    FileText,
    RefreshCw,
    TextAlignStart,
    TriangleAlert,
  } from "@lucide/svelte";
  import { formatCount, LL } from "cassini-viewer/i18n";
  import ModelCombobox from "./ModelCombobox.svelte";
  import { OperatorClient, OperatorHttpError } from "./operator/client";
  import type {
    LLMModel,
    LLMProviderUpdate,
    LLMProviderView,
    LLMSettings,
    LLMStep,
  } from "./operator/types";

  export let operatorClient: OperatorClient | null = null;

  let settings: LLMSettings | null = null;

  let loading = true;
  let saving = false;
  let loadError = "";
  let saveError = "";

  // The draft in the add/edit form. Null when no form is open. Editing carries
  // the id it is editing, so the same form serves both: the mock offers only
  // add and remove, but removing an endpoint to change its timeout would make
  // an administrator retype a key they cannot read back.
  interface ProviderDraft {
    id: string;
    name: string;
    baseUrl: string;
    key: string;
    // True when this id already exists on the server, which is what decides
    // whether an empty key field means "no key" or "keep the stored one".
    existing: boolean;
    keyConfigured: boolean;
    // Explicit, because an empty key input is ambiguous on an edit.
    keyCleared: boolean;
    timeoutSec: number | null;
    maxTokens: number | null;
    advanced: boolean;
    // The endpoint's default model — the one thing every step on it, and
    // every insight run that picks it, will ask for unless a step names its
    // own (D-749).
    model: string;
  }
  let draft: ProviderDraft | null = null;

  // The endpoint Remove was pressed on, awaiting confirmation. Removing is the
  // one action here that cannot be undone from this panel: the stored key is
  // destroyed with the row and cannot be read back, and a step that ran on
  // the endpoint is switched off with it. One click was too little for that.
  let pendingRemoval: LLMProviderView | null = null;

  // What each saved endpoint answered when asked for its models. It doubles as
  // the mock's verified tick: "we listed 41 models from this URL with this key"
  // is a fact, where a tick that only means "a row exists" is decoration. A
  // failure here is reported as itself and never as a broken endpoint — the
  // list may 404 on a server that answers completions perfectly well.
  // The listing itself is kept, not only its count: it is what the default
  // model field offers when a saved endpoint is edited.
  type ProbeState =
    | { status: "checking" }
    | { status: "ok"; count: number; models: LLMModel[] }
    | { status: "failed"; message: string };
  let probes: Record<string, ProbeState> = {};

  onMount(() => {
    void load();
  });

  async function load() {
    if (!operatorClient) {
      return;
    }
    loading = true;
    loadError = "";
    saveError = "";
    try {
      apply(await settingsClient().getLLMSettings());
    } catch (error) {
      loadError = asMessage(error);
    } finally {
      loading = false;
    }
  }

  function settingsClient(): OperatorClient {
    if (!operatorClient) {
      throw new Error($LL.app.settings.llm.no_client());
    }
    return operatorClient;
  }

  function apply(next: LLMSettings) {
    settings = next;
    // Opened on a fresh install with nothing configured: the form IS the page,
    // because there is no reveal worth putting between an administrator and
    // the field they came to fill.
    draft = next.providers.length === 0 ? freshDraft() : null;
    void probeAll(next.providers);
  }

  async function probeAll(providers: LLMProviderView[]) {
    probes = {};
    await Promise.all(providers.map((provider) => probe(provider.id)));
  }

  async function probe(providerId: string) {
    if (!operatorClient) {
      return;
    }
    probes = { ...probes, [providerId]: { status: "checking" } };
    try {
      const models = await operatorClient.listProviderModels(providerId);
      probes = { ...probes, [providerId]: { status: "ok", count: models.length, models } };
    } catch (error) {
      probes = { ...probes, [providerId]: { status: "failed", message: asMessage(error) } };
    }
  }

  function newProviderId(): string {
    const bytes = new Uint8Array(4);
    crypto.getRandomValues(bytes);
    return "p-" + Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
  }

  function freshDraft(): ProviderDraft {
    return {
      id: newProviderId(),
      name: "",
      baseUrl: "",
      key: "",
      existing: false,
      keyConfigured: false,
      keyCleared: false,
      timeoutSec: null,
      maxTokens: null,
      advanced: false,
      model: "",
    };
  }

  function editDraft(provider: LLMProviderView): ProviderDraft {
    return {
      id: provider.id,
      name: provider.name,
      baseUrl: provider.base_url,
      key: "",
      existing: true,
      keyConfigured: provider.api_key_configured,
      keyCleared: false,
      timeoutSec: provider.timeout_sec > 0 ? provider.timeout_sec : null,
      maxTokens: provider.max_tokens > 0 ? provider.max_tokens : null,
      advanced: provider.timeout_sec > 0 || provider.max_tokens > 0,
      model: provider.model,
    };
  }

  // What the default-model field can offer for the draft. A saved endpoint's
  // listing is the probe's; an unsaved one has no id the operator can ask on
  // its behalf, and the field says so rather than showing an empty list that
  // would read as an endpoint with no models.
  $: draftModels =
    draft && probes[draft.id]?.status === "ok" ? probeModelsOf(probes[draft.id]) : [];
  $: draftModelsLoading = draft !== null && probes[draft.id]?.status === "checking";
  $: draftModelsError = draft
    ? draft.existing
      ? modelListUnavailable(probeErrorOf(probes[draft.id]))
      : $LL.app.settings.llm.save_first_for_models()
    : "";

  // One wording for a listing that failed, on the card and in the combobox.
  function modelListUnavailable(detail: string): string {
    return detail === "" ? "" : $LL.app.settings.llm.model_list_unavailable({ detail: detail.replace(/\.$/, "") });
  }

  function probeModelsOf(state: ProbeState | undefined): LLMModel[] {
    return state?.status === "ok" ? state.models : [];
  }

  function probeErrorOf(state: ProbeState | undefined): string {
    return state?.status === "failed" ? state.message : "";
  }

  // Opening the field on a saved endpoint whose listing failed is a retry;
  // on one that listed, it is free.
  function reprobeDraft() {
    if (draft?.existing && probes[draft.id]?.status === "failed") {
      void probe(draft.id);
    }
  }

  // A URL is the only field a request cannot be made without. A key is not:
  // llama.cpp, vLLM and Ollama need none, and demanding one would lock this
  // panel against exactly the self-hosted endpoints the privacy story is built
  // around.
  $: draftReady = (draft?.baseUrl ?? "").trim() !== "";

  // OpenRouter keys start with "sk-or-v1-". One pasted without the prefix
  // saves fine and then fails every run with a 401, and the placeholder that
  // showed the prefix is the likely reason it was left off. Said under the
  // field, never enforced: only OpenRouter's keys have a known shape.
  $: keyPrefixWarning =
    draft && hostOf(draft.baseUrl) === "openrouter.ai" && looksUnprefixed(draft.key)
      ? $LL.app.settings.llm.openrouter_key_prefix()
      : "";

  function hostOf(url: string): string {
    try {
      return new URL(url.trim()).hostname.toLowerCase();
    } catch {
      return "";
    }
  }

  function looksUnprefixed(key: string): boolean {
    const typed = key.trim();
    return typed !== "" && !typed.startsWith("sk-or-");
  }

  // Every provider, with the draft's edits standing in for the row it is
  // editing: PUT replaces the whole list, so a save has to send the untouched
  // rows back exactly as they came.
  function providerUpdates(current: ProviderDraft): LLMProviderUpdate[] {
    const rows = (settings?.providers ?? []).map((provider): LLMProviderUpdate => ({
      id: provider.id,
      name: provider.name,
      base_url: provider.base_url,
      timeout_sec: provider.timeout_sec,
      max_tokens: provider.max_tokens,
      model: provider.model,
      // api_key omitted: the server keeps the stored key for an id it is not
      // told about, which is the only way a list it never serves can survive a
      // round trip.
    }));
    const edited: LLMProviderUpdate = {
      id: current.id,
      name: current.name.trim(),
      base_url: current.baseUrl.trim(),
      timeout_sec: current.timeoutSec ?? 0,
      max_tokens: current.maxTokens ?? 0,
      model: current.model.trim(),
    };
    if (current.keyCleared) {
      edited.api_key = "";
    } else if (current.key !== "") {
      edited.api_key = current.key;
    } else if (!current.existing) {
      // A new keyless endpoint says so outright rather than leaving the field
      // absent, which on a fresh id would mean nothing either way.
      edited.api_key = "";
    }
    const at = rows.findIndex((row) => row.id === current.id);
    if (at >= 0) {
      rows[at] = edited;
      return rows;
    }
    return [...rows, edited];
  }

  async function saveProvider() {
    if (!draft || !draftReady || saving) {
      return;
    }
    const current = draft;
    saving = true;
    saveError = "";
    try {
      apply(await settingsClient().putLLMSettings({ providers: providerUpdates(current) }));
    } catch (error) {
      saveError = asMessage(error);
      // The draft survives a rejected save: an administrator who mistyped a URL
      // should be able to fix that character, not retype the key.
      draft = current;
    } finally {
      saving = false;
    }
  }

  // Which steps run on this endpoint, for the confirmation to name. The
  // insight step is not shown in this panel, but a deployment that set it by
  // hand still loses it here, and the prompt should say so.
  function stepsRunningOn(provider: LLMProviderView): ("summary" | "insight")[] {
    if (!settings) {
      return [];
    }
    const steps: ("summary" | "insight")[] = [];
    if (settings.summary.enabled && settings.summary.provider === provider.id) {
      steps.push("summary");
    }
    if (settings.insight.enabled && settings.insight.provider === provider.id) {
      steps.push("insight");
    }
    return steps;
  }

  // The confirmation's sentence for what removing stops: one whole sentence
  // per combination, never a list joined with "and".
  function stopsSentence(steps: ("summary" | "insight")[]): string {
    const t = $LL.app.settings.llm;
    const place = slot("place");
    if (steps.length === 2) return t.remove_stops_both({ place });
    return steps[0] === "summary" ? t.remove_stops_summaries({ place }) : t.remove_stops_insights({ place });
  }

  // Sentences with marked-up parts in them (a bold place name, a provider in
  // medium weight, a model as code). The sentence is translated whole, with a
  // marker where each part goes, then cut at the markers so the parts can be
  // drawn as elements without piecing the sentence together from fragments.
  const MARK = "\u0000";
  function slot(name: "place" | "provider" | "model"): string {
    return `${MARK}${name}${MARK}`;
  }
  type Segment = { text: string; slot: "" | "place" | "provider" | "model" };
  function segments(sentence: string): Segment[] {
    return sentence
      .split(new RegExp(`${MARK}(\\w+)${MARK}`))
      .map((bit, index) => (index % 2 === 1 ? { text: "", slot: bit as Segment["slot"] } : { text: bit, slot: "" }));
  }
  $: firstSave = segments($LL.app.settings.llm.first_save_turns_on({ place: slot("place") }));
  $: summaryUsage = effectiveSummary
    ? segments(
        effectiveSummary.model
          ? $LL.app.settings.llm.summary_uses_with_model({ provider: slot("provider"), model: slot("model") })
          : $LL.app.settings.llm.summary_uses({ provider: slot("provider") }),
      )
    : [];
  $: changeInPipeline = segments($LL.app.settings.llm.change_in_pipeline({ place: slot("place") }));
  $: insightUsage = effectiveInsight
    ? segments(
        effectiveInsight.model
          ? $LL.app.settings.llm.insight_default_with_model({ provider: slot("provider"), model: slot("model") })
          : $LL.app.settings.llm.insight_default({ provider: slot("provider") }),
      )
    : [];

  async function removeProvider(provider: LLMProviderView) {
    if (!settings || saving) {
      return;
    }
    const current = settings;
    saving = true;
    saveError = "";
    pendingRemoval = null;
    try {
      apply(
        await settingsClient().putLLMSettings({
          providers: current.providers
            .filter((row) => row.id !== provider.id)
            .map((row) => ({
              id: row.id,
              name: row.name,
              base_url: row.base_url,
              timeout_sec: row.timeout_sec,
              max_tokens: row.max_tokens,
              model: row.model,
            })),
          // A step still pointing at the removed endpoint would be rejected —
          // the operator refuses an enabled step with an unknown provider —
          // so removing an endpoint switches off what was running on it. That
          // is the truth either way: the step could not have run.
          summary: detachedStep(current.summary, provider.id),
          insight: detachedStep(current.insight, provider.id),
        }),
      );
    } catch (error) {
      saveError = asMessage(error);
    } finally {
      saving = false;
    }
  }

  function detachedStep(step: LLMStep, removedId: string): LLMStep {
    if (step.provider !== removedId) {
      return step;
    }
    return { ...step, enabled: false, provider: "" };
  }

  function asMessage(error: unknown): string {
    if (error instanceof OperatorHttpError) {
      return error.message;
    }
    return error instanceof Error ? error.message : String(error);
  }

  $: providers = settings?.providers ?? [];
  // The tiles below say it better than a subtitle can, so the line only appears
  // once they are gone.
  $: showSubtitle = providers.length > 0;

  // The endpoint an insight reaches when NOBODY CHOSE one — not "the endpoint
  // insights run on", which stopped being a single answer when the Prepare
  // panel began letting the asker pick.
  //
  // That distinction is the whole reason this line is worded the way it is
  // below. The asker's choice is the normal path now (the picker defaults to
  // the first provider, so most runs name one outright); this is the fallback,
  // and it covers a run that named none, a caller whose provider list could not
  // be read, and a run whose chosen endpoint has since been removed. The
  // operator computes it — insightEndpoint, which is also what builds the
  // child's environment — so this page and the run cannot say different things.
  $: effectiveInsight = settings?.effective.insight ?? null;
  $: effectiveSummary = settings?.effective.summary ?? null;

  function providerName(id: string): string {
    const provider = providers.find((row) => row.id === id);
    return provider ? providerLabel(provider) : id;
  }

  // What a card and its in-place edit form call the endpoint: the name when
  // there is one, else the URL, else the id — the same fallback everywhere.
  function providerLabel(provider: LLMProviderView): string {
    return provider.name || provider.base_url || provider.id;
  }
</script>

<!-- The add/edit form, as one piece of markup rendered in two places: in
     place of the card being edited, or under the list for a new provider.
     Editing used to open this under the whole list too, which read as
     adding a second endpoint rather than changing the one just clicked.
     The heading is the other half of that fix: the form says which it is. -->
{#snippet providerForm(heading: string)}
  {#if draft}
    <!-- Keyed on the draft's id so a form that survives a draft swap — a
         fresh install's automatic draft replaced by another — resets its
         inputs rather than carrying the first draft's text into the second. -->
    {#key draft.id}
      <section class="ep-form">
        <div class="ep-form-head">
          <h3 class="set-row-name ep-form-title">{heading}</h3>
        </div>
        <div class="ep-form-grid">
          <label class="op-field">
            <span class="op-field-label">{$LL.app.settings.llm.field_provider()}</span>
            <input
              bind:value={draft.name}
              type="text"
              class="op-input"
              placeholder={$LL.app.settings.llm.provider_placeholder()}
            />
          </label>
          <label class="op-field">
            <span class="op-field-label">{$LL.app.settings.llm.field_base_url()}</span>
            <input
              bind:value={draft.baseUrl}
              type="url"
              class="op-input"
              placeholder={$LL.app.settings.llm.base_url_placeholder()}
            />
          </label>
        </div>
        <label class="op-field">
          <span class="op-field-label">
            {$LL.app.settings.llm.field_api_key()}
            {#if draft.keyConfigured && !draft.keyCleared}
              <span class="ep-key-state ep-key-ok">{$LL.app.settings.llm.key_stored()}</span>
            {:else if draft.keyCleared}
              <span class="ep-key-state ep-key-warn">{$LL.app.settings.llm.key_will_be_removed()}</span>
            {/if}
          </span>
          <input
            bind:value={draft.key}
            type="password"
            autocomplete="off"
            class="op-input"
            placeholder={draft.keyConfigured && !draft.keyCleared
              ? $LL.app.settings.llm.key_placeholder_keep()
              : $LL.app.settings.llm.key_placeholder_new()}
          />
          {#if keyPrefixWarning}
            <p class="ep-warn">{keyPrefixWarning}</p>
          {/if}
          {#if draft.keyConfigured}
            <button
              class="link-btn self-start"
              class:ep-key-remove={!draft.keyCleared}
              type="button"
              on:click={() => {
                if (draft) {
                  draft.keyCleared = !draft.keyCleared;
                  if (draft.keyCleared) {
                    draft.key = "";
                  }
                }
              }}
            >
              {draft.keyCleared ? $LL.app.settings.llm.keep_saved_key() : $LL.app.settings.llm.remove_saved_key()}
            </button>
          {/if}
        </label>

        <!-- One model per endpoint (D-749). Summaries and insights ask
             for this unless a step names its own; a person creating an
             insight picks an endpoint and gets this model with it. Free
             text over the endpoint's own listing, for the reason the
             combobox gives: the registry of models is the endpoint's. -->
        <ModelCombobox
          bind:value={draft.model}
          label={$LL.app.settings.llm.default_model()}
          models={draftModels}
          loading={draftModelsLoading}
          error={draftModelsError}
          placeholder={$LL.app.settings.llm.model_placeholder()}
          on:open={reprobeDraft}
        />

        <!-- Behind a disclosure rather than dropped: these describe the
             HOST, and a CPU-bound local model needs a longer leash than a
             hosted API does. Nobody adding their first endpoint needs to
             decide either. -->
        <details bind:open={draft.advanced}>
          <summary class="tpl-toggle">
            <span class="tpl-chev" aria-hidden="true"></span>
            {$LL.app.settings.llm.request_limits()}
          </summary>
          <div class="ep-form-grid ep-bounds">
            <label class="op-field">
              <span class="op-field-label">
                {$LL.app.settings.llm.request_timeout()}
              </span>
              <input
                bind:value={draft.timeoutSec}
                type="number"
                min="1"
                class="op-input"
                placeholder={$LL.app.settings.llm.timeout_placeholder({ seconds: formatCount(900) })}
              />
            </label>
            <label class="op-field">
              <span class="op-field-label">
                {$LL.app.settings.llm.token_limit_field()}
              </span>
              <input
                bind:value={draft.maxTokens}
                type="number"
                min="1"
                class="op-input"
                placeholder={$LL.app.settings.llm.token_limit_placeholder({ tokens: formatCount(4096) })}
              />
            </label>
          </div>
        </details>

        <!-- Under the form, not over it: the save belongs after the fields it
             writes, which is where a reader's eye ends up. -->
        <div class="ep-form-actions">
          <button
            class="op-btn"
            type="button"
            disabled={!draftReady || saving}
            on:click={saveProvider}
          >
            {#if saving}
              <span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
              {$LL.app.settings.llm.saving()}
            {:else}
              {$LL.app.settings.llm.save_provider()}
            {/if}
          </button>
          {#if providers.length > 0}
            <button
              class="link-btn op-cancel"
              type="button"
              disabled={saving}
              on:click={() => (draft = null)}
            >
              {$LL.app.settings.llm.cancel()}
            </button>
          {/if}
        </div>
      </section>
    {/key}
  {/if}
{/snippet}

<header class="op-panel-head">
    <div>
      <div class="op-panel-title">
        <h1>{$LL.app.settings.llm.heading()}</h1>
        <!-- Optional, and said out loud: recording and transcription need none
             of this, and an administrator who reads this page as a required
             step has been misled about what Cassini does on its own. -->
        <span class="panel-badge">{$LL.app.settings.llm.optional_badge()}</span>
      </div>
      {#if showSubtitle}
        <p>{$LL.app.settings.llm.subtitle()}</p>
      {/if}
    </div>
    <div class="op-panel-actions">
      <button
        class="icon-btn"
        type="button"
        on:click={load}
        disabled={loading || !operatorClient}
        aria-label={$LL.app.settings.llm.reload()}
      >
        <RefreshCw size={15} aria-hidden="true" />
      </button>
      {#if settings && providers.length > 0 && draft === null}
        <button
          class="op-btn"
          type="button"
          on:click={() => (draft = freshDraft())}
        >
          {$LL.app.settings.llm.add_provider()}
        </button>
      {/if}
    </div>
  </header>

  {#if loadError}
    <div class="err-box" role="alert">{loadError}</div>
  {:else if loading}
    <p class="op-state">{$LL.app.settings.llm.loading()}</p>
  {:else if !settings}
    <p class="op-state">{$LL.app.settings.llm.unavailable()}</p>
  {:else}
    <div class="ep-body">
      {#if providers.length === 0}
        <!-- Nobody arrives here knowing what an endpoint buys them, so the empty
             screen says it before it asks for a key. -->
        <section class="ep-explain">
          <div class="ep-explain-grid">
            <div class="op-tint ep-feat">
              <TextAlignStart size={16} class="ep-feat-icon" aria-hidden="true" />
              <strong>{$LL.app.settings.llm.feature_summaries()}</strong>
              <span>{$LL.app.settings.llm.feature_summaries_description()}</span>
            </div>
            <div class="op-tint ep-feat">
              <FileText size={16} class="ep-feat-icon" aria-hidden="true" />
              <strong>{$LL.app.settings.llm.feature_insights()}</strong>
              <span>{$LL.app.settings.llm.feature_insights_description()}</span>
            </div>
          </div>
          <p class="ep-note">{$LL.app.settings.llm.only_for_ai()}</p>
          <!-- Said before the save, not discovered after it. Registering the
               first endpoint switches summarising on — which is what a fresh
               install wants and what makes the configured state reachable in
               one go — and that is a decision about what leaves this
               deployment, so it is not a surprise worth saving for later. -->
          <p class="ep-note">
            {#each firstSave as part}{#if part.slot === "place"}<strong>{$LL.app.settings.llm.publish_pipeline_place()}</strong>{:else}{part.text}{/if}{/each}
          </p>
        </section>
      {:else}
        <ul class="ep-list">
          {#each providers as provider (provider.id)}
            <li class="op-tint ep-card">
              {#if draft?.existing && draft.id === provider.id}
                <!-- Edited where it stands, so the form is unmistakably
                     this endpoint's and not a second one being added. -->
                {@render providerForm($LL.app.settings.llm.editing({ name: providerLabel(provider) }))}
              {:else}
                <!-- Facts left, actions right, and the row never wraps: a
                     failure message long enough to fill the card used to push
                     Edit and Remove onto a line of their own beneath it. The
                     message has its own full-width line below instead. -->
                <div class="flex items-start justify-between gap-2">
                  <div class="min-w-0">
                    <div class="set-row-name">
                      <span>{providerLabel(provider)}</span>
                      <!-- The tick is a listing that came back, not a row that
                           exists. A failure says so in its own words: an endpoint
                           with no /models route still answers completions, and
                           calling that broken would be wrong. -->
                      {#if probes[provider.id]?.status === "ok"}
                        <CircleCheck
                          size={14}
                          class="text-success"
                          aria-label={$LL.app.settings.llm.probe_ok()}
                        />
                      {:else if probes[provider.id]?.status === "failed"}
                        <TriangleAlert
                          size={14}
                          class="text-warning"
                          aria-label={$LL.app.settings.llm.probe_failed()}
                        />
                      {/if}
                    </div>
                    <div class="set-row-sub">
                      <code class="ep-code">{provider.base_url}</code>
                      <span>
                        {provider.api_key_configured ? $LL.app.settings.llm.key_saved() : $LL.app.settings.llm.no_key()}
                      </span>
                      <!-- The model every step on this endpoint asks for. Not
                           having one is worth seeing: the recorder then falls
                           back to its own default, which a local endpoint has
                           probably never heard of. -->
                      <span class:text-warning={!provider.model}>
                        {provider.model ? $LL.app.settings.llm.model_named({ model: provider.model }) : $LL.app.settings.llm.no_default_model()}
                      </span>
                      {#if probes[provider.id]?.status === "ok"}
                        {@const state = probes[provider.id]}
                        {@const count = state.status === "ok" ? state.count : 0}
                        <span>
                          {count === 1 ? $LL.app.settings.llm.model_count_one() : $LL.app.settings.llm.model_count_other({ count: formatCount(count) })}
                        </span>
                      {:else if probes[provider.id]?.status === "checking"}
                        <span>{$LL.app.settings.llm.loading_models()}</span>
                      {/if}
                      {#if provider.timeout_sec > 0}
                        <span>{$LL.app.settings.llm.timeout_fact({ seconds: formatCount(provider.timeout_sec) })}</span>
                      {/if}
                      {#if provider.max_tokens > 0}
                        <span>{$LL.app.settings.llm.token_limit_fact({ tokens: formatCount(provider.max_tokens) })}</span>
                      {/if}
                    </div>
                  </div>
                  <div class="flex flex-none items-center gap-3">
                    <button
                      class="link-btn"
                      type="button"
                      disabled={saving}
                      on:click={() => (draft = editDraft(provider))}
                    >
                      {$LL.app.settings.llm.edit()}
                    </button>
                    <button
                      class="link-btn danger"
                      type="button"
                      disabled={saving}
                      aria-expanded={pendingRemoval?.id === provider.id}
                      on:click={() => (pendingRemoval = provider)}
                    >
                      {$LL.app.settings.llm.remove()}
                    </button>
                  </div>
                </div>
                {#if probes[provider.id]?.status === "failed"}
                  {@const state = probes[provider.id]}
                  <p class="ep-warn ep-probe-fail">
                    {$LL.app.settings.llm.probe_failed_hint({ reason: modelListUnavailable(state.status === "failed" ? state.message : "") })}
                  </p>
                {/if}
                {#if pendingRemoval?.id === provider.id}
                  <!-- Inline, like StoragePanel's: the app runs inside a shadow
                       root on Nextcloud's page, where a top-layer <dialog> does
                       not reliably follow its styling or focus. It names the
                       endpoint and says what is lost, because neither can be
                       recovered from here: the key is not readable back, and a
                       step switched off here has to be switched on again in
                       Publish pipeline. -->
                  <div
                    class="ep-confirm"
                    role="alertdialog"
                    aria-label={$LL.app.settings.llm.remove_confirm_label()}
                  >
                    <div class="flex items-start gap-2">
                      <TriangleAlert size={18} class="mt-0.5 shrink-0 text-error" aria-hidden="true" />
                      <div class="grid gap-1">
                        <p class="ep-confirm-title">
                          {$LL.app.settings.llm.remove_confirm_title({ name: provider.name || provider.base_url || provider.id })}
                        </p>
                        <p class="ep-confirm-text">
                          <code class="ep-code">{provider.base_url}</code>
                        </p>
                        <ul class="ep-confirm-text grid gap-1">
                          {#if provider.api_key_configured}
                            <li>{$LL.app.settings.llm.remove_deletes_key()}</li>
                          {/if}
                          {#if stepsRunningOn(provider).length > 0}
                            <li>
                              {#each segments(stopsSentence(stepsRunningOn(provider))) as part}{#if part.slot === "place"}<strong>{$LL.app.settings.llm.publish_pipeline_place()}</strong>{:else}{part.text}{/if}{/each}
                            </li>
                          {/if}
                          <li>{$LL.app.settings.llm.remove_keeps_summaries()}</li>
                        </ul>
                      </div>
                    </div>
                    <div class="ep-confirm-actions flex flex-wrap items-center gap-3">
                      <button
                        class="btn btn-sm btn-error"
                        type="button"
                        disabled={saving}
                        on:click={() => void removeProvider(provider)}
                      >
                        {$LL.app.settings.llm.remove_confirm()}
                      </button>
                      <button
                        class="link-btn op-cancel"
                        type="button"
                        disabled={saving}
                        on:click={() => (pendingRemoval = null)}
                      >
                        {$LL.app.settings.llm.cancel()}
                      </button>
                    </div>
                  </div>
                {/if}
              {/if}
            </li>
          {/each}
        </ul>
      {/if}

      {#if draft && !draft.existing}
        <!-- Only a NEW provider's form lives here, under the list; editing
             happens on the card itself. -->
        <section class="ep-new" class:ep-new-ruled={providers.length > 0}>
          {@render providerForm($LL.app.settings.llm.new_provider())}
        </section>
      {/if}

      {#if effectiveInsight}
        <!-- Two facts, and only the second one used to be here.
             "Insights run on X" was true while the endpoint was policy and
             nobody could choose. It is not any more: whoever creates an insight
             picks from this list, so there is no single endpoint insights run
             on — X is what a run that chose none falls back to.
             The rule underneath it survives the change and is now literally
             true rather than true by fallback: the create handler accepts any
             registered provider, and every user is shown all of them. So every
             endpoint on this page is one somebody's transcripts can be sent to,
             and removing them all is still the off switch. -->
        <div class="ep-usage">
          <p class="ep-note">
            {#if effectiveSummary}
              {#each summaryUsage as part}{#if part.slot === "provider"}<span class="font-medium">{providerName(effectiveSummary.provider)}</span>{:else if part.slot === "model"}<code class="ep-model">{effectiveSummary.model}</code>{:else}{part.text}{/if}{/each}
            {:else}
              {$LL.app.settings.llm.summaries_off()}
            {/if}
            {#each changeInPipeline as part}{#if part.slot === "place"}<strong>{$LL.app.settings.llm.publish_pipeline_place()}</strong>{:else}{part.text}{/if}{/each}
          </p>
          <p class="ep-note">
            {#each insightUsage as part}{#if part.slot === "provider"}<span class="font-medium">{providerName(effectiveInsight.provider)}</span>{:else if part.slot === "model"}<code class="ep-model">{effectiveInsight.model}</code>{:else}{part.text}{/if}{/each}
          </p>
          <p class="ep-note">{$LL.app.settings.llm.remove_all_turns_off()}</p>
        </div>
      {/if}

      {#if saveError}
        <div class="err-box" role="alert">{saveError}</div>
      {/if}
    </div>
  {/if}

<style>
  .ep-body {
    display: grid;
    gap: 16px;
  }
  .ep-list {
    display: grid;
    gap: 8px;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .ep-card {
    padding: 14px 16px;
  }
  .ep-code {
    font-family: var(--font-mono);
    font-size: 11.5px;
    font-weight: 500;
    overflow-wrap: anywhere;
  }
  .ep-model {
    padding: 1px 5px;
    font-family: var(--font-mono);
    font-size: 11.5px;
    font-weight: 500;
    overflow-wrap: anywhere;
    background-color: var(--op-code-bg);
    border: 1px solid var(--op-code-border);
    border-radius: var(--radius-selector, 0.25rem);
  }
  .ep-warn {
    margin: 0;
    font-size: 12.5px;
    line-height: 1.5;
    overflow-wrap: anywhere;
    color: var(--color-warning);
  }
  .ep-probe-fail {
    margin-top: 4px;
  }
  .ep-confirm {
    display: grid;
    gap: 10px;
    margin-top: 12px;
    padding: 10px 12px;
    background-color: color-mix(in oklch, var(--color-error) 16%, var(--color-base-100));
    border: 1px solid var(--color-error);
    border-radius: var(--radius-box, 0.5rem);
  }
  .ep-confirm :global(svg) {
    color: var(--color-error);
  }
  .ep-confirm-actions {
    padding-left: 26px;
  }
  .ep-confirm-title {
    margin: 0;
    font-size: 12.5px;
    font-weight: 600;
    color: var(--color-base-content);
  }
  .ep-confirm-text {
    margin: 0;
    font-size: 12.5px;
    line-height: 1.5;
    overflow-wrap: anywhere;
    color: color-mix(in oklch, var(--color-base-content) 80%, transparent);
  }

  .ep-explain {
    display: grid;
    gap: 0;
    padding-bottom: 6px;
  }
  .ep-explain-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(210px, 1fr));
    gap: 8px;
  }
  .ep-feat {
    display: grid;
    align-content: start;
    gap: 3px;
    padding: 13px 14px;
  }
  .ep-feat :global(.ep-feat-icon) {
    margin-bottom: 3px;
    color: var(--color-secondary);
  }
  .ep-feat strong {
    font-size: 13.5px;
    font-weight: 620;
    color: var(--color-base-content);
  }
  .ep-feat span {
    font-size: 12.5px;
    line-height: 1.5;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  .ep-note {
    margin: 10px 0 0;
    font-size: 12.5px;
    line-height: 1.5;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  .ep-body > .ep-note {
    margin-top: 0;
  }
  .ep-usage {
    display: grid;
    gap: 4px;
  }
  .ep-usage .ep-note {
    margin: 0;
  }

  .ep-form {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .ep-new {
    padding: 0 0 6px;
  }
  .ep-new-ruled {
    padding-top: 16px;
    margin-top: 2px;
    border-top: 1px solid var(--color-base-300);
  }
  .ep-form-grid {
    display: grid;
    grid-template-columns: minmax(0, 200px) minmax(0, 1fr);
    gap: 12px;
  }
  .ep-bounds {
    margin-top: 10px;
  }
  /* The form's own heading: it names the thing being made, so it reads as a
     title rather than as another row label. */
  .ep-form-title {
    font-size: 15px;
    font-weight: 650;
  }
  .ep-form-head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 8px 12px;
  }
  .ep-form-actions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 12px;
    margin-top: 4px;
  }
  .ep-key-state {
    display: inline-flex;
    align-items: center;
    gap: 3px;
    margin-left: 6px;
    padding: 1px 6px;
    font-family: var(--font-mono);
    font-size: 11px;
    font-weight: 500;
    line-height: 1.4;
    vertical-align: 1px;
    border-radius: 5px;
  }
  .ep-key-ok {
    color: var(--color-success);
    background-color: color-mix(in srgb, var(--color-success) 16%, transparent);
  }
  .ep-key-warn {
    color: var(--color-warning);
    background-color: color-mix(in srgb, var(--color-warning) 16%, transparent);
  }
  .ep-key-remove {
    color: var(--color-base-content);
    text-decoration: underline;
    text-decoration-color: color-mix(in oklch, var(--color-base-content) 40%, transparent);
    text-underline-offset: 2px;
  }
  .ep-key-remove:hover {
    color: var(--color-error);
    text-decoration-color: currentColor;
  }

  @media (max-width: 560px) {
    .ep-form-grid {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>
