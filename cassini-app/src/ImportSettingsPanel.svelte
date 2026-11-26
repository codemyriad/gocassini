<script lang="ts">
  // Operator › Storage: how many meeting imports run at once. A value saved
  // here is kept on the operator's volume, applies without a restart and
  // survives redeploys; until one is saved, the default applies. A server that
  // does not serve imports answers 404, and the card is not shown.
  import { onMount } from "svelte";
  import { LL } from "cassini-viewer/i18n";
  import { OperatorHttpError, type ImportSettings, type OperatorClient } from "./operator/client";
  export let operatorClient: OperatorClient | null = null;

  let settings: ImportSettings | null = null;
  let value = "";
  let busy = false, error = "", notice = "", loadError = "";
  let unavailable = false;
  let alive = true;

  function accept(next: ImportSettings) {
    settings = next;
    value = String(next.max_concurrent_imports);
  }
  async function load() {
    if (!operatorClient) return;
    busy = true; loadError = "";
    try { accept(await operatorClient.getImportSettings()); }
    catch (e) {
      if (!alive) return;
      if (e instanceof OperatorHttpError && e.status === 404) unavailable = true;
      else loadError = e instanceof Error ? e.message : String(e);
    }
    finally { busy = false; }
  }
  $: parsed = /^\s*\d+\s*$/.test(value) ? Number(value) : NaN;
  $: valid = !!settings && Number.isInteger(parsed) && parsed >= 1 && parsed <= settings.max;
  $: dirty = !!settings && parsed !== settings.max_concurrent_imports;
  $: if (dirty) notice = "";
  async function save() {
    if (!operatorClient || !settings || busy || !valid) return;
    busy = true; error = ""; notice = "";
    try {
      accept(await operatorClient.putImportSettings(parsed));
      if (alive) notice = $LL.app.settings.imports.saved();
    } catch (e) { if (alive) error = e instanceof Error ? e.message : String(e); }
    finally { busy = false; }
  }
  onMount(() => { void load(); return () => { alive = false; }; });
</script>

{#if !unavailable}
<section class="import-settings" id="import-settings" aria-labelledby="import-settings-heading">
  <div class="grid gap-1">
    <h2 id="import-settings-heading" class="text-xl font-semibold">{$LL.app.settings.imports.heading()}</h2>
    <p class="text-sm opacity-80">{$LL.app.settings.imports.intro()}</p>
  </div>
  {#if settings}
    <form class="op-tint p-4 grid gap-2" on:submit|preventDefault={save}>
      <label class="flex flex-wrap items-center gap-2 text-sm">
        <span class="font-semibold">{$LL.app.settings.imports.limit_label()}</span>
        <input class="input input-bordered w-24" type="number" min="1" max={settings.max} step="1" inputmode="numeric"
          bind:value aria-invalid={!valid} aria-describedby="import-settings-range import-settings-default import-settings-help" disabled={busy} />
        <span id="import-settings-range" class="opacity-80">{$LL.app.settings.imports.range({ max: settings.max })}</span>
        <button type="submit" class="btn btn-primary btn-sm" aria-label={$LL.app.settings.imports.save_label()} disabled={busy || !valid || !dirty}>{$LL.common.action.save()}</button>
      </label>
      {#if !valid}<p class="text-sm text-error" role="alert">{$LL.app.settings.imports.out_of_range({ max: settings.max })}</p>{/if}
      <p id="import-settings-default" class="text-sm">{$LL.app.settings.imports.default_hint({ value: settings.default })}</p>
      <p id="import-settings-help" class="text-sm opacity-80">{$LL.app.settings.imports.help()}</p>
      {#if error}<p class="text-sm text-error" role="alert">{error}</p>{/if}
      {#if notice}<p class="text-sm" role="status">{notice}</p>{/if}
    </form>
  {:else if loadError}
    <p class="text-sm text-error" role="alert">{$LL.app.settings.imports.load_failed({ detail: loadError })}</p>
  {:else if busy}
    <p role="status">{$LL.app.settings.imports.loading()}</p>
  {/if}
</section>
{/if}

<style>
  .import-settings { display: grid; gap: 16px; min-width: 0; }
</style>
