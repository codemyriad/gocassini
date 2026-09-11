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
    Plus,
    RefreshCw,
    TextAlignStart,
    TriangleAlert,
  } from "@lucide/svelte";
  import { OperatorClient, OperatorHttpError } from "./operator/client";
  import type {
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
  type ProbeState =
    | { status: "checking" }
    | { status: "ok"; count: number }
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
      probes = { ...probes, [providerId]: { status: "ok", count: models.length } };
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
    };
  }

  // A URL is the only field a request cannot be made without. A key is not:
  // llama.cpp, vLLM and Ollama need none, and demanding one would lock this
  // panel against exactly the self-hosted endpoints the privacy story is built
  // around.
  $: draftReady = (draft?.baseUrl ?? "").trim() !== "";

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
  $: insightEndpointLabel = effectiveInsight
    ? `${providerName(effectiveInsight.provider)}${effectiveInsight.model ? ` · ${effectiveInsight.model}` : ""}`
    : "";

  function providerName(id: string): string {
    const provider = providers.find((row) => row.id === id);
    return provider ? provider.name || provider.base_url || provider.id : id;
  }
</script>

<section class="rounded-box border border-base-300 bg-base-100 shadow-sm">
  <header class="flex items-start justify-between gap-3 px-4 py-3">
    <div>
      <div class="flex items-center gap-2">
        <h2 class="font-semibold">AI providers</h2>
        <!-- Optional, and said out loud: recording and transcription need none
             of this, and an administrator who reads this page as a required
             step has been misled about what Cassini does on its own. -->
        <span class="badge badge-outline badge-sm border-base-content/25 text-base-content/70">
          Optional
        </span>
      </div>
      {#if showSubtitle}
        <p class="text-xs text-base-content/60">
          Where Cassini sends transcripts for summaries and insights.
        </p>
      {/if}
    </div>
    <div class="flex items-center gap-2">
      {#if settings && providers.length > 0 && draft === null}
        <button
          class="btn btn-primary btn-sm text-sm"
          type="button"
          on:click={() => (draft = freshDraft())}
        >
          <Plus size={14} aria-hidden="true" />
          Add a provider
        </button>
      {/if}
      <button
        class="btn btn-ghost btn-sm btn-square"
        type="button"
        on:click={load}
        disabled={loading || !operatorClient}
        aria-label="Reload AI providers"
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
    <div class="flex items-center justify-center p-6 text-sm text-base-content/60">
      Loading AI providers…
    </div>
  {:else if !settings}
    <div class="flex items-center justify-center p-6 text-sm text-base-content/60">
      No AI provider settings available.
    </div>
  {:else}
    <div class="grid gap-4 p-4">
      {#if providers.length === 0}
        <!-- Nobody arrives here knowing what an endpoint buys them, so the empty
             screen says it before it asks for a key. -->
        <section class="grid gap-3">
          <div class="grid gap-3 sm:grid-cols-2">
            <div class="rounded-box border border-base-300 bg-base-200 p-3">
              <TextAlignStart size={16} class="mb-2 text-secondary" aria-hidden="true" />
              <p class="text-sm font-semibold">Summaries</p>
              <p class="text-xs text-base-content/60">Shown at the top of every meeting.</p>
            </div>
            <div class="rounded-box border border-base-300 bg-base-200 p-3">
              <FileText size={16} class="mb-2 text-secondary" aria-hidden="true" />
              <p class="text-sm font-semibold">Insights</p>
              <p class="text-xs text-base-content/60">
                Answers drawn from meetings you choose.
              </p>
            </div>
          </div>
          <p class="text-xs leading-relaxed text-base-content/70">
            Recording and transcription run entirely on your own infrastructure. This is the
            only step that sends data to a third party. Without an endpoint you still get the
            transcript, just no summary or insights.
          </p>
          <!-- Said before the save, not discovered after it. Registering the
               first endpoint switches summarising on — which is what a fresh
               install wants and what makes the configured state reachable in
               one go — and that is a decision about what leaves this
               deployment, so it is not a surprise worth saving for later. -->
          <p class="text-xs leading-relaxed text-base-content/70">
            Saving your first endpoint switches meeting summaries on, so every recording's
            transcript is sent to it. You can turn that off again in Publish pipeline.
          </p>
        </section>
      {:else}
        <ul class="grid gap-2">
          {#each providers as provider (provider.id)}
            <li class="rounded-box border border-base-300 bg-base-200 p-3">
              <div class="flex flex-wrap items-start justify-between gap-2">
                <div class="min-w-0">
                  <div class="flex items-center gap-1.5">
                    <span class="text-sm font-semibold">
                      {provider.name || provider.base_url || provider.id}
                    </span>
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
                  <div
                    class="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-base-content/60"
                  >
                    <code class="font-mono">{provider.base_url}</code>
                    <span>
                      {provider.api_key_configured ? "Key stored" : "No key"}
                    </span>
                    {#if probes[provider.id]?.status === "ok"}
                      {@const state = probes[provider.id]}
                      <span>
                        {state.status === "ok" ? state.count : 0}
                        {state.status === "ok" && state.count === 1 ? "model" : "models"}
                      </span>
                    {:else if probes[provider.id]?.status === "checking"}
                      <span>listing models…</span>
                    {/if}
                    {#if provider.timeout_sec > 0}
                      <span>{provider.timeout_sec}s timeout</span>
                    {/if}
                    {#if provider.max_tokens > 0}
                      <span>{provider.max_tokens} token limit</span>
                    {/if}
                  </div>
                  {#if probes[provider.id]?.status === "failed"}
                    {@const state = probes[provider.id]}
                    <p class="mt-1 text-xs text-warning">
                      Its model list could not be read: {state.status === "failed"
                        ? state.message
                        : ""} Summaries and insights may still work — a model can always be
                      named by hand.
                    </p>
                  {/if}
                </div>
                <div class="flex flex-none items-center gap-1">
                  <button
                    class="btn btn-ghost btn-xs"
                    type="button"
                    disabled={saving}
                    on:click={() => (draft = editDraft(provider))}
                  >
                    Edit
                  </button>
                  <button
                    class="btn btn-ghost btn-xs text-error"
                    type="button"
                    disabled={saving}
                    aria-expanded={pendingRemoval?.id === provider.id}
                    on:click={() => (pendingRemoval = provider)}
                  >
                    Remove
                  </button>
                </div>
              </div>
              {#if pendingRemoval?.id === provider.id}
                <!-- Inline, like StoragePanel's: the app runs inside a shadow
                     root on Nextcloud's page, where a top-layer <dialog> does
                     not reliably follow its styling or focus. It names the
                     endpoint and says what is lost, because neither can be
                     recovered from here: the key is not readable back, and a
                     step switched off here has to be switched on again in
                     Publish pipeline. -->
                <div
                  class="mt-3 grid gap-2 rounded-box border border-error bg-error/10 p-3"
                  role="alertdialog"
                  aria-label="Confirm removing this endpoint"
                >
                  <div class="flex items-start gap-2">
                    <TriangleAlert size={18} class="mt-0.5 shrink-0 text-error" aria-hidden="true" />
                    <div class="grid gap-1">
                      <p class="text-sm font-semibold">
                        Remove {provider.name || provider.base_url || provider.id}?
                      </p>
                      <p class="text-xs break-words text-base-content/80">
                        <code class="font-mono">{provider.base_url}</code>
                      </p>
                      <ul class="grid gap-1 text-xs text-base-content/80">
                        {#if provider.api_key_configured}
                          <li>The stored key is destroyed. Cassini cannot show it to you first.</li>
                        {/if}
                        {#if stepsRunningOn(provider).length > 0}
                          <li>
                            It stops {stepsRunningOn(provider).join(" and ")}: nothing will run
                            them until an endpoint is chosen again in Publish pipeline.
                          </li>
                        {/if}
                        <li>Meetings already published keep their summaries.</li>
                      </ul>
                    </div>
                  </div>
                  <div class="flex flex-wrap items-center gap-2">
                    <button
                      class="btn btn-sm btn-error"
                      type="button"
                      disabled={saving}
                      on:click={() => void removeProvider(provider)}
                    >
                      Yes, remove it
                    </button>
                    <button
                      class="btn btn-sm btn-ghost"
                      type="button"
                      disabled={saving}
                      on:click={() => (pendingRemoval = null)}
                    >
                      Cancel
                    </button>
                  </div>
                </div>
              {/if}
            </li>
          {/each}
        </ul>
      {/if}

      {#if draft}
        <!-- Keyed on the draft's id so switching from one row's Edit straight to
             another's resets the inputs rather than carrying the first row's
             text into the second. -->
        {#key draft.id}
          <section class="grid gap-3 rounded-box border border-base-300 bg-base-200 p-3">
            <div class="grid gap-3 sm:grid-cols-2">
              <label class="flex w-full flex-col gap-1">
                <span class="text-xs font-medium text-base-content/70">Provider</span>
                <input
                  bind:value={draft.name}
                  type="text"
                  class="input input-sm w-full border-base-300 shadow-none"
                  placeholder="OpenRouter, local Qwen…"
                />
              </label>
              <label class="flex w-full flex-col gap-1">
                <span class="text-xs font-medium text-base-content/70">Base URL</span>
                <input
                  bind:value={draft.baseUrl}
                  type="url"
                  class="input input-sm w-full border-base-300 shadow-none"
                  placeholder="https://openrouter.ai/api/v1 or http://your-host:8000/v1"
                />
              </label>
            </div>
            <label class="flex w-full flex-col gap-1">
              <span class="text-xs font-medium text-base-content/70">
                API key
                {#if draft.keyConfigured && !draft.keyCleared}
                  <span class="badge badge-success badge-outline badge-xs align-middle">
                    stored
                  </span>
                {:else if draft.keyCleared}
                  <span class="badge badge-warning badge-outline badge-xs align-middle">
                    will be removed
                  </span>
                {/if}
              </span>
              <input
                bind:value={draft.key}
                type="password"
                autocomplete="off"
                class="input input-sm w-full border-base-300 shadow-none"
                placeholder={draft.keyConfigured && !draft.keyCleared
                  ? "leave blank to keep the stored key"
                  : "sk-or-v1-… — self-hosted servers usually need none"}
              />
              {#if draft.keyConfigured}
                <button
                  class="link link-hover self-start text-xs text-base-content/60"
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
                  {draft.keyCleared ? "Keep the stored key" : "Remove the stored key"}
                </button>
              {/if}
            </label>

            <!-- Behind a disclosure rather than dropped: these describe the
                 HOST, and a CPU-bound local model needs a longer leash than a
                 hosted API does. Nobody adding their first endpoint needs to
                 decide either. -->
            <details bind:open={draft.advanced}>
              <summary class="cursor-pointer text-xs text-base-content/60">
                Request bounds
              </summary>
              <div class="mt-2 grid gap-3 sm:grid-cols-2">
                <label class="flex w-full flex-col gap-1">
                  <span class="text-xs font-medium text-base-content/70">
                    Request timeout (s)
                  </span>
                  <input
                    bind:value={draft.timeoutSec}
                    type="number"
                    min="1"
                    class="input input-sm w-full border-base-300 shadow-none"
                    placeholder="900 (default)"
                  />
                </label>
                <label class="flex w-full flex-col gap-1">
                  <span class="text-xs font-medium text-base-content/70">
                    Response token limit
                  </span>
                  <input
                    bind:value={draft.maxTokens}
                    type="number"
                    min="1"
                    class="input input-sm w-full border-base-300 shadow-none"
                    placeholder="4096 (default)"
                  />
                </label>
              </div>
            </details>

            <div class="flex items-center gap-2">
              <button
                class="btn btn-primary btn-sm text-sm"
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
                  class="btn btn-ghost btn-sm text-sm"
                  type="button"
                  disabled={saving}
                  on:click={() => (draft = null)}
                >
                  Cancel
                </button>
              {/if}
            </div>
          </section>
        {/key}
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
        <p class="text-xs text-base-content/60">
          Anyone creating an insight chooses which of these answers it; one that chooses none
          runs on <span class="font-medium">{insightEndpointLabel}</span>. Every endpoint listed
          here is one an insight may reach, and removing them all is how insights are switched
          off.
        </p>
      {/if}

      {#if saveError}
        <div class="alert alert-error text-sm">{saveError}</div>
      {/if}
    </div>
  {/if}
</section>
