<script lang="ts">
  import { onMount } from "svelte";
  import { Bot, Plus, RefreshCw, Trash2 } from "@lucide/svelte";
  import { OperatorClient, OperatorHttpError } from "./operator/client";
  import type { LLMModel, LLMProviderUpdate, LLMSettings, LLMStep } from "./operator/types";

  export let operatorClient: OperatorClient | null = null;

  // Editable provider rows carry UI-only key state beside the wire fields.
  // The server never sends a stored key (api_key_configured only), so the key
  // column is: a badge for "stored", a password input for a replacement, and a
  // remove toggle that sends api_key: "" on save. Leaving the input empty
  // omits api_key entirely, which keeps the stored key.
  interface ProviderRow {
    id: string;
    name: string;
    baseUrl: string;
    keyConfigured: boolean;
    keyInput: string;
    keyCleared: boolean;
  }

  let settings: LLMSettings | null = null;
  let providers: ProviderRow[] = [];
  let readable: LLMStep = { enabled: false, provider: "", model: "" };
  let summary: LLMStep = { enabled: false, provider: "", model: "" };
  let timeoutSec = 0;
  let maxTokens = 0;
  let snapshot = "";

  let loading = true;
  let saving = false;
  let loadError = "";
  let saveError = "";

  // Fetched model lists per provider id, feeding the datalists. Fetching is a
  // convenience: model fields always accept free text.
  let modelsByProvider: Record<string, LLMModel[]> = {};
  let modelsError = "";
  let loadingModelsFor = "";

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
      apply(await operatorClient.getLLMSettings());
    } catch (error) {
      loadError = asMessage(error);
    } finally {
      loading = false;
    }
  }

  function apply(next: LLMSettings) {
    settings = next;
    providers = next.providers.map((p) => ({
      id: p.id,
      name: p.name,
      baseUrl: p.base_url,
      keyConfigured: p.api_key_configured,
      keyInput: "",
      keyCleared: false,
    }));
    readable = { ...next.readable };
    summary = { ...next.summary };
    timeoutSec = next.timeout_sec;
    maxTokens = next.max_tokens;
    snapshot = JSON.stringify({ providers, readable, summary, timeoutSec, maxTokens });
  }

  function newProviderId(): string {
    const bytes = new Uint8Array(4);
    crypto.getRandomValues(bytes);
    return "p-" + Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
  }

  function addProvider() {
    providers = [
      ...providers,
      { id: newProviderId(), name: "", baseUrl: "", keyConfigured: false, keyInput: "", keyCleared: false },
    ];
  }

  function removeProvider(id: string) {
    providers = providers.filter((p) => p.id !== id);
    // A step pointing at the removed endpoint cannot stay enabled — the server
    // would reject it; disable it and forget the reference instead.
    if (readable.provider === id) {
      readable = { ...readable, provider: "", enabled: false };
    }
    if (summary.provider === id) {
      summary = { ...summary, provider: "", enabled: false };
    }
  }

  async function loadModels(providerId: string) {
    if (!operatorClient || providerId === "" || loadingModelsFor !== "") {
      return;
    }
    modelsError = "";
    if (!settings?.providers.some((p) => p.id === providerId)) {
      modelsError = "Save the endpoint first, then load its models.";
      return;
    }
    loadingModelsFor = providerId;
    try {
      modelsByProvider = { ...modelsByProvider, [providerId]: await operatorClient.listProviderModels(providerId) };
    } catch (error) {
      modelsError = asMessage(error);
    } finally {
      loadingModelsFor = "";
    }
  }

  async function handleSave() {
    if (!operatorClient || saving) {
      return;
    }
    saving = true;
    saveError = "";
    try {
      apply(
        await operatorClient.putLLMSettings({
          providers: providers.map((row): LLMProviderUpdate => {
            const update: LLMProviderUpdate = { id: row.id, name: row.name, base_url: row.baseUrl };
            if (row.keyCleared) {
              update.api_key = "";
            } else if (row.keyInput !== "") {
              update.api_key = row.keyInput;
            }
            return update;
          }),
          readable,
          summary,
          timeout_sec: Number(timeoutSec) || 0,
          max_tokens: Number(maxTokens) || 0,
        }),
      );
    } catch (error) {
      saveError = asMessage(error);
    } finally {
      saving = false;
    }
  }

  function asMessage(error: unknown): string {
    if (error instanceof OperatorHttpError) {
      return error.message;
    }
    return error instanceof Error ? error.message : String(error);
  }

  function effectiveLabel(step: "readable" | "summary"): string {
    const effective = step === "readable" ? settings?.effective.readable : settings?.effective.summary;
    if (!effective) {
      return "Currently off — this step is skipped.";
    }
    return `Currently: ${effective.base_url} · ${effective.model || "endpoint default model"}`;
  }

  $: current = JSON.stringify({ providers, readable, summary, timeoutSec, maxTokens });
  $: isDirty = settings !== null && current !== snapshot;
</script>

<section class="rounded-box border border-base-300 bg-base-100 shadow-sm">
  <header class="flex items-center justify-between gap-3 px-4 py-3">
    <div>
      <h2 class="font-semibold">Summaries &amp; cleanup</h2>
      <p class="text-xs text-base-content/60">
        Which model endpoints turn transcripts into readable text and meeting summaries. The full
        transcript is sent to the endpoint a step uses; with no step enabled, meetings publish raw
        transcripts and no summary.
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
        on:click={load}
        disabled={loading || !operatorClient}
        aria-label="Reload LLM settings"
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
    <div class="flex items-center justify-center p-6 text-sm text-base-content/60">Loading LLM settings…</div>
  {:else if !settings}
    <div class="flex items-center justify-center p-6 text-sm text-base-content/60">No LLM settings available.</div>
  {:else}
    <div class="grid gap-4 p-4">
      <section class="grid content-start gap-2 rounded-box border border-base-300 bg-base-200 p-3">
        <div class="flex items-center justify-between gap-2">
          <div class="flex items-center gap-2">
            <Bot size={16} aria-hidden="true" />
            <h3 class="text-sm font-semibold">Endpoints</h3>
          </div>
          <button class="btn btn-ghost btn-sm" type="button" on:click={addProvider}>
            <Plus size={14} aria-hidden="true" />
            Add endpoint
          </button>
        </div>
        {#if providers.length === 0}
          <p class="text-sm text-base-content/60">
            No endpoints yet. Add an OpenAI-compatible one — a hosted provider, or your own model
            server (llama.cpp, vLLM, Ollama), which usually needs no key.
          </p>
        {/if}
        {#each providers as row (row.id)}
          <div class="grid gap-2 rounded-box border border-base-300 bg-base-100 p-2 lg:grid-cols-[minmax(0,0.7fr)_minmax(0,1.3fr)_minmax(0,1fr)_auto] lg:items-end">
            <label class="flex w-full flex-col gap-1">
              <span class="text-xs font-medium text-base-content/70">Name</span>
              <input
                bind:value={row.name}
                type="text"
                class="input input-sm w-full border-base-300 shadow-none"
                placeholder="OpenRouter, local Qwen…"
              />
            </label>
            <label class="flex w-full flex-col gap-1">
              <span class="text-xs font-medium text-base-content/70">Base URL</span>
              <input
                bind:value={row.baseUrl}
                type="url"
                class="input input-sm w-full border-base-300 shadow-none"
                placeholder="https://openrouter.ai/api/v1 or http://your-host:8000/v1"
              />
            </label>
            <label class="flex w-full flex-col gap-1">
              <span class="text-xs font-medium text-base-content/70">
                API key
                {#if row.keyConfigured && !row.keyCleared}
                  <span class="badge badge-success badge-outline badge-xs align-middle">stored</span>
                {:else if row.keyCleared}
                  <span class="badge badge-warning badge-outline badge-xs align-middle">will be removed</span>
                {/if}
              </span>
              <input
                bind:value={row.keyInput}
                type="password"
                autocomplete="off"
                class="input input-sm w-full border-base-300 shadow-none"
                placeholder={row.keyConfigured && !row.keyCleared
                  ? "leave blank to keep the stored key"
                  : "optional — self-hosted servers usually need none"}
              />
            </label>
            <div class="flex items-center gap-1">
              {#if row.keyConfigured}
                <button
                  class="btn btn-ghost btn-sm"
                  type="button"
                  on:click={() => {
                    row.keyCleared = !row.keyCleared;
                    if (row.keyCleared) {
                      row.keyInput = "";
                    }
                  }}
                >
                  {row.keyCleared ? "Keep key" : "Remove key"}
                </button>
              {/if}
              <button
                class="btn btn-ghost btn-sm btn-square"
                type="button"
                aria-label={`Delete endpoint ${row.name || row.baseUrl || row.id}`}
                on:click={() => removeProvider(row.id)}
              >
                <Trash2 size={14} aria-hidden="true" />
              </button>
            </div>
          </div>
        {/each}
      </section>

      <div class="grid gap-4 lg:grid-cols-2">
        <section class="grid content-start gap-2 rounded-box border border-base-300 bg-base-200 p-3">
          <label class="flex cursor-pointer items-center gap-2">
            <input type="checkbox" class="toggle toggle-primary toggle-sm" bind:checked={readable.enabled} />
            <h3 class="text-sm font-semibold">Readable transcript cleanup</h3>
          </label>
          <label class="flex w-full flex-col gap-1">
            <span class="text-xs font-medium text-base-content/70">Endpoint</span>
            <select bind:value={readable.provider} class="select select-sm w-full border-base-300 shadow-none">
              <option value="">— none —</option>
              {#each providers as p (p.id)}
                <option value={p.id}>{p.name || p.baseUrl || p.id}</option>
              {/each}
            </select>
          </label>
          <label class="flex w-full flex-col gap-1">
            <span class="text-xs font-medium text-base-content/70">Model</span>
            <div class="flex w-full items-center gap-1">
              <input
                bind:value={readable.model}
                type="text"
                class="input input-sm w-full border-base-300 shadow-none"
                placeholder="endpoint default"
                list="llm-models-readable"
              />
              <button
                class="btn btn-ghost btn-sm shrink-0"
                type="button"
                disabled={readable.provider === "" || loadingModelsFor !== ""}
                on:click={() => loadModels(readable.provider)}
              >
                {#if loadingModelsFor !== "" && loadingModelsFor === readable.provider}
                  <span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
                {:else}
                  Load models
                {/if}
              </button>
            </div>
            <datalist id="llm-models-readable">
              {#each modelsByProvider[readable.provider] ?? [] as model (model.id)}
                <option value={model.id}>{model.name ?? model.id}</option>
              {/each}
            </datalist>
          </label>
          <p class="text-xs text-base-content/60">{effectiveLabel("readable")}</p>
        </section>
        <section class="grid content-start gap-2 rounded-box border border-base-300 bg-base-200 p-3">
          <label class="flex cursor-pointer items-center gap-2">
            <input type="checkbox" class="toggle toggle-primary toggle-sm" bind:checked={summary.enabled} />
            <h3 class="text-sm font-semibold">Meeting summary</h3>
          </label>
          <label class="flex w-full flex-col gap-1">
            <span class="text-xs font-medium text-base-content/70">Endpoint</span>
            <select bind:value={summary.provider} class="select select-sm w-full border-base-300 shadow-none">
              <option value="">— none —</option>
              {#each providers as p (p.id)}
                <option value={p.id}>{p.name || p.baseUrl || p.id}</option>
              {/each}
            </select>
          </label>
          <label class="flex w-full flex-col gap-1">
            <span class="text-xs font-medium text-base-content/70">Model</span>
            <div class="flex w-full items-center gap-1">
              <input
                bind:value={summary.model}
                type="text"
                class="input input-sm w-full border-base-300 shadow-none"
                placeholder="endpoint default"
                list="llm-models-summary"
              />
              <button
                class="btn btn-ghost btn-sm shrink-0"
                type="button"
                disabled={summary.provider === "" || loadingModelsFor !== ""}
                on:click={() => loadModels(summary.provider)}
              >
                {#if loadingModelsFor !== "" && loadingModelsFor === summary.provider}
                  <span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
                {:else}
                  Load models
                {/if}
              </button>
            </div>
            <datalist id="llm-models-summary">
              {#each modelsByProvider[summary.provider] ?? [] as model (model.id)}
                <option value={model.id}>{model.name ?? model.id}</option>
              {/each}
            </datalist>
          </label>
          <p class="text-xs text-base-content/60">{effectiveLabel("summary")}</p>
        </section>
      </div>
      {#if modelsError}
        <div class="alert alert-warning text-sm">{modelsError}</div>
      {/if}

      <section class="grid content-start gap-2 rounded-box border border-base-300 bg-base-200 p-3">
        <h3 class="text-sm font-semibold">Request bounds</h3>
        <div class="grid gap-3 sm:grid-cols-2">
          <label class="flex w-full flex-col gap-1">
            <span class="text-xs font-medium text-base-content/70">Timeout (seconds)</span>
            <input
              bind:value={timeoutSec}
              type="number"
              min="0"
              class="input input-sm w-full border-base-300 shadow-none"
              placeholder="900"
            />
          </label>
          <label class="flex w-full flex-col gap-1">
            <span class="text-xs font-medium text-base-content/70">Response token limit</span>
            <input
              bind:value={maxTokens}
              type="number"
              min="0"
              class="input input-sm w-full border-base-300 shadow-none"
              placeholder="4096"
            />
          </label>
        </div>
        <p class="text-xs text-base-content/60">
          0 uses the default (900s / 4096 tokens). Raise the timeout for a CPU-bound self-hosted
          model.
        </p>
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
