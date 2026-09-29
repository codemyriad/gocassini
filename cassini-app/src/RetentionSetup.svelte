<script lang="ts">
  import { createEventDispatcher, onMount } from "svelte";
  import type { OperatorClient } from "./operator/client";
  import type { RetentionSettings } from "./operator/retention";
  import { guardLeave, leavePrompt, unsavedChanges } from "./operator/unsaved";
  import DirectShareAccessPanel from "./DirectShareAccessPanel.svelte";
  import RetentionPanel from "./RetentionPanel.svelte";

  export let operatorClient: OperatorClient;
  export let initialSettings: RetentionSettings;
  const dispatch = createEventDispatcher<{ done: void }>();
  let saving = false;
  let accountBusy = false;
  let dialog: HTMLDivElement;

  function later() {
    if (!saving && !accountBusy) guardLeave(() => dispatch("done"));
  }
  function keydown(event: KeyboardEvent) {
    // Account creation can open Nextcloud's own password-confirmation dialog.
    // Its controls live outside Cassini's shadow root and must keep focus.
    if (accountBusy) return;
    if (event.key === "Escape") {
      event.preventDefault();
      // Keep an existing discard confirmation in place until it is answered.
      if (!$leavePrompt) later();
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

<svelte:window on:beforeunload={beforeUnload} on:keydown={keydown} />

<div class="setup-backdrop">
  <div class="setup-dialog" role="dialog" aria-modal="true" aria-labelledby="retention-setup-title" tabindex="-1" bind:this={dialog}>
    <header>
      <p class="text-sm text-base-content/70">Cassini setup</p>
      <h1 id="retention-setup-title" class="text-2xl font-semibold">Choose what Cassini keeps</h1>
      <p>Review recording access and retention in one place. You can change retention later in Operator → Storage.</p>
    </header>
    <DirectShareAccessPanel {operatorClient} bind:busy={accountBusy} disabled={saving} />
    <RetentionPanel {operatorClient} {initialSettings} review bind:busy={saving} disabled={accountBusy} on:saved={() => dispatch("done")} />
    <footer>
      <button class="btn btn-ghost" type="button" disabled={saving || accountBusy} on:click={later}>Set up later</button>
      <p class="text-sm text-base-content/70">Later keeps the saved policies and asks again next time you open Cassini.</p>
    </footer>
  </div>
</div>

<style>
  .setup-backdrop {
    height: 100%;
    overflow-y: auto;
    padding: clamp(12px, 3vw, 32px);
    background: var(--color-base-200);
  }
  .setup-dialog {
    display: grid;
    gap: 24px;
    width: 100%;
    max-width: 860px;
    min-width: 0;
    margin: 0 auto;
    padding: clamp(16px, 3vw, 32px);
    color: var(--color-base-content);
    background: var(--color-base-100);
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-box, 12px);
  }
  header, footer { display: grid; gap: 8px; }
  footer { border-top: 1px solid var(--color-base-300); padding-top: 16px; }
  footer button { justify-self: start; }
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
