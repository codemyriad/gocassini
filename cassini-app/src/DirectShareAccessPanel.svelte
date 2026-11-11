<script lang="ts">
  import { onMount } from "svelte";
  import { LL } from "cassini-viewer/i18n";
  import type { OperatorClient } from "./operator/client";
  import { accountSteps, isSetupAvailable, runSetupPlan } from "./operator/ncSetup";
  import { notifySetupChanged } from "./operator/setupSignal";
  import type { StorageStatus } from "./operator/types";

  export let operatorClient: OperatorClient | null = null;

  let status: StorageStatus | null = null;
  let loading = true;
  export let busy = false;
  export let disabled = false;
  let error = "";
  // Confirms a fix made from this panel; a healthy setup otherwise says nothing.
  let fixed = false;

  async function load(recheck = false): Promise<void> {
    if (!operatorClient) return;
    loading = true;
    error = "";
    try {
      status = recheck ? await operatorClient.recheckStorage() : await operatorClient.getStorage();
    } catch (cause) {
      error = cause instanceof Error ? cause.message : $LL.app.settings.direct_share.check_failed();
    } finally {
      loading = false;
    }
  }

  async function createAccount(): Promise<void> {
    if (!operatorClient || !status || busy || disabled) return;
    busy = true;
    error = "";
    try {
      await runSetupPlan(accountSteps(status));
      status = await operatorClient.recheckStorage();
      fixed = status.ok;
      notifySetupChanged();
    } catch (cause) {
      error = cause instanceof Error ? cause.message : $LL.app.settings.direct_share.create_failed();
    } finally {
      busy = false;
    }
  }

  onMount(() => { void load(); });
</script>

<section class="op-tint access">
  <h2 class="set-row-name op-card-title">{$LL.app.settings.direct_share.heading()}</h2>
  <div class="access-body grid gap-3 text-sm">
    <!-- Same points as the rooms rail's "Shared with participants" note
         (cassini-viewer RoomsRail.svelte); change both together. -->
    <p>{$LL.common.audience.intro()}</p>
    <ul class="access-points">
      <li>{$LL.common.audience.point_participants()}</li>
      <li>{$LL.common.audience.point_guests()}</li>
      <li>{$LL.common.audience.point_public_links()}</li>
    </ul>
    {#if loading}
      <p role="status">{$LL.app.settings.direct_share.checking()}</p>
    {:else if error || (status && (!status.service_account.exists || !status.ok))}
      {#if error}
        <p class="text-error" role="alert">{error}</p>
      {:else if status && !status.service_account.exists}
        <p role="status">{$LL.app.settings.direct_share.needs_account()}</p>
      {:else if status}
        <p role="status">{status.detail || $LL.app.settings.direct_share.needs_attention()}</p>
      {/if}
      <div class="access-actions">
        {#if status && !status.service_account.exists && operatorClient && isSetupAvailable() && accountSteps(status).length > 0}
          <button class="btn btn-sm btn-primary" type="button" on:click={createAccount} disabled={busy || disabled}>
            {busy ? $LL.app.settings.direct_share.creating() : $LL.app.settings.direct_share.create_account()}
          </button>
        {/if}
        <button class="btn btn-sm" type="button" on:click={() => load(true)} disabled={busy || disabled || !operatorClient}>{$LL.app.settings.direct_share.check_again()}</button>
      </div>
    {:else if fixed}
      <p role="status">{$LL.app.settings.direct_share.account_created()}</p>
    {/if}
  </div>
</section>

<style>
  /* Same inset as the other settings cards (.pipe-step, .hw-card). */
  .access {
    display: grid;
    gap: 12px;
    padding: 14px 16px;
  }
  .access-points {
    display: grid;
    gap: 4px;
    margin: 0;
    padding-left: 1.1rem;
    list-style: disc;
  }
  .access-actions {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }
</style>
