<script lang="ts">
  import { onMount } from "svelte";
  import { RefreshCw } from "@lucide/svelte";
  import { loadConfig } from "./operator/config";
  import { OperatorClient } from "./operator/client";
  import { wizardNeeded } from "./operator/storageWizard";
  import { buildSetupLoadError, type SetupLoadError } from "./operator/setupLoadError";
  import type { StorageStatus } from "./operator/types";
  import SetupWizard from "./SetupWizard.svelte";
  import StoragePanel from "./StoragePanel.svelte";

  // The Setup surface (D-616 first pass): instance-level configuration, as
  // opposed to the operator surface's runs and the browse surface's meetings.
  //
  // It shows one of two things, and which one is the whole of D-708:
  //
  //	nobody has chosen   the WIZARD. Cassini used to answer this itself, by
  //	                    falling back to the model in which everyone can read
  //	                    everything and writing that down permanently.
  //	somebody has        the PANEL. A settled instance, with the switch, the
  //	                    repair and the service account's password.
  //
  // The test is `mode_confirmed`, not "is a mode recorded": an install carrying
  // a mode a previous build chose on its own has one and has never been asked.
  //
  // It exists as its own surface because the thing it holds moves every
  // recording in the instance and does not belong beside a run list — and
  // because the next few settings of this kind (who may record, retention, the
  // disclosure notice) are the same sort of decision, not more run controls.

  let operatorClient: OperatorClient | null = null;
  let status: StorageStatus | null = null;
  let loadError: SetupLoadError | null = null;
  let loading = true;

  onMount(() => {
    void decide();
  });

  // decide reads one thing — whether a person has chosen — and hands over. It
  // deliberately does NOT hold the state either half then works from: both
  // fetch their own and re-fetch after every action, and a shared copy would be
  // a third place for it to go stale.
  async function decide(): Promise<void> {
    loading = true;
    loadError = null;
    try {
      operatorClient ??= new OperatorClient(loadConfig().operatorBasePath);
      status = await operatorClient.getStorage();
    } catch (error) {
      loadError = buildSetupLoadError(error);
    } finally {
      loading = false;
    }
  }

  $: showWizard = wizardNeeded(status);
</script>

<div class="flex min-h-full flex-col bg-base-200 text-base-content">
  <div class="mx-auto flex min-h-full w-full max-w-5xl flex-col gap-4 px-4 pt-4 pb-10">
    {#if loadError}
      <section class="rounded-box border border-base-300 bg-base-100 p-5 shadow-sm" aria-labelledby="setup-load-error-title">
        <div role="alert">
          <h2 id="setup-load-error-title" class="font-semibold">{loadError.title}</h2>
          <p class="mt-2 text-sm text-base-content/70">{loadError.summary}</p>
        </div>
        <button class="btn btn-primary btn-sm mt-4" type="button" on:click={decide}>
          <RefreshCw size={16} aria-hidden="true" />
          Try again
        </button>
        <details class="mt-4 text-sm text-base-content/60">
          <summary class="cursor-pointer">Technical details</summary>
          <pre class="mt-2 whitespace-pre-wrap break-words rounded-box bg-base-200 p-3 text-xs">{loadError.detail}</pre>
        </details>
      </section>
    {:else if loading}
      <section class="flex items-center justify-center gap-2 p-6 text-sm text-base-content/60">
        <span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
        Loading setup…
      </section>
    {:else if operatorClient && showWizard}
      <!-- Re-keyed on the verdict so that confirming a mode inside the wizard
           tears it down and mounts the settled panel, rather than leaving the
           wizard on screen asking a question that has been answered. -->
      {#key showWizard}
        <SetupWizard {operatorClient} on:decided={decide} />
      {/key}
    {:else if operatorClient}
      {#key operatorClient}
        <StoragePanel {operatorClient} />
      {/key}
    {/if}
  </div>
</div>
