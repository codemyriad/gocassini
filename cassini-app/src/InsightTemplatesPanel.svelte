<script module lang="ts">
  // Enough hash to recognise a document by, with the whole of it a hover or a
  // copy away. A truncated hash is an identifier, not a checksum, which is why
  // the full value stays on the element it labels.
  export function shortHash(sha256: string): string {
    return sha256.length > 12 ? `${sha256.slice(0, 12)}…` : sha256;
  }
</script>

<script lang="ts">
  // Insight templates: the registry, rendered (D-718).
  //
  // Every row opens onto the instruction it actually sends — the system prompt
  // with its template spliced in, byte for byte — rather than a plain-language
  // paraphrase of it. A paraphrase reads better right up to the day it stops
  // matching the prompt, and nothing can guarantee that it tracks bytes; the
  // hash beside it names the bytes on the screen, and an insight document
  // records the same hash, so the two can be compared rather than trusted.
  //
  // Read-only, deliberately. Prompts are authored in the repository and
  // compiled into the image, a change is a new version rather than an edit, and
  // there is no PUT behind this panel to write one with.
  import { createEventDispatcher, onMount } from "svelte";
  import { FileText, RefreshCw } from "@lucide/svelte";
  import { OperatorClient } from "./operator/client";
  import NeedsProviderCard from "./NeedsProviderCard.svelte";
  import type { InsightWorkflow } from "./operator/types";

  const dispatch = createEventDispatcher<{ openProviders: void }>();

  // Handed in by Settings.svelte, exactly as the two sibling panels are: it
  // already builds the client and already renders the config error, so a second
  // one here would be a second place that decision could be made differently.
  export let operatorClient: OperatorClient | null = null;

  let client: OperatorClient | null = operatorClient;

  // Three states that must not be confused for each other: still loading, the
  // fetch failed, and this deployment ships no templates. Rendering an empty
  // list for the second would send an administrator looking for a feature that
  // is there.
  let workflows: InsightWorkflow[] = [];
  let loading = true;
  let loadError = "";

  // A template on its own does nothing: it is the starting point for a request
  // to a model, so the list reads as inert until there is one to send it to.
  // Three states again — null is "the question was never answered", and a panel
  // that could not read the AI settings must not accuse a configured
  // deployment of having no endpoint.
  let hasProvider: boolean | null = null;

  onMount(() => {
    void load();
  });

  async function load() {
    if (!client) {
      loading = false;
      return;
    }
    loading = true;
    loadError = "";
    try {
      workflows = await client.listInsightWorkflows();
    } catch (error) {
      loadError = error instanceof Error ? error.message : String(error);
    } finally {
      loading = false;
    }
    void loadProviders();
  }

  async function loadProviders() {
    if (!client) {
      return;
    }
    try {
      hasProvider = (await client.getLLMSettings()).providers.length > 0;
    } catch {
      // Swallowed: the templates are the page, and an unreadable AI setting
      // costs the locked notice and nothing else.
      hasProvider = null;
    }
  }
</script>

<header class="op-panel-head">
  <div>
    <div class="op-panel-title">
      <h1>Insight templates</h1>
    </div>
    <p>Used for insights and meeting summaries. For now, only the templates that come with Cassini are available.</p>
  </div>
  <div class="op-panel-actions">
    <button
      class="icon-btn"
      type="button"
      on:click={load}
      disabled={loading || !client}
      aria-label="Reload insight templates"
    >
      <RefreshCw size={15} aria-hidden="true" />
    </button>
  </div>
</header>

{#if hasProvider === false}
  <NeedsProviderCard
    title="Add a provider to use these templates"
    on:open={() => dispatch("openProviders")}
  />
{/if}
{#if loadError}
  <div class="err-box" role="alert">Templates could not be listed.</div>
{:else if loading}
  <p class="op-state">Loading insight templates…</p>
{:else if workflows.length === 0}
  <div class="op-empty">
    <FileText size={20} aria-hidden="true" />
    <p class="t">This build ships no templates.</p>
  </div>
{:else}
  <!-- Dimmed rather than hidden while there is no endpoint: the templates
       are real and worth reading, they just have nowhere to be sent. -->
  <section class="tpl-list">
    {#each workflows as workflow (workflow.id)}
      <article class="op-tint tpl-card" class:off={hasProvider === false}>
        <div class="set-row-main">
          <h3 class="set-row-name">{workflow.name}</h3>
          {#if workflow.description}
            <p class="set-row-sub">{workflow.description}</p>
          {/if}
        </div>

        {#if workflow.origin}
          <div class="tpl-origin">{workflow.origin}</div>
        {/if}

        <!-- The question is the affordance: it says what the template asks,
             and opening it says how it asks for it. A template with no
             question of its own (the asker's) opens on a plain label. -->
        <details class="tpl-prompt">
          <summary class="tpl-toggle">
            <span class="tpl-chev" aria-hidden="true"></span>
            {#if workflow.question}“{workflow.question}”{:else}Prompt{/if}
          </summary>
          <div class="tpl-body">
            <pre class="tpl-instruction">{workflow.instruction}</pre>
            <p class="tpl-ident">
              <code>{workflow.id}</code>
              <code>{workflow.version}</code>
              <code title={workflow.sha256}>{shortHash(workflow.sha256)}</code>
            </p>
          </div>
        </details>
      </article>
    {/each}
  </section>
{/if}

<style>
  .tpl-list {
    display: grid;
    gap: 12px;
  }
  .tpl-card {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-start;
    gap: 0 14px;
    padding: 14px 16px;
  }
  .tpl-prompt {
    flex-basis: 100%;
    min-width: 0;
    margin-top: 8px;
  }
  .tpl-body {
    margin-top: 9px;
  }
  .tpl-ident {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin: 10px 0 0;
  }
  .tpl-ident code {
    padding: 2px 6px;
    font-family: var(--font-mono);
    font-size: 11.5px;
    font-weight: 500;
    line-height: 1.5;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
    background-color: var(--op-code-bg);
    border: 1px solid var(--op-code-border);
    border-radius: var(--radius-selector, 0.25rem);
  }
  .tpl-instruction {
    max-height: 24rem;
    margin: 0;
    padding: 8px 12px 8px 11px;
    background-color: var(--op-inset);
    border-left: 2px solid var(--color-base-300);
    border-radius: 0 var(--radius-field, 0.5rem) var(--radius-field, 0.5rem) 0;
    font-size: 12.5px;
    line-height: 1.6;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
    overflow: auto;
    font-family: inherit;
    white-space: pre-wrap;
  }
  .tpl-origin {
    flex: none;
    margin-top: 1px;
    padding: 2px 7px;
    font-size: 11px;
    font-weight: 500;
    line-height: 1.4;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-selector, 0.25rem);
  }
</style>
