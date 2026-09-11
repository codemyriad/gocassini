<script lang="ts">
  import { onMount } from "svelte";
  import { TriangleAlert } from "@lucide/svelte";
  import { loadConfig } from "./operator/config";
  import { OperatorClient } from "./operator/client";
  import { wizardNeeded } from "./operator/storageWizard";
  import type { StorageStatus } from "./operator/types";
  import SetupWizard from "./SetupWizard.svelte";
  import RecordingAccessPanel from "./RecordingAccessPanel.svelte";

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
  let configError = "";
  let status: StorageStatus | null = null;
  let loadError = "";
  let loading = true;

  onMount(() => {
    try {
      operatorClient = new OperatorClient(loadConfig().operatorBasePath);
    } catch (error) {
      configError = error instanceof Error ? error.message : String(error);
      loading = false;
      return;
    }
    void decide();
  });

  // decide reads one thing — whether a person has chosen — and hands over. It
  // deliberately does NOT hold the state either half then works from: both
  // fetch their own and re-fetch after every action, and a shared copy would be
  // a third place for it to go stale.
  async function decide(): Promise<void> {
    if (!operatorClient) {
      return;
    }
    loading = true;
    loadError = "";
    try {
      status = await operatorClient.getStorage();
    } catch (error) {
      loadError = error instanceof Error ? error.message : String(error);
    } finally {
      loading = false;
    }
  }

  $: showWizard = wizardNeeded(status);
</script>

<div class="flex min-h-full flex-col bg-base-200 text-base-content">
  <div class="mx-auto flex min-h-full w-full max-w-5xl flex-col gap-4 px-4 pt-4 pb-10">
    {#if configError}
      <section class="alert alert-error">
        <TriangleAlert size={16} aria-hidden="true" />
        <span>{configError}</span>
      </section>
    {:else if loadError}
      <section class="alert alert-error">
        <TriangleAlert size={16} aria-hidden="true" />
        <span>{loadError}</span>
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
        <RecordingAccessPanel {operatorClient} />
      {/key}
    {/if}
  </div>
</div>
