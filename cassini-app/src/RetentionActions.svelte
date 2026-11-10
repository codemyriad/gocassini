<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { LL } from "cassini-viewer/i18n";
  import { leavePrompt, cancelLeave, confirmLeave } from "./operator/unsaved";

  export let formId: string;
  export let disabled = false;
  export let canSave = false;
  export let dirty = false;
  export let error = "";
  export let notice = "";
  const dispatch = createEventDispatcher<{ reload: void }>();
</script>

<div class="retention-actions">
  {#if error}<p class="alert alert-error" role="alert">{error}</p>{/if}
  {#if notice}<p role="status">{notice}</p>{/if}
  <div class="buttons">
    <p class="text-sm opacity-80">{$LL.app.settings.retention_actions.takes_effect()}</p>
    <div class="button-row">
      {#if dirty}<button class="btn btn-ghost" type="button" {disabled} on:click={() => dispatch("reload")}>{$LL.app.settings.retention_actions.discard()}</button>{/if}
      <!-- Native form association preserves validation when actions sit outside the scroller. -->
      <button class="btn btn-primary" type="submit" form={formId} disabled={disabled || !canSave}>{$LL.app.settings.retention_actions.save()}</button>
    </div>
  </div>
  {#if $leavePrompt}
    <div class="alert" role="alertdialog" tabindex="-1" aria-label={$LL.app.settings.retention_actions.leave_title()}>
      <p>{$LL.app.settings.retention_actions.leave_prompt()}</p>
      <button class="btn btn-sm" on:click={cancelLeave}>{$LL.app.settings.retention_actions.stay()}</button>
      <button class="btn btn-sm" on:click={confirmLeave}>{$LL.app.settings.retention_actions.leave()}</button>
    </div>
  {/if}
</div>

<style>
  .retention-actions { display: grid; gap: 12px; }
  .buttons { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 8px; }
  .buttons > p { flex: 1 1 260px; min-width: 0; }
  .button-row { display: flex; gap: 8px; margin-left: auto; }
  @media (max-width: 480px) {
    .button-row { flex: 1 1 100%; }
    .button-row > button { flex: 1 1 auto; }
  }
</style>
