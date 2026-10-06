<script lang="ts">
  import { onMount } from "svelte";
  import RecordingSetup from "../RecordingSetup.svelte";
  import { createPreviewClient, doctorScenarios } from "./doctorScenarios";

  export let scenario = "";
  const selected = doctorScenarios.find(item => item.id === scenario);
  const operatorClient = selected ? createPreviewClient(selected.id) : null;
  let notice = "";
  // Not a media query: the panel's own breakpoints are what a designer needs to
  // see, and the gallery cannot resize the window.
  let width = "";
  const fallbackTheme = window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "saturn-dark" : "saturn-light";
  let theme = "page";
  let root: HTMLDivElement;
  let themeHost: HTMLElement | null = null;
  let pageTheme: string | undefined;
  onMount(() => {
    const tree = root.getRootNode();
    if (!(tree instanceof ShadowRoot) || !(tree.host instanceof HTMLElement)) return;
    themeHost = tree.host;
    const previous = themeHost.dataset.ncTheme;
    pageTheme = previous;
    return () => {
      if (themeHost) {
        if (previous === undefined) delete themeHost.dataset.ncTheme;
        else themeHost.dataset.ncTheme = previous;
      }
    };
  });
  function changeTheme(event: Event) {
    const selected = (event.currentTarget as HTMLSelectElement).value;
    if (!themeHost) return;
    // Native Nextcloud colours are inherited from the surrounding page. Merely
    // changing its mode attribute cannot switch those inherited colours. For
    // explicit Cassini themes, opt out of that bridge within this shadow host.
    if (selected === "page" && pageTheme !== undefined) themeHost.dataset.ncTheme = pageTheme;
    else delete themeHost.dataset.ncTheme;
  }

</script>

<div bind:this={root} class="cassini-root h-full overflow-auto bg-base-200 text-base-content" data-theme={theme === "page" ? fallbackTheme : theme}>
  <main class="mx-auto max-w-5xl space-y-5 p-4 md:p-8">
    <header class="rounded-box border border-info bg-base-100 p-4">
      <h1 class="text-xl font-semibold">Doctor usability previews</h1>
      <p class="mt-2 text-sm">Example data rendered by the current Doctor component. Checks, saves and repairs are simulated in this tab; they do not change the server. Reload to reset.</p>
      <div class="mt-3 flex flex-wrap items-center gap-3">
        <!-- A switcher that stays on screen, so comparing two states is one
             click rather than a trip back to the index and in again. -->
        <label class="text-sm">Scenario
          <select class="select select-sm ml-2" aria-label="Preview scenario" value={scenario ?? ""}
            on:change={(event) => { window.location.hash = `doctor-preview=${(event.currentTarget as HTMLSelectElement).value}`; }}>
            <option value="">All scenarios…</option>
            {#each doctorScenarios as item}<option value={item.id}>{item.title}</option>{/each}
          </select>
        </label>
        <label class="text-sm">Theme
          <select class="select select-sm ml-2" aria-label="Preview theme" bind:value={theme} on:change={changeTheme}>
            <option value="page">Page theme</option><option value="saturn-light">Cassini light</option><option value="saturn-dark">Cassini dark</option>
          </select>
        </label>
        <!-- Narrow is the layout most likely to be wrong and least likely to be
             opened: the panel ships inside Nextcloud, where nobody resizes a
             browser to check it. -->
        <label class="text-sm">Width
          <select class="select select-sm ml-2" aria-label="Preview width" bind:value={width}>
            <option value="">Full</option><option value="420px">Phone (420px)</option><option value="760px">Tablet (760px)</option>
          </select>
        </label>
      </div>
    </header>
    {#if selected && operatorClient}
      <div>
        <h2 class="text-lg font-semibold">{selected.title}</h2>
        <p class="mt-1 text-sm text-base-content/70">{selected.description}</p>
      </div>
      <div style:max-width={width || null} class="op-settings {width ? 'rounded-box border border-dashed border-base-300 p-2' : ''}">
        <RecordingSetup {operatorClient} provisioningBase="https://preview.invalid/operator" on:openStorage={() => { notice = "This would open Publish pipeline in the live app. Server configuration is unavailable in this preview."; }} />
      </div>
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
