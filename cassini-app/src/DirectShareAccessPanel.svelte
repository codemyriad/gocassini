<script lang="ts">
  import { onMount } from "svelte";
  import { RefreshCw } from "@lucide/svelte";
  import type { OperatorClient } from "./operator/client";
  import { accountSteps, isSetupAvailable, runSetupPlan } from "./operator/ncSetup";
  import { notifySetupChanged } from "./operator/setupSignal";
  import type { StorageStatus } from "./operator/types";

  export let operatorClient: OperatorClient | null = null;

  let status: StorageStatus | null = null;
  let loading = true;
  let busy = false;
  let error = "";

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
    if (!operatorClient || !status || busy) return;
    busy = true;
    error = "";
    try {
      await runSetupPlan(accountSteps(status));
      status = await operatorClient.recheckStorage();
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
  <header class="access-head">
    <div class="set-row-main">
      <h2 class="set-row-name op-card-title">Who can see recordings</h2>
      <p class="set-row-sub">Cassini uses Nextcloud file shares for every meeting.</p>
    </div>
    <button class="icon-btn" type="button" on:click={() => load(true)} disabled={loading || busy || !operatorClient} aria-label="Check this Nextcloud again">
      <RefreshCw size={15} aria-hidden="true" />
    </button>
  </header>
  <div class="access-body grid gap-3 text-sm">
    <p>People who belonged to the Talk room while it was recorded can open the recording. People invited but absent are included. Guests without a Nextcloud account are not.</p>
    <p>For public meetings, participants may share the file onward when this Nextcloud allows resharing. Cassini does not create a public link.</p>
    {#if loading}
      <p role="status">Checking recordings setup…</p>
    {:else if error}
      <p class="text-error" role="alert">{error}</p>
    {:else if status && !status.service_account.exists}
      <p role="status">Cassini needs its recordings account before it can publish.</p>
      {#if operatorClient && isSetupAvailable() && accountSteps(status).length > 0}
        <button class="btn btn-sm btn-primary" type="button" on:click={createAccount} disabled={busy}>
          {busy ? "Creating…" : "Create recordings account"}
        </button>
      {/if}
    {:else if status && !status.ok}
      <p role="status">{status.detail || "Recordings setup needs attention."}</p>
    {:else if status}
      <p role="status">Nextcloud sharing is ready.</p>
    {/if}
  </div>
</section>
