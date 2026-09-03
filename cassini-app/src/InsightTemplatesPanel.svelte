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
  import { onMount } from "svelte";
  import { FileText, RefreshCw } from "@lucide/svelte";
  import { OperatorClient } from "./operator/client";
  import type { InsightWorkflow } from "./operator/types";

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
  }
</script>

<section class="rounded-box border border-base-300 bg-base-100 shadow-sm">
  <header class="flex items-start justify-between gap-3 px-4 py-3">
    <div>
      <h2 class="font-semibold">Insight templates</h2>
      <p class="text-xs text-base-content/60">
        Used when creating an insight, and by the summary step in the publish pipeline. Authoring
        your own and pulling them from shared repositories comes later — for now these ship with
        Cassini.
      </p>
    </div>
    <button
      class="btn btn-ghost btn-sm btn-square"
      type="button"
      on:click={load}
      disabled={loading || !client}
      aria-label="Reload insight templates"
    >
      <RefreshCw size={16} aria-hidden="true" />
    </button>
  </header>

  <div class="border-t border-base-300 p-4">
    {#if loadError}
      <div class="grid gap-2">
        <div class="alert alert-error text-sm">{loadError}</div>
        <p class="text-xs text-base-content/60">
          This says the template list could not be read, not that Cassini ships none. The templates
          are compiled into the recorder image; the panel asks it what they are.
        </p>
      </div>
    {:else if loading}
      <div class="flex items-center justify-center p-6 text-sm text-base-content/60">
        Loading insight templates…
      </div>
    {:else if workflows.length === 0}
      <div
        class="grid content-start justify-items-center gap-2 rounded-box border border-base-300 bg-base-200 px-4 py-8 text-center"
      >
        <FileText size={20} class="text-base-content/40" aria-hidden="true" />
        <p class="text-sm font-medium">This build ships no templates.</p>
        <p class="max-w-md text-xs text-base-content/60">
          The registry answered, and it is empty. A template whose prompt is not compiled into this
          image is not listed here and cannot be run, so nothing is being hidden from you.
        </p>
      </div>
    {:else}
      <div class="grid gap-2">
        {#each workflows as workflow (workflow.id)}
          <article class="rounded-box border border-base-300 bg-base-200 p-3">
            <div class="flex flex-wrap items-start justify-between gap-2">
              <h3 class="text-sm font-semibold">{workflow.name}</h3>
              <div
                class="flex flex-wrap items-center gap-x-2 gap-y-1 font-mono text-xs text-base-content/60"
              >
                <span>{workflow.id}</span>
                <span aria-hidden="true">·</span>
                <span>{workflow.version}</span>
                <span aria-hidden="true">·</span>
                <span title={workflow.sha256}>{shortHash(workflow.sha256)}</span>
                {#if workflow.origin}
                  <span class="badge badge-ghost badge-sm font-sans">{workflow.origin}</span>
                {/if}
              </div>
            </div>

            {#if workflow.description}
              <p class="mt-1 text-xs text-base-content/60">{workflow.description}</p>
            {/if}

            <!-- The question is the affordance: it says what the template asks,
                 and opening it says how it asks for it. -->
            <details class="mt-2">
              <summary class="cursor-pointer text-sm">“{workflow.question}”</summary>
              <pre
                class="mt-2 max-h-96 overflow-auto rounded-box border border-base-300 bg-base-100 p-3 text-xs whitespace-pre-wrap">{workflow.instruction}</pre>
            </details>
          </article>
        {/each}
      </div>
    {/if}
  </div>
</section>
