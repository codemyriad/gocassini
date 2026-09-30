<script lang="ts">
  import { onDestroy } from "svelte";
  import RecordingSetup from "../RecordingSetup.svelte";
  import { createPreviewClient, doctorScenarios } from "./doctorScenarios";

  export let scenario = "";
  const selected = doctorScenarios.find(item => item.id === scenario);
  const operatorClient = selected ? createPreviewClient(selected.id) : null;
  let notice = "";
  let theme = window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "saturn-dark" : "saturn-light";

  // The advanced handoff instructions must not generate a live provisioning
  // URL. The gallery never mounts App, probes admin access or reads real data.
  const liveConfig = window.__CASSINI_CONFIG__;
  window.__CASSINI_CONFIG__ = { operatorBasePath: "https://preview.invalid/operator" };
  onDestroy(() => { window.__CASSINI_CONFIG__ = liveConfig; });
</script>

<div class="cassini-root h-full overflow-auto bg-base-200 text-base-content" data-theme={theme}>
  <main class="mx-auto max-w-5xl space-y-5 p-4 md:p-8">
    <header class="rounded-box border border-info bg-base-100 p-4">
      <h1 class="text-xl font-semibold">Doctor usability previews</h1>
      <p class="mt-2 text-sm">Example data rendered by the current Doctor component. Checks, saves and repairs are simulated in this tab; they do not change the server. Reload to reset.</p>
      <div class="mt-3 flex flex-wrap items-center gap-3">
        <a class="link" href="#doctor-preview=">All scenarios</a>
        <label class="text-sm">Theme
          <select class="select select-sm ml-2" bind:value={theme}>
            <option value="saturn-light">Light</option><option value="saturn-dark">Dark</option>
          </select>
        </label>
      </div>
    </header>
    {#if selected && operatorClient}
      <div>
        <h2 class="text-lg font-semibold">{selected.title}</h2>
        <p class="mt-1 text-sm text-base-content/70">{selected.description}</p>
      </div>
      <RecordingSetup {operatorClient} on:openStorage={() => { notice = "This would open Publish pipeline in the live app. Server configuration is unavailable in this preview."; }} />
      {#if notice}<p class="rounded-box bg-base-100 p-4 text-sm" role="status">{notice}</p>{/if}
    {:else}
      {#if scenario}<p role="alert">Unknown preview scenario. Choose one below.</p>{/if}
      <ul class="grid gap-3 md:grid-cols-2">
        {#each doctorScenarios as item}
          <li class="rounded-box border border-base-300 bg-base-100 p-4">
            <a class="link font-semibold" href={`#doctor-preview=${item.id}`}>{item.title}</a>
            <p class="mt-1 text-sm text-base-content/70">{item.description}</p>
          </li>
        {/each}
      </ul>
    {/if}
  </main>
</div>
