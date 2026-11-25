<script lang="ts">
  // Operator › Storage: how many meeting imports run at once. A value saved
  // here is kept on the operator's volume, applies without a restart and
  // survives redeploys; until one is saved, the deploy setting or the default
  // applies.
  import { onMount } from "svelte";
  import { LL } from "cassini-viewer/i18n";
  import type { ImportSettings, OperatorClient } from "./operator/client";
  export let operatorClient: OperatorClient | null = null;

  let settings: ImportSettings | null = null;
  let value = "";
  let busy = false, error = "", notice = "", loadError = "";
  let alive = true;

  function accept(next: ImportSettings) {
    settings = next;
    value = String(next.max_concurrent_imports);
  }
  async function load() {
    if (!operatorClient) return;
    busy = true; loadError = "";
    try { accept(await operatorClient.getImportSettings()); }
    catch (e) { if (alive) loadError = e instanceof Error ? e.message : String(e); }
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
  $: source = settings?.source === "admin"
    ? $LL.app.settings.imports.source_admin()
    : settings?.source === "deploy" ? $LL.app.settings.imports.source_deploy() : $LL.app.settings.imports.source_default();
  onMount(() => { void load(); return () => { alive = false; }; });
</script>

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
          bind:value aria-invalid={!valid} aria-describedby="import-settings-source import-settings-help" disabled={busy} />
        <button type="submit" class="btn btn-primary btn-sm" aria-label={$LL.app.settings.imports.save_label()} disabled={busy || !valid || !dirty}>{$LL.common.action.save()}</button>
      </label>
      {#if !valid}<p class="text-sm text-error" role="alert">{$LL.app.settings.imports.out_of_range({ max: settings.max })}</p>{/if}
      <p id="import-settings-source" class="text-sm">{source}</p>
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

<style>
  .import-settings { display: grid; gap: 16px; min-width: 0; }
</style>
