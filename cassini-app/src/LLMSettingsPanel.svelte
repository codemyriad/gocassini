<script lang="ts">
  import { onMount } from "svelte";
  import { Bot, Plus, RefreshCw, Trash2 } from "@lucide/svelte";
  import { OperatorClient, OperatorHttpError } from "./operator/client";
  import type {
    InsightWorkflow,
    LLMEffectiveStep,
    LLMModel,
    LLMProviderUpdate,
    LLMSettings,
    LLMStep,
  } from "./operator/types";

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
    // null renders the input empty so the placeholder shows the default.
    timeoutSec: number | null;
    maxTokens: number | null;
  }

  let settings: LLMSettings | null = null;
  let providers: ProviderRow[] = [];
  let summary: LLMStep = emptyStep();
  let insight: LLMStep = emptyStep();
  let snapshot = "";

  function emptyStep(): LLMStep {
    return { enabled: false, provider: "", model: "", template: "" };
  }

  let loading = true;
  let saving = false;
  let loadError = "";
  let saveError = "";

  // Fetched model lists per provider id, feeding the datalists. Fetching is a
  // convenience: model fields always accept free text.
  let modelsByProvider: Record<string, LLMModel[]> = {};
  let modelsError = "";
  let loadingModelsFor = "";

  // The workflows this build actually ships (D-718's GET /settings/workflows).
  // A step stores its workflow as a plain id and the operator validates only
  // the shape of it — it is a separate Go module and cannot see the registry —
  // so this panel is the only place a typo can be caught before it is saved.
  // The list is advisory, not a gate: the field stays free text so an id from a
  // newer recorder image can still be typed, and a failed fetch must not stop
  // an administrator configuring an endpoint. It only lets the panel say when
  // an id will not resolve.
  let knownWorkflows: InsightWorkflow[] = [];
  let workflowsKnown = false;

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
    void loadWorkflows();
  }

  async function loadWorkflows() {
    if (!operatorClient) {
      return;
    }
    try {
      knownWorkflows = await operatorClient.listInsightWorkflows();
      workflowsKnown = true;
    } catch {
      // Swallowed on purpose. Not knowing the registry costs the warning below
      // and nothing else; reporting it beside the endpoint errors would put a
      // failure in front of an administrator who came here to fix a different
      // one.
      workflowsKnown = false;
    }
  }

  // Empty means "the workflow Cassini ships", which is what every policy
  // written before the field existed reads as, so it is never unknown.
  function unknownWorkflow(id: string, known: InsightWorkflow[], answered: boolean): boolean {
    return answered && id.trim() !== "" && !known.some((w) => w.id === id.trim());
  }

  // Reactive rather than called from the markup: the registry arrives after the
  // settings do, and a template expression naming only `summary` would never be
  // re-evaluated when it lands.
  $: summaryWorkflowUnknown = unknownWorkflow(summary.template, knownWorkflows, workflowsKnown);
  $: insightWorkflowUnknown = unknownWorkflow(insight.template, knownWorkflows, workflowsKnown);

  // Named ids rather than "unknown workflow": the remedy is one of them, and
  // an administrator should not have to open another panel to find out which.
  $: unknownWorkflowWarning =
    `This build ships no workflow with that id, so a run naming it would refuse to start. ` +
    `It ships: ${knownWorkflows.map((w) => w.id).join(", ")}.`;

  function apply(next: LLMSettings) {
    settings = next;
    providers = next.providers.map((p) => ({
      id: p.id,
      name: p.name,
      baseUrl: p.base_url,
      keyConfigured: p.api_key_configured,
      keyInput: "",
      keyCleared: false,
      timeoutSec: p.timeout_sec > 0 ? p.timeout_sec : null,
      maxTokens: p.max_tokens > 0 ? p.max_tokens : null,
    }));
    summary = { ...next.summary };
    insight = { ...next.insight };
    snapshot = JSON.stringify({ providers, summary, insight });
  }

  function newProviderId(): string {
    const bytes = new Uint8Array(4);
    crypto.getRandomValues(bytes);
    return "p-" + Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
  }

  function addProvider() {
    providers = [
      ...providers,
      {
        id: newProviderId(),
        name: "",
        baseUrl: "",
        keyConfigured: false,
        keyInput: "",
        keyCleared: false,
        timeoutSec: null,
        maxTokens: null,
      },
    ];
  }

  function removeProvider(id: string) {
    providers = providers.filter((p) => p.id !== id);
    // A step pointing at the removed endpoint cannot stay enabled — the server
    // would reject it; disable it and forget the reference instead.
    if (summary.provider === id) {
      summary = { ...summary, provider: "", enabled: false };
    }
    if (insight.provider === id) {
      insight = { ...insight, provider: "", enabled: false };
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
            const update: LLMProviderUpdate = {
              id: row.id,
              name: row.name,
              base_url: row.baseUrl,
              timeout_sec: row.timeoutSec ?? 0,
              max_tokens: row.maxTokens ?? 0,
            };
            if (row.keyCleared) {
              update.api_key = "";
            } else if (row.keyInput !== "") {
              update.api_key = row.keyInput;
            }
            return update;
          }),
          summary,
          insight,
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

  function endpointLabel(effective: LLMEffectiveStep): string {
    return `${effective.base_url} · ${effective.model || "endpoint default model"}`;
  }

  function summaryEffectiveLabel(): string {
    const effective = settings?.effective.summary;
    if (!effective) {
      return "Currently off — meetings publish without a summary.";
    }
    return `Currently: ${endpointLabel(effective)}`;
  }

  // Three outcomes, not two. An insight step with no endpoint of its own is
  // not off — the recorder layers INSIGHT_* over SUMMARY_* — and an admin who
  // cannot tell the inherited case from the owned one cannot predict what
  // happens when the summary endpoint is repointed (D-719).
  function insightEffectiveLabel(): string {
    const effective = settings?.effective.insight;
    if (!effective) {
      return "No endpoint configured — an insight has nothing to ask.";
    }
    if (effective.inherited) {
      return `Inherits the meeting-summary endpoint: ${endpointLabel(effective)} — and moves with it.`;
    }
    return `Currently: ${endpointLabel(effective)}`;
  }

  $: current = JSON.stringify({ providers, summary, insight });
  $: isDirty = settings !== null && current !== snapshot;
</script>

<section class="rounded-box border border-base-300 bg-base-100 shadow-sm">
  <header class="flex items-center justify-between gap-3 px-4 py-3">
    <div>
      <h2 class="font-semibold">AI providers</h2>
      <p class="text-xs text-base-content/60">
        Which model endpoint each step runs on. The full transcript is sent to that endpoint;
        summaries and insights can use different ones, so a small local model can write every
        meeting's summary while a larger one answers a question you ask by hand.
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
            <label class="flex w-full flex-col gap-1">
              <span class="text-xs font-medium text-base-content/70">Request timeout (s)</span>
              <input
                bind:value={row.timeoutSec}
                type="number"
                min="1"
                class="input input-sm w-full border-base-300 shadow-none"
                placeholder="900 (default)"
              />
            </label>
            <label class="flex w-full flex-col gap-1">
              <span class="text-xs font-medium text-base-content/70">Response token limit</span>
              <input
                bind:value={row.maxTokens}
                type="number"
                min="1"
                class="input input-sm w-full border-base-300 shadow-none"
                placeholder="4096 (default)"
              />
            </label>
          </div>
        {/each}
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
        <label class="flex w-full flex-col gap-1">
          <span class="text-xs font-medium text-base-content/70">Workflow</span>
          <input
            bind:value={summary.template}
            type="text"
            class="input input-sm w-full border-base-300 shadow-none"
            placeholder="summarise (the one Cassini ships)"
            list="llm-workflows"
          />
          {#if summaryWorkflowUnknown}
            <span class="text-xs text-warning">{unknownWorkflowWarning}</span>
          {/if}
          <!-- The field is saved and served; the publish pipeline does not read
               it back yet. Saying so beats a control that silently does nothing
               — delete this line when the pipeline resolves it (D-719). -->
          <span class="text-xs text-base-content/50">
            Saved with the policy. The publish pipeline still runs the workflow Cassini ships.
          </span>
        </label>
        <p class="text-xs text-base-content/60">{summaryEffectiveLabel()}</p>
      </section>

      <section class="grid content-start gap-2 rounded-box border border-base-300 bg-base-200 p-3">
        <label class="flex cursor-pointer items-center gap-2">
          <input type="checkbox" class="toggle toggle-primary toggle-sm" bind:checked={insight.enabled} />
          <h3 class="text-sm font-semibold">Insights</h3>
        </label>
        <p class="text-xs text-base-content/60">
          A question asked of several meetings at once. Off means insights run on the meeting-summary
          endpoint above; turn it on to send them somewhere else — a larger model, or one you are
          willing to wait longer for. There is no setting here that stops insights running: that is
          removing the endpoint.
        </p>
        {#if insight.enabled}
          <label class="flex w-full flex-col gap-1">
            <span class="text-xs font-medium text-base-content/70">Endpoint</span>
            <select bind:value={insight.provider} class="select select-sm w-full border-base-300 shadow-none">
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
                bind:value={insight.model}
                type="text"
                class="input input-sm w-full border-base-300 shadow-none"
                placeholder="endpoint default"
                list="llm-models-insight"
              />
              <button
                class="btn btn-ghost btn-sm shrink-0"
                type="button"
                disabled={insight.provider === "" || loadingModelsFor !== ""}
                on:click={() => loadModels(insight.provider)}
              >
                {#if loadingModelsFor !== "" && loadingModelsFor === insight.provider}
                  <span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
                {:else}
                  Load models
                {/if}
              </button>
            </div>
            <datalist id="llm-models-insight">
              {#each modelsByProvider[insight.provider] ?? [] as model (model.id)}
                <option value={model.id}>{model.name ?? model.id}</option>
              {/each}
            </datalist>
          </label>
        {/if}
        <label class="flex w-full flex-col gap-1">
          <span class="text-xs font-medium text-base-content/70">Workflow</span>
          <input
            bind:value={insight.template}
            type="text"
            class="input input-sm w-full border-base-300 shadow-none"
            placeholder="summarise (the one Cassini ships)"
            list="llm-workflows"
          />
          {#if insightWorkflowUnknown}
            <span class="text-xs text-warning">{unknownWorkflowWarning}</span>
          {/if}
          <span class="text-xs text-base-content/50">
            Used as the default for in-app insight runs; an individual run can still choose another
            workflow.
          </span>
        </label>
        <!-- One list for both steps: the registry is what the recorder ships,
             not a property of a step. Insight templates renders the same set in
             full, with each one's instruction. -->
        <datalist id="llm-workflows">
          {#each knownWorkflows as workflow (workflow.id)}
            <option value={workflow.id}>{workflow.name}</option>
          {/each}
        </datalist>
        <p class="text-xs text-base-content/60">{insightEffectiveLabel()}</p>
      </section>
      {#if modelsError}
        <div class="alert alert-warning text-sm">{modelsError}</div>
      {/if}

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
