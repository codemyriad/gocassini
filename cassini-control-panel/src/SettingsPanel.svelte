<script lang="ts">
  import { onMount } from "svelte";
  import { CheckCircle2, Cpu, RefreshCw, SlidersHorizontal } from "@lucide/svelte";
  import { OperatorClient, OperatorHttpError } from "./operator/client";
  import type { Settings, SettingsQuality } from "./operator/types";

  export let operatorClient: OperatorClient | null = null;

  interface QualityOption {
    value: SettingsQuality;
    label: string;
    description: string;
  }

  const QUALITY_OPTIONS: QualityOption[] = [
    {
      value: "fast",
      label: "Fast",
      description: "Fastest transcription, lower accuracy.",
    },
    {
      value: "balanced",
      label: "Balanced",
      description: "Default — good accuracy at reasonable speed.",
    },
    {
      value: "best",
      label: "Best",
      description: "Highest accuracy (fp32), slowest.",
    },
  ];

  let settings: Settings | null = null;
  let quality: SettingsQuality = "balanced";
  let deviceOverride = "";
  let modelOverride = "";
  let showAdvanced = false;

  let loading = true;
  let saving = false;
  let loadError = "";
  let saveError = "";
  let saveSuccess = false;

  onMount(() => {
    void loadSettings();
  });

  async function loadSettings() {
    if (!operatorClient) {
      return;
    }
    loading = true;
    loadError = "";
    saveError = "";
    saveSuccess = false;
    try {
      const next = await operatorClient.getSettings();
      applySettings(next);
    } catch (error) {
      loadError = asMessage(error);
    } finally {
      loading = false;
    }
  }

  async function handleSave() {
    if (!operatorClient || saving) {
      return;
    }
    saving = true;
    saveError = "";
    saveSuccess = false;
    try {
      const next = await operatorClient.putSettings({
        quality,
        device_override: deviceOverride,
        model_override: modelOverride,
      });
      applySettings(next);
      saveSuccess = true;
    } catch (error) {
      saveError = asMessage(error);
    } finally {
      saving = false;
    }
  }

  function applySettings(next: Settings) {
    settings = next;
    quality = next.quality;
    deviceOverride = next.device_override;
    modelOverride = next.model_override;
    if (next.device_override !== "" || next.model_override !== "") {
      showAdvanced = true;
    }
  }

  function asMessage(error: unknown): string {
    if (error instanceof OperatorHttpError) {
      return error.message;
    }
    return error instanceof Error ? error.message : String(error);
  }

  function sourceLabel(source: string): string {
    if (source === "user") {
      return "User override";
    }
    if (source === "auto") {
      return "Auto-detected";
    }
    return source || "—";
  }

  // Clear the save confirmation as soon as the admin edits a field so a stale
  // "saved" badge never lingers over unsaved changes.
  $: quality, deviceOverride, modelOverride, (saveSuccess = false);
</script>

<section class="rounded-box border border-base-300 bg-base-100 shadow-sm">
  <header class="flex items-center justify-between gap-3 border-b border-base-300 px-4 py-3">
    <div class="flex items-center gap-2">
      <SlidersHorizontal size={16} aria-hidden="true" />
      <div>
        <h2 class="font-semibold">Transcription quality</h2>
        <p class="text-xs text-base-content/60">
          STT settings from <code>GET /settings</code>, saved via <code>PUT /settings</code>.
        </p>
      </div>
    </div>
    <button
      class="btn btn-ghost btn-sm"
      type="button"
      on:click={loadSettings}
      disabled={loading || !operatorClient}
      aria-label="Reload settings"
    >
      <RefreshCw size={16} aria-hidden="true" />
    </button>
  </header>

  {#if loadError}
    <div class="px-4 py-4">
      <div class="alert alert-error text-sm">{loadError}</div>
    </div>
  {:else if loading}
    <div class="flex items-center justify-center p-6 text-sm text-base-content/60">Loading settings…</div>
  {:else if !settings}
    <div class="flex items-center justify-center p-6 text-sm text-base-content/60">No settings available.</div>
  {:else}
    <div class="grid gap-4 p-4">
      <section class="grid gap-2 rounded-box border border-base-300 bg-base-200/50 p-4">
        <div class="flex items-center gap-2">
          <Cpu size={16} aria-hidden="true" />
          <h3 class="font-semibold">Detected hardware</h3>
        </div>
        <dl class="grid gap-3 sm:grid-cols-3">
          <div>
            <dt class="text-xs uppercase tracking-wide text-base-content/60">GPU</dt>
            <dd class="text-sm">
              {#if settings.detected_gpu}
                <span class="badge badge-success badge-outline">Yes</span>
              {:else}
                <span class="badge badge-neutral badge-outline">No</span>
              {/if}
            </dd>
          </div>
          <div>
            <dt class="text-xs uppercase tracking-wide text-base-content/60">CPU cores</dt>
            <dd class="text-sm">{settings.cores}</dd>
          </div>
          <div>
            <dt class="text-xs uppercase tracking-wide text-base-content/60">Source</dt>
            <dd class="text-sm">
              <span class="badge badge-outline">{sourceLabel(settings.source)}</span>
            </dd>
          </div>
        </dl>
      </section>

      <fieldset class="grid gap-2">
        <legend class="label-text font-medium">Quality</legend>
        <div class="grid gap-2">
          {#each QUALITY_OPTIONS as option}
            <label
              class="flex cursor-pointer items-start gap-3 rounded-box border border-base-300 p-3 transition hover:border-primary {quality === option.value ? 'border-primary bg-primary/5' : ''}"
            >
              <input
                type="radio"
                name="stt-quality"
                class="radio radio-primary mt-0.5"
                value={option.value}
                bind:group={quality}
              />
              <span class="min-w-0">
                <span class="block text-sm font-medium">{option.label}</span>
                <span class="block text-xs text-base-content/60">{option.description}</span>
              </span>
            </label>
          {/each}
        </div>
      </fieldset>

      <div class="collapse collapse-arrow rounded-box border border-base-300 bg-base-100">
        <input type="checkbox" bind:checked={showAdvanced} />
        <div class="collapse-title text-sm font-medium">Advanced</div>
        <div class="collapse-content">
          <div class="grid gap-3">
            <label class="form-control w-full gap-2">
              <span class="label-text font-medium">Device override</span>
              <select bind:value={deviceOverride} class="select select-bordered w-full">
                <option value="">Auto</option>
                <option value="cpu">CPU</option>
                <option value="cuda">CUDA</option>
              </select>
            </label>
            <label class="form-control w-full gap-2">
              <span class="label-text font-medium">Model override</span>
              <input
                bind:value={modelOverride}
                type="text"
                class="input input-bordered w-full"
                placeholder="(use default for selected quality)"
              />
            </label>
          </div>
        </div>
      </div>

      <div class="flex flex-wrap items-center justify-between gap-3">
        <button class="btn btn-primary" type="button" disabled={saving} on:click={handleSave}>
          {#if saving}
            <span class="loading loading-spinner loading-sm" aria-hidden="true"></span>
            Saving…
          {:else}
            Save
          {/if}
        </button>
        {#if saveSuccess}
          <span class="flex items-center gap-2 text-sm text-success">
            <CheckCircle2 size={16} aria-hidden="true" />
            Settings saved.
          </span>
        {/if}
      </div>

      {#if saveError}
        <div class="alert alert-error text-sm">{saveError}</div>
      {/if}
    </div>
  {/if}
</section>
