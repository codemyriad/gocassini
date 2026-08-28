<script lang="ts">
  import { onMount } from "svelte";
  import { Cpu, CircleGauge, RefreshCw, Settings as SettingsIcon } from "@lucide/svelte";
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
  let transcriptionTermsText = "";

  let savedQuality: SettingsQuality = "balanced";
  let savedDeviceOverride = "";
  let savedModelOverride = "";
  let savedTranscriptionTermsText = "";

  let loading = true;
  let saving = false;
  let loadError = "";
  let saveError = "";

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
    try {
      const next = await operatorClient.putSettings({
        quality,
        device_override: deviceOverride,
        model_override: modelOverride,
        transcription_terms: transcriptionTermsText.split(/\r?\n/),
      });
      applySettings(next);
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
    transcriptionTermsText = next.transcription_terms.join("\n");
    savedQuality = next.quality;
    savedDeviceOverride = next.device_override;
    savedModelOverride = next.model_override;
    savedTranscriptionTermsText = transcriptionTermsText;
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

  $: isDirty =
    settings !== null &&
    (quality !== savedQuality ||
      deviceOverride !== savedDeviceOverride ||
      modelOverride !== savedModelOverride ||
      transcriptionTermsText !== savedTranscriptionTermsText);
</script>

<section class="rounded-box border border-base-300 bg-base-100 shadow-sm">
  <header class="flex items-center justify-between gap-3 px-4 py-3">
    <div class="flex items-center gap-2">
      <div>
        <h2 class="font-semibold">Transcription quality</h2>
        <p class="text-xs text-base-content/60">
          How accurate transcripts should be, traded against speed.
        </p>
      </div>
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
        on:click={loadSettings}
        disabled={loading || !operatorClient}
        aria-label="Reload settings"
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
    <div class="flex items-center justify-center p-6 text-sm text-base-content/60">Loading settings…</div>
  {:else if !settings}
    <div class="flex items-center justify-center p-6 text-sm text-base-content/60">No settings available.</div>
  {:else}
    <div class="grid gap-4 p-4">
      <div
        class="grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.6fr)_minmax(0,1.15fr)]"
      >
        <section class="grid content-start gap-2 rounded-box border border-base-300 bg-base-200 p-3">
          <div class="flex items-center gap-2">
            <Cpu size={16} aria-hidden="true" />
            <h3 class="text-sm font-semibold">Detected hardware</h3>
          </div>
          <dl class="grid gap-3 grid-cols-1">
            <div class="flex items-center justify-between gap-2 py-1">
              <dt class="text-sm text-base-content/60">GPU</dt>
              <dd class="text-sm">
                {#if settings.detected_gpu}
                  <span class="badge badge-success badge-outline badge-sm">Yes</span>
                {:else}
                  <span class="badge badge-outline badge-md border-base-content/20 text-base-content">No</span>
                {/if}
              </dd>
            </div>
            <div class="flex items-center justify-between gap-2 py-1">
              <dt class="text-sm text-base-content/60">CPU cores</dt>
              <dd class="text-sm">
                <span class="badge badge-outline badge-md border-base-content/20 text-base-content"
                  >{settings.cores}</span
                >
              </dd>
            </div>
            <div class="flex items-center justify-between gap-2 py-1">
              <dt class="text-sm text-base-content/60">Source</dt>
              <dd class="text-sm">
                <span class="badge badge-outline badge-md border-base-content/20 text-base-content"
                  >{sourceLabel(settings.source)}</span
                >
              </dd>
            </div>
          </dl>
        </section>

        <section class="grid content-start gap-2 rounded-box border border-base-300 bg-base-200 p-3">
          <div class="flex items-center gap-2">
            <CircleGauge size={16} aria-hidden="true" />
            <h3 id="stt-quality-heading" class="text-sm font-semibold">Quality</h3>
          </div>
          <div class="grid gap-3" role="radiogroup" aria-labelledby="stt-quality-heading">
            {#each QUALITY_OPTIONS as option}
              <label
                class="flex cursor-pointer items-center gap-2.5 rounded-box border px-2 py-1 transition {quality ===
                option.value
                  ? 'border-primary bg-primary/25 ring-1 ring-inset ring-primary'
                  : 'border-base-300 bg-base-100 hover:border-primary/50'}"
              >
                <input
                  type="radio"
                  name="stt-quality"
                  class="radio radio-primary radio-xs shrink-0"
                  value={option.value}
                  bind:group={quality}
                />
                <span class="min-w-0 flex-1 truncate">
                  <span class="text-sm font-medium">{option.label}</span>
                  <span class="ml-2 text-xs text-base-content/60">{option.description}</span>
                </span>
              </label>
            {/each}
          </div>
        </section>

        <section
          class="grid content-start gap-2 rounded-box border border-base-300 bg-base-200 p-3"
        >
          <div class="flex items-center gap-2">
            <SettingsIcon size={16} aria-hidden="true" />
            <h3 class="text-sm font-semibold">Device model and overrides</h3>
          </div>
          <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-1">
            <label class="flex w-full flex-col gap-1">
              <span class="text-xs font-medium text-base-content/70">Device override</span>
              <select bind:value={deviceOverride} class="select select-sm w-full border-base-300 shadow-none">
                <option value="">Auto</option>
                <option value="cuda">CUDA</option>
              </select>
            </label>
            <label class="flex w-full flex-col gap-1">
              <span class="text-xs font-medium text-base-content/70">Model override</span>
              <input
                bind:value={modelOverride}
                type="text"
                class="input input-sm w-full border-base-300 shadow-none"
                placeholder="(use default for selected quality)"
              />
            </label>
          </div>
        </section>
      </div>

      <section class="grid content-start gap-2 rounded-box border border-base-300 bg-base-200 p-3">
        <div>
          <h3 class="text-sm font-semibold">Participant and project vocabulary</h3>
          <p class="text-xs text-base-content/60">
            Preferred spellings for names and terms used during readable transcript cleanup.
          </p>
        </div>
        <label class="flex w-full flex-col gap-1">
          <span class="text-xs font-medium text-base-content/70">One term per line</span>
          <textarea
            bind:value={transcriptionTermsText}
            class="textarea min-h-28 w-full border-base-300 shadow-none"
            maxlength={10_100}
            placeholder={'Gocassini\nNextcloud Talk\nProject Cassini'}
          ></textarea>
          <span class="text-xs text-base-content/60">
            Up to 100 terms and 100 characters per term. Participant display names are supplied
            automatically. These spellings guide the optional readable-cleanup model and do not
            alter the raw speech-recognition transcript.
          </span>
        </label>
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
