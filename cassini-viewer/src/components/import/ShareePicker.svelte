<script lang="ts">
  // A small people/groups/Teams combobox over the provider's sharee search.
  // Deliberately minimal: type, pick, repeat — the chips beside it are the
  // state, so the picker itself holds nothing but the current query.
  import { createEventDispatcher, onDestroy } from "svelte";
  import type { ImportSharee } from "../../viewer/imports";

  export let search: (query: string) => Promise<ImportSharee[]>;
  export let placeholder = "Add a person or group…";

  const dispatch = createEventDispatcher<{ pick: ImportSharee }>();

  let query = "";
  let results: ImportSharee[] = [];
  let open = false;
  let failed = false;
  let searching = false;
  let debounce: ReturnType<typeof setTimeout> | null = null;
  let requestSequence = 0;

  function handleInput() {
    if (debounce !== null) clearTimeout(debounce);
    const value = query.trim();
    if (value === "") {
      open = false;
      results = [];
      return;
    }
    debounce = setTimeout(() => void run(value), 250);
  }

  async function run(value: string) {
    const sequence = ++requestSequence;
    searching = true;
    try {
      const found = await search(value);
      if (sequence !== requestSequence) return;
      results = found;
      failed = false;
      open = true;
    } catch {
      if (sequence !== requestSequence) return;
      results = [];
      failed = true;
      open = true;
    } finally {
      if (sequence === requestSequence) searching = false;
    }
  }

  function pick(sharee: ImportSharee) {
    dispatch("pick", sharee);
    query = "";
    results = [];
    open = false;
  }

  function kindLabel(sharee: ImportSharee): string {
    return sharee.type === "user" ? "" : sharee.type === "group" ? "group" : "team";
  }

  onDestroy(() => {
    if (debounce !== null) clearTimeout(debounce);
  });
</script>

<div class="sharee-picker">
  <input
    type="text"
    class="input input-sm input-bordered w-52 max-w-full"
    {placeholder}
    bind:value={query}
    on:input={handleInput}
    on:keydown={(event) => {
      if (event.key === "Escape") {
        open = false;
      } else if (event.key === "Enter" && results.length > 0) {
        event.preventDefault();
        pick(results[0]);
      }
    }}
    on:blur={() => setTimeout(() => (open = false), 150)}
    role="combobox"
    aria-expanded={open}
    aria-label={placeholder}
  />
  {#if open}
    <ul class="sharee-results menu menu-sm" role="listbox">
      {#if failed}
        <li class="px-3 py-1.5 text-sm opacity-70">Could not search people right now.</li>
      {:else if results.length === 0}
        <li class="px-3 py-1.5 text-sm opacity-70">{searching ? "Searching…" : "Nobody matches that."}</li>
      {:else}
        {#each results as sharee (sharee.type + sharee.id)}
          <li>
            <button type="button" role="option" aria-selected="false" on:mousedown|preventDefault={() => pick(sharee)}>
              <span class="truncate">{sharee.label}</span>
              {#if kindLabel(sharee)}
                <span class="badge badge-xs badge-ghost">{kindLabel(sharee)}</span>
              {/if}
            </button>
          </li>
        {/each}
      {/if}
    </ul>
  {/if}
</div>

<style>
  .sharee-picker {
    position: relative;
    display: inline-block;
  }
  .sharee-results {
    position: absolute;
    z-index: 30;
    top: calc(100% + 4px);
    left: 0;
    min-width: 220px;
    max-height: 240px;
    overflow-y: auto;
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-box, 12px);
    background: var(--color-base-100);
    box-shadow: 0 8px 24px oklch(0% 0 0 / 0.18);
  }
</style>
