<script lang="ts">
  import { createEventDispatcher, onMount } from "svelte";
  import type { OperatorClient } from "./operator/client";
  import type { RetentionSettings } from "./operator/retention";
  import { leavePrompt, unsavedChanges } from "./operator/unsaved";
  import DirectShareAccessPanel from "./DirectShareAccessPanel.svelte";
  import RetentionPanel from "./RetentionPanel.svelte";

  export let operatorClient: OperatorClient;
  export let initialSettings: RetentionSettings;
  const dispatch = createEventDispatcher<{ done: void }>();
  let saving = false;
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
  onMount(() => dialog.focus());
</script>

<svelte:window on:beforeunload={beforeUnload} on:keydown|capture={keydown} />

<div class="setup-backdrop">
  <div class="setup-dialog" role="dialog" aria-modal="true" aria-labelledby="retention-setup-title" tabindex="-1" bind:this={dialog}>
    <header>
      <p class="text-sm text-base-content/70">Cassini setup</p>
      <h1 id="retention-setup-title" class="text-2xl font-semibold">Choose what Cassini keeps</h1>
      <p>Review recording access and retention in one place. You can change retention later in Operator → Storage.</p>
    </header>
    <RetentionPanel {operatorClient} {initialSettings} formId="retention-setup-form" review bind:busy={saving} disabled={accountBusy} on:saved={() => dispatch("done")}>
      <DirectShareAccessPanel slot="before" {operatorClient} bind:busy={accountBusy} disabled={saving} />
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
