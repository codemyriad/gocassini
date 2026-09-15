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
      throw new Error("No operator client is available.");
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
      : "Save the provider first to list its models."
    : "";

  // One wording for a listing that failed, on the card and in the combobox.
  function modelListUnavailable(detail: string): string {
    return detail === "" ? "" : `Model list unavailable: ${detail.replace(/\.$/, "")}.`;
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
      ? "OpenRouter keys start with sk-or-v1-. Check you pasted the whole key."
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
  function stepsRunningOn(provider: LLMProviderView): string[] {
    if (!settings) {
      return [];
    }
    const names: string[] = [];
    if (settings.summary.enabled && settings.summary.provider === provider.id) {
      names.push("meeting summaries");
    }
    if (settings.insight.enabled && settings.insight.provider === provider.id) {
      names.push("insights");
    }
    return names;
  }

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
          <h3 class="set-row-name">{heading}</h3>
          <div class="ep-form-actions">
            <button
              class="op-btn"
              type="button"
              disabled={!draftReady || saving}
              on:click={saveProvider}
            >
              {#if saving}
                <span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
                Saving…
              {:else}
                Save provider
              {/if}
            </button>
            {#if providers.length > 0}
              <button
                class="link-btn op-cancel"
                type="button"
                disabled={saving}
                on:click={() => (draft = null)}
              >
                Cancel
              </button>
            {/if}
          </div>
        </div>
        <div class="ep-form-grid">
          <label class="op-field">
            <span class="op-field-label">Provider</span>
            <input
              bind:value={draft.name}
              type="text"
              class="op-input"
              placeholder="OpenRouter, local Qwen…"
            />
          </label>
          <label class="op-field">
            <span class="op-field-label">Base URL</span>
            <input
              bind:value={draft.baseUrl}
              type="url"
              class="op-input"
              placeholder="https://openrouter.ai/api/v1 or http://your-host:8000/v1"
            />
          </label>
        </div>
        <label class="op-field">
          <span class="op-field-label">
            API key
            {#if draft.keyConfigured && !draft.keyCleared}
              <span class="ep-key-state ep-key-ok">stored</span>
            {:else if draft.keyCleared}
              <span class="ep-key-state ep-key-warn">will be removed</span>
            {/if}
          </span>
          <input
            bind:value={draft.key}
            type="password"
            autocomplete="off"
            class="op-input"
            placeholder={draft.keyConfigured && !draft.keyCleared
              ? "Leave blank to keep the saved key"
              : "Paste the full API key (sk-or-v1-… for OpenRouter). Self-hosted servers usually don't need one."}
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
              {draft.keyCleared ? "Keep the saved key" : "Remove the saved key"}
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
          label="Default model"
          models={draftModels}
          loading={draftModelsLoading}
          error={draftModelsError}
          placeholder="e.g. openai/gpt-4o-mini or qwen3-30b"
          on:open={reprobeDraft}
        />

        <!-- Behind a disclosure rather than dropped: these describe the
             HOST, and a CPU-bound local model needs a longer leash than a
             hosted API does. Nobody adding their first endpoint needs to
             decide either. -->
        <details bind:open={draft.advanced}>
          <summary class="tpl-toggle">
            <span class="tpl-chev" aria-hidden="true"></span>
            Request limits
          </summary>
          <div class="ep-form-grid ep-bounds">
            <label class="op-field">
              <span class="op-field-label">
                Request timeout (s)
              </span>
              <input
                bind:value={draft.timeoutSec}
                type="number"
                min="1"
                class="op-input"
                placeholder="900 (default)"
              />
            </label>
            <label class="op-field">
              <span class="op-field-label">
                Response token limit
              </span>
              <input
                bind:value={draft.maxTokens}
                type="number"
                min="1"
                class="op-input"
                placeholder="4096 (default)"
              />
            </label>
          </div>
        </details>

      </section>
    {/key}
  {/if}
{/snippet}

<header class="op-panel-head">
    <div>
      <div class="op-panel-title">
        <h1>AI providers</h1>
        <!-- Optional, and said out loud: recording and transcription need none
             of this, and an administrator who reads this page as a required
             step has been misled about what Cassini does on its own. -->
        <span class="panel-badge">Optional</span>
      </div>
      {#if showSubtitle}
        <p>Where Cassini sends transcripts for summaries and insights.</p>
      {/if}
    </div>
    <div class="op-panel-actions">
      <button
        class="icon-btn"
        type="button"
        on:click={load}
        disabled={loading || !operatorClient}
        aria-label="Reload AI providers"
      >
        <RefreshCw size={15} aria-hidden="true" />
      </button>
      {#if settings && providers.length > 0 && draft === null}
        <button
          class="op-btn"
          type="button"
          on:click={() => (draft = freshDraft())}
        >
          Add a provider
        </button>
      {/if}
    </div>
  </header>

  {#if loadError}
    <div class="err-box" role="alert">{loadError}</div>
  {:else if loading}
    <p class="op-state">Loading AI providers…</p>
  {:else if !settings}
    <p class="op-state">No AI provider settings available.</p>
  {:else}
    <div class="ep-body">
      {#if providers.length === 0}
        <!-- Nobody arrives here knowing what an endpoint buys them, so the empty
             screen says it before it asks for a key. -->
        <section class="ep-explain">
          <div class="ep-explain-grid">
            <div class="op-tint ep-feat">
              <TextAlignStart size={16} class="ep-feat-icon" aria-hidden="true" />
              <strong>Summaries</strong>
              <span>Shown at the top of every meeting.</span>
            </div>
            <div class="op-tint ep-feat">
              <FileText size={16} class="ep-feat-icon" aria-hidden="true" />
              <strong>Insights</strong>
              <span>Answers drawn from meetings you choose.</span>
            </div>
          </div>
          <p class="ep-note">
            Recording and transcription run on your own servers. A provider is only needed for
            summaries and insights.
          </p>
          <!-- Said before the save, not discovered after it. Registering the
               first endpoint switches summarising on — which is what a fresh
               install wants and what makes the configured state reachable in
               one go — and that is a decision about what leaves this
               deployment, so it is not a surprise worth saving for later. -->
          <p class="ep-note">
            Saving your first provider turns on summaries for every meeting. You can turn them off in
            <strong>Operator › Publish pipeline</strong>.
          </p>
        </section>
      {:else}
        <ul class="ep-list">
          {#each providers as provider (provider.id)}
            <li class="op-tint ep-card">
              {#if draft?.existing && draft.id === provider.id}
                <!-- Edited where it stands, so the form is unmistakably
                     this endpoint's and not a second one being added. -->
                {@render providerForm(`Editing ${providerLabel(provider)}`)}
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
                          aria-label="This endpoint answered with its model list"
                        />
                      {:else if probes[provider.id]?.status === "failed"}
                        <TriangleAlert
                          size={14}
                          class="text-warning"
                          aria-label="This endpoint did not list its models"
                        />
                      {/if}
                    </div>
                    <div class="set-row-sub">
                      <code class="ep-code">{provider.base_url}</code>
                      <span>
                        {provider.api_key_configured ? "Key saved" : "No key"}
                      </span>
                      <!-- The model every step on this endpoint asks for. Not
                           having one is worth seeing: the recorder then falls
                           back to its own default, which a local endpoint has
                           probably never heard of. -->
                      <span class:text-warning={!provider.model}>
                        {provider.model ? `Model ${provider.model}` : "No default model"}
                      </span>
                      {#if probes[provider.id]?.status === "ok"}
                        {@const state = probes[provider.id]}
                        <span>
                          {state.status === "ok" ? state.count : 0}
                          {state.status === "ok" && state.count === 1 ? "model" : "models"}
                        </span>
                      {:else if probes[provider.id]?.status === "checking"}
                        <span>Loading models…</span>
                      {/if}
                      {#if provider.timeout_sec > 0}
                        <span>{provider.timeout_sec}s timeout</span>
                      {/if}
                      {#if provider.max_tokens > 0}
                        <span>{provider.max_tokens} token limit</span>
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
                      Edit
                    </button>
                    <button
                      class="link-btn danger"
                      type="button"
                      disabled={saving}
                      aria-expanded={pendingRemoval?.id === provider.id}
                      on:click={() => (pendingRemoval = provider)}
                    >
                      Remove
                    </button>
                  </div>
                </div>
                {#if probes[provider.id]?.status === "failed"}
                  {@const state = probes[provider.id]}
                  <p class="ep-warn ep-probe-fail">
                    {modelListUnavailable(state.status === "failed" ? state.message : "")}
                    You can still type a model ID.
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
                    aria-label="Confirm removing this endpoint"
                  >
                    <div class="flex items-start gap-2">
                      <TriangleAlert size={18} class="mt-0.5 shrink-0 text-error" aria-hidden="true" />
                      <div class="grid gap-1">
                        <p class="ep-confirm-title">
                          Remove {provider.name || provider.base_url || provider.id}?
                        </p>
                        <p class="ep-confirm-text">
                          <code class="ep-code">{provider.base_url}</code>
                        </p>
                        <ul class="ep-confirm-text grid gap-1">
                          {#if provider.api_key_configured}
                            <li>The saved API key is deleted and can't be recovered.</li>
                          {/if}
                          {#if stepsRunningOn(provider).length > 0}
                            <li>
                              This stops {stepsRunningOn(provider).join(" and ")} until you choose
                              another provider in <strong>Operator › Publish pipeline</strong>.
                            </li>
                          {/if}
                          <li>Meetings already published keep their summaries.</li>
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
                        Yes, remove it
                      </button>
                      <button
                        class="link-btn op-cancel"
                        type="button"
                        disabled={saving}
                        on:click={() => (pendingRemoval = null)}
                      >
                        Cancel
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
          {@render providerForm("New provider")}
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
              Summaries use <span class="font-medium">{providerName(effectiveSummary.provider)}</span>{#if effectiveSummary.model}{" "}with
                <code class="ep-model">{effectiveSummary.model}</code>{/if}.
            {:else}
              Summaries are off.
            {/if}
            Change this in <strong>Operator › Publish pipeline</strong>.
          </p>
          <p class="ep-note">
            Whoever creates an insight picks which provider it uses. If they don't pick one, it uses
            <span class="font-medium">{providerName(effectiveInsight.provider)}</span>{#if effectiveInsight.model}{" "}with
              <code class="ep-model">{effectiveInsight.model}</code>{/if}.
          </p>
          <p class="ep-note">Remove all providers to turn off both summaries and insights.</p>
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
  .ep-form-head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 8px 12px;
  }
  .ep-form-actions {
    display: flex;
    align-items: center;
    gap: 12px;
    margin-left: auto;
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
