<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { leavePrompt, cancelLeave, confirmLeave } from "./operator/unsaved";

  export let formId: string;
  export let review = false;
  export let reviewed = false;
  export let disabled = false;
  export let canSave = false;
  export let error = "";
  export let notice = "";
  const dispatch = createEventDispatcher<{ reload: void }>();
</script>

<div class="retention-actions">
  {#if error}<p class="alert alert-error" role="alert">{error}</p>{/if}
  {#if notice}<p role="status">{notice}</p>{/if}
  {#if review}
    <p class="text-sm text-base-content/70" role="status">
      {reviewed ? "You can change these settings later in Operator → Storage." : "Scroll to the bottom to review all settings before continuing."}
    </p>
  {/if}
  <div class="buttons">
    <button class="btn btn-ghost" type="button" {disabled} on:click={() => dispatch("reload")}>Reload saved settings</button>
    <!-- Native form association preserves validation when actions sit outside the scroller. -->
    <button class="btn btn-primary" type="submit" form={formId} disabled={disabled || !canSave}>{review ? "Save and continue" : "Save retention settings"}</button>
  </div>
  {#if $leavePrompt}
    <div class="alert" role="alertdialog" tabindex="-1" aria-label="Leave without saving?">
      <p>You have unsaved changes. Leave without saving?</p>
      <button class="btn btn-sm" on:click={cancelLeave}>Stay</button>
      <button class="btn btn-sm" on:click={confirmLeave}>Leave</button>
    </div>
  {/if}
</div>

<style>
  .retention-actions { display: grid; gap: 12px; }
  .buttons { display: flex; flex-wrap: wrap; justify-content: space-between; gap: 8px; }
  @media (max-width: 480px) {
    .buttons > button { flex: 1 1 auto; }
  }
</style>
