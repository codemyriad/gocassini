<script lang="ts">
  import { createEventDispatcher, onMount } from "svelte";
  import type { OperatorClient } from "./operator/client";
  import type { RetentionSettings } from "./operator/retention";
  import { leavePrompt, unsavedChanges } from "./operator/unsaved";
  import DirectShareAccessPanel from "./DirectShareAccessPanel.svelte";
  import RetentionPanel from "./RetentionPanel.svelte";
  import CaptureVideoField from "./CaptureVideoField.svelte";
  import type { Settings } from "./operator/types";

  export let operatorClient: OperatorClient;
  export let initialSettings: RetentionSettings;
  const dispatch = createEventDispatcher<{ done: void }>();
  let saving = false;
  let captureSettings: Settings | null = null;
  let retainVideo = false;
  let savedRetainVideo = false;
  let captureLoading = false;
  let captureError = "";
  async function loadCapturePolicy() {
    captureLoading = true; captureError = "";
    try {
      captureSettings = await operatorClient.getSettings();
      retainVideo = captureSettings.retain_video === true;
      savedRetainVideo = retainVideo;
    } catch (e) { captureError = e instanceof Error ? e.message : String(e); }
    finally { captureLoading = false; }
  }
  async function saveCapturePolicy() {
    // Retention revision is setup completion. Persist capture consent first.
    captureSettings = await operatorClient.putSettings({ retain_video: retainVideo });
    savedRetainVideo = captureSettings.retain_video === true;
    if (savedRetainVideo !== retainVideo) throw new Error("Capture video choice was not saved. Please retry.");
  }
  let accountBusy = false;
  let dialog: HTMLDivElement;

  function keydown(event: KeyboardEvent) {
    // Account creation can open Nextcloud's own password-confirmation dialog.
    // Its controls live outside Cassini's shadow root and must keep focus.
    if (accountBusy) return;
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopImmediatePropagation();
    }
    if (event.key !== "Tab") return;
    const region = $leavePrompt ? dialog.querySelector('[role="alertdialog"]') ?? dialog : dialog;
    const controls = [...region.querySelectorAll<HTMLElement>(
      'button:not(:disabled), input:not(:disabled), select:not(:disabled), a[href], [tabindex="0"]',
    )].filter(el => !el.matches(":disabled") && el.getClientRects().length > 0);
    const first = controls[0], last = controls[controls.length - 1];
    const active = (dialog.getRootNode() as Document | ShadowRoot).activeElement;
    if (event.shiftKey && (active === first || !active || !region.contains(active) || active === dialog)) {
      event.preventDefault(); last?.focus();
    } else if (!event.shiftKey && (active === last || !active || !region.contains(active) || active === dialog)) {
      event.preventDefault(); first?.focus();
    }
  }
  function beforeUnload(event: BeforeUnloadEvent) {
    if ($unsavedChanges || saving || accountBusy) {
      event.preventDefault();
      event.returnValue = "";
    }
  }
  onMount(() => { dialog.focus(); void loadCapturePolicy(); });
</script>

<svelte:window on:beforeunload={beforeUnload} on:keydown|capture={keydown} />

<div class="setup-backdrop">
  <div class="setup-dialog" role="dialog" aria-modal="true" aria-labelledby="retention-setup-title" tabindex="-1" bind:this={dialog}>
    <header>
      <p class="text-sm text-base-content/70">Cassini setup</p>
      <h1 id="retention-setup-title" class="text-2xl font-semibold">Choose what Cassini keeps</h1>
      <p>Review recording access, video capture and storage retention in one place. You can change video capture later in Settings and retention in Operator → Storage.</p>
    </header>
    <RetentionPanel {operatorClient} {initialSettings} formId="retention-setup-form" review bind:busy={saving} disabled={accountBusy || captureLoading || captureSettings === null || !!captureError} beforeSave={saveCapturePolicy} beforeReload={loadCapturePolicy} extraDirty={retainVideo !== savedRetainVideo} on:saved={() => dispatch("done")}>
      <div slot="before" class="grid gap-4">
        <DirectShareAccessPanel {operatorClient} bind:busy={accountBusy} disabled={saving} />
        {#if captureLoading}<p role="status">Loading capture policy…</p>
        {:else if captureError}<p role="alert">{captureError}</p><button class="btn btn-sm" type="button" on:click={loadCapturePolicy}>Retry capture policy</button>
        {:else if captureSettings}<CaptureVideoField bind:retainVideo disabled={saving || accountBusy} />{/if}
      </div>
    </RetentionPanel>
  </div>
</div>

<style>
  .setup-backdrop {
    position: absolute;
    inset: 0;
    display: grid;
    place-items: center;
    padding: clamp(12px, 3vw, 32px);
    background: color-mix(in srgb, var(--color-base-content) 35%, transparent);
    backdrop-filter: blur(3px);
  }
  .setup-dialog {
    display: flex;
    flex-direction: column;
    width: 100%;
    max-width: 860px;
    height: 100%;
    max-height: 960px;
    min-height: 0;
    min-width: 0;
    overflow: hidden;
    color: var(--color-base-content);
    background: var(--color-base-100);
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-box, 12px);
    box-shadow: 0 24px 80px #0004;
  }
  header { display: grid; gap: 8px; flex: none; padding: 24px; border-bottom: 1px solid var(--color-base-300); }
  @media (max-width: 600px) {
    header { padding: 16px; gap: 4px; }
    header h1 { font-size: 1.25rem; }
    header p { font-size: 0.875rem; }
  }
  :global(.setup-dialog .op-tint) {
    background: var(--color-base-200);
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-box, 8px);
  }
  :global(.setup-dialog .access) { padding: 16px; }
  :global(.setup-dialog .access-head) { display: flex; gap: 12px; justify-content: space-between; margin-bottom: 12px; }
  :global(.setup-dialog .access-head h2) { font-weight: 600; }
  :global(.setup-dialog .access-head button) { flex: none; padding: 8px; align-self: start; }
  :global(.setup-dialog fieldset) { min-width: 0; }
</style>
