<script lang="ts">
  import { onMount } from "svelte";
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
      error = cause instanceof Error ? cause.message : "Could not check Nextcloud recordings setup.";
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
      error = cause instanceof Error ? cause.message : "Could not create the recordings account.";
    } finally {
      busy = false;
    }
  }

  onMount(() => { void load(); });
</script>

<section class="op-tint access">
  <h2 class="set-row-name op-card-title">Who can see recordings</h2>
  <div class="access-body grid gap-3 text-sm">
    <!-- Same points as the rooms rail's "Shared with participants" note
         (cassini-viewer RoomsRail.svelte); change both together. -->
    <p>Nextcloud Files controls who can read each recording.</p>
    <ul class="access-points">
      <li>Each recording is shared with all of the Talk room's participants, whether they were present on the call or not.</li>
      <li>Guests without a Nextcloud account can't open it.</li>
      <li>Cassini never creates a public link. In public rooms, people can reshare the file if this Nextcloud allows it.</li>
    </ul>
    {#if loading}
      <p role="status">Checking recordings setup…</p>
    {:else if error || (status && (!status.service_account.exists || !status.ok))}
      {#if error}
        <p class="text-error" role="alert">{error}</p>
      {:else if status && !status.service_account.exists}
        <p role="status">Cassini needs its recordings account before it can publish.</p>
      {:else if status}
        <p role="status">{status.detail || "Recordings setup needs attention."}</p>
      {/if}
      <div class="access-actions">
        {#if status && !status.service_account.exists && operatorClient && isSetupAvailable() && accountSteps(status).length > 0}
          <button class="btn btn-sm btn-primary" type="button" on:click={createAccount} disabled={busy || disabled}>
            {busy ? "Creating…" : "Create recordings account"}
          </button>
        {/if}
        <button class="btn btn-sm" type="button" on:click={() => load(true)} disabled={busy || disabled || !operatorClient}>Check again</button>
      </div>
    {:else if fixed}
      <p role="status">Recordings account created. Sharing is ready.</p>
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
